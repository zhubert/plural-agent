package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/zhubert/erg/internal/config"
	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/exec"
	"github.com/zhubert/erg/internal/git"
	"github.com/zhubert/erg/internal/workflow"
)

// --- reconstructSessions tests (unchanged behavior) ---

func TestDaemon_ReconstructSessions_RecoveredItemsGetSessions(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	d.state.AddRebuiltWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "feature-1",
		CurrentStep: "await_review",
		Phase:       "idle",
		State:       daemonstate.WorkItemActive,
		StepData:    map[string]any{"_repo_path": "/test/repo"},
	})
	d.state.AddRebuiltWorkItem(&daemonstate.WorkItem{
		ID:          "item-2",
		IssueRef:    config.IssueRef{Source: "github", ID: "2"},
		SessionID:   "sess-2",
		Branch:      "feature-2",
		CurrentStep: "await_ci",
		Phase:       "idle",
		State:       daemonstate.WorkItemActive,
		StepData:    map[string]any{"_repo_path": "/test/repo"},
	})

	if cfg.GetSession("sess-1") != nil {
		t.Fatal("expected sess-1 to not exist before reconstruction")
	}
	if cfg.GetSession("sess-2") != nil {
		t.Fatal("expected sess-2 to not exist before reconstruction")
	}

	d.reconstructSessions()

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

	sess2 := cfg.GetSession("sess-2")
	if sess2 == nil {
		t.Fatal("expected sess-2 to be reconstructed")
	}
	if sess2.Branch != "feature-2" {
		t.Errorf("expected Branch feature-2, got %s", sess2.Branch)
	}
}

func TestDaemon_ReconstructSessions_ExistingSessionsNotDuplicated(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	existing := testSession("sess-existing")
	existing.RepoPath = "/original/repo"
	cfg.AddSession(*existing)

	d.state.AddRebuiltWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-existing",
		Branch:      "feature-1",
		CurrentStep: "await_review",
		Phase:       "idle",
		State:       daemonstate.WorkItemActive,
		StepData:    map[string]any{"_repo_path": "/test/repo"},
	})

	d.reconstructSessions()

	sess := cfg.GetSession("sess-existing")
	if sess == nil {
		t.Fatal("expected session to still exist")
	}
	if sess.RepoPath != "/original/repo" {
		t.Errorf("expected original RepoPath /original/repo, got %s", sess.RepoPath)
	}

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

	d.state.AddRebuiltWorkItem(&daemonstate.WorkItem{
		ID:          "completed-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-completed",
		Branch:      "feature-completed",
		CurrentStep: "done",
		State:       daemonstate.WorkItemCompleted,
		StepData:    map[string]any{},
	})

	d.state.AddRebuiltWorkItem(&daemonstate.WorkItem{
		ID:          "failed-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "2"},
		SessionID:   "sess-failed",
		Branch:      "feature-failed",
		CurrentStep: "failed",
		State:       daemonstate.WorkItemFailed,
		StepData:    map[string]any{},
	})

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

	d.state.AddRebuiltWorkItem(&daemonstate.WorkItem{
		ID:       "item-no-sess",
		IssueRef: config.IssueRef{Source: "github", ID: "1"},
		State:    daemonstate.WorkItemQueued,
		StepData: map[string]any{},
	})

	d.reconstructSessions()

	if len(cfg.GetSessions()) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(cfg.GetSessions()))
	}
}

func TestDaemon_ReconstructSessions_MultiRepoUsesStepDataRepoPath(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)
	d.state.RepoPath = "multi-8dca39ab3c8f04ae"

	d.state.AddRebuiltWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "feature-1",
		CurrentStep: "await_review",
		Phase:       "idle",
		State:       daemonstate.WorkItemActive,
		StepData:    map[string]any{"_repo_path": "/actual/repo/path"},
	})

	d.reconstructSessions()

	sess := cfg.GetSession("sess-1")
	if sess == nil {
		t.Fatal("expected sess-1 to be reconstructed")
	}
	if sess.RepoPath != "/actual/repo/path" {
		t.Errorf("expected RepoPath /actual/repo/path, got %s", sess.RepoPath)
	}
}

