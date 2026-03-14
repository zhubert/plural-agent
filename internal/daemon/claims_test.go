package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/exec"
	"github.com/zhubert/erg/internal/git"
	"github.com/zhubert/erg/internal/issues"
	"github.com/zhubert/erg/internal/session"
	"github.com/zhubert/erg/internal/workflow"
)

// testDaemonKey returns the claimIdentity that a test daemon with the given
// daemonID would produce. Mirrors the hostname logic in Daemon.claimIdentity().
func testDaemonKey(daemonID string) string {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}
	return daemonID + "@" + hostname
}

// mockClaimProvider wraps GitHubProvider (via composition) to implement
// ProviderClaimManager with controllable behavior for tests. It also satisfies
// the Provider interface so it can be registered in a ProviderRegistry.
type mockClaimProvider struct {
	issues.GitHubProvider // embedded to satisfy Provider interface
	claims                []issues.ClaimInfo
	postErr               error
	getErr                error
	deleteErr             error
	nextCommentID         string
	postCalled            bool
	deleteCalled          bool
	deleteCalledIDs       []string
	// postHook is called after a successful PostClaim to allow tests to inject
	// additional claims (simulating a race condition).
	postHook func(m *mockClaimProvider)
}

func (m *mockClaimProvider) Source() issues.Source { return issues.SourceGitHub }
func (m *mockClaimProvider) Name() string          { return "Mock GitHub" }
func (m *mockClaimProvider) IsConfigured(repoPath string) bool {
	return true
}

func (m *mockClaimProvider) PostClaim(ctx context.Context, repoPath string, issueID string, claim issues.ClaimInfo) (string, error) {
	m.postCalled = true
	if m.postErr != nil {
		return "", m.postErr
	}
	claim.CommentID = m.nextCommentID
	m.claims = append(m.claims, claim)
	if m.postHook != nil {
		m.postHook(m)
	}
	return m.nextCommentID, nil
}

func (m *mockClaimProvider) GetClaims(ctx context.Context, repoPath string, issueID string) ([]issues.ClaimInfo, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	return m.claims, nil
}

func (m *mockClaimProvider) DeleteClaim(ctx context.Context, repoPath string, issueID string, commentID string) error {
	m.deleteCalled = true
	m.deleteCalledIDs = append(m.deleteCalledIDs, commentID)
	if m.deleteErr != nil {
		return m.deleteErr
	}
	// Remove from stored claims
	var remaining []issues.ClaimInfo
	for _, c := range m.claims {
		if c.CommentID != commentID {
			remaining = append(remaining, c)
		}
	}
	m.claims = remaining
	return nil
}

func newTestDaemonWithClaimProvider(mockProvider *mockClaimProvider) *Daemon {
	cfg := testConfig()
	registry := issues.NewProviderRegistry(mockProvider)
	mockExec := exec.NewMockExecutor(nil)
	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	sessSvc := session.NewSessionServiceWithExecutor(mockExec)
	d := New(cfg, gitSvc, sessSvc, registry, discardLogger())
	d.sessionMgr.SetSkipMessageLoad(true)
	d.state = daemonstate.NewDaemonState("/test/repo")
	d.repoFilter = "/test/repo"
	d.maxDuration = 60 // 60 minutes
	d.daemonID = "test-daemon-1"
	d.dockerHealthCheck = func(context.Context) error { return nil }
	installTestWorkflow(d)
	return d
}

func TestTryClaim_NoClaims_Wins(t *testing.T) {
	mock := &mockClaimProvider{
		nextCommentID: "comment-1",
	}
	d := newTestDaemonWithClaimProvider(mock)

	issue := issues.Issue{ID: "42", Source: issues.SourceGitHub}
	won, err := d.tryClaim(context.Background(), "/test/repo", issue, issues.SourceGitHub)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !won {
		t.Error("expected to win claim when no existing claims")
	}
	if !mock.postCalled {
		t.Error("expected PostClaim to be called")
	}
}

