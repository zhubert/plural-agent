package claude

import (
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"

	"github.com/zhubert/erg/internal/mcp"
)

// ToolResultInfo contains details about the result of a tool execution.
// This is extracted from the tool_use_result field in user messages.
type ToolResultInfo struct {
	// For Read tool results
	FilePath   string // Path to the file that was read
	NumLines   int    // Number of lines returned
	StartLine  int    // Starting line number (1-indexed)
	TotalLines int    // Total lines in the file

	// For Edit tool results
	Edited bool // Whether an edit was applied

	// For Glob tool results
	NumFiles int // Number of files matched

	// For Bash tool results
	ExitCode *int // Exit code (nil if not available)
}

// Summary returns a brief human-readable summary of the tool result.
func (t *ToolResultInfo) Summary() string {
	if t == nil {
		return ""
	}

	// Read tool: show line info
	if t.FilePath != "" && t.TotalLines > 0 {
		if t.NumLines < t.TotalLines {
			return fmt.Sprintf("lines %d-%d of %d", t.StartLine, t.StartLine+t.NumLines-1, t.TotalLines)
		}
		return fmt.Sprintf("%d lines", t.TotalLines)
	}

	// Edit tool: show edited status
	if t.Edited {
		return "applied"
	}

	// Glob tool: show file count
	if t.NumFiles > 0 {
		if t.NumFiles == 1 {
			return "1 file"
		}
		return fmt.Sprintf("%d files", t.NumFiles)
	}

	// Bash tool: show exit code
	if t.ExitCode != nil {
		if *t.ExitCode == 0 {
			return "success"
		}
		return fmt.Sprintf("exit %d", *t.ExitCode)
	}

	return ""
}

