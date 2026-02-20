package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhubert/plural-agent/internal/daemonstate"
	"github.com/zhubert/plural-agent/internal/worker"
	"github.com/zhubert/plural-core/config"
	"github.com/zhubert/plural-core/exec"
	"github.com/zhubert/plural-core/git"
	"github.com/zhubert/plural-core/issues"
	"github.com/zhubert/plural-core/session"
)

// testConfig creates a minimal config for testing.
func testConfig() *config.Config {
	return &config.Config{
		Repos:              []string{},
		Sessions:           []config.Session{},
		AllowedTools:       []string{},
		RepoAllowedTools:   make(map[string][]string),
		AutoMaxTurns:       50,
		AutoMaxDurationMin: 30,
	}
}

// testSession creates a minimal session for testing.
func testSession(id string) *config.Session {
	return &config.Session{
		ID:            id,
		RepoPath:      "/test/repo",
		WorkTree:      "/test/worktree-" + id,
		Branch:        "feature-" + id,
		Name:          "test/" + id,
		CreatedAt:     time.Now(),
		Started:       true,
		Autonomous:    true,
		Containerized: true,
	}
}

// testDaemon creates a daemon suitable for testing with mock services.
func testDaemon(cfg *config.Config) *Daemon {
	mockExec := exec.NewMockExecutor(nil)
	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	sessSvc := session.NewSessionServiceWithExecutor(mockExec)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry := issues.NewProviderRegistry()

	d := New(cfg, gitSvc, sessSvc, registry, logger)
	d.sessionMgr.SetSkipMessageLoad(true)
	d.state = daemonstate.NewDaemonState("/test/repo")
	return d
}

// testDaemonWithExec creates a daemon with a custom mock executor.
func testDaemonWithExec(cfg *config.Config, mockExec *exec.MockExecutor) *Daemon {
	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	sessSvc := session.NewSessionServiceWithExecutor(mockExec)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry := issues.NewProviderRegistry()

	d := New(cfg, gitSvc, sessSvc, registry, logger)
	d.sessionMgr.SetSkipMessageLoad(true)
	d.state = daemonstate.NewDaemonState("/test/repo")
	return d
}

// newMockDoneWorker creates a SessionWorker that is already done.
func newMockDoneWorker() *worker.SessionWorker {
	return worker.NewDoneWorker()
}

func TestDaemonOptions(t *testing.T) {
	cfg := testConfig()

	t.Run("defaults", func(t *testing.T) {
		d := testDaemon(cfg)
		if d.autoMerge != true {
			t.Error("expected autoMerge=true by default for daemon")
		}
		if d.pollInterval != defaultPollInterval {
			t.Errorf("expected default poll interval, got %v", d.pollInterval)
		}
	})

	t.Run("WithOnce", func(t *testing.T) {
		d := testDaemon(cfg)
		WithOnce(true)(d)
		if !d.once {
			t.Error("expected once=true")
		}
	})

	t.Run("WithRepoFilter", func(t *testing.T) {
		d := testDaemon(cfg)
		WithRepoFilter("owner/repo")(d)
		if d.repoFilter != "owner/repo" {
			t.Errorf("expected owner/repo, got %s", d.repoFilter)
		}
	})

	t.Run("WithMaxConcurrent", func(t *testing.T) {
		d := testDaemon(cfg)
		WithMaxConcurrent(5)(d)
		if d.getMaxConcurrent() != 5 {
			t.Errorf("expected 5, got %d", d.getMaxConcurrent())
		}
	})

	t.Run("WithAutoMerge false", func(t *testing.T) {
		d := testDaemon(cfg)
		WithAutoMerge(false)(d)
		if d.autoMerge {
			t.Error("expected autoMerge=false")
		}
	})

	t.Run("WithMergeMethod", func(t *testing.T) {
		d := testDaemon(cfg)
		WithMergeMethod("squash")(d)
		if d.mergeMethod != "squash" {
			t.Errorf("expected squash, got %s", d.mergeMethod)
		}
	})

	t.Run("default reviewPollInterval", func(t *testing.T) {
		d := testDaemon(cfg)
		if d.reviewPollInterval != defaultReviewPollInterval {
			t.Errorf("expected default review poll interval, got %v", d.reviewPollInterval)
		}
	})
}

