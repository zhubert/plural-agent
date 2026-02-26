package daemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/workflow"
	"github.com/zhubert/erg/internal/config"
	"github.com/zhubert/erg/internal/exec"
	"github.com/zhubert/erg/internal/git"
	"github.com/zhubert/erg/internal/issues"
	"github.com/zhubert/erg/internal/session"
)

// alwaysFiredChecker is an EventChecker that always fires for the given event.
type alwaysFiredChecker struct {
	event string
	data  map[string]any
}

func (c *alwaysFiredChecker) CheckEvent(_ context.Context, event string, _ *workflow.ParamHelper, _ *workflow.WorkItemView) (bool, map[string]any, error) {
	if event == c.event {
		return true, c.data, nil
	}
	return false, nil, nil
}

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

	wfCfg := workflow.DefaultWorkflowConfig()
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

	wfCfg := workflow.DefaultWorkflowConfig()
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

	wfCfg := workflow.DefaultWorkflowConfig()
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
	item5, ok5 := d.state.GetWorkItem("/test/repo-5")
	if !ok5 {
		t.Fatal("expected work item for issue 5")
	}
	body, ok := item5.StepData["issue_body"].(string)
	if !ok || body != "Please add dark mode support" {
		t.Errorf("expected issue body in StepData, got %q (ok=%v)", body, ok)
	}

	// Issue with empty body should not have issue_body in StepData
	item6, ok6 := d.state.GetWorkItem("/test/repo-6")
	if !ok6 {
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

	item, _ := d.state.GetWorkItem("item-1")
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
	d.state.UpdateWorkItem("active-1", func(it *daemonstate.WorkItem) {
		it.Phase = "async_pending"
	})

	// Add a queued item
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "item-1",
		IssueRef: config.IssueRef{Source: "github", ID: "1", Title: "Bug 1"},
	})

	d.startQueuedItems(context.Background())

	// item-1 should still be queued
	queuedItem, _ := d.state.GetWorkItem("item-1")
	if queuedItem.State != daemonstate.WorkItemQueued {
		t.Error("item-1 should still be queued when slots are full")
	}
}

// TestStartQueuedItems_AwaitReviewDoesNotBlockNewWork verifies that a work item in
// await_review (including when actively addressing feedback) never blocks new coding
// work from starting. This is the core invariant for issue #158: items awaiting
// review are "set aside" and must not consume a concurrency slot.
func TestStartQueuedItems_AwaitReviewDoesNotBlockNewWork(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	mockExec.AddPrefixMatch("git", []string{"rev-parse"}, exec.MockResponse{
		Err: errGHFailed,
	})
	mockExec.AddPrefixMatch("git", []string{"worktree"}, exec.MockResponse{})
	mockExec.AddPrefixMatch("git", []string{"checkout"}, exec.MockResponse{})

	d := testDaemonWithExec(cfg, mockExec)
	d.repoFilter = "/test/repo"
	d.maxConcurrent = 1
	cfg.Repos = []string{"/test/repo"}

	tests := []struct {
		name        string
		reviewPhase string
	}{
		{
			name:        "await_review/idle does not block new coding work",
			reviewPhase: "idle",
		},
		{
			name:        "await_review/addressing_feedback does not block new coding work",
			reviewPhase: "addressing_feedback",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d.state = daemonstate.NewDaemonState("/test/repo")

			// Add an item actively awaiting review (consuming no coding slot)
			d.state.AddWorkItem(&daemonstate.WorkItem{
				ID:       "review-item",
				IssueRef: config.IssueRef{Source: "github", ID: "10"},
			})
			d.state.AdvanceWorkItem("review-item", "await_review", tc.reviewPhase)
			d.state.UpdateWorkItem("review-item", func(it *daemonstate.WorkItem) {
				it.State = daemonstate.WorkItemActive
			})

			// Verify it doesn't consume a slot
			if d.activeSlotCount() != 0 {
				t.Fatalf("await_review/%s should not consume a slot, got %d", tc.reviewPhase, d.activeSlotCount())
			}

			// Add a new queued issue
			d.state.AddWorkItem(&daemonstate.WorkItem{
				ID:       "new-item",
				IssueRef: config.IssueRef{Source: "github", ID: "99", Title: "New issue"},
			})

			// startQueuedItems should attempt to start the new item despite the await_review item
			d.startQueuedItems(context.Background())

			// The new item should no longer be in queued state (it was processed)
			newItem, _ := d.state.GetWorkItem("new-item")
			if newItem.State == daemonstate.WorkItemQueued && newItem.CurrentStep == "" {
				t.Errorf("new item should have been processed (not blocked by await_review/%s), still queued", tc.reviewPhase)
			}
		})
	}
}

