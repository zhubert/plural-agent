package daemon

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/zhubert/erg/internal/config"
	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/git"
	"github.com/zhubert/erg/internal/issues"
	"github.com/zhubert/erg/internal/workflow"
)

// pollForNewIssues checks for new issues and creates work items for them.
func (d *Daemon) pollForNewIssues(ctx context.Context) {
	log := d.logger.With("component", "issue-poller")

	if d.configSavePaused {
		log.Warn("config save failures exceed threshold, skipping new issue polling to prevent state drift")
		return
	}

	if d.repoFilter == "" && len(d.repoWorkflowFiles) == 0 {
		log.Debug("no repo filter set, skipping issue polling")
		return
	}

	// Check concurrency
	maxConcurrent := d.getMaxConcurrent()
	activeSlots := d.activeSlotCount()
	queuedCount := len(d.state.GetWorkItemsByState(daemonstate.WorkItemQueued))

	if activeSlots+queuedCount >= maxConcurrent {
		log.Debug("at concurrency limit, skipping poll",
			"active", activeSlots, "queued", queuedCount, "max", maxConcurrent)
		return
	}

	// Find matching repos
	repos := d.config.GetRepos()
	var pollingRepos []string
	for _, repoPath := range repos {
		if d.matchesRepoFilter(ctx, repoPath) {
			pollingRepos = append(pollingRepos, repoPath)
		}
	}

	if len(pollingRepos) == 0 {
		log.Debug("no repos to poll")
		return
	}

	pollCtx, cancel := context.WithTimeout(ctx, timeoutStandardOp)
	defer cancel()

	for _, repoPath := range pollingRepos {
		remaining := maxConcurrent - activeSlots - queuedCount
		if remaining <= 0 {
			break
		}

		wfCfg := d.getWorkflowConfig(repoPath)
		provider := issues.Source(wfCfg.Source.Provider)

		var fetchedIssues []issues.Issue
		if d.preseededIssue != nil {
			fetchedIssues = []issues.Issue{*d.preseededIssue}
			d.preseededIssue = nil // consume — only inject once
		} else {
			var err error
			fetchedIssues, err = d.fetchIssuesForProvider(pollCtx, repoPath, wfCfg)
			if err != nil {
				log.Debug("failed to fetch issues", "repo", repoPath, "provider", provider, "error", err)
				continue
			}
		}

		for _, issue := range fetchedIssues {
			if remaining <= 0 {
				break
			}

			// Check if we already have a work item for this issue
			if d.state.HasWorkItemForIssue(string(provider), issue.ID) {
				continue
			}

			// Also check config sessions for deduplication
			if d.hasExistingSession(repoPath, issue.ID) {
				continue
			}

			// Pre-flight: for GitHub issues, check if an open/merged PR already
			// addresses this issue. If so, unqueue it without spawning a session.
			if provider == issues.SourceGitHub {
				if skip := d.checkLinkedPRsAndUnqueue(pollCtx, repoPath, issue); skip {
					continue
				}
			}

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

			d.state.AddWorkItem(item)
			queuedCount++
			remaining--

			log.Info("queued new issue", "issue", issue.ID, "title", issue.Title, "provider", provider)
		}
	}
}

// fetchIssuesForProvider fetches issues using the appropriate provider.
func (d *Daemon) fetchIssuesForProvider(ctx context.Context, repoPath string, wfCfg *workflow.Config) ([]issues.Issue, error) {
	provider := issues.Source(wfCfg.Source.Provider)

	switch provider {
	case issues.SourceGitHub:
		label := wfCfg.Source.Filter.Label
		if label == "" {
			label = autonomousFilterLabel
		}
		ghIssues, err := d.gitService.FetchGitHubIssuesWithLabel(ctx, repoPath, label)
		if err != nil {
			return nil, err
		}
		result := make([]issues.Issue, 0, len(ghIssues))
		for _, ghIssue := range ghIssues {
			result = append(result, issues.Issue{
				ID:     strconv.Itoa(ghIssue.Number),
				Title:  ghIssue.Title,
				Body:   ghIssue.Body,
				URL:    ghIssue.URL,
				Source: issues.SourceGitHub,
			})
		}
		return result, nil

	case issues.SourceAsana, issues.SourceLinear:
		p := d.issueRegistry.GetProvider(provider)
		if p == nil {
			return nil, fmt.Errorf("provider %q not registered", provider)
		}
		return p.FetchIssues(ctx, repoPath, issues.FilterConfig{
			Label:   wfCfg.Source.Filter.Label,
			Project: wfCfg.Source.Filter.Project,
			Team:    wfCfg.Source.Filter.Team,
			Section: wfCfg.Source.Filter.Section,
		})

	default:
		return nil, fmt.Errorf("unknown provider %q", provider)
	}
}

