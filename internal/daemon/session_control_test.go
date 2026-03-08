package daemon

import (
	"testing"

	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/worker"
)

// addTestWorkItem adds a WorkItem to the daemon's state with the given state.
// Note: AddWorkItem always forces State=WorkItemQueued, so we override it after.
func addTestWorkItem(d *Daemon, itemID, sessionID string, itemState daemonstate.WorkItemState) daemonstate.WorkItem {
	item := &daemonstate.WorkItem{
		ID:        itemID,
		SessionID: sessionID,
	}
	d.state.AddWorkItem(item)
	// Override the state set by AddWorkItem.
	d.state.UpdateWorkItem(itemID, func(it *daemonstate.WorkItem) {
		it.State = itemState
		if itemState == daemonstate.WorkItemActive {
			it.CurrentStep = "code"
			it.Phase = "async_pending"
		}
	})
	got, _ := d.state.GetWorkItem(itemID)
	return got
}

// ---- StopSession ----

func TestStopSession_WorkItemNotFound(t *testing.T) {
	d := testDaemon(testConfig())
	err := d.StopSession("nonexistent")
	if err == nil {
		t.Error("expected error for missing work item")
	}
}

func TestStopSession_NoSession(t *testing.T) {
	d := testDaemon(testConfig())
	addTestWorkItem(d, "item-1", "", daemonstate.WorkItemActive)

	// Item has no session ID — should be a no-op success.
	if err := d.StopSession("item-1"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestStopSession_WorkerAlreadyDone(t *testing.T) {
	d := testDaemon(testConfig())
	addTestWorkItem(d, "item-1", "sess-1", daemonstate.WorkItemActive)

	// Register a done worker.
	d.mu.Lock()
	d.workers["sess-1"] = worker.NewDoneWorker()
	d.mu.Unlock()

	// Should succeed even though worker is already done.
	if err := d.StopSession("item-1"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestStopSession_NoWorkerRegistered(t *testing.T) {
	d := testDaemon(testConfig())
	addTestWorkItem(d, "item-1", "sess-1", daemonstate.WorkItemActive)
	// No worker registered — should return nil (already stopped).
	if err := d.StopSession("item-1"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// ---- RetryWorkItem ----

func TestRetryWorkItem_NotFound(t *testing.T) {
	d := testDaemon(testConfig())
	err := d.RetryWorkItem("nonexistent")
	if err == nil {
		t.Error("expected error for missing work item")
	}
}

func TestRetryWorkItem_ActiveBlocked(t *testing.T) {
	d := testDaemon(testConfig())
	addTestWorkItem(d, "item-1", "sess-1", daemonstate.WorkItemActive)

	err := d.RetryWorkItem("item-1")
	if err == nil {
		t.Error("expected error when retrying active work item")
	}
}

func TestRetryWorkItem_AlreadyQueued(t *testing.T) {
	d := testDaemon(testConfig())
	addTestWorkItem(d, "item-1", "", daemonstate.WorkItemQueued)

	// Should be a no-op.
	if err := d.RetryWorkItem("item-1"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	item, _ := d.state.GetWorkItem("item-1")
	if item.State != daemonstate.WorkItemQueued {
		t.Errorf("expected state=queued, got %s", item.State)
	}
}

func TestRetryWorkItem_FailedResetToQueued(t *testing.T) {
	d := testDaemon(testConfig())
	addTestWorkItem(d, "item-1", "sess-1", daemonstate.WorkItemFailed)

	// Set some error fields to verify they are cleared.
	d.state.UpdateWorkItem("item-1", func(it *daemonstate.WorkItem) {
		it.ErrorMessage = "something went wrong"
		it.Phase = "async_pending"
		it.CurrentStep = "code"
	})

	if err := d.RetryWorkItem("item-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	item, ok := d.state.GetWorkItem("item-1")
	if !ok {
		t.Fatal("work item not found after retry")
	}
	if item.State != daemonstate.WorkItemQueued {
		t.Errorf("expected state=queued, got %s", item.State)
	}
	if item.ErrorMessage != "" {
		t.Errorf("expected empty error message, got %q", item.ErrorMessage)
	}
	if item.CurrentStep != "" {
		t.Errorf("expected empty current step, got %q", item.CurrentStep)
	}
	if item.Phase != "" {
		t.Errorf("expected empty phase, got %q", item.Phase)
	}
}

func TestRetryWorkItem_CompletedResetToQueued(t *testing.T) {
	d := testDaemon(testConfig())
	addTestWorkItem(d, "item-1", "sess-1", daemonstate.WorkItemCompleted)

	if err := d.RetryWorkItem("item-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	item, _ := d.state.GetWorkItem("item-1")
	if item.State != daemonstate.WorkItemQueued {
		t.Errorf("expected state=queued, got %s", item.State)
	}
}

// ---- SendMessage ----

func TestSendMessage_ItemNotFound(t *testing.T) {
	d := testDaemon(testConfig())
	err := d.SendMessage("nonexistent", "hello")
	if err == nil {
		t.Error("expected error for missing work item")
	}
}

func TestSendMessage_NoSession(t *testing.T) {
	d := testDaemon(testConfig())
	addTestWorkItem(d, "item-1", "", daemonstate.WorkItemActive)

	err := d.SendMessage("item-1", "hello")
	if err == nil {
		t.Error("expected error when work item has no session")
	}
}

func TestSendMessage_WorkerDone(t *testing.T) {
	d := testDaemon(testConfig())
	addTestWorkItem(d, "item-1", "sess-1", daemonstate.WorkItemActive)

	// Register a done worker.
	d.mu.Lock()
	d.workers["sess-1"] = worker.NewDoneWorker()
	d.mu.Unlock()

	err := d.SendMessage("item-1", "hello")
	if err == nil {
		t.Error("expected error when worker is done")
	}
}

func TestSendMessage_NoWorker(t *testing.T) {
	d := testDaemon(testConfig())
	addTestWorkItem(d, "item-1", "sess-1", daemonstate.WorkItemActive)

	// No worker registered.
	err := d.SendMessage("item-1", "hello")
	if err == nil {
		t.Error("expected error when no worker registered")
	}
}

func TestSendMessage_MissingWorkerForSession(t *testing.T) {
	d := testDaemon(testConfig())
	addTestWorkItem(d, "item-1", "sess-missing", daemonstate.WorkItemActive)

	// No worker registered for this session — expect error.
	err := d.SendMessage("item-1", "hello")
	if err == nil {
		t.Error("expected error when no worker registered for session")
	}
}

// ---- WithDashboard option ----

func TestWithDashboard_SetsAddr(t *testing.T) {
	cfg := testConfig()
	d := New(cfg, nil, nil, nil, discardLogger(), WithDashboard("localhost:9999"))
	if d.dashboardAddr != "localhost:9999" {
		t.Errorf("expected dashboardAddr=localhost:9999, got %q", d.dashboardAddr)
	}
}

func TestWithDashboard_EmptyAddr(t *testing.T) {
	cfg := testConfig()
	d := New(cfg, nil, nil, nil, discardLogger())
	if d.dashboardAddr != "" {
		t.Errorf("expected empty dashboardAddr by default, got %q", d.dashboardAddr)
	}
}