// handleProcessLine processes a line of output from the Claude process.
func (r *Runner) handleProcessLine(line string) {
	// Snapshot streamLogFile under the lock to avoid racing with Stop(),
	// which sets r.streamLogFile to nil after closing the file.
	r.mu.RLock()
	logFile := r.streamLogFile
	r.mu.RUnlock()

	// Write raw message to dedicated stream log file (pretty-printed JSON).
	// Redact known secrets before writing so they never appear in log files.
	if logFile != nil {
		safeLine := r.redactor.Redact(line)
		var prettyJSON map[string]any
		if err := json.Unmarshal([]byte(safeLine), &prettyJSON); err == nil {
			if formatted, err := json.MarshalIndent(prettyJSON, "", "  "); err == nil {
				fmt.Fprintf(logFile, "%s\n", formatted)
			} else {
				fmt.Fprintf(logFile, "%s\n", safeLine)
			}
		} else {
			fmt.Fprintf(logFile, "%s\n", safeLine)
		}
	}

	// Mark session as started as soon as we receive the init message.
	// This is the earliest signal that Claude CLI has accepted the session ID.
	// Without this, interrupting before a result message leaves sessionStarted=false,
	// causing subsequent starts to use --session-id (which fails with "already in use")
	// instead of --resume.
	if !r.sessionStarted && strings.Contains(line, `"type":"system"`) && strings.Contains(line, `"subtype":"init"`) {
		r.mu.Lock()
		r.sessionStarted = true
		pm := r.processManager
		r.mu.Unlock()
		// Call MarkSessionStarted outside r.mu to avoid deadlock:
		// MarkSessionStarted -> OnContainerReady -> handleContainerReady acquires r.mu.RLock
		if pm != nil {
			pm.MarkSessionStarted()
		}
		r.log.Info("session marked as started on init message")
	}

	// Parse the JSON message
	// hasStreamEvents depends on whether we're using --include-partial-messages.
	// When enabled (default for TUI), text arrives via stream_event deltas and the full
	// assistant message text is skipped to avoid duplication.
	// When disabled (agent mode), complete assistant messages are processed.
	r.mu.RLock()
	hasStreamEvents := !r.disableStreamingChunks
	r.mu.RUnlock()
	chunks := parseStreamMessage(line, hasStreamEvents, r.log)

	// Get the current response channel (nil if already closed)
	r.mu.RLock()
	ch := r.responseChan.Channel
	if r.responseChan.Closed {
		ch = nil
	}
	r.mu.RUnlock()

	for _, chunk := range chunks {
		r.mu.Lock()
		switch chunk.Type {
		case ChunkTypeText:
			// Add extra newline after tool use for visual separation
			if r.streaming.LastWasToolUse && r.streaming.EndsWithNewline && !r.streaming.EndsWithDoubleNL {
				r.streaming.Response.WriteString("\n")
				r.streaming.EndsWithDoubleNL = true
			}
			r.streaming.Response.WriteString(chunk.Content)
			// Update newline tracking based on content
			if len(chunk.Content) > 0 {
				r.streaming.EndsWithNewline = chunk.Content[len(chunk.Content)-1] == '\n'
				r.streaming.EndsWithDoubleNL = len(chunk.Content) >= 2 && chunk.Content[len(chunk.Content)-2:] == "\n\n"
			}
			r.streaming.LastWasToolUse = false
		case ChunkTypeToolUse:
			// Format tool use line - add newline if needed
			if r.streaming.Response.Len() > 0 && !r.streaming.EndsWithNewline {
				r.streaming.Response.WriteString("\n")
			}
			r.streaming.Response.WriteString("● ")
			r.streaming.Response.WriteString(FormatToolIcon(chunk.ToolName))
			r.streaming.Response.WriteString("(")
			r.streaming.Response.WriteString(chunk.ToolName)
			if chunk.ToolInput != "" {
				r.streaming.Response.WriteString(": ")
				r.streaming.Response.WriteString(chunk.ToolInput)
			}
			r.streaming.Response.WriteString(")\n")
			r.streaming.EndsWithNewline = true
			r.streaming.EndsWithDoubleNL = false
			r.streaming.LastWasToolUse = true
		}

		if r.streaming.FirstChunk {
			r.log.Debug("first response chunk received", "elapsed", time.Since(r.streaming.StartTime))
			r.streaming.FirstChunk = false
		}
		r.mu.Unlock()

		// Send to response channel if available with timeout
		if ch != nil {
			if err := r.sendChunkWithTimeout(ch, chunk); err != nil {
				if err == errChannelFull {
					// Report error to user instead of silently dropping
					r.log.Error("response channel full, reporting error")
					r.sendChunkWithTimeout(ch, ResponseChunk{
						Type:    ChunkTypeText,
						Content: "\n[Error: Response buffer full - some output may be lost]\n",
					})
				}
				return
			}
		}
	}

	// Parse the message to handle token accumulation, subagent tracking, and result messages
	var msg streamMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &msg); err == nil {
		// Handle token accumulation from stream_event messages (with --include-partial-messages)
		// These provide real-time token count updates during streaming
		if msg.Type == "stream_event" && msg.Event != nil {
			r.handleStreamEventTokens(msg.Event, ch)
		}

		// Handle subagent status tracking
		// When parent_tool_use_id is non-empty and we have a model, we're in a subagent (e.g., Haiku via Task)
		if msg.Type == "assistant" || msg.Type == "user" {
			r.mu.Lock()
			isSubagent := msg.ParentToolUseID != ""
			subagentModel := ""
			if isSubagent && msg.Message.Model != "" {
				subagentModel = msg.Message.Model
			}

			// Check for state change
			previousModel := r.streaming.CurrentSubagentModel
			stateChanged := (previousModel == "" && subagentModel != "") || // Entering subagent
				(previousModel != "" && subagentModel == "") // Exiting subagent

			if stateChanged {
				r.streaming.CurrentSubagentModel = subagentModel
				r.mu.Unlock()

				// Emit subagent status chunk
				if ch != nil {
					r.sendChunkWithTimeout(ch, ResponseChunk{
						Type:          ChunkTypeSubagentStatus,
						SubagentModel: subagentModel, // Empty string means subagent ended
					})
				}
			} else {
				r.mu.Unlock()
			}
		}

		// Handle token accumulation for assistant messages
		// Claude CLI sends cumulative output_tokens within each API call, but resets on new API calls.
		// We track message IDs to detect new API calls and accumulate across them.
		if msg.Type == "assistant" && msg.Message.Usage != nil && msg.Message.Usage.OutputTokens > 0 {
			r.mu.Lock()
			messageID := msg.Message.ID

			// If this is a new message ID, we're starting a new API call
			// Add the final token count from the previous API call to the accumulator
			if messageID != "" && messageID != r.tokens.LastMessageID {
				if r.tokens.LastMessageID != "" {
					// Add the previous message's final token count to the accumulator
					r.tokens.AccumulatedOutput += r.tokens.LastMessageTokens
				}
				r.tokens.LastMessageID = messageID
				r.tokens.LastMessageTokens = 0
			}

			// Update the current message's token count (this is cumulative within the API call)
			r.tokens.LastMessageTokens = msg.Message.Usage.OutputTokens

			// Update cache efficiency stats (these are cumulative values)
			r.tokens.CacheCreation = msg.Message.Usage.CacheCreationInputTokens
			r.tokens.CacheRead = msg.Message.Usage.CacheReadInputTokens
			r.tokens.Input = msg.Message.Usage.InputTokens

			// The displayed total is accumulated tokens from completed API calls
			// plus the current API call's running token count
			currentTotal := r.tokens.CurrentTotal()

			// Capture token values while still holding the lock to avoid race condition
			cacheCreation := r.tokens.CacheCreation
			cacheRead := r.tokens.CacheRead
			inputTokens := r.tokens.Input

			r.mu.Unlock()

			// Emit stream stats with the accumulated token count and cache stats
			if ch != nil {
				r.sendChunkWithTimeout(ch, ResponseChunk{
					Type: ChunkTypeStreamStats,
					Stats: &StreamStats{
						OutputTokens:        currentTotal,
						TotalCostUSD:        0, // Not available during streaming, only on result
						CacheCreationTokens: cacheCreation,
						CacheReadTokens:     cacheRead,
						InputTokens:         inputTokens,
					},
				})
			}
		}

		if msg.Type == "result" {
			r.log.Debug("result message received",
				"subtype", msg.Subtype,
				"result", msg.Result,
				"error", msg.Error,
				"raw", strings.TrimSpace(line))

			r.mu.Lock()
			r.sessionStarted = true
			r.streaming.Complete = true // Mark that response finished - process exit after this is expected
			pm := r.processManager
			r.mu.Unlock()
			// Call MarkSessionStarted outside r.mu to avoid deadlock:
			// MarkSessionStarted -> OnContainerReady -> handleContainerReady acquires r.mu.RLock
			if pm != nil {
				pm.MarkSessionStarted()
				pm.ResetRestartAttempts()
			}
			r.mu.Lock()

			// Determine error message from Result, Error, or Errors fields
			errorText := msg.Result
			if errorText == "" {
				errorText = msg.Error
			}
			if errorText == "" && len(msg.Errors) > 0 {
				errorText = strings.Join(msg.Errors, "; ")
			}

			// If this is an error result, send the error message to the user
			// Check for various error subtypes that Claude CLI might use
			isError := msg.Subtype == "error_during_execution" ||
				msg.Subtype == "error" ||
				strings.Contains(msg.Subtype, "error")
			if isError && errorText != "" {
				if ch != nil && !r.responseChan.Closed {
					errorMsg := fmt.Sprintf("\n[Error: %s]\n", errorText)
					r.streaming.Response.WriteString(errorMsg)
					select {
					case ch <- ResponseChunk{Type: ChunkTypeText, Content: errorMsg}:
					default:
					}
				}
			}

			// Emit permission denials if any were recorded during the session
			if len(msg.PermissionDenials) > 0 {
				r.log.Debug("permission denials in result",
					"count", len(msg.PermissionDenials))
				if ch != nil && !r.responseChan.Closed {
					select {
					case ch <- ResponseChunk{
						Type:              ChunkTypePermissionDenials,
						PermissionDenials: msg.PermissionDenials,
					}:
					default:
					}
				}
			}

			r.messages = append(r.messages, Message{Role: "assistant", Content: r.redactor.Redact(r.streaming.Response.String())})

			// Emit stream stats chunk before Done if we have usage data
			// Prefer modelUsage (which includes sub-agent tokens) over the streaming accumulator
			if ch != nil && !r.responseChan.Closed {
				var totalOutputTokens int
				var byModel []ModelTokenCount

				// If modelUsage is present, sum up output tokens from all models
				// This includes both the parent model and any sub-agents (e.g., Haiku for Task)
				var modelUsageInputTokens, modelUsageCacheCreation, modelUsageCacheRead int
				if len(msg.ModelUsage) > 0 {
					for model, usage := range msg.ModelUsage {
						totalOutputTokens += usage.OutputTokens
						modelUsageInputTokens += usage.InputTokens
						modelUsageCacheCreation += usage.CacheCreationInputTokens
						modelUsageCacheRead += usage.CacheReadInputTokens
						byModel = append(byModel, ModelTokenCount{
							Model:        model,
							OutputTokens: usage.OutputTokens,
						})
					}
					r.log.Debug("using modelUsage for token count",
						"modelCount", len(msg.ModelUsage),
						"totalOutputTokens", totalOutputTokens,
						"totalInputTokens", modelUsageInputTokens,
						"totalCacheCreation", modelUsageCacheCreation,
						"totalCacheRead", modelUsageCacheRead)
				} else if msg.Usage != nil {
					// Fall back to streaming accumulator if no modelUsage
					totalOutputTokens = r.tokens.AccumulatedOutput + r.tokens.LastMessageTokens
					if msg.Usage.OutputTokens > r.tokens.LastMessageTokens {
						totalOutputTokens = r.tokens.AccumulatedOutput + msg.Usage.OutputTokens
					}
					r.log.Debug("using streaming accumulator for token count",
						"accumulated", r.tokens.AccumulatedOutput,
						"lastMessage", r.tokens.LastMessageTokens,
						"totalOutputTokens", totalOutputTokens)
				}

				if totalOutputTokens > 0 || msg.TotalCostUSD > 0 || msg.DurationMs > 0 {
					// Get input token stats. Prefer modelUsage totals when present (they include
					// sub-agent input tokens). Fall back to top-level usage otherwise.
					var cacheCreation, cacheRead, inputTokens int
					if modelUsageInputTokens > 0 || modelUsageCacheCreation > 0 || modelUsageCacheRead > 0 {
						// modelUsage had per-model input breakdowns — use those aggregated values
						inputTokens = modelUsageInputTokens
						cacheCreation = modelUsageCacheCreation
						cacheRead = modelUsageCacheRead
					} else if msg.Usage != nil {
						cacheCreation = msg.Usage.CacheCreationInputTokens
						cacheRead = msg.Usage.CacheReadInputTokens
						inputTokens = msg.Usage.InputTokens
					}

					stats := &StreamStats{
						OutputTokens:        totalOutputTokens,
						TotalCostUSD:        msg.TotalCostUSD,
						ByModel:             byModel,
						DurationMs:          msg.DurationMs,
						DurationAPIMs:       msg.DurationAPIMs,
						CacheCreationTokens: cacheCreation,
						CacheReadTokens:     cacheRead,
						InputTokens:         inputTokens,
					}
					r.log.Debug("emitting final stream stats",
						"outputTokens", stats.OutputTokens,
						"totalCostUSD", stats.TotalCostUSD,
						"modelCount", len(byModel),
						"durationMs", stats.DurationMs,
						"durationAPIMs", stats.DurationAPIMs,
						"cacheRead", cacheRead,
						"cacheCreation", cacheCreation)
					select {
					case ch <- ResponseChunk{Type: ChunkTypeStreamStats, Stats: stats}:
					default:
					}
				}
			}

			// Signal completion and close channel
			if ch != nil && !r.responseChan.Closed {
				select {
				case ch <- ResponseChunk{Done: true}:
				default:
				}
				r.closeResponseChannel()
			}
			r.streaming.Active = false

			// Reset for next message
			r.streaming.Reset()
			r.streaming.StartTime = time.Now()
			r.mu.Unlock()
		}
	}
}