// startQueuedItems starts coding on queued work items that have available slots.
// Before starting any new work, it first checks whether any set-aside await_review
// workflows are ready to continue — finishing existing work takes priority over
// starting new work.
func (d *Daemon) startQueuedItems(ctx context.Context) {
	if d.configSavePaused {
		d.logger.Warn("config save failures exceed threshold, skipping start of queued items to prevent state drift")
		return
	}

	maxConcurrent := d.getMaxConcurrent()
	queued := d.state.GetWorkItemsByState(daemonstate.WorkItemQueued)

	if len(queued) == 0 {
		return
	}

	// Give priority to set-aside workflows that are ready to continue.
	// processWaitItems checks await_review items for fired events (review
	// approved, PR merged, etc.) and advances them immediately, bypassing
	// the regular review-poll interval. Any slots consumed by resumed
	// workflows reduce the budget available for new queued items below.
	d.processWaitItems(ctx)

	for _, item := range queued {
		if d.activeSlotCount() >= maxConcurrent {
			break
		}

		sess := d.config.GetSession(item.SessionID)
		repoPath := ""
		if sess != nil {
			repoPath = sess.RepoPath
		} else if rp, ok := item.StepData["_repo_path"].(string); ok && rp != "" {
			repoPath = rp
		} else if d.repoFilter != "" {
			repoPath = d.findRepoPath(ctx)
		}

		engine := d.getEngine(repoPath)
		if engine == nil {
			d.logger.Error("no engine for repo", "repo", repoPath, "workItem", item.ID)
			continue
		}

		// Transition out of "queued" state before running the sync chain.
		// This is critical: if the chain takes the existing-PR shortcut
		// (errExistingPR in codingAction), startCoding never runs and
		// State stays WorkItemQueued. GetActiveWorkItems() excludes queued
		// items, so CI/review polling would never see this item, and
		// startQueuedItems would re-queue it on the next tick.
		d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
			it.State = daemonstate.WorkItemActive
		})

		// Initialize to the engine's start state
		startState := engine.GetStartState()
		d.state.AdvanceWorkItem(item.ID, startState, "idle")

		// Process through the engine — this will invoke codingAction.Execute
		// which calls startCoding to create the session and spawn the worker.
		d.executeSyncChain(ctx, item.ID, engine)
	}
}

// matchesRepoFilter checks if a repo path matches the daemon's repo filter.
func (d *Daemon) matchesRepoFilter(ctx context.Context, repoPath string) bool {
	// In multi-repo mode (no single repoFilter), all configured repos match.
	if d.repoFilter == "" && len(d.repoWorkflowFiles) > 0 {
		return true
	}
	if repoPath == d.repoFilter {
		return true
	}
	if strings.Contains(d.repoFilter, "/") && !strings.HasPrefix(d.repoFilter, "/") {
		remoteURL, err := d.gitService.GetRemoteOriginURL(ctx, repoPath)
		if err != nil {
			return false
		}
		ownerRepo := git.ExtractOwnerRepo(remoteURL)
		return ownerRepo == d.repoFilter
	}
	return false
}