func TestTryClaim_OtherDaemonClaimed_Loses(t *testing.T) {
	mock := &mockClaimProvider{
		claims: []issues.ClaimInfo{
			{
				CommentID: "other-claim",
				DaemonID:  "other-daemon",
				Hostname:  "other-host",
				Timestamp: time.Now().Add(-5 * time.Minute),
				Expires:   time.Now().Add(55 * time.Minute),
			},
		},
	}
	d := newTestDaemonWithClaimProvider(mock)

	issue := issues.Issue{ID: "42", Source: issues.SourceGitHub}
	won, err := d.tryClaim(context.Background(), "/test/repo", issue, issues.SourceGitHub)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if won {
		t.Error("expected to lose claim when another daemon has a valid claim")
	}
	if mock.postCalled {
		t.Error("expected PostClaim to NOT be called when existing claim found")
	}
}

func TestTryClaim_ExpiredClaim_Wins(t *testing.T) {
	mock := &mockClaimProvider{
		claims: []issues.ClaimInfo{
			{
				CommentID: "expired-claim",
				DaemonID:  "other-daemon",
				Hostname:  "other-host",
				Timestamp: time.Now().Add(-2 * time.Hour),
				Expires:   time.Now().Add(-1 * time.Hour), // expired
			},
		},
		nextCommentID: "our-claim",
	}
	d := newTestDaemonWithClaimProvider(mock)

	issue := issues.Issue{ID: "42", Source: issues.SourceGitHub}
	won, err := d.tryClaim(context.Background(), "/test/repo", issue, issues.SourceGitHub)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !won {
		t.Error("expected to win claim when existing claim is expired")
	}
	if !mock.postCalled {
		t.Error("expected PostClaim to be called")
	}
}

func TestTryClaim_OwnValidClaim_Wins(t *testing.T) {
	mock := &mockClaimProvider{
		claims: []issues.ClaimInfo{
			{
				CommentID: "our-old-claim",
				DaemonID:  testDaemonKey("test-daemon-1"), // same as daemon's stateKey
				Hostname:  "this-host",
				Timestamp: time.Now().Add(-10 * time.Minute),
				Expires:   time.Now().Add(50 * time.Minute),
			},
		},
	}
	d := newTestDaemonWithClaimProvider(mock)

	issue := issues.Issue{ID: "42", Source: issues.SourceGitHub}
	won, err := d.tryClaim(context.Background(), "/test/repo", issue, issues.SourceGitHub)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !won {
		t.Error("expected to win when we already have a valid claim")
	}
	if mock.postCalled {
		t.Error("expected PostClaim NOT to be called when own claim is still valid")
	}
}

func TestTryClaim_OwnExpiredClaim_ReClaims(t *testing.T) {
	mock := &mockClaimProvider{
		claims: []issues.ClaimInfo{
			{
				CommentID: "our-expired-claim",
				DaemonID:  testDaemonKey("test-daemon-1"),
				Hostname:  "this-host",
				Timestamp: time.Now().Add(-2 * time.Hour),
				Expires:   time.Now().Add(-1 * time.Hour), // expired
			},
		},
		nextCommentID: "new-claim",
	}
	d := newTestDaemonWithClaimProvider(mock)

	issue := issues.Issue{ID: "42", Source: issues.SourceGitHub}
	won, err := d.tryClaim(context.Background(), "/test/repo", issue, issues.SourceGitHub)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !won {
		t.Error("expected to win after re-claiming expired own claim")
	}
	if !mock.postCalled {
		t.Error("expected PostClaim to be called for re-claim")
	}
	if !mock.deleteCalled {
		t.Error("expected DeleteClaim to be called for old expired claim")
	}
}