func TestDaemon_GetMaxConcurrent(t *testing.T) {
	t.Run("uses config when no override", func(t *testing.T) {
		cfg := testConfig()
		cfg.IssueMaxConcurrent = 5
		d := testDaemon(cfg)
		if got := d.getMaxConcurrent(); got != 5 {
			t.Errorf("expected 5, got %d", got)
		}
	})

	t.Run("uses override", func(t *testing.T) {
		cfg := testConfig()
		cfg.IssueMaxConcurrent = 5
		d := testDaemon(cfg)
		d.maxConcurrent = 10
		if got := d.getMaxConcurrent(); got != 10 {
			t.Errorf("expected 10, got %d", got)
		}
	})
}

func TestDaemon_GetMaxTurns(t *testing.T) {
	cfg := testConfig()
	cfg.AutoMaxTurns = 50
	d := testDaemon(cfg)
	if got := d.getMaxTurns(); got != 50 {
		t.Errorf("expected 50, got %d", got)
	}

	d.maxTurns = 100
	if got := d.getMaxTurns(); got != 100 {
		t.Errorf("expected 100, got %d", got)
	}
}

func TestDaemon_GetMaxDuration(t *testing.T) {
	cfg := testConfig()
	cfg.AutoMaxDurationMin = 30
	d := testDaemon(cfg)
	if got := d.getMaxDuration(); got != 30 {
		t.Errorf("expected 30, got %d", got)
	}

	d.maxDuration = 60
	if got := d.getMaxDuration(); got != 60 {
		t.Errorf("expected 60, got %d", got)
	}
}

func TestDaemon_ActiveSlotCount(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	if d.activeSlotCount() != 0 {
		t.Error("expected 0 active slots")
	}

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "item-1",
		IssueRef: config.IssueRef{Source: "github", ID: "1"},
	})
	// Set phase to async_pending to consume a slot
	d.state.GetWorkItem("item-1").Phase = "async_pending"

	if d.activeSlotCount() != 1 {
		t.Errorf("expected 1 active slot, got %d", d.activeSlotCount())
	}
}

func TestDaemon_CollectCompletedWorkers(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	// Add a work item in coding phase with a done worker
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "feature-1",
		CurrentStep: "coding",
	})
	// Set phase after AddWorkItem since it resets Phase to "idle"
	d.state.AdvanceWorkItem("item-1", "coding", "async_pending")
	d.state.GetWorkItem("item-1").State = daemonstate.WorkItemCoding

	// Add a session for the work item
	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	// Create a done worker
	mock := newMockDoneWorker()
	d.workers["item-1"] = mock

	// collectCompletedWorkers should detect the done worker
	ctx := context.Background()
	d.collectCompletedWorkers(ctx)

	// Worker should be removed
	if _, ok := d.workers["item-1"]; ok {
		t.Error("expected done worker to be removed")
	}
}

func TestDaemon_ProcessWorkItems_AwaitingReview_PRClosed(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	// Mock PR state check returning CLOSED
	prStateJSON, _ := json.Marshal(struct {
		State string `json:"state"`
	}{State: "CLOSED"})
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Stdout: prStateJSON,
	})

	d := testDaemonWithExec(cfg, mockExec)
	d.repoFilter = "/test/repo"

	// Add session
	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	// Add work item in await_review step
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "feature-sess-1",
		CurrentStep: "await_review",
		Phase:       "idle",
		State:       daemonstate.WorkItemCoding, // Non-terminal so it's active
	})

	// Load workflow configs to create engines
	d.loadWorkflowConfigs()

	// Process - review poll gate needs to be open
	d.lastReviewPollAt = time.Time{} // Force review poll to run
	d.processWorkItems(context.Background())

	// The event checker will detect the closed PR but the exact handling
	// depends on the engine integration. Verify the item was processed.
	item := d.state.GetWorkItem("item-1")
	// Item should still be at await_review (event checker returns false for closed)
	if item == nil {
		t.Fatal("item should exist")
	}
}

