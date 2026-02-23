package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/zhubert/plural-agent/internal/daemonstate"
	"github.com/zhubert/plural-agent/internal/worker"
	"github.com/zhubert/plural-agent/internal/workflow"
	"github.com/zhubert/plural-core/claude"
	"github.com/zhubert/plural-core/config"
	"github.com/zhubert/plural-core/git"
	"github.com/zhubert/plural-core/issues"
	"github.com/zhubert/plural-core/paths"
	"github.com/zhubert/plural-core/session"
)

// Sentinel errors for recoverable situations that should not cause infinite re-queue.
var (
	errExistingPR = errors.New("existing open PR")
	errMergedPR   = errors.New("existing merged PR")
	errNoChanges  = errors.New("no changes")
)

// DefaultCodingSystemPrompt is the system prompt used for daemon-managed coding sessions
// when no custom system_prompt is configured in the workflow. It tells Claude to focus on
// coding and explicitly NOT attempt remote git operations (push, PR creation, etc.)
// since those are handled by the daemon workflow.
const DefaultCodingSystemPrompt = `You are an autonomous coding agent working on a task.

FOCUS: Write code, tests, and commit your changes locally.

DO NOT:
- Push branches or create pull requests — the system handles this automatically after you finish
- Run "git push", "gh pr create", or any remote git operations
- Look for or use push_branch, create_pr, or similar tools
- Attempt to find git credentials or authenticate with GitHub

WORKFLOW:
1. Read and understand the task
2. Implement the changes with clean, well-tested code
3. Run relevant tests locally to verify your changes work (quick tests only — the full CI suite runs after push)
4. Commit your changes locally with a clear commit message
5. Stop when the implementation is complete — the system will handle pushing and PR creation

TESTING — TWO-PHASE APPROACH:
- Run relevant unit tests locally to catch obvious issues before committing
- Do NOT try to run the entire CI pipeline locally — CI handles the full test suite after push
- If CI fails later, you may be resumed with failure logs to fix specific issues

CONTAINER ENVIRONMENT:
You are running inside a Docker container with the project's toolchain pre-installed.
- If a build or test command fails with a signal (segfault, SIGBUS, signal: killed),
  retry the command up to 2 times — the failure is likely transient due to container resource constraints.`

// codingAction implements the ai.code action.
type codingAction struct {
	daemon *Daemon
}

// Execute creates a session and starts a Claude worker for the work item.
func (a *codingAction) Execute(ctx context.Context, ac *workflow.ActionContext) workflow.ActionResult {
	d := a.daemon
	item := d.state.GetWorkItem(ac.WorkItemID)
	if item == nil {
		return workflow.ActionResult{Error: fmt.Errorf("work item not found: %s", ac.WorkItemID)}
	}

	if err := d.startCoding(ctx, item); err != nil {
		if errors.Is(err, errExistingPR) {
			// Branch has an open PR from a previous attempt — skip coding,
			// advance to open_pr which will detect the existing PR.
			return workflow.ActionResult{Success: true}
		}
		if errors.Is(err, errMergedPR) {
			// Branch already merged — close the issue and skip to done.
			d.closeIssueGracefully(ctx, item)
			return workflow.ActionResult{Success: true, OverrideNext: "done"}
		}
		return workflow.ActionResult{Error: err}
	}

	return workflow.ActionResult{Success: true, Async: true}
}

// createPRAction implements the github.create_pr action.
type createPRAction struct {
	daemon *Daemon
}

// Execute creates a PR. This is a synchronous action.
func (a *createPRAction) Execute(ctx context.Context, ac *workflow.ActionContext) workflow.ActionResult {
	d := a.daemon
	item := d.state.GetWorkItem(ac.WorkItemID)
	if item == nil {
		return workflow.ActionResult{Error: fmt.Errorf("work item not found: %s", ac.WorkItemID)}
	}

	prURL, err := d.createPR(ctx, item)
	if err != nil {
		if errors.Is(err, errNoChanges) {
			// Coding session made no changes — close the issue and skip to done.
			d.closeIssueGracefully(ctx, item)
			return workflow.ActionResult{Success: true, OverrideNext: "done"}
		}
		return workflow.ActionResult{Error: fmt.Errorf("PR creation failed: %v", err)}
	}

	return workflow.ActionResult{
		Success: true,
		Data:    map[string]any{"pr_url": prURL},
	}
}

// pushAction implements the github.push action.
type pushAction struct {
	daemon *Daemon
}

// Execute pushes changes. This is a synchronous action.
func (a *pushAction) Execute(ctx context.Context, ac *workflow.ActionContext) workflow.ActionResult {
	d := a.daemon
	item := d.state.GetWorkItem(ac.WorkItemID)
	if item == nil {
		return workflow.ActionResult{Error: fmt.Errorf("work item not found: %s", ac.WorkItemID)}
	}

	if err := d.pushChanges(ctx, item); err != nil {
		return workflow.ActionResult{Error: fmt.Errorf("push failed: %v", err)}
	}

	return workflow.ActionResult{Success: true}
}

