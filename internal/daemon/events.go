package daemon

import (
	"context"
	"regexp"
	"strconv"
	"time"

	"github.com/zhubert/erg/internal/git"
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
	case "gate.approved":
		return c.checkGateApproved(ctx, params, item)
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

	workItem, ok := d.state.GetWorkItem(item.ID)
	if !ok {
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

		// Start addressing feedback (this is an internal sub-action of the wait state).
		// Pass the batch CommentCount so addressFeedback sets CommentsAddressed
		// using the same counting source used for detection here.
		d.addressFeedback(ctx, workItem, result.CommentCount)
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
// It first checks for merge conflicts — if the PR is CONFLICTING, it fires
// with {conflicting: true} so the workflow can route to a rebase step.
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

	// Check mergeable status first — conflicts prevent CI from running
	mergeStatus, mergeErr := d.gitService.CheckPRMergeableStatus(pollCtx, sess.RepoPath, item.Branch)
	if mergeErr != nil {
		log.Debug("mergeable check failed, falling through to CI", "error", mergeErr)
	} else if mergeStatus == git.MergeableConflicting {
		log.Warn("PR has merge conflicts")
		return true, map[string]any{"conflicting": true}, nil
	}

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

// checkGateApproved implements the gate.approved event.
// It pauses the workflow until a human provides an explicit approval signal
// on the GitHub issue. Two trigger modes are supported:
//
//   - label_added (default): fires when the configured label is present on the issue.
//   - comment_match: fires when a comment matching the configured regex pattern is
//     posted after the gate step was entered.
//
// Params:
//
//	trigger         - "label_added" (default) or "comment_match"
//	label           - label name to check for (trigger=label_added)
//	comment_pattern - regex pattern to match against comment bodies (trigger=comment_match)
func (c *EventChecker) checkGateApproved(ctx context.Context, params *workflow.ParamHelper, item *workflow.WorkItemView) (bool, map[string]any, error) {
	d := c.daemon
	log := d.logger.With("workItem", item.ID, "event", "gate.approved")

	// Resolve issue number from the work item's IssueRef.
	workItem, ok := d.state.GetWorkItem(item.ID)
	if !ok {
		log.Warn("work item not found")
		return false, nil, nil
	}
	if workItem.IssueRef.Source != "github" {
		log.Debug("gate.approved only supports github issues", "source", workItem.IssueRef.Source)
		return false, nil, nil
	}
	issueNumber, err := strconv.Atoi(workItem.IssueRef.ID)
	if err != nil {
		log.Warn("invalid issue number", "id", workItem.IssueRef.ID, "error", err)
		return false, nil, nil
	}

	repoPath := item.RepoPath
	if repoPath == "" {
		log.Warn("no repo path for work item")
		return false, nil, nil
	}

	pollCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	trigger := params.String("trigger", "label_added")

	switch trigger {
	case "label_added":
		label := params.String("label", "approved")
		log.Debug("checking for label", "label", label, "issueNumber", issueNumber)

		hasLabel, err := d.gitService.CheckIssueHasLabel(pollCtx, repoPath, issueNumber, label)
		if err != nil {
			log.Debug("failed to check issue label", "error", err)
			return false, nil, nil
		}
		if hasLabel {
			log.Info("gate label found on issue", "label", label)
			return true, map[string]any{"gate_approved": true, "gate_trigger": "label_added", "gate_label": label}, nil
		}
		log.Debug("gate label not yet present", "label", label)
		return false, nil, nil

	case "comment_match":
		pattern := params.String("comment_pattern", "")
		if pattern == "" {
			log.Warn("comment_match trigger requires comment_pattern param")
			return false, nil, nil
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			log.Warn("invalid comment_pattern regex", "pattern", pattern, "error", err)
			return false, nil, nil
		}

		log.Debug("checking for matching comment", "pattern", pattern, "issueNumber", issueNumber, "since", item.StepEnteredAt)

		comments, err := d.gitService.GetIssueComments(pollCtx, repoPath, issueNumber)
		if err != nil {
			log.Debug("failed to fetch issue comments", "error", err)
			return false, nil, nil
		}

		for _, comment := range comments {
			// Only consider comments posted after the gate step was entered.
			if !item.StepEnteredAt.IsZero() && !comment.CreatedAt.After(item.StepEnteredAt) {
				continue
			}
			if re.MatchString(comment.Body) {
				log.Info("gate comment pattern matched", "pattern", pattern, "author", comment.Author)
				return true, map[string]any{"gate_approved": true, "gate_trigger": "comment_match", "gate_comment_author": comment.Author}, nil
			}
		}
		log.Debug("no matching comment found")
		return false, nil, nil

	default:
		log.Warn("unknown gate trigger", "trigger", trigger)
		return false, nil, nil
	}
}
