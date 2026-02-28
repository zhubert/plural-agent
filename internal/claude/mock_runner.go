package claude

import (
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/zhubert/erg/internal/mcp"
)

// MockRunner is a test double for Runner that doesn't spawn real processes.
// It allows tests to control response chunks, simulate permissions/questions,
// and verify messages sent to Claude.
//
// NOTE: This file is used by integration tests in internal/app/*_test.go.
type MockRunner struct {
	mu sync.RWMutex

	// State
	sessionID      string
	sessionStarted bool
	isStreaming    bool
	messages       []Message
	allowedTools   []string
	mcpServers     []MCPServer

	// Response queue - chunks queued by tests to be returned by Send/SendContent
	responseQueue []ResponseChunk
	responseChan  chan ResponseChunk

	// Permission/Question/Plan channels
	permission   *mcp.ChannelPair[mcp.PermissionRequest, mcp.PermissionResponse]
	question     *mcp.ChannelPair[mcp.QuestionRequest, mcp.QuestionResponse]
	planApproval *mcp.ChannelPair[mcp.PlanApprovalRequest, mcp.PlanApprovalResponse]

	// Host tool channels
	createPR          *mcp.ChannelPair[mcp.CreatePRRequest, mcp.CreatePRResponse]
	pushBranch        *mcp.ChannelPair[mcp.PushBranchRequest, mcp.PushBranchResponse]
	getReviewComments *mcp.ChannelPair[mcp.GetReviewCommentsRequest, mcp.GetReviewCommentsResponse]
	commentIssue      *mcp.ChannelPair[mcp.CommentIssueRequest, mcp.CommentIssueResponse]
	submitReview      *mcp.ChannelPair[mcp.SubmitReviewRequest, mcp.SubmitReviewResponse]

	// Callbacks for test assertions
	OnSend             func(content []ContentBlock)
	OnPermissionResp   func(resp mcp.PermissionResponse)
	OnQuestionResp     func(resp mcp.QuestionResponse)
	OnPlanApprovalResp func(resp mcp.PlanApprovalResponse)

	// Fork tracking
	forkFromSessionID string

	// Simulated streaming content for GetMessagesWithStreaming
	streamingContent string

	stopped bool
}

// NewMockRunner creates a mock runner for testing.
func NewMockRunner(sessionID string, sessionStarted bool, initialMessages []Message) *MockRunner {
	msgs := initialMessages
	if msgs == nil {
		msgs = []Message{}
	}
	allowedTools := []string{}

	return &MockRunner{
		sessionID:      sessionID,
		sessionStarted: sessionStarted,
		messages:       msgs,
		allowedTools:   allowedTools,
		permission:     mcp.NewChannelPair[mcp.PermissionRequest, mcp.PermissionResponse](1),
		question:       mcp.NewChannelPair[mcp.QuestionRequest, mcp.QuestionResponse](1),
		planApproval:   mcp.NewChannelPair[mcp.PlanApprovalRequest, mcp.PlanApprovalResponse](1),
	}
}

// QueueResponse queues response chunks to be returned by Send/SendContent.
// Chunks are delivered in order when SendContent is called.
func (m *MockRunner) QueueResponse(chunks ...ResponseChunk) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.responseQueue = append(m.responseQueue, chunks...)
}

// InjectChunk sends a chunk directly to the current response channel.
// This is useful for tests that need to feed responses after SendContent
// has been called with an empty queue (e.g., to unblock a waiting worker).
func (m *MockRunner) InjectChunk(chunk ResponseChunk) {
	m.mu.RLock()
	ch := m.responseChan
	m.mu.RUnlock()
	if ch != nil {
		ch <- chunk
	}
}

// ClearResponseQueue clears any queued responses.
func (m *MockRunner) ClearResponseQueue() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.responseQueue = nil
}

// SimulatePermissionRequest triggers a permission request that the UI will receive.
func (m *MockRunner) SimulatePermissionRequest(req mcp.PermissionRequest) {
	m.mu.RLock()
	stopped := m.stopped
	m.mu.RUnlock()
	if stopped {
		return
	}
	m.permission.Req <- req
}

// SimulateQuestionRequest triggers a question request that the UI will receive.
func (m *MockRunner) SimulateQuestionRequest(req mcp.QuestionRequest) {
	m.mu.RLock()
	stopped := m.stopped
	m.mu.RUnlock()
	if stopped {
		return
	}
	m.question.Req <- req
}

// SimulatePlanApprovalRequest triggers a plan approval request that the UI will receive.
func (m *MockRunner) SimulatePlanApprovalRequest(req mcp.PlanApprovalRequest) {
	m.mu.RLock()
	stopped := m.stopped
	m.mu.RUnlock()
	if stopped {
		return
	}
	m.planApproval.Req <- req
}

