// Package claude provides the Claude CLI wrapper for managing conversations.
//
// The package is organized into focused modules:
//   - claude.go: Runner struct and core message handling
//   - channels.go: MCP channel accessor pairs (permission, question, plan approval, etc.)
//   - streaming.go: Stream processing, process exit handling, and container MCP connection
//   - runner_state.go: State structs (MCPChannels, StreamingState, TokenTracking, ResponseChannelState)
//   - parsing.go: Stream message parsing and tool input extraction
//   - mcp_config.go: MCP server configuration and socket management
//   - process_manager.go: Process lifecycle and auto-recovery
//   - container_auth.go: Container authentication and Docker argument building
//   - runner_interface.go: Interfaces for testing
//   - mock_runner.go: Mock runner for testing
//   - todo.go: TodoWrite tool parsing
//   - plugins.go: Plugin/marketplace management
package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/zhubert/erg/internal/logger"
	"github.com/zhubert/erg/internal/mcp"
)

// Claude runner constants
const (
	// PermissionChannelBuffer is the buffer size for permission request/response channels.
	// We use a buffer of 1 to allow the MCP server to send a request without blocking,
	// giving the user time to respond before the channel blocks on a second request.
	// A larger buffer would allow multiple permissions to queue up, which could confuse users.
	PermissionChannelBuffer = 1

	// PermissionTimeout is the timeout for waiting for permission responses.
	// 5 minutes allows users to read the prompt, check documentation, or switch tasks
	// without the request timing out. If this expires, the permission is denied.
	PermissionTimeout = 5 * time.Minute

	// MaxProcessRestartAttempts is the maximum number of times to try restarting
	// a crashed Claude process before giving up.
	MaxProcessRestartAttempts = 3

	// ProcessRestartDelay is the delay between restart attempts.
	ProcessRestartDelay = 500 * time.Millisecond

	// ResponseChannelFullTimeout is how long to wait when the response channel is full
	// before reporting an error (instead of silently dropping chunks).
	ResponseChannelFullTimeout = 10 * time.Second

	// ContainerStartupTimeout is the default timeout for a containerized session to
	// produce its first output (init message) before killing the process.
	// If Claude CLI hangs during startup (e.g., MCP server initialization with an
	// outdated image), the watchdog kills it after this timeout instead of hanging forever.
	// Can be overridden per-session via ProcessConfig.ContainerStartupTimeout.
	ContainerStartupTimeout = 5 * time.Minute
)

// DefaultAllowedTools is the minimal set of safe tools allowed by default.
// Users can add more tools via global or per-repo config, or by pressing 'a' during sessions.
// Composed from ToolSetBase + ToolSetSafeShell (see tools.go).
var DefaultAllowedTools = ComposeTools(ToolSetBase, ToolSetSafeShell)

// containerAllowedTools is a broad set of pre-authorized tools for containerized sessions.
// The container IS the sandbox, so all tools are safe to use without permission prompts.
// Composed from ToolSetBase + ToolSetContainerShell + ToolSetWeb + ToolSetProductivity (see tools.go).
var containerAllowedTools = ComposeTools(ToolSetBase, ToolSetContainerShell, ToolSetWeb, ToolSetProductivity)

// Message represents a chat message
type Message struct {
	Role    string // "user" or "assistant"
	Content string
}

// ContentType represents the type of content in a message block
type ContentType string

const (
	ContentTypeText  ContentType = "text"
	ContentTypeImage ContentType = "image"
)

// ContentBlock represents a single piece of content in a message
type ContentBlock struct {
	Type   ContentType  `json:"type"`
	Text   string       `json:"text,omitempty"`
	Source *ImageSource `json:"source,omitempty"`
}

// ImageSource represents an embedded image
type ImageSource struct {
	Type      string `json:"type"`       // "base64"
	MediaType string `json:"media_type"` // "image/png", "image/jpeg", etc.
	Data      string `json:"data"`       // base64 encoded image data
}

// StreamInputMessage is the format sent to Claude CLI via stdin in stream-json mode
type StreamInputMessage struct {
	Type    string `json:"type"` // "user"
	Message struct {
		Role    string         `json:"role"`    // "user"
		Content []ContentBlock `json:"content"` // content blocks
	} `json:"message"`
}

