package daemon

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/zhubert/plural-agent/internal/daemonstate"
	"github.com/zhubert/plural-agent/internal/worker"
	"github.com/zhubert/plural-agent/internal/workflow"
	"github.com/zhubert/plural-core/claude"
	"github.com/zhubert/plural-core/config"
	"github.com/zhubert/plural-core/issues"
	"github.com/zhubert/plural-core/session"
)

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

	// Check if branch already exists
	if d.sessionService.BranchExists(ctx, repoPath, fullBranchName) {
		return fmt.Errorf("branch %s already exists", fullBranchName)
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
	sess.IsSupervisor = params.Bool("supervisor", true)
	sess.IssueRef = &config.IssueRef{
		Source: item.IssueRef.Source,
		ID:     item.IssueRef.ID,
		Title:  item.IssueRef.Title,
		URL:    item.IssueRef.URL,
	}

	d.config.AddSession(*sess)
	d.saveConfig("startCoding")

	// Update work item with session info.
	item.SessionID = sess.ID
	item.Branch = sess.Branch
	item.State = daemonstate.WorkItemCoding
	item.UpdatedAt = time.Now()

	// Build initial message using provider-aware formatting
	initialMsg := worker.FormatInitialMessage(item.IssueRef)

	// Resolve coding system prompt from workflow config
	systemPrompt := params.String("system_prompt", "")
	codingPrompt, err := workflow.ResolveSystemPrompt(systemPrompt, repoPath)
	if err != nil {
		log.Warn("failed to resolve coding system prompt", "error", err)
	}

	// Start worker, applying any per-session limits from workflow params
	w := d.createWorkerWithPrompt(item, sess, initialMsg, codingPrompt)
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

	// Mark comments as addressed
	item.CommentsAddressed += len(comments)
	item.Phase = "addressing_feedback"
	item.UpdatedAt = time.Now()

	// Format comments as a prompt
	prompt := worker.FormatPRCommentsPrompt(comments)

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

// createWorkerWithPrompt creates a session worker with an optional custom system prompt
// but does not start it. The caller is responsible for calling w.Start(ctx).
func (d *Daemon) createWorkerWithPrompt(item *daemonstate.WorkItem, sess *config.Session, initialMsg, customPrompt string) *worker.SessionWorker {
	runner := d.sessionMgr.GetOrCreateRunner(sess)
	if customPrompt != "" {
		runner.SetCustomSystemPrompt(customPrompt)
	}
	w := worker.NewSessionWorker(d, sess, runner, initialMsg)

	d.mu.Lock()
	d.workers[item.ID] = w
	d.mu.Unlock()

	go func() {
		w.Wait()
		d.notifyWorkerDone()
	}()

	return w
}

// startWorkerWithPrompt creates and starts a session worker with an optional custom system prompt.
func (d *Daemon) startWorkerWithPrompt(ctx context.Context, item *daemonstate.WorkItem, sess *config.Session, initialMsg, customPrompt string) {
	w := d.createWorkerWithPrompt(item, sess, initialMsg, customPrompt)
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

// saveRunnerMessages saves messages for a session's runner.
func (d *Daemon) saveRunnerMessages(sessionID string, runner claude.RunnerInterface) {
	if err := d.sessionMgr.SaveRunnerMessages(sessionID, runner); err != nil {
		d.logger.Error("failed to save session messages", "sessionID", sessionID, "error", err)
	}
}