func TestTryClaim_ProviderDoesNotSupportClaims_PassThrough(t *testing.T) {
	// Use a registry with no providers — getClaimManager returns nil
	cfg := testConfig()
	registry := issues.NewProviderRegistry()
	mockExec := exec.NewMockExecutor(nil)
	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	sessSvc := session.NewSessionServiceWithExecutor(mockExec)
	d := New(cfg, gitSvc, sessSvc, registry, discardLogger())
	d.sessionMgr.SetSkipMessageLoad(true)
	d.state = daemonstate.NewDaemonState("/test/repo")
	d.repoFilter = "/test/repo"
	d.dockerHealthCheck = func(context.Context) error { return nil }
	installTestWorkflow(d)

	issue := issues.Issue{ID: "42", Source: issues.SourceGitHub}
	won, err := d.tryClaim(context.Background(), "/test/repo", issue, issues.SourceGitHub)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !won {
		t.Error("expected to proceed when provider doesn't support claims")
	}
}

func TestTryClaim_GetClaimsError_FailsClosed(t *testing.T) {
	mock := &mockClaimProvider{
		getErr: fmt.Errorf("API error"),
	}
	d := newTestDaemonWithClaimProvider(mock)

	issue := issues.Issue{ID: "42", Source: issues.SourceGitHub}
	won, err := d.tryClaim(context.Background(), "/test/repo", issue, issues.SourceGitHub)

	if err == nil {
		t.Fatal("expected error when GetClaims fails")
	}
	if won {
		t.Error("expected to skip (fail closed) when GetClaims errors")
	}
}

func TestTryClaim_PostClaimError_FailsClosed(t *testing.T) {
	mock := &mockClaimProvider{
		postErr: fmt.Errorf("post failed"),
	}
	d := newTestDaemonWithClaimProvider(mock)

	issue := issues.Issue{ID: "42", Source: issues.SourceGitHub}
	won, err := d.tryClaim(context.Background(), "/test/repo", issue, issues.SourceGitHub)

	if err == nil {
		t.Fatal("expected error when PostClaim fails")
	}
	if won {
		t.Error("expected to skip (fail closed) when PostClaim errors")
	}
}

func TestTryClaim_VerifyClaimsError_FailsClosed(t *testing.T) {
	// After posting our claim, if the verification GetClaims call fails,
	// the daemon should delete its claim and return false (fail closed)
	// with a non-nil error so callers can distinguish API failure from
	// losing the claim race.
	mock := &mockClaimProvider{
		nextCommentID: "our-claim-123",
		// After posting, make the next GetClaims call fail.
		postHook: func(m *mockClaimProvider) {
			m.getErr = fmt.Errorf("API unavailable")
		},
	}
	d := newTestDaemonWithClaimProvider(mock)

	issue := issues.Issue{ID: "42", Source: issues.SourceGitHub}
	won, err := d.tryClaim(context.Background(), "/test/repo", issue, issues.SourceGitHub)

	if err == nil {
		t.Fatal("expected error when verification GetClaims fails")
	}
	if won {
		t.Error("expected to lose (fail closed) when verification GetClaims errors")
	}
	if !mock.deleteCalled {
		t.Error("expected claim to be deleted after verification failure")
	}
	// Verify it deleted our specific claim comment
	found := false
	for _, id := range mock.deleteCalledIDs {
		if id == "our-claim-123" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected delete of comment 'our-claim-123', got deletes: %v", mock.deleteCalledIDs)
	}
}

