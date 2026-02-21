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

		case "addressing_feedback":
			// Was addressing feedback — reset to idle for re-polling.
			// Re-stamp StepEnteredAt so timeout enforcement works after recovery.
			log.Info("was addressing feedback, resetting to idle")
			d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
				now := time.Now()
				it.Phase = "idle"
				if it.StepEnteredAt.IsZero() {
					it.StepEnteredAt = now
				}
				it.UpdatedAt = now
			})

		case "pushing":
			// Was pushing — reset to idle for re-polling.
			// Re-stamp StepEnteredAt so timeout enforcement works after recovery.
			log.Info("was pushing, resetting to idle")
			d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
				now := time.Now()
				it.Phase = "idle"
				if it.StepEnteredAt.IsZero() {
					it.StepEnteredAt = now
				}
				it.UpdatedAt = now
			})

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