// mergeAction implements the github.merge action.
type mergeAction struct {
	daemon *Daemon
}

// Execute merges the PR. This is a synchronous action.
func (a *mergeAction) Execute(ctx context.Context, ac *workflow.ActionContext) workflow.ActionResult {
	d := a.daemon
	item := d.state.GetWorkItem(ac.WorkItemID)
	if item == nil {
		return workflow.ActionResult{Error: fmt.Errorf("work item not found: %s", ac.WorkItemID)}
	}

	if err := d.mergePR(ctx, item); err != nil {
		return workflow.ActionResult{Error: fmt.Errorf("merge failed: %v", err)}
	}

	return workflow.ActionResult{Success: true}
}

// commentIssueAction implements the github.comment_issue action.
type commentIssueAction struct {
	daemon *Daemon
}

// Execute posts a comment on the GitHub issue for the work item.
func (a *commentIssueAction) Execute(ctx context.Context, ac *workflow.ActionContext) workflow.ActionResult {
	d := a.daemon
	item := d.state.GetWorkItem(ac.WorkItemID)
	if item == nil {
		return workflow.ActionResult{Error: fmt.Errorf("work item not found: %s", ac.WorkItemID)}
	}

	if err := d.commentOnIssue(ctx, item, ac.Params); err != nil {
		return workflow.ActionResult{Error: fmt.Errorf("issue comment failed: %v", err)}
	}

	return workflow.ActionResult{Success: true}
}

// commentPRAction implements the github.comment_pr action.
type commentPRAction struct {
	daemon *Daemon
}

// Execute posts a comment on the PR for the work item.
func (a *commentPRAction) Execute(ctx context.Context, ac *workflow.ActionContext) workflow.ActionResult {
	d := a.daemon
	item := d.state.GetWorkItem(ac.WorkItemID)
	if item == nil {
		return workflow.ActionResult{Error: fmt.Errorf("work item not found: %s", ac.WorkItemID)}
	}

	if err := d.commentOnPR(ctx, item, ac.Params); err != nil {
		return workflow.ActionResult{Error: fmt.Errorf("PR comment failed: %v", err)}
	}

	return workflow.ActionResult{Success: true}
}

// addLabelAction implements the github.add_label action.
type addLabelAction struct {
	daemon *Daemon
}

// Execute adds a label to the issue for the work item.
func (a *addLabelAction) Execute(ctx context.Context, ac *workflow.ActionContext) workflow.ActionResult {
	d := a.daemon
	item := d.state.GetWorkItem(ac.WorkItemID)
	if item == nil {
		return workflow.ActionResult{Error: fmt.Errorf("work item not found: %s", ac.WorkItemID)}
	}

	if err := d.addLabel(ctx, item, ac.Params); err != nil {
		return workflow.ActionResult{Error: fmt.Errorf("add label failed: %v", err)}
	}

	return workflow.ActionResult{Success: true}
}

// removeLabelAction implements the github.remove_label action.
type removeLabelAction struct {
	daemon *Daemon
}

// Execute removes a label from the issue for the work item.
func (a *removeLabelAction) Execute(ctx context.Context, ac *workflow.ActionContext) workflow.ActionResult {
	d := a.daemon
	item := d.state.GetWorkItem(ac.WorkItemID)
	if item == nil {
		return workflow.ActionResult{Error: fmt.Errorf("work item not found: %s", ac.WorkItemID)}
	}

	if err := d.removeLabel(ctx, item, ac.Params); err != nil {
		return workflow.ActionResult{Error: fmt.Errorf("remove label failed: %v", err)}
	}

	return workflow.ActionResult{Success: true}
}

// closeIssueAction implements the github.close_issue action.
type closeIssueAction struct {
	daemon *Daemon
}

// Execute closes the GitHub issue for the work item.
func (a *closeIssueAction) Execute(ctx context.Context, ac *workflow.ActionContext) workflow.ActionResult {
	d := a.daemon
	item := d.state.GetWorkItem(ac.WorkItemID)
	if item == nil {
		return workflow.ActionResult{Error: fmt.Errorf("work item not found: %s", ac.WorkItemID)}
	}

	if err := d.closeIssue(ctx, item); err != nil {
		return workflow.ActionResult{Error: fmt.Errorf("close issue failed: %v", err)}
	}

	return workflow.ActionResult{Success: true}
}

// requestReviewAction implements the github.request_review action.
type requestReviewAction struct {
	daemon *Daemon
}

