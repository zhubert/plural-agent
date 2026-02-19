package worker

import (
	"context"
	"time"

	"github.com/zhubert/plural-core/config"
	"github.com/zhubert/plural-core/git"
)

const (
	maxAutoMergePollAttempts = 120 // ~2 hours at 60s intervals
	autoMergePollInterval   = 60 * time.Second
)

// RunAutoMerge runs the auto-merge state machine for a session using the Host interface.
// It polls for review approval and CI status, then merges the PR.
// This is a blocking function intended to run in a goroutine.
// The ctx parameter allows the caller to cancel polling (e.g., on daemon shutdown).
func RunAutoMerge(ctx context.Context, h Host, sessionID string) {
	log := h.Logger().With("sessionID", sessionID, "component", "auto-merge")
	sess := h.Config().GetSession(sessionID)
	if sess == nil {
		return
	}

	log.Info("starting auto-merge polling", "branch", sess.Branch)

	for attempt := 1; attempt <= maxAutoMergePollAttempts; attempt++ {
		// Wait before check to give CI/reviews time
		select {
		case <-ctx.Done():
			log.Info("auto-merge cancelled", "branch", sess.Branch)
			return
		case <-time.After(autoMergePollInterval):
		}

		// Refresh session in case it was updated
		sess = h.Config().GetSession(sessionID)
		if sess == nil {
			log.Warn("session disappeared during auto-merge polling")
			return
		}

		// Step 1: Check for unaddressed review comments
		action := CheckAndAddressComments(ctx, h, sessionID, sess, attempt)
		switch action {
		case MergeActionContinue:
			continue // Poll again
		case MergeActionStop:
			return
		case MergeActionProceed:
			// Fall through to review/CI checks
		}

		// Step 2: Check review approval
		action = CheckReviewApproval(ctx, h, sessionID, sess, attempt)
		switch action {
		case MergeActionContinue:
			continue
		case MergeActionStop:
			return
		case MergeActionProceed:
			// Fall through to CI check
		}

		// Step 3: Check CI status
		action = CheckCIAndMerge(ctx, h, sessionID, sess, attempt)
		switch action {
		case MergeActionContinue:
			continue
		case MergeActionStop:
			return
		case MergeActionProceed:
			// Merge succeeded, done
			return
		}
	}

	log.Warn("auto-merge polling exhausted all attempts", "branch", sess.Branch)
}

// MergeAction represents the result of a merge check step.
type MergeAction int

const (
	MergeActionContinue MergeAction = iota // Keep polling
	MergeActionStop                        // Stop polling (failure or done)
	MergeActionProceed                     // Move to next step
)

// CheckAndAddressComments checks for unaddressed PR review comments.
func CheckAndAddressComments(ctx context.Context, h Host, sessionID string, sess *config.Session, attempt int) MergeAction {
	log := h.Logger().With("sessionID", sessionID)

	opCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	results, err := h.GitService().GetBatchPRStatesWithComments(opCtx, sess.RepoPath, []string{sess.Branch})
	if err != nil {
		log.Warn("failed to check PR comment count", "error", err)
		return MergeActionProceed // Don't block on comment check failure
	}

	result, ok := results[sess.Branch]
	if !ok {
		return MergeActionProceed
	}

	if result.CommentCount > sess.PRCommentsAddressedCount {
		log.Info("unaddressed review comments detected",
			"addressed", sess.PRCommentsAddressedCount,
			"current", result.CommentCount,
		)

		// Mark these comments as addressed
		h.Config().UpdateSessionPRCommentsAddressedCount(sessionID, result.CommentCount)

		// Fetch and send comments to Claude
		if addressComments(ctx, h, sessionID, sess) {
			// Comments were sent to Claude — the worker's main loop will handle
			// the response and re-trigger auto-merge via handleCompletion
			return MergeActionStop
		}
	}

	return MergeActionProceed
}

