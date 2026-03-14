package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/zhubert/erg/internal/config"
	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/git"
	"github.com/zhubert/erg/internal/issues"
	"github.com/zhubert/erg/internal/paths"
	"github.com/zhubert/erg/internal/workflow"
)

// rebuildStateFromTracker scans the issue tracker for active work items and
// rebuilds daemon state from scratch. Instead of trusting the persisted state
// file, it queries the tracker to determine what's actually completed, walks
// the workflow graph, and places each item at the correct step.
//
// Terminal items (completed/failed) are preserved from the old state for
// dashboard display and history. All non-terminal items are discarded and
// rebuilt from the tracker.
func (d *Daemon) rebuildStateFromTracker(ctx context.Context) {
	if d.state == nil {
		return
	}

	log := d.logger.With("component", "rebuild")

	// 1. Clear all non-terminal items — we'll rebuild them from the tracker.
	d.state.ClearNonTerminalItems()

	// 2. For each repo, fetch issues matching the workflow filter and rebuild.
	repos := d.config.GetRepos()

	// In single-repo mode with a repoFilter but no repos in config yet,
	// use findRepoPath to discover it.
	if len(repos) == 0 && d.repoFilter != "" {
		if rp := d.findRepoPath(ctx); rp != "" {
			repos = []string{rp}
		}
	}

	if len(repos) == 0 {
		log.Debug("no repos to rebuild state for")
		return
	}

	rebuildCtx, cancel := context.WithTimeout(ctx, timeoutStandardOp)
	defer cancel()

	for _, repoPath := range repos {
		if !d.matchesRepoFilter(ctx, repoPath) {
			continue
		}

		wfCfg := d.getWorkflowConfig(repoPath)
		engine := d.getEngine(repoPath)
		if engine == nil {
			log.Warn("no workflow engine for repo, skipping rebuild", "repo", repoPath)
			continue
		}

		provider := issues.Source(wfCfg.Source.Provider)

		fetchedIssues, err := d.fetchIssuesForProvider(rebuildCtx, repoPath, wfCfg)
		if err != nil {
			log.Warn("failed to fetch issues for rebuild", "repo", repoPath, "error", err)
			continue
		}

		log.Info("rebuilding state from tracker",
			"repo", repoPath, "provider", provider, "issues", len(fetchedIssues))

		for _, issue := range fetchedIssues {
			// Skip if an item already exists for this issue in this repo.
			// Use the repo-scoped work item ID to avoid collisions when
			// different repos have issues with the same number.
			workItemID := fmt.Sprintf("%s-%s", repoPath, issue.ID)
			if _, exists := d.state.GetWorkItem(workItemID); exists {
				continue
			}

			// Claim the issue before rebuilding (multi-daemon coordination).
			// Unlike isClaimedByOther, tryClaim posts a claim so other daemons
			// can see that this daemon is tracking the issue after restart.
			won, claimErr := d.tryClaim(rebuildCtx, repoPath, issue, provider)
			if claimErr != nil {
				log.Debug("claim attempt failed during rebuild", "issue", issue.ID, "error", claimErr)
				continue
			}
			if !won {
				log.Debug("issue claimed by another daemon during rebuild, skipping", "issue", issue.ID)
				continue
			}

			item := d.rebuildWorkItem(rebuildCtx, repoPath, issue, engine, provider)
			if item != nil {
				d.state.AddRebuiltWorkItem(item)
				log.Info("rebuilt work item",
					"issue", issue.ID, "title", issue.Title,
					"step", item.CurrentStep, "state", item.State,
					"branch", item.Branch)
			}
		}
	}

	// 3. Reconstruct sessions for all rebuilt items so GetSession() works.
	d.reconstructSessions()
}

// rebuildWorkItem determines the correct workflow position for a single issue
// by querying the tracker for artifacts (PR, CI, review status) and walking
// the workflow graph.
func (d *Daemon) rebuildWorkItem(
	ctx context.Context,
	repoPath string,
	issue issues.Issue,
	engine *workflow.Engine,
	provider issues.Source,
) *daemonstate.WorkItem {
	log := d.logger.With("component", "rebuild", "issue", issue.ID)

	item := &daemonstate.WorkItem{
		ID: fmt.Sprintf("%s-%s", repoPath, issue.ID),
		IssueRef: config.IssueRef{
			Source: string(provider),
			ID:     issue.ID,
			Title:  issue.Title,
			URL:    issue.URL,
		},
		StepData: map[string]any{
			"_repo_path": repoPath,
		},
	}
	if issue.Body != "" {
		item.StepData["issue_body"] = issue.Body
	}

	// For GitHub, check for linked PRs to determine progress.
	if provider == issues.SourceGitHub {
		return d.rebuildGitHubWorkItem(ctx, repoPath, issue, engine, item, log)
	}

	// For non-GitHub providers, use event checkers to probe wait states.
	return d.rebuildGenericWorkItem(ctx, repoPath, engine, item, log)
}