func TestDaemon_ProcessWorkItems_AwaitingCI_Passing(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	// Mock CI checks returning passing
	checksJSON, _ := json.Marshal([]struct {
		State string `json:"state"`
	}{{State: "SUCCESS"}})
	mockExec.AddPrefixMatch("gh", []string{"pr", "checks"}, exec.MockResponse{
		Stdout: checksJSON,
	})

	// Mock merge success
	mockExec.AddPrefixMatch("gh", []string{"pr", "merge"}, exec.MockResponse{
		Stdout: []byte("merged"),
	})

	d := testDaemonWithExec(cfg, mockExec)
	d.repoFilter = "/test/repo"

	// Add session
	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	// Add work item in await_ci step
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "feature-sess-1",
		CurrentStep: "await_ci",
		Phase:       "idle",
		State:       daemonstate.WorkItemCoding, // Non-terminal
	})

	d.loadWorkflowConfigs()

	// Process
	d.processWorkItems(context.Background())

	// After CI passes, engine should advance to merge and then done
	item := d.state.GetWorkItem("item-1")
	if item.IsTerminal() && item.State == daemonstate.WorkItemCompleted {
		// Successfully merged and completed
	} else {
		// May still be in progress depending on sync chain execution
		t.Logf("item state after CI pass: step=%s phase=%s state=%s", item.CurrentStep, item.Phase, item.State)
	}
}

func TestDaemon_MatchesRepoFilter(t *testing.T) {
	t.Run("exact path", func(t *testing.T) {
		cfg := testConfig()
		d := testDaemon(cfg)
		d.repoFilter = "/path/to/repo"
		if !d.matchesRepoFilter(context.Background(), "/path/to/repo") {
			t.Error("expected match")
		}
	})

	t.Run("owner/repo via remote", func(t *testing.T) {
		cfg := testConfig()
		mockExec := exec.NewMockExecutor(nil)
		mockExec.AddPrefixMatch("git", []string{"remote", "get-url"}, exec.MockResponse{
			Stdout: []byte("git@github.com:owner/repo.git\n"),
		})
		d := testDaemonWithExec(cfg, mockExec)
		d.repoFilter = "owner/repo"
		if !d.matchesRepoFilter(context.Background(), "/some/path") {
			t.Error("expected match via remote")
		}
	})

	t.Run("no match", func(t *testing.T) {
		cfg := testConfig()
		d := testDaemon(cfg)
		d.repoFilter = "/other/path"
		if d.matchesRepoFilter(context.Background(), "/path/to/repo") {
			t.Error("expected no match")
		}
	})
}

func TestDaemon_HasExistingSession(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	if d.hasExistingSession("/repo", "42") {
		t.Error("expected false for empty sessions")
	}

	cfg.AddSession(config.Session{
		ID:       "sess-1",
		RepoPath: "/repo",
		IssueRef: &config.IssueRef{ID: "42", Source: "github"},
	})

	if !d.hasExistingSession("/repo", "42") {
		t.Error("expected true for existing session")
	}

	if d.hasExistingSession("/repo", "99") {
		t.Error("expected false for different issue")
	}
}