// handleStreamEventTokens extracts and emits token counts from stream_event messages.
// These are sent when --include-partial-messages is enabled and provide real-time token updates.
func (r *Runner) handleStreamEventTokens(event *streamEvent, ch chan ResponseChunk) {
	if event == nil {
		return
	}

	var outputTokens int
	var messageID string

	switch event.Type {
	case "message_start":
		// Initial message with starting token count
		if event.Message != nil {
			messageID = event.Message.ID
			if event.Message.Usage != nil {
				outputTokens = event.Message.Usage.OutputTokens
			}
		}
	case "message_delta":
		// Updated token count during/after streaming
		if event.Usage != nil {
			outputTokens = event.Usage.OutputTokens
		}
	default:
		// Other event types don't have token updates
		return
	}

	if outputTokens == 0 {
		return
	}

	r.mu.Lock()

	// If this is a message_start with a new message ID, handle API call transitions
	if messageID != "" && messageID != r.tokens.LastMessageID {
		if r.tokens.LastMessageID != "" {
			// Add the previous message's final token count to the accumulator
			r.tokens.AccumulatedOutput += r.tokens.LastMessageTokens
		}
		r.tokens.LastMessageID = messageID
		r.tokens.LastMessageTokens = 0
	}

	// Update the current message's token count
	r.tokens.LastMessageTokens = outputTokens

	// Calculate total and check channel state under lock
	currentTotal := r.tokens.CurrentTotal()
	canSend := ch != nil && !r.responseChan.Closed

	// Release lock BEFORE sending to avoid holding it during the 10s timeout
	// in sendChunkWithTimeout, which would block all runner operations.
	r.mu.Unlock()

	if canSend {
		r.sendChunkWithTimeout(ch, ResponseChunk{
			Type: ChunkTypeStreamStats,
			Stats: &StreamStats{
				OutputTokens: currentTotal,
				TotalCostUSD: 0, // Not available during streaming
			},
		})
	}
}