// rebuildGitHubWorkItem handles GitHub-specific reconstruction by checking
// for linked PRs and their state.
func (d *Daemon) rebuildGitHubWorkItem(
	ctx context.Context,
	repoPath string,
	issue issues.Issue,
	engine *workflow.Engine,
	item *daemonstate.WorkItem,
	log interface {
		Info(string, ...any)
		Debug(string, ...any)
	},
) *daemonstate.WorkItem {
	issueNum, err := strconv.Atoi(issue.ID)
	if err != nil {
		// Not a valid GitHub issue number — queue from start
		item.State = daemonstate.WorkItemQueued
		return item
	}

	linkedPRs, err := d.gitService.GetLinkedPRsForIssue(ctx, repoPath, issueNum)
	if err != nil {
		log.Debug("failed to check linked PRs, queuing from start", "error", err)
		item.State = daemonstate.WorkItemQueued
		return item
	}

	if len(linkedPRs) == 0 {
		// No PR exists — start from the beginning
		item.State = daemonstate.WorkItemQueued
		return item
	}

	// Prefer an open PR over a merged one — an open PR represents active
	// work, whereas a merged PR may be from a previous attempt while a
	// newer open PR supersedes it.
	pr := linkedPRs[0]
	for _, candidate := range linkedPRs {
		if candidate.State == git.PRStateOpen {
			pr = candidate
			break
		}
	}
	item.Branch = pr.HeadRefName
	item.PRURL = pr.URL

	// PR merged → terminal success
	if pr.State == git.PRStateMerged {
		now := time.Now()
		item.State = daemonstate.WorkItemCompleted
		item.CurrentStep = "done"
		item.Phase = "idle"
		item.CompletedAt = &now
		return item
	}

	// PR closed → terminal failure
	if pr.State == git.PRStateClosed {
		now := time.Now()
		item.State = daemonstate.WorkItemFailed
		item.CurrentStep = "failed"
		item.Phase = "idle"
		item.CompletedAt = &now
		item.ErrorMessage = "PR was closed"
		return item
	}

	// PR is open — create a synthetic session so event checkers work,
	// then walk the workflow to find the correct position.
	sessionID := uuid.New().String()
	item.SessionID = sessionID

	var worktreePath string
	if worktreesDir, err := paths.WorktreesDir(); err == nil {
		worktreePath = filepath.Join(worktreesDir, sessionID)
	}

	sess := config.Session{
		ID:            sessionID,
		RepoPath:      repoPath,
		WorkTree:      worktreePath,
		Branch:        pr.HeadRefName,
		DaemonManaged: true,
		Autonomous:    true,
		Containerized: true,
		Started:       true,
		IssueRef: &config.IssueRef{
			Source: item.IssueRef.Source,
			ID:     item.IssueRef.ID,
			Title:  item.IssueRef.Title,
			URL:    item.IssueRef.URL,
		},
	}
	d.config.AddSession(sess)

	return d.walkWorkflowForPosition(ctx, repoPath, engine, item, log, true)
}

// rebuildGenericWorkItem handles non-GitHub providers by walking the workflow
// and probing wait states with event checkers.
func (d *Daemon) rebuildGenericWorkItem(
	ctx context.Context,
	repoPath string,
	engine *workflow.Engine,
	item *daemonstate.WorkItem,
	log interface {
		Info(string, ...any)
		Debug(string, ...any)
	},
) *daemonstate.WorkItem {
	// For non-GitHub providers without a PR concept, we can still probe
	// wait states using event checkers (e.g., asana.in_section, linear.in_state).
	// However, we need a session for the event checker to resolve the repo path.
	// Create a minimal session with Branch and WorkTree populated so
	// downstream code that inspects the session doesn't get empty values.
	sessionID := uuid.New().String()
	item.SessionID = sessionID

	var worktreePath string
	if worktreesDir, err := paths.WorktreesDir(); err == nil {
		worktreePath = filepath.Join(worktreesDir, sessionID)
	}

	sess := config.Session{
		ID:            sessionID,
		RepoPath:      repoPath,
		WorkTree:      worktreePath,
		Branch:        item.Branch,
		DaemonManaged: true,
		Autonomous:    true,
		Containerized: true,
		Started:       true,
		IssueRef: &config.IssueRef{
			Source: item.IssueRef.Source,
			ID:     item.IssueRef.ID,
			Title:  item.IssueRef.Title,
			URL:    item.IssueRef.URL,
		},
	}
	d.config.AddSession(sess)

	return d.walkWorkflowForPosition(ctx, repoPath, engine, item, log, false)
}

