package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/zhubert/erg/internal/claude"
	"github.com/zhubert/erg/internal/config"
	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/git"
	"github.com/zhubert/erg/internal/paths"
	"github.com/zhubert/erg/internal/session"
	"github.com/zhubert/erg/internal/worker"
	"github.com/zhubert/erg/internal/workflow"
)

// startPlanning creates a read-only planning session and starts a Claude worker to analyze
// the issue and codebase, then post a structured implementation plan as an issue comment.
// Unlike startCoding, no new branch or worktree is created: Claude reads from the main
// repo directory and uses the gh CLI to post the plan as a GitHub issue comment.
func (d *Daemon) startPlanning(ctx context.Context, item daemonstate.WorkItem) error {
	log := d.logger.With("workItem", item.ID, "issue", item.IssueRef.ID)

	repoPath := d.findRepoPath(ctx)
	if repoPath == "" {
		return fmt.Errorf("no matching repo found")
	}

	baseBranch := d.gitService.GetDefaultBranch(ctx, repoPath)

	// Create a lightweight session on the default branch.
	// WorkTree is set to repoPath so Claude can read the codebase.
	// No new branch or git worktree is created — this is read-only.
	sessID := uuid.New().String()
	sess := &config.Session{
		ID:            sessID,
		RepoPath:      repoPath,
		WorkTree:      repoPath,
		Branch:        baseBranch,
		BaseBranch:    baseBranch,
		DaemonManaged: true,
		Autonomous:    true,
		IssueRef: &config.IssueRef{
			Source: item.IssueRef.Source,
			ID:     item.IssueRef.ID,
			Title:  item.IssueRef.Title,
			URL:    item.IssueRef.URL,
		},
	}

	// Configure from workflow params for the planning state
	wfCfg := d.getWorkflowConfig(repoPath)
	planningState := wfCfg.States["planning"]
	params := workflow.NewParamHelper(nil)
	if planningState != nil {
		params = workflow.NewParamHelper(planningState.Params)
	}

	sess.Containerized = params.Bool("containerized", true)
	d.config.AddSession(*sess)

	d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
		it.SessionID = sess.ID
		it.State = daemonstate.WorkItemActive
		it.UpdatedAt = time.Now()
	})

	d.saveConfig("startPlanning")
	d.saveState()

	// Build initial message using provider-aware formatting
	issueBody, _ := item.StepData["issue_body"].(string)
	initialMsg := worker.FormatInitialMessage(item.IssueRef, issueBody)

	// Resolve planning system prompt from workflow config
	systemPrompt := params.String("system_prompt", "")
	planningPrompt, err := workflow.ResolveSystemPrompt(systemPrompt, repoPath)
	if err != nil {
		log.Warn("failed to resolve planning system prompt", "error", err)
	}

	if planningPrompt == "" {
		planningPrompt = DefaultPlanningSystemPrompt
	}

	// Start worker, applying any per-session limits from workflow params
	w := d.createWorkerWithPrompt(ctx, item, sess, initialMsg, planningPrompt)
	maxTurns := params.Int("max_turns", 0)
	maxDuration := params.Duration("max_duration", 0)
	if maxTurns > 0 || maxDuration > 0 {
		w.SetLimits(maxTurns, maxDuration)
	}
	w.Start(ctx)

	log.Info("started planning", "sessionID", sess.ID, "branch", baseBranch)
	return nil
}

