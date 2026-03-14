package daemon

import (
	"context"
	"fmt"
	osexec "os/exec"
	"strconv"
	"strings"

	"github.com/zhubert/erg/internal/config"
	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/git"
	"github.com/zhubert/erg/internal/issues"
	"github.com/zhubert/erg/internal/worker"
	"github.com/zhubert/erg/internal/workflow"
)

// createPR creates a pull request for a work item's session.
// When draft is true the PR is created in draft state.
func (d *Daemon) createPR(ctx context.Context, item daemonstate.WorkItem, draft bool) (string, error) {
	sess, err := d.getSessionOrError(item.SessionID)
	if err != nil {
		return "", err
	}

	log := d.logger.With("workItem", item.ID, "branch", item.Branch)

	// Always check if a PR already exists for this branch before creating.
	// This is idempotent: querying GitHub state instead of relying on local flags.
	prCheckCtx, prCheckCancel := context.WithTimeout(ctx, timeoutQuickAPI)
	existingState, existingURL, prCheckErr := d.gitService.GetPRForBranch(prCheckCtx, sess.RepoPath, sess.Branch)
	prCheckCancel()
	if prCheckErr == nil && existingState == git.PRStateOpen {
		log.Info("PR already exists, returning existing URL", "url", existingURL)
		return existingURL, nil
	}

	// Check if there are any changes to create a PR for.
	// If the coding session determined no changes were needed (e.g., the fix
	// was already applied), there will be no commits on the branch and no
	// uncommitted changes. Bail early with a clear error instead of letting
	// the GitHub API reject the PR with a cryptic GraphQL error.
	if hasChanges, err := d.branchHasChanges(ctx, sess); err != nil {
		log.Warn("failed to check branch for changes, proceeding with PR creation", "error", err)
	} else if !hasChanges {
		return "", fmt.Errorf("no changes on branch %s — coding session made no commits: %w", sess.Branch, errNoChanges)
	}

	log.Info("creating PR")

	prCtx, cancel := context.WithTimeout(ctx, timeoutGitPush)
	defer cancel()

	resultCh := d.gitService.CreatePR(prCtx, sess.RepoPath, sess.WorkTree, sess.Branch, sess.BaseBranch, "", sess.GetIssueRef(), item.SessionID, draft)

	var lastErr error
	var prURL string
	for result := range resultCh {
		if result.Error != nil {
			lastErr = result.Error
		}
		if result.Output != "" {
			trimmed := worker.TrimURL(result.Output)
			if trimmed != "" {
				prURL = trimmed
			}
		}
	}

	if lastErr != nil {
		return "", lastErr
	}

	return prURL, nil
}

// branchHasChanges returns true if the session's branch has new commits relative
// to the base branch OR has uncommitted changes in the worktree. Returns false
// when the coding session made no changes at all.
func (d *Daemon) branchHasChanges(ctx context.Context, sess *config.Session) (bool, error) {
	workDir := sess.GetWorkDir()

	checkCtx, cancel := context.WithTimeout(ctx, timeoutQuickAPI)
	defer cancel()

	// Check for uncommitted changes first (staged or unstaged).
	statusCmd := osexec.CommandContext(checkCtx, "git", "status", "--porcelain")
	statusCmd.Dir = workDir
	statusOut, err := statusCmd.Output()
	if err != nil {
		return false, fmt.Errorf("git status failed: %w", err)
	}
	if len(strings.TrimSpace(string(statusOut))) > 0 {
		return true, nil // Has uncommitted changes
	}

	// Check for new commits on the branch relative to the base branch.
	baseBranch := sess.BaseBranch
	if baseBranch == "" {
		baseBranch = "main"
	}
	revListCmd := osexec.CommandContext(checkCtx, "git", "rev-list", "--count", baseBranch+"..HEAD")
	revListCmd.Dir = workDir
	revOut, err := revListCmd.Output()
	if err != nil {
		return false, fmt.Errorf("git rev-list failed: %w", err)
	}
	count := strings.TrimSpace(string(revOut))
	return count != "0", nil
}

