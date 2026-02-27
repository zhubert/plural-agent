package claude

import (
	"github.com/zhubert/erg/internal/mcp"
)

// PermissionRequestChan returns the channel for receiving permission requests.
// Returns nil if the runner has been stopped to prevent reading from closed channel.
func (r *Runner) PermissionRequestChan() <-chan mcp.PermissionRequest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.stopped || r.mcp == nil {
		return nil
	}
	return r.mcp.Permission.Req
}

// SendPermissionResponse sends a response to a permission request.
// Safe to call even if the runner has been stopped - will silently drop the response.
func (r *Runner) SendPermissionResponse(resp mcp.PermissionResponse) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.stopped || r.mcp == nil || r.mcp.Permission == nil {
		r.log.Debug("SendPermissionResponse called on stopped runner, ignoring")
		return
	}

	// Send under lock to make check-and-send atomic, eliminating race window with Stop()
	ch := r.mcp.Permission.Resp
	select {
	case ch <- resp:
		// Success
	default:
		r.log.Debug("SendPermissionResponse channel full, ignoring")
	}
}

// QuestionRequestChan returns the channel for receiving question requests.
// Returns nil if the runner has been stopped to prevent reading from closed channel.
func (r *Runner) QuestionRequestChan() <-chan mcp.QuestionRequest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.stopped || r.mcp == nil {
		return nil
	}
	return r.mcp.Question.Req
}

// SendQuestionResponse sends a response to a question request.
// Safe to call even if the runner has been stopped - will silently drop the response.
func (r *Runner) SendQuestionResponse(resp mcp.QuestionResponse) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.stopped || r.mcp == nil || r.mcp.Question == nil {
		r.log.Debug("SendQuestionResponse called on stopped runner, ignoring")
		return
	}

	// Send under lock to make check-and-send atomic, eliminating race window with Stop()
	ch := r.mcp.Question.Resp
	select {
	case ch <- resp:
		// Success
	default:
		r.log.Debug("SendQuestionResponse channel full, ignoring")
	}
}

// PlanApprovalRequestChan returns the channel for receiving plan approval requests.
// Returns nil if the runner has been stopped to prevent reading from closed channel.
func (r *Runner) PlanApprovalRequestChan() <-chan mcp.PlanApprovalRequest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.stopped || r.mcp == nil {
		return nil
	}
	return r.mcp.PlanApproval.Req
}

// SendPlanApprovalResponse sends a response to a plan approval request.
// Safe to call even if the runner has been stopped - will silently drop the response.
func (r *Runner) SendPlanApprovalResponse(resp mcp.PlanApprovalResponse) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.stopped || r.mcp == nil || r.mcp.PlanApproval == nil {
		r.log.Debug("SendPlanApprovalResponse called on stopped runner, ignoring")
		return
	}

	// Send under lock to make check-and-send atomic, eliminating race window with Stop()
	ch := r.mcp.PlanApproval.Resp
	select {
	case ch <- resp:
		// Success
	default:
		r.log.Debug("SendPlanApprovalResponse channel full, ignoring")
	}
}

// CreatePRRequestChan returns the channel for receiving create PR requests.
func (r *Runner) CreatePRRequestChan() <-chan mcp.CreatePRRequest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.stopped || r.mcp == nil || r.mcp.CreatePR == nil {
		return nil
	}
	return r.mcp.CreatePR.Req
}

// SendCreatePRResponse sends a response to a create PR request.
func (r *Runner) SendCreatePRResponse(resp mcp.CreatePRResponse) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.stopped || r.mcp == nil || r.mcp.CreatePR == nil {
		return
	}

	// Send under lock to make check-and-send atomic, eliminating race window with Stop()
	ch := r.mcp.CreatePR.Resp
	select {
	case ch <- resp:
		// Success
	default:
		r.log.Debug("SendCreatePRResponse channel full, ignoring")
	}
}

// PushBranchRequestChan returns the channel for receiving push branch requests.
func (r *Runner) PushBranchRequestChan() <-chan mcp.PushBranchRequest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.stopped || r.mcp == nil || r.mcp.PushBranch == nil {
		return nil
	}
	return r.mcp.PushBranch.Req
}

// SendPushBranchResponse sends a response to a push branch request.
func (r *Runner) SendPushBranchResponse(resp mcp.PushBranchResponse) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.stopped || r.mcp == nil || r.mcp.PushBranch == nil {
		return
	}

	// Send under lock to make check-and-send atomic, eliminating race window with Stop()
	ch := r.mcp.PushBranch.Resp
	select {
	case ch <- resp:
		// Success
	default:
		r.log.Debug("SendPushBranchResponse channel full, ignoring")
	}
}

// GetReviewCommentsRequestChan returns the channel for receiving get review comments requests.
func (r *Runner) GetReviewCommentsRequestChan() <-chan mcp.GetReviewCommentsRequest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.stopped || r.mcp == nil || r.mcp.GetReviewComments == nil {
		return nil
	}
	return r.mcp.GetReviewComments.Req
}

// SendGetReviewCommentsResponse sends a response to a get review comments request.
func (r *Runner) SendGetReviewCommentsResponse(resp mcp.GetReviewCommentsResponse) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.stopped || r.mcp == nil || r.mcp.GetReviewComments == nil {
		return
	}

	// Send under lock to make check-and-send atomic, eliminating race window with Stop()
	ch := r.mcp.GetReviewComments.Resp
	select {
	case ch <- resp:
		// Success
	default:
		r.log.Debug("SendGetReviewCommentsResponse channel full, ignoring")
	}
}

// CommentIssueRequestChan returns the channel for receiving comment issue requests.
func (r *Runner) CommentIssueRequestChan() <-chan mcp.CommentIssueRequest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.stopped || r.mcp == nil || r.mcp.CommentIssue == nil {
		return nil
	}
	return r.mcp.CommentIssue.Req
}

// SendCommentIssueResponse sends a response to a comment issue request.
func (r *Runner) SendCommentIssueResponse(resp mcp.CommentIssueResponse) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.stopped || r.mcp == nil || r.mcp.CommentIssue == nil {
		return
	}

	ch := r.mcp.CommentIssue.Resp
	select {
	case ch <- resp:
		// Success
	default:
		r.log.Debug("SendCommentIssueResponse channel full, ignoring")
	}
}

// SubmitReviewRequestChan returns the channel for receiving submit review requests.
func (r *Runner) SubmitReviewRequestChan() <-chan mcp.SubmitReviewRequest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.stopped || r.mcp == nil || r.mcp.SubmitReview == nil {
		return nil
	}
	return r.mcp.SubmitReview.Req
}

// SendSubmitReviewResponse sends a response to a submit review request.
func (r *Runner) SendSubmitReviewResponse(resp mcp.SubmitReviewResponse) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.stopped || r.mcp == nil || r.mcp.SubmitReview == nil {
		return
	}

	ch := r.mcp.SubmitReview.Resp
	select {
	case ch <- resp:
		// Success
	default:
		r.log.Debug("SendSubmitReviewResponse channel full, ignoring")
	}
}
