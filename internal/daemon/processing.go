package daemon

import (
	"context"
	"fmt"
	"maps"
	osexec "os/exec"
	"strings"
	"time"

	"github.com/zhubert/erg/internal/config"
	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/workflow"
)

// collectCompletedWorkers checks for finished Claude sessions and advances work items.
func (d *Daemon) collectCompletedWorkers(ctx context.Context) {
	// Phase 1: collect finished workers under lock and remove them from d.workers.
	// We release d.mu before processing because the handlers (handleAsyncComplete,
	// handleFeedbackComplete) can re-acquire d.mu via executeSyncChain -> action ->
	// createWorkerWithPrompt/refreshStaleSession. sync.Mutex is not reentrant, so
	// holding it here while calling handlers would cause a deadlock in any custom
	// workflow that routes the sync chain through an async action (ai.code, ai.fix_ci).
	var completed []struct {
		workItemID string
		exitErr    error
	}
	d.mu.Lock()
	for workItemID, w := range d.workers {
		if !w.Done() {
			continue
		}
		completed = append(completed, struct {
			workItemID string
			exitErr    error
		}{workItemID, w.ExitError()})
		delete(d.workers, workItemID)
	}
	d.mu.Unlock()

	// Phase 2: process each completed worker without holding d.mu.
	for _, cw := range completed {
		item, ok := d.state.GetWorkItem(cw.workItemID)
		if !ok {
			continue
		}

		if cw.exitErr != nil {
			d.logger.Warn("worker completed with error", "workItem", cw.workItemID, "step", item.CurrentStep, "phase", item.Phase, "error", cw.exitErr)
		} else {
			d.logger.Info("worker completed", "workItem", cw.workItemID, "step", item.CurrentStep, "phase", item.Phase)
		}

		switch item.Phase {
		case "async_pending":
			// If the worker failed due to Docker being unavailable,
			// mark for retry instead of permanent failure.
			if cw.exitErr != nil && isDockerError(cw.exitErr) {
				d.logger.Warn("worker failed due to Docker unavailability, will retry",
					"workItem", cw.workItemID, "error", cw.exitErr)
				d.state.UpdateWorkItem(cw.workItemID, func(it *daemonstate.WorkItem) {
					it.Phase = "docker_pending"
					it.UpdatedAt = time.Now()
				})
				d.state.SetErrorMessage(cw.workItemID, fmt.Sprintf("Docker unavailable: %v", cw.exitErr))
				continue
			}
			// Main async action completed (e.g., coding)
			d.handleAsyncComplete(ctx, item, cw.exitErr)

		case "addressing_feedback":
			// Feedback addressing completed -- push changes (skip if worker failed)
			if cw.exitErr != nil {
				d.logger.Warn("skipping push after failed feedback session", "workItem", cw.workItemID, "error", cw.exitErr)
				d.state.SetErrorMessage(item.ID, fmt.Sprintf("feedback session failed: %v", cw.exitErr))
				d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
					it.Phase = "idle"
					it.UpdatedAt = time.Now()
				})
			} else {
				d.handleFeedbackComplete(ctx, item)
			}
		}
	}
}