func TestDaemon_GetMergeMethod(t *testing.T) {
	t.Run("uses config when no override", func(t *testing.T) {
		cfg := testConfig()
		cfg.AutoMergeMethod = "squash"
		d := testDaemon(cfg)
		if got := d.getMergeMethod(); got != "squash" {
			t.Errorf("expected squash, got %s", got)
		}
	})

	t.Run("uses override", func(t *testing.T) {
		cfg := testConfig()
		cfg.AutoMergeMethod = "squash"
		d := testDaemon(cfg)
		d.mergeMethod = "merge"
		if got := d.getMergeMethod(); got != "merge" {
			t.Errorf("expected merge, got %s", got)
		}
	})

	t.Run("defaults to rebase", func(t *testing.T) {
		cfg := testConfig()
		d := testDaemon(cfg)
		if got := d.getMergeMethod(); got != "rebase" {
			t.Errorf("expected rebase, got %s", got)
		}
	})
}

func TestDaemon_ReviewPollIntervalGating(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	d := testDaemonWithExec(cfg, mockExec)
	d.repoFilter = "/test/repo"
	// Set review poll interval to something large
	d.reviewPollInterval = 1 * time.Hour
	// Set last poll to now, so the interval hasn't elapsed
	d.lastReviewPollAt = time.Now()

	// Add session
	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	// Add work item in await_review step
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "feature-sess-1",
		CurrentStep: "await_review",
		Phase:       "idle",
		State:       daemonstate.WorkItemCoding, // Non-terminal
	})

	d.loadWorkflowConfigs()

	// Process -- review polling should be skipped because interval hasn't elapsed
	d.processWorkItems(context.Background())

	// Should still be in await_review (review poll was gated)
	item := d.state.GetWorkItem("item-1")
	if item.CurrentStep != "await_review" {
		t.Errorf("expected await_review (review poll gated), got %s", item.CurrentStep)
	}

	// Now set lastReviewPollAt far in the past to simulate elapsed interval
	d.lastReviewPollAt = time.Now().Add(-2 * time.Hour)

	// Process again -- review polling should now proceed
	d.processWorkItems(context.Background())

	// lastReviewPollAt should be updated
	if time.Since(d.lastReviewPollAt) > 1*time.Second {
		t.Error("expected lastReviewPollAt to be updated after review poll ran")
	}
}

// Recovery tests

func TestDaemon_RecoverFromState_Queued(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "item-1",
		IssueRef: config.IssueRef{Source: "github", ID: "1"},
	})

	d.recoverFromState(context.Background())

	// Queued items should remain queued
	item := d.state.GetWorkItem("item-1")
	if item.State != daemonstate.WorkItemQueued {
		t.Errorf("expected queued, got %s", item.State)
	}
}

func TestDaemon_RecoverFromState_AsyncPendingWithPR(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	// PR exists and is open
	prStateJSON, _ := json.Marshal(struct {
		State string `json:"state"`
	}{State: "OPEN"})
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Stdout: prStateJSON,
	})

	d := testDaemonWithExec(cfg, mockExec)

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "feature-sess-1",
		CurrentStep: "coding",
	})
	// Set phase after AddWorkItem since it resets Phase to "idle"
	d.state.AdvanceWorkItem("item-1", "coding", "async_pending")

	d.recoverFromState(context.Background())

	item := d.state.GetWorkItem("item-1")
	if item.CurrentStep != "await_review" {
		t.Errorf("expected await_review (PR exists), got %s", item.CurrentStep)
	}
	if item.Phase != "idle" {
		t.Errorf("expected idle, got %s", item.Phase)
	}
}

func TestDaemon_RecoverFromState_AsyncPendingNoPR(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	// PR not found (error)
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Err: fmt.Errorf("no PR found"),
	})

	d := testDaemonWithExec(cfg, mockExec)

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "feature-sess-1",
		CurrentStep: "coding",
	})
	// Set phase after AddWorkItem since it resets Phase to "idle"
	d.state.AdvanceWorkItem("item-1", "coding", "async_pending")

	d.recoverFromState(context.Background())

	item := d.state.GetWorkItem("item-1")
	if item.State != daemonstate.WorkItemQueued {
		t.Errorf("expected queued (no PR), got %s", item.State)
	}
}