// pushChanges pushes changes for a work item's session.
func (d *Daemon) pushChanges(ctx context.Context, item daemonstate.WorkItem) error {
	sess, err := d.getSessionOrError(item.SessionID)
	if err != nil {
		return err
	}

	pushCtx, cancel := context.WithTimeout(ctx, timeoutGitPush)
	defer cancel()

	resultCh := d.gitService.PushUpdates(pushCtx, sess.RepoPath, sess.WorkTree, sess.Branch, "Address review feedback")

	var lastErr error
	for result := range resultCh {
		if result.Error != nil {
			lastErr = result.Error
		}
	}

	return lastErr
}

// mergePR merges the PR for a work item.
func (d *Daemon) mergePR(ctx context.Context, item daemonstate.WorkItem) error {
	sess, err := d.getSessionOrError(item.SessionID)
	if err != nil {
		return err
	}

	// Check PR state before attempting merge — if already merged, return
	// success without re-attempting (idempotent).
	stateCtx, stateCancel := context.WithTimeout(ctx, timeoutQuickAPI)
	prState, stateErr := d.gitService.GetPRState(stateCtx, sess.RepoPath, item.Branch)
	stateCancel()
	if stateErr == nil && prState == git.PRStateMerged {
		d.logger.Info("PR already merged, skipping merge", "workItem", item.ID, "branch", item.Branch)
		return nil
	}

	method := d.getEffectiveMergeMethod(sess.RepoPath)

	mergeCtx, cancel := context.WithTimeout(ctx, timeoutGitHubMerge)
	defer cancel()

	mergeErr := d.gitService.MergePR(mergeCtx, sess.RepoPath, item.Branch, false, method)
	if mergeErr != nil {
		// When using rebase merge, GitHub rejects branches with merge commits
		// (rebaseable=false). Linearize the branch locally and retry.
		if method != "rebase" {
			return mergeErr
		}

		log := d.logger.With("workItem", item.ID, "branch", item.Branch)
		log.Info("rebase merge failed, attempting to linearize branch", "error", mergeErr)

		// Get or recreate a worktree for rebasing
		worktree := sess.WorkTree
		if worktree == "" {
			var wtErr error
			worktree, wtErr = d.recreateWorktree(ctx, sess.RepoPath, sess.Branch, item.SessionID)
			if wtErr != nil {
				log.Warn("failed to create worktree for linearization", "error", wtErr)
				return mergeErr
			}
		}

		// Determine base branch
		baseBranch := sess.BaseBranch
		if baseBranch == "" {
			baseBranch = d.gitService.GetDefaultBranch(ctx, sess.RepoPath)
		}

		// Rebase to linearize history and force-push
		rebaseCtx, rebaseCancel := context.WithTimeout(ctx, timeoutGitPush)
		defer rebaseCancel()

		if rebaseErr := d.gitService.RebaseBranch(rebaseCtx, worktree, sess.Branch, baseBranch); rebaseErr != nil {
			log.Warn("linearization rebase failed, falling back to squash merge", "rebaseError", rebaseErr)

			squashCtx, squashCancel := context.WithTimeout(ctx, timeoutGitHubMerge)
			defer squashCancel()

			if squashErr := d.gitService.MergePR(squashCtx, sess.RepoPath, item.Branch, false, "squash"); squashErr != nil {
				log.Warn("squash merge fallback also failed", "squashError", squashErr)
				return mergeErr
			}

			log.Info("merged PR via squash fallback")
			// Fall through to post-merge cleanup below.
		} else {
			log.Info("branch linearized successfully, retrying merge")

			// Retry merge
			retryCtx, retryCancel := context.WithTimeout(ctx, timeoutGitHubMerge)
			defer retryCancel()

			if retryErr := d.gitService.MergePR(retryCtx, sess.RepoPath, item.Branch, false, method); retryErr != nil {
				return retryErr
			}
		}
	}

	// Mark session as merged
	d.config.MarkSessionPRMerged(item.SessionID)
	d.saveConfig("mergePR")

	// Persist the repo path before cleanup so workItemView can find it
	// after the session is removed from config.
	d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
		it.StepData["_repo_path"] = sess.RepoPath
	})

	// Auto-cleanup if enabled
	if d.config.GetAutoCleanupMerged() {
		d.cleanupSession(ctx, item.SessionID)
	}

	return nil
}

