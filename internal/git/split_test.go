package git

import (
	"context"
	"fmt"
	"testing"

	pexec "github.com/zhubert/erg/internal/exec"
)

func TestAnalyzeDiffForSplit_NoChangedFiles(t *testing.T) {
	mock := pexec.NewMockExecutor(nil)
	mock.AddPrefixMatch("git", []string{"fetch", "origin"}, pexec.MockResponse{})
	mock.AddPrefixMatch("git", []string{"rev-parse", "--verify"}, pexec.MockResponse{
		Stdout: []byte("abc123\n"),
	})
	// git diff --name-only returns empty → no changed files
	mock.AddPrefixMatch("git", []string{"diff", "--name-only"}, pexec.MockResponse{
		Stdout: []byte(""),
	})
	mock.AddPrefixMatch("git", []string{"symbolic-ref"}, pexec.MockResponse{
		Stdout: []byte("refs/remotes/origin/main\n"),
	})

	s := NewGitServiceWithExecutor(mock)
	_, err := s.AnalyzeDiffForSplit(context.Background(), "/repo", "feature", "main")
	if err == nil {
		t.Error("expected error when no changed files")
	}
}

func TestAnalyzeDiffForSplit_ClaudeFailure(t *testing.T) {
	mock := pexec.NewMockExecutor(nil)
	mock.AddPrefixMatch("git", []string{"fetch", "origin"}, pexec.MockResponse{})
	mock.AddPrefixMatch("git", []string{"rev-parse", "--verify"}, pexec.MockResponse{
		Stdout: []byte("abc123\n"),
	})
	mock.AddPrefixMatch("git", []string{"diff", "--name-only"}, pexec.MockResponse{
		Stdout: []byte("auth.go\n"),
	})
	mock.AddPrefixMatch("git", []string{"diff", "--no-ext-diff"}, pexec.MockResponse{
		Stdout: []byte("diff content\n"),
	})
	mock.AddPrefixMatch("claude", []string{"--print", "-p"}, pexec.MockResponse{
		Err: fmt.Errorf("claude: not found"),
	})
	mock.AddPrefixMatch("git", []string{"symbolic-ref"}, pexec.MockResponse{
		Stdout: []byte("refs/remotes/origin/main\n"),
	})

	s := NewGitServiceWithExecutor(mock)
	_, err := s.AnalyzeDiffForSplit(context.Background(), "/repo", "feature", "main")
	if err == nil {
		t.Error("expected error when Claude fails")
	}
}

func TestAnalyzeDiffForSplit_InvalidJSON(t *testing.T) {
	mock := pexec.NewMockExecutor(nil)
	mock.AddPrefixMatch("git", []string{"fetch", "origin"}, pexec.MockResponse{})
	mock.AddPrefixMatch("git", []string{"rev-parse", "--verify"}, pexec.MockResponse{
		Stdout: []byte("abc123\n"),
	})
	mock.AddPrefixMatch("git", []string{"diff", "--name-only"}, pexec.MockResponse{
		Stdout: []byte("auth.go\n"),
	})
	mock.AddPrefixMatch("git", []string{"diff", "--no-ext-diff"}, pexec.MockResponse{
		Stdout: []byte("diff content\n"),
	})
	// Claude returns invalid JSON
	mock.AddPrefixMatch("claude", []string{"--print", "-p"}, pexec.MockResponse{
		Stdout: []byte("this is not json"),
	})
	mock.AddPrefixMatch("git", []string{"symbolic-ref"}, pexec.MockResponse{
		Stdout: []byte("refs/remotes/origin/main\n"),
	})

	s := NewGitServiceWithExecutor(mock)
	_, err := s.AnalyzeDiffForSplit(context.Background(), "/repo", "feature", "main")
	if err == nil {
		t.Error("expected error when Claude returns invalid JSON")
	}
}