// startCoding creates a session and starts a Claude worker for a work item.
func (d *Daemon) startCoding(ctx context.Context, item daemonstate.WorkItem) error {
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
		prCtx, prCancel := context.WithTimeout(ctx, timeoutQuickAPI)
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

			d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
				it.SessionID = trackingSess.ID
				it.Branch = fullBranchName
				it.UpdatedAt = time.Now()
			})

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
	d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
		it.SessionID = sess.ID
		it.Branch = sess.Branch
		it.State = daemonstate.WorkItemActive
		it.UpdatedAt = time.Now()
	})

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

	// If a format_command is configured, store it in step data so
	// handleAsyncComplete can run it after coding finishes, and inject
	// instructions into the system prompt so Claude also runs it before
	// each commit.
	formatCommand := params.String("format_command", "")
	if formatCommand != "" {
		formatMessage := params.String("format_message", "Apply auto-formatting")
		d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
			it.StepData["_format_command"] = formatCommand
			it.StepData["_format_message"] = formatMessage
		})
		d.saveState()

		codingPrompt = codingPrompt + "\n\nFORMATTING: Before committing any changes, run the following formatter command:\n  " + formatCommand + "\nStage and include all formatting changes in your commit."
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
// batchCommentCount is the CommentCount from GetBatchPRStatesWithComments,
// which must be used to update CommentsAddressed so the two stay in sync.
func (d *Daemon) addressFeedback(ctx context.Context, item daemonstate.WorkItem, batchCommentCount int) {
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
	pollCtx, cancel := context.WithTimeout(ctx, timeoutStandardOp)
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

	// Set CommentsAddressed to the batch count from GetBatchPRStatesWithComments
	// (the same source used for detection in checkPRReviewed) so the two stay
	// in sync. Using len(comments) here would over-count because
	// FetchPRReviewComments expands inline code review comments individually,
	// while the batch API counts each review submission as one.
	d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
		it.CommentsAddressed = batchCommentCount
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

	// Resolve review system prompt and format_command from workflow config.
	wfCfg := d.getWorkflowConfig(sess.RepoPath)
	reviewState := wfCfg.States["await_review"]
	systemPrompt := ""
	formatCommand := ""
	if reviewState != nil {
		p := workflow.NewParamHelper(reviewState.Params)
		systemPrompt = p.String("system_prompt", "")
		formatCommand = p.String("format_command", "")
		if formatCommand != "" {
			// await_review has its own format_command — update step data so
			// handleAsyncComplete uses it as the formatter safety net after
			// this feedback round completes.
			formatMessage := p.String("format_message", "Apply auto-formatting")
			d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
				it.StepData["_format_command"] = formatCommand
				it.StepData["_format_message"] = formatMessage
			})
			d.saveState()
		}
	}

	reviewPrompt, err := workflow.ResolveSystemPrompt(systemPrompt, sess.RepoPath)
	if err != nil {
		log.Warn("failed to resolve review system prompt", "error", err)
	}

	// Inject formatting instructions if a format command is configured.
	// If await_review has no format_command param, fall back to whatever the
	// coding step stored in step data.
	if formatCommand == "" {
		formatCommand, _ = item.StepData["_format_command"].(string)
	}
	if formatCommand != "" {
		reviewPrompt = reviewPrompt + "\n\nFORMATTING: Before committing any changes, run the following formatter command:\n  " + formatCommand + "\nStage and include all formatting changes in your commit."
	}

	// Resume the existing session with the review system prompt
	d.startWorkerWithPrompt(ctx, item, sess, prompt, reviewPrompt)

	log.Info("addressing review feedback", "commentCount", len(comments), "round", item.FeedbackRounds+1)
}

// refreshStaleSession checks if the Claude conversation for this item is still
// alive by looking for an active worker. If no worker is running, the container
// and conversation are gone and we generate a new session ID so the Claude runner
// starts a fresh conversation instead of trying to resume a dead one.
// If the session has no WorkTree (reconstructed after restart), a new worktree is
// created for the existing branch so the container has a directory to mount.
// Returns the (possibly new) session.
func (d *Daemon) refreshStaleSession(ctx context.Context, item daemonstate.WorkItem, sess *config.Session) *config.Session {
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

	wtCtx, cancel := context.WithTimeout(ctx, timeoutStandardOp)
	defer cancel()

	addOut, err := osexec.CommandContext(wtCtx, "git", "-C", repoPath, "worktree", "add", worktreePath, branch).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git worktree add failed: %w (output: %s)", err, strings.TrimSpace(string(addOut)))
	}

	return worktreePath, nil
}