// handleAsyncComplete handles the completion of an async action.
// exitErr is non-nil when the worker exited due to an error (API error, etc.).
func (d *Daemon) handleAsyncComplete(ctx context.Context, item daemonstate.WorkItem, exitErr error) {
	log := d.logger.With("workItem", item.ID, "step", item.CurrentStep)

	sess := d.config.GetSession(item.SessionID)
	repoPath := ""
	if sess != nil {
		repoPath = sess.RepoPath
	}

	engine := d.getEngine(repoPath)
	if engine == nil {
		log.Error("no engine for repo", "repo", repoPath)
		return
	}

	// Get state definition for after-hooks
	state := engine.GetState(item.CurrentStep)

	// Safety net: if the session already has a PR created or merged (e.g., Claude
	// ran `gh pr create` in the container bash, or a race condition), the workflow
	// engine's open_pr step would fail trying to create a duplicate PR. Handle
	// these cases gracefully but log a warning -- in daemon mode, the worker should
	// never create or merge PRs; the workflow engine handles those steps via
	// open_pr and merge actions.
	if sess != nil && sess.PRMerged {
		log.Warn("PR already merged outside workflow, fast-pathing to completed")
		if state != nil {
			d.runHooks(ctx, state.After, item, sess)
		}
		d.state.AdvanceWorkItem(item.ID, "done", "idle")
		d.state.MarkWorkItemTerminal(item.ID, true)

		mergeState := engine.GetState("merge")
		if mergeState != nil {
			d.runHooks(ctx, mergeState.After, item, sess)
		}
		return
	}

	if sess != nil && sess.PRCreated && item.CurrentStep == "coding" {
		log.Warn("PR already created outside workflow, skipping open_pr step")
		if state != nil {
			d.runHooks(ctx, state.After, item, sess)
		}
		prState := engine.GetState("open_pr")
		if prState != nil {
			d.runHooks(ctx, prState.After, item, sess)
		}
		d.state.AdvanceWorkItem(item.ID, "await_review", "idle")
		return
	}

	// If a format_command was configured on the coding action, run the formatter
	// now (after coding succeeds, before the PR is created). This is a
	// daemon-side safety net: Claude is also instructed to run the formatter
	// before each commit, but this ensures any uncommitted formatting changes
	// are captured even if Claude skipped that step.
	if exitErr == nil {
		if formatCmd, ok := item.StepData["_format_command"].(string); ok && formatCmd != "" {
			formatMsg, _ := item.StepData["_format_message"].(string)
			if formatMsg == "" {
				formatMsg = "Apply auto-formatting"
			}
			formatParams := workflow.NewParamHelper(map[string]any{
				"command": formatCmd,
				"message": formatMsg,
			})
			if fmtErr := d.runFormatter(ctx, item, formatParams); fmtErr != nil {
				log.Warn("post-coding formatter failed (non-fatal)", "error", fmtErr)
			}
		}
	}

	// For ai.review steps, check review result from MCP tool (StepData) first,
	// then fall back to reading the .erg/ai_review.json file for backward compat
	// with custom prompts. If the review blocked (passed=false), treat as failure
	// so the engine follows the error edge.
	if exitErr == nil && state != nil && state.Action == "ai.review" && sess != nil {
		// Re-fetch item to see StepData updated by the submit_review MCP tool handler.
		if fresh, ok := d.state.GetWorkItem(item.ID); !ok {
			log.Warn("work item deleted during async completion", "itemID", item.ID)
			return
		} else {
			item = fresh
		}

		reviewPassed := true
		reviewSummary := ""

		// Check if submit_review MCP tool already stored the result in StepData.
		if rp, ok := item.StepData["review_passed"].(bool); ok {
			reviewPassed = rp
			reviewSummary, _ = item.StepData["ai_review_summary"].(string)
		} else {
			// Fall back to reading .erg/ai_review.json (backward compat with custom prompts)
			reviewPassed, reviewSummary = d.readAIReviewResult(sess)
			d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
				it.StepData["review_passed"] = reviewPassed
				it.StepData["ai_review_summary"] = reviewSummary
			})
			// Re-fetch item so workItemView sees the updated StepData.
			if fresh, ok := d.state.GetWorkItem(item.ID); ok {
				item = fresh
			}
		}

		if !reviewPassed {
			exitErr = fmt.Errorf("AI review blocked: %s", reviewSummary)
		}
	}

	// For ai.plan steps, store the repo path in StepData (so workItemView can
	// resolve it after the planning session is cleaned up) and release the session.
	if exitErr == nil && state != nil && state.Action == "ai.plan" && sess != nil {
		d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
			if it.StepData == nil {
				it.StepData = make(map[string]any)
			}
			it.StepData["_repo_path"] = sess.RepoPath
		})
		d.cleanupPlanningSession(ctx, item.SessionID)
		// Re-fetch item so workItemView sees the updated StepData.
		if fresh, ok := d.state.GetWorkItem(item.ID); ok {
			item = fresh
		}
	}

	// Normal async completion -- advance via engine
	view := d.workItemView(item)
	success := exitErr == nil
	result, err := engine.AdvanceAfterAsync(view, success)
	if err != nil {
		log.Error("failed to advance after async", "error", err)
		d.state.SetErrorMessage(item.ID, err.Error())
		d.state.MarkWorkItemTerminal(item.ID, false)
		return
	}

	// Run after-hooks
	if state != nil && sess != nil {
		d.runHooks(ctx, state.After, item, sess)
	}

	if result.Terminal {
		d.state.AdvanceWorkItem(item.ID, result.NewStep, result.NewPhase)
		d.state.MarkWorkItemTerminal(item.ID, result.TerminalOK)
		return
	}

	// For task states with sync next actions, execute them inline
	d.state.AdvanceWorkItem(item.ID, result.NewStep, result.NewPhase)

	// If the next step is a sync task (like open_pr), execute it now
	d.executeSyncChain(ctx, item.ID, engine)
}

