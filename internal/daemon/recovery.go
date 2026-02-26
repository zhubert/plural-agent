package daemon

import (
	"context"
	"path/filepath"
	"time"

	"github.com/zhubert/erg/internal/config"
	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/git"
	"github.com/zhubert/erg/internal/paths"
)

// reconstructSessions creates minimal config.Session objects for recovered work items
// whose sessions are missing from the in-memory config. On daemon restart, the config
// starts empty (AgentConfig.Save() is a no-op), so all sessions are lost. Without
// reconstruction, GetSession() returns nil and every polling path silently skips
// recovered items forever.
func (d *Daemon) reconstructSessions() {
	log := d.logger.With("component", "recovery")

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

		// Only mark PRCreated if the item is past the coding step.
		prCreated := item.CurrentStep != "" && item.CurrentStep != "coding"

		// Reconstruct the worktree path from the session ID. Worktrees follow
		// a deterministic layout: <worktreesDir>/<sessionID>. Without this,
		// cleanup operations fail because they try to run `git worktree remove ""`.
		var worktreePath string
		if worktreesDir, err := paths.WorktreesDir(); err == nil {
			worktreePath = filepath.Join(worktreesDir, item.SessionID)
		}

		sess := config.Session{
			ID:            item.SessionID,
			RepoPath:      d.state.RepoPath,
			WorkTree:      worktreePath,
			Branch:        item.Branch,
			DaemonManaged: true,
			Autonomous:    true,
			Containerized: true,
			Started:       true,
			PRCreated:     prCreated,
		}
		d.config.AddSession(sess)

		log.Info("reconstructed session for recovered work item",
			"workItem", item.ID, "sessionID", item.SessionID, "branch", item.Branch)
	}
}

// recoverFromState reconciles daemon state with reality after a restart.
func (d *Daemon) recoverFromState(ctx context.Context) {
	if d.state == nil {
		return
	}

	// Reconstruct sessions before recovery so GetSession() works for all items.
	d.reconstructSessions()

	items := d.state.GetAllWorkItems()
	if len(items) == 0 {
		return
	}

	log := d.logger.With("component", "recovery")
	log.Info("recovering from previous state", "workItems", len(items))

	for _, item := range items {
		if item.IsTerminal() {
			continue
		}

		log := log.With("workItem", item.ID, "step", item.CurrentStep, "phase", item.Phase, "branch", item.Branch)

		switch item.Phase {
		case "async_pending":
			// Worker was running but daemon restarted — no worker exists
			d.recoverAsyncPending(ctx, &item, log)

		case "addressing_feedback", "pushing":
			// Worker or push was in-flight — check actual PR state to decide next step
			d.recoverWaitPhase(ctx, &item, log)

		case "retry_pending":
			// Was waiting to retry — reset to idle so it retries on next tick
			log.Info("was retry_pending, resetting to idle for immediate retry")
			d.state.AdvanceWorkItem(item.ID, item.CurrentStep, "idle")

		default:
			// "idle" or empty — normal wait/queue state
			if item.State == daemonstate.WorkItemQueued {
				log.Info("work item queued, will start on next tick")
			} else {
				log.Info("work item in wait state, resuming polling")
			}
		}
	}
}

// recoverWaitPhase handles recovery for items that were in addressing_feedback or pushing
// phase when the daemon stopped. Instead of blindly resetting to idle, it checks the
// actual PR state on GitHub to determine the correct recovery action.
func (d *Daemon) recoverWaitPhase(ctx context.Context, item *daemonstate.WorkItem, log interface{ Info(string, ...any) }) {
	sess := d.config.GetSession(item.SessionID)
	if sess == nil {
		log.Info("session not found, resetting to idle", "phase", item.Phase)
		d.resetPhaseToIdle(item)
		return
	}

	if item.Branch == "" {
		log.Info("no branch, resetting to idle", "phase", item.Phase)
		d.resetPhaseToIdle(item)
		return
	}

	pollCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	prState, err := d.gitService.GetPRState(pollCtx, sess.RepoPath, item.Branch)
	if err != nil {
		log.Info("could not check PR state, resetting to idle", "phase", item.Phase, "error", err)
		d.resetPhaseToIdle(item)
		return
	}

	if prState == git.PRStateMerged {
		log.Info("PR already merged, marking completed")
		d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
			it.CurrentStep = "done"
			it.Phase = "idle"
			it.State = daemonstate.WorkItemCompleted
			now := time.Now()
			it.CompletedAt = &now
			it.UpdatedAt = now
		})
		return
	}

	if prState == git.PRStateClosed {
		log.Info("PR was closed, marking failed")
		d.state.SetErrorMessage(item.ID, "PR was closed while daemon was offline")
		d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
			it.Phase = "idle"
			it.UpdatedAt = time.Now()
		})
		d.state.MarkWorkItemTerminal(item.ID, false)
		return
	}

	// PR is open — reset to idle so the normal tick loop can process it.
	// We intentionally do NOT advance to "merge" here even if the review is
	// approved, because the merge step is a sync task that requires
	// executeSyncChain() to run. Recovery only restores state; the tick loop
	// handles execution.
	log.Info("PR open, resetting to idle for continued polling", "phase", item.Phase)
	d.resetPhaseToIdle(item)
}