// configureRunner explicitly configures a runner for daemon use.
// The daemon makes all policy decisions here rather than relying on SessionManager.
func (d *Daemon) configureRunner(runner claude.RunnerConfig, sess *config.Session, customPrompt string) {
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

	// Enable host tools so Claude can use comment_issue and submit_review.
	// The worker rejects create_pr and push_branch with helpful error messages.
	runner.SetHostTools(true)

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
func (d *Daemon) createWorkerWithPrompt(ctx context.Context, item daemonstate.WorkItem, sess *config.Session, initialMsg, customPrompt string) *worker.SessionWorker {
	runner := d.sessionMgr.GetOrCreateRunner(sess)
	d.configureRunner(runner, sess, customPrompt)
	w := worker.NewSessionWorker(d, sess, runner, initialMsg)

	d.mu.Lock()
	d.workers[item.ID] = w
	d.mu.Unlock()

	go func() {
		select {
		case <-w.DoneChan():
			select {
			case <-ctx.Done():
			default:
				d.notifyWorkerDone()
			}
		case <-ctx.Done():
		}
	}()

	return w
}

// startWorkerWithPrompt creates and starts a session worker with an optional custom system prompt.
func (d *Daemon) startWorkerWithPrompt(ctx context.Context, item daemonstate.WorkItem, sess *config.Session, initialMsg, customPrompt string) {
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

// cleanupPlanningSession cleans up a planning session's runner/container and config
// without deleting the git worktree or branch (planning sessions are read-only on
// the default branch, so there's nothing to delete).
func (d *Daemon) cleanupPlanningSession(ctx context.Context, sessionID string) {
	sess := d.config.GetSession(sessionID)
	if sess == nil {
		return
	}

	log := d.logger.With("sessionID", sessionID, "branch", sess.Branch)

	d.sessionMgr.DeleteSession(sessionID)
	d.config.RemoveSession(sessionID)
	config.DeleteSessionMessages(sessionID)

	d.saveConfig("cleanupPlanningSession")

	log.Info("cleaned up planning session")
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
		if after, ok := strings.CutPrefix(line, "worktree "); ok {
			currentPath = after
		}
		if after, ok := strings.CutPrefix(line, "branch refs/heads/"); ok {
			branch := after
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

// runFormatter runs the specified formatter command in the session's worktree
// and commits any resulting changes.
func (d *Daemon) runFormatter(ctx context.Context, item daemonstate.WorkItem, params *workflow.ParamHelper) error {
	sess, err := d.getSessionOrError(item.SessionID)
	if err != nil {
		return err
	}

	command := params.String("command", "")
	if command == "" {
		return fmt.Errorf("command parameter is required for git.format")
	}

	message := params.String("message", "Apply auto-formatting")

	// Use worktree if available, fall back to repo path
	workDir := sess.GetWorkDir()

	formatCtx, cancel := context.WithTimeout(ctx, timeoutGitRewrite)
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

// startFixCI resumes the coding session with CI failure context.
func (d *Daemon) startFixCI(ctx context.Context, item daemonstate.WorkItem, sess *config.Session, round int, ciLogs string) error {
	// Refresh stale session (same reason as addressFeedback)
	sess = d.refreshStaleSession(ctx, item, sess)

	prompt := formatCIFixPrompt(round, ciLogs)

	// Resolve system prompt and format_command from workflow config's fix_ci state.
	wfCfg := d.getWorkflowConfig(sess.RepoPath)
	fixState := wfCfg.States["fix_ci"]
	systemPrompt := ""
	formatCommand := ""
	if fixState != nil {
		p := workflow.NewParamHelper(fixState.Params)
		systemPrompt = p.String("system_prompt", "")
		formatCommand = p.String("format_command", "")
		if formatCommand != "" {
			// fix_ci has its own format_command — update step data so
			// handleAsyncComplete uses it as the formatter safety net after
			// this round completes.
			formatMessage := p.String("format_message", "Apply auto-formatting")
			d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
				it.StepData["_format_command"] = formatCommand
				it.StepData["_format_message"] = formatMessage
			})
			d.saveState()
		}
	}

	resolvedPrompt, err := workflow.ResolveSystemPrompt(systemPrompt, sess.RepoPath)
	if err != nil {
		d.logger.Warn("failed to resolve fix_ci system prompt", "error", err)
	}

	if resolvedPrompt == "" {
		resolvedPrompt = DefaultCodingSystemPrompt
	}

	// Inject formatting instructions if a format command is configured.
	// If fix_ci has no format_command param, fall back to whatever the coding
	// step stored in step data (so Claude keeps formatting during CI fix rounds).
	if formatCommand == "" {
		formatCommand, _ = item.StepData["_format_command"].(string)
	}
	if formatCommand != "" {
		resolvedPrompt = resolvedPrompt + "\n\nFORMATTING: Before committing any changes, run the following formatter command:\n  " + formatCommand + "\nStage and include all formatting changes in your commit."
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
	fetchCtx, cancel := context.WithTimeout(ctx, timeoutGitHubMerge)
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

// startResolveConflicts starts a Claude session to resolve merge conflicts.
func (d *Daemon) startResolveConflicts(ctx context.Context, item *daemonstate.WorkItem, sess *config.Session, round int, conflictedFiles []string) error {
	prompt := formatConflictResolutionPrompt(round, conflictedFiles)

	// Resolve system prompt from workflow config
	wfCfg := d.getWorkflowConfig(sess.RepoPath)
	resolveState := wfCfg.States["resolve_conflicts"]
	systemPrompt := ""
	if resolveState != nil {
		p := workflow.NewParamHelper(resolveState.Params)
		systemPrompt = p.String("system_prompt", "")
	}

	resolvedPrompt, err := workflow.ResolveSystemPrompt(systemPrompt, sess.RepoPath)
	if err != nil {
		d.logger.Warn("failed to resolve resolve_conflicts system prompt", "error", err)
	}

	if resolvedPrompt == "" {
		resolvedPrompt = DefaultCodingSystemPrompt
	}

	d.startWorkerWithPrompt(ctx, *item, sess, prompt, resolvedPrompt)
	d.logger.Info("started conflict resolution session", "workItem", item.ID, "round", round, "conflictedFiles", len(conflictedFiles))
	return nil
}

// formatConflictResolutionPrompt builds the prompt that instructs Claude to resolve merge conflicts.
func formatConflictResolutionPrompt(round int, conflictedFiles []string) string {
	fileList := strings.Join(conflictedFiles, "\n  ")
	return fmt.Sprintf(`MERGE CONFLICT RESOLUTION — ROUND %d

The base branch has been merged into this feature branch, but there are merge conflicts that need to be resolved.

CONFLICTED FILES:
  %s

INSTRUCTIONS:
1. Read each conflicted file and understand the conflict markers (<<<<<<< HEAD, =======, >>>>>>> origin/...)
2. Resolve each conflict by choosing the correct code or combining both sides appropriately
3. After resolving all conflicts in a file, stage it with: git add <file>
4. After all conflicts are resolved and staged, complete the merge with: git commit --no-edit
5. Run relevant tests to verify your resolution is correct

DO NOT push — the system handles pushing after you commit.`, round, fileList)
}

// getConflictRounds extracts the conflict resolution round counter from step data.
func getConflictRounds(stepData map[string]any) int {
	v, ok := stepData["conflict_rounds"]
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

// getRebaseRounds extracts the rebase round counter from step data.
func getRebaseRounds(stepData map[string]any) int {
	v, ok := stepData["rebase_rounds"]
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

// startAddressReview resumes the coding session with PR review feedback context.
func (d *Daemon) startAddressReview(ctx context.Context, item daemonstate.WorkItem, sess *config.Session, round int, comments []git.PRReviewComment) error {
	// Refresh stale session (handles daemon restarts where old session is gone)
	sess = d.refreshStaleSession(ctx, item, sess)

	var prompt string
	if len(comments) > 0 {
		prompt = worker.FormatPRCommentsPrompt(comments)
	} else {
		prompt = formatAddressReviewPrompt(round)
	}

	// Resolve system prompt and format_command from workflow config's address_review state.
	wfCfg := d.getWorkflowConfig(sess.RepoPath)
	reviewState := wfCfg.States["address_review"]
	systemPrompt := ""
	formatCommand := ""
	if reviewState != nil {
		p := workflow.NewParamHelper(reviewState.Params)
		systemPrompt = p.String("system_prompt", "")
		formatCommand = p.String("format_command", "")
		if formatCommand != "" {
			// address_review has its own format_command — update step data so
			// handleAsyncComplete uses it as the formatter safety net after
			// this round completes.
			formatMessage := p.String("format_message", "Apply auto-formatting")
			d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
				it.StepData["_format_command"] = formatCommand
				it.StepData["_format_message"] = formatMessage
			})
			d.saveState()
		}
	}

	resolvedPrompt, err := workflow.ResolveSystemPrompt(systemPrompt, sess.RepoPath)
	if err != nil {
		d.logger.Warn("failed to resolve address_review system prompt", "error", err)
	}

	if resolvedPrompt == "" {
		resolvedPrompt = DefaultCodingSystemPrompt
	}

	// Inject formatting instructions if a format command is configured.
	// If address_review has no format_command param, fall back to whatever the
	// coding step stored in step data.
	if formatCommand == "" {
		formatCommand, _ = item.StepData["_format_command"].(string)
	}
	if formatCommand != "" {
		resolvedPrompt = resolvedPrompt + "\n\nFORMATTING: Before committing any changes, run the following formatter command:\n  " + formatCommand + "\nStage and include all formatting changes in your commit."
	}

	d.startWorkerWithPrompt(ctx, item, sess, prompt, resolvedPrompt)
	d.logger.Info("started address review session", "workItem", item.ID, "round", round, "commentCount", len(comments))
	return nil
}

// formatAddressReviewPrompt formats a fallback prompt for Claude when review comments are unavailable.
func formatAddressReviewPrompt(round int) string {
	return fmt.Sprintf(`PR REVIEW — ADDRESS ROUND %d

The PR has received review feedback requesting changes. Please review the PR comments and make the requested changes.

INSTRUCTIONS:
1. Review all open comments on this PR carefully
2. Make the requested changes to address the feedback
3. Run relevant tests locally to verify your changes
4. Commit your changes locally — the system will push and re-request review automatically

DO NOT push or create PRs — the system handles this.`, round)
}

// getReviewRounds extracts the review round counter from step data.
func getReviewRounds(stepData map[string]any) int {
	v, ok := stepData["review_rounds"]
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

// writePRDescription generates a rich PR description from the branch diff and updates the PR body.
// It uses Claude with a tailored prompt focused on description quality, then edits the open PR body.
func (d *Daemon) writePRDescription(ctx context.Context, item daemonstate.WorkItem, sess *config.Session) error {
	// Refresh stale session so worktree path is valid.
	sess = d.refreshStaleSession(ctx, item, sess)

	workDir := sess.GetWorkDir()

	baseBranch := sess.BaseBranch
	if baseBranch == "" {
		baseBranch = d.gitService.GetDefaultBranch(ctx, sess.RepoPath)
	}

	descCtx, cancel := context.WithTimeout(ctx, timeoutPRDescription)
	defer cancel()

	body, err := d.gitService.GenerateRichPRDescription(descCtx, workDir, item.Branch, baseBranch, sess.GetIssueRef())
	if err != nil {
		return fmt.Errorf("failed to generate PR description: %w", err)
	}

	updateCtx, updateCancel := context.WithTimeout(ctx, timeoutStandardOp)
	defer updateCancel()

	if err := d.gitService.UpdatePRBody(updateCtx, workDir, item.Branch, body); err != nil {
		return fmt.Errorf("failed to update PR body: %w", err)
	}

	d.logger.Info("updated PR description", "workItem", item.ID, "branch", item.Branch)
	return nil
}

// saveRunnerMessages saves messages for a session's runner.
func (d *Daemon) saveRunnerMessages(sessionID string, runner claude.RunnerSession) {
	if err := d.sessionMgr.SaveRunnerMessages(sessionID, runner); err != nil {
		d.logger.Error("failed to save session messages", "sessionID", sessionID, "error", err)
	}
}

// DefaultReviewSystemPrompt is the system prompt used for ai.review sessions when no
// custom system_prompt is configured in the workflow. It instructs Claude to review
// the diff and write a structured result file rather than modifying code.
const DefaultReviewSystemPrompt = `You are an autonomous code reviewer performing a self-review before changes are pushed to a pull request.

FOCUS: Review the provided diff for correctness, security, and code quality. Submit your findings via the submit_review MCP tool.

DO NOT:
- Modify any source files — this is a read-only review
- Push branches or create pull requests
- Run "git push" or "gh pr create"

REVIEW CRITERIA:
1. Correctness: Does the code implement the intended functionality correctly?
2. Tests: Are there adequate tests for new code and bug fixes?
3. Security: Are there vulnerabilities (SQL injection, XSS, path traversal, command injection, etc.)?
4. Edge cases: Are error conditions and edge cases handled properly?
5. Code quality: Is the code readable and following project conventions?

OUTPUT:
After reviewing, use the submit_review MCP tool to report your findings:
- Set passed=true if the code looks good or has only WARNING-level issues
- Set passed=false only for BLOCKING issues (critical bugs, security holes, missing required tests)
- Include a brief one-sentence summary describing the review outcome

Do NOT write to .erg/ai_review.json — use the submit_review tool instead.`

// startAIReview starts a Claude session to review the branch diff.
func (d *Daemon) startAIReview(ctx context.Context, item daemonstate.WorkItem, sess *config.Session, round int, diff string) error {
	// Refresh stale session (handles daemon restarts where old session is gone)
	sess = d.refreshStaleSession(ctx, item, sess)

	prompt := formatAIReviewPrompt(round, diff)

	// Resolve system prompt from the workflow config state for this step.
	// The state name is item.CurrentStep (e.g., "ai_review").
	wfCfg := d.getWorkflowConfig(sess.RepoPath)
	reviewState := wfCfg.States[item.CurrentStep]
	systemPrompt := ""
	if reviewState != nil {
		p := workflow.NewParamHelper(reviewState.Params)
		systemPrompt = p.String("system_prompt", "")
	}

	resolvedPrompt, err := workflow.ResolveSystemPrompt(systemPrompt, sess.RepoPath)
	if err != nil {
		d.logger.Warn("failed to resolve ai_review system prompt", "error", err)
	}

	if resolvedPrompt == "" {
		resolvedPrompt = DefaultReviewSystemPrompt
	}

	d.startWorkerWithPrompt(ctx, item, sess, prompt, resolvedPrompt)
	d.logger.Info("started AI review session", "workItem", item.ID, "round", round)
	return nil
}

// formatAIReviewPrompt builds the initial message for a Claude review session.
// It includes the round number and the full diff for Claude to review.
func formatAIReviewPrompt(round int, diff string) string {
	return fmt.Sprintf(`AI SELF-REVIEW — ROUND %d

Please review the following git diff and assess the code quality before this branch is pushed.

INSTRUCTIONS:
1. Read the diff carefully and understand what changes were made
2. Use bash tools to explore the full context of changed files if needed
3. Check for correctness, adequate tests, security issues, edge cases, and code quality
4. Use the submit_review MCP tool to report your findings
5. Set passed=false only for BLOCKING issues (critical bugs, security holes, missing required tests)
6. Set passed=true for warnings-only or clean reviews

DO NOT modify any source files — this is a review-only session.
DO NOT push or create PRs.
DO NOT write to .erg/ai_review.json — use the submit_review tool instead.

GIT DIFF:
%s`, round, diff)
}

// getAIReviewDiff returns the git diff between the remote base branch and HEAD.
// workDir is the worktree or repo path. baseBranch is e.g. "main".
func getAIReviewDiff(ctx context.Context, workDir, baseBranch string) (string, error) {
	if baseBranch == "" {
		baseBranch = "main"
	}

	ref := fmt.Sprintf("origin/%s...HEAD", baseBranch)
	cmd := osexec.CommandContext(ctx, "git", "diff", ref)
	cmd.Dir = workDir

	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git diff failed: %w", err)
	}

	const maxDiffRunes = 50000
	const truncSuffix = "\n\n... (diff truncated)"
	return truncateDiff(string(output), maxDiffRunes, truncSuffix), nil
}

// truncateDiff truncates diff to at most maxRunes runes, appending truncSuffix
// when truncation occurs. Using rune counts prevents splitting multi-byte UTF-8
// characters that would produce invalid sequences for Claude's API.
func truncateDiff(diff string, maxRunes int, truncSuffix string) string {
	runes := []rune(diff)
	if len(runes) <= maxRunes {
		return diff
	}
	suffixRunes := []rune(truncSuffix)
	return string(runes[:maxRunes-len(suffixRunes)]) + truncSuffix
}

// getAIReviewRounds extracts the AI review round counter from step data.
func getAIReviewRounds(stepData map[string]any) int {
	v, ok := stepData["ai_review_rounds"]
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

// aiReviewResult is the JSON structure written by Claude to .erg/ai_review.json.
type aiReviewResult struct {
	Passed  bool   `json:"passed"`
	Summary string `json:"summary"`
}

// readAIReviewResult reads the AI review result from .erg/ai_review.json in
// the session's worktree. Returns (passed=true, summary="") if the file is
// absent or unparseable — absent means Claude found no blocking issues.
func (d *Daemon) readAIReviewResult(sess *config.Session) (bool, string) {
	workDir := sess.GetWorkDir()

	resultPath := filepath.Join(workDir, ".erg", "ai_review.json")
	data, err := os.ReadFile(resultPath)
	if err != nil {
		// File not present — Claude didn't flag any blocking issues
		d.logger.Debug("ai_review.json not found, assuming review passed", "path", resultPath)
		return true, ""
	}

	var result aiReviewResult
	if err := json.Unmarshal(data, &result); err != nil {
		d.logger.Warn("failed to parse ai_review.json, assuming passed", "error", err, "path", resultPath)
		return true, ""
	}

	d.logger.Info("read AI review result", "passed", result.Passed, "summary", result.Summary)
	return result.Passed, result.Summary
}