// SimulateCreatePRRequest triggers a create PR request that the UI will receive.
func (m *MockRunner) SimulateCreatePRRequest(req mcp.CreatePRRequest) {
	m.mu.RLock()
	stopped := m.stopped
	ch := m.createPR
	m.mu.RUnlock()
	if stopped || ch == nil {
		return
	}
	ch.Req <- req
}

// SimulatePushBranchRequest triggers a push branch request that the UI will receive.
func (m *MockRunner) SimulatePushBranchRequest(req mcp.PushBranchRequest) {
	m.mu.RLock()
	stopped := m.stopped
	ch := m.pushBranch
	m.mu.RUnlock()
	if stopped || ch == nil {
		return
	}
	ch.Req <- req
}

// SimulateGetReviewCommentsRequest triggers a get review comments request that the UI will receive.
func (m *MockRunner) SimulateGetReviewCommentsRequest(req mcp.GetReviewCommentsRequest) {
	m.mu.RLock()
	stopped := m.stopped
	ch := m.getReviewComments
	m.mu.RUnlock()
	if stopped || ch == nil {
		return
	}
	ch.Req <- req
}

// SimulateCommentIssueRequest triggers a comment issue request that the UI will receive.
func (m *MockRunner) SimulateCommentIssueRequest(req mcp.CommentIssueRequest) {
	m.mu.RLock()
	stopped := m.stopped
	ch := m.commentIssue
	m.mu.RUnlock()
	if stopped || ch == nil {
		return
	}
	ch.Req <- req
}

// SimulateSubmitReviewRequest triggers a submit review request that the UI will receive.
func (m *MockRunner) SimulateSubmitReviewRequest(req mcp.SubmitReviewRequest) {
	m.mu.RLock()
	stopped := m.stopped
	ch := m.submitReview
	m.mu.RUnlock()
	if stopped || ch == nil {
		return
	}
	ch.Req <- req
}

// SessionStarted implements RunnerSession.
func (m *MockRunner) SessionStarted() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessionStarted
}

// IsStreaming implements RunnerSession.
func (m *MockRunner) IsStreaming() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.isStreaming
}

// Send implements RunnerSession.
func (m *MockRunner) Send(ctx context.Context, prompt string) <-chan ResponseChunk {
	return m.SendContent(ctx, TextContent(prompt))
}

// SendContent implements RunnerSession.
func (m *MockRunner) SendContent(ctx context.Context, content []ContentBlock) <-chan ResponseChunk {
	m.mu.Lock()

	// Add user message
	displayContent := GetDisplayContent(content)
	m.messages = append(m.messages, Message{Role: "user", Content: displayContent})

	// Call callback if set
	if m.OnSend != nil {
		m.OnSend(content)
	}

	// Create response channel
	ch := make(chan ResponseChunk, 100)
	m.responseChan = ch
	m.isStreaming = true

	// Copy queued responses
	queue := make([]ResponseChunk, len(m.responseQueue))
	copy(queue, m.responseQueue)
	m.responseQueue = nil

	m.mu.Unlock()

	// Stream queued responses in goroutine
	go func() {
		var fullResponse strings.Builder
		for _, chunk := range queue {
			select {
			case <-ctx.Done():
				ch <- ResponseChunk{Done: true}
				close(ch)
				m.mu.Lock()
				m.isStreaming = false
				m.mu.Unlock()
				return
			default:
				if chunk.Type == ChunkTypeText {
					fullResponse.WriteString(chunk.Content)
				}
				ch <- chunk

				// If this is the done chunk, finalize
				if chunk.Done {
					m.mu.Lock()
					m.sessionStarted = true
					m.messages = append(m.messages, Message{Role: "assistant", Content: fullResponse.String()})
					m.isStreaming = false
					m.mu.Unlock()
					close(ch)
					return
				}
			}
		}

		// If no done chunk was in the queue, don't close the channel
		// This allows tests to simulate in-progress streaming
	}()

	return ch
}

// GetMessages implements RunnerSession.
func (m *MockRunner) GetMessages() []Message {
	m.mu.RLock()
	defer m.mu.RUnlock()
	// Return a copy to prevent race conditions
	msgs := make([]Message, len(m.messages))
	copy(msgs, m.messages)
	return msgs
}

// GetMessagesWithStreaming is not part of any interface — it is only used by tests
// within the claude package. Returns GetMessages() plus any simulated streaming
// content set via SetStreamingContent.
func (m *MockRunner) GetMessagesWithStreaming() []Message {
	m.mu.RLock()
	defer m.mu.RUnlock()
	msgs := make([]Message, len(m.messages))
	copy(msgs, m.messages)
	if m.streamingContent != "" {
		msgs = append(msgs, Message{Role: "assistant", Content: m.streamingContent})
	}
	return msgs
}

