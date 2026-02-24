package worker

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/zhubert/erg/internal/claude"
	"github.com/zhubert/erg/internal/config"
	"github.com/zhubert/erg/internal/mcp"
)

// SessionWorker manages a single autonomous session's lifecycle.
// It runs a goroutine with a select loop over all runner channels,
// replacing the TUI's Bubble Tea listener pattern.
type SessionWorker struct {
	host       Host
	sessionID  string
	session    *config.Session
	runner     claude.RunnerInterface
	initialMsg string
	turns      int
	startTime  time.Time

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once

	exitErr         error // Set when the worker exits due to an error
	apiErrorInStream bool  // Set when an API error is detected in streamed content

	// Per-session limit overrides (zero = use host defaults)
	overrideMaxTurns    int
	overrideMaxDuration time.Duration
}

// NewSessionWorker creates a new session worker.
func NewSessionWorker(host Host, sess *config.Session, runner claude.RunnerInterface, initialMsg string) *SessionWorker {
	return &SessionWorker{
		host:       host,
		sessionID:  sess.ID,
		session:    sess,
		runner:     runner,
		initialMsg: initialMsg,
		startTime:  time.Now(),
		done:       make(chan struct{}),
	}
}

// NewDoneWorker creates a SessionWorker that is already done.
// Used by tests in other packages that need a completed worker.
func NewDoneWorker() *SessionWorker {
	w := &SessionWorker{
		done: make(chan struct{}),
	}
	close(w.done)
	return w
}

// NewDoneWorkerWithError creates a SessionWorker that is already done with an error.
// Used by tests in other packages that need a completed-with-error worker.
func NewDoneWorkerWithError(err error) *SessionWorker {
	w := &SessionWorker{
		done:    make(chan struct{}),
		exitErr: err,
	}
	close(w.done)
	return w
}

// Turns returns the number of completed turns.
func (w *SessionWorker) Turns() int {
	return w.turns
}

// SessionID returns the worker's session ID.
func (w *SessionWorker) SessionID() string {
	return w.sessionID
}

// InitialMsg returns the initial message.
func (w *SessionWorker) InitialMsg() string {
	return w.initialMsg
}

// SetTurns sets the number of completed turns (for testing).
func (w *SessionWorker) SetTurns(n int) {
	w.turns = n
}

// SetStartTime sets the worker start time (for testing).
func (w *SessionWorker) SetStartTime(t time.Time) {
	w.startTime = t
}

// SetLimits overrides the per-session turn and duration limits.
// Must be called before Start. Zero values fall back to host defaults.
func (w *SessionWorker) SetLimits(maxTurns int, maxDuration time.Duration) {
	w.overrideMaxTurns = maxTurns
	w.overrideMaxDuration = maxDuration
}

// CheckLimits returns true if the session has hit its turn or duration limit.
func (w *SessionWorker) CheckLimits() bool {
	return w.checkLimits()
}

// HasActiveChildren checks if any child sessions are still running.
func (w *SessionWorker) HasActiveChildren() bool {
	return w.hasActiveChildren()
}

// ExitError returns the error that caused the worker to exit, or nil if it completed normally.
func (w *SessionWorker) ExitError() error {
	return w.exitErr
}

// Start begins the worker's goroutine.
func (w *SessionWorker) Start(ctx context.Context) {
	w.ctx, w.cancel = context.WithCancel(ctx)
	go w.run()
}

// Cancel requests the worker to stop.
func (w *SessionWorker) Cancel() {
	if w.cancel != nil {
		w.cancel()
	}
}

// Wait blocks until the worker has finished.
func (w *SessionWorker) Wait() {
	<-w.done
}

// Done returns true if the worker has finished.
func (w *SessionWorker) Done() bool {
	select {
	case <-w.done:
		return true
	default:
		return false
	}
}