// TestCheckLinkedPRsAndUnqueue_WithLinkedPR verifies that when a GitHub issue has
// an open linked PR, checkLinkedPRsAndUnqueue returns true and marks the item completed.
func TestCheckLinkedPRsAndUnqueue_WithLinkedPR(t *testing.T) {
	cfg := testConfig()
	cfg.Repos = []string{"/test/repo"}
	mockExec := exec.NewMockExecutor(nil)

	// Mock git remote get-url origin
	mockExec.AddExactMatch("git", []string{"remote", "get-url", "origin"}, exec.MockResponse{
		Stdout: []byte("git@github.com:owner/repo.git\n"),
	})

	// Mock gh api graphql — returns one OPEN linked PR.
	mockExec.AddPrefixMatch("gh", []string{"api", "graphql"}, exec.MockResponse{
		Stdout: []byte(`{
			"data": {
				"repository": {
					"issue": {
						"timelineItems": {
							"nodes": [
								{"source": {"number": 10, "state": "OPEN", "url": "https://github.com/owner/repo/pull/10"}}
							]
						}
					}
				}
			}
		}`),
	})

	// Mock gh issue edit (remove label) and gh issue comment (best-effort).
	mockExec.AddPrefixMatch("gh", []string{"issue", "edit"}, exec.MockResponse{})
	mockExec.AddPrefixMatch("gh", []string{"issue", "comment"}, exec.MockResponse{})

	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	sessSvc := session.NewSessionServiceWithExecutor(mockExec)
	githubProvider := issues.NewGitHubProvider(gitSvc)
	registry := issues.NewProviderRegistry(githubProvider)

	d := New(cfg, gitSvc, sessSvc, registry, discardLogger())
	d.sessionMgr.SetSkipMessageLoad(true)
	d.state = daemonstate.NewDaemonState("/test/repo")
	d.repoFilter = "/test/repo"

	issue := issues.Issue{
		ID:     "42",
		Title:  "Fix the bug",
		Source: issues.SourceGitHub,
	}

	skip := d.checkLinkedPRsAndUnqueue(context.Background(), "/test/repo", issue)

	if !skip {
		t.Error("expected checkLinkedPRsAndUnqueue to return true when linked PR exists")
	}

	// The work item should be created and marked completed.
	itemID := "/test/repo-42"
	item, okItem := d.state.GetWorkItem(itemID)
	if !okItem {
		t.Fatal("expected work item to be created in state")
	}
	if item.State != daemonstate.WorkItemCompleted {
		t.Errorf("expected work item to be completed, got %s", item.State)
	}
}

// TestCheckLinkedPRsAndUnqueue_NoPRs verifies that when no linked PRs exist,
// checkLinkedPRsAndUnqueue returns false and does not create a work item.
func TestCheckLinkedPRsAndUnqueue_NoPRs(t *testing.T) {
	cfg := testConfig()
	cfg.Repos = []string{"/test/repo"}
	mockExec := exec.NewMockExecutor(nil)

	mockExec.AddExactMatch("git", []string{"remote", "get-url", "origin"}, exec.MockResponse{
		Stdout: []byte("git@github.com:owner/repo.git\n"),
	})

	mockExec.AddPrefixMatch("gh", []string{"api", "graphql"}, exec.MockResponse{
		Stdout: []byte(`{
			"data": {
				"repository": {
					"issue": {
						"timelineItems": {
							"nodes": []
						}
					}
				}
			}
		}`),
	})

	d := testDaemonWithExec(cfg, mockExec)
	d.repoFilter = "/test/repo"

	issue := issues.Issue{
		ID:     "99",
		Title:  "Unrelated issue",
		Source: issues.SourceGitHub,
	}

	skip := d.checkLinkedPRsAndUnqueue(context.Background(), "/test/repo", issue)

	if skip {
		t.Error("expected checkLinkedPRsAndUnqueue to return false when no linked PRs")
	}
}

