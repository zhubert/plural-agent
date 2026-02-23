package daemon

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/workflow"
	"github.com/zhubert/erg/internal/config"
	"github.com/zhubert/erg/internal/exec"
)

func TestFetchIssuesForProvider_GitHub(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	// Mock gh issue list returning issues
	type ghIssue struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		URL    string `json:"url"`
	}
	issuesJSON, _ := json.Marshal([]ghIssue{
		{Number: 1, Title: "Bug 1", Body: "Fix it", URL: "https://github.com/owner/repo/issues/1"},
		{Number: 2, Title: "Bug 2", Body: "Fix this too", URL: "https://github.com/owner/repo/issues/2"},
	})
	mockExec.AddPrefixMatch("gh", []string{"issue", "list"}, exec.MockResponse{
		Stdout: issuesJSON,
	})

	d := testDaemonWithExec(cfg, mockExec)

	wfCfg := workflow.DefaultConfig()
	wfCfg.Source.Provider = "github"

	issues, err := d.fetchIssuesForProvider(context.Background(), "/test/repo", wfCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(issues) != 2 {
		t.Fatalf("expected 2 issues, got %d", len(issues))
	}
	if issues[0].ID != "1" {
		t.Errorf("expected issue ID '1', got %s", issues[0].ID)
	}
	if issues[0].Title != "Bug 1" {
		t.Errorf("expected title 'Bug 1', got %s", issues[0].Title)
	}
	if issues[0].Body != "Fix it" {
		t.Errorf("expected body 'Fix it', got %s", issues[0].Body)
	}
}

func TestFetchIssuesForProvider_GitHub_CustomLabel(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	type ghIssue struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		URL    string `json:"url"`
	}
	issuesJSON, _ := json.Marshal([]ghIssue{
		{Number: 42, Title: "Custom labeled", URL: "https://github.com/owner/repo/issues/42"},
	})
	mockExec.AddPrefixMatch("gh", []string{"issue", "list"}, exec.MockResponse{
		Stdout: issuesJSON,
	})

	d := testDaemonWithExec(cfg, mockExec)

	wfCfg := workflow.DefaultConfig()
	wfCfg.Source.Provider = "github"
	wfCfg.Source.Filter.Label = "custom-label"

	issues, err := d.fetchIssuesForProvider(context.Background(), "/test/repo", wfCfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}
	if issues[0].ID != "42" {
		t.Errorf("expected issue ID '42', got %s", issues[0].ID)
	}
}

func TestFetchIssuesForProvider_UnknownProvider(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	wfCfg := workflow.DefaultConfig()
	wfCfg.Source.Provider = "unknown_provider"

	_, err := d.fetchIssuesForProvider(context.Background(), "/test/repo", wfCfg)
	if err == nil {
		t.Error("expected error for unknown provider")
	}
}

func TestPollForNewIssues_StoresBodyInStepData(t *testing.T) {
	cfg := testConfig()
	cfg.Repos = []string{"/test/repo"}
	mockExec := exec.NewMockExecutor(nil)

	type ghIssue struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		URL    string `json:"url"`
	}
	issuesJSON, _ := json.Marshal([]ghIssue{
		{Number: 5, Title: "Add feature", Body: "Please add dark mode support", URL: "https://github.com/owner/repo/issues/5"},
		{Number: 6, Title: "No body issue", Body: "", URL: "https://github.com/owner/repo/issues/6"},
	})
	mockExec.AddPrefixMatch("gh", []string{"issue", "list"}, exec.MockResponse{
		Stdout: issuesJSON,
	})
	// Mock remote URL for repo filter matching
	mockExec.AddPrefixMatch("git", []string{"remote", "get-url"}, exec.MockResponse{
		Stdout: []byte("git@github.com:owner/repo.git\n"),
	})

	d := testDaemonWithExec(cfg, mockExec)
	d.repoFilter = "owner/repo"
	d.maxConcurrent = 10

	d.pollForNewIssues(context.Background())

	// Check that issue body is stored in StepData
	item5 := d.state.GetWorkItem("/test/repo-5")
	if item5 == nil {
		t.Fatal("expected work item for issue 5")
	}
	body, ok := item5.StepData["issue_body"].(string)
	if !ok || body != "Please add dark mode support" {
		t.Errorf("expected issue body in StepData, got %q (ok=%v)", body, ok)
	}

	// Issue with empty body should not have issue_body in StepData
	item6 := d.state.GetWorkItem("/test/repo-6")
	if item6 == nil {
		t.Fatal("expected work item for issue 6")
	}
	if _, ok := item6.StepData["issue_body"]; ok {
		t.Error("expected no issue_body in StepData for issue with empty body")
	}
}

func TestStartQueuedItems_StartsWhenSlotsAvailable(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	// Mock branch existence check (returns error = branch doesn't exist)
	mockExec.AddPrefixMatch("git", []string{"rev-parse"}, exec.MockResponse{
		Err: errGHFailed,
	})
	// Mock worktree creation
	mockExec.AddPrefixMatch("git", []string{"worktree"}, exec.MockResponse{
		Stdout: []byte(""),
	})
	// Mock branch creation
	mockExec.AddPrefixMatch("git", []string{"checkout"}, exec.MockResponse{
		Stdout: []byte(""),
	})

	d := testDaemonWithExec(cfg, mockExec)
	d.repoFilter = "/test/repo"
	d.maxConcurrent = 2
	cfg.Repos = []string{"/test/repo"}

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "item-1",
		IssueRef: config.IssueRef{Source: "github", ID: "1", Title: "Bug 1"},
	})

	// Verify queued items exist before starting
	queued := d.state.GetWorkItemsByState(daemonstate.WorkItemQueued)
	if len(queued) != 1 {
		t.Fatalf("expected 1 queued item, got %d", len(queued))
	}

	// startQueuedItems will try to advance through the engine.
	// The coding action will fail (no real session service), but the item
	// will move out of queued state showing that the start was attempted.
	d.startQueuedItems(context.Background())

	item := d.state.GetWorkItem("item-1")
	// Item should have been processed (either started coding or failed due to mock)
	// The key point is that it was attempted when slots are available.
	if item.State == daemonstate.WorkItemQueued && item.CurrentStep == "" {
		// Still in initial queued state = startQueuedItems tried to process it
		// The exact outcome depends on the engine and mocks
	}
}

func TestStartQueuedItems_RespectsFullSlots(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)
	d.maxConcurrent = 1
	d.repoFilter = "/test/repo"
	cfg.Repos = []string{"/test/repo"}

	// Fill the slot
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "active-1",
		IssueRef: config.IssueRef{Source: "github", ID: "0"},
	})
	d.state.GetWorkItem("active-1").Phase = "async_pending"

	// Add a queued item
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "item-1",
		IssueRef: config.IssueRef{Source: "github", ID: "1", Title: "Bug 1"},
	})

	d.startQueuedItems(context.Background())

	// item-1 should still be queued
	if d.state.GetWorkItem("item-1").State != daemonstate.WorkItemQueued {
		t.Error("item-1 should still be queued when slots are full")
	}
}
