package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/zhubert/erg/internal/config"
	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/exec"
	"github.com/zhubert/erg/internal/workflow"
)

func TestDaemon_ReconstructSessions_RecoveredItemsGetSessions(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	// Add non-terminal work items with session IDs but no sessions in config
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "feature-1",
		CurrentStep: "await_review",
		Phase:       "idle",
	})
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-2",
		IssueRef:    config.IssueRef{Source: "github", ID: "2"},
		SessionID:   "sess-2",
		Branch:      "feature-2",
		CurrentStep: "await_ci",
		Phase:       "idle",
	})

	// Verify sessions don't exist yet
	if cfg.GetSession("sess-1") != nil {
		t.Fatal("expected sess-1 to not exist before reconstruction")
	}
	if cfg.GetSession("sess-2") != nil {
		t.Fatal("expected sess-2 to not exist before reconstruction")
	}

	d.reconstructSessions()

	// Verify sessions were created
	sess1 := cfg.GetSession("sess-1")
	if sess1 == nil {
		t.Fatal("expected sess-1 to be reconstructed")
	}
	if sess1.RepoPath != "/test/repo" {
		t.Errorf("expected RepoPath /test/repo, got %s", sess1.RepoPath)
	}
	if sess1.Branch != "feature-1" {
		t.Errorf("expected Branch feature-1, got %s", sess1.Branch)
	}
	if !sess1.DaemonManaged {
		t.Error("expected DaemonManaged=true")
	}
	if !sess1.Autonomous {
		t.Error("expected Autonomous=true")
	}
	if !sess1.Started {
		t.Error("expected Started=true")
	}
	if !sess1.Containerized {
		t.Error("expected Containerized=true")
	}
	if !sess1.PRCreated {
		t.Error("expected PRCreated=true for post-coding item")
	}

	sess2 := cfg.GetSession("sess-2")
	if sess2 == nil {
		t.Fatal("expected sess-2 to be reconstructed")
	}
	if sess2.Branch != "feature-2" {
		t.Errorf("expected Branch feature-2, got %s", sess2.Branch)
	}
}

func TestDaemon_ReconstructSessions_CodingItemNotMarkedPRCreated(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	// Item still in coding step — PR hasn't been created yet
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-coding",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-coding",
		Branch:      "feature-coding",
		CurrentStep: "coding",
	})
	d.state.AdvanceWorkItem("item-coding", "coding", "async_pending")

	d.reconstructSessions()

	sess := cfg.GetSession("sess-coding")
	if sess == nil {
		t.Fatal("expected session to be reconstructed")
	}
	if sess.PRCreated {
		t.Error("expected PRCreated=false for item still in coding step")
	}
}

func TestDaemon_ReconstructSessions_ExistingSessionsNotDuplicated(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	// Add a session that already exists in config
	existing := testSession("sess-existing")
	existing.RepoPath = "/original/repo"
	cfg.AddSession(*existing)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-existing",
		Branch:      "feature-1",
		CurrentStep: "await_review",
		Phase:       "idle",
	})

	d.reconstructSessions()

	// Verify the original session is preserved (not overwritten)
	sess := cfg.GetSession("sess-existing")
	if sess == nil {
		t.Fatal("expected session to still exist")
	}
	if sess.RepoPath != "/original/repo" {
		t.Errorf("expected original RepoPath /original/repo, got %s", sess.RepoPath)
	}

	// Verify no duplicate — count sessions with this ID
	count := 0
	for _, s := range cfg.GetSessions() {
		if s.ID == "sess-existing" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 session with ID sess-existing, got %d", count)
	}
}

func TestDaemon_ReconstructSessions_TerminalItemsSkipped(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	// Add terminal work items — their sessions should NOT be reconstructed
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "completed-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-completed",
		Branch:      "feature-completed",
		CurrentStep: "done",
	})
	d.state.MarkWorkItemTerminal("completed-1", true)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "failed-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "2"},
		SessionID:   "sess-failed",
		Branch:      "feature-failed",
		CurrentStep: "failed",
	})
	d.state.MarkWorkItemTerminal("failed-1", false)

	d.reconstructSessions()

	if cfg.GetSession("sess-completed") != nil {
		t.Error("expected terminal completed item's session to NOT be reconstructed")
	}
	if cfg.GetSession("sess-failed") != nil {
		t.Error("expected terminal failed item's session to NOT be reconstructed")
	}
}