// run is the main worker loop.
func (w *SessionWorker) run() {
	defer w.once.Do(func() { close(w.done) })

	log := w.host.Logger().With("sessionID", w.sessionID, "branch", w.session.Branch)
	log.Info("worker started")

	// Send initial message
	content := []claude.ContentBlock{{Type: claude.ContentTypeText, Text: w.initialMsg}}
	responseChan := w.runner.SendContent(w.ctx, content)

	for {
		if err := w.processOneResponse(responseChan); err != nil {
			log.Info("worker stopping", "reason", err.Error())
			w.exitErr = err
			return
		}

		// Check if the response contained an API error (e.g., 500 errors
		// emitted as streamed text rather than as chunk.Error)
		if w.apiErrorInStream {
			w.exitErr = fmt.Errorf("API error detected in response stream")
			log.Warn("worker stopping due to API error in stream")
			return
		}

		// Check limits
		if w.checkLimits() {
			log.Warn("autonomous limit reached", "turns", w.turns)
			return
		}

		// Check for pending messages (e.g., child completion notifications)
		pendingMsg := w.host.SessionManager().StateManager().GetPendingMessage(w.sessionID)
		if pendingMsg != "" {
			log.Debug("sending pending message")
			content := []claude.ContentBlock{{Type: claude.ContentTypeText, Text: pendingMsg}}
			responseChan = w.runner.SendContent(w.ctx, content)
			continue
		}

		// For supervisor sessions, check if children are still active
		if w.session.IsSupervisor && w.hasActiveChildren() {
			log.Debug("supervisor has active children, waiting...")
			// Wait a bit then check again for pending messages
			select {
			case <-w.ctx.Done():
				return
			case <-time.After(5 * time.Second):
				continue
			}
		}

		// Session completed
		log.Info("session completed")
		w.handleCompletion()
		return
	}
}

// processOneResponse processes a single streaming response from Claude.
// It blocks until the response is done or an error occurs.
// Returns nil when the response completes normally, or an error to stop the worker.
func (w *SessionWorker) processOneResponse(responseChan <-chan claude.ResponseChunk) error {
	log := w.host.Logger().With("sessionID", w.sessionID)

	for {
		select {
		case <-w.ctx.Done():
			return fmt.Errorf("context cancelled")

		case chunk, ok := <-responseChan:
			if !ok {
				// Channel closed = done
				w.turns++
				w.handleDone()
				return nil
			}

			if chunk.Error != nil {
				log.Error("Claude error", "error", chunk.Error)
				return fmt.Errorf("claude error: %w", chunk.Error)
			}

			if chunk.Done {
				w.turns++
				w.handleDone()
				return nil
			}

			// Log streaming progress periodically
			w.handleStreaming(chunk)

		case req, ok := <-w.runner.PermissionRequestChan():
			if !ok {
				continue
			}
			// Should not happen in containerized mode, but auto-deny
			log.Warn("unexpected permission request in containerized mode", "tool", req.Tool)
			w.runner.SendPermissionResponse(mcp.PermissionResponse{
				ID:      req.ID,
				Allowed: false,
				Message: "Permission auto-denied in headless agent mode",
			})

		case req, ok := <-w.runner.QuestionRequestChan():
			if !ok {
				continue
			}
			w.autoRespondQuestion(req)

		case req, ok := <-w.runner.PlanApprovalRequestChan():
			if !ok {
				continue
			}
			w.autoApprovePlan(req)

		case req, ok := <-w.safeChanCreateChild():
			if !ok {
				continue
			}
			w.handleCreateChild(req)

		case req, ok := <-w.safeChanListChildren():
			if !ok {
				continue
			}
			w.handleListChildren(req)

		case req, ok := <-w.safeChanMergeChild():
			if !ok {
				continue
			}
			w.handleMergeChild(req)

		case req, ok := <-w.safeChanCreatePR():
			if !ok {
				continue
			}
			w.handleCreatePR(req)

		case req, ok := <-w.safeChanPushBranch():
			if !ok {
				continue
			}
			w.handlePushBranch(req)

		case req, ok := <-w.safeChanGetReviewComments():
			if !ok {
				continue
			}
			w.handleGetReviewComments(req)
		}
	}
}