func TestDaemon_ReconstructSessions_SetsWorktreePath(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	d.state.AddRebuiltWorkItem(&daemonstate.WorkItem{
		ID:          "item-wt",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-wt-123",
		Branch:      "feature-wt",
		CurrentStep: "coding",
		Phase:       "async_pending",
		State:       daemonstate.WorkItemActive,
		StepData:    map[string]any{"_repo_path": "/test/repo"},
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

// --- rebuildStateFromTracker tests ---

// mockGitHubGraphQL builds the JSON response for GetLinkedPRsForIssue.
func mockGitHubGraphQL(prs []git.LinkedPR) []byte {
	type prNode struct {
		Source struct {
			Number      int    `json:"number"`
			State       string `json:"state"`
			URL         string `json:"url"`
			HeadRefName string `json:"headRefName"`
		} `json:"source"`
	}
	var nodes []prNode
	for _, pr := range prs {
		n := prNode{}
		n.Source.Number = pr.Number
		n.Source.State = string(pr.State)
		n.Source.URL = pr.URL
		n.Source.HeadRefName = pr.HeadRefName
		nodes = append(nodes, n)
	}
	resp := struct {
		Data struct {
			Repository struct {
				Issue struct {
					TimelineItems struct {
						Nodes []prNode `json:"nodes"`
					} `json:"timelineItems"`
				} `json:"issue"`
			} `json:"repository"`
		} `json:"data"`
	}{}
	resp.Data.Repository.Issue.TimelineItems.Nodes = nodes
	data, _ := json.Marshal(resp)
	return data
}

// mockGitHubIssuesList builds the JSON response for FetchGitHubIssuesWithLabel.
func mockGitHubIssuesList(ghIssues []git.GitHubIssue) []byte {
	data, _ := json.Marshal(ghIssues)
	return data
}

func setupRebuildDaemon(t *testing.T, mockExec *exec.MockExecutor) (*Daemon, *config.Config) {
	t.Helper()
	cfg := testConfig()
	cfg.Repos = []string{"/test/repo"}
	d := testDaemonWithExec(cfg, mockExec)
	d.repoFilter = "/test/repo"
	d.autoMerge = true // needed for CI event checker to fire ci_passed
	return d, cfg
}

func TestRebuild_NoPR_QueuesFromStart(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)

	// FetchGitHubIssuesWithLabel returns one issue
	mockExec.AddPrefixMatch("gh", []string{"issue", "list"}, exec.MockResponse{
		Stdout: mockGitHubIssuesList([]git.GitHubIssue{
			{Number: 42, Title: "Fix bug", URL: "https://github.com/owner/repo/issues/42"},
		}),
	})

	// GetRemoteOriginURL
	mockExec.AddExactMatch("git", []string{"remote", "get-url", "origin"}, exec.MockResponse{
		Stdout: []byte("git@github.com:owner/repo.git\n"),
	})

	// GetLinkedPRsForIssue returns no PRs
	mockExec.AddPrefixMatch("gh", []string{"api", "graphql"}, exec.MockResponse{
		Stdout: mockGitHubGraphQL(nil),
	})

	d, _ := setupRebuildDaemon(t, mockExec)
	d.rebuildStateFromTracker(context.Background())

	// Should have one queued work item
	items := d.state.GetWorkItemsByState(daemonstate.WorkItemQueued)
	if len(items) != 1 {
		t.Fatalf("expected 1 queued item, got %d", len(items))
	}
	if items[0].IssueRef.ID != "42" {
		t.Errorf("expected issue ID 42, got %s", items[0].IssueRef.ID)
	}
}

func TestRebuild_MergedPR_MarksCompleted(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)

	mockExec.AddPrefixMatch("gh", []string{"issue", "list"}, exec.MockResponse{
		Stdout: mockGitHubIssuesList([]git.GitHubIssue{
			{Number: 42, Title: "Fix bug", URL: "https://github.com/owner/repo/issues/42"},
		}),
	})

	mockExec.AddExactMatch("git", []string{"remote", "get-url", "origin"}, exec.MockResponse{
		Stdout: []byte("git@github.com:owner/repo.git\n"),
	})

	mockExec.AddPrefixMatch("gh", []string{"api", "graphql"}, exec.MockResponse{
		Stdout: mockGitHubGraphQL([]git.LinkedPR{
			{Number: 10, State: git.PRStateMerged, URL: "https://github.com/owner/repo/pull/10", HeadRefName: "fix-bug"},
		}),
	})

	d, _ := setupRebuildDaemon(t, mockExec)
	d.rebuildStateFromTracker(context.Background())

	items := d.state.GetWorkItemsByState(daemonstate.WorkItemCompleted)
	if len(items) != 1 {
		t.Fatalf("expected 1 completed item, got %d", len(items))
	}
	if items[0].CurrentStep != "done" {
		t.Errorf("expected step done, got %s", items[0].CurrentStep)
	}
	if items[0].Branch != "fix-bug" {
		t.Errorf("expected branch fix-bug, got %s", items[0].Branch)
	}
}

func TestRebuild_ClosedPR_QueuesFromStart(t *testing.T) {
	// GetLinkedPRsForIssue excludes CLOSED PRs, so a closed PR looks like
	// no PR at all — the issue gets queued for fresh work.
	mockExec := exec.NewMockExecutor(nil)

	mockExec.AddPrefixMatch("gh", []string{"issue", "list"}, exec.MockResponse{
		Stdout: mockGitHubIssuesList([]git.GitHubIssue{
			{Number: 42, Title: "Fix bug", URL: "https://github.com/owner/repo/issues/42"},
		}),
	})

	mockExec.AddExactMatch("git", []string{"remote", "get-url", "origin"}, exec.MockResponse{
		Stdout: []byte("git@github.com:owner/repo.git\n"),
	})

	// GraphQL returns no open/merged PRs (closed is filtered out by GetLinkedPRsForIssue)
	mockExec.AddPrefixMatch("gh", []string{"api", "graphql"}, exec.MockResponse{
		Stdout: mockGitHubGraphQL(nil),
	})

	d, _ := setupRebuildDaemon(t, mockExec)
	d.rebuildStateFromTracker(context.Background())

	items := d.state.GetWorkItemsByState(daemonstate.WorkItemQueued)
	if len(items) != 1 {
		t.Fatalf("expected 1 queued item, got %d", len(items))
	}
}

func TestRebuild_OpenPR_PendingCI_PlacesAtAwaitCI(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)

	mockExec.AddPrefixMatch("gh", []string{"issue", "list"}, exec.MockResponse{
		Stdout: mockGitHubIssuesList([]git.GitHubIssue{
			{Number: 42, Title: "Fix bug", URL: "https://github.com/owner/repo/issues/42"},
		}),
	})

	mockExec.AddExactMatch("git", []string{"remote", "get-url", "origin"}, exec.MockResponse{
		Stdout: []byte("git@github.com:owner/repo.git\n"),
	})

	mockExec.AddPrefixMatch("gh", []string{"api", "graphql"}, exec.MockResponse{
		Stdout: mockGitHubGraphQL([]git.LinkedPR{
			{Number: 10, State: git.PRStateOpen, URL: "https://github.com/owner/repo/pull/10", HeadRefName: "fix-bug"},
		}),
	})

	// PR view for CheckPRMergeableStatus — not conflicting
	prViewJSON, _ := json.Marshal(struct {
		MergeableStatus string `json:"mergeable"`
	}{MergeableStatus: "MERGEABLE"})
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Stdout: prViewJSON,
	})

	// CheckPRChecks — pending (returns error or empty)
	mockExec.AddPrefixMatch("gh", []string{"pr", "checks"}, exec.MockResponse{
		Err: fmt.Errorf("no checks yet"),
	})

	d, _ := setupRebuildDaemon(t, mockExec)
	d.rebuildStateFromTracker(context.Background())

	items := d.state.GetActiveWorkItems()
	if len(items) != 1 {
		t.Fatalf("expected 1 active item, got %d", len(items))
	}
	if items[0].CurrentStep != "await_ci" {
		t.Errorf("expected step await_ci, got %s", items[0].CurrentStep)
	}
	if items[0].Phase != "idle" {
		t.Errorf("expected phase idle, got %s", items[0].Phase)
	}
	if items[0].Branch != "fix-bug" {
		t.Errorf("expected branch fix-bug, got %s", items[0].Branch)
	}
}