// Execute requests review on the PR for the work item.
func (a *requestReviewAction) Execute(ctx context.Context, ac *workflow.ActionContext) workflow.ActionResult {
	d := a.daemon
	item := d.state.GetWorkItem(ac.WorkItemID)
	if item == nil {
		return workflow.ActionResult{Error: fmt.Errorf("work item not found: %s", ac.WorkItemID)}
	}

	if err := d.requestReview(ctx, item, ac.Params); err != nil {
		return workflow.ActionResult{Error: fmt.Errorf("request review failed: %v", err)}
	}

	return workflow.ActionResult{Success: true}
}

// startCoding creates a session and starts a Claude worker for a work item.
func (d *Daemon) startCoding(ctx context.Context, item *daemonstate.WorkItem) error {
	log := d.logger.With("workItem", item.ID, "issue", item.IssueRef.ID)

	// Find the matching repo path
	repoPath := d.findRepoPath(ctx)
	if repoPath == "" {
		return fmt.Errorf("no matching repo found")
	}

	branchPrefix := d.config.GetDefaultBranchPrefix()

	// Generate branch name
	var branchName string
	if d.issueRegistry != nil {
		issue := issueFromWorkItem(item)
		provider := d.issueRegistry.GetProvider(issue.Source)
		if provider != nil {
			branchName = provider.GenerateBranchName(issue)
		}
	}
	if branchName == "" {
		branchName = fmt.Sprintf("issue-%s", item.IssueRef.ID)
	}

	fullBranchName := branchPrefix + branchName

	// Check if branch already exists (stale from a previous crashed session)
	if d.sessionService.BranchExists(ctx, repoPath, fullBranchName) {
		// Before cleaning up, check if there's a live PR on this branch.
		// If so, create a minimal tracking session so the workflow can advance
		// to the PR monitoring states instead of failing and re-queuing.
		prCtx, prCancel := context.WithTimeout(ctx, 15*time.Second)
		prState, prErr := d.gitService.GetPRState(prCtx, repoPath, fullBranchName)
		prCancel()
		if prErr == nil && (prState == git.PRStateOpen || prState == git.PRStateMerged) {
			log.Warn("branch has existing PR, creating tracking session", "branch", fullBranchName, "prState", prState)

			// Create a minimal tracking session so the work item has a
			// session reference for downstream actions (push, PR view, etc.).
			trackingSess := &config.Session{
				ID:            uuid.New().String(),
				RepoPath:      repoPath,
				Branch:        fullBranchName,
				BaseBranch:    d.sessionService.GetDefaultBranch(ctx, repoPath),
				DaemonManaged: true,
				Autonomous:    true,
				Containerized: true,
				PRCreated:     true,
			}
			d.config.AddSession(*trackingSess)

			item.SessionID = trackingSess.ID
			item.Branch = fullBranchName
			item.UpdatedAt = time.Now()

			d.saveConfig("startCoding:existingPR")
			d.saveState()

			if prState == git.PRStateMerged {
				return fmt.Errorf("branch %s has an existing %s PR: %w", fullBranchName, prState, errMergedPR)
			}
			return fmt.Errorf("branch %s has an existing %s PR: %w", fullBranchName, prState, errExistingPR)
		}

		log.Warn("stale branch from previous attempt, cleaning up", "branch", fullBranchName)
		d.cleanupStaleBranch(ctx, repoPath, fullBranchName)
		if d.sessionService.BranchExists(ctx, repoPath, fullBranchName) {
			return fmt.Errorf("branch %s exists and could not be cleaned up", fullBranchName)
		}
	}

	// Create new session
	sess, err := d.sessionService.Create(ctx, repoPath, branchName, branchPrefix, session.BasePointOrigin)
	if err != nil {
		return fmt.Errorf("session creation failed: %w", err)
	}

	// Configure session from workflow config params
	wfCfg := d.getWorkflowConfig(repoPath)
	codingState := wfCfg.States["coding"]
	params := workflow.NewParamHelper(nil)
	if codingState != nil {
		params = workflow.NewParamHelper(codingState.Params)
	}

	sess.Autonomous = true
	sess.Containerized = params.Bool("containerized", true)
	sess.IsSupervisor = params.Bool("supervisor", false)
	sess.DaemonManaged = true
	sess.IssueRef = &config.IssueRef{
		Source: item.IssueRef.Source,
		ID:     item.IssueRef.ID,
		Title:  item.IssueRef.Title,
		URL:    item.IssueRef.URL,
	}

	d.config.AddSession(*sess)

	// Update work item with session info BEFORE saving to disk so that both
	// config and state are persisted atomically. If the daemon crashes between
	// these two saves there is still a small window, but it is much smaller
	// than the previous order (config saved, work item updated in memory only,
	// state saved at end of tick). Recovery will detect the orphaned branch on
	// the next start and clean it up.
	item.SessionID = sess.ID
	item.Branch = sess.Branch
	item.State = daemonstate.WorkItemCoding
	item.UpdatedAt = time.Now()

	d.saveConfig("startCoding")
	d.saveState()

	// Build initial message using provider-aware formatting
	issueBody, _ := item.StepData["issue_body"].(string)
	initialMsg := worker.FormatInitialMessage(item.IssueRef, issueBody)

	// Resolve coding system prompt from workflow config
	systemPrompt := params.String("system_prompt", "")
	codingPrompt, err := workflow.ResolveSystemPrompt(systemPrompt, repoPath)
	if err != nil {
		log.Warn("failed to resolve coding system prompt", "error", err)
	}

	if codingPrompt == "" {
		codingPrompt = DefaultCodingSystemPrompt
	}

	// Start worker, applying any per-session limits from workflow params
	w := d.createWorkerWithPrompt(ctx, item, sess, initialMsg, codingPrompt)
	maxTurns := params.Int("max_turns", 0)
	maxDuration := params.Duration("max_duration", 0)
	if maxTurns > 0 || maxDuration > 0 {
		w.SetLimits(maxTurns, maxDuration)
	}
	w.Start(ctx)

	log.Info("started coding", "sessionID", sess.ID, "branch", sess.Branch)
	return nil
}