// Safe channel accessors that return nil channels (which block forever in select) when not available.
func (w *SessionWorker) safeChanCreateChild() <-chan mcp.CreateChildRequest {
	ch := w.runner.CreateChildRequestChan()
	if ch == nil {
		return nil
	}
	return ch
}

func (w *SessionWorker) safeChanListChildren() <-chan mcp.ListChildrenRequest {
	ch := w.runner.ListChildrenRequestChan()
	if ch == nil {
		return nil
	}
	return ch
}

func (w *SessionWorker) safeChanMergeChild() <-chan mcp.MergeChildRequest {
	ch := w.runner.MergeChildRequestChan()
	if ch == nil {
		return nil
	}
	return ch
}

func (w *SessionWorker) safeChanCreatePR() <-chan mcp.CreatePRRequest {
	ch := w.runner.CreatePRRequestChan()
	if ch == nil {
		return nil
	}
	return ch
}

func (w *SessionWorker) safeChanPushBranch() <-chan mcp.PushBranchRequest {
	ch := w.runner.PushBranchRequestChan()
	if ch == nil {
		return nil
	}
	return ch
}

func (w *SessionWorker) safeChanGetReviewComments() <-chan mcp.GetReviewCommentsRequest {
	ch := w.runner.GetReviewCommentsRequestChan()
	if ch == nil {
		return nil
	}
	return ch
}

// handleStreaming logs streaming progress and records spend from final stats chunks.
func (w *SessionWorker) handleStreaming(chunk claude.ResponseChunk) {
	if chunk.Type == claude.ChunkTypeText && chunk.Content != "" {
		// Detect API errors emitted as text content (e.g., 500 errors from
		// the Anthropic API that the runner surfaces as text rather than
		// as chunk.Error).
		if isAPIErrorContent(chunk.Content) || isSessionNotFoundContent(chunk.Content) {
			w.apiErrorInStream = true
		}

		// Log first 100 chars of content for debugging
		preview := chunk.Content
		if len(preview) > 100 {
			preview = preview[:100] + "..."
		}
		w.host.Logger().Debug("streaming", "sessionID", w.sessionID, "content", preview)
	}

	// Record spend from the final stats chunk (identified by DurationMs > 0,
	// which is only set on the result message, not on intermediate streaming chunks).
	if chunk.Type == claude.ChunkTypeStreamStats && chunk.Stats != nil && chunk.Stats.DurationMs > 0 {
		s := chunk.Stats
		// Total input tokens = direct input + cache creation + cache read.
		// s.InputTokens alone is typically tiny (non-cached direct input only).
		// The bulk of token usage is in cache creation and cache read, which
		// must be included for an accurate count.
		totalInputTokens := s.InputTokens + s.CacheCreationTokens + s.CacheReadTokens
		w.host.RecordSpend(s.TotalCostUSD, s.OutputTokens, totalInputTokens)
		w.host.Logger().Info("session spend recorded",
			"sessionID", w.sessionID,
			"costUSD", s.TotalCostUSD,
			"outputTokens", s.OutputTokens,
			"inputTokens", totalInputTokens,
			"inputDirect", s.InputTokens,
			"cacheCreation", s.CacheCreationTokens,
			"cacheRead", s.CacheReadTokens,
		)
	}
}

// isAPIErrorContent returns true if the streamed text content looks like an
// API error response (e.g., "API Error: 500 {"type":"error",...}").
// We require both the "API Error:" prefix AND a JSON error structure
// to avoid false positives on normal text that happens to mention API errors.
func isAPIErrorContent(content string) bool {
	if !strings.HasPrefix(content, "API Error:") && !strings.HasPrefix(content, "API error:") {
		return false
	}
	return strings.Contains(content, `"type":"error"`) ||
		strings.Contains(content, `"type": "error"`)
}