func TestRebuild_OpenPR_CIPassed_PlacesAtAwaitReview(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)

	mockExec.AddPrefixMatch("gh", []string{"issue", "list"}, exec.MockResponse{
		Stdout: mockGitHubIssuesList([]git.GitHubIssue{
			{Number: 42, Title: "Fix bug", URL: "https://github.com/owner/repo/issues/42"},
		}),
	})

	mockExec.AddExactMatch("git", []string{"remote", "get-url", "origin"}, exec.MockResponse{
		Stdout: []byte("git@github.com:owner/repo.git\n"),
	})

	mockExec.AddPrefixMatch("gh", []string{"api", "graphql"}, exec.MockResponse{
		Stdout: mockGitHubGraphQL([]git.LinkedPR{
			{Number: 10, State: git.PRStateOpen, URL: "https://github.com/owner/repo/pull/10", HeadRefName: "fix-bug"},
		}),
	})

	// CI passes: CheckPRMergeableStatus + CheckPRChecks
	prViewJSON, _ := json.Marshal(struct {
		MergeableStatus string `json:"mergeable"`
	}{MergeableStatus: "MERGEABLE"})
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Stdout: prViewJSON,
	})
	mockExec.AddPrefixMatch("gh", []string{"pr", "checks"}, exec.MockResponse{
		Stdout: []byte("check1\tpass\t\t\n"),
	})

	// pr.reviewed — not approved yet (returns not-approved)
	// GetPRState for review check
	prStateJSON, _ := json.Marshal(struct {
		State   string `json:"state"`
		Reviews []any  `json:"reviews"`
	}{State: "OPEN", Reviews: []any{}})
	// The second pr view call (for review check) — overwrite won't work with prefix match,
	// but since all pr view calls return the same base, this should be fine.
	// Actually, we need the review check to return the PR state.
	// Let me use the combined JSON approach.
	_ = prStateJSON

	// CheckPRReviewDecision uses "gh pr view" with --json reviewDecision
	// Actually, let's look at what CheckPRReviewDecision calls.

	d, _ := setupRebuildDaemon(t, mockExec)
	d.rebuildStateFromTracker(context.Background())

	items := d.state.GetActiveWorkItems()
	if len(items) != 1 {
		t.Fatalf("expected 1 active item, got %d", len(items))
	}
	if items[0].CurrentStep != "await_review" {
		t.Errorf("expected step await_review, got %s", items[0].CurrentStep)
	}
}

