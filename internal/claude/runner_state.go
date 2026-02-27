package claude

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/zhubert/erg/internal/mcp"
)

// MCPChannels groups all MCP communication channels for interactive prompts.
// Each prompt type (permission, question, plan approval) has a request/response pair.
// Request channels are populated by the MCP server, response channels by the TUI.
type MCPChannels struct {
	Permission   *mcp.ChannelPair[mcp.PermissionRequest, mcp.PermissionResponse]
	Question     *mcp.ChannelPair[mcp.QuestionRequest, mcp.QuestionResponse]
	PlanApproval *mcp.ChannelPair[mcp.PlanApprovalRequest, mcp.PlanApprovalResponse]

	// Host tool channels (nil when not an autonomous session)
	CreatePR          *mcp.ChannelPair[mcp.CreatePRRequest, mcp.CreatePRResponse]
	PushBranch        *mcp.ChannelPair[mcp.PushBranchRequest, mcp.PushBranchResponse]
	GetReviewComments *mcp.ChannelPair[mcp.GetReviewCommentsRequest, mcp.GetReviewCommentsResponse]
	CommentIssue      *mcp.ChannelPair[mcp.CommentIssueRequest, mcp.CommentIssueResponse]
	SubmitReview      *mcp.ChannelPair[mcp.SubmitReviewRequest, mcp.SubmitReviewResponse]
}

// NewMCPChannels creates a new MCPChannels with buffered channels.
func NewMCPChannels() *MCPChannels {
	return &MCPChannels{
		Permission:   mcp.NewChannelPair[mcp.PermissionRequest, mcp.PermissionResponse](PermissionChannelBuffer),
		Question:     mcp.NewChannelPair[mcp.QuestionRequest, mcp.QuestionResponse](PermissionChannelBuffer),
		PlanApproval: mcp.NewChannelPair[mcp.PlanApprovalRequest, mcp.PlanApprovalResponse](PermissionChannelBuffer),
	}
}

// InitHostToolChannels initializes the host tool channels.
// These are only created when the session is autonomous.
func (m *MCPChannels) InitHostToolChannels() {
	m.CreatePR = mcp.NewChannelPair[mcp.CreatePRRequest, mcp.CreatePRResponse](PermissionChannelBuffer)
	m.PushBranch = mcp.NewChannelPair[mcp.PushBranchRequest, mcp.PushBranchResponse](PermissionChannelBuffer)
	m.GetReviewComments = mcp.NewChannelPair[mcp.GetReviewCommentsRequest, mcp.GetReviewCommentsResponse](PermissionChannelBuffer)
	m.CommentIssue = mcp.NewChannelPair[mcp.CommentIssueRequest, mcp.CommentIssueResponse](PermissionChannelBuffer)
	m.SubmitReview = mcp.NewChannelPair[mcp.SubmitReviewRequest, mcp.SubmitReviewResponse](PermissionChannelBuffer)
}

// Close closes all channels. Safe to call multiple times.
func (m *MCPChannels) Close() {
	m.Permission.Close()
	m.Question.Close()
	m.PlanApproval.Close()
	m.CreatePR.Close()
	m.PushBranch.Close()
	m.GetReviewComments.Close()
	m.CommentIssue.Close()
	m.SubmitReview.Close()
}

// StreamingState tracks state during response streaming.
// All fields are protected by the Runner's mutex.
type StreamingState struct {
	Active    bool               // Whether currently streaming
	Ctx       context.Context    // Context for current operation
	Cancel    context.CancelFunc // Cancel function for interruption
	StartTime time.Time          // When streaming started
	Complete  bool               // Whether result message was received

	// Response building
	Response         strings.Builder // Accumulates response content
	LastWasToolUse   bool            // Track if last chunk was tool use
	EndsWithNewline  bool            // Track if response ends with \n
	EndsWithDoubleNL bool            // Track if response ends with \n\n
	FirstChunk       bool            // Track if this is first chunk

	// Subagent tracking
	CurrentSubagentModel string // Model of active subagent (empty when no subagent)
}

// NewStreamingState creates a new StreamingState ready for use.
func NewStreamingState() *StreamingState {
	s := &StreamingState{
		FirstChunk: true,
	}
	s.Response.Grow(8192)
	return s
}

// Reset resets the streaming state for a new response.
func (s *StreamingState) Reset() {
	s.Active = false
	s.Ctx = nil
	s.Cancel = nil
	s.StartTime = time.Time{}
	s.Complete = false
	s.Response.Reset()
	s.Response.Grow(8192)
	s.LastWasToolUse = false
	s.EndsWithNewline = false
	s.EndsWithDoubleNL = false
	s.FirstChunk = true
	s.CurrentSubagentModel = ""
}

// TokenTracking accumulates token usage across API calls within a request.
// Claude CLI sends cumulative output_tokens within each API call, but resets on new API calls.
// We track message IDs to detect new API calls and accumulate across them.
type TokenTracking struct {
	AccumulatedOutput int    // Accumulated output tokens from completed API calls
	LastMessageID     string // Track the message ID to detect new API calls
	LastMessageTokens int    // Last seen output tokens for the current message ID

	// Cache efficiency tracking (updated from streaming messages)
	CacheCreation int // Tokens written to cache
	CacheRead     int // Tokens read from cache (cache hits)
	Input         int // Non-cached input tokens
}

// Reset resets the token tracking for a new request.
func (t *TokenTracking) Reset() {
	t.AccumulatedOutput = 0
	t.LastMessageID = ""
	t.LastMessageTokens = 0
	t.CacheCreation = 0
	t.CacheRead = 0
	t.Input = 0
}

// CurrentTotal returns the total output tokens (accumulated + current message).
func (t *TokenTracking) CurrentTotal() int {
	return t.AccumulatedOutput + t.LastMessageTokens
}

// ResponseChannelState manages the current response channel for routing.
// All fields are protected by the Runner's mutex.
type ResponseChannelState struct {
	Channel   chan ResponseChunk // Current response channel
	Closed    bool               // Whether channel has been closed
	CloseOnce *sync.Once         // Ensures channel is closed exactly once
}

// NewResponseChannelState creates a new ResponseChannelState.
func NewResponseChannelState() *ResponseChannelState {
	return &ResponseChannelState{}
}

// Setup prepares the state for a new response channel.
func (r *ResponseChannelState) Setup(ch chan ResponseChunk) {
	r.Channel = ch
	r.Closed = false
	r.CloseOnce = &sync.Once{}
}

// Close safely closes the channel exactly once.
func (r *ResponseChannelState) Close() {
	if r.CloseOnce == nil || r.Channel == nil {
		return
	}
	r.CloseOnce.Do(func() {
		close(r.Channel)
		r.Closed = true
	})
}

// IsOpen returns true if the channel is set and not closed.
func (r *ResponseChannelState) IsOpen() bool {
	return r.Channel != nil && !r.Closed
}