// handleProcessExit is called when the process exits.
// Returns true if the process should be restarted.
func (r *Runner) handleProcessExit(err error, stderrContent string) bool {
	r.mu.Lock()
	stopped := r.stopped
	responseComplete := r.streaming.Complete

	// If stopped, don't do anything
	if stopped {
		r.mu.Unlock()
		return false
	}

	// If response was already complete (we got a result message), the process
	// exiting is expected behavior - don't restart
	if responseComplete {
		r.log.Debug("process exited after response complete, not restarting")
		r.mu.Unlock()
		return false
	}

	// Don't close the response channel here — return true to allow the
	// ProcessManager to attempt a restart.  The channel must stay open so
	// that handleRestartAttempt can send status messages and, if all
	// retries fail, handleFatalError can send the final error+done chunk.
	// Closing the channel prematurely causes the Bubble Tea listener to
	// interpret the close as a successful completion, which triggers the
	// autonomous pipeline (auto-PR creation) on what was actually a crash.
	//
	// Mark streaming as inactive so no code path assumes we're still streaming.
	// handleFatalError also sets this, but we set it here for robustness in case
	// a restart succeeds (which resets streaming state via a new SendContent call).
	r.streaming.Active = false
	r.mu.Unlock()

	// Return true to allow ProcessManager to handle restart logic
	return true
}