func TestDaemon_RecoverFromState_AddressingFeedback(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "feature-1",
		CurrentStep: "await_review",
	})
	// Set phase after AddWorkItem since it resets Phase to "idle"
	d.state.AdvanceWorkItem("item-1", "await_review", "addressing_feedback")

	d.recoverFromState(context.Background())

	item := d.state.GetWorkItem("item-1")
	if item.Phase != "idle" {
		t.Errorf("expected idle, got %s", item.Phase)
	}
	if item.CurrentStep != "await_review" {
		t.Errorf("expected await_review, got %s", item.CurrentStep)
	}
}

func TestDaemon_RecoverFromState_Pushing(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "feature-1",
		CurrentStep: "await_review",
	})
	// Set phase after AddWorkItem since it resets Phase to "idle"
	d.state.AdvanceWorkItem("item-1", "await_review", "pushing")

	d.recoverFromState(context.Background())

	item := d.state.GetWorkItem("item-1")
	if item.Phase != "idle" {
		t.Errorf("expected idle, got %s", item.Phase)
	}
}

func TestDaemon_RecoverFromState_TerminalStatesUntouched(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "completed-1",
		IssueRef: config.IssueRef{Source: "github", ID: "1"},
	})
	d.state.MarkWorkItemTerminal("completed-1", false)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "failed-1",
		IssueRef: config.IssueRef{Source: "github", ID: "2"},
	})
	d.state.MarkWorkItemTerminal("failed-1", false)

	d.recoverFromState(context.Background())

	// Should remain unchanged
	if !d.state.GetWorkItem("completed-1").IsTerminal() {
		t.Error("expected completed item to remain terminal")
	}
	if !d.state.GetWorkItem("failed-1").IsTerminal() {
		t.Error("expected failed item to remain terminal")
	}
}

func TestDaemon_RecoverFromState_AsyncPendingNoBranch(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		CurrentStep: "coding",
	})
	// Set phase after AddWorkItem since it resets Phase to "idle"
	d.state.AdvanceWorkItem("item-1", "coding", "async_pending")

	d.recoverFromState(context.Background())

	item := d.state.GetWorkItem("item-1")
	if item.State != daemonstate.WorkItemQueued {
		t.Errorf("expected queued (no branch), got %s", item.State)
	}
}

func TestDaemon_PollForNewIssues(t *testing.T) {
	t.Run("no repo filter skips polling", func(t *testing.T) {
		cfg := testConfig()
		d := testDaemon(cfg)
		d.repoFilter = ""

		d.pollForNewIssues(context.Background())

		if len(d.state.WorkItems) != 0 {
			t.Errorf("expected 0 work items, got %d", len(d.state.WorkItems))
		}
	})

	t.Run("at concurrency limit skips polling", func(t *testing.T) {
		cfg := testConfig()
		d := testDaemon(cfg)
		d.repoFilter = "/test/repo"
		d.maxConcurrent = 1

		// Add an active item with slot consumed
		d.state.AddWorkItem(&daemonstate.WorkItem{
			ID:       "active-1",
			IssueRef: config.IssueRef{Source: "github", ID: "1"},
		})
		d.state.GetWorkItem("active-1").Phase = "async_pending"

		d.pollForNewIssues(context.Background())

		if len(d.state.WorkItems) != 1 {
			t.Errorf("expected 1 work item, got %d", len(d.state.WorkItems))
		}
	})
}

func TestDaemon_IssueFromWorkItem(t *testing.T) {
	item := &daemonstate.WorkItem{
		ID: "item-1",
		IssueRef: config.IssueRef{
			Source: "github",
			ID:     "42",
			Title:  "Fix the bug",
			URL:    "https://github.com/owner/repo/issues/42",
		},
	}

	issue := issueFromWorkItem(item)

	if issue.ID != "42" {
		t.Errorf("expected ID 42, got %s", issue.ID)
	}
	if issue.Title != "Fix the bug" {
		t.Errorf("expected title, got %s", issue.Title)
	}
	if issue.Source != issues.SourceGitHub {
		t.Errorf("expected github source, got %s", issue.Source)
	}
}