// ergGitHubMarker returns the idempotency HTML comment marker for GitHub comments.
// It is invisible when rendered by GitHub's Markdown parser.
func ergGitHubMarker(step string) string {
	return fmt.Sprintf("<!-- erg:step=%s -->", step)
}

// ergProviderMarker returns the idempotency marker for Asana/Linear comments.
// Uses HTML comment format (<!-- erg:step=… -->) which is hidden in rendered
// output for Linear (markdown). For Asana, the marker is visible in plain text.
func ergProviderMarker(step string) string {
	return fmt.Sprintf("<!-- erg:step=%s -->", step)
}

// containsMarker checks if a comment contains the given marker string.
func containsMarker(c issues.IssueComment, marker string) bool {
	return strings.Contains(c.Body, marker)
}

// commentOnIssue posts a comment on the GitHub issue for a work item.
// When step is non-empty, the comment is idempotent: if a comment with the
// matching marker already exists it is updated in place rather than creating
// a duplicate. This mirrors the pattern used by CI bots (Vercel, Codecov, etc.).
func (d *Daemon) commentOnIssue(ctx context.Context, item daemonstate.WorkItem, params *workflow.ParamHelper, step string) error {
	if item.IssueRef.Source != "github" {
		d.logger.Warn("github.comment_issue skipped: not a github issue",
			"workItem", item.ID, "source", item.IssueRef.Source)
		return nil
	}

	issueNum, err := strconv.Atoi(item.IssueRef.ID)
	if err != nil {
		return fmt.Errorf("invalid github issue number %q: %w", item.IssueRef.ID, err)
	}

	// Resolve repo path: prefer session's path, fall back to daemon's repo filter.
	repoPath := ""
	if item.SessionID != "" {
		if sess := d.config.GetSession(item.SessionID); sess != nil {
			repoPath = sess.RepoPath
		}
	}
	if repoPath == "" {
		repoPath = d.findRepoPath(ctx)
	}
	if repoPath == "" {
		return fmt.Errorf("no repo path found for work item %s", item.ID)
	}

	bodyTemplate := params.String("body", "")
	body, err := workflow.ResolveSystemPrompt(bodyTemplate, repoPath)
	if err != nil {
		return fmt.Errorf("failed to resolve comment body: %w", err)
	}
	if body == "" {
		return fmt.Errorf("comment body is empty")
	}

	commentCtx, cancel := context.WithTimeout(ctx, timeoutStandardOp)
	defer cancel()

	if step != "" {
		marker := ergGitHubMarker(step)
		markedBody := body + "\n" + marker

		existing, listErr := d.gitService.ListIssueComments(commentCtx, repoPath, issueNum)
		if listErr == nil {
			for _, c := range existing {
				if strings.Contains(c.Body, marker) {
					return d.gitService.UpdateIssueComment(commentCtx, repoPath, c.ID, markedBody)
				}
			}
		}
		// No existing comment found (or listing failed) — create a new one with marker.
		return d.gitService.CommentOnIssue(commentCtx, repoPath, issueNum, markedBody)
	}

	return d.gitService.CommentOnIssue(commentCtx, repoPath, issueNum, body)
}