// handleRestartAttempt is called when a restart is being attempted.
func (r *Runner) handleRestartAttempt(attemptNum int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	ch := r.responseChan.Channel
	chClosed := r.responseChan.Closed

	if ch != nil && !chClosed {
		// Non-blocking send under lock
		select {
		case ch <- ResponseChunk{
			Type:    ChunkTypeText,
			Content: fmt.Sprintf("\n[Process crashed, attempting restart %d/%d...]\n", attemptNum, MaxProcessRestartAttempts),
		}:
			// Success
		default:
			// Channel full, ignore
		}
	}
}

// handleRestartFailed is called when restart fails.
func (r *Runner) handleRestartFailed(err error) {
	r.log.Error("restart failed", "error", err)
}

// handleFatalError is called when max restarts exceeded or unrecoverable error.
func (r *Runner) handleFatalError(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	ch := r.responseChan.Channel
	chClosed := r.responseChan.Closed

	if ch != nil && !chClosed {
		// Non-blocking send under lock
		select {
		case ch <- ResponseChunk{Error: err, Done: true}:
			// Success
		default:
			// Channel full, ignore
		}
		r.closeResponseChannel()
	}
	r.streaming.Active = false
}

// handleContainerReady is called when a containerized session is ready (init message received).
func (r *Runner) handleContainerReady() {
	r.mu.RLock()
	callback := r.onContainerReady
	r.mu.RUnlock()

	if callback != nil {
		callback()
	}
}