// executeSyncChain executes synchronous task states in sequence until
// hitting an async task, a wait state, or a terminal state.
// It re-fetches the work item at the start of each iteration so it always
// sees the latest state after AdvanceWorkItem / UpdateWorkItem calls.
func (d *Daemon) executeSyncChain(ctx context.Context, itemID string, engine *workflow.Engine) {
	for {
		// Re-fetch a fresh snapshot at the top of every iteration so we see
		// any state changes made by AdvanceWorkItem / UpdateWorkItem calls
		// from the previous iteration.
		item, ok := d.state.GetWorkItem(itemID)
		if !ok {
			return
		}

		// Run before-hooks for the current step (blocking -- failure stops execution)
		beforeHooks := engine.GetBeforeHooks(item.CurrentStep)
		if len(beforeHooks) > 0 {
			sess := d.config.GetSession(item.SessionID)
			if sess == nil {
				d.logger.Warn("session not found, skipping before-hooks", "workItem", item.ID, "step", item.CurrentStep, "session", item.SessionID)
			} else {
				hookCtx := workflow.HookContext{
					RepoPath:   sess.RepoPath,
					Branch:     item.Branch,
					SessionID:  item.SessionID,
					IssueID:    item.IssueRef.ID,
					IssueTitle: item.IssueRef.Title,
					IssueURL:   item.IssueRef.URL,
					PRURL:      item.PRURL,
					WorkTree:   sess.WorkTree,
					Provider:   item.IssueRef.Source,
				}
				if err := workflow.RunBeforeHooks(ctx, beforeHooks, hookCtx, d.logger); err != nil {
					d.logger.Error("before hook failed", "workItem", item.ID, "step", item.CurrentStep, "error", err)
					state := engine.GetState(item.CurrentStep)
					if state != nil && state.Error != "" {
						d.state.AdvanceWorkItem(item.ID, state.Error, "idle")
						continue // follow error edge
					}
					d.state.SetErrorMessage(item.ID, err.Error())
					d.state.MarkWorkItemTerminal(item.ID, false)
					return
				}
			}
		}

		view := d.workItemView(item)
		result, err := engine.ProcessStep(ctx, view)
		if err != nil {
			d.logger.Error("sync chain error", "workItem", item.ID, "step", item.CurrentStep, "error", err)
			d.state.SetErrorMessage(item.ID, err.Error())
			d.state.MarkWorkItemTerminal(item.ID, false)
			return
		}

		if result.Terminal {
			d.state.AdvanceWorkItem(item.ID, result.NewStep, result.NewPhase)
			d.state.MarkWorkItemTerminal(item.ID, result.TerminalOK)
			if !result.TerminalOK {
				errMsg := ""
				if e, ok := item.StepData["_last_error"].(string); ok {
					errMsg = e
				}
				if errMsg == "" {
					if e, ok := result.Data["_last_error"].(string); ok {
						errMsg = e
					}
				}
				if errMsg != "" {
					d.state.SetErrorMessage(item.ID, errMsg)
				}
				d.logger.Error("work item failed", "workItem", item.ID, "step", item.CurrentStep, "error", errMsg)
			}
			return
		}

		// Run after-hooks
		if len(result.Hooks) > 0 {
			sess := d.config.GetSession(item.SessionID)
			if sess != nil {
				d.runHooks(ctx, result.Hooks, item, sess)
			}
		}

		// Merge data and apply known fields to the work item (via state lock)
		if result.Data != nil {
			d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
				maps.Copy(it.StepData, result.Data)
				if prURL, ok := result.Data["pr_url"].(string); ok && prURL != "" {
					it.PRURL = prURL
					it.UpdatedAt = time.Now()
				}
			})
		}

		d.state.AdvanceWorkItem(item.ID, result.NewStep, result.NewPhase)

		// Stop if we hit an async pending state or a wait state
		if result.NewPhase == "async_pending" {
			return
		}
		nextState := engine.GetState(result.NewStep)
		if nextState != nil && nextState.Type == workflow.StateTypeWait {
			return
		}
	}
}

// handleFeedbackComplete handles the transition after Claude finishes addressing feedback.
func (d *Daemon) handleFeedbackComplete(ctx context.Context, item daemonstate.WorkItem) {
	log := d.logger.With("workItem", item.ID, "branch", item.Branch)

	// Push changes
	if err := d.pushChanges(ctx, item); err != nil {
		log.Error("failed to push changes", "error", err)
		d.state.SetErrorMessage(item.ID, fmt.Sprintf("push failed: %v", err))
		d.state.MarkWorkItemTerminal(item.ID, false)
		return
	}

	// Run review after-hooks
	sess := d.config.GetSession(item.SessionID)
	if sess != nil {
		engine := d.getEngine(sess.RepoPath)
		if engine != nil {
			state := engine.GetState(item.CurrentStep)
			if state != nil {
				d.runHooks(ctx, state.After, item, sess)
			}
		}
	}

	// Back to idle phase for the wait state to continue polling
	d.state.AdvanceWorkItem(item.ID, item.CurrentStep, "idle")

	d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
		it.FeedbackRounds++
		it.UpdatedAt = time.Now()
	})
	log.Info("pushed feedback changes", "round", item.FeedbackRounds)
}