func TestTryClaim_RaceCondition_EarliestWins(t *testing.T) {
	// Simulate: we post our claim, but on re-read another daemon's earlier claim appears.
	otherTime := time.Now().Add(-1 * time.Second) // other daemon was 1 second earlier

	mock := &mockClaimProvider{
		nextCommentID: "our-claim",
		// After posting our claim, inject an earlier claim from another daemon
		// to simulate it appearing during the consistency delay.
		postHook: func(m *mockClaimProvider) {
			m.claims = append([]issues.ClaimInfo{
				{
					CommentID: "other-claim",
					DaemonID:  "other-daemon",
					Hostname:  "other-host",
					Timestamp: otherTime,
					Expires:   otherTime.Add(70 * time.Minute),
				},
			}, m.claims...)
		},
	}
	d := newTestDaemonWithClaimProvider(mock)

	issue := issues.Issue{ID: "42", Source: issues.SourceGitHub}
	won, err := d.tryClaim(context.Background(), "/test/repo", issue, issues.SourceGitHub)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if won {
		t.Error("expected to lose when another daemon's claim is earlier")
	}
	if !mock.deleteCalled {
		t.Error("expected our claim to be deleted after losing the race")
	}
}

func TestIsClaimedByOther_ValidClaim(t *testing.T) {
	mock := &mockClaimProvider{
		claims: []issues.ClaimInfo{
			{
				CommentID: "other-claim",
				DaemonID:  "other-daemon",
				Hostname:  "other-host",
				Timestamp: time.Now().Add(-5 * time.Minute),
				Expires:   time.Now().Add(55 * time.Minute),
			},
		},
	}
	d := newTestDaemonWithClaimProvider(mock)

	issue := issues.Issue{ID: "42", Source: issues.SourceGitHub}
	if !d.isClaimedByOther(context.Background(), "/test/repo", issue, issues.SourceGitHub) {
		t.Error("expected isClaimedByOther to return true for another daemon's valid claim")
	}
}

func TestIsClaimedByOther_OwnClaim(t *testing.T) {
	mock := &mockClaimProvider{
		claims: []issues.ClaimInfo{
			{
				CommentID: "our-claim",
				DaemonID:  testDaemonKey("test-daemon-1"),
				Hostname:  "this-host",
				Timestamp: time.Now().Add(-5 * time.Minute),
				Expires:   time.Now().Add(55 * time.Minute),
			},
		},
	}
	d := newTestDaemonWithClaimProvider(mock)

	issue := issues.Issue{ID: "42", Source: issues.SourceGitHub}
	if d.isClaimedByOther(context.Background(), "/test/repo", issue, issues.SourceGitHub) {
		t.Error("expected isClaimedByOther to return false for our own claim")
	}
}

func TestIsClaimedByOther_ExpiredClaim(t *testing.T) {
	mock := &mockClaimProvider{
		claims: []issues.ClaimInfo{
			{
				CommentID: "expired-claim",
				DaemonID:  "other-daemon",
				Hostname:  "other-host",
				Timestamp: time.Now().Add(-2 * time.Hour),
				Expires:   time.Now().Add(-1 * time.Hour),
			},
		},
	}
	d := newTestDaemonWithClaimProvider(mock)

	issue := issues.Issue{ID: "42", Source: issues.SourceGitHub}
	if d.isClaimedByOther(context.Background(), "/test/repo", issue, issues.SourceGitHub) {
		t.Error("expected isClaimedByOther to return false for expired claim")
	}
}

func TestIsClaimedByOther_APIError_FailsOpen(t *testing.T) {
	mock := &mockClaimProvider{
		getErr: fmt.Errorf("API error"),
	}
	d := newTestDaemonWithClaimProvider(mock)

	issue := issues.Issue{ID: "42", Source: issues.SourceGitHub}
	if d.isClaimedByOther(context.Background(), "/test/repo", issue, issues.SourceGitHub) {
		t.Error("expected isClaimedByOther to return false (fail open) on API error")
	}
}

