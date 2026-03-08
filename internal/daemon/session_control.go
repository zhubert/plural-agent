package daemon

import (
	"fmt"
	"time"

	"github.com/zhubert/erg/internal/dashboard"
	"github.com/zhubert/erg/internal/daemonstate"
)

// Compile-time assertion that Daemon implements dashboard.SessionController.
var _ dashboard.SessionController = (*Daemon)(nil)

// StopSession cancels the running worker for the given work item ID.
// If the worker has already finished, it returns nil (already stopped is a success).
func (d *Daemon) StopSession(itemID string) error {
	item, ok := d.state.GetWorkItem(itemID)
	if !ok {
		return fmt.Errorf("work item not found: %s", itemID)
	}
	if item.SessionID == "" {
		// Item has no active session — nothing to stop.
		return nil
	}
	d.mu.Lock()
	w, exists := d.workers[itemID]
	d.mu.Unlock()

	if !exists || w.Done() {
		// Worker already finished — treat as success.
		return nil
	}
	w.Cancel()
	return nil
}

// RetryWorkItem resets a failed or completed work item back to queued state so
// the daemon picks it up on the next polling tick.
// Returns an error if the item is currently active (would cause duplicate workers).
func (d *Daemon) RetryWorkItem(itemID string) error {
	item, ok := d.state.GetWorkItem(itemID)
	if !ok {
		return fmt.Errorf("work item not found: %s", itemID)
	}
	switch item.State {
	case daemonstate.WorkItemQueued:
		// Already queued — no-op.
		return nil
	case daemonstate.WorkItemActive:
		return fmt.Errorf("work item is still active, stop it first: %s", itemID)
	}

	// Reset to queued so the daemon re-processes it on the next tick.
	now := time.Now()
	d.state.UpdateWorkItem(itemID, func(it *daemonstate.WorkItem) {
		it.State = daemonstate.WorkItemQueued
		it.CurrentStep = ""
		it.Phase = ""
		it.ErrorMessage = ""
		it.CompletedAt = nil
		it.UpdatedAt = now
		// Clear session-related fields so the retried item starts a fresh session
		// and does not show stale session IDs/logs while queued.
		it.SessionID = ""
		it.Branch = ""
		it.PRURL = ""
		it.StepEnteredAt = time.Time{}
		// Reset per-session spend so costs don't accumulate across retries.
		it.CostUSD = 0
		it.InputTokens = 0
		it.OutputTokens = 0
	})
	d.saveState()
	return nil
}

// SendMessage injects a message into an active session's pending message queue.
// The message is delivered at the session's next turn boundary.
func (d *Daemon) SendMessage(itemID, message string) error {
	item, ok := d.state.GetWorkItem(itemID)
	if !ok {
		return fmt.Errorf("work item not found: %s", itemID)
	}
	if item.SessionID == "" {
		return fmt.Errorf("work item has no active session: %s", itemID)
	}

	// Verify the worker is still running before queuing the message.
	d.mu.Lock()
	w, exists := d.workers[itemID]
	d.mu.Unlock()
	if !exists || w.Done() {
		return fmt.Errorf("session is no longer active: %s", item.SessionID)
	}

	d.SetPendingMessage(item.SessionID, message)
	return nil
}
