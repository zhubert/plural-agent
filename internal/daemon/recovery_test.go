package daemon

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/config"
	"github.com/zhubert/erg/internal/exec"
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

	d.recoverFromState(context.Background())

	item := d.state.GetWorkItem("item-1")
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