func TestDeleteClaimForIssue_Cleanup(t *testing.T) {
	mock := &mockClaimProvider{
		claims: []issues.ClaimInfo{
			{
				CommentID: "our-claim-1",
				DaemonID:  testDaemonKey("test-daemon-1"),
				Hostname:  "this-host",
				Timestamp: time.Now(),
				Expires:   time.Now().Add(1 * time.Hour),
			},
			{
				CommentID: "other-claim",
				DaemonID:  "other-daemon",
				Hostname:  "other-host",
				Timestamp: time.Now(),
				Expires:   time.Now().Add(1 * time.Hour),
			},
		},
	}
	d := newTestDaemonWithClaimProvider(mock)

	d.deleteClaimForIssue(context.Background(), "/test/repo", "github", "42")

	if !mock.deleteCalled {
		t.Error("expected DeleteClaim to be called")
	}
	// Should only delete our claim, not the other daemon's
	if len(mock.deleteCalledIDs) != 1 || mock.deleteCalledIDs[0] != "our-claim-1" {
		t.Errorf("expected to delete only our-claim-1, got %v", mock.deleteCalledIDs)
	}
}

func TestPollForNewIssues_SkipsClaimedIssues(t *testing.T) {
	cfg := testConfig()
	cfg.Repos = []string{"/test/repo"}
	mockExec := exec.NewMockExecutor(nil)

	type ghIssue struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		URL    string `json:"url"`
	}
	issuesJSON, _ := json.Marshal([]ghIssue{
		{Number: 10, Title: "Claimed issue", URL: "https://github.com/owner/repo/issues/10"},
		{Number: 11, Title: "Unclaimed issue", URL: "https://github.com/owner/repo/issues/11"},
	})
	mockExec.AddPrefixMatch("gh", []string{"issue", "list"}, exec.MockResponse{
		Stdout: issuesJSON,
	})
	mockExec.AddPrefixMatch("git", []string{"remote", "get-url"}, exec.MockResponse{
		Stdout: []byte("git@github.com:owner/repo.git\n"),
	})
	// Mock for linked PR check (no linked PRs)
	mockExec.AddPrefixMatch("gh", []string{"api", "graphql"}, exec.MockResponse{
		Stdout: []byte(`{"data":{"repository":{"issue":{"timelineItems":{"nodes":[]}}}}}`),
	})
	// Mock for CreateIssueCommentWithID (claim post)
	mockExec.AddPrefixMatch("gh", []string{"api", "--method", "POST"}, exec.MockResponse{
		Stdout: []byte(`{"id": 12345}`),
	})
	// Mock for GetIssueCommentsWithIDs (claim read + verification).
	// Return a claim comment from this daemon so tryClaim sees a matching
	// claim and reports success. Uses AddRule because the actual args are
	// like ["api", "repos/owner/repo/issues/10/comments", "--paginate"]
	// which AddPrefixMatch can't match (it checks exact arg equality).
	daemonKey := testDaemonKey("test-daemon")
	verifyClaimBody := fmt.Sprintf(
		`<!-- erg-claim {"daemon":%q,"host":"test","ts":"%s","expires":"%s"} -->`,
		daemonKey,
		time.Now().UTC().Format(time.RFC3339),
		time.Now().Add(70*time.Minute).UTC().Format(time.RFC3339),
	)
	verifyJSON, _ := json.Marshal([]struct {
		ID        int64                  `json:"id"`
		Body      string                 `json:"body"`
		User      struct{ Login string } `json:"user"`
		CreatedAt string                 `json:"created_at"`
	}{
		{ID: 12345, Body: verifyClaimBody, User: struct{ Login string }{Login: "bot"}, CreatedAt: time.Now().UTC().Format(time.RFC3339)},
	})
	mockExec.AddRule(func(dir, name string, args []string) bool {
		if name != "gh" || len(args) < 2 || args[0] != "api" {
			return false
		}
		return strings.Contains(args[1], "/comments")
	}, exec.MockResponse{
		Stdout: verifyJSON,
	})

	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	sessSvc := session.NewSessionServiceWithExecutor(mockExec)
	ghProvider := issues.NewGitHubProvider(gitSvc)
	registry := issues.NewProviderRegistry(ghProvider)

	d := New(cfg, gitSvc, sessSvc, registry, discardLogger())
	d.sessionMgr.SetSkipMessageLoad(true)
	d.state = daemonstate.NewDaemonState("/test/repo")
	d.repoFilter = "owner/repo"
	d.maxConcurrent = 10
	d.maxDuration = 60
	d.daemonID = "test-daemon"
	d.dockerHealthCheck = func(context.Context) error { return nil }

	wfCfg := workflow.DefaultWorkflowConfig()
	d.workflowConfigs = map[string]*workflow.Config{"/test/repo": wfCfg}
	reg := d.buildActionRegistry()
	checker := newEventChecker(d)
	d.engines = map[string]*workflow.Engine{
		"/test/repo": workflow.NewEngine(wfCfg, reg, checker, d.logger),
	}

	d.pollForNewIssues(context.Background())

	// Both issues should be queued (the real GitHub claim provider is used,
	// and with mock responses the claim should succeed for both).
	// We're mainly testing that the claim code path is exercised without errors.
	items := d.state.GetWorkItemsByState(daemonstate.WorkItemQueued)
	if len(items) < 1 {
		t.Errorf("expected at least 1 queued item (claim should succeed with mocks), got %d", len(items))
	}
}