// addressFeedback resumes the Claude session to address review comments.
func (d *Daemon) addressFeedback(ctx context.Context, item *daemonstate.WorkItem) {
	log := d.logger.With("workItem", item.ID, "branch", item.Branch)

	sess := d.config.GetSession(item.SessionID)
	if sess == nil {
		log.Error("session not found")
		return
	}

	// If there's no active worker, the old Claude conversation is gone.
	// Generate a new session ID (and recreate the worktree if missing) so
	// the runner starts a fresh conversation.
	sess = d.refreshStaleSession(ctx, item, sess)

	// Fetch review comments
	pollCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	comments, err := d.gitService.FetchPRReviewComments(pollCtx, sess.RepoPath, item.Branch)
	if err != nil {
		log.Warn("failed to fetch review comments", "error", err)
		return
	}

	if len(comments) == 0 {
		log.Debug("no comments to address")
		return
	}

	// Mark ALL comments as addressed (including transcripts) so the count
	// stays in sync with GetBatchPRStatesWithComments and doesn't re-trigger.
	commentCount := len(comments)
	d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
		it.CommentsAddressed += commentCount
		it.Phase = "addressing_feedback"
		it.UpdatedAt = time.Now()
	})

	// Filter out our own transcript comments — they aren't review feedback.
	reviewComments := worker.FilterTranscriptComments(comments)
	if len(reviewComments) == 0 {
		log.Debug("all comments are transcripts, nothing to address")
		d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
			it.Phase = "idle"
		})
		return
	}

	// Format comments as a prompt
	prompt := worker.FormatPRCommentsPrompt(reviewComments)

	// Resolve review system prompt from workflow config
	wfCfg := d.getWorkflowConfig(sess.RepoPath)
	reviewState := wfCfg.States["await_review"]
	systemPrompt := ""
	if reviewState != nil {
		p := workflow.NewParamHelper(reviewState.Params)
		systemPrompt = p.String("system_prompt", "")
	}

	reviewPrompt, err := workflow.ResolveSystemPrompt(systemPrompt, sess.RepoPath)
	if err != nil {
		log.Warn("failed to resolve review system prompt", "error", err)
	}

	// Resume the existing session with the review system prompt
	d.startWorkerWithPrompt(ctx, item, sess, prompt, reviewPrompt)

	log.Info("addressing review feedback", "commentCount", len(comments), "round", item.FeedbackRounds+1)
}