// connectToContainerMCP discovers the host-mapped port for the container's MCP
// listener and dials into it, passing the connection to the socket server.
// This runs as a goroutine after the container process starts.
//
// Retry logic: Docker's port forwarding accepts TCP connections even before the
// MCP subprocess starts listening inside the container, which causes an immediate
// EOF. The outer loop retries the entire connect+handle cycle when the connection
// drops within a few seconds, indicating the MCP subprocess wasn't ready yet.
func (r *Runner) connectToContainerMCP() {
	r.mu.RLock()
	sessionID := r.sessionID
	port := mcp.ContainerMCPPort
	r.mu.RUnlock()

	containerName := "erg-" + sessionID
	portSpec := fmt.Sprintf("%d/tcp", port)

	const maxAttempts = 30
	const retryInterval = 1 * time.Second
	const dialTimeout = 5 * time.Second
	// If HandleConn returns within this duration, the MCP subprocess likely
	// wasn't listening yet — Docker forwarded the TCP handshake but there was
	// no backend process, resulting in an immediate EOF.
	const immediateDisconnectThreshold = 2 * time.Second

	// Step 1: Discover the host-mapped port via `docker port`
	var hostPort string
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		r.mu.RLock()
		stopped := r.stopped
		r.mu.RUnlock()
		if stopped {
			r.log.Debug("connectToContainerMCP: runner stopped, aborting")
			return
		}

		out, err := exec.Command("docker", "port", containerName, portSpec).Output()
		if err == nil {
			// Output looks like "0.0.0.0:49153\n" or ":::49153\n"
			line := strings.TrimSpace(string(out))
			// Take the first line (may have both IPv4 and IPv6)
			if idx := strings.Index(line, "\n"); idx >= 0 {
				line = line[:idx]
			}
			// Extract the port after the last colon
			if idx := strings.LastIndex(line, ":"); idx >= 0 {
				hostPort = line[idx+1:]
			}
			if hostPort != "" {
				r.log.Info("discovered container MCP port", "hostPort", hostPort, "attempt", attempt)
				break
			}
		}

		if attempt < maxAttempts {
			r.log.Debug("docker port not ready, retrying", "attempt", attempt, "error", err)
			time.Sleep(retryInterval)
		}
	}

	if hostPort == "" {
		r.log.Error("failed to discover container MCP port after retries", "maxAttempts", maxAttempts)
		return
	}

	// Step 2: Connect and handle messages, retrying if the connection drops immediately.
	addr := "localhost:" + hostPort
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		r.mu.RLock()
		stopped := r.stopped
		r.mu.RUnlock()
		if stopped {
			r.log.Debug("connectToContainerMCP: runner stopped, aborting")
			return
		}

		conn, err := net.DialTimeout("tcp", addr, dialTimeout)
		if err != nil {
			if attempt < maxAttempts {
				r.log.Debug("dial to container MCP failed, retrying", "addr", addr, "attempt", attempt, "error", err)
				time.Sleep(retryInterval)
				continue
			}
			r.log.Error("failed to connect to container MCP after retries", "addr", addr, "maxAttempts", maxAttempts, "error", err)
			return
		}

		connectTime := time.Now()
		r.log.Info("connected to container MCP", "addr", addr, "attempt", attempt)

		// Hand the connection to the socket server (blocks until closed)
		r.mu.RLock()
		ss := r.socketServer
		r.mu.RUnlock()
		if ss == nil {
			conn.Close()
			return
		}

		ss.HandleConn(conn)

		// If HandleConn returned quickly, the MCP subprocess likely wasn't
		// listening yet. Docker's port forwarding accepted the TCP handshake
		// but there was no backend process, causing immediate EOF. Retry.
		elapsed := time.Since(connectTime)
		if elapsed < immediateDisconnectThreshold {
			r.log.Debug("container MCP connection dropped immediately, MCP subprocess may not be ready yet",
				"elapsed", elapsed, "attempt", attempt)
			time.Sleep(retryInterval)
			continue
		}

		// HandleConn ran for a meaningful duration — normal shutdown
		r.log.Info("container MCP connection closed", "elapsed", elapsed)
		return
	}

	r.log.Error("failed to establish stable container MCP connection after retries", "maxAttempts", maxAttempts)
}

// sendChunkWithTimeout sends a chunk to the response channel with timeout handling.
func (r *Runner) sendChunkWithTimeout(ch chan ResponseChunk, chunk ResponseChunk) error {
	select {
	case ch <- chunk:
		return nil
	case <-time.After(ResponseChannelFullTimeout):
		r.log.Error("response channel full after timeout", "timeout", ResponseChannelFullTimeout)
		return errChannelFull
	}
}

// closeResponseChannel safely closes the current response channel exactly once.
// Uses sync.Once to prevent double-close panics when multiple code paths
// (processResponse, handleProcessExit, handleFatalError) race to close the channel.
// The caller must hold r.mu when calling this method.
func (r *Runner) closeResponseChannel() {
	r.responseChan.Close()
}