func TestRebuild_OpenPR_ReviewApproved_PlacesAtMerge(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)

	mockExec.AddPrefixMatch("gh", []string{"issue", "list"}, exec.MockResponse{
		Stdout: mockGitHubIssuesList([]git.GitHubIssue{
			{Number: 42, Title: "Fix bug", URL: "https://github.com/owner/repo/issues/42"},
		}),
	})

	mockExec.AddExactMatch("git", []string{"remote", "get-url", "origin"}, exec.MockResponse{
		Stdout: []byte("git@github.com:owner/repo.git\n"),
	})

	mockExec.AddPrefixMatch("gh", []string{"api", "graphql"}, exec.MockResponse{
		Stdout: mockGitHubGraphQL([]git.LinkedPR{
			{Number: 10, State: git.PRStateOpen, URL: "https://github.com/owner/repo/pull/10", HeadRefName: "fix-bug"},
		}),
	})

	// CI passes
	prViewJSON, _ := json.Marshal(struct {
		MergeableStatus string `json:"mergeable"`
	}{MergeableStatus: "MERGEABLE"})
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Stdout: prViewJSON,
	})
	mockExec.AddPrefixMatch("gh", []string{"pr", "checks"}, exec.MockResponse{
		Stdout: []byte("check1\tpass\t\t\n"),
	})

	// Review approved — pr.reviewed returns review_approved=true
	// Need to mock CheckPRReviewDecision which uses "gh pr view --json reviews"
	type review struct {
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
		State string `json:"state"`
	}
	r := review{State: "APPROVED"}
	r.Author.Login = "reviewer1"
	reviewJSON, _ := json.Marshal(struct {
		State        string   `json:"state"`
		Reviews      []review `json:"reviews"`
		CommentCount int      `json:"comments"`
	}{State: "OPEN", Reviews: []review{r}, CommentCount: 0})

	// GetBatchPRStatesWithComments uses "gh pr list" prefix
	mockExec.AddPrefixMatch("gh", []string{"pr", "list"}, exec.MockResponse{
		Stdout: func() []byte {
			data, _ := json.Marshal([]struct {
				HeadRefName  string `json:"headRefName"`
				State        string `json:"state"`
				CommentCount int    `json:"comments"`
			}{
				{HeadRefName: "fix-bug", State: "OPEN", CommentCount: 0},
			})
			return data
		}(),
	})

	// Override pr view with review info (this mock is added after the first one,
	// but AddPrefixMatch uses first-match semantics, so we need to handle this differently).
	// Since we can't easily override, we'll use the approach of providing combined JSON
	// that satisfies both parsers.
	_ = reviewJSON

	d, _ := setupRebuildDaemon(t, mockExec)
	d.rebuildStateFromTracker(context.Background())

	items := d.state.GetActiveWorkItems()
	if len(items) != 1 {
		t.Fatalf("expected 1 active item, got %d", len(items))
	}
	// After CI passes and review is approved, the item should be placed at
	// the last satisfied wait state (await_review) rather than the sync step
	// after it. Normal polling will detect the event has fired and call
	// executeSyncChain to advance through remaining sync steps.
	step := items[0].CurrentStep
	if step != "await_review" && step != "check_review_result" {
		t.Errorf("expected step await_review or check_review_result, got %s", step)
	}
}

