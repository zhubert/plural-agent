package daemon

import (
	"context"
	"fmt"
	"testing"

	"github.com/zhubert/erg/internal/config"
	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/exec"
)

func TestMergePR_RebasesOnFailure(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)
	cfg := testConfig()
	d := testDaemonWithExec(cfg, mockExec)
	d.repoFilter = "/test/repo"

	sess := testSession("sess-1")
	sess.BaseBranch = "main"
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "wi-1",
		SessionID: "sess-1",
		Branch:    sess.Branch,
		StepData:  map[string]any{},
	})

	item, _ := d.state.GetWorkItem("wi-1")

	// First MergePR call fails (gh pr merge feature-sess-1 --rebase)
	mergeAttempts := 0
	mockExec.AddRule(func(dir, name string, args []string) bool {
		if name == "gh" && len(args) >= 3 && args[0] == "pr" && args[1] == "merge" {
			mergeAttempts++
			return mergeAttempts == 1
		}
		return false
	}, exec.MockResponse{Err: fmt.Errorf("merge failed"), Stderr: []byte("merge failed")})

	// RebaseBranch: git fetch origin main
	mockExec.AddExactMatch("git", []string{"fetch", "origin", "main"}, exec.MockResponse{})
	// RebaseBranch: git rebase origin/main
	mockExec.AddExactMatch("git", []string{"rebase", "origin/main"}, exec.MockResponse{})
	// RebaseBranch: git push --force-with-lease origin <branch>
	mockExec.AddExactMatch("git", []string{"push", "--force-with-lease", "origin", sess.Branch}, exec.MockResponse{})

	// Second MergePR call succeeds (catch-all for gh pr merge)
	mockExec.AddPrefixMatch("gh", []string{"pr", "merge"}, exec.MockResponse{})

	ctx := context.Background()
	err := d.mergePR(ctx, item)
	if err != nil {
		t.Fatalf("expected mergePR to succeed after linearization, got: %v", err)
	}

	// Verify the session was marked as merged
	updatedSess := cfg.GetSession("sess-1")
	if updatedSess == nil {
		t.Fatal("session not found after merge")
	}
	if !updatedSess.PRMerged {
		t.Error("expected session to be marked as PR merged")
	}

	// Verify merge was attempted twice
	if mergeAttempts != 2 {
		t.Errorf("expected 2 merge attempts, got %d", mergeAttempts)
	}

	// Verify rebase commands were called
	calls := mockExec.GetCalls()
	foundFetch := false
	foundRebase := false
	foundForcePush := false
	for _, c := range calls {
		if c.Name == "git" && len(c.Args) >= 3 && c.Args[0] == "fetch" && c.Args[1] == "origin" && c.Args[2] == "main" {
			foundFetch = true
		}
		if c.Name == "git" && len(c.Args) >= 2 && c.Args[0] == "rebase" && c.Args[1] == "origin/main" {
			foundRebase = true
		}
		if c.Name == "git" && len(c.Args) >= 4 && c.Args[0] == "push" && c.Args[1] == "--force-with-lease" {
			foundForcePush = true
		}
	}
	if !foundFetch {
		t.Error("expected git fetch origin main to be called")
	}
	if !foundRebase {
		t.Error("expected git rebase origin/main to be called")
	}
	if !foundForcePush {
		t.Error("expected git push --force-with-lease to be called")
	}
}

func TestMergePR_RebaseFailsFallsThrough(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)
	cfg := testConfig()
	d := testDaemonWithExec(cfg, mockExec)
	d.repoFilter = "/test/repo"

	sess := testSession("sess-2")
	sess.BaseBranch = "main"
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "wi-2",
		SessionID: "sess-2",
		Branch:    sess.Branch,
		StepData:  map[string]any{},
	})

	item, _ := d.state.GetWorkItem("wi-2")

	// MergePR fails
	mockExec.AddPrefixMatch("gh", []string{"pr", "merge"}, exec.MockResponse{
		Err:    fmt.Errorf("merge failed"),
		Stderr: []byte("merge failed"),
	})

	// RebaseBranch: fetch succeeds
	mockExec.AddExactMatch("git", []string{"fetch", "origin", "main"}, exec.MockResponse{})
	// RebaseBranch: rebase fails (real conflicts)
	mockExec.AddExactMatch("git", []string{"rebase", "origin/main"}, exec.MockResponse{
		Err: fmt.Errorf("rebase conflict"),
	})
	// RebaseBranch: abort after failure
	mockExec.AddExactMatch("git", []string{"rebase", "--abort"}, exec.MockResponse{})

	ctx := context.Background()
	err := d.mergePR(ctx, item)
	if err == nil {
		t.Fatal("expected mergePR to return an error")
	}

	// Should return the original merge error, not the rebase error
	if err.Error() != "gh pr merge failed: merge failed" {
		t.Errorf("expected original merge error, got: %v", err)
	}

	// Verify session was NOT marked as merged
	updatedSess := cfg.GetSession("sess-2")
	if updatedSess != nil && updatedSess.PRMerged {
		t.Error("session should not be marked as merged when merge fails")
	}
}

func TestMergePR_NonRebaseMethodNoRetry(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)
	cfg := testConfig()
	d := testDaemonWithExec(cfg, mockExec)
	d.repoFilter = "/test/repo"
	d.mergeMethod = "squash" // Non-rebase method

	sess := testSession("sess-3")
	sess.BaseBranch = "main"
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "wi-3",
		SessionID: "sess-3",
		Branch:    sess.Branch,
		StepData:  map[string]any{},
	})

	item, _ := d.state.GetWorkItem("wi-3")

	// MergePR fails (squash merge)
	mergeAttempts := 0
	mockExec.AddRule(func(dir, name string, args []string) bool {
		if name == "gh" && len(args) >= 3 && args[0] == "pr" && args[1] == "merge" {
			mergeAttempts++
			return true
		}
		return false
	}, exec.MockResponse{Err: fmt.Errorf("squash merge failed"), Stderr: []byte("squash merge failed")})

	ctx := context.Background()
	err := d.mergePR(ctx, item)
	if err == nil {
		t.Fatal("expected mergePR to return an error")
	}

	// Should return error directly without retry
	if mergeAttempts != 1 {
		t.Errorf("expected exactly 1 merge attempt for squash method, got %d", mergeAttempts)
	}

	// Should not have called any rebase commands
	calls := mockExec.GetCalls()
	for _, c := range calls {
		if c.Name == "git" && len(c.Args) > 0 && c.Args[0] == "rebase" {
			t.Error("should not attempt rebase for non-rebase merge method")
		}
	}
}

// Silence unused import warning for config (used in testSession from daemon_test.go).
var _ = config.Session{}