// walkWorkflowForPosition walks the workflow's wait states in BFS order,
// probing each with the event checker to determine which have been satisfied.
// It places the work item at the first unsatisfied wait state.
//
// hasProgressEvidence indicates whether the caller has independent proof that
// the workflow has progressed (e.g. a linked PR exists for GitHub issues).
// When false and the workflow starts with a task state, the walker returns
// WorkItemQueued if no wait states have been satisfied — this prevents a
// brand-new issue from being placed at a wait state whose preceding task
// never ran.
func (d *Daemon) walkWorkflowForPosition(
	ctx context.Context,
	repoPath string,
	engine *workflow.Engine,
	item *daemonstate.WorkItem,
	log interface {
		Info(string, ...any)
		Debug(string, ...any)
	},
	hasProgressEvidence bool,
) *daemonstate.WorkItem {
	checker := newEventChecker(d)
	waitStates := engine.GetOrderedWaitStates()

	if len(waitStates) == 0 {
		// No wait states in workflow — queue from start
		item.State = daemonstate.WorkItemQueued
		return item
	}

	var lastSatisfiedIdx = -1

	for i, ws := range waitStates {
		// Build a probe view for this wait state
		view := &workflow.WorkItemView{
			ID:            item.ID,
			SessionID:     item.SessionID,
			RepoPath:      repoPath,
			Branch:        item.Branch,
			PRURL:         item.PRURL,
			CurrentStep:   ws.Name,
			Phase:         "idle",
			StepData:      item.StepData,
			StepEnteredAt: time.Now().Add(-1 * time.Hour), // backdate so timeouts don't fire
		}

		params := workflow.NewParamHelper(ws.Params)
		fired, data, err := checker.CheckEvent(ctx, ws.Event, params, view)
		if err != nil {
			log.Debug("event check error during rebuild", "event", ws.Event, "state", ws.Name, "error", err)
			// Can't determine — place at this wait state
			item.State = daemonstate.WorkItemActive
			item.CurrentStep = ws.Name
			item.Phase = "idle"
			return item
		}

		if !fired {
			// Without external progress evidence (e.g. a linked PR),
			// an unsatisfied first wait state is indistinguishable from
			// "the preceding task never ran." Queue from start so the
			// initial task executes instead of skipping to a wait state.
			if !hasProgressEvidence && lastSatisfiedIdx < 0 {
				if workflowStartsWithTask(engine) {
					log.Info("no progress evidence and workflow starts with task, queuing from start",
						"firstUnsatisfied", ws.Name)
					item.State = daemonstate.WorkItemQueued
					return item
				}
			}

			// This wait state hasn't been satisfied — place item here
			item.State = daemonstate.WorkItemActive
			item.CurrentStep = ws.Name
			item.Phase = "idle"
			log.Info("placing at unsatisfied wait state", "state", ws.Name, "event", ws.Event)
			return item
		}

		// Event fired — merge data and continue checking subsequent states
		if data != nil {
			for k, v := range data {
				item.StepData[k] = v
			}
		}
		lastSatisfiedIdx = i
	}

	// All wait states satisfied — place at the last wait state rather than
	// the sync task after it. processWaitItems / processCIItems will detect
	// that the event has already fired, advance the item, and call
	// executeSyncChain to run any subsequent sync steps (choice, pass, task).
	// Placing directly at a sync step would leave the item stuck because
	// normal polling only processes wait states.
	if lastSatisfiedIdx >= 0 {
		lastWS := waitStates[lastSatisfiedIdx]
		item.State = daemonstate.WorkItemActive
		item.CurrentStep = lastWS.Name
		item.Phase = "idle"
		log.Info("all wait states satisfied, placing at last wait state for re-evaluation", "state", lastWS.Name)
		return item
	}

	// If we somehow got here, place at the first wait state as a safe default
	item.State = daemonstate.WorkItemActive
	item.CurrentStep = waitStates[0].Name
	item.Phase = "idle"
	return item
}

// workflowStartsWithTask follows the workflow's start state through any pass
// states (produced by template expansion) and returns true if the effective
// first real state is a task.
func workflowStartsWithTask(engine *workflow.Engine) bool {
	name := engine.GetStartState()
	seen := map[string]bool{}
	for {
		if seen[name] {
			return false // cycle
		}
		seen[name] = true
		s := engine.GetState(name)
		if s == nil {
			return false
		}
		if s.Type == workflow.StateTypePass {
			name = s.Next
			continue
		}
		return s.Type == workflow.StateTypeTask
	}
}

// reconstructSessions creates minimal config.Session objects for work items
// whose sessions are missing from the in-memory config. On daemon restart,
// the config starts empty (AgentConfig.Save() is a no-op), so sessions need
// to be recreated for GetSession() to work during normal polling.
func (d *Daemon) reconstructSessions() {
	log := d.logger.With("component", "rebuild")

	for _, item := range d.state.GetAllWorkItems() {
		if item.IsTerminal() {
			continue
		}
		if item.SessionID == "" {
			continue
		}
		if d.config.GetSession(item.SessionID) != nil {
			continue
		}

		var worktreePath string
		if worktreesDir, err := paths.WorktreesDir(); err == nil {
			worktreePath = filepath.Join(worktreesDir, item.SessionID)
		}

		repoPath := d.state.RepoPath
		if rp, ok := item.StepData["_repo_path"].(string); ok && rp != "" {
			repoPath = rp
		}

		sess := config.Session{
			ID:            item.SessionID,
			RepoPath:      repoPath,
			WorkTree:      worktreePath,
			Branch:        item.Branch,
			DaemonManaged: true,
			Autonomous:    true,
			Containerized: true,
			Started:       true,
		}
		d.config.AddSession(sess)

		log.Info("reconstructed session for work item",
			"workItem", item.ID, "sessionID", item.SessionID, "branch", item.Branch)
	}
}