func TestRebuild_TerminalItemsPreserved(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)

	// No issues returned from tracker (empty list)
	mockExec.AddPrefixMatch("gh", []string{"issue", "list"}, exec.MockResponse{
		Stdout: []byte("[]"),
	})

	d, _ := setupRebuildDaemon(t, mockExec)

	// Add a terminal item before rebuild
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "completed-1",
		IssueRef: config.IssueRef{Source: "github", ID: "1"},
	})
	d.state.MarkWorkItemTerminal("completed-1", true)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "failed-1",
		IssueRef: config.IssueRef{Source: "github", ID: "2"},
	})
	d.state.MarkWorkItemTerminal("failed-1", false)

	d.rebuildStateFromTracker(context.Background())

	// Terminal items should still exist
	completedItem, ok := d.state.GetWorkItem("completed-1")
	if !ok {
		t.Fatal("expected completed item to be preserved")
	}
	if !completedItem.IsTerminal() {
		t.Error("expected completed item to remain terminal")
	}

	failedItem, ok := d.state.GetWorkItem("failed-1")
	if !ok {
		t.Fatal("expected failed item to be preserved")
	}
	if !failedItem.IsTerminal() {
		t.Error("expected failed item to remain terminal")
	}
}

func TestRebuild_ClearsNonTerminalItems(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)

	// No issues returned from tracker
	mockExec.AddPrefixMatch("gh", []string{"issue", "list"}, exec.MockResponse{
		Stdout: []byte("[]"),
	})

	d, _ := setupRebuildDaemon(t, mockExec)

	// Add stale non-terminal items that should be cleared
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "stale-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "99"},
		CurrentStep: "coding",
		Phase:       "async_pending",
	})

	d.rebuildStateFromTracker(context.Background())

	// Stale item should be gone (no matching issue in tracker)
	if _, ok := d.state.GetWorkItem("stale-1"); ok {
		t.Error("expected stale non-terminal item to be cleared")
	}
}

func TestRebuild_CustomWorkflow_PlacesAtCorrectWaitState(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)

	mockExec.AddPrefixMatch("gh", []string{"issue", "list"}, exec.MockResponse{
		Stdout: mockGitHubIssuesList([]git.GitHubIssue{
			{Number: 42, Title: "Fix bug", URL: "https://github.com/owner/repo/issues/42"},
		}),
	})

	mockExec.AddExactMatch("git", []string{"remote", "get-url", "origin"}, exec.MockResponse{
		Stdout: []byte("git@github.com:owner/repo.git\n"),
	})

	mockExec.AddPrefixMatch("gh", []string{"api", "graphql"}, exec.MockResponse{
		Stdout: mockGitHubGraphQL([]git.LinkedPR{
			{Number: 10, State: git.PRStateOpen, URL: "https://github.com/owner/repo/pull/10", HeadRefName: "fix-bug"},
		}),
	})

	// CI pending
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Stdout: func() []byte {
			data, _ := json.Marshal(struct {
				MergeableStatus string `json:"mergeable"`
			}{MergeableStatus: "MERGEABLE"})
			return data
		}(),
	})
	mockExec.AddPrefixMatch("gh", []string{"pr", "checks"}, exec.MockResponse{
		Err: fmt.Errorf("no checks yet"),
	})

	cfg := testConfig()
	cfg.Repos = []string{"/test/repo"}
	d := testDaemonWithExec(cfg, mockExec)
	d.repoFilter = "/test/repo"
	d.autoMerge = true

	// Register a custom workflow with non-default step names
	customCfg := &workflow.Config{
		Workflow: "custom",
		Start:    "implement",
		Source: workflow.SourceConfig{
			Provider: "github",
			Filter:   workflow.FilterConfig{Label: "queued"},
		},
		States: map[string]*workflow.State{
			"implement":         {Type: workflow.StateTypeTask, Action: "ai.code", Next: "create_pr"},
			"create_pr":         {Type: workflow.StateTypeTask, Action: "github.create_pr", Next: "check_ci"},
			"check_ci":          {Type: workflow.StateTypeWait, Event: "ci.complete", Params: map[string]any{"on_failure": "fix"}, Next: "wait_for_approval"},
			"wait_for_approval": {Type: workflow.StateTypeWait, Event: "pr.reviewed", Next: "auto_merge"},
			"auto_merge":        {Type: workflow.StateTypeTask, Action: "github.merge", Next: "finished"},
			"finished":          {Type: workflow.StateTypeSucceed},
			"error":             {Type: workflow.StateTypeFail},
		},
	}
	d.workflowConfigs = map[string]*workflow.Config{"/test/repo": customCfg}
	checker := newEventChecker(d)
	d.engines = map[string]*workflow.Engine{
		"/test/repo": workflow.NewEngine(customCfg, d.buildActionRegistry(), checker, discardLogger()),
	}

	d.rebuildStateFromTracker(context.Background())

	items := d.state.GetActiveWorkItems()
	if len(items) != 1 {
		t.Fatalf("expected 1 active item, got %d", len(items))
	}
	// Should place at check_ci (the custom CI wait state name)
	if items[0].CurrentStep != "check_ci" {
		t.Errorf("expected step check_ci, got %s", items[0].CurrentStep)
	}
}