// createPR creates a pull request for a work item's session.
func (d *Daemon) createPR(ctx context.Context, item *daemonstate.WorkItem) (string, error) {
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

	resultCh := d.gitService.CreatePR(prCtx, sess.RepoPath, sess.WorkTree, sess.Branch, sess.BaseBranch, "", sess.GetIssueRef(), item.SessionID)

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
func (d *Daemon) pushChanges(ctx context.Context, item *daemonstate.WorkItem) error {
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
func (d *Daemon) mergePR(ctx context.Context, item *daemonstate.WorkItem) error {
	sess := d.config.GetSession(item.SessionID)
	if sess == nil {
		return fmt.Errorf("session not found")
	}

	mergeCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	err := d.gitService.MergePR(mergeCtx, sess.RepoPath, item.Branch, false, d.getEffectiveMergeMethod(sess.RepoPath))
	if err != nil {
		return err
	}

	// Mark session as merged
	d.config.MarkSessionPRMerged(item.SessionID)
	d.saveConfig("mergePR")

	// Auto-cleanup if enabled
	if d.config.GetAutoCleanupMerged() {
		d.cleanupSession(ctx, item.SessionID)
	}

	return nil
}

// commentOnIssue posts a comment on the GitHub issue for a work item.
func (d *Daemon) commentOnIssue(ctx context.Context, item *daemonstate.WorkItem, params *workflow.ParamHelper) error {
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

// refreshStaleSession checks if the Claude conversation for this item is still
// alive by looking for an active worker. If no worker is running, the container
// and conversation are gone and we generate a new session ID so the Claude runner
// starts a fresh conversation instead of trying to resume a dead one.
// If the session has no WorkTree (reconstructed after restart), a new worktree is
// created for the existing branch so the container has a directory to mount.
// Returns the (possibly new) session.
func (d *Daemon) refreshStaleSession(ctx context.Context, item *daemonstate.WorkItem, sess *config.Session) *config.Session {
	d.mu.Lock()
	_, hasWorker := d.workers[item.ID]
	d.mu.Unlock()
	if hasWorker {
		return sess // Active worker — conversation is still alive
	}

	log := d.logger.With("workItem", item.ID, "oldSessionID", sess.ID, "branch", item.Branch)

	oldID := sess.ID
	newID := uuid.New().String()

	// Clone session with new ID
	newSess := *sess
	newSess.ID = newID

	// If the session has no WorkTree (reconstructed after daemon restart),
	// create one so the container has a directory to mount.
	if newSess.WorkTree == "" && newSess.Branch != "" {
		wt, err := d.recreateWorktree(ctx, newSess.RepoPath, newSess.Branch, newID)
		if err != nil {
			log.Error("failed to recreate worktree for stale session", "error", err)
		} else {
			newSess.WorkTree = wt
			log.Info("recreated worktree for stale session", "worktree", wt)
		}
	}

	// Swap in config
	d.config.RemoveSession(oldID)
	d.config.AddSession(newSess)

	// Update work item to reference the new session
	d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
		it.SessionID = newID
	})

	d.saveConfig("refreshStaleSession")

	log.Info("refreshed stale session with new ID", "newSessionID", newID)
	return &newSess
}

// recreateWorktree creates a git worktree for an existing branch.
// This is used when recovering a session that lost its worktree after daemon restart.
// If another worktree already has this branch checked out (stale from a previous
// session), it is removed first.
func (d *Daemon) recreateWorktree(ctx context.Context, repoPath, branch, sessionID string) (string, error) {
	log := d.logger.With("branch", branch, "sessionID", sessionID)

	// Fetch latest from origin so the branch ref is up-to-date
	d.sessionService.FetchOrigin(ctx, repoPath)

	// Prune stale worktree entries (handles cases where the directory was
	// deleted but git still tracks the worktree).
	_ = osexec.CommandContext(ctx, "git", "-C", repoPath, "worktree", "prune").Run()

	// Check if a worktree already has this branch checked out and remove it.
	out, _ := osexec.CommandContext(ctx, "git", "-C", repoPath, "worktree", "list", "--porcelain").Output()
	if wtPath := parseWorktreeForBranch(string(out), branch); wtPath != "" {
		log.Info("removing stale worktree that holds branch", "stalePath", wtPath)
		_ = osexec.CommandContext(ctx, "git", "-C", repoPath, "worktree", "remove", "--force", wtPath).Run()
	}

	worktreesDir, err := paths.WorktreesDir()
	if err != nil {
		return "", fmt.Errorf("failed to get worktrees directory: %w", err)
	}

	worktreePath := filepath.Join(worktreesDir, sessionID)

	wtCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	addOut, err := osexec.CommandContext(wtCtx, "git", "-C", repoPath, "worktree", "add", worktreePath, branch).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git worktree add failed: %w (output: %s)", err, strings.TrimSpace(string(addOut)))
	}

	return worktreePath, nil
}

// configureRunner explicitly configures a runner for daemon use.
// The daemon makes all policy decisions here rather than relying on SessionManager.
func (d *Daemon) configureRunner(runner claude.RunnerInterface, sess *config.Session, customPrompt string) {
	// Tools: compose the container tool set for daemon sessions
	runner.SetAllowedTools(claude.ComposeTools(
		claude.ToolSetBase,
		claude.ToolSetContainerShell,
		claude.ToolSetWeb,
		claude.ToolSetProductivity,
	))

	// Container mode
	if sess.Containerized {
		runner.SetContainerized(true, d.config.GetContainerImage())
	}

	// Supervisor mode (if workflow config enables it)
	if sess.IsSupervisor {
		runner.SetSupervisor(true)
	}

	// No host tools — daemon manages push/PR/merge via workflow actions
	// (intentionally omitted: runner.SetHostTools)

	// Headless: no streaming chunks needed for autonomous sessions
	if sess.Autonomous {
		runner.SetDisableStreamingChunks(true)
	}

	// System prompt
	if customPrompt != "" {
		runner.SetSystemPrompt(customPrompt)
	}
}

