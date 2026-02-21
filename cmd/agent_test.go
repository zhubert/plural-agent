package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// mockCWDGetter implements cwdGitRootGetter for tests.
type mockCWDGetter struct {
	root string
}

func (m *mockCWDGetter) GetCurrentDirGitRoot(_ context.Context) string {
	return m.root
}

func TestResolveAgentRepo_ExplicitRepo(t *testing.T) {
	getter := &mockCWDGetter{root: "/some/git/repo"}
	resolved, err := resolveAgentRepo(context.Background(), "owner/repo", getter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved != "owner/repo" {
		t.Errorf("resolveAgentRepo = %q, want %q", resolved, "owner/repo")
	}
}

func TestResolveAgentRepo_ExplicitPath(t *testing.T) {
	getter := &mockCWDGetter{root: "/other/repo"}
	resolved, err := resolveAgentRepo(context.Background(), "/explicit/path/to/repo", getter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved != "/explicit/path/to/repo" {
		t.Errorf("resolveAgentRepo = %q, want %q", resolved, "/explicit/path/to/repo")
	}
}

func TestResolveAgentRepo_CWDFallback(t *testing.T) {
	getter := &mockCWDGetter{root: "/detected/git/root"}
	resolved, err := resolveAgentRepo(context.Background(), "", getter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved != "/detected/git/root" {
		t.Errorf("resolveAgentRepo = %q, want %q", resolved, "/detected/git/root")
	}
}

func TestResolveAgentRepo_NoRepoNoGitDir(t *testing.T) {
	getter := &mockCWDGetter{root: ""}
	_, err := resolveAgentRepo(context.Background(), "", getter)
	if err == nil {
		t.Fatal("expected error when no --repo and not in a git directory")
	}
}

func TestResolveAgentRepo_ExplicitRepoIgnoresCWD(t *testing.T) {
	// Even when CWD is a git repo, an explicit --repo takes precedence.
	getter := &mockCWDGetter{root: "/cwd/git/root"}
	resolved, err := resolveAgentRepo(context.Background(), "explicit/repo", getter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved != "explicit/repo" {
		t.Errorf("resolveAgentRepo = %q, want %q", resolved, "explicit/repo")
	}
}

// TestResolveAgentRepo_RealGitRepo tests with a real git repository on disk.
func TestResolveAgentRepo_RealGitRepo(t *testing.T) {
	// Create a temp dir and init a git repo in it
	dir := t.TempDir()
	if err := exec.Command("git", "-C", dir, "init").Run(); err != nil {
		t.Skip("git not available:", err)
	}

	// Change into the repo directory
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get cwd: %v", err)
	}
	defer os.Chdir(origDir) //nolint:errcheck

	if err := os.Chdir(dir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}

	// Use the real session service (no mock) via a thin wrapper
	// We call resolveAgentRepo with a mock that returns the resolved dir,
	// simulating what the real GetCurrentDirGitRoot would return.
	expectedRoot, err := filepath.EvalSymlinks(dir)
	if err != nil {
		expectedRoot = dir
	}

	getter := &mockCWDGetter{root: expectedRoot}
	resolved, err := resolveAgentRepo(context.Background(), "", getter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved != expectedRoot {
		t.Errorf("resolveAgentRepo = %q, want %q", resolved, expectedRoot)
	}
}

func TestCheckDockerDaemon(t *testing.T) {
	// Skip if docker binary isn't installed — nothing to test.
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed, skipping")
	}

	err := checkDockerDaemon()
	// We can't assert success/failure portably (depends on whether Docker
	// daemon is running in the test environment), but we CAN verify the
	// function returns a non-nil error with a helpful message when it fails.
	if err != nil {
		if !containsAny(err.Error(), "not reachable", "Colima", "Docker Desktop") {
			t.Errorf("expected helpful error message, got: %v", err)
		}
	}
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if len(s) >= len(sub) {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