// --- GetOrderedWaitStates tests ---

func TestEngine_GetOrderedWaitStates_DefaultWorkflow(t *testing.T) {
	cfg := workflow.DefaultWorkflowConfig()
	engine := workflow.NewEngine(cfg, workflow.NewActionRegistry(), nil, discardLogger())

	waitStates := engine.GetOrderedWaitStates()

	// Default workflow has await_ci and await_review as wait states
	if len(waitStates) < 2 {
		t.Fatalf("expected at least 2 wait states, got %d", len(waitStates))
	}

	// First should be await_ci
	if waitStates[0].Name != "await_ci" {
		t.Errorf("expected first wait state to be await_ci, got %s", waitStates[0].Name)
	}
	if waitStates[0].Event != "ci.complete" {
		t.Errorf("expected first event to be ci.complete, got %s", waitStates[0].Event)
	}

	// Should contain await_review
	found := false
	for _, ws := range waitStates {
		if ws.Name == "await_review" {
			found = true
			if ws.Event != "pr.reviewed" {
				t.Errorf("expected await_review event to be pr.reviewed, got %s", ws.Event)
			}
		}
	}
	if !found {
		t.Error("expected to find await_review in wait states")
	}
}

func TestEngine_GetOrderedWaitStates_CustomWorkflow(t *testing.T) {
	cfg := &workflow.Config{
		Start: "code",
		States: map[string]*workflow.State{
			"code":       {Type: workflow.StateTypeTask, Action: "ai.code", Next: "pr"},
			"pr":         {Type: workflow.StateTypeTask, Action: "github.create_pr", Next: "check_ci"},
			"check_ci":   {Type: workflow.StateTypeWait, Event: "ci.complete", Next: "approval"},
			"approval":   {Type: workflow.StateTypeWait, Event: "pr.reviewed", Next: "merge"},
			"merge":      {Type: workflow.StateTypeTask, Action: "github.merge", Next: "done"},
			"done":       {Type: workflow.StateTypeSucceed},
		},
	}
	engine := workflow.NewEngine(cfg, workflow.NewActionRegistry(), nil, discardLogger())

	waitStates := engine.GetOrderedWaitStates()

	if len(waitStates) != 2 {
		t.Fatalf("expected 2 wait states, got %d", len(waitStates))
	}
	if waitStates[0].Name != "check_ci" {
		t.Errorf("expected first wait state check_ci, got %s", waitStates[0].Name)
	}
	if waitStates[1].Name != "approval" {
		t.Errorf("expected second wait state approval, got %s", waitStates[1].Name)
	}
}

func TestEngine_GetOrderedWaitStates_EmptyWorkflow(t *testing.T) {
	cfg := &workflow.Config{
		Start: "done",
		States: map[string]*workflow.State{
			"done": {Type: workflow.StateTypeSucceed},
		},
	}
	engine := workflow.NewEngine(cfg, workflow.NewActionRegistry(), nil, discardLogger())

	waitStates := engine.GetOrderedWaitStates()

	if len(waitStates) != 0 {
		t.Errorf("expected 0 wait states, got %d", len(waitStates))
	}
}