// isSessionNotFoundContent returns true if the streamed text content indicates
// the Claude conversation could not be found (e.g., after a daemon restart when
// the session ID is stale). This is a fatal error — the worker cannot resume.
func isSessionNotFoundContent(content string) bool {
	return strings.Contains(content, "No conversation found with session ID:")
}

// handleDone handles completion of a streaming response.
func (w *SessionWorker) handleDone() {
	log := w.host.Logger().With("sessionID", w.sessionID, "turn", w.turns)
	log.Debug("response completed")

	// Mark session as started if needed
	sess := w.host.Config().GetSession(w.sessionID)
	if sess != nil && w.runner.SessionStarted() && !sess.Started {
		w.host.Config().MarkSessionStarted(w.sessionID)
		if err := w.host.Config().Save(); err != nil {
			log.Error("failed to save config after marking started", "error", err)
		}
	}

	// Save messages
	w.host.SaveRunnerMessages(w.sessionID, w.runner)
}

// handleCompletion handles full session completion (all turns done, no pending work).
// The daemon's workflow engine handles PR creation, merge, and cleanup.
func (w *SessionWorker) handleCompletion() {
	sess := w.host.Config().GetSession(w.sessionID)
	if sess == nil {
		return
	}

	// Notify supervisor if this is a child session
	if sess.SupervisorID != "" {
		w.notifySupervisor(sess.SupervisorID, true)
	}
}

// checkLimits returns true if the session has hit its turn or duration limit.
func (w *SessionWorker) checkLimits() bool {
	maxTurns := w.host.MaxTurns()
	if w.overrideMaxTurns > 0 {
		maxTurns = w.overrideMaxTurns
	}

	maxDuration := time.Duration(w.host.MaxDuration()) * time.Minute
	if w.overrideMaxDuration > 0 {
		maxDuration = w.overrideMaxDuration
	}

	if w.turns >= maxTurns {
		w.host.Logger().Warn("turn limit reached",
			"sessionID", w.sessionID,
			"turns", w.turns,
			"max", maxTurns,
		)
		return true
	}

	if time.Since(w.startTime) >= maxDuration {
		w.host.Logger().Warn("duration limit reached",
			"sessionID", w.sessionID,
			"elapsed", time.Since(w.startTime),
			"max", maxDuration,
		)
		return true
	}

	return false
}

// autoRespondQuestion automatically responds to questions by selecting the first option.
func (w *SessionWorker) autoRespondQuestion(req mcp.QuestionRequest) {
	log := w.host.Logger().With("sessionID", w.sessionID)
	log.Info("auto-responding to question")

	answers := make(map[string]string)
	for _, q := range req.Questions {
		if len(q.Options) > 0 {
			answers[q.Question] = q.Options[0].Label
		} else {
			answers[q.Question] = "Continue as you see fit"
		}
	}

	w.runner.SendQuestionResponse(mcp.QuestionResponse{
		ID:      req.ID,
		Answers: answers,
	})
}

// autoApprovePlan automatically approves plans.
func (w *SessionWorker) autoApprovePlan(req mcp.PlanApprovalRequest) {
	log := w.host.Logger().With("sessionID", w.sessionID)
	log.Info("auto-approving plan")

	w.runner.SendPlanApprovalResponse(mcp.PlanApprovalResponse{
		ID:       req.ID,
		Approved: true,
	})
}

// handleCreateChild handles a create_child_session MCP tool call.
func (w *SessionWorker) handleCreateChild(req mcp.CreateChildRequest) {
	log := w.host.Logger().With("sessionID", w.sessionID)

	childInfo, err := w.host.CreateChildSession(w.ctx, w.sessionID, req.Task)
	if err != nil {
		log.Error("failed to create child session", "error", err)
		w.runner.SendCreateChildResponse(mcp.CreateChildResponse{
			ID:    req.ID,
			Error: fmt.Sprintf("Failed to create child session: %v", err),
		})
		return
	}

	w.runner.SendCreateChildResponse(mcp.CreateChildResponse{
		ID:      req.ID,
		Success: true,
		ChildID: childInfo.ID,
		Branch:  childInfo.Branch,
	})
}