// TextContent creates a text-only content block slice for convenience
func TextContent(text string) []ContentBlock {
	return []ContentBlock{{Type: ContentTypeText, Text: text}}
}

// GetDisplayContent returns the text representation of content blocks for display
func GetDisplayContent(blocks []ContentBlock) string {
	var parts []string
	for _, block := range blocks {
		switch block.Type {
		case ContentTypeText:
			parts = append(parts, block.Text)
		case ContentTypeImage:
			parts = append(parts, "[Image]")
		}
	}
	return strings.Join(parts, "\n")
}

// Runner manages a Claude Code CLI session.
//
// MCP Channel Architecture:
// The Runner uses pairs of channels to communicate with the MCP server for interactive
// prompts (permissions, questions, plan approvals). Each pair has a request channel
// (populated by the MCP server) and a response channel (populated by the TUI).
//
// Channel Flow:
//  1. MCP server receives permission/question/plan request from Claude
//  2. MCP server sends request to the appropriate reqChan
//  3. Runner reads from reqChan and displays prompt to user (via TUI)
//  4. User responds, TUI sends response to respChan
//  5. MCP server reads from respChan and returns result to Claude
//
// All channels have a buffer of PermissionChannelBuffer (1) to allow the MCP server
// to send a request without blocking, while still limiting how many can queue up.
// Only one request of each type can be pending at a time.
type Runner struct {
	sessionID      string
	workingDir     string
	repoPath       string // Main repository path (for containerized worktree support)
	messages       []Message
	sessionStarted bool // tracks if session has been created
	mu             sync.RWMutex
	allowedTools   []string          // Pre-allowed tools for this session
	socketServer   *mcp.SocketServer // Socket server for MCP communication (persistent)
	mcpConfigPath  string            // Path to MCP config file (persistent)
	serverRunning  bool              // Whether the socket server is running

	// Session-scoped logger with sessionID pre-attached
	log *slog.Logger

	// Stream log file for raw Claude messages (separate from main debug log)
	streamLogFile *os.File

	// MCP interactive prompt channels (grouped in sub-struct)
	mcp *MCPChannels

	stopOnce sync.Once // Ensures Stop() is idempotent
	stopped  bool      // Set to true when Stop() is called, prevents reading from closed channels

	// Fork support: when set, first CLI invocation uses --resume <parentID> --fork-session
	// to inherit the parent's conversation history while creating a new session
	forkFromSessionID string

	// Process management via ProcessManager
	processManager *ProcessManager // Manages Claude CLI process lifecycle

	// Response channel management (grouped in sub-struct)
	responseChan *ResponseChannelState

	// Per-session streaming state (grouped in sub-struct)
	streaming *StreamingState

	// Token tracking state (grouped in sub-struct)
	tokens *TokenTracking

	// External MCP servers to include in config
	mcpServers []MCPServer

	// Container mode: when true, skip MCP and run inside a container
	containerized  bool
	containerImage string

	// Supervisor mode: when true, MCP config includes --supervisor flag
	supervisor bool

	// Host tools mode: when true, expose create_pr and push_branch MCP tools
	// Only used for autonomous supervisor sessions running inside containers
	hostTools bool

	// Disable streaming chunks: when true, omits --include-partial-messages for less verbose output
	// Useful for agent mode where real-time streaming is not needed
	disableStreamingChunks bool

	// System prompt: passed to Claude CLI via --append-system-prompt
	systemPrompt string

	// Container ready callback: invoked when containerized session receives init message
	onContainerReady func()

	// Redactor scrubs known secret values from transcripts and stream logs
	redactor *Redactor
}

// New creates a new Claude runner for a session
func New(sessionID, workingDir, repoPath string, sessionStarted bool, initialMessages []Message) *Runner {
	log := logger.WithSession(sessionID)
	log.Debug("runner created", "workDir", workingDir, "repoPath", repoPath, "started", sessionStarted, "messageCount", len(initialMessages))

	msgs := initialMessages
	if msgs == nil {
		msgs = []Message{}
	}
	// Start with empty tools - consumers build the full list via SetAllowedTools
	allowedTools := []string{}

	// Open stream log file for raw Claude messages
	var streamLogFile *os.File
	if streamLogPath, err := logger.StreamLogPath(sessionID); err != nil {
		log.Warn("failed to get stream log path", "error", err)
	} else {
		streamLogFile, err = os.OpenFile(streamLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			log.Warn("failed to open stream log file", "path", streamLogPath, "error", err)
		}
	}

	r := &Runner{
		sessionID:      sessionID,
		workingDir:     workingDir,
		repoPath:       repoPath,
		messages:       msgs,
		sessionStarted: sessionStarted,
		allowedTools:   allowedTools,
		log:            log,
		streamLogFile:  streamLogFile,
		mcp:            NewMCPChannels(),
		streaming:      NewStreamingState(),
		tokens:         &TokenTracking{},
		responseChan:   NewResponseChannelState(),
		redactor:       NewRedactor(),
	}

	// ProcessManager will be created lazily when first needed (after MCP config is ready)
	return r
}