// commentOnPR posts a comment on the PR for a work item.
// When step is non-empty, the comment is idempotent: if a comment with the
// matching marker already exists it is updated in place rather than creating a duplicate.
func (d *Daemon) commentOnPR(ctx context.Context, item daemonstate.WorkItem, params *workflow.ParamHelper, step string) error {
	sess, err := d.getSessionOrError(item.SessionID)
	if err != nil {
		return err
	}

	bodyTemplate := params.String("body", "")
	body, err := workflow.ResolveSystemPrompt(bodyTemplate, sess.RepoPath)
	if err != nil {
		return fmt.Errorf("failed to resolve comment body: %w", err)
	}
	if body == "" {
		return fmt.Errorf("comment body is empty")
	}

	commentCtx, cancel := context.WithTimeout(ctx, timeoutStandardOp)
	defer cancel()

	if step != "" {
		marker := ergGitHubMarker(step)
		markedBody := body + "\n" + marker

		prNum, prErr := d.gitService.GetPRNumber(commentCtx, sess.RepoPath, item.Branch)
		if prErr == nil {
			existing, listErr := d.gitService.ListIssueComments(commentCtx, sess.RepoPath, prNum)
			if listErr == nil {
				for _, c := range existing {
					if strings.Contains(c.Body, marker) {
						return d.gitService.UpdateIssueComment(commentCtx, sess.RepoPath, c.ID, markedBody)
					}
				}
			}
		}
		// No existing comment found (or PR/list lookup failed) — create a new one with marker.
		cmd := osexec.CommandContext(commentCtx, "gh", "pr", "comment", item.Branch, "--body", markedBody)
		cmd.Dir = sess.RepoPath
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("gh pr comment failed: %w (output: %s)", err, strings.TrimSpace(string(output)))
		}
		return nil
	}

	cmd := osexec.CommandContext(commentCtx, "gh", "pr", "comment", item.Branch, "--body", body)
	cmd.Dir = sess.RepoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("gh pr comment failed: %w (output: %s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// commentViaProvider posts a comment on the issue for a work item using the
// ProviderActions interface. expectedSource must match the work item's source;
// if it doesn't, the call is a no-op (with a warning). Returns an error if the
// provider is not registered, does not implement ProviderActions, the body is
// empty, or the API call fails.
// When step is non-empty, the comment is idempotent: if the provider also
// implements ProviderGateChecker and ProviderCommentUpdater, an existing comment
// with the matching marker is updated in place instead of creating a duplicate.
func (d *Daemon) commentViaProvider(ctx context.Context, item daemonstate.WorkItem, params *workflow.ParamHelper, expectedSource issues.Source, step string) error {
	if issues.Source(item.IssueRef.Source) != expectedSource {
		d.logger.Warn("comment action skipped: source mismatch",
			"workItem", item.ID, "source", item.IssueRef.Source, "expected", expectedSource)
		return nil
	}

	repoPath := d.resolveRepoPath(ctx, item)
	if repoPath == "" {
		return fmt.Errorf("no repo path found for work item %s", item.ID)
	}

	bodyTemplate := params.String("body", "")
	body, err := workflow.ResolveSystemPrompt(bodyTemplate, repoPath)
	if err != nil {
		return fmt.Errorf("failed to resolve comment body: %w", err)
	}
	if body == "" {
		return fmt.Errorf("comment body is empty")
	}

	p := d.issueRegistry.GetProvider(expectedSource)
	if p == nil {
		return fmt.Errorf("%s provider not registered", expectedSource)
	}
	pa, ok := p.(issues.ProviderActions)
	if !ok {
		return fmt.Errorf("%s provider does not support commenting", expectedSource)
	}

	commentCtx, cancel := context.WithTimeout(ctx, timeoutStandardOp)
	defer cancel()

	if step != "" {
		marker := ergProviderMarker(step)
		legacyMarker := fmt.Sprintf("[erg:step=%s]", step) // backward compat with old plain-text markers
		markedBody := body + "\n" + marker

		// Attempt idempotent upsert if the provider supports it.
		if gc, ok := p.(issues.ProviderGateChecker); ok {
			if cu, ok := p.(issues.ProviderCommentUpdater); ok {
				existing, listErr := gc.GetIssueComments(commentCtx, repoPath, item.IssueRef.ID)
				if listErr == nil {
					for _, c := range existing {
						if containsMarker(c, marker) || containsMarker(c, legacyMarker) {
							return cu.UpdateComment(commentCtx, repoPath, item.IssueRef.ID, c.ID, markedBody)
						}
					}
				}
			}
		}
		// No existing comment found (or provider doesn't support upsert) — create new.
		return pa.Comment(commentCtx, repoPath, item.IssueRef.ID, markedBody)
	}

	return pa.Comment(commentCtx, repoPath, item.IssueRef.ID, body)
}

// addLabel adds a label to the issue for a work item.
func (d *Daemon) addLabel(ctx context.Context, item daemonstate.WorkItem, params *workflow.ParamHelper) error {
	if item.IssueRef.Source != "github" {
		d.logger.Warn("github.add_label skipped: not a github issue",
			"workItem", item.ID, "source", item.IssueRef.Source)
		return nil
	}

	issueNum, err := strconv.Atoi(item.IssueRef.ID)
	if err != nil {
		return fmt.Errorf("invalid github issue number %q: %w", item.IssueRef.ID, err)
	}

	repoPath := d.resolveRepoPath(ctx, item)
	if repoPath == "" {
		return fmt.Errorf("no repo path found for work item %s", item.ID)
	}

	label := params.String("label", "")
	if label == "" {
		return fmt.Errorf("label parameter is required")
	}

	labelCtx, cancel := context.WithTimeout(ctx, timeoutStandardOp)
	defer cancel()

	return d.gitService.AddIssueLabel(labelCtx, repoPath, issueNum, label)
}

// removeLabel removes a label from the issue for a work item.
func (d *Daemon) removeLabel(ctx context.Context, item daemonstate.WorkItem, params *workflow.ParamHelper) error {
	if item.IssueRef.Source != "github" {
		d.logger.Warn("github.remove_label skipped: not a github issue",
			"workItem", item.ID, "source", item.IssueRef.Source)
		return nil
	}

	issueNum, err := strconv.Atoi(item.IssueRef.ID)
	if err != nil {
		return fmt.Errorf("invalid github issue number %q: %w", item.IssueRef.ID, err)
	}

	repoPath := d.resolveRepoPath(ctx, item)
	if repoPath == "" {
		return fmt.Errorf("no repo path found for work item %s", item.ID)
	}

	label := params.String("label", "")
	if label == "" {
		return fmt.Errorf("label parameter is required")
	}

	labelCtx, cancel := context.WithTimeout(ctx, timeoutStandardOp)
	defer cancel()

	return d.gitService.RemoveIssueLabel(labelCtx, repoPath, issueNum, label)
}

// moveToSection moves an Asana task to a named section within its project.
func (d *Daemon) moveToSection(ctx context.Context, item daemonstate.WorkItem, params *workflow.ParamHelper) error {
	if issues.Source(item.IssueRef.Source) != issues.SourceAsana {
		d.logger.Warn("asana.move_to_section skipped: not an asana issue",
			"workItem", item.ID, "source", item.IssueRef.Source)
		return nil
	}

	section := params.String("section", "")
	if section == "" {
		return fmt.Errorf("section parameter is required")
	}

	repoPath := d.resolveRepoPath(ctx, item)
	if repoPath == "" {
		return fmt.Errorf("no repo path found for work item %s", item.ID)
	}

	p := d.issueRegistry.GetProvider(issues.SourceAsana)
	if p == nil {
		return fmt.Errorf("asana provider not registered")
	}
	sm, ok := p.(issues.ProviderSectionMover)
	if !ok {
		return fmt.Errorf("asana provider does not support moving to sections")
	}

	moveCtx, cancel := context.WithTimeout(ctx, timeoutStandardOp)
	defer cancel()

	return sm.MoveToSection(moveCtx, repoPath, item.IssueRef.ID, section)
}

// moveToState moves a Linear issue to a named workflow state.
func (d *Daemon) moveToState(ctx context.Context, item daemonstate.WorkItem, params *workflow.ParamHelper) error {
	if issues.Source(item.IssueRef.Source) != issues.SourceLinear {
		d.logger.Warn("linear.move_to_state skipped: not a linear issue",
			"workItem", item.ID, "source", item.IssueRef.Source)
		return nil
	}

	state := params.String("state", "")
	if state == "" {
		return fmt.Errorf("state parameter is required")
	}

	repoPath := d.resolveRepoPath(ctx, item)
	if repoPath == "" {
		return fmt.Errorf("no repo path found for work item %s", item.ID)
	}

	p := d.issueRegistry.GetProvider(issues.SourceLinear)
	if p == nil {
		return fmt.Errorf("linear provider not registered")
	}
	sm, ok := p.(issues.ProviderSectionMover)
	if !ok {
		return fmt.Errorf("linear provider does not support moving to states")
	}

	moveCtx, cancel := context.WithTimeout(ctx, timeoutStandardOp)
	defer cancel()

	return sm.MoveToSection(moveCtx, repoPath, item.IssueRef.ID, state)
}

// closeIssue closes the GitHub issue for a work item.
// It is idempotent: if the issue is already closed, it returns nil.
func (d *Daemon) closeIssue(ctx context.Context, item daemonstate.WorkItem) error {
	if item.IssueRef.Source != "github" {
		d.logger.Warn("github.close_issue skipped: not a github issue",
			"workItem", item.ID, "source", item.IssueRef.Source)
		return nil
	}

	repoPath := d.resolveRepoPath(ctx, item)
	if repoPath == "" {
		return fmt.Errorf("no repo path found for work item %s", item.ID)
	}

	closeCtx, cancel := context.WithTimeout(ctx, timeoutStandardOp)
	defer cancel()

	// Check current state before closing to make this idempotent.
	if state, err := d.gitService.GetIssueState(closeCtx, repoPath, item.IssueRef.ID); err == nil {
		if strings.EqualFold(state, "CLOSED") {
			d.logger.Debug("github.close_issue skipped: issue already closed",
				"workItem", item.ID, "issue", item.IssueRef.ID)
			return nil
		}
	}

	return d.gitService.CloseIssue(closeCtx, repoPath, item.IssueRef.ID)
}

// unqueueIssue leaves a comment with an unqueued marker explaining why the
// issue is being dequeued, but does NOT close the issue or remove the label.
// The label stays as a permanent AI-assisted marker. The hidden marker in the
// comment prevents the poller from rediscovering the issue after terminal work
// items are pruned. All operations are best-effort — failures are logged but
// do not block the workflow from advancing. Also cleans up any claim comments
// posted by this daemon.
func (d *Daemon) unqueueIssue(ctx context.Context, item daemonstate.WorkItem, reason string) {
	d.unqueueIssueWithSuffix(ctx, item, reason, "")
}

// unqueueIssueWithSuffix is like unqueueIssue but embeds a machine-readable
// suffix in the marker (e.g. "success", "failed", "no_changes"). An empty
// suffix produces the legacy marker format.
func (d *Daemon) unqueueIssueWithSuffix(ctx context.Context, item daemonstate.WorkItem, reason, suffix string) {
	log := d.logger.With("workItem", item.ID, "issue", item.IssueRef.ID, "source", item.IssueRef.Source)

	repoPath := d.resolveRepoPath(ctx, item)
	if repoPath == "" {
		log.Debug("no repo path found, skipping unqueue")
		return
	}

	opCtx, cancel := context.WithTimeout(ctx, timeoutStandardOp)
	defer cancel()

	// Attempt to use the ProviderActions interface if the provider supports it.
	src := issues.Source(item.IssueRef.Source)
	p := d.issueRegistry.GetProvider(src)
	if pa, ok := p.(issues.ProviderActions); ok {
		body := issues.FormatUnqueuedCommentWithSuffix(src, reason, suffix)
		if err := pa.Comment(opCtx, repoPath, item.IssueRef.ID, body); err != nil {
			log.Debug("failed to comment during unqueue", "error", err)
		}
	} else {
		log.Debug("provider does not support ProviderActions, skipping comment")
	}

	// Clean up claim comments posted by this daemon.
	d.deleteClaimForIssue(opCtx, repoPath, src, item.IssueRef.ID)
}

// closeIssueGracefully closes the issue with an explanatory comment containing
// the unqueued marker. The label is kept as a permanent marker so humans can
// always identify AI-assisted issues. All operations are best-effort — failures
// are logged but do not block the workflow from advancing.
func (d *Daemon) closeIssueGracefully(ctx context.Context, item daemonstate.WorkItem) {
	repoPath := d.resolveRepoPath(ctx, item)
	if repoPath == "" {
		return
	}

	log := d.logger.With("workItem", item.ID, "issue", item.IssueRef.ID, "source", item.IssueRef.Source)

	reason := "Closing this issue — no work was needed (the branch already has a merged PR or the coding session made no changes)."

	opCtx, cancel := context.WithTimeout(ctx, timeoutStandardOp)
	defer cancel()

	// Post comment with unqueued marker so the poller won't rediscover this
	// issue after terminal work items are pruned.
	src := issues.Source(item.IssueRef.Source)
	if d.issueRegistry != nil {
		if p := d.issueRegistry.GetProvider(src); p != nil {
			if pa, ok := p.(issues.ProviderActions); ok {
				body := issues.FormatUnqueuedCommentWithSuffix(src, reason, "success")
				if err := pa.Comment(opCtx, repoPath, item.IssueRef.ID, body); err != nil {
					log.Debug("failed to comment during graceful close", "error", err)
				}
			}
		}
	}

	// Mark that the unqueued comment was posted so postTerminalMarker won't
	// double-post.
	d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
		if it.StepData == nil {
			it.StepData = make(map[string]any)
		}
		it.StepData["_unqueued_posted"] = true
	})

	// Close the issue (currently only supported for GitHub)
	if src == issues.SourceGitHub {
		if err := d.closeIssue(opCtx, item); err != nil {
			log.Debug("failed to close issue during graceful close", "error", err)
		}
	}

	// Clean up claim comments posted by this daemon.
	d.deleteClaimForIssue(opCtx, repoPath, src, item.IssueRef.ID)
}