// resetPhaseToIdle resets a work item's phase to idle while preserving its current step.
func (d *Daemon) resetPhaseToIdle(item *daemonstate.WorkItem) {
	d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
		now := time.Now()
		it.Phase = "idle"
		if it.StepEnteredAt.IsZero() {
			it.StepEnteredAt = now
		}
		it.UpdatedAt = now
	})
}

// recoveryGracePeriod is how long after a work item was last updated before
// recovery will treat it as stale. This prevents a race where a freshly started
// session gets cleaned up because recovery runs before the worker has time to
// make progress (e.g., create a PR).
const recoveryGracePeriod = 2 * time.Minute

// recoverAsyncPending handles recovery when a worker was active but daemon restarted.
func (d *Daemon) recoverAsyncPending(ctx context.Context, item *daemonstate.WorkItem, log interface{ Info(string, ...any) }) {
	if item.Branch == "" {
		log.Info("no branch, re-queuing")
		d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
			it.State = daemonstate.WorkItemQueued
			it.CurrentStep = ""
			it.Phase = "idle"
			it.UpdatedAt = time.Now()
		})
		return
	}

	// Skip items that were updated very recently — they likely have a live
	// worker that hasn't finished yet. Without this guard, recovery can race
	// with a freshly started session and clean it up before it produces a PR.
	if time.Since(item.UpdatedAt) < recoveryGracePeriod {
		log.Info("item updated recently, skipping recovery to avoid racing with active worker",
			"updatedAt", item.UpdatedAt, "age", time.Since(item.UpdatedAt))
		return
	}

	sess := d.config.GetSession(item.SessionID)
	if sess == nil {
		log.Info("session not found, re-queuing")
		d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
			it.State = daemonstate.WorkItemQueued
			it.CurrentStep = ""
			it.Phase = "idle"
			it.UpdatedAt = time.Now()
		})
		return
	}

	// Check if PR was already created
	pollCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	prState, err := d.gitService.GetPRState(pollCtx, sess.RepoPath, item.Branch)
	if err == nil && (prState == git.PRStateOpen || prState == git.PRStateMerged) {
		if prState == git.PRStateMerged {
			log.Info("PR merged, marking completed")
			d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
				it.CurrentStep = "done"
				it.Phase = "idle"
				it.State = daemonstate.WorkItemCompleted
				now := time.Now()
				it.CompletedAt = &now
				it.UpdatedAt = now
			})
		} else {
			// Determine recovery target using the workflow engine so that custom
			// workflows with non-default step names are handled correctly.
			engine := d.getEngine(sess.RepoPath)
			recoveryStep := engine.FindRecoveryWaitStep(item.CurrentStep)
			if recoveryStep == "" {
				// No wait state found — re-queue to restart from the beginning.
				log.Info("no wait state found for recovery, re-queuing")
				d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
					it.State = daemonstate.WorkItemQueued
					it.CurrentStep = ""
					it.Phase = "idle"
					it.UpdatedAt = time.Now()
				})
				return
			}
			log.Info("PR exists, advancing to wait state", "recoveryStep", recoveryStep)
			d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
				now := time.Now()
				it.CurrentStep = recoveryStep
				it.Phase = "idle"
				// Must set State to non-queued so GetActiveWorkItems() includes
				// this item for CI/review polling. Without this, the item stays
				// WorkItemQueued and startQueuedItems() resets it to "coding"
				// on every tick, creating an infinite loop.
				it.State = daemonstate.WorkItemActive
				it.StepEnteredAt = now
				it.UpdatedAt = now
			})
		}
		return
	}

	// No PR — re-queue to restart coding
	log.Info("no PR found, re-queuing")
	d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
		it.State = daemonstate.WorkItemQueued
		it.CurrentStep = ""
		it.Phase = "idle"
		it.UpdatedAt = time.Now()
	})
}