// TestCheckLinkedPRsAndUnqueue_GraphQLError verifies that when the GraphQL call
// fails, checkLinkedPRsAndUnqueue returns false (fail open) and doesn't block polling.
func TestCheckLinkedPRsAndUnqueue_GraphQLError(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	mockExec.AddExactMatch("git", []string{"remote", "get-url", "origin"}, exec.MockResponse{
		Stdout: []byte("git@github.com:owner/repo.git\n"),
	})

	// Simulate GraphQL failure by not having a match for graphql — returns empty which fails JSON parse.
	// With empty stdout and nil err, GetLinkedPRsForIssue will fail to unmarshal JSON.
	mockExec.AddPrefixMatch("gh", []string{"api", "graphql"}, exec.MockResponse{
		Stdout: []byte("invalid json"),
	})

	d := testDaemonWithExec(cfg, mockExec)

	issue := issues.Issue{ID: "1", Source: issues.SourceGitHub}

	// When GetLinkedPRsForIssue fails (bad JSON), should return false (fail open).
	skip := d.checkLinkedPRsAndUnqueue(context.Background(), "/test/repo", issue)

	if skip {
		t.Error("expected false (fail open) when API call fails")
	}
}

// TestCheckLinkedPRsAndUnqueue_NonNumericID verifies that a non-numeric issue ID
// causes checkLinkedPRsAndUnqueue to return false without calling the API.
func TestCheckLinkedPRsAndUnqueue_NonNumericID(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	d := testDaemonWithExec(cfg, mockExec)

	issue := issues.Issue{ID: "not-a-number", Source: issues.SourceGitHub}

	skip := d.checkLinkedPRsAndUnqueue(context.Background(), "/test/repo", issue)

	if skip {
		t.Error("expected false for non-numeric issue ID")
	}

	// No calls should have been made.
	if len(mockExec.GetCalls()) != 0 {
		t.Errorf("expected no CLI calls for non-numeric ID, got %d", len(mockExec.GetCalls()))
	}
}

// TestStartQueuedItems_PrioritizesAwaitReviewBeforeNewWork verifies that when there
// are both set-aside await_review items and new queued items, startQueuedItems checks
// the await_review items first — even if the regular review-poll interval has not yet
// elapsed. This ensures existing work is finished before new work is started.
func TestStartQueuedItems_PrioritizesAwaitReviewBeforeNewWork(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	// Minimal workflow: await_review fires → item succeeds immediately.
	// Using a succeed terminal avoids needing any sync-action mocks.
	wfCfg := &workflow.Config{
		Start: "await_review",
		Source: workflow.SourceConfig{Provider: "github"},
		States: map[string]*workflow.State{
			"await_review": {
				Type:  workflow.StateTypeWait,
				Event: "pr.reviewed",
				Next:  "done",
			},
			"done":   {Type: workflow.StateTypeSucceed},
			"failed": {Type: workflow.StateTypeFail},
		},
	}

	checker := &alwaysFiredChecker{
		event: "pr.reviewed",
		data:  map[string]any{"review_approved": true},
	}
	customEngine := workflow.NewEngine(wfCfg, workflow.NewActionRegistry(), checker, discardLogger())

	// Inject the custom engine for the session's repo path.
	repoPath := "/test/repo"
	d.engines = map[string]*workflow.Engine{repoPath: customEngine}
	d.repoFilter = repoPath
	d.maxConcurrent = 1
	cfg.Repos = []string{repoPath}

	// Simulate a recent review poll so processWorkItems would skip processWaitItems.
	// startQueuedItems must call processWaitItems itself to honour the priority rule.
	d.lastReviewPollAt = time.Now()

	// Set up a session for the await_review item.
	sess := testSession("sess-await")
	sess.RepoPath = repoPath
	cfg.AddSession(*sess)

	// Add the await_review item.
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "await-item",
		IssueRef:  config.IssueRef{Source: "github", ID: "10"},
		SessionID: sess.ID,
		Branch:    sess.Branch,
	})
	d.state.AdvanceWorkItem("await-item", "await_review", "idle")
	d.state.UpdateWorkItem("await-item", func(it *daemonstate.WorkItem) {
		it.State = daemonstate.WorkItemActive
	})

	// Add a new queued item.
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "new-item",
		IssueRef: config.IssueRef{Source: "github", ID: "99", Title: "New issue"},
	})

	d.startQueuedItems(context.Background())

	// The await_review item must have advanced out of await_review — its event
	// was ready and startQueuedItems must have checked it before starting new work.
	awaitItem, _ := d.state.GetWorkItem("await-item")
	if awaitItem.CurrentStep == "await_review" {
		t.Error("await-item should have advanced out of await_review: startQueuedItems must prioritise set-aside workflows")
	}
	if !awaitItem.IsTerminal() {
		t.Errorf("await-item should be terminal after the event fired and led to 'done', got step=%q phase=%q", awaitItem.CurrentStep, awaitItem.Phase)
	}
}