// maxTerminalReasonLen caps the length of error details included in the
// unqueued marker comment to avoid leaking noisy or sensitive operational
// details (paths, command output, etc.) into public issue comments.
const maxTerminalReasonLen = 200

// postTerminalMarker posts an unqueued marker comment on the issue when a work
// item reaches a terminal state (success or failure). This is the durable guard
// that prevents re-polling after PruneTerminalItems cleans up old work items.
//
// The method is idempotent: an atomic check-and-set on the _unqueued_posted
// flag in StepData ensures at most one comment is posted, even if multiple
// callers race on the same work item. If the comment cannot be attempted
// (e.g. repo path unresolvable), the flag is NOT set so a later retry can
// succeed.
//
// All operations are best-effort — failures are logged but do not block the
// workflow from advancing.
func (d *Daemon) postTerminalMarker(ctx context.Context, itemID string, success bool) {
	// Atomic check-and-set: claim the right to post under the state lock.
	// If another caller already set the flag we return immediately.
	alreadyPosted := false
	d.state.UpdateWorkItem(itemID, func(it *daemonstate.WorkItem) {
		if it.StepData == nil {
			it.StepData = make(map[string]any)
		}
		if posted, _ := it.StepData["_unqueued_posted"].(bool); posted {
			alreadyPosted = true
			return
		}
		// Tentatively claim — cleared below if the comment can't be attempted.
		it.StepData["_unqueued_posted"] = true
	})
	if alreadyPosted {
		return
	}

	item, ok := d.state.GetWorkItem(itemID)
	if !ok {
		return
	}

	// Verify the comment can actually be attempted (repo path resolvable).
	// If not, clear the flag so a future call can retry.
	repoPath := d.resolveRepoPath(ctx, item)
	if repoPath == "" {
		d.state.UpdateWorkItem(itemID, func(it *daemonstate.WorkItem) {
			delete(it.StepData, "_unqueued_posted")
		})
		return
	}

	// Determine suffix and reason.
	suffix := "failed"
	reason := "Work item failed."
	if success {
		suffix = "success"
		reason = "Work completed successfully — PR merged."
	}
	if item.ErrorMessage != "" && !success {
		errMsg := item.ErrorMessage
		if len(errMsg) > maxTerminalReasonLen {
			errMsg = errMsg[:maxTerminalReasonLen] + "..."
		}
		reason = "Work item failed: " + errMsg
	}

	d.unqueueIssueWithSuffix(ctx, item, reason, suffix)
}