func TestDaemon_ReconstructSessions_EmptySessionIDSkipped(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	// Add a work item with no session ID (e.g., just queued)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "item-no-sess",
		IssueRef: config.IssueRef{Source: "github", ID: "1"},
	})

	d.reconstructSessions()

	// Should not create any sessions
	if len(cfg.GetSessions()) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(cfg.GetSessions()))
	}
}

// TestDaemon_RecoverAsyncPending_SetsStateToCoding verifies that when
// recoverAsyncPending finds an open PR and advances the item to a wait state,
// it also sets State to WorkItemActive. Without this, the item stays
// WorkItemQueued and GetActiveWorkItems() excludes it from CI/review polling,
// while startQueuedItems resets it to "coding" on every tick (infinite loop).
func TestDaemon_RecoverAsyncPending_SetsStateToCoding(t *testing.T) {
	cfg := testConfig()
	cfg.Repos = []string{"/test/repo"}
	mockExec := exec.NewMockExecutor(nil)

	// Mock GetPRState to return OPEN
	prViewJSON, _ := json.Marshal(struct {
		State string `json:"state"`
	}{State: "OPEN"})
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Stdout: prViewJSON,
	})

	d := testDaemonWithExec(cfg, mockExec)
	d.repoFilter = "/test/repo"

	sess := testSession("sess-1")
	sess.Branch = "issue-1"
	cfg.AddSession(*sess)

	// Simulate an item that was async_pending (worker was running) when daemon
	// stopped. AddWorkItem sets State=WorkItemQueued by default.
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "issue-1",
		CurrentStep: "await_ci",
		Phase:       "async_pending",
		StepData:    map[string]any{},
	})
	// Override phase since AddWorkItem resets it
	d.state.AdvanceWorkItem("item-1", "await_ci", "async_pending")
	// Backdate so recovery doesn't skip this item due to the grace period
	d.state.UpdateWorkItem("item-1", func(it *daemonstate.WorkItem) {
		it.UpdatedAt = time.Now().Add(-3 * time.Minute)
	})

	d.recoverFromState(context.Background())

	item, _ := d.state.GetWorkItem("item-1")
	// recoverAsyncPending should set State to WorkItemActive so the item
	// is visible to GetActiveWorkItems() for CI/review polling.
	if item.State == daemonstate.WorkItemQueued {
		t.Error("expected State to be changed from WorkItemQueued after recovery with existing PR")
	}
	if item.CurrentStep != "await_ci" {
		t.Errorf("expected CurrentStep await_ci, got %s", item.CurrentStep)
	}
	if item.Phase != "idle" {
		t.Errorf("expected Phase idle, got %s", item.Phase)
	}
}

func TestDaemon_ReconstructSessions_CalledByRecoverFromState(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	// Add a non-terminal item with session ID but no session in config
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "feature-1",
		CurrentStep: "await_review",
		Phase:       "idle",
	})

	// recoverFromState should call reconstructSessions internally
	d.recoverFromState(context.Background())

	// Verify the session was created by reconstructSessions during recovery
	sess := cfg.GetSession("sess-1")
	if sess == nil {
		t.Fatal("expected sess-1 to be reconstructed during recoverFromState")
	}
	if sess.RepoPath != "/test/repo" {
		t.Errorf("expected RepoPath /test/repo, got %s", sess.RepoPath)
	}
}

func TestDaemon_RecoverAsyncPending_SkipsRecentlyUpdatedItems(t *testing.T) {
	cfg := testConfig()
	cfg.Repos = []string{"/test/repo"}
	mockExec := exec.NewMockExecutor(nil)

	// Mock GetPRState — should NOT be called because the item is too fresh
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Err: fmt.Errorf("should not be called"),
	})

	d := testDaemonWithExec(cfg, mockExec)
	d.repoFilter = "/test/repo"

	sess := testSession("sess-fresh")
	sess.Branch = "issue-42"
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-fresh",
		IssueRef:    config.IssueRef{Source: "github", ID: "42"},
		SessionID:   "sess-fresh",
		Branch:      "issue-42",
		CurrentStep: "coding",
		Phase:       "async_pending",
		StepData:    map[string]any{},
	})
	d.state.AdvanceWorkItem("item-fresh", "coding", "async_pending")

	// UpdatedAt was just set by AdvanceWorkItem — well within the grace period
	d.recoverFromState(context.Background())

	item, _ := d.state.GetWorkItem("item-fresh")
	// The item should remain in async_pending — recovery should skip it
	if item.Phase != "async_pending" {
		t.Errorf("expected phase to remain async_pending, got %s", item.Phase)
	}
	if item.CurrentStep != "coding" {
		t.Errorf("expected step to remain coding, got %s", item.CurrentStep)
	}
}