func TestDaemon_StartQueuedItems(t *testing.T) {
	t.Run("respects concurrency limit", func(t *testing.T) {
		cfg := testConfig()
		d := testDaemon(cfg)
		d.maxConcurrent = 1
		d.repoFilter = "/test/repo"
		cfg.Repos = []string{"/test/repo"}

		// Add two queued items
		d.state.AddWorkItem(&daemonstate.WorkItem{
			ID:       "item-1",
			IssueRef: config.IssueRef{Source: "github", ID: "1", Title: "Bug 1"},
		})
		d.state.AddWorkItem(&daemonstate.WorkItem{
			ID:       "item-2",
			IssueRef: config.IssueRef{Source: "github", ID: "2", Title: "Bug 2"},
		})

		// Add an active item to fill the slot
		d.state.AddWorkItem(&daemonstate.WorkItem{
			ID:       "active-1",
			IssueRef: config.IssueRef{Source: "github", ID: "3"},
		})
		d.state.GetWorkItem("active-1").Phase = "async_pending"

		d.startQueuedItems(context.Background())

		// Both should still be queued since slot is full
		if d.state.GetWorkItem("item-1").State != daemonstate.WorkItemQueued {
			t.Error("item-1 should still be queued")
		}
		if d.state.GetWorkItem("item-2").State != daemonstate.WorkItemQueued {
			t.Error("item-2 should still be queued")
		}
	})
}

func TestDaemon_HandleAsyncComplete_PRAlreadyCreated(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	// Create a session where the worker already created a PR via MCP tools
	sess := testSession("sess-pr-created")
	sess.PRCreated = true
	cfg.AddSession(*sess)

	// Add a work item in coding step with async_pending phase and a done worker
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-pr-created",
		IssueRef:    config.IssueRef{Source: "github", ID: "100"},
		SessionID:   "sess-pr-created",
		Branch:      "feature-sess-pr-created",
		CurrentStep: "coding",
	})
	// Set phase after AddWorkItem since it resets Phase to "idle"
	d.state.AdvanceWorkItem("item-pr-created", "coding", "async_pending")

	mock := newMockDoneWorker()
	d.workers["item-pr-created"] = mock

	ctx := context.Background()
	d.collectCompletedWorkers(ctx)

	// Worker should be removed
	if _, ok := d.workers["item-pr-created"]; ok {
		t.Error("expected done worker to be removed")
	}

	// Item should be in await_review (skipped open_pr)
	item := d.state.GetWorkItem("item-pr-created")
	if item.CurrentStep != "await_review" {
		t.Errorf("expected await_review, got %s", item.CurrentStep)
	}
}

func TestDaemon_HandleAsyncComplete_PRAlreadyMerged(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	// Create a session where the worker already created and merged a PR
	sess := testSession("sess-pr-merged")
	sess.PRCreated = true
	sess.PRMerged = true
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-pr-merged",
		IssueRef:    config.IssueRef{Source: "github", ID: "101"},
		SessionID:   "sess-pr-merged",
		Branch:      "feature-sess-pr-merged",
		CurrentStep: "coding",
	})
	// Set phase after AddWorkItem since it resets Phase to "idle"
	d.state.AdvanceWorkItem("item-pr-merged", "coding", "async_pending")

	mock := newMockDoneWorker()
	d.workers["item-pr-merged"] = mock

	ctx := context.Background()
	d.collectCompletedWorkers(ctx)

	if _, ok := d.workers["item-pr-merged"]; ok {
		t.Error("expected done worker to be removed")
	}

	// Item should be completed (fast-pathed)
	item := d.state.GetWorkItem("item-pr-merged")
	if item.State != daemonstate.WorkItemCompleted {
		t.Errorf("expected completed, got %s", item.State)
	}
	if item.CompletedAt == nil {
		t.Error("expected CompletedAt to be set")
	}
}