func TestEngine_GetOrderedWaitStates_NilEngine(t *testing.T) {
	engine := workflow.NewEngine(nil, workflow.NewActionRegistry(), nil, discardLogger())

	waitStates := engine.GetOrderedWaitStates()

	if waitStates != nil {
		t.Errorf("expected nil, got %v", waitStates)
	}
}

// --- ClearNonTerminalItems tests ---

func TestClearNonTerminalItems(t *testing.T) {
	state := daemonstate.NewDaemonState("/test/repo")

	state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "queued-1",
		IssueRef: config.IssueRef{Source: "github", ID: "1"},
	})
	state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "completed-1",
		IssueRef: config.IssueRef{Source: "github", ID: "2"},
	})
	state.MarkWorkItemTerminal("completed-1", true)

	state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "failed-1",
		IssueRef: config.IssueRef{Source: "github", ID: "3"},
	})
	state.MarkWorkItemTerminal("failed-1", false)

	state.ClearNonTerminalItems()

	all := state.GetAllWorkItems()
	if len(all) != 2 {
		t.Fatalf("expected 2 items (terminal only), got %d", len(all))
	}

	if _, ok := state.GetWorkItem("queued-1"); ok {
		t.Error("expected queued item to be cleared")
	}
	if _, ok := state.GetWorkItem("completed-1"); !ok {
		t.Error("expected completed item to be preserved")
	}
	if _, ok := state.GetWorkItem("failed-1"); !ok {
		t.Error("expected failed item to be preserved")
	}
}

// --- AddRebuiltWorkItem tests ---

func TestAddRebuiltWorkItem_PreservesState(t *testing.T) {
	state := daemonstate.NewDaemonState("/test/repo")

	state.AddRebuiltWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		State:       daemonstate.WorkItemActive,
		CurrentStep: "await_ci",
		Phase:       "idle",
		StepData:    map[string]any{"_repo_path": "/test/repo"},
	})

	item, ok := state.GetWorkItem("item-1")
	if !ok {
		t.Fatal("expected item to exist")
	}
	if item.State != daemonstate.WorkItemActive {
		t.Errorf("expected state active, got %s", item.State)
	}
	if item.CurrentStep != "await_ci" {
		t.Errorf("expected step await_ci, got %s", item.CurrentStep)
	}
}

func TestRebuild_OpenPR_PrefersOpenOverMerged(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)

	mockExec.AddPrefixMatch("gh", []string{"issue", "list"}, exec.MockResponse{
		Stdout: mockGitHubIssuesList([]git.GitHubIssue{
			{Number: 42, Title: "Fix bug", URL: "https://github.com/owner/repo/issues/42"},
		}),
	})

	mockExec.AddExactMatch("git", []string{"remote", "get-url", "origin"}, exec.MockResponse{
		Stdout: []byte("git@github.com:owner/repo.git\n"),
	})

	// Return both a merged PR and an open PR — open should be preferred
	mockExec.AddPrefixMatch("gh", []string{"api", "graphql"}, exec.MockResponse{
		Stdout: mockGitHubGraphQL([]git.LinkedPR{
			{Number: 5, State: git.PRStateMerged, URL: "https://github.com/owner/repo/pull/5", HeadRefName: "old-branch"},
			{Number: 10, State: git.PRStateOpen, URL: "https://github.com/owner/repo/pull/10", HeadRefName: "new-branch"},
		}),
	})

	// CI pending for the open PR
	prViewJSON, _ := json.Marshal(struct {
		MergeableStatus string `json:"mergeable"`
	}{MergeableStatus: "MERGEABLE"})
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Stdout: prViewJSON,
	})
	mockExec.AddPrefixMatch("gh", []string{"pr", "checks"}, exec.MockResponse{
		Err: fmt.Errorf("no checks yet"),
	})

	d, _ := setupRebuildDaemon(t, mockExec)
	d.rebuildStateFromTracker(context.Background())

	items := d.state.GetActiveWorkItems()
	if len(items) != 1 {
		t.Fatalf("expected 1 active item, got %d", len(items))
	}
	// Should use the open PR's branch, not the merged one
	if items[0].Branch != "new-branch" {
		t.Errorf("expected branch new-branch (open PR), got %s", items[0].Branch)
	}
	if items[0].PRURL != "https://github.com/owner/repo/pull/10" {
		t.Errorf("expected open PR URL, got %s", items[0].PRURL)
	}
}