func TestDaemon_RecoverAsyncPending_RecoversStaleItems(t *testing.T) {
	cfg := testConfig()
	cfg.Repos = []string{"/test/repo"}
	mockExec := exec.NewMockExecutor(nil)

	// Mock GetPRState to return not found (no PR)
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Err: fmt.Errorf("no PR"),
	})

	d := testDaemonWithExec(cfg, mockExec)
	d.repoFilter = "/test/repo"

	sess := testSession("sess-stale")
	sess.Branch = "issue-99"
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-stale",
		IssueRef:    config.IssueRef{Source: "github", ID: "99"},
		SessionID:   "sess-stale",
		Branch:      "issue-99",
		CurrentStep: "coding",
		Phase:       "async_pending",
		StepData:    map[string]any{},
	})
	d.state.AdvanceWorkItem("item-stale", "coding", "async_pending")

	// Backdate the UpdatedAt to be older than the grace period
	d.state.UpdateWorkItem("item-stale", func(it *daemonstate.WorkItem) {
		it.UpdatedAt = time.Now().Add(-3 * time.Minute)
	})

	d.recoverFromState(context.Background())

	item, _ := d.state.GetWorkItem("item-stale")
	// Stale item with no PR should be re-queued
	if item.State != daemonstate.WorkItemQueued {
		t.Errorf("expected state to be queued, got %s", item.State)
	}
	if item.Phase != "idle" {
		t.Errorf("expected phase to be idle, got %s", item.Phase)
	}
}

// TestDaemon_RecoverAsyncPending_CustomWorkflow verifies that recovery uses the
// workflow engine to determine the correct wait state, rather than hardcoded
// step names. A custom workflow with non-default step names like "check_ci" and
// "wait_for_approval" should route to those steps correctly.
func TestDaemon_RecoverAsyncPending_CustomWorkflow(t *testing.T) {
	cfg := testConfig()
	cfg.Repos = []string{"/test/repo"}
	mockExec := exec.NewMockExecutor(nil)

	// Mock GetPRState to return OPEN
	prViewJSON, _ := json.Marshal(struct {
		State string `json:"state"`
	}{State: "OPEN"})
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Stdout: prViewJSON,
	})

	d := testDaemonWithExec(cfg, mockExec)
	d.repoFilter = "/test/repo"

	// Register a custom workflow engine with non-default step names.
	customCfg := &workflow.Config{
		Start: "implement",
		States: map[string]*workflow.State{
			"implement":         {Type: workflow.StateTypeTask, Action: "ai.code", Next: "create_pr"},
			"create_pr":         {Type: workflow.StateTypeTask, Action: "github.create_pr", Next: "check_ci"},
			"check_ci":          {Type: workflow.StateTypeWait, Event: "ci.complete", Next: "wait_for_approval"},
			"wait_for_approval": {Type: workflow.StateTypeWait, Event: "pr.reviewed", Next: "auto_merge"},
			"auto_merge":        {Type: workflow.StateTypeTask, Action: "github.merge", Next: "finished"},
			"finished":          {Type: workflow.StateTypeSucceed},
			"error":             {Type: workflow.StateTypeFail},
		},
	}
	d.engines = map[string]*workflow.Engine{
		"/test/repo": workflow.NewEngine(customCfg, workflow.NewActionRegistry(), nil, discardLogger()),
	}

	// Item that was in "implement" (coding) step when daemon stopped, PR already exists.
	sess := testSession("sess-custom")
	sess.Branch = "issue-custom"
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-custom",
		IssueRef:    config.IssueRef{Source: "github", ID: "custom"},
		SessionID:   "sess-custom",
		Branch:      "issue-custom",
		CurrentStep: "implement",
		Phase:       "async_pending",
		StepData:    map[string]any{},
	})
	d.state.AdvanceWorkItem("item-custom", "implement", "async_pending")
	d.state.UpdateWorkItem("item-custom", func(it *daemonstate.WorkItem) {
		it.UpdatedAt = time.Now().Add(-3 * time.Minute)
	})

	d.recoverFromState(context.Background())

	item, _ := d.state.GetWorkItem("item-custom")
	if item.CurrentStep != "check_ci" {
		t.Errorf("expected CurrentStep check_ci (custom CI wait state), got %s", item.CurrentStep)
	}
	if item.Phase != "idle" {
		t.Errorf("expected Phase idle, got %s", item.Phase)
	}
	if item.State == daemonstate.WorkItemQueued {
		t.Error("expected State to not be WorkItemQueued after recovery with existing PR")
	}
}

