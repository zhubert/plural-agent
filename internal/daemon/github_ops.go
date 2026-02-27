package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	osexec "os/exec"
	"strconv"
	"strings"
	"time"

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
	sess := d.config.GetSession(item.SessionID)
	if sess == nil {
		return "", fmt.Errorf("session not found")
	}

	log := d.logger.With("workItem", item.ID, "branch", item.Branch)

	// If the session already has a PR (e.g., from a previous attempt that was
	// recovered via the existing-PR path in startCoding), return its URL
	// instead of trying to create a duplicate.
	if sess.PRCreated {
		prCtx, prCancel := context.WithTimeout(ctx, 15*time.Second)
		prState, prErr := d.gitService.GetPRState(prCtx, sess.RepoPath, sess.Branch)
		prCancel()
		if prErr == nil && prState == git.PRStateOpen {
			prURL, urlErr := getPRURL(ctx, sess.RepoPath, sess.Branch)
			if urlErr != nil {
				log.Debug("failed to get existing PR URL, using empty", "error", urlErr)
			}
			log.Info("PR already exists, returning existing URL", "url", prURL)
			return prURL, nil
		}
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

	prCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
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

	// Mark session as PR created
	d.config.MarkSessionPRCreated(item.SessionID)
	d.saveConfig("createPR")

	return prURL, nil
}