// requestReview requests a review on the PR for a work item.
// It is idempotent: if the reviewer has already been requested, it returns nil
// without sending a duplicate notification.
func (d *Daemon) requestReview(ctx context.Context, item daemonstate.WorkItem, params *workflow.ParamHelper) error {
	sess, err := d.getSessionOrError(item.SessionID)
	if err != nil {
		return err
	}

	reviewer := params.String("reviewer", "")
	if reviewer == "" {
		return fmt.Errorf("reviewer parameter is required")
	}

	reviewCtx, cancel := context.WithTimeout(ctx, timeoutStandardOp)
	defer cancel()

	// Check existing review requests to avoid sending duplicate notifications.
	if existing, err := d.gitService.GetPRReviewRequests(reviewCtx, sess.RepoPath, item.Branch); err == nil {
		if existing[strings.ToLower(reviewer)] {
			return nil // already requested, skip to avoid duplicate notification
		}
	}

	return d.gitService.RequestPRReview(reviewCtx, sess.RepoPath, item.Branch, reviewer)
}

// createRelease creates a GitHub release for a work item.
// Params:
//   - tag (required): release tag name, e.g. "v1.2.3"
//   - title (optional): release title; defaults to the tag name if empty
//   - notes (optional): release notes body; if empty, GitHub auto-generates notes
//   - draft (optional, default false): save as a draft release
//   - prerelease (optional, default false): mark as a pre-release
//   - target (optional): branch or SHA to tag; defaults to the repo's default branch
func (d *Daemon) createRelease(ctx context.Context, item daemonstate.WorkItem, params *workflow.ParamHelper) (string, error) {
	repoPath := d.resolveRepoPath(ctx, item)
	if repoPath == "" {
		return "", fmt.Errorf("no repo path found for work item %s", item.ID)
	}

	tag := params.String("tag", "")
	if tag == "" {
		return "", fmt.Errorf("tag parameter is required")
	}

	title := params.String("title", "")
	notes := params.String("notes", "")
	draft := params.Bool("draft", false)
	prerelease := params.Bool("prerelease", false)
	target := params.String("target", "")

	releaseCtx, cancel := context.WithTimeout(ctx, timeoutGitHubMerge)
	defer cancel()

	return d.gitService.CreateRelease(releaseCtx, repoPath, tag, title, notes, draft, prerelease, target)
}