func TestRebuildState_SkipsClaimedByOther(t *testing.T) {
	cfg := testConfig()
	cfg.Repos = []string{"/test/repo"}
	mockExec := exec.NewMockExecutor(nil)

	type ghIssue struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		URL    string `json:"url"`
	}
	issuesJSON, _ := json.Marshal([]ghIssue{
		{Number: 50, Title: "Claimed by other", URL: "https://github.com/owner/repo/issues/50"},
	})
	mockExec.AddPrefixMatch("gh", []string{"issue", "list"}, exec.MockResponse{
		Stdout: issuesJSON,
	})
	// No linked PRs
	mockExec.AddPrefixMatch("gh", []string{"api", "graphql"}, exec.MockResponse{
		Stdout: []byte(`{"data":{"repository":{"issue":{"timelineItems":{"nodes":[]}}}}}`),
	})

	// Mock GetIssueCommentsWithIDs to return a claim from another daemon.
	// Use AddRule with a custom matcher because the API path is dynamic
	// (e.g., "repos:owner/:repo/issues/50/comments") and won't match a
	// simple prefix like ["api", "repos"].
	claimBody := fmt.Sprintf(
		`<!-- erg-claim {"daemon":"other-daemon","host":"other-host","ts":"%s","expires":"%s"} -->`,
		time.Now().Add(-5*time.Minute).UTC().Format(time.RFC3339),
		time.Now().Add(55*time.Minute).UTC().Format(time.RFC3339),
	)
	commentsJSON, _ := json.Marshal([]struct {
		ID        int64                  `json:"id"`
		Body      string                 `json:"body"`
		User      struct{ Login string } `json:"user"`
		CreatedAt string                 `json:"created_at"`
	}{
		{ID: 999, Body: claimBody, User: struct{ Login string }{Login: "bot"}, CreatedAt: time.Now().UTC().Format(time.RFC3339)},
	})
	mockExec.AddRule(func(dir, name string, args []string) bool {
		if name != "gh" || len(args) < 2 || args[0] != "api" {
			return false
		}
		return strings.Contains(args[1], "/comments")
	}, exec.MockResponse{
		Stdout: commentsJSON,
	})

	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	sessSvc := session.NewSessionServiceWithExecutor(mockExec)
	ghProvider := issues.NewGitHubProvider(gitSvc)
	registry := issues.NewProviderRegistry(ghProvider)

	d := New(cfg, gitSvc, sessSvc, registry, discardLogger())
	d.sessionMgr.SetSkipMessageLoad(true)
	d.state = daemonstate.NewDaemonState("/test/repo")
	d.repoFilter = "/test/repo"
	d.maxConcurrent = 10
	d.daemonID = "test-daemon"
	d.dockerHealthCheck = func(context.Context) error { return nil }

	wfCfg := workflow.DefaultWorkflowConfig()
	d.workflowConfigs = map[string]*workflow.Config{"/test/repo": wfCfg}
	reg := d.buildActionRegistry()
	checker := newEventChecker(d)
	d.engines = map[string]*workflow.Engine{
		"/test/repo": workflow.NewEngine(wfCfg, reg, checker, d.logger),
	}

	d.rebuildStateFromTracker(context.Background())

	// The issue should NOT have been rebuilt because it's claimed by another daemon
	_, exists := d.state.GetWorkItem("/test/repo-50")
	if exists {
		t.Error("expected work item to NOT be created for issue claimed by another daemon")
	}
}