// checkLinkedPRsAndUnqueue checks if a GitHub issue already has an open or merged PR
// addressing it. If the PR is already merged, the issue is unqueued and marked
// completed. If the PR is open, the daemon adopts it: it creates a work item and
// session, then advances to the appropriate wait state (e.g. await_ci) so the
// normal tick loop monitors CI and review status.
func (d *Daemon) checkLinkedPRsAndUnqueue(ctx context.Context, repoPath string, issue issues.Issue) bool {
	log := d.logger.With("issue", issue.ID, "component", "pre-flight")

	issueNum, err := strconv.Atoi(issue.ID)
	if err != nil {
		return false
	}

	linkedPRs, err := d.gitService.GetLinkedPRsForIssue(ctx, repoPath, issueNum)
	if err != nil {
		log.Debug("failed to check linked PRs, continuing with issue", "error", err)
		return false
	}

	if len(linkedPRs) == 0 {
		return false
	}

	pr := linkedPRs[0]

	log.Info("existing PR found for issue",
		"pr", pr.Number, "state", pr.State, "branch", pr.HeadRefName)

	item := &daemonstate.WorkItem{
		ID: fmt.Sprintf("%s-%s", repoPath, issue.ID),
		IssueRef: config.IssueRef{
			Source: string(issues.SourceGitHub),
			ID:     issue.ID,
			Title:  issue.Title,
			URL:    issue.URL,
		},
		Branch: pr.HeadRefName,
		PRURL:  pr.URL,
		StepData: map[string]any{
			"_repo_path": repoPath,
		},
	}
	d.state.AddWorkItem(item)

	if pr.State == git.PRStateMerged {
		// PR already merged — just unqueue and mark completed.
		comment := fmt.Sprintf(
			"PR #%d has already been merged. Removing from the queue.",
			pr.Number,
		)
		d.unqueueIssue(ctx, *item, comment)
		if err := d.state.MarkWorkItemTerminal(item.ID, true); err != nil {
			log.Debug("failed to mark pre-flight item terminal", "error", err)
		}
		log.Info("linked PR already merged, marked completed", "pr", pr.Number)
		return true
	}

	// PR is open — adopt it into the workflow so the daemon monitors CI/review.
	// Keep the queued label as a durable safety net: if the daemon crashes before
	// persisting state, the label ensures this issue is rediscovered on next start.
	// HasWorkItemForIssue prevents re-adoption on subsequent polls within the same run.
	// The label is removed later when the PR is actually merged (normal workflow path).

	// Use the workflow engine to find the right wait state (e.g. await_ci).
	// We cannot hardcode state names because template expansion namespaces
	// them (e.g. "await_ci" becomes "_t_ci_await_ci"). Search by event type
	// in priority order: CI first, then review, then mergeable.
	// Compute this before creating the session to avoid orphaned session entries on failure.
	engine := d.getEngine(repoPath)
	recoveryStep := engine.FindFirstWaitStateByEvents([]string{
		"ci.complete",
		"ci.wait_for_checks",
		"pr.reviewed",
		"pr.mergeable",
	})
	if recoveryStep == "" {
		log.Warn("no wait state found in workflow, cannot adopt PR")
		d.unqueueIssue(ctx, *item, "Cannot adopt PR: no matching wait state found in workflow configuration. Please check your workflow definition.")
		if err := d.state.MarkWorkItemTerminal(item.ID, false); err != nil {
			log.Debug("failed to mark work item terminal after adoption failure", "error", err)
		}
		d.state.SetErrorMessage(item.ID, "no wait state found in workflow for PR adoption")
		return true
	}

	// Create a synthetic session so GetSession() works for CI/review polling.
	sessionID := uuid.New().String()
	d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
		it.SessionID = sessionID
	})

	sess := config.Session{
		ID:            sessionID,
		RepoPath:      repoPath,
		Branch:        pr.HeadRefName,
		DaemonManaged: true,
		Autonomous:    true,
		Containerized: true,
		Started:       true,
		IssueRef: &config.IssueRef{
			Source: string(issues.SourceGitHub),
			ID:     issue.ID,
			Title:  issue.Title,
			URL:    issue.URL,
		},
	}
	d.config.AddSession(sess)

	d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
		now := time.Now()
		it.State = daemonstate.WorkItemActive
		it.CurrentStep = recoveryStep
		it.Phase = "idle"
		it.StepEnteredAt = now
		it.UpdatedAt = now
	})

	log.Info("adopted open PR into workflow",
		"pr", pr.Number, "branch", pr.HeadRefName, "step", recoveryStep, "sessionID", sessionID)
	return true
}

