package daemon

import (
	"context"
	"time"

	"github.com/zhubert/plural-core/git"
	"github.com/zhubert/erg/internal/workflow"
)

// EventChecker implements workflow.EventChecker for the daemon.
type EventChecker struct {
	daemon *Daemon
}

// NewEventChecker creates a new event checker backed by the daemon.
func NewEventChecker(d *Daemon) *EventChecker {
	return &EventChecker{daemon: d}
}

// CheckEvent checks whether an event has fired for a work item.
func (c *EventChecker) CheckEvent(ctx context.Context, event string, params *workflow.ParamHelper, item *workflow.WorkItemView) (bool, map[string]any, error) {
	switch event {
	case "pr.reviewed":
		return c.checkPRReviewed(ctx, params, item)
	case "ci.complete":
		return c.checkCIComplete(ctx, params, item)
	case "pr.mergeable":
		return c.checkPRMergeable(ctx, params, item)
	default:
		return false, nil, nil
	}
}

// checkPRReviewed checks for PR review status.
func (c *EventChecker) checkPRReviewed(ctx context.Context, params *workflow.ParamHelper, item *workflow.WorkItemView) (bool, map[string]any, error) {
	d := c.daemon
	log := d.logger.With("workItem", item.ID, "branch", item.Branch, "event", "pr.reviewed")

	sess := d.config.GetSession(item.SessionID)
	if sess == nil {
		log.Warn("session not found for work item")
		return false, nil, nil
	}

	pollCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	// Check if PR was closed
	prState, err := d.gitService.GetPRState(pollCtx, sess.RepoPath, item.Branch)
	if err != nil {
		log.Debug("failed to check PR state", "error", err)
		return false, nil, nil
	}

	if prState == git.PRStateClosed {
		log.Info("PR was closed, marking as failed")
		return false, map[string]any{"pr_closed": true}, nil
	}

	if prState == git.PRStateMerged {
		log.Info("PR was merged externally")
		return true, map[string]any{"pr_merged_externally": true}, nil
	}

	// If we're currently addressing feedback or pushing, don't poll for more
	if item.Phase == "addressing_feedback" || item.Phase == "pushing" {
		return false, nil, nil
	}

	workItem := d.state.GetWorkItem(item.ID)
	if workItem == nil {
		return false, nil, nil
	}

	// Check for new review comments
	results, err := d.gitService.GetBatchPRStatesWithComments(pollCtx, sess.RepoPath, []string{item.Branch})
	if err != nil {
		log.Debug("failed to check PR comments", "error", err)
		return false, nil, nil
	}

	result, ok := results[item.Branch]
	if !ok {
		return false, nil, nil
	}

	if result.CommentCount > item.CommentsAddressed {
		log.Debug("new comments detected, checking for review feedback",
			"addressed", item.CommentsAddressed,
			"current", result.CommentCount,
		)

		autoAddress := params.Bool("auto_address", true)
		maxRounds := params.Int("max_feedback_rounds", 3)

		if !autoAddress {
			log.Debug("auto_address disabled, skipping feedback")
			return false, nil, nil
		}

		if item.FeedbackRounds >= maxRounds {
			log.Warn("max feedback rounds reached",
				"rounds", item.FeedbackRounds,
				"max", maxRounds,
			)
			return false, nil, nil
		}

		// Check concurrency before starting feedback
		if d.activeSlotCount() >= d.getMaxConcurrent() {
			log.Debug("no concurrency slot available for feedback, deferring")
			return false, nil, nil
		}

		// Start addressing feedback (this is an internal sub-action of the wait state)
		d.addressFeedback(ctx, workItem)
		return false, nil, nil
	}

	// Check review decision
	reviewDecision, err := d.gitService.CheckPRReviewDecision(pollCtx, sess.RepoPath, item.Branch)
	if err != nil {
		log.Debug("failed to check review decision", "error", err)
		return false, nil, nil
	}

	if reviewDecision == git.ReviewApproved {
		log.Info("PR approved")
		return true, map[string]any{"review_approved": true}, nil
	}

	return false, nil, nil
}