// createWorkerWithPrompt creates a session worker with an optional custom system prompt
// but does not start it. The caller is responsible for calling w.Start(ctx).
// ctx is used to cancel the notification goroutine on shutdown.
func (d *Daemon) createWorkerWithPrompt(ctx context.Context, item *daemonstate.WorkItem, sess *config.Session, initialMsg, customPrompt string) *worker.SessionWorker {
	runner := d.sessionMgr.GetOrCreateRunner(sess)
	d.configureRunner(runner, sess, customPrompt)
	w := worker.NewSessionWorker(d, sess, runner, initialMsg)

	d.mu.Lock()
	d.workers[item.ID] = w
	d.mu.Unlock()

	go func() {
		w.Wait()
		select {
		case <-ctx.Done():
		default:
			d.notifyWorkerDone()
		}
	}()

	return w
}

// startWorkerWithPrompt creates and starts a session worker with an optional custom system prompt.
func (d *Daemon) startWorkerWithPrompt(ctx context.Context, item *daemonstate.WorkItem, sess *config.Session, initialMsg, customPrompt string) {
	w := d.createWorkerWithPrompt(ctx, item, sess, initialMsg, customPrompt)
	w.Start(ctx)
}

// cleanupSession cleans up a session's worktree and removes it from config.
func (d *Daemon) cleanupSession(ctx context.Context, sessionID string) {
	sess := d.config.GetSession(sessionID)
	if sess == nil {
		return
	}

	log := d.logger.With("sessionID", sessionID, "branch", sess.Branch)

	d.sessionMgr.DeleteSession(sessionID)

	if err := d.sessionService.Delete(ctx, sess); err != nil {
		log.Warn("failed to delete worktree", "error", err)
	}

	d.config.RemoveSession(sessionID)
	d.config.ClearOrphanedParentIDs([]string{sessionID})
	config.DeleteSessionMessages(sessionID)

	d.saveConfig("cleanupSession")

	log.Info("cleaned up session")
}

// cleanupStaleBranch attempts to clean up a stale branch left from a previous crashed session.
// It first looks for a matching session in config and uses the standard cleanup flow.
// If no session is found (orphaned branch), it falls back to direct git cleanup.
func (d *Daemon) cleanupStaleBranch(ctx context.Context, repoPath, branchName string) {
	log := d.logger.With("branch", branchName)

	// Look for a session with this branch and clean it up via the standard flow
	for _, sess := range d.config.GetSessions() {
		if sess.Branch == branchName {
			log.Info("found stale session, cleaning up", "sessionID", sess.ID)
			d.cleanupSession(ctx, sess.ID)
			return
		}
	}

	// No matching session — orphaned branch from incomplete cleanup
	log.Info("no matching session found, attempting direct git cleanup")

	// Try to find and remove any worktree associated with this branch
	out, err := osexec.CommandContext(ctx, "git", "-C", repoPath, "worktree", "list", "--porcelain").Output()
	if err == nil {
		wtPath := parseWorktreeForBranch(string(out), branchName)
		if wtPath != "" {
			log.Info("removing orphaned worktree", "path", wtPath)
			_ = osexec.CommandContext(ctx, "git", "-C", repoPath, "worktree", "remove", "--force", wtPath).Run()
		}
	}

	// Delete the orphaned branch
	_ = osexec.CommandContext(ctx, "git", "-C", repoPath, "branch", "-D", branchName).Run()
}

// parseWorktreeForBranch parses `git worktree list --porcelain` output to find
// the worktree path associated with a given branch name.
func parseWorktreeForBranch(porcelainOutput, branchName string) string {
	lines := strings.Split(porcelainOutput, "\n")
	var currentPath string
	for _, line := range lines {
		if strings.HasPrefix(line, "worktree ") {
			currentPath = strings.TrimPrefix(line, "worktree ")
		}
		if strings.HasPrefix(line, "branch refs/heads/") {
			branch := strings.TrimPrefix(line, "branch refs/heads/")
			if branch == branchName {
				return currentPath
			}
		}
	}
	return ""
}

// findRepoPath returns the first repo path that matches the daemon's filter.
func (d *Daemon) findRepoPath(ctx context.Context) string {
	for _, repoPath := range d.config.GetRepos() {
		if d.matchesRepoFilter(ctx, repoPath) {
			return repoPath
		}
	}
	return ""
}

// issueFromWorkItem converts a WorkItem's issue ref to an issues.Issue.
func issueFromWorkItem(item *daemonstate.WorkItem) issues.Issue {
	return issues.Issue{
		ID:     item.IssueRef.ID,
		Title:  item.IssueRef.Title,
		URL:    item.IssueRef.URL,
		Source: issues.Source(item.IssueRef.Source),
	}
}