// SetStreamingContent sets simulated in-progress streaming content
// that will be included by GetMessagesWithStreaming but not GetMessages.
func (m *MockRunner) SetStreamingContent(content string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.streamingContent = content
}

// AddAssistantMessage is not part of any interface — it is only used by tests
// within the claude package.
func (m *MockRunner) AddAssistantMessage(content string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, Message{Role: "assistant", Content: content})
}

// GetResponseChan is not part of any interface — it is only used by tests
// within the claude package.
func (m *MockRunner) GetResponseChan() <-chan ResponseChunk {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.responseChan
}

// SetAllowedTools implements RunnerConfig.
func (m *MockRunner) SetAllowedTools(tools []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.allowedTools = make([]string, len(tools))
	copy(m.allowedTools, tools)
}

// AddAllowedTool implements RunnerConfig.
func (m *MockRunner) AddAllowedTool(tool string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !slices.Contains(m.allowedTools, tool) {
		m.allowedTools = append(m.allowedTools, tool)
	}
}

// SetMCPServers implements RunnerConfig.
func (m *MockRunner) SetMCPServers(servers []MCPServer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mcpServers = servers
}

// SetForkFromSession implements RunnerConfig.
// In mock, this stores the parent session ID for test verification.
func (m *MockRunner) SetForkFromSession(parentSessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.forkFromSessionID = parentSessionID
}

// GetForkFromSessionID returns the parent session ID if set (for testing).
func (m *MockRunner) GetForkFromSessionID() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.forkFromSessionID
}

// SetContainerized implements RunnerConfig.
// In mock, this is a no-op since we don't spawn real processes.
func (m *MockRunner) SetContainerized(containerized bool, image string) {
	// No-op for mock
}

// SetOnContainerReady implements RunnerConfig.
// In mock, this is a no-op since we don't spawn real containers.
func (m *MockRunner) SetOnContainerReady(callback func()) {
	// No-op for mock
}

// SetDisableStreamingChunks implements RunnerConfig.
// In mock, this is a no-op since we don't use real streaming.
func (m *MockRunner) SetDisableStreamingChunks(disable bool) {
	// No-op for mock
}

// SetSystemPrompt implements RunnerConfig.
func (m *MockRunner) SetSystemPrompt(prompt string) {
	// No-op for mock
}

// PermissionRequestChan implements RunnerSession.
func (m *MockRunner) PermissionRequestChan() <-chan mcp.PermissionRequest {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.stopped {
		return nil
	}
	return m.permission.Req
}

// SendPermissionResponse implements RunnerSession.
func (m *MockRunner) SendPermissionResponse(resp mcp.PermissionResponse) {
	m.mu.RLock()
	stopped := m.stopped
	m.mu.RUnlock()

	if stopped {
		return
	}

	if m.OnPermissionResp != nil {
		m.OnPermissionResp(resp)
	}

	select {
	case m.permission.Resp <- resp:
	default:
	}
}

// QuestionRequestChan implements RunnerSession.
func (m *MockRunner) QuestionRequestChan() <-chan mcp.QuestionRequest {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.stopped {
		return nil
	}
	return m.question.Req
}

// SendQuestionResponse implements RunnerSession.
func (m *MockRunner) SendQuestionResponse(resp mcp.QuestionResponse) {
	m.mu.RLock()
	stopped := m.stopped
	m.mu.RUnlock()

	if stopped {
		return
	}

	if m.OnQuestionResp != nil {
		m.OnQuestionResp(resp)
	}

	select {
	case m.question.Resp <- resp:
	default:
	}
}

// PlanApprovalRequestChan implements RunnerSession.
func (m *MockRunner) PlanApprovalRequestChan() <-chan mcp.PlanApprovalRequest {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.stopped {
		return nil
	}
	return m.planApproval.Req
}

// SendPlanApprovalResponse implements RunnerSession.
func (m *MockRunner) SendPlanApprovalResponse(resp mcp.PlanApprovalResponse) {
	m.mu.RLock()
	stopped := m.stopped
	m.mu.RUnlock()

	if stopped {
		return
	}

	if m.OnPlanApprovalResp != nil {
		m.OnPlanApprovalResp(resp)
	}

	select {
	case m.planApproval.Resp <- resp:
	default:
	}
}

// SetHostTools implements RunnerConfig.
func (m *MockRunner) SetHostTools(hostTools bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if hostTools && m.createPR == nil {
		m.createPR = mcp.NewChannelPair[mcp.CreatePRRequest, mcp.CreatePRResponse](1)
		m.pushBranch = mcp.NewChannelPair[mcp.PushBranchRequest, mcp.PushBranchResponse](1)
		m.getReviewComments = mcp.NewChannelPair[mcp.GetReviewCommentsRequest, mcp.GetReviewCommentsResponse](1)
		m.commentIssue = mcp.NewChannelPair[mcp.CommentIssueRequest, mcp.CommentIssueResponse](1)
		m.submitReview = mcp.NewChannelPair[mcp.SubmitReviewRequest, mcp.SubmitReviewResponse](1)
	}
}