// assignPR assigns the PR to specific users for a work item.
func (d *Daemon) assignPR(ctx context.Context, item daemonstate.WorkItem, params *workflow.ParamHelper) error {
	sess, err := d.getSessionOrError(item.SessionID)
	if err != nil {
		return err
	}

	assignee := params.String("assignee", "")
	if assignee == "" {
		return fmt.Errorf("assignee parameter is required")
	}

	assignCtx, cancel := context.WithTimeout(ctx, timeoutStandardOp)
	defer cancel()

	cmd := osexec.CommandContext(assignCtx, "gh", "pr", "edit", item.Branch, "--add-assignee", assignee)
	cmd.Dir = sess.RepoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("gh pr edit --add-assignee failed: %w (output: %s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// isUnqueued checks whether an issue has a comment with the erg unqueued marker.
// This is the durable guard that prevents re-polling after terminal work items
// are pruned. Fails open (returns false) on errors so issues are not silently skipped.
func (d *Daemon) isUnqueued(ctx context.Context, repoPath string, issue issues.Issue, provider issues.Source) bool {
	p := d.issueRegistry.GetProvider(provider)
	if p == nil {
		return false
	}
	gc, ok := p.(issues.ProviderGateChecker)
	if !ok {
		return false
	}
	comments, err := gc.GetIssueComments(ctx, repoPath, issue.ID)
	if err != nil {
		return false
	}
	return issues.HasUnqueuedMarker(comments)
}

// resolveRepoPath resolves the repo path for a work item, preferring the session's path.
func (d *Daemon) resolveRepoPath(ctx context.Context, item daemonstate.WorkItem) string {
	if item.SessionID != "" {
		if sess := d.config.GetSession(item.SessionID); sess != nil {
			return sess.RepoPath
		}
	}
	// After planning cleanup the session is removed, but the repo path is
	// stashed in StepData so subsequent actions (e.g. asana.move_to_section)
	// can still resolve it.
	if rp, ok := item.StepData["_repo_path"].(string); ok && rp != "" {
		return rp
	}
	return d.findRepoPath(ctx)
}

// issueFromWorkItem converts a WorkItem's issue ref to an issues.Issue.
func issueFromWorkItem(item daemonstate.WorkItem) issues.Issue {
	return issues.Issue{
		ID:     item.IssueRef.ID,
		Title:  item.IssueRef.Title,
		URL:    item.IssueRef.URL,
		Source: issues.Source(item.IssueRef.Source),
	}
}
