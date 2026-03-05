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
	if !r.sessionStarted && strings.Contains(line, `"type":"system"`) && strings.Contains(line, `"subtype":"init"`) {
		r.mu.Lock()
		r.sessionStarted = true
		pm := r.processManager
		r.mu.Unlock()
		if pm != nil {
			pm.MarkSessionStarted()
		}
		r.log.Info("session marked as started on init message")
	}

	// Parse the JSON message into chunks
	chunks := parseStreamMessage(line, r.log)

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
			r.streaming.Response.WriteString(chunk.Content)
		case ChunkTypeToolUse:
			// Append a simple tool marker to the response for message history
			if r.streaming.Response.Len() > 0 {
				last := r.streaming.Response.String()
				if last[len(last)-1] != '\n' {
					r.streaming.Response.WriteString("\n")
				}
			}
			r.streaming.Response.WriteString("[")
			r.streaming.Response.WriteString(chunk.ToolName)
			if chunk.ToolInput != "" {
				r.streaming.Response.WriteString(": ")
				r.streaming.Response.WriteString(chunk.ToolInput)
			}
			r.streaming.Response.WriteString("]\n")
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

	// Parse the message to handle token accumulation and result messages
	var msg streamMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &msg); err == nil {
		// Handle token accumulation for assistant messages
		if msg.Type == "assistant" && msg.Message.Usage != nil && msg.Message.Usage.OutputTokens > 0 {
			r.mu.Lock()
			messageID := msg.Message.ID

			if messageID != "" && messageID != r.tokens.LastMessageID {
				if r.tokens.LastMessageID != "" {
					r.tokens.AccumulatedOutput += r.tokens.LastMessageTokens
				}
				r.tokens.LastMessageID = messageID
				r.tokens.LastMessageTokens = 0
			}

			r.tokens.LastMessageTokens = msg.Message.Usage.OutputTokens

			// Update cache efficiency stats
			r.tokens.CacheCreation = msg.Message.Usage.CacheCreationInputTokens
			r.tokens.CacheRead = msg.Message.Usage.CacheReadInputTokens
			r.tokens.Input = msg.Message.Usage.InputTokens

			currentTotal := r.tokens.CurrentTotal()
			cacheCreation := r.tokens.CacheCreation
			cacheRead := r.tokens.CacheRead
			inputTokens := r.tokens.Input

			r.mu.Unlock()

			if ch != nil {
				r.sendChunkWithTimeout(ch, ResponseChunk{
					Type: ChunkTypeStreamStats,
					Stats: &StreamStats{
						OutputTokens:        currentTotal,
						TotalCostUSD:        0,
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
			r.streaming.Complete = true
			pm := r.processManager
			r.mu.Unlock()
			if pm != nil {
				pm.MarkSessionStarted()
				pm.ResetRestartAttempts()
			}
			r.mu.Lock()

			// Determine error message
			errorText := msg.Result
			if errorText == "" {
				errorText = msg.Error
			}
			if errorText == "" && len(msg.Errors) > 0 {
				errorText = strings.Join(msg.Errors, "; ")
			}

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

			// Log permission denials if any
			if len(msg.PermissionDenials) > 0 {
				r.log.Warn("permission denials in result", "count", len(msg.PermissionDenials))
				for _, d := range msg.PermissionDenials {
					r.log.Warn("permission denied", "tool", d.Tool, "description", d.Description, "reason", d.Reason)
				}
			}

			r.messages = append(r.messages, Message{Role: "assistant", Content: r.redactor.Redact(r.streaming.Response.String())})

			// Emit stream stats chunk before Done if we have usage data
			if ch != nil && !r.responseChan.Closed {
				var totalOutputTokens int
				var byModel []ModelTokenCount

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
					var cacheCreation, cacheRead, inputTokens int
					if modelUsageInputTokens > 0 || modelUsageCacheCreation > 0 || modelUsageCacheRead > 0 {
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

// handleProcessExit is called when the process exits.
// Returns true if the process should be restarted.
func (r *Runner) handleProcessExit(err error, stderrContent string) bool {
	r.mu.Lock()
	stopped := r.stopped
	responseComplete := r.streaming.Complete

	if stopped {
		r.mu.Unlock()
		return false
	}

	if responseComplete {
		r.log.Debug("process exited after response complete, not restarting")
		r.mu.Unlock()
		return false
	}

	r.streaming.Active = false
	r.mu.Unlock()

	return true
}

// handleRestartAttempt is called when a restart is being attempted.
func (r *Runner) handleRestartAttempt(attemptNum int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	ch := r.responseChan.Channel
	chClosed := r.responseChan.Closed

	if ch != nil && !chClosed {
		select {
		case ch <- ResponseChunk{
			Type:    ChunkTypeText,
			Content: fmt.Sprintf("\n[Process crashed, attempting restart %d/%d...]\n", attemptNum, MaxProcessRestartAttempts),
		}:
		default:
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
		select {
		case ch <- ResponseChunk{Error: err, Done: true}:
		default:
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
			line := strings.TrimSpace(string(out))
			if idx := strings.Index(line, "\n"); idx >= 0 {
				line = line[:idx]
			}
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

	// Step 2: Connect and handle messages
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

		r.mu.RLock()
		ss := r.socketServer
		r.mu.RUnlock()
		if ss == nil {
			conn.Close()
			return
		}

		ss.HandleConn(conn)

		elapsed := time.Since(connectTime)
		if elapsed < immediateDisconnectThreshold {
			r.log.Debug("container MCP connection dropped immediately, MCP subprocess may not be ready yet",
				"elapsed", elapsed, "attempt", attempt)
			time.Sleep(retryInterval)
			continue
		}

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
func (r *Runner) closeResponseChannel() {
	r.responseChan.Close()
}