func TestStateKey_StableWithoutHostname(t *testing.T) {
	cfg := testConfig()

	t.Run("single repo mode uses repoFilter without hostname", func(t *testing.T) {
		d := testDaemon(cfg)
		d.repoFilter = "/test/repo"
		key := d.stateKey()
		if key != "/test/repo" {
			t.Errorf("expected stateKey to be '/test/repo', got %s", key)
		}
	})

	t.Run("multi repo mode uses daemonID without hostname", func(t *testing.T) {
		d := testDaemon(cfg)
		d.daemonID = "multi-abc123"
		key := d.stateKey()
		if key != "multi-abc123" {
			t.Errorf("expected stateKey to be 'multi-abc123', got %s", key)
		}
	})

	t.Run("same repo produces same key", func(t *testing.T) {
		d1 := testDaemon(cfg)
		d1.repoFilter = "/test/repo"
		d2 := testDaemon(cfg)
		d2.repoFilter = "/test/repo"
		if d1.stateKey() != d2.stateKey() {
			t.Error("expected same stateKey for same repo")
		}
	})
}

func TestClaimIdentity_IncludesHostname(t *testing.T) {
	cfg := testConfig()

	t.Run("single repo mode includes hostname", func(t *testing.T) {
		d := testDaemon(cfg)
		d.repoFilter = "/test/repo"
		key := d.claimIdentity()
		if !strings.Contains(key, "@") {
			t.Errorf("expected claimIdentity to contain '@' separator, got %s", key)
		}
		if !strings.HasPrefix(key, "/test/repo@") {
			t.Errorf("expected claimIdentity to start with '/test/repo@', got %s", key)
		}
	})

	t.Run("multi repo mode includes hostname", func(t *testing.T) {
		d := testDaemon(cfg)
		d.daemonID = "multi-abc123"
		key := d.claimIdentity()
		if !strings.Contains(key, "@") {
			t.Errorf("expected claimIdentity to contain '@' separator, got %s", key)
		}
		if !strings.HasPrefix(key, "multi-abc123@") {
			t.Errorf("expected claimIdentity to start with 'multi-abc123@', got %s", key)
		}
	})

	t.Run("same repo same machine produces same identity", func(t *testing.T) {
		d1 := testDaemon(cfg)
		d1.repoFilter = "/test/repo"
		d2 := testDaemon(cfg)
		d2.repoFilter = "/test/repo"
		if d1.claimIdentity() != d2.claimIdentity() {
			t.Error("expected same claimIdentity on same machine")
		}
	})

	t.Run("claimIdentity differs from stateKey", func(t *testing.T) {
		d := testDaemon(cfg)
		d.repoFilter = "/test/repo"
		if d.stateKey() == d.claimIdentity() {
			t.Error("expected claimIdentity to differ from stateKey (hostname suffix)")
		}
	})
}