// SessionStarted returns whether the session has been started
func (r *Runner) SessionStarted() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sessionStarted
}

// SetAllowedTools replaces the allowed tools list with the given tools.
// Consumers are responsible for building the full list (e.g., DefaultAllowedTools + repo tools).
func (r *Runner) SetAllowedTools(tools []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.allowedTools = make([]string, len(tools))
	copy(r.allowedTools, tools)
}

// AddAllowedTool adds a tool to the allowed list
func (r *Runner) AddAllowedTool(tool string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if slices.Contains(r.allowedTools, tool) {
		return
	}
	r.allowedTools = append(r.allowedTools, tool)
}

// SetForkFromSession sets the parent session ID to fork from.
// When set and the session hasn't started yet, the CLI will use
// --resume <parentID> --fork-session to inherit the parent's conversation history.
func (r *Runner) SetForkFromSession(parentSessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.forkFromSessionID = parentSessionID
	r.log.Debug("set fork from session", "parentSessionID", parentSessionID)
}

// SetContainerized configures the runner to run inside a container.
// When containerized, the MCP permission system is skipped entirely.
func (r *Runner) SetContainerized(containerized bool, image string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.containerized = containerized
	r.containerImage = image
	r.log.Debug("set containerized mode", "containerized", containerized, "image", image)
}

// SetOnContainerReady sets the callback to invoke when a containerized session is ready.
// This callback is called when the container initialization completes (init message received).
func (r *Runner) SetOnContainerReady(callback func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onContainerReady = callback
}

// SetDisableStreamingChunks configures the runner to disable streaming chunks.
// When disabled, Claude CLI will send complete messages instead of partial streaming deltas.
// This reduces logging verbosity and is useful for agent mode where real-time streaming is not needed.
func (r *Runner) SetDisableStreamingChunks(disable bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.disableStreamingChunks = disable
	r.log.Debug("set disable streaming chunks", "disabled", disable)
}

// SetSystemPrompt sets the system prompt passed to Claude CLI via --append-system-prompt.
func (r *Runner) SetSystemPrompt(prompt string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.systemPrompt = prompt
}

// SetSupervisor enables or disables supervisor mode for this runner.
// When enabled, supervisor tool channels are initialized and the MCP config
// will include the --supervisor flag.
func (r *Runner) SetSupervisor(supervisor bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.supervisor = supervisor
	if supervisor && r.mcp != nil && r.mcp.CreateChild == nil {
		r.mcp.InitSupervisorChannels()
	}
}

// SetHostTools enables or disables host tools mode for this runner.
// When enabled, host tool channels are initialized and the MCP config
// will include the --host-tools flag.
func (r *Runner) SetHostTools(hostTools bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hostTools = hostTools
	if hostTools && r.mcp != nil && r.mcp.CreatePR == nil {
		r.mcp.InitHostToolChannels()
	}
}

// IsStreaming returns whether this runner is currently streaming a response
func (r *Runner) IsStreaming() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.streaming.Active
}

// GetResponseChan returns the current response channel (nil if not streaming)
func (r *Runner) GetResponseChan() <-chan ResponseChunk {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.responseChan.Channel
}

// ChunkType represents the type of streaming chunk
type ChunkType string

const (
	ChunkTypeText              ChunkType = "text"               // Regular text content
	ChunkTypeToolUse           ChunkType = "tool_use"           // Claude is calling a tool
	ChunkTypeToolResult        ChunkType = "tool_result"        // Tool execution result
	ChunkTypeTodoUpdate        ChunkType = "todo_update"        // TodoWrite tool call with todo list
	ChunkTypeStreamStats       ChunkType = "stream_stats"       // Streaming statistics from result message
	ChunkTypeSubagentStatus    ChunkType = "subagent_status"    // Subagent activity started or ended
	ChunkTypePermissionDenials ChunkType = "permission_denials" // Permission denials from result message
)