// addressComments fetches PR comments and queues them for Claude.
// Returns true if comments were queued successfully.
func addressComments(ctx context.Context, h Host, sessionID string, sess *config.Session) bool {
	log := h.Logger().With("sessionID", sessionID)

	opCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	comments, err := h.GitService().FetchPRReviewComments(opCtx, sess.RepoPath, sess.Branch)
	if err != nil {
		log.Warn("failed to fetch PR review comments", "error", err)
		return false
	}

	if len(comments) == 0 {
		return false
	}

	prompt := FormatPRCommentsPrompt(comments)
	state := h.SessionManager().StateManager().GetOrCreate(sessionID)
	state.SetPendingMsg(prompt)

	log.Info("queued review comments for Claude", "commentCount", len(comments))
	return true
}

// CheckReviewApproval checks the PR review status.
func CheckReviewApproval(ctx context.Context, h Host, sessionID string, sess *config.Session, attempt int) MergeAction {
	log := h.Logger().With("sessionID", sessionID)

	opCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	reviewDecision, err := h.GitService().CheckPRReviewDecision(opCtx, sess.RepoPath, sess.Branch)
	if err != nil {
		log.Warn("failed to check PR review decision", "error", err)
	}

	switch reviewDecision {
	case git.ReviewChangesRequested:
		log.Info("changes requested, waiting for re-review", "branch", sess.Branch)
		return MergeActionContinue

	case git.ReviewNone:
		if attempt >= maxAutoMergePollAttempts {
			log.Warn("timed out waiting for review", "branch", sess.Branch)
			return MergeActionStop
		}
		if attempt == 1 {
			log.Info("waiting for review", "branch", sess.Branch)
		}
		return MergeActionContinue

	case git.ReviewApproved:
		return MergeActionProceed
	}

	return MergeActionProceed
}

// CheckCIAndMerge checks CI status and merges if passing.
func CheckCIAndMerge(ctx context.Context, h Host, sessionID string, sess *config.Session, attempt int) MergeAction {
	log := h.Logger().With("sessionID", sessionID)

	opCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	ciStatus, err := h.GitService().CheckPRChecks(opCtx, sess.RepoPath, sess.Branch)
	if err != nil {
		log.Warn("failed to check CI status", "error", err)
		ciStatus = git.CIStatusPending
	}

	switch ciStatus {
	case git.CIStatusPassing, git.CIStatusNone:
		log.Info("review approved, CI passed, merging PR", "branch", sess.Branch, "ciStatus", ciStatus)
		return DoMerge(ctx, h, sessionID, sess)

	case git.CIStatusFailing:
		log.Warn("CI checks failed, skipping auto-merge", "branch", sess.Branch)
		return MergeActionStop

	case git.CIStatusPending:
		if attempt >= maxAutoMergePollAttempts {
			log.Warn("timed out waiting for CI", "branch", sess.Branch)
			return MergeActionStop
		}
		log.Debug("CI checks still pending", "branch", sess.Branch, "attempt", attempt)
		return MergeActionContinue
	}

	return MergeActionContinue
}

// DoMerge performs the actual PR merge.
func DoMerge(ctx context.Context, h Host, sessionID string, sess *config.Session) MergeAction {
	log := h.Logger().With("sessionID", sessionID)

	opCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	// Don't delete branch - it will be deleted during session cleanup
	err := h.GitService().MergePR(opCtx, sess.RepoPath, sess.Branch, false, h.MergeMethod())
	if err != nil {
		log.Error("auto-merge failed", "error", err)
		return MergeActionStop
	}

	log.Info("auto-merge successful", "branch", sess.Branch)

	// Mark as merged
	h.Config().MarkSessionPRMerged(sessionID)
	if err := h.Config().Save(); err != nil {
		log.Error("failed to save config after merge", "error", err)
	}

	// Auto-cleanup if enabled
	if h.Config().GetAutoCleanupMerged() {
		if err := h.CleanupSession(ctx, sessionID); err != nil {
			log.Error("auto-cleanup failed", "error", err)
		}
	}

	return MergeActionProceed
}