// handleListChildren handles a list_child_sessions MCP tool call.
func (w *SessionWorker) handleListChildren(req mcp.ListChildrenRequest) {
	children := w.host.Config().GetChildSessions(w.sessionID)
	var childInfos []mcp.ChildSessionInfo
	for _, child := range children {
		status := "idle"
		if w.host.IsWorkerRunning(child.ID) {
			status = "running"
		} else if child.MergedToParent {
			status = "merged"
		} else if child.PRCreated {
			status = "pr_created"
		}

		childInfos = append(childInfos, mcp.ChildSessionInfo{
			ID:     child.ID,
			Branch: child.Branch,
			Status: status,
		})
	}

	w.runner.SendListChildrenResponse(mcp.ListChildrenResponse{
		ID:       req.ID,
		Children: childInfos,
	})
}

// handleMergeChild handles a merge_child_to_parent MCP tool call.
func (w *SessionWorker) handleMergeChild(req mcp.MergeChildRequest) {
	log := w.host.Logger().With("sessionID", w.sessionID)

	sess := w.host.Config().GetSession(w.sessionID)
	if sess == nil {
		w.runner.SendMergeChildResponse(mcp.MergeChildResponse{
			ID:    req.ID,
			Error: "Supervisor session not found",
		})
		return
	}

	childSess := w.host.Config().GetSession(req.ChildSessionID)
	if childSess == nil {
		w.runner.SendMergeChildResponse(mcp.MergeChildResponse{
			ID:    req.ID,
			Error: "Child session not found",
		})
		return
	}

	if childSess.SupervisorID != w.sessionID {
		w.runner.SendMergeChildResponse(mcp.MergeChildResponse{
			ID:    req.ID,
			Error: "Child session does not belong to this supervisor",
		})
		return
	}

	if childSess.MergedToParent {
		w.runner.SendMergeChildResponse(mcp.MergeChildResponse{
			ID:    req.ID,
			Error: "Child session already merged",
		})
		return
	}

	log.Info("merging child to parent", "childID", childSess.ID, "childBranch", childSess.Branch)

	ctx, cancel := context.WithTimeout(w.ctx, 2*time.Minute)
	defer cancel()

	resultCh := w.host.GitService().MergeToParent(ctx, childSess.WorkTree, childSess.Branch, sess.WorkTree, sess.Branch, "")

	var lastErr error
	for result := range resultCh {
		if result.Error != nil {
			lastErr = result.Error
		}
	}

	if lastErr != nil {
		log.Error("merge child failed", "childID", childSess.ID, "error", lastErr)
		w.runner.SendMergeChildResponse(mcp.MergeChildResponse{
			ID:    req.ID,
			Error: lastErr.Error(),
		})
		return
	}

	// Mark child as merged
	w.host.Config().MarkSessionMergedToParent(childSess.ID)
	if err := w.host.Config().Save(); err != nil {
		log.Error("failed to save config after merge", "error", err)
	}

	w.runner.SendMergeChildResponse(mcp.MergeChildResponse{
		ID:      req.ID,
		Success: true,
		Message: fmt.Sprintf("Successfully merged %s into %s", childSess.Branch, sess.Branch),
	})
}

// handleCreatePR handles a create_pr MCP tool call.
// The daemon's workflow handles PR creation, so we reject Claude's attempt.
func (w *SessionWorker) handleCreatePR(req mcp.CreatePRRequest) {
	log := w.host.Logger().With("sessionID", w.sessionID)
	log.Warn("rejecting create_pr — PR creation is managed by the workflow")
	w.runner.SendCreatePRResponse(mcp.CreatePRResponse{
		ID:    req.ID,
		Error: "PR creation is managed by the workflow. Do not create PRs directly — just make your code changes and commit them.",
	})
}