// commentOnPR posts a comment on the PR for a work item.
func (d *Daemon) commentOnPR(ctx context.Context, item *daemonstate.WorkItem, params *workflow.ParamHelper) error {
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

// addLabel adds a label to the issue for a work item.
func (d *Daemon) addLabel(ctx context.Context, item *daemonstate.WorkItem, params *workflow.ParamHelper) error {
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
func (d *Daemon) removeLabel(ctx context.Context, item *daemonstate.WorkItem, params *workflow.ParamHelper) error {
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
func (d *Daemon) closeIssue(ctx context.Context, item *daemonstate.WorkItem) error {
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

// closeIssueGracefully removes the "queued" label and closes the issue with an
// explanatory comment. All operations are best-effort — failures are logged but
// do not block the workflow from advancing.
func (d *Daemon) closeIssueGracefully(ctx context.Context, item *daemonstate.WorkItem) {
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

	opCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Remove queued label (best-effort)
	if err := d.gitService.RemoveIssueLabel(opCtx, repoPath, issueNum, "queued"); err != nil {
		log.Debug("failed to remove queued label during graceful close", "error", err)
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
func (d *Daemon) requestReview(ctx context.Context, item *daemonstate.WorkItem, params *workflow.ParamHelper) error {
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

// resolveRepoPath resolves the repo path for a work item, preferring the session's path.
func (d *Daemon) resolveRepoPath(ctx context.Context, item *daemonstate.WorkItem) string {
	if item.SessionID != "" {
		if sess := d.config.GetSession(item.SessionID); sess != nil {
			return sess.RepoPath
		}
	}
	return d.findRepoPath(ctx)
}

// fixCIAction implements the ai.fix_ci action.
type fixCIAction struct {
	daemon *Daemon
}

// Execute fetches CI failure logs and resumes the coding session to fix CI.
func (a *fixCIAction) Execute(ctx context.Context, ac *workflow.ActionContext) workflow.ActionResult {
	d := a.daemon
	item := d.state.GetWorkItem(ac.WorkItemID)
	if item == nil {
		return workflow.ActionResult{Error: fmt.Errorf("work item not found: %s", ac.WorkItemID)}
	}

	// Check max rounds
	maxRounds := ac.Params.Int("max_ci_fix_rounds", 3)
	rounds := getCIFixRounds(item.StepData)
	if rounds >= maxRounds {
		return workflow.ActionResult{Error: fmt.Errorf("max CI fix rounds exceeded (%d/%d)", rounds, maxRounds)}
	}

	// Fetch CI failure logs
	sess := d.config.GetSession(item.SessionID)
	if sess == nil {
		return workflow.ActionResult{Error: fmt.Errorf("session not found")}
	}

	logs, err := fetchCIFailureLogs(ctx, sess.RepoPath, item.Branch)
	if err != nil {
		d.logger.Warn("failed to fetch CI logs, proceeding with generic message", "error", err)
		logs = "(CI failure logs unavailable)"
	}

	// Increment rounds
	d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
		it.StepData["ci_fix_rounds"] = rounds + 1
		it.UpdatedAt = time.Now()
	})

	// Resume session
	if err := d.startFixCI(ctx, item, sess, rounds+1, logs); err != nil {
		return workflow.ActionResult{Error: err}
	}

	return workflow.ActionResult{Success: true, Async: true}
}

// startFixCI resumes the coding session with CI failure context.
func (d *Daemon) startFixCI(ctx context.Context, item *daemonstate.WorkItem, sess *config.Session, round int, ciLogs string) error {
	// Refresh stale session (same reason as addressFeedback)
	sess = d.refreshStaleSession(ctx, item, sess)

	prompt := formatCIFixPrompt(round, ciLogs)

	// Resolve system prompt from workflow config's fix_ci state
	wfCfg := d.getWorkflowConfig(sess.RepoPath)
	fixState := wfCfg.States["fix_ci"]
	systemPrompt := ""
	if fixState != nil {
		p := workflow.NewParamHelper(fixState.Params)
		systemPrompt = p.String("system_prompt", "")
	}

	resolvedPrompt, err := workflow.ResolveSystemPrompt(systemPrompt, sess.RepoPath)
	if err != nil {
		d.logger.Warn("failed to resolve fix_ci system prompt", "error", err)
	}

	if resolvedPrompt == "" {
		resolvedPrompt = DefaultCodingSystemPrompt
	}

	d.startWorkerWithPrompt(ctx, item, sess, prompt, resolvedPrompt)
	d.logger.Info("started CI fix session", "workItem", item.ID, "round", round)
	return nil
}

// formatCIFixPrompt formats CI failure logs into a prompt for Claude.
func formatCIFixPrompt(round int, ciLogs string) string {
	return fmt.Sprintf(`CI FAILURE — FIX ROUND %d

The CI pipeline has failed after your changes were pushed. Please analyze the failure logs below and fix the issues.

INSTRUCTIONS:
1. Read the CI failure logs carefully
2. Identify the root cause of the failure
3. Fix the code to resolve the CI failure
4. Run relevant tests locally to verify your fix
5. Commit your changes locally — the system will push and re-run CI automatically

DO NOT push or create PRs — the system handles this.

CI FAILURE LOGS:
%s`, round, ciLogs)
}

// fetchCIFailureLogs fetches failure logs from the most recent failed CI run.
func fetchCIFailureLogs(ctx context.Context, repoPath, branch string) (string, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	// Find the most recent failed run
	listCmd := osexec.CommandContext(fetchCtx, "gh", "run", "list",
		"--branch", branch,
		"--status", "failure",
		"--limit", "1",
		"--json", "databaseId",
	)
	listCmd.Dir = repoPath
	listOutput, err := listCmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to list CI runs: %w", err)
	}

	var runs []struct {
		DatabaseID int `json:"databaseId"`
	}
	if err := json.Unmarshal(listOutput, &runs); err != nil {
		return "", fmt.Errorf("failed to parse CI runs: %w", err)
	}
	if len(runs) == 0 {
		return "", fmt.Errorf("no failed CI runs found for branch %s", branch)
	}

	// Get the failure logs
	runID := fmt.Sprintf("%d", runs[0].DatabaseID)
	logCmd := osexec.CommandContext(fetchCtx, "gh", "run", "view", runID,
		"--log-failed",
	)
	logCmd.Dir = repoPath
	logOutput, err := logCmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to fetch CI logs: %w", err)
	}

	logs := string(logOutput)
	// Truncate to ~50k chars if too long
	const maxLogLen = 50000
	const truncSuffix = "\n\n... (truncated)"
	if len(logs) > maxLogLen {
		logs = logs[:maxLogLen-len(truncSuffix)] + truncSuffix
	}

	return logs, nil
}

// formatAction implements the git.format action.
type formatAction struct {
	daemon *Daemon
}

// Execute runs a formatter command in the session's worktree and commits any resulting changes.
func (a *formatAction) Execute(ctx context.Context, ac *workflow.ActionContext) workflow.ActionResult {
	d := a.daemon
	item := d.state.GetWorkItem(ac.WorkItemID)
	if item == nil {
		return workflow.ActionResult{Error: fmt.Errorf("work item not found: %s", ac.WorkItemID)}
	}

	if err := d.runFormatter(ctx, item, ac.Params); err != nil {
		return workflow.ActionResult{Error: fmt.Errorf("format failed: %v", err)}
	}

	return workflow.ActionResult{Success: true}
}

// runFormatter runs the specified formatter command in the session's worktree
// and commits any resulting changes.
func (d *Daemon) runFormatter(ctx context.Context, item *daemonstate.WorkItem, params *workflow.ParamHelper) error {
	sess := d.config.GetSession(item.SessionID)
	if sess == nil {
		return fmt.Errorf("session not found for work item %s", item.ID)
	}

	command := params.String("command", "")
	if command == "" {
		return fmt.Errorf("command parameter is required for git.format")
	}

	message := params.String("message", "Apply auto-formatting")

	// Use worktree if available, fall back to repo path
	workDir := sess.WorkTree
	if workDir == "" {
		workDir = sess.RepoPath
	}

	formatCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	// Run the formatter command via shell
	formatCmd := osexec.CommandContext(formatCtx, "sh", "-c", command)
	formatCmd.Dir = workDir
	if output, err := formatCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("formatter failed: %w (output: %s)", err, strings.TrimSpace(string(output)))
	}

	// Stage all changes (handles new files, modifications, and deletions)
	addCmd := osexec.CommandContext(formatCtx, "git", "add", "-A")
	addCmd.Dir = workDir
	if out, err := addCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add failed: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}

	// Check if there is anything staged to commit (exit 0 = no changes)
	diffCmd := osexec.CommandContext(formatCtx, "git", "diff", "--cached", "--quiet")
	diffCmd.Dir = workDir
	if err := diffCmd.Run(); err == nil {
		d.logger.Info("formatter produced no changes", "workItem", item.ID)
		return nil
	}

	// Commit the formatting changes
	commitCmd := osexec.CommandContext(formatCtx, "git", "commit", "-m", message)
	commitCmd.Dir = workDir
	if out, err := commitCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit failed: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}

	d.logger.Info("applied formatting and committed", "workItem", item.ID, "command", command)
	return nil
}

// getCIFixRounds extracts the CI fix round counter from step data.
func getCIFixRounds(stepData map[string]any) int {
	v, ok := stepData["ci_fix_rounds"]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	default:
		return 0
	}
}

// saveRunnerMessages saves messages for a session's runner.
func (d *Daemon) saveRunnerMessages(sessionID string, runner claude.RunnerInterface) {
	if err := d.sessionMgr.SaveRunnerMessages(sessionID, runner); err != nil {
		d.logger.Error("failed to save session messages", "sessionID", sessionID, "error", err)
	}
}