func TestAnalyzeDiffForSplit_EmptyGroups(t *testing.T) {
	mock := pexec.NewMockExecutor(nil)
	mock.AddPrefixMatch("git", []string{"fetch", "origin"}, pexec.MockResponse{})
	mock.AddPrefixMatch("git", []string{"rev-parse", "--verify"}, pexec.MockResponse{
		Stdout: []byte("abc123\n"),
	})
	mock.AddPrefixMatch("git", []string{"diff", "--name-only"}, pexec.MockResponse{
		Stdout: []byte("auth.go\n"),
	})
	mock.AddPrefixMatch("git", []string{"diff", "--no-ext-diff"}, pexec.MockResponse{
		Stdout: []byte("diff content\n"),
	})
	// Claude returns JSON with no groups
	mock.AddPrefixMatch("claude", []string{"--print", "-p"}, pexec.MockResponse{
		Stdout: []byte(`{"groups":[]}`),
	})
	mock.AddPrefixMatch("git", []string{"symbolic-ref"}, pexec.MockResponse{
		Stdout: []byte("refs/remotes/origin/main\n"),
	})

	s := NewGitServiceWithExecutor(mock)
	_, err := s.AnalyzeDiffForSplit(context.Background(), "/repo", "feature", "main")
	if err == nil {
		t.Error("expected error when Claude returns empty groups")
	}
}

func TestAnalyzeDiffForSplit_Success(t *testing.T) {
	mock := pexec.NewMockExecutor(nil)
	mock.AddPrefixMatch("git", []string{"fetch", "origin"}, pexec.MockResponse{})
	mock.AddPrefixMatch("git", []string{"rev-parse", "--verify"}, pexec.MockResponse{
		Stdout: []byte("abc123\n"),
	})
	mock.AddPrefixMatch("git", []string{"diff", "--name-only"}, pexec.MockResponse{
		Stdout: []byte("auth.go\ntests.go\n"),
	})
	mock.AddPrefixMatch("git", []string{"diff", "--no-ext-diff"}, pexec.MockResponse{
		Stdout: []byte("diff content\n"),
	})
	plan := `{"groups":[{"name":"auth","title":"Add auth","description":"Auth impl","files":["auth.go"]},{"name":"tests","title":"Add tests","description":"Tests","files":["tests.go"]}]}`
	mock.AddPrefixMatch("claude", []string{"--print", "-p"}, pexec.MockResponse{
		Stdout: []byte(plan),
	})
	mock.AddPrefixMatch("git", []string{"symbolic-ref"}, pexec.MockResponse{
		Stdout: []byte("refs/remotes/origin/main\n"),
	})

	s := NewGitServiceWithExecutor(mock)
	result, err := s.AnalyzeDiffForSplit(context.Background(), "/repo", "feature", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Groups) != 2 {
		t.Errorf("expected 2 groups, got %d", len(result.Groups))
	}
	if result.Groups[0].Name != "auth" {
		t.Errorf("expected first group name 'auth', got %q", result.Groups[0].Name)
	}
	if len(result.Groups[0].Files) != 1 || result.Groups[0].Files[0] != "auth.go" {
		t.Errorf("expected first group files [auth.go], got %v", result.Groups[0].Files)
	}
}

func TestAnalyzeDiffForSplit_UsesDefaultBranch(t *testing.T) {
	// When baseBranch is empty, GetDefaultBranch should be called.
	mock := pexec.NewMockExecutor(nil)
	mock.AddPrefixMatch("git", []string{"symbolic-ref"}, pexec.MockResponse{
		Stdout: []byte("refs/remotes/origin/main\n"),
	})
	// fetch will fail (no remote configured in test) but that's okay
	mock.AddPrefixMatch("git", []string{"fetch", "origin"}, pexec.MockResponse{
		Err: fmt.Errorf("no remote"),
	})
	// Since fetch failed, comparisonRef falls back to local baseBranch "main"
	mock.AddPrefixMatch("git", []string{"diff", "--name-only"}, pexec.MockResponse{
		Stdout: []byte("auth.go\n"),
	})
	mock.AddPrefixMatch("git", []string{"diff", "--no-ext-diff"}, pexec.MockResponse{
		Stdout: []byte("diff content\n"),
	})
	mock.AddPrefixMatch("claude", []string{"--print", "-p"}, pexec.MockResponse{
		Stdout: []byte(`{"groups":[{"name":"auth","title":"Auth","description":"desc","files":["auth.go"]}]}`),
	})

	s := NewGitServiceWithExecutor(mock)
	result, err := s.AnalyzeDiffForSplit(context.Background(), "/repo", "feature", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Groups) != 1 {
		t.Errorf("expected 1 group, got %d", len(result.Groups))
	}
}