// processWorkItems checks active items via the engine.
func (d *Daemon) processWorkItems(ctx context.Context) {
	// Check wait-state items (review, CI) at the review poll interval
	if time.Since(d.lastReviewPollAt) >= d.reviewPollInterval {
		d.processWaitItems(ctx)
		d.lastReviewPollAt = time.Now()
	}

	// Check CI items on every tick (they don't need the slower interval)
	d.processCIItems(ctx)
}

// processWaitItems processes items in wait states for review events.
func (d *Daemon) processWaitItems(ctx context.Context) {
	for _, item := range d.state.GetActiveWorkItems() {
		if item.IsTerminal() || item.Phase == "async_pending" || item.Phase == "addressing_feedback" {
			continue
		}

		sess := d.config.GetSession(item.SessionID)
		if sess == nil {
			continue
		}

		engine := d.getEngine(sess.RepoPath)
		if engine == nil {
			continue
		}

		state := engine.GetState(item.CurrentStep)
		if state == nil || state.Type != workflow.StateTypeWait {
			continue
		}

		// Skip CI events — they're handled by processCIItems at higher frequency
		if state.Event == "ci.complete" || state.Event == "ci.wait_for_checks" {
			continue
		}

		view := d.workItemView(item)
		result, err := engine.ProcessStep(ctx, view)
		if err != nil {
			d.logger.Error("wait step error", "workItem", item.ID, "error", err)
			continue
		}

		// Compare against the view (snapshot before ProcessStep) rather than
		// the live item. Event handlers like addressFeedback may mutate
		// item.Phase during ProcessStep; comparing against the stale item
		// would incorrectly detect a "change" and overwrite the handler's
		// phase update.
		if result.NewStep != view.CurrentStep || result.NewPhase != view.Phase {
			if len(result.Hooks) > 0 {
				d.runHooks(ctx, result.Hooks, item, sess)
			}
			// Merge event data into step data so downstream choice states
			// can evaluate it.
			if result.Data != nil {
				d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
					maps.Copy(it.StepData, result.Data)
				})
			}
			d.state.AdvanceWorkItem(item.ID, result.NewStep, result.NewPhase)
			if result.Terminal {
				d.state.MarkWorkItemTerminal(item.ID, result.TerminalOK)
			} else {
				// Continue sync chain if next is a sync task
				d.executeSyncChain(ctx, item.ID, engine)
			}
		}
	}
}

// processCIItems processes items waiting for CI events.
func (d *Daemon) processCIItems(ctx context.Context) {
	for _, item := range d.state.GetActiveWorkItems() {
		if item.IsTerminal() || item.Phase == "async_pending" || item.Phase == "addressing_feedback" {
			continue
		}

		sess := d.config.GetSession(item.SessionID)
		if sess == nil {
			continue
		}

		engine := d.getEngine(sess.RepoPath)
		if engine == nil {
			continue
		}

		state := engine.GetState(item.CurrentStep)
		if state == nil || state.Type != workflow.StateTypeWait {
			continue
		}
		if state.Event != "ci.complete" && state.Event != "ci.wait_for_checks" {
			continue
		}

		view := d.workItemView(item)
		result, err := engine.ProcessStep(ctx, view)
		if err != nil {
			d.logger.Error("ci step error", "workItem", item.ID, "error", err)
			continue
		}

		// Compare against the view snapshot, not the live item (see processWaitItems).
		if result.NewStep != view.CurrentStep || result.NewPhase != view.Phase {
			if len(result.Hooks) > 0 {
				d.runHooks(ctx, result.Hooks, item, sess)
			}
			// Merge event data (e.g. ci_passed) into step data so downstream
			// choice states can evaluate it.
			if result.Data != nil {
				d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
					maps.Copy(it.StepData, result.Data)
				})
			}
			d.state.AdvanceWorkItem(item.ID, result.NewStep, result.NewPhase)
			if result.Terminal {
				d.state.MarkWorkItemTerminal(item.ID, result.TerminalOK)
			} else {
				d.executeSyncChain(ctx, item.ID, engine)
			}
		}
	}
}