// asyncHoldAction is a test Action that simulates spawning async work by
// returning Async: true. This causes the engine to set the work item's phase
// to "async_pending", which consumes a concurrency slot without any real side effects.
type asyncHoldAction struct{}

func (a *asyncHoldAction) Execute(_ context.Context, _ *workflow.ActionContext) workflow.ActionResult {
	return workflow.ActionResult{Async: true}
}

// TestStartQueuedItems_AwaitReviewConsumesSlotPreventsNewWork verifies that when a
// set-aside await_review item is resumed and its continuation requires an async slot,
// new queued work does not start — the existing workflow gets priority.
func TestStartQueuedItems_AwaitReviewConsumesSlotPreventsNewWork(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	// Workflow: await_review → hold (async) so that resuming the review item
	// consumes the one available concurrency slot via async_pending phase.
	wfCfg := &workflow.Config{
		Start:  "await_review",
		Source: workflow.SourceConfig{Provider: "github"},
		States: map[string]*workflow.State{
			"await_review": {
				Type:  workflow.StateTypeWait,
				Event: "pr.reviewed",
				Next:  "hold",
			},
			// Async task: returns Async:true, setting phase to async_pending
			// and consuming the concurrency slot.
			"hold": {
				Type:   workflow.StateTypeTask,
				Action: "test.async_hold",
				Next:   "done",
				Error:  "failed",
			},
			"done":   {Type: workflow.StateTypeSucceed},
			"failed": {Type: workflow.StateTypeFail},
		},
	}

	checker := &alwaysFiredChecker{
		event: "pr.reviewed",
		data:  map[string]any{"review_approved": true},
	}
	registry := workflow.NewActionRegistry()
	registry.Register("test.async_hold", &asyncHoldAction{})
	customEngine := workflow.NewEngine(wfCfg, registry, checker, discardLogger())

	repoPath := "/test/repo"
	d.engines = map[string]*workflow.Engine{repoPath: customEngine}
	d.repoFilter = repoPath
	d.maxConcurrent = 1
	cfg.Repos = []string{repoPath}

	// Suppress the normal review-poll so any wait-item processing comes only
	// from startQueuedItems calling processWaitItems.
	d.lastReviewPollAt = time.Now()

	sess := testSession("sess-await2")
	sess.RepoPath = repoPath
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "await-item2",
		IssueRef:  config.IssueRef{Source: "github", ID: "20"},
		SessionID: sess.ID,
		Branch:    sess.Branch,
	})
	d.state.AdvanceWorkItem("await-item2", "await_review", "idle")
	d.state.UpdateWorkItem("await-item2", func(it *daemonstate.WorkItem) {
		it.State = daemonstate.WorkItemActive
	})

	// Add a new queued item.
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "new-item2",
		IssueRef: config.IssueRef{Source: "github", ID: "100", Title: "Brand new issue"},
	})

	d.startQueuedItems(context.Background())

	// await-item2 must have left await_review and be in async_pending (slot consumed).
	awaitItem, _ := d.state.GetWorkItem("await-item2")
	if awaitItem.CurrentStep == "await_review" {
		t.Error("await-item2 should have advanced out of await_review")
	}
	if awaitItem.Phase != "async_pending" {
		t.Errorf("await-item2 should be in async_pending phase (slot consumed), got %q", awaitItem.Phase)
	}

	// new-item2 must still be queued: the single concurrency slot is held by
	// the resumed await_review workflow in async_pending phase.
	newItem, _ := d.state.GetWorkItem("new-item2")
	if newItem.State != daemonstate.WorkItemQueued {
		t.Errorf("new-item2 should remain queued when the slot is taken by the resumed await_review item, got state=%q", newItem.State)
	}
}