// reconcileClosedIssues checks non-terminal work items to see if their underlying
// issue has been closed externally. If so, the work item is cancelled: any running
// worker is stopped, the queued label is removed, and the item is marked terminal.
// This prevents closed issues from lingering as "active" in the dashboard.
func (d *Daemon) reconcileClosedIssues(ctx context.Context) {
	if time.Since(d.lastReconcileAt) < defaultReconcileInterval {
		return
	}
	d.lastReconcileAt = time.Now()

	log := d.logger.With("component", "issue-reconciler")

	for _, item := range d.state.GetActiveWorkItems() {
		if item.IsTerminal() {
			continue
		}

		repoPath := d.resolveRepoPath(ctx, item)
		if repoPath == "" {
			continue
		}

		closed, err := d.isIssueClosed(ctx, repoPath, item)
		if err != nil {
			log.Debug("failed to check issue state", "workItem", item.ID, "error", err)
			continue
		}

		if !closed {
			continue
		}

		log.Info("issue closed externally, cancelling work item",
			"workItem", item.ID, "issue", item.IssueRef.ID, "source", item.IssueRef.Source)

		// Cancel any running worker for this item
		d.mu.Lock()
		w, ok := d.workers[item.ID]
		if ok {
			delete(d.workers, item.ID)
		}
		d.mu.Unlock()
		if ok {
			w.Cancel()
		}

		// Remove the queued label so it doesn't get re-queued
		d.unqueueIssue(ctx, item, "Issue was closed externally. Cancelling work.")

		// Mark terminal
		if err := d.state.MarkWorkItemTerminal(item.ID, false); err != nil {
			log.Debug("failed to mark work item terminal", "workItem", item.ID, "error", err)
		}
		d.state.SetErrorMessage(item.ID, "issue closed externally")
	}

	// Also check queued items that haven't started yet
	for _, item := range d.state.GetWorkItemsByState(daemonstate.WorkItemQueued) {
		repoPath := ""
		if rp, ok := item.StepData["_repo_path"].(string); ok && rp != "" {
			repoPath = rp
		} else {
			repoPath = d.resolveRepoPath(ctx, item)
		}
		if repoPath == "" {
			continue
		}

		closed, err := d.isIssueClosed(ctx, repoPath, item)
		if err != nil {
			log.Debug("failed to check queued issue state", "workItem", item.ID, "error", err)
			continue
		}

		if !closed {
			continue
		}

		log.Info("queued issue closed externally, removing",
			"workItem", item.ID, "issue", item.IssueRef.ID)

		d.unqueueIssue(ctx, item, "Issue was closed externally. Removing from queue.")

		if err := d.state.MarkWorkItemTerminal(item.ID, false); err != nil {
			log.Debug("failed to mark queued item terminal", "workItem", item.ID, "error", err)
		}
		d.state.SetErrorMessage(item.ID, "issue closed externally")
	}
}

// isIssueClosed checks whether the issue backing a work item is closed.
// Uses the IssueStateChecker interface when a provider is registered,
// falling back to GitService.GetIssueState for GitHub if no provider is available.
func (d *Daemon) isIssueClosed(ctx context.Context, repoPath string, item daemonstate.WorkItem) (bool, error) {
	checkCtx, cancel := context.WithTimeout(ctx, timeoutQuickAPI)
	defer cancel()

	source := issues.Source(item.IssueRef.Source)

	// Try the provider registry first (works for all sources including GitHub)
	if d.issueRegistry != nil {
		if p := d.issueRegistry.GetProvider(source); p != nil {
			if sc, ok := p.(issues.IssueStateChecker); ok {
				return sc.IsIssueClosed(checkCtx, repoPath, item.IssueRef.ID)
			}
		}
	}

	// Fallback: direct GitService call for GitHub when no provider is registered
	if source == issues.SourceGitHub {
		state, err := d.gitService.GetIssueState(checkCtx, repoPath, item.IssueRef.ID)
		if err != nil {
			return false, err
		}
		return state == "CLOSED", nil
	}

	return false, nil
}

// hasExistingSession checks if a session already exists for the given issue.
func (d *Daemon) hasExistingSession(repoPath, issueID string) bool {
	for _, sess := range d.config.GetSessions() {
		if sess.RepoPath == repoPath && sess.IssueRef != nil && sess.IssueRef.ID == issueID {
			return true
		}
	}
	return false
}