// CreatePRRequestChan implements RunnerSession.
func (m *MockRunner) CreatePRRequestChan() <-chan mcp.CreatePRRequest {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.stopped || m.createPR == nil {
		return nil
	}
	return m.createPR.Req
}

// SendCreatePRResponse implements RunnerSession.
func (m *MockRunner) SendCreatePRResponse(resp mcp.CreatePRResponse) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.stopped || m.createPR == nil {
		return
	}
	select {
	case m.createPR.Resp <- resp:
	default:
	}
}

// PushBranchRequestChan implements RunnerSession.
func (m *MockRunner) PushBranchRequestChan() <-chan mcp.PushBranchRequest {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.stopped || m.pushBranch == nil {
		return nil
	}
	return m.pushBranch.Req
}

// SendPushBranchResponse implements RunnerSession.
func (m *MockRunner) SendPushBranchResponse(resp mcp.PushBranchResponse) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.stopped || m.pushBranch == nil {
		return
	}
	select {
	case m.pushBranch.Resp <- resp:
	default:
	}
}

// GetReviewCommentsRequestChan implements RunnerSession.
func (m *MockRunner) GetReviewCommentsRequestChan() <-chan mcp.GetReviewCommentsRequest {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.stopped || m.getReviewComments == nil {
		return nil
	}
	return m.getReviewComments.Req
}

// SendGetReviewCommentsResponse implements RunnerSession.
func (m *MockRunner) SendGetReviewCommentsResponse(resp mcp.GetReviewCommentsResponse) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.stopped || m.getReviewComments == nil {
		return
	}
	select {
	case m.getReviewComments.Resp <- resp:
	default:
	}
}

// CommentIssueRequestChan implements RunnerSession.
func (m *MockRunner) CommentIssueRequestChan() <-chan mcp.CommentIssueRequest {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.stopped || m.commentIssue == nil {
		return nil
	}
	return m.commentIssue.Req
}

// SendCommentIssueResponse implements RunnerSession.
func (m *MockRunner) SendCommentIssueResponse(resp mcp.CommentIssueResponse) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.stopped || m.commentIssue == nil {
		return
	}
	select {
	case m.commentIssue.Resp <- resp:
	default:
	}
}

// SubmitReviewRequestChan implements RunnerSession.
func (m *MockRunner) SubmitReviewRequestChan() <-chan mcp.SubmitReviewRequest {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.stopped || m.submitReview == nil {
		return nil
	}
	return m.submitReview.Req
}

// SendSubmitReviewResponse implements RunnerSession.
func (m *MockRunner) SendSubmitReviewResponse(resp mcp.SubmitReviewResponse) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.stopped || m.submitReview == nil {
		return
	}
	select {
	case m.submitReview.Resp <- resp:
	default:
	}
}

// Stop implements RunnerSession.
func (m *MockRunner) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.stopped {
		return
	}
	m.stopped = true

	// Close channels
	m.permission.Close()
	m.question.Close()
	m.planApproval.Close()
	m.createPR.Close()
	m.pushBranch.Close()
	m.getReviewComments.Close()
	m.commentIssue.Close()
	m.submitReview.Close()
	if m.responseChan != nil {
		// Only close if we control it
		select {
		case <-m.responseChan:
			// Already closed or has data
		default:
			close(m.responseChan)
		}
	}
}

// GetAllowedTools returns the current allowed tools list (for test assertions).
func (m *MockRunner) GetAllowedTools() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	tools := make([]string, len(m.allowedTools))
	copy(tools, m.allowedTools)
	return tools
}

// SetStreaming allows tests to manually set the streaming state.
func (m *MockRunner) SetStreaming(streaming bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.isStreaming = streaming
}

// CompleteStreaming signals that streaming is done and adds the assistant message.
func (m *MockRunner) CompleteStreaming(content string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.isStreaming = false
	m.sessionStarted = true
	if content != "" {
		m.messages = append(m.messages, Message{Role: "assistant", Content: content})
	}
}

// Interrupt implements RunnerSession.Interrupt for mock.
// In tests, this is a no-op since there's no real Claude process.
func (m *MockRunner) Interrupt() error {
	return nil
}

// Ensure MockRunner implements all runner interfaces at compile time.
var _ RunnerInterface = (*MockRunner)(nil)
var _ RunnerConfig = (*MockRunner)(nil)
var _ RunnerSession = (*MockRunner)(nil)