// handlePushBranch handles a push_branch MCP tool call.
// The daemon's workflow handles pushing, so we reject Claude's attempt.
func (w *SessionWorker) handlePushBranch(req mcp.PushBranchRequest) {
	log := w.host.Logger().With("sessionID", w.sessionID)
	log.Warn("rejecting push_branch — branch pushing is managed by the workflow")
	w.runner.SendPushBranchResponse(mcp.PushBranchResponse{
		ID:    req.ID,
		Error: "Branch pushing is managed by the workflow. Do not push directly — just make your code changes and commit them.",
	})
}

// handleGetReviewComments handles a get_review_comments MCP tool call.
func (w *SessionWorker) handleGetReviewComments(req mcp.GetReviewCommentsRequest) {
	log := w.host.Logger().With("sessionID", w.sessionID)
	sess := w.host.Config().GetSession(w.sessionID)
	if sess == nil {
		w.runner.SendGetReviewCommentsResponse(mcp.GetReviewCommentsResponse{
			ID:    req.ID,
			Error: "Session not found",
		})
		return
	}

	log.Info("fetching review comments via MCP tool", "branch", sess.Branch)

	ctx, cancel := context.WithTimeout(w.ctx, 30*time.Second)
	defer cancel()

	comments, err := w.host.GitService().FetchPRReviewComments(ctx, sess.RepoPath, sess.Branch)
	if err != nil {
		w.runner.SendGetReviewCommentsResponse(mcp.GetReviewCommentsResponse{
			ID:    req.ID,
			Error: fmt.Sprintf("Failed to fetch review comments: %v", err),
		})
		return
	}

	// Filter out our own transcript comments — they aren't review feedback.
	comments = FilterTranscriptComments(comments)

	mcpComments := make([]mcp.ReviewComment, len(comments))
	for i, c := range comments {
		mcpComments[i] = mcp.ReviewComment{
			Author: c.Author,
			Body:   c.Body,
			Path:   c.Path,
			Line:   c.Line,
			URL:    c.URL,
		}
	}

	w.runner.SendGetReviewCommentsResponse(mcp.GetReviewCommentsResponse{
		ID:       req.ID,
		Success:  true,
		Comments: mcpComments,
	})
}

// hasActiveChildren checks if any child sessions are still running.
func (w *SessionWorker) hasActiveChildren() bool {
	children := w.host.Config().GetChildSessions(w.sessionID)
	for _, child := range children {
		if w.host.IsWorkerRunning(child.ID) {
			return true
		}
	}
	return false
}

// notifySupervisor sends a status update to the supervisor session.
func (w *SessionWorker) notifySupervisor(supervisorID string, testsPassed bool) {
	childSess := w.host.Config().GetSession(w.sessionID)
	if childSess == nil {
		return
	}

	status := "completed successfully"
	if !testsPassed {
		status = "completed (tests failed)"
	}

	allChildren := w.host.Config().GetChildSessions(supervisorID)
	allDone := true
	completedCount := 0
	for _, child := range allChildren {
		if w.host.IsWorkerRunning(child.ID) {
			allDone = false
		} else {
			completedCount++
		}
	}

	var prompt string
	sessionName := childSess.Branch
	if allDone {
		prompt = fmt.Sprintf("Child session '%s' %s.\n\nAll %d child sessions have completed. You should now review the results, merge children to parent with `merge_child_to_parent`, and create a PR with `push_branch` and `create_pr`.",
			sessionName, status, len(allChildren))
	} else {
		prompt = fmt.Sprintf("Child session '%s' %s. (%d/%d children completed)\n\nWait for all children to complete before merging or creating PRs.",
			sessionName, status, completedCount, len(allChildren))
	}

	// Queue the message for the supervisor
	state := w.host.SessionManager().StateManager().GetOrCreate(supervisorID)
	state.SetPendingMsg(prompt)
}

