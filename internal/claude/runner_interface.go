package claude

import (
	"context"

	"github.com/zhubert/erg/internal/mcp"
)

// RunnerConfig is the interface for configuring a runner before a session starts.
// It contains all Set* methods used by the daemon and session manager to set policy
// on runners before they send any messages.
type RunnerConfig interface {
	SetAllowedTools(tools []string)
	AddAllowedTool(tool string)
	SetMCPServers(servers []MCPServer)
	SetForkFromSession(parentSessionID string)
	SetContainerized(containerized bool, image string)
	SetOnContainerReady(callback func())
	SetDisableStreamingChunks(disable bool)
	SetSystemPrompt(prompt string)
	SetHostTools(hostTools bool)
}

// RunnerSession is the interface for interacting with an active Claude session.
// It contains session state queries, message sending, channel accessors, and
// lifecycle methods. This is what SessionWorker needs to drive a session.
type RunnerSession interface {
	// Session state
	SessionStarted() bool
	IsStreaming() bool

	// Message sending and retrieval
	Send(ctx context.Context, prompt string) <-chan ResponseChunk
	SendContent(ctx context.Context, content []ContentBlock) <-chan ResponseChunk
	GetMessages() []Message

	// Permission/Question/Plan channels
	PermissionRequestChan() <-chan mcp.PermissionRequest
	SendPermissionResponse(resp mcp.PermissionResponse)
	QuestionRequestChan() <-chan mcp.QuestionRequest
	SendQuestionResponse(resp mcp.QuestionResponse)
	PlanApprovalRequestChan() <-chan mcp.PlanApprovalRequest
	SendPlanApprovalResponse(resp mcp.PlanApprovalResponse)

	// Host tool channels (for autonomous sessions)
	CreatePRRequestChan() <-chan mcp.CreatePRRequest
	SendCreatePRResponse(resp mcp.CreatePRResponse)
	PushBranchRequestChan() <-chan mcp.PushBranchRequest
	SendPushBranchResponse(resp mcp.PushBranchResponse)
	GetReviewCommentsRequestChan() <-chan mcp.GetReviewCommentsRequest
	SendGetReviewCommentsResponse(resp mcp.GetReviewCommentsResponse)
	CommentIssueRequestChan() <-chan mcp.CommentIssueRequest
	SendCommentIssueResponse(resp mcp.CommentIssueResponse)
	SubmitReviewRequestChan() <-chan mcp.SubmitReviewRequest
	SendSubmitReviewResponse(resp mcp.SubmitReviewResponse)

	// Lifecycle
	Stop()
	Interrupt() error
}

// RunnerInterface is the full interface combining RunnerConfig and RunnerSession.
// It is satisfied by the concrete Runner and MockRunner types.
// Most consumers should depend on RunnerConfig or RunnerSession directly.
type RunnerInterface interface {
	RunnerConfig
	RunnerSession
}

// Ensure Runner implements RunnerInterface at compile time.
var _ RunnerInterface = (*Runner)(nil)
