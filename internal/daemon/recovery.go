package daemon

import (
	"context"
	"time"

	"github.com/zhubert/plural-agent/internal/daemonstate"
	"github.com/zhubert/plural-core/git"
)

// recoverFromState reconciles daemon state with reality after a restart.
func (d *Daemon) recoverFromState(ctx context.Context) {
	if d.state == nil || len(d.state.WorkItems) == 0 {
		return
	}

	log := d.logger.With("component", "recovery")
	log.Info("recovering from previous state", "workItems", len(d.state.WorkItems))

	for _, item := range d.state.WorkItems {
		if item.IsTerminal() {
			continue
		}

		log := log.With("workItem", item.ID, "step", item.CurrentStep, "phase", item.Phase, "branch", item.Branch)

		switch item.Phase {
		case "async_pending":
			// Worker was running but daemon restarted — no worker exists
			d.recoverAsyncPending(ctx, item, log)

		case "addressing_feedback", "pushing":
			// Worker or push was in-flight — check actual PR state to decide next step
			d.recoverWaitPhase(ctx, item, log)

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

	// PR is open — check if it's already approved (only relevant at await_review)
	if item.CurrentStep == "await_review" {
		reviewDecision, err := d.gitService.CheckPRReviewDecision(pollCtx, sess.RepoPath, item.Branch)
		if err == nil && reviewDecision == git.ReviewApproved {
			log.Info("PR approved, advancing to merge")
			d.state.AdvanceWorkItem(item.ID, "merge", "idle")
			return
		}
	}

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
			// Determine recovery target based on current step.
			// Since await_ci now comes before await_review, we need to
			// recover to the right wait state.
			recoveryStep := "await_ci"
			if item.CurrentStep == "await_review" || item.CurrentStep == "merge" {
				recoveryStep = "await_review" // was past CI, resume at review
			}
			log.Info("PR exists, advancing to wait state", "recoveryStep", recoveryStep)
			d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
				now := time.Now()
				it.CurrentStep = recoveryStep
				it.Phase = "idle"
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