func TestCreateSplitBranch_Success(t *testing.T) {
	mock := pexec.NewMockExecutor(nil)
	mock.AddPrefixMatch("git", []string{"checkout", "-b"}, pexec.MockResponse{})
	mock.AddPrefixMatch("git", []string{"checkout", "feature-branch", "--"}, pexec.MockResponse{})
	mock.AddPrefixMatch("git", []string{"commit", "-m"}, pexec.MockResponse{})
	mock.AddPrefixMatch("git", []string{"push", "-u"}, pexec.MockResponse{})
	mock.AddPrefixMatch("gh", []string{"pr", "create"}, pexec.MockResponse{})
	// Return to original branch
	mock.AddPrefixMatch("git", []string{"checkout", "feature-branch"}, pexec.MockResponse{})

	s := NewGitServiceWithExecutor(mock)
	err := s.CreateSplitBranch(
		context.Background(),
		"/repo",
		"feature-branch",
		"feature-branch-split-auth",
		"main",
		"Add authentication",
		"## Summary\nAuth changes",
		[]string{"auth.go"},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify gh pr create was called with correct flags
	calls := mock.GetCalls()
	found := false
	for _, c := range calls {
		if c.Name == "gh" && len(c.Args) >= 2 && c.Args[0] == "pr" && c.Args[1] == "create" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected gh pr create to be called")
	}
}

func TestCreateSplitBranch_CheckoutNewBranchFails(t *testing.T) {
	mock := pexec.NewMockExecutor(nil)
	mock.AddPrefixMatch("git", []string{"checkout", "-b"}, pexec.MockResponse{
		Err: fmt.Errorf("branch already exists"),
	})

	s := NewGitServiceWithExecutor(mock)
	err := s.CreateSplitBranch(
		context.Background(),
		"/repo",
		"feature-branch",
		"feature-branch-split-auth",
		"main",
		"Add authentication",
		"## Summary\nAuth changes",
		[]string{"auth.go"},
	)
	if err == nil {
		t.Error("expected error when checkout -b fails")
	}
}

func TestCreateSplitBranch_PushFails(t *testing.T) {
	mock := pexec.NewMockExecutor(nil)
	mock.AddPrefixMatch("git", []string{"checkout", "-b"}, pexec.MockResponse{})
	mock.AddPrefixMatch("git", []string{"checkout", "feature-branch", "--"}, pexec.MockResponse{})
	mock.AddPrefixMatch("git", []string{"commit", "-m"}, pexec.MockResponse{})
	mock.AddPrefixMatch("git", []string{"push", "-u"}, pexec.MockResponse{
		Err: fmt.Errorf("push failed: network error"),
	})
	// Best-effort return to original branch
	mock.AddPrefixMatch("git", []string{"checkout", "feature-branch"}, pexec.MockResponse{})

	s := NewGitServiceWithExecutor(mock)
	err := s.CreateSplitBranch(
		context.Background(),
		"/repo",
		"feature-branch",
		"feature-branch-split-auth",
		"main",
		"Add authentication",
		"## Summary\nAuth changes",
		[]string{"auth.go"},
	)
	if err == nil {
		t.Error("expected error when push fails")
	}
}