// branchHasChanges returns true if the session's branch has new commits relative
// to the base branch OR has uncommitted changes in the worktree. Returns false
// when the coding session made no changes at all.
func (d *Daemon) branchHasChanges(ctx context.Context, sess *config.Session) (bool, error) {
	workDir := sess.WorkTree
	if workDir == "" {
		workDir = sess.RepoPath
	}

	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
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
	sess := d.config.GetSession(item.SessionID)
	if sess == nil {
		return fmt.Errorf("session not found")
	}

	pushCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
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
	sess := d.config.GetSession(item.SessionID)
	if sess == nil {
		return fmt.Errorf("session not found")
	}

	method := d.getEffectiveMergeMethod(sess.RepoPath)

	mergeCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
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
		rebaseCtx, rebaseCancel := context.WithTimeout(ctx, 2*time.Minute)
		defer rebaseCancel()

		if rebaseErr := d.gitService.RebaseBranch(rebaseCtx, worktree, sess.Branch, baseBranch); rebaseErr != nil {
			log.Warn("linearization rebase failed, returning original merge error", "rebaseError", rebaseErr)
			return mergeErr
		}

		log.Info("branch linearized successfully, retrying merge")

		// Retry merge
		retryCtx, retryCancel := context.WithTimeout(ctx, 60*time.Second)
		defer retryCancel()

		if retryErr := d.gitService.MergePR(retryCtx, sess.RepoPath, item.Branch, false, method); retryErr != nil {
			return retryErr
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

// commentOnIssue posts a comment on the GitHub issue for a work item.
func (d *Daemon) commentOnIssue(ctx context.Context, item daemonstate.WorkItem, params *workflow.ParamHelper) error {
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

	commentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	return d.gitService.CommentOnIssue(commentCtx, repoPath, issueNum, body)
}

// commentOnPR posts a comment on the PR for a work item.
func (d *Daemon) commentOnPR(ctx context.Context, item daemonstate.WorkItem, params *workflow.ParamHelper) error {
	sess := d.config.GetSession(item.SessionID)
	if sess == nil {
		return fmt.Errorf("session not found for work item %s", item.ID)
	}

	bodyTemplate := params.String("body", "")
	body, err := workflow.ResolveSystemPrompt(bodyTemplate, sess.RepoPath)
	if err != nil {
		return fmt.Errorf("failed to resolve comment body: %w", err)
	}
	if body == "" {
		return fmt.Errorf("comment body is empty")
	}

	commentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

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
func (d *Daemon) commentViaProvider(ctx context.Context, item daemonstate.WorkItem, params *workflow.ParamHelper, expectedSource issues.Source) error {
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

	commentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

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

	labelCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
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

	labelCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	return d.gitService.RemoveIssueLabel(labelCtx, repoPath, issueNum, label)
}

// closeIssue closes the GitHub issue for a work item.
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

	closeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := osexec.CommandContext(closeCtx, "gh", "issue", "close", item.IssueRef.ID)
	cmd.Dir = repoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("gh issue close failed: %w (output: %s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// unqueueIssue removes the queue label and leaves a comment explaining why,
// but does NOT close the issue. This is used when an existing PR already addresses
// the issue, or when the coding session made no changes. All operations are
// best-effort — failures are logged but do not block the workflow from advancing.
func (d *Daemon) unqueueIssue(ctx context.Context, item daemonstate.WorkItem, reason string) {
	log := d.logger.With("workItem", item.ID, "issue", item.IssueRef.ID, "source", item.IssueRef.Source)

	repoPath := d.resolveRepoPath(ctx, item)
	if repoPath == "" {
		log.Debug("no repo path found, skipping unqueue")
		return
	}

	label := d.resolveQueueLabel(repoPath)

	opCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Attempt to use the ProviderActions interface if the provider supports it.
	src := issues.Source(item.IssueRef.Source)
	p := d.issueRegistry.GetProvider(src)
	if pa, ok := p.(issues.ProviderActions); ok {
		if err := pa.RemoveLabel(opCtx, repoPath, item.IssueRef.ID, label); err != nil {
			log.Debug("failed to remove queue label during unqueue", "error", err, "label", label)
		}
		if err := pa.Comment(opCtx, repoPath, item.IssueRef.ID, reason); err != nil {
			log.Debug("failed to comment during unqueue", "error", err)
		}
	} else {
		log.Debug("provider does not support ProviderActions, skipping label removal and comment")
	}
}

// closeIssueGracefully removes the queue label and closes the issue with an
// explanatory comment. All operations are best-effort — failures are logged but
// do not block the workflow from advancing.
func (d *Daemon) closeIssueGracefully(ctx context.Context, item daemonstate.WorkItem) {
	if item.IssueRef.Source != "github" {
		return
	}

	repoPath := d.resolveRepoPath(ctx, item)
	if repoPath == "" {
		return
	}

	log := d.logger.With("workItem", item.ID, "issue", item.IssueRef.ID)

	issueNum, err := strconv.Atoi(item.IssueRef.ID)
	if err != nil {
		return
	}

	label := d.resolveQueueLabel(repoPath)

	opCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Remove queue label (best-effort)
	if err := d.gitService.RemoveIssueLabel(opCtx, repoPath, issueNum, label); err != nil {
		log.Debug("failed to remove queue label during graceful close", "error", err, "label", label)
	}

	// Comment explaining why we're closing
	comment := "Closing this issue — no work was needed (the branch already has a merged PR or the coding session made no changes)."
	if err := d.gitService.CommentOnIssue(opCtx, repoPath, issueNum, comment); err != nil {
		log.Debug("failed to comment during graceful close", "error", err)
	}

	// Close the issue
	if err := d.closeIssue(opCtx, item); err != nil {
		log.Debug("failed to close issue during graceful close", "error", err)
	}
}

// getPRURL fetches the URL of an existing PR for the given branch using the gh CLI.
func getPRURL(ctx context.Context, repoPath, branch string) (string, error) {
	prCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := osexec.CommandContext(prCtx, "gh", "pr", "view", branch, "--json", "url")
	cmd.Dir = repoPath
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("gh pr view failed: %w", err)
	}

	var result struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return "", fmt.Errorf("failed to parse PR URL: %w", err)
	}

	return result.URL, nil
}

// requestReview requests a review on the PR for a work item.
func (d *Daemon) requestReview(ctx context.Context, item daemonstate.WorkItem, params *workflow.ParamHelper) error {
	sess := d.config.GetSession(item.SessionID)
	if sess == nil {
		return fmt.Errorf("session not found for work item %s", item.ID)
	}

	reviewer := params.String("reviewer", "")
	if reviewer == "" {
		return fmt.Errorf("reviewer parameter is required")
	}

	reviewCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := osexec.CommandContext(reviewCtx, "gh", "pr", "edit", item.Branch, "--add-reviewer", reviewer)
	cmd.Dir = sess.RepoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("gh pr edit --add-reviewer failed: %w (output: %s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// assignPR assigns the PR to specific users for a work item.
func (d *Daemon) assignPR(ctx context.Context, item daemonstate.WorkItem, params *workflow.ParamHelper) error {
	sess := d.config.GetSession(item.SessionID)
	if sess == nil {
		return fmt.Errorf("session not found for work item %s", item.ID)
	}

	assignee := params.String("assignee", "")
	if assignee == "" {
		return fmt.Errorf("assignee parameter is required")
	}

	assignCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := osexec.CommandContext(assignCtx, "gh", "pr", "edit", item.Branch, "--add-assignee", assignee)
	cmd.Dir = sess.RepoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("gh pr edit --add-assignee failed: %w (output: %s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// resolveRepoPath resolves the repo path for a work item, preferring the session's path.
func (d *Daemon) resolveRepoPath(ctx context.Context, item daemonstate.WorkItem) string {
	if item.SessionID != "" {
		if sess := d.config.GetSession(item.SessionID); sess != nil {
			return sess.RepoPath
		}
	}
	return d.findRepoPath(ctx)
}

// resolveQueueLabel returns the configured filter label for the given repo,
// falling back to the default "queued" label.
func (d *Daemon) resolveQueueLabel(repoPath string) string {
	wfCfg := d.getWorkflowConfig(repoPath)
	if wfCfg != nil && wfCfg.Source.Filter.Label != "" {
		return wfCfg.Source.Filter.Label
	}
	return autonomousFilterLabel
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