// TestDaemon_RecoverAsyncPending_CustomWorkflow_PastCI verifies that recovery
// routes to the second wait state when the item was past the CI wait state.
func TestDaemon_RecoverAsyncPending_CustomWorkflow_PastCI(t *testing.T) {
	cfg := testConfig()
	cfg.Repos = []string{"/test/repo"}
	mockExec := exec.NewMockExecutor(nil)

	prViewJSON, _ := json.Marshal(struct {
		State string `json:"state"`
	}{State: "OPEN"})
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Stdout: prViewJSON,
	})

	d := testDaemonWithExec(cfg, mockExec)
	d.repoFilter = "/test/repo"

	customCfg := &workflow.Config{
		Start: "implement",
		States: map[string]*workflow.State{
			"implement":         {Type: workflow.StateTypeTask, Action: "ai.code", Next: "create_pr"},
			"create_pr":         {Type: workflow.StateTypeTask, Action: "github.create_pr", Next: "check_ci"},
			"check_ci":          {Type: workflow.StateTypeWait, Event: "ci.complete", Next: "wait_for_approval"},
			"wait_for_approval": {Type: workflow.StateTypeWait, Event: "pr.reviewed", Next: "auto_merge"},
			"auto_merge":        {Type: workflow.StateTypeTask, Action: "github.merge", Next: "finished"},
			"finished":          {Type: workflow.StateTypeSucceed},
			"error":             {Type: workflow.StateTypeFail},
		},
	}
	d.engines = map[string]*workflow.Engine{
		"/test/repo": workflow.NewEngine(customCfg, workflow.NewActionRegistry(), nil, discardLogger()),
	}

	sess := testSession("sess-past-ci")
	sess.Branch = "issue-past-ci"
	cfg.AddSession(*sess)

	// Item was at "auto_merge" (after both wait states) when daemon stopped.
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-past-ci",
		IssueRef:    config.IssueRef{Source: "github", ID: "past-ci"},
		SessionID:   "sess-past-ci",
		Branch:      "issue-past-ci",
		CurrentStep: "auto_merge",
		Phase:       "async_pending",
		StepData:    map[string]any{},
	})
	d.state.AdvanceWorkItem("item-past-ci", "auto_merge", "async_pending")
	d.state.UpdateWorkItem("item-past-ci", func(it *daemonstate.WorkItem) {
		it.UpdatedAt = time.Now().Add(-3 * time.Minute)
	})

	d.recoverFromState(context.Background())

	item, _ := d.state.GetWorkItem("item-past-ci")
	if item.CurrentStep != "wait_for_approval" {
		t.Errorf("expected CurrentStep wait_for_approval (last wait state before auto_merge), got %s", item.CurrentStep)
	}
	if item.Phase != "idle" {
		t.Errorf("expected Phase idle, got %s", item.Phase)
	}
}

func TestDaemon_ReconstructSessions_SetsWorktreePath(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-wt",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-wt-123",
		Branch:      "feature-wt",
		CurrentStep: "coding",
		Phase:       "async_pending",
	})

	d.reconstructSessions()

	sess := cfg.GetSession("sess-wt-123")
	if sess == nil {
		t.Fatal("expected session to be reconstructed")
	}
	if sess.WorkTree == "" {
		t.Error("expected WorkTree to be set, got empty string")
	}
	if !strings.Contains(sess.WorkTree, "sess-wt-123") {
		t.Errorf("expected WorkTree to contain session ID, got %s", sess.WorkTree)
	}
}