// StreamUsage represents token usage data from Claude's result message
type StreamUsage struct {
	InputTokens              int `json:"input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	OutputTokens             int `json:"output_tokens"`
}

// ModelTokenCount represents token usage for a specific model
type ModelTokenCount struct {
	Model        string // Model name (e.g., "claude-opus-4-5-20251101")
	OutputTokens int    // Output tokens for this model
}

// StreamStats represents streaming statistics for display in the UI
type StreamStats struct {
	OutputTokens        int               // Total output tokens generated (sum of all models)
	TotalCostUSD        float64           // Total cost in USD
	ByModel             []ModelTokenCount // Per-model breakdown (only populated from result message)
	DurationMs          int               // Total request duration in milliseconds (from result message)
	DurationAPIMs       int               // API-only duration in milliseconds (from result message)
	CacheCreationTokens int               // Tokens written to cache
	CacheReadTokens     int               // Tokens read from cache (cache hits)
	InputTokens         int               // Non-cached input tokens
}

// ResponseChunk represents a chunk of streaming response
type ResponseChunk struct {
	Type              ChunkType          // Type of this chunk
	Content           string             // Text content (for text chunks and status)
	ToolName          string             // Tool being used (for tool_use chunks)
	ToolInput         string             // Brief description of tool input
	ToolUseID         string             // Unique ID for tool use (for matching tool_use to tool_result)
	ResultInfo        *ToolResultInfo    // Details about tool result (for tool_result chunks)
	TodoList          *TodoList          // Todo list (for ChunkTypeTodoUpdate)
	Stats             *StreamStats       // Streaming statistics (for ChunkTypeStreamStats)
	SubagentModel     string             // Model name when this is from a subagent (e.g., "claude-haiku-4-5-20251001")
	PermissionDenials []PermissionDenial // Permission denials (for ChunkTypePermissionDenials)
	Done              bool
	Error             error
}

// ModelUsageEntry represents usage statistics for a specific model in the result message.
// This includes both the parent model and any sub-agents (e.g., Haiku for Task agents).
type ModelUsageEntry struct {
	InputTokens              int `json:"inputTokens"`
	CacheCreationInputTokens int `json:"cacheCreationInputTokens"`
	CacheReadInputTokens     int `json:"cacheReadInputTokens"`
	OutputTokens             int `json:"outputTokens"`
}

// ensureProcessRunning starts the ProcessManager if not already running.
func (r *Runner) ensureProcessRunning() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// If there's already a running ProcessManager, nothing to do.
	if r.processManager != nil && r.processManager.IsRunning() {
		return nil
	}

	// Always create a fresh ProcessManager when one doesn't exist or isn't running.
	// After an interrupt or crash, the old ProcessManager's goroutines (readOutput,
	// drainStderr, monitorExit) may still be winding down. Reusing it would cause
	// race conditions between old and new goroutines competing for pipes and locks.
	// Set ContainerMCPPort for container sessions so docker publishes the port
	var containerMCPPort int
	if r.containerized && r.socketServer != nil {
		containerMCPPort = mcp.ContainerMCPPort
	}

	config := ProcessConfig{
		SessionID:              r.sessionID,
		WorkingDir:             r.workingDir,
		RepoPath:               r.repoPath,
		SessionStarted:         r.sessionStarted,
		AllowedTools:           make([]string, len(r.allowedTools)),
		MCPConfigPath:          r.mcpConfigPath,
		ForkFromSessionID:      r.forkFromSessionID,
		Containerized:          r.containerized,
		ContainerImage:         r.containerImage,
		ContainerMCPPort:       containerMCPPort,
		Supervisor:             r.supervisor,
		DisableStreamingChunks: r.disableStreamingChunks,
		SystemPrompt:           r.systemPrompt,
	}
	copy(config.AllowedTools, r.allowedTools)

	r.processManager = NewProcessManager(config, r.createProcessCallbacks(), r.log)

	err := r.processManager.Start()
	if err != nil && config.SessionStarted {
		// Resume failed (e.g., session was interrupted and can't be resumed).
		// Fall back to starting as a new session.
		r.log.Warn("resume failed, falling back to new session", "error", err)
		config.SessionStarted = false
		config.ForkFromSessionID = ""
		r.processManager = NewProcessManager(config, r.createProcessCallbacks(), r.log)
		err = r.processManager.Start()
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	// For container sessions, launch a goroutine that discovers the published
	// MCP port and dials into the container to establish the IPC connection.
	if r.containerized && r.socketServer != nil {
		go r.connectToContainerMCP()
	}

	return nil
}

// createProcessCallbacks creates the callbacks for ProcessManager events.
func (r *Runner) createProcessCallbacks() ProcessCallbacks {
	return ProcessCallbacks{
		OnLine:           r.handleProcessLine,
		OnProcessExit:    r.handleProcessExit,
		OnRestartAttempt: r.handleRestartAttempt,
		OnRestartFailed:  r.handleRestartFailed,
		OnFatalError:     r.handleFatalError,
		OnContainerReady: r.handleContainerReady,
	}
}

// Interrupt sends SIGINT to the Claude process to interrupt its current operation.
// This is used when the user presses Escape to stop a streaming response.
// Unlike Stop(), this doesn't terminate the process - it just interrupts the current task.
func (r *Runner) Interrupt() error {
	r.mu.Lock()
	pm := r.processManager
	r.mu.Unlock()

	if pm == nil {
		r.log.Debug("interrupt called but no process manager")
		return nil
	}

	// Set interrupted flag so handleProcessExit doesn't report an error
	pm.SetInterrupted(true)

	return pm.Interrupt()
}

// Send sends a message to Claude and streams the response
func (r *Runner) Send(cmdCtx context.Context, prompt string) <-chan ResponseChunk {
	return r.SendContent(cmdCtx, TextContent(prompt))
}

// SendContent sends structured content to Claude and streams the response
func (r *Runner) SendContent(cmdCtx context.Context, content []ContentBlock) <-chan ResponseChunk {
	ch := make(chan ResponseChunk, 100) // Buffered to avoid blocking response reader

	go func() {
		sendStartTime := time.Now()

		// Build display content for logging and history
		displayContent := GetDisplayContent(content)
		promptPreview := displayContent
		if len(promptPreview) > 50 {
			promptPreview = promptPreview[:50] + "..."
		}
		r.log.Debug("SendContent started", "content", promptPreview)

		// Add user message to history
		r.mu.Lock()
		r.messages = append(r.messages, Message{Role: "user", Content: r.redactor.Redact(displayContent)})
		r.mu.Unlock()

		// Ensure MCP server is running (persistent across Send calls).
		// For containerized sessions, the socket server runs on the host and the
		// MCP config uses --auto-approve so regular permissions auto-approve while
		// AskUserQuestion and ExitPlanMode still route through the TUI.
		if err := r.ensureServerRunning(); err != nil {
			ch <- ResponseChunk{Error: err, Done: true}
			close(ch)
			return
		}

		// Set up the response channel for routing BEFORE starting the process.
		// This is critical because the process might crash immediately after starting,
		// and handleFatalError needs the channel to report the error to the user.
		r.mu.Lock()
		r.streaming.Active = true
		r.streaming.Ctx = cmdCtx
		r.streaming.StartTime = time.Now()
		r.streaming.Complete = false // Reset for new message - we haven't received result yet
		r.responseChan.Setup(ch)
		r.tokens.Reset() // Reset token accumulator for new request
		if r.processManager != nil {
			r.processManager.SetInterrupted(false) // Reset interrupt flag for new message
		}
		r.mu.Unlock()

		// Start process manager if not running
		if err := r.ensureProcessRunning(); err != nil {
			// Send error before closing channel
			ch <- ResponseChunk{Error: err, Done: true}

			// Clean up state using Close() to keep sync.Once consistent
			r.mu.Lock()
			r.streaming.Active = false
			r.closeResponseChannel()
			r.mu.Unlock()
			return
		}

		// Build the input message
		inputMsg := StreamInputMessage{
			Type: "user",
		}
		inputMsg.Message.Role = "user"
		inputMsg.Message.Content = content

		// Serialize to JSON
		msgJSON, err := json.Marshal(inputMsg)
		if err != nil {
			r.log.Error("failed to serialize message", "error", err)
			ch <- ResponseChunk{Error: fmt.Errorf("failed to serialize message: %w", err), Done: true}
			close(ch)
			return
		}

		// Log message without base64 image data (which can be huge)
		hasImage := false
		for _, block := range content {
			if block.Type == ContentTypeImage {
				hasImage = true
				break
			}
		}
		if hasImage {
			r.log.Debug("writing message to stdin", "size", len(msgJSON), "hasImage", true)
		} else {
			r.log.Debug("writing message to stdin", "message", string(msgJSON))
		}

		// Write to process via ProcessManager
		r.mu.Lock()
		pm := r.processManager
		r.mu.Unlock()

		if pm == nil {
			ch <- ResponseChunk{Error: fmt.Errorf("process manager not available"), Done: true}
			close(ch)
			return
		}

		if err := pm.WriteMessage(append(msgJSON, '\n')); err != nil {
			r.log.Error("failed to write to stdin", "error", err)
			ch <- ResponseChunk{Error: err, Done: true}
			close(ch)
			return
		}

		r.log.Debug("message sent, waiting for response", "elapsed", time.Since(sendStartTime))

		// The response will be read by ProcessManager and routed via callbacks
	}()

	return ch
}

// GetMessages returns a copy of the message history.
// Thread-safe: takes a snapshot of messages under lock to prevent
// race conditions with concurrent appends from readPersistentResponses
// and SendContent goroutines.
func (r *Runner) GetMessages() []Message {
	r.mu.RLock()
	// Create a new slice with exact capacity to prevent any aliasing
	// issues during concurrent appends to the original slice
	msgLen := len(r.messages)
	messages := make([]Message, msgLen)
	copy(messages, r.messages)
	r.mu.RUnlock()
	return messages
}

// GetMessagesWithStreaming returns a copy of the message history plus the
// current in-progress streaming response (if any) as an assistant message.
// This is useful when a mid-turn MCP tool (like create_pr) needs the full
// transcript including content that hasn't been finalized into r.messages yet.
func (r *Runner) GetMessagesWithStreaming() []Message {
	r.mu.RLock()
	streamingContent := r.streaming.Response.String()
	msgLen := len(r.messages)
	hasStreaming := r.streaming.Active && streamingContent != ""
	extra := 0
	if hasStreaming {
		extra = 1
	}
	messages := make([]Message, msgLen, msgLen+extra)
	copy(messages, r.messages)
	r.mu.RUnlock()

	if hasStreaming {
		messages = append(messages, Message{Role: "assistant", Content: streamingContent})
	}
	return messages
}

// AddAssistantMessage adds an assistant message to the history
func (r *Runner) AddAssistantMessage(content string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = append(r.messages, Message{Role: "assistant", Content: r.redactor.Redact(content)})
}

// Stop cleanly stops the runner and releases resources.
// This method is idempotent - multiple calls are safe.
func (r *Runner) Stop() {
	r.stopOnce.Do(func() {
		r.log.Info("stopping runner")

		// Stop the ProcessManager first
		r.mu.Lock()
		pm := r.processManager
		r.mu.Unlock()

		if pm != nil {
			pm.Stop()
		}

		r.mu.Lock()
		defer r.mu.Unlock()

		// Mark as stopped BEFORE closing channels to prevent reads from closed channels
		// PermissionRequestChan() and QuestionRequestChan() check this flag
		r.stopped = true

		// Close socket server if running (runs on host for both container and non-container sessions)
		if r.socketServer != nil {
			r.log.Debug("closing persistent socket server")
			r.socketServer.Close()
			r.socketServer = nil
		}

		// Remove MCP config file and log any errors
		if r.mcpConfigPath != "" {
			r.log.Debug("removing MCP config file", "path", r.mcpConfigPath)
			if err := os.Remove(r.mcpConfigPath); err != nil && !os.IsNotExist(err) {
				r.log.Warn("failed to remove MCP config file", "path", r.mcpConfigPath, "error", err)
			}
			r.mcpConfigPath = ""
		}

		r.serverRunning = false

		// Close MCP channels to unblock any waiting goroutines
		if r.mcp != nil {
			r.mcp.Close()
		}

		// Close stream log file
		if r.streamLogFile != nil {
			r.streamLogFile.Close()
			r.streamLogFile = nil
		}

		r.log.Info("runner stopped")
	})
}