// processIdleSyncItems finds items in idle phase sitting on synchronous task steps
// (e.g. "merge") and executes them. This catches items that were advanced to a sync
// task step during recovery but never had executeSyncChain called.
func (d *Daemon) processIdleSyncItems(ctx context.Context) {
	for _, item := range d.state.GetActiveWorkItems() {
		if item.Phase != "idle" || item.IsTerminal() {
			continue
		}

		sess := d.config.GetSession(item.SessionID)
		if sess == nil {
			continue
		}

		engine := d.getEngine(sess.RepoPath)
		if engine == nil {
			continue
		}

		state := engine.GetState(item.CurrentStep)
		if state == nil || state.Type != workflow.StateTypeTask {
			continue
		}

		d.logger.Info("executing idle sync task", "workItem", item.ID, "step", item.CurrentStep)
		d.executeSyncChain(ctx, item.ID, engine)
	}
}

// processRetryItems checks for items in retry_pending phase whose delay has elapsed,
// and re-executes them via the engine.
func (d *Daemon) processRetryItems(ctx context.Context) {
	for _, item := range d.state.GetActiveWorkItems() {
		if item.Phase != "retry_pending" {
			continue
		}

		// Check if the retry delay has elapsed
		if retryAfter, ok := item.StepData["_retry_after"].(string); ok {
			t, err := time.Parse(time.RFC3339, retryAfter)
			if err == nil && time.Now().Before(t) {
				continue // Delay hasn't elapsed yet
			}
		}

		sess := d.config.GetSession(item.SessionID)
		if sess == nil {
			continue
		}

		engine := d.getEngine(sess.RepoPath)
		if engine == nil {
			continue
		}

		d.logger.Info("retry delay elapsed, re-executing", "workItem", item.ID, "step", item.CurrentStep)

		// Reset to idle so the engine will re-process the task state
		d.state.AdvanceWorkItem(item.ID, item.CurrentStep, "idle")
		d.executeSyncChain(ctx, item.ID, engine)
	}
}

// isDockerError returns true if the error indicates Docker/container runtime
// is unavailable. These errors are transient and should trigger retry rather
// than permanent failure.
func isDockerError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Cannot connect to the Docker daemon") ||
		strings.Contains(msg, "docker daemon is not running") ||
		strings.Contains(msg, "Is the docker daemon running")
}

// resumeDockerPendingItems resets items that were paused due to Docker
// unavailability back to a retryable state.
func (d *Daemon) resumeDockerPendingItems() {
	for _, item := range d.state.GetActiveWorkItems() {
		if item.Phase != "docker_pending" {
			continue
		}
		d.logger.Info("resuming Docker-pending item", "workItem", item.ID, "step", item.CurrentStep)
		d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
			it.Phase = "idle"
			it.State = daemonstate.WorkItemActive
			it.UpdatedAt = time.Now()
		})
		d.state.SetErrorMessage(item.ID, "")
	}
}

// checkDockerHealth probes Docker availability. Returns true if Docker is OK.
// When Docker is down, it logs a warning once and returns false. When Docker
// recovers after being down, it logs recovery and returns true.
func (d *Daemon) checkDockerHealth() bool {
	var err error
	if d.dockerHealthCheck != nil {
		err = d.dockerHealthCheck()
	} else {
		err = defaultDockerHealthCheck()
	}

	if err != nil {
		d.dockerDown = true
		if !d.dockerDownLogged {
			d.logger.Warn("docker is unavailable, pausing work dispatch", "error", err)
			d.dockerDownLogged = true
		}
		return false
	}

	if d.dockerDown {
		d.logger.Info("docker recovered, resuming work dispatch")
		d.dockerDown = false
		d.dockerDownLogged = false
		d.resumeDockerPendingItems()
	}
	return true
}

// defaultDockerHealthCheck runs "docker version" with a 5-second timeout.
func defaultDockerHealthCheck() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return osexec.CommandContext(ctx, "docker", "version").Run()
}

// runHooks runs the after-hooks for a given workflow step.
func (d *Daemon) runHooks(ctx context.Context, hooks []workflow.HookConfig, item daemonstate.WorkItem, sess *config.Session) {
	if len(hooks) == 0 {
		return
	}

	hookCtx := workflow.HookContext{
		RepoPath:   sess.RepoPath,
		Branch:     item.Branch,
		SessionID:  item.SessionID,
		IssueID:    item.IssueRef.ID,
		IssueTitle: item.IssueRef.Title,
		IssueURL:   item.IssueRef.URL,
		PRURL:      item.PRURL,
		WorkTree:   sess.WorkTree,
		Provider:   item.IssueRef.Source,
	}

	workflow.RunHooks(ctx, hooks, hookCtx, d.logger)
}