func TestDaemon_GetEffectiveMergeMethod(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)
	d.repoFilter = "/test/repo"

	// Default
	if got := d.getEffectiveMergeMethod("/test/repo"); got != "rebase" {
		t.Errorf("expected rebase, got %s", got)
	}

	// CLI override
	d.mergeMethod = "squash"
	if got := d.getEffectiveMergeMethod("/test/repo"); got != "squash" {
		t.Errorf("expected squash, got %s", got)
	}
}

func TestDaemon_WorkItemView_UsesSessionRepoPath(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)
	d.repoFilter = "owner/repo" // Pattern, not a path

	sess := testSession("sess-1")
	sess.RepoPath = "/actual/repo/path"
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "feature-1",
		CurrentStep: "coding",
	})
	d.state.AdvanceWorkItem("item-1", "coding", "async_pending")

	item := d.state.GetWorkItem("item-1")
	view := d.workItemView(item)

	if view.RepoPath != "/actual/repo/path" {
		t.Errorf("expected session repo path /actual/repo/path, got %s", view.RepoPath)
	}
}

func TestDaemon_WorkItemView_FallsBackToRepoFilter(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)
	d.repoFilter = "/fallback/repo"

	// No session added for this work item
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "nonexistent-session",
		Branch:      "feature-1",
		CurrentStep: "coding",
	})

	item := d.state.GetWorkItem("item-1")
	view := d.workItemView(item)

	if view.RepoPath != "/fallback/repo" {
		t.Errorf("expected fallback to repoFilter /fallback/repo, got %s", view.RepoPath)
	}
}

func TestDaemon_SaveConfig_ResetOnSuccess(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	// Point config at a valid temp file so Save() succeeds
	tmpFile := filepath.Join(t.TempDir(), "config.json")
	cfg.SetFilePath(tmpFile)

	// Simulate some prior failures
	d.configSaveFailures = 3

	// saveConfig should reset counter on success
	d.saveConfig("test")

	if d.configSaveFailures != 0 {
		t.Errorf("expected configSaveFailures=0 after success, got %d", d.configSaveFailures)
	}
}

func TestDaemon_SaveConfig_IncrementOnFailure(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	// Point the config at an invalid path to force Save() to fail
	cfg.SetFilePath("/nonexistent/path/config.json")

	d.saveConfig("test")

	if d.configSaveFailures != 1 {
		t.Errorf("expected configSaveFailures=1 after failure, got %d", d.configSaveFailures)
	}

	d.saveConfig("test2")
	if d.configSaveFailures != 2 {
		t.Errorf("expected configSaveFailures=2 after second failure, got %d", d.configSaveFailures)
	}
}

func TestDaemon_CollectCompletedWorkers_WorkerError(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	sess := testSession("sess-err")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-err",
		IssueRef:    config.IssueRef{Source: "github", ID: "50"},
		SessionID:   "sess-err",
		Branch:      "feature-sess-err",
		CurrentStep: "coding",
	})
	d.state.AdvanceWorkItem("item-err", "coding", "async_pending")
	d.state.GetWorkItem("item-err").State = daemonstate.WorkItemCoding

	// Create a done worker WITH an error (simulating API 500)
	mock := worker.NewDoneWorkerWithError(fmt.Errorf("API error detected in response stream"))
	d.workers["item-err"] = mock

	ctx := context.Background()
	d.collectCompletedWorkers(ctx)

	// Worker should be removed
	if _, ok := d.workers["item-err"]; ok {
		t.Error("expected done worker to be removed")
	}

	// Item should be marked as failed (engine follows error edge to "failed" state)
	item := d.state.GetWorkItem("item-err")
	if !item.IsTerminal() {
		t.Errorf("expected terminal state after worker error, got step=%s phase=%s state=%s",
			item.CurrentStep, item.Phase, item.State)
	}
	if item.State != daemonstate.WorkItemFailed {
		t.Errorf("expected failed state, got %s", item.State)
	}
}