// checkCIComplete checks CI status and returns true when CI has passed.
func (c *EventChecker) checkCIComplete(ctx context.Context, params *workflow.ParamHelper, item *workflow.WorkItemView) (bool, map[string]any, error) {
	d := c.daemon
	log := d.logger.With("workItem", item.ID, "branch", item.Branch, "event", "ci.complete")

	sess := d.config.GetSession(item.SessionID)
	if sess == nil {
		log.Warn("session not found for work item")
		return false, nil, nil
	}

	pollCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	ciStatus, err := d.gitService.CheckPRChecks(pollCtx, sess.RepoPath, item.Branch)
	if err != nil {
		log.Debug("CI checks not available yet", "error", err)
		return false, nil, nil
	}

	switch ciStatus {
	case git.CIStatusPassing, git.CIStatusNone:
		if !d.autoMerge {
			log.Info("CI passed but auto-merge disabled")
			return false, nil, nil
		}
		log.Info("CI passed")
		return true, map[string]any{"ci_passed": true}, nil

	case git.CIStatusFailing:
		onFailure := params.String("on_failure", "retry")
		log.Warn("CI failed", "on_failure", onFailure)

		switch onFailure {
		case "abandon":
			return false, map[string]any{"ci_failed": true, "ci_action": "abandon"}, nil
		case "notify":
			return false, map[string]any{"ci_failed": true, "ci_action": "notify"}, nil
		case "fix":
			log.Warn("CI failed, advancing for fix", "on_failure", onFailure)
			return true, map[string]any{"ci_passed": false, "ci_failed": true}, nil
		default: // "retry"
			return false, map[string]any{"ci_failed": true, "ci_action": "retry"}, nil
		}

	case git.CIStatusPending:
		log.Debug("CI still pending")
	}

	return false, nil, nil
}

// checkPRMergeable checks if the PR is mergeable — approved review AND CI passing.
// This is a convenience event that combines pr.reviewed (approval) and ci.complete in one check.
func (c *EventChecker) checkPRMergeable(ctx context.Context, params *workflow.ParamHelper, item *workflow.WorkItemView) (bool, map[string]any, error) {
	d := c.daemon
	log := d.logger.With("workItem", item.ID, "branch", item.Branch, "event", "pr.mergeable")

	sess := d.config.GetSession(item.SessionID)
	if sess == nil {
		log.Warn("session not found for work item")
		return false, nil, nil
	}

	pollCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Check PR state
	prState, err := d.gitService.GetPRState(pollCtx, sess.RepoPath, item.Branch)
	if err != nil {
		log.Debug("failed to check PR state", "error", err)
		return false, nil, nil
	}

	if prState == git.PRStateClosed {
		log.Info("PR was closed")
		return false, map[string]any{"pr_closed": true}, nil
	}

	if prState == git.PRStateMerged {
		log.Info("PR was merged externally")
		return true, map[string]any{"pr_merged_externally": true}, nil
	}

	// Check review approval
	reviewDecision, err := d.gitService.CheckPRReviewDecision(pollCtx, sess.RepoPath, item.Branch)
	if err != nil {
		log.Debug("failed to check review decision", "error", err)
		return false, nil, nil
	}

	requireReview := params.Bool("require_review", true)
	if requireReview && reviewDecision != git.ReviewApproved {
		log.Debug("PR not approved yet", "review", reviewDecision)
		return false, nil, nil
	}

	// Check CI status
	requireCI := params.Bool("require_ci", true)
	if requireCI {
		ciStatus, err := d.gitService.CheckPRChecks(pollCtx, sess.RepoPath, item.Branch)
		if err != nil {
			log.Debug("CI checks not available yet", "error", err)
			return false, nil, nil
		}
		if ciStatus == git.CIStatusPending {
			log.Debug("CI still pending")
			return false, nil, nil
		}
		if ciStatus == git.CIStatusFailing {
			log.Warn("CI failed")
			return false, map[string]any{"ci_failed": true}, nil
		}
	}

	log.Info("PR is mergeable")
	return true, map[string]any{
		"review_approved": true,
		"ci_passed":       true,
	}, nil
}
