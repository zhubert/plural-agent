package daemon

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/zhubert/erg/internal/git"
	"github.com/zhubert/erg/internal/issues"
	"github.com/zhubert/erg/internal/workflow"
)

// eventChecker implements workflow.EventChecker for the daemon.
type eventChecker struct {
	daemon *Daemon
}

// newEventChecker creates a new event checker backed by the daemon.
func newEventChecker(d *Daemon) *eventChecker {
	return &eventChecker{daemon: d}
}

// CheckEvent checks whether an event has fired for a work item.
func (c *eventChecker) CheckEvent(ctx context.Context, event string, params *workflow.ParamHelper, item *workflow.WorkItemView) (bool, map[string]any, error) {
	switch event {
	case "pr.reviewed":
		return c.checkPRReviewed(ctx, params, item)
	case "ci.complete":
		return c.checkCIComplete(ctx, params, item)
	case "ci.wait_for_checks":
		return c.checkCIWaitForChecks(ctx, params, item)
	case "pr.mergeable":
		return c.checkPRMergeable(ctx, params, item)
	case "gate.approved":
		return c.checkGateApproved(ctx, params, item)
	case "plan.user_replied":
		return c.checkPlanUserReplied(ctx, params, item)
	default:
		return false, nil, nil
	}
}

// checkPRReviewed checks for PR review status.
func (c *eventChecker) checkPRReviewed(ctx context.Context, params *workflow.ParamHelper, item *workflow.WorkItemView) (bool, map[string]any, error) {
	d := c.daemon
	log := d.logger.With("workItem", item.ID, "branch", item.Branch, "event", "pr.reviewed")

	sess := d.config.GetSession(item.SessionID)
	if sess == nil {
		log.Warn("session not found for work item")
		return false, nil, nil
	}

	pollCtx, cancel := context.WithTimeout(ctx, timeoutQuickAPI)
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

	autoAddress := params.Bool("auto_address", true)

	if result.CommentCount > item.CommentsAddressed {
		log.Debug("new comments detected, checking for review feedback",
			"addressed", item.CommentsAddressed,
			"current", result.CommentCount,
		)

		if autoAddress {
			maxRounds := params.Int("max_feedback_rounds", 3)
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

	// When auto_address is disabled, fire the event for changes_requested so
	// the workflow engine can route to an explicit address_review state.
	if !autoAddress && reviewDecision == git.ReviewChangesRequested {
		log.Info("PR has changes requested, advancing for address_review")
		return true, map[string]any{"changes_requested": true}, nil
	}

	return false, nil, nil
}

// checkCIComplete checks CI status and returns true when CI has passed.
// It first checks for merge conflicts — if the PR is CONFLICTING, it fires
// with {conflicting: true} so the workflow can route to a rebase step.
func (c *eventChecker) checkCIComplete(ctx context.Context, params *workflow.ParamHelper, item *workflow.WorkItemView) (bool, map[string]any, error) {
	d := c.daemon
	log := d.logger.With("workItem", item.ID, "branch", item.Branch, "event", "ci.complete")

	sess := d.config.GetSession(item.SessionID)
	if sess == nil {
		log.Warn("session not found for work item")
		return false, nil, nil
	}

	pollCtx, cancel := context.WithTimeout(ctx, timeoutStandardOp)
	defer cancel()

	// Check mergeable status first — conflicts prevent CI from running
	mergeStatus, mergeErr := d.gitService.CheckPRMergeableStatus(pollCtx, sess.RepoPath, item.Branch)
	if mergeErr != nil {
		log.Debug("mergeable check failed, falling through to CI", "error", mergeErr)
	} else if mergeStatus == git.MergeableConflicting {
		// After a clean rebase + force-push, GitHub's mergeable status can remain
		// CONFLICTING for a period while it recalculates. Skip the conflict signal
		// during a grace period to avoid a phantom rebase loop.
		if isRecentCleanRebase(item.StepData, 5*time.Minute) {
			log.Info("PR reports conflicting but recent rebase was clean, skipping conflict signal")
		} else {
			log.Warn("PR has merge conflicts")
			return true, map[string]any{"conflicting": true}, nil
		}
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
func (c *eventChecker) checkPRMergeable(ctx context.Context, params *workflow.ParamHelper, item *workflow.WorkItemView) (bool, map[string]any, error) {
	d := c.daemon
	log := d.logger.With("workItem", item.ID, "branch", item.Branch, "event", "pr.mergeable")

	sess := d.config.GetSession(item.SessionID)
	if sess == nil {
		log.Warn("session not found for work item")
		return false, nil, nil
	}

	pollCtx, cancel := context.WithTimeout(ctx, timeoutStandardOp)
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

// checkCIWaitForChecks waits for all GitHub CI checks to reach a terminal state.
// Unlike ci.complete (which only fires on passing CI), this event fires once all
// checks have finished — whether passing or failing — and populates step data
// with individual check results so downstream choice states can route accordingly.
//
// Data returned on fire:
//
//	ci_status     - "passing" | "failing" | "none"
//	passed_checks - list of check names that passed
//	failed_checks - list of check names that failed
func (c *eventChecker) checkCIWaitForChecks(ctx context.Context, params *workflow.ParamHelper, item *workflow.WorkItemView) (bool, map[string]any, error) {
	d := c.daemon
	log := d.logger.With("workItem", item.ID, "branch", item.Branch, "event", "ci.wait_for_checks")

	sess := d.config.GetSession(item.SessionID)
	if sess == nil {
		log.Warn("session not found for work item")
		return false, nil, nil
	}

	pollCtx, cancel := context.WithTimeout(ctx, timeoutStandardOp)
	defer cancel()

	checks, err := d.gitService.GetPRCheckDetails(pollCtx, sess.RepoPath, item.Branch)
	if err != nil {
		log.Debug("CI check details not available yet", "error", err)
		return false, nil, nil
	}

	if len(checks) == 0 {
		log.Debug("no CI checks configured")
		return true, map[string]any{
			"ci_status":     string(git.CIStatusNone),
			"passed_checks": []string{},
			"failed_checks": []string{},
		}, nil
	}

	var passedChecks, failedChecks []string
	hasPending := false
	for _, check := range checks {
		switch check.State {
		case "FAILURE", "ERROR", "CANCELLED", "TIMED_OUT":
			failedChecks = append(failedChecks, check.Name)
		case "PENDING", "QUEUED", "IN_PROGRESS", "WAITING", "REQUESTED":
			hasPending = true
		default: // SUCCESS, NEUTRAL, SKIPPED, etc.
			passedChecks = append(passedChecks, check.Name)
		}
	}

	if hasPending {
		log.Debug("CI checks still pending")
		return false, nil, nil
	}

	ciStatus := git.CIStatusPassing
	if len(failedChecks) > 0 {
		ciStatus = git.CIStatusFailing
	}

	log.Info("all CI checks complete", "ci_status", ciStatus, "passed", len(passedChecks), "failed", len(failedChecks))
	return true, map[string]any{
		"ci_status":     string(ciStatus),
		"passed_checks": passedChecks,
		"failed_checks": failedChecks,
	}, nil
}

// checkGateApproved implements the gate.approved event.
// It pauses the workflow until a human provides an explicit approval signal
// on the issue. Supports GitHub, Asana, and Linear. Two trigger modes are supported:
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
func (c *eventChecker) checkGateApproved(ctx context.Context, params *workflow.ParamHelper, item *workflow.WorkItemView) (bool, map[string]any, error) {
	d := c.daemon
	log := d.logger.With("workItem", item.ID, "event", "gate.approved")

	workItem, ok := d.state.GetWorkItem(item.ID)
	if !ok {
		log.Warn("work item not found")
		return false, nil, nil
	}

	repoPath := item.RepoPath
	if repoPath == "" {
		log.Warn("no repo path for work item")
		return false, nil, nil
	}

	pollCtx, cancel := context.WithTimeout(ctx, timeoutQuickAPI)
	defer cancel()

	source := workItem.IssueRef.Source
	issueID := workItem.IssueRef.ID
	trigger := params.String("trigger", "label_added")

	switch trigger {
	case "label_added":
		label := params.String("label", "approved")
		log.Debug("checking for label", "label", label, "issueID", issueID, "source", source)

		hasLabel, err := c.issueHasLabel(pollCtx, repoPath, source, issueID, label)
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

		log.Debug("checking for matching comment", "pattern", pattern, "issueID", issueID, "since", item.StepEnteredAt)

		comments, err := c.issueComments(pollCtx, repoPath, source, issueID)
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

// issueHasLabel checks if an issue has the given label, supporting GitHub, Asana, and Linear.
func (c *eventChecker) issueHasLabel(ctx context.Context, repoPath, source, issueID, label string) (bool, error) {
	d := c.daemon
	if source == "github" {
		issueNumber, err := strconv.Atoi(issueID)
		if err != nil {
			return false, fmt.Errorf("invalid github issue number %q: %w", issueID, err)
		}
		return d.gitService.CheckIssueHasLabel(ctx, repoPath, issueNumber, label)
	}

	if d.issueRegistry == nil {
		return false, fmt.Errorf("no issue registry configured for source %q", source)
	}
	p := d.issueRegistry.GetProvider(issues.Source(source))
	if p == nil {
		return false, fmt.Errorf("no provider found for source %q", source)
	}
	gc, ok := p.(issues.ProviderGateChecker)
	if !ok {
		return false, fmt.Errorf("provider for source %q does not support gate checking", source)
	}
	return gc.CheckIssueHasLabel(ctx, repoPath, issueID, label)
}

// issueComments returns all comments on an issue, supporting GitHub, Asana, and Linear.
func (c *eventChecker) issueComments(ctx context.Context, repoPath, source, issueID string) ([]issues.IssueComment, error) {
	d := c.daemon
	if source == "github" {
		issueNumber, err := strconv.Atoi(issueID)
		if err != nil {
			return nil, fmt.Errorf("invalid github issue number %q: %w", issueID, err)
		}
		gitComments, err := d.gitService.GetIssueComments(ctx, repoPath, issueNumber)
		if err != nil {
			return nil, err
		}
		result := make([]issues.IssueComment, len(gitComments))
		for i, gc := range gitComments {
			result[i] = issues.IssueComment{Author: gc.Author, Body: gc.Body, CreatedAt: gc.CreatedAt}
		}
		return result, nil
	}

	if d.issueRegistry == nil {
		return nil, fmt.Errorf("no issue registry configured for source %q", source)
	}
	p := d.issueRegistry.GetProvider(issues.Source(source))
	if p == nil {
		return nil, fmt.Errorf("no provider found for source %q", source)
	}
	gc, ok := p.(issues.ProviderGateChecker)
	if !ok {
		return nil, fmt.Errorf("provider for source %q does not support gate checking", source)
	}
	return gc.GetIssueComments(ctx, repoPath, issueID)
}

// checkPlanUserReplied implements the plan.user_replied event.
// It fires when a human posts a comment on the issue after the current
// plan_review step was entered, allowing the workflow to loop back for re-planning
// or advance to coding based on the comment content.
// Supports GitHub, Asana, and Linear.
//
// Params:
//
//	approval_pattern - optional regex; if set and the comment matches, fires with
//	                   plan_approved=true; otherwise fires with plan_approved=false
//	                   and user_feedback set to the comment body.
//
// Data returned on fire:
//
//	plan_approved        - true if the comment matched approval_pattern, false otherwise
//	user_feedback        - comment body (always set; useful for re-planning context)
//	user_feedback_author - username or display name of the commenter
func (c *eventChecker) checkPlanUserReplied(ctx context.Context, params *workflow.ParamHelper, item *workflow.WorkItemView) (bool, map[string]any, error) {
	d := c.daemon
	log := d.logger.With("workItem", item.ID, "event", "plan.user_replied")

	workItem, ok := d.state.GetWorkItem(item.ID)
	if !ok {
		log.Warn("work item not found")
		return false, nil, nil
	}

	repoPath := item.RepoPath
	if repoPath == "" {
		log.Warn("no repo path for work item")
		return false, nil, nil
	}

	pollCtx, cancel := context.WithTimeout(ctx, timeoutQuickAPI)
	defer cancel()

	comments, err := c.issueComments(pollCtx, repoPath, workItem.IssueRef.Source, workItem.IssueRef.ID)
	if err != nil {
		log.Debug("failed to fetch issue comments", "error", err)
		return false, nil, nil
	}

	approvalPattern := params.String("approval_pattern", "")
	var approvalRe *regexp.Regexp
	if approvalPattern != "" {
		re, err := regexp.Compile(approvalPattern)
		if err != nil {
			log.Warn("invalid approval_pattern regex", "pattern", approvalPattern, "error", err)
		} else {
			approvalRe = re
		}
	}

	for _, comment := range comments {
		// Only consider comments posted after the step was entered.
		if !item.StepEnteredAt.IsZero() && !comment.CreatedAt.After(item.StepEnteredAt) {
			continue
		}

		// Found a new comment — check if it's an approval.
		approved := approvalRe != nil && approvalRe.MatchString(comment.Body)
		log.Info("user replied to plan", "author", comment.Author, "approved", approved)
		return true, map[string]any{
			"plan_approved":        approved,
			"user_feedback":        comment.Body,
			"user_feedback_author": comment.Author,
		}, nil
	}

	log.Debug("no new user comments found", "since", item.StepEnteredAt)
	return false, nil, nil
}

// isRecentCleanRebase returns true if the step data indicates the most recent
// rebase was a no-op (clean) and occurred within the given grace period.
// This is used to suppress phantom conflict signals from GitHub's stale
// mergeable status after a rebase + force-push.
func isRecentCleanRebase(stepData map[string]any, grace time.Duration) bool {
	clean, ok := stepData["last_rebase_clean"]
	if !ok {
		return false
	}
	// Handle both native bool and JSON-deserialized values
	switch v := clean.(type) {
	case bool:
		if !v {
			return false
		}
	default:
		return false
	}

	rawTS, ok := stepData["last_rebase_at"]
	if !ok {
		return false
	}
	tsStr, ok := rawTS.(string)
	if !ok {
		return false
	}
	ts, err := time.Parse(time.RFC3339, tsStr)
	if err != nil {
		return false
	}
	return time.Since(ts) < grace
}