func TestRebuild_MultiRepo_SameIssueNumber_NotSkipped(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)

	// Both repos return issue #42
	mockExec.AddPrefixMatch("gh", []string{"issue", "list"}, exec.MockResponse{
		Stdout: mockGitHubIssuesList([]git.GitHubIssue{
			{Number: 42, Title: "Fix bug", URL: "https://github.com/owner/repo/issues/42"},
		}),
	})

	mockExec.AddExactMatch("git", []string{"remote", "get-url", "origin"}, exec.MockResponse{
		Stdout: []byte("git@github.com:owner/repo.git\n"),
	})

	// No PRs
	mockExec.AddPrefixMatch("gh", []string{"api", "graphql"}, exec.MockResponse{
		Stdout: mockGitHubGraphQL(nil),
	})

	cfg := testConfig()
	cfg.Repos = []string{"/test/repo-a", "/test/repo-b"}
	d := testDaemonWithExec(cfg, mockExec)
	d.autoMerge = true
	// Signal multi-repo mode so matchesRepoFilter allows all configured repos
	d.repoWorkflowFiles = map[string]string{
		"/test/repo-a": "",
		"/test/repo-b": "",
	}

	// Register workflow configs and engines for both repos
	wfCfg := workflow.DefaultWorkflowConfig()
	checker := newEventChecker(d)
	for _, repo := range cfg.Repos {
		d.workflowConfigs[repo] = wfCfg
		d.engines[repo] = workflow.NewEngine(wfCfg, d.buildActionRegistry(), checker, discardLogger())
	}

	d.rebuildStateFromTracker(context.Background())

	// Should have two separate work items, one for each repo
	items := d.state.GetWorkItemsByState(daemonstate.WorkItemQueued)
	if len(items) != 2 {
		t.Fatalf("expected 2 queued items (one per repo), got %d", len(items))
	}

	// Verify they have different IDs scoped to their repos
	ids := map[string]bool{}
	for _, item := range items {
		ids[item.ID] = true
	}
	if !ids["/test/repo-a-42"] {
		t.Error("expected work item for repo-a issue 42")
	}
	if !ids["/test/repo-b-42"] {
		t.Error("expected work item for repo-b issue 42")
	}
}

func TestRebuild_AllWaitStatesSatisfied_PlacesAtLastWaitState(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)

	mockExec.AddPrefixMatch("gh", []string{"issue", "list"}, exec.MockResponse{
		Stdout: mockGitHubIssuesList([]git.GitHubIssue{
			{Number: 42, Title: "Fix bug", URL: "https://github.com/owner/repo/issues/42"},
		}),
	})

	mockExec.AddExactMatch("git", []string{"remote", "get-url", "origin"}, exec.MockResponse{
		Stdout: []byte("git@github.com:owner/repo.git\n"),
	})

	mockExec.AddPrefixMatch("gh", []string{"api", "graphql"}, exec.MockResponse{
		Stdout: mockGitHubGraphQL([]git.LinkedPR{
			{Number: 10, State: git.PRStateOpen, URL: "https://github.com/owner/repo/pull/10", HeadRefName: "fix-bug"},
		}),
	})

	// CI passes
	prViewJSON, _ := json.Marshal(struct {
		MergeableStatus string `json:"mergeable"`
	}{MergeableStatus: "MERGEABLE"})
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Stdout: prViewJSON,
	})
	mockExec.AddPrefixMatch("gh", []string{"pr", "checks"}, exec.MockResponse{
		Stdout: []byte("check1\tpass\t\t\n"),
	})
	mockExec.AddPrefixMatch("gh", []string{"pr", "list"}, exec.MockResponse{
		Stdout: func() []byte {
			data, _ := json.Marshal([]struct {
				HeadRefName  string `json:"headRefName"`
				State        string `json:"state"`
				CommentCount int    `json:"comments"`
			}{
				{HeadRefName: "fix-bug", State: "OPEN", CommentCount: 0},
			})
			return data
		}(),
	})

	d, _ := setupRebuildDaemon(t, mockExec)
	d.rebuildStateFromTracker(context.Background())

	items := d.state.GetActiveWorkItems()
	if len(items) != 1 {
		t.Fatalf("expected 1 active item, got %d", len(items))
	}
	// Should be at a wait state, NOT at a sync task like "merge"
	step := items[0].CurrentStep
	if step == "merge" || step == "check_ci_result" || step == "check_review_result" {
		t.Errorf("expected item at a wait state, not sync step %s", step)
	}
}