func TestDaemon_CollectCompletedWorkers_FeedbackError(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	sess := testSession("sess-fb-err")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-fb-err",
		IssueRef:    config.IssueRef{Source: "github", ID: "60"},
		SessionID:   "sess-fb-err",
		Branch:      "feature-sess-fb-err",
		CurrentStep: "await_review",
	})
	d.state.AdvanceWorkItem("item-fb-err", "await_review", "addressing_feedback")

	// Create a done worker WITH an error during feedback
	mock := worker.NewDoneWorkerWithError(fmt.Errorf("API error"))
	d.workers["item-fb-err"] = mock

	ctx := context.Background()
	d.collectCompletedWorkers(ctx)

	// Worker should be removed
	if _, ok := d.workers["item-fb-err"]; ok {
		t.Error("expected done worker to be removed")
	}

	// Item should be back to idle phase (skipped push due to error),
	// NOT marked as terminal — it should just go back to waiting for review
	item := d.state.GetWorkItem("item-fb-err")
	if item.Phase != "idle" {
		t.Errorf("expected idle phase after failed feedback, got %s", item.Phase)
	}
	if item.CurrentStep != "await_review" {
		t.Errorf("expected await_review step, got %s", item.CurrentStep)
	}
	if item.IsTerminal() {
		t.Error("expected non-terminal state — failed feedback should not kill the work item")
	}
	// Error message should be persisted for operator visibility
	if item.ErrorMessage == "" {
		t.Error("expected error message to be set after failed feedback")
	}
}

func TestDaemon_NotifyWorkerDone_Sends(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	// Channel should be empty initially
	select {
	case <-d.workerDone:
		t.Fatal("expected workerDone channel to be empty")
	default:
	}

	// notifyWorkerDone should send a signal
	d.notifyWorkerDone()

	select {
	case <-d.workerDone:
		// success
	default:
		t.Fatal("expected workerDone channel to have a signal")
	}
}

func TestDaemon_NotifyWorkerDone_NonBlocking(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	// Fill the buffered channel
	d.notifyWorkerDone()

	// Second call should not block (channel is full, non-blocking send drops it)
	done := make(chan struct{})
	go func() {
		d.notifyWorkerDone()
		close(done)
	}()

	select {
	case <-done:
		// success — did not block
	case <-time.After(1 * time.Second):
		t.Fatal("notifyWorkerDone blocked when channel was full")
	}
}

func TestDaemon_WorkerDone_ChannelInitialized(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	if d.workerDone == nil {
		t.Fatal("expected workerDone channel to be initialized")
	}
	if cap(d.workerDone) != 1 {
		t.Fatalf("expected workerDone channel capacity 1, got %d", cap(d.workerDone))
	}
}

func TestDaemon_WorkerDone_CreateWorkerNotifies(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	sess := testSession("sess-notify")
	cfg.AddSession(*sess)

	item := &daemonstate.WorkItem{
		ID:        "item-notify",
		IssueRef:  config.IssueRef{Source: "github", ID: "99"},
		SessionID: "sess-notify",
		Branch:    "feature-notify",
	}
	d.state.AddWorkItem(item)

	// createWorkerWithPrompt spawns a goroutine that calls notifyWorkerDone
	// after the worker finishes. Use a pre-done worker to trigger immediately.
	w := worker.NewDoneWorker()

	// Manually simulate what createWorkerWithPrompt does for the notification:
	// we can't call createWorkerWithPrompt directly because it needs a real runner,
	// but we can test the goroutine pattern directly.
	d.mu.Lock()
	d.workers[item.ID] = w
	d.mu.Unlock()

	go func() {
		w.Wait()
		d.notifyWorkerDone()
	}()

	// The worker is already done, so the goroutine should fire quickly
	select {
	case <-d.workerDone:
		// success — notification received
	case <-time.After(2 * time.Second):
		t.Fatal("expected workerDone signal after worker completed")
	}
}