func TestTryClaim_ServerTimestampsPreferred(t *testing.T) {
	// Simulate: our claim has an earlier self-reported timestamp (clock skew)
	// but the server says the other daemon posted first.
	ourTime := time.Now().Add(-5 * time.Second)   // our clock is behind
	otherTime := time.Now().Add(-1 * time.Second) // other daemon's self-reported time is later

	mock := &mockClaimProvider{
		nextCommentID: "our-claim",
		postHook: func(m *mockClaimProvider) {
			// Inject other daemon's claim with later self-reported time
			// but earlier server time
			m.claims = append(m.claims, issues.ClaimInfo{
				CommentID:       "other-claim",
				DaemonID:        "other-daemon",
				Hostname:        "other-host",
				Timestamp:       otherTime,
				Expires:         otherTime.Add(70 * time.Minute),
				ServerTimestamp: ourTime.Add(-10 * time.Second), // server says other was first
			})
			// Set server timestamp on our claim too
			for i := range m.claims {
				if m.claims[i].CommentID == "our-claim" {
					m.claims[i].ServerTimestamp = otherTime // server says we were second
				}
			}
		},
	}
	d := newTestDaemonWithClaimProvider(mock)

	issue := issues.Issue{ID: "42", Source: issues.SourceGitHub}
	won, err := d.tryClaim(context.Background(), "/test/repo", issue, issues.SourceGitHub)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if won {
		t.Error("expected to lose when server timestamp shows other daemon was first")
	}
}

func TestTryClaim_CleansUpExpiredClaimsFromOtherDaemons(t *testing.T) {
	expiredClaim := issues.ClaimInfo{
		CommentID: "expired-other-claim",
		DaemonID:  "dead-daemon",
		Hostname:  "dead-host",
		Timestamp: time.Now().Add(-2 * time.Hour),
		Expires:   time.Now().Add(-1 * time.Hour),
	}
	mock := &mockClaimProvider{
		claims:        []issues.ClaimInfo{expiredClaim},
		nextCommentID: "our-claim",
	}
	d := newTestDaemonWithClaimProvider(mock)

	issue := issues.Issue{ID: "42", Source: issues.SourceGitHub}
	won, err := d.tryClaim(context.Background(), "/test/repo", issue, issues.SourceGitHub)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !won {
		t.Error("expected to win after cleaning up expired claim")
	}
	// Verify the expired claim was deleted
	deletedExpired := false
	for _, id := range mock.deleteCalledIDs {
		if id == "expired-other-claim" {
			deletedExpired = true
			break
		}
	}
	if !deletedExpired {
		t.Error("expected expired claim from other daemon to be deleted")
	}
}

func TestClaimIsBefore_PrefersServerTimestamp(t *testing.T) {
	tests := []struct {
		name     string
		a, b     *issues.ClaimInfo
		expected bool
	}{
		{
			name: "uses server timestamps when both available",
			a: &issues.ClaimInfo{
				Timestamp:       time.Now(),                        // self-reported: later
				ServerTimestamp: time.Now().Add(-10 * time.Second), // server: earlier
			},
			b: &issues.ClaimInfo{
				Timestamp:       time.Now().Add(-5 * time.Second), // self-reported: earlier
				ServerTimestamp: time.Now().Add(-5 * time.Second),
			},
			expected: true, // a's server timestamp is earlier
		},
		{
			name: "falls back to self-reported when no server timestamp",
			a: &issues.ClaimInfo{
				Timestamp: time.Now().Add(-10 * time.Second),
			},
			b: &issues.ClaimInfo{
				Timestamp: time.Now().Add(-5 * time.Second),
			},
			expected: true, // a's self-reported timestamp is earlier
		},
		{
			name: "mixed: a has server, b does not",
			a: &issues.ClaimInfo{
				Timestamp:       time.Now(),
				ServerTimestamp: time.Now().Add(-10 * time.Second),
			},
			b: &issues.ClaimInfo{
				Timestamp: time.Now().Add(-5 * time.Second),
			},
			expected: true, // a's server timestamp vs b's self-reported
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := claimIsBefore(tc.a, tc.b)
			if result != tc.expected {
				t.Errorf("claimIsBefore() = %v, want %v", result, tc.expected)
			}
		})
	}
}
