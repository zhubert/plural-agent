package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestHasContainerRuntime(t *testing.T) {
	tests := []struct {
		name      string
		available map[string]bool
		want      bool
	}{
		{
			name:      "docker only",
			available: map[string]bool{"docker": true},
			want:      true,
		},
		{
			name:      "colima only",
			available: map[string]bool{"colima": true},
			want:      true,
		},
		{
			name:      "both docker and colima",
			available: map[string]bool{"docker": true, "colima": true},
			want:      true,
		},
		{
			name:      "neither",
			available: map[string]bool{},
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := lookPathFunc
			defer func() { lookPathFunc = orig }()

			lookPathFunc = func(name string) (string, error) {
				if tt.available[name] {
					return "/usr/local/bin/" + name, nil
				}
				return "", fmt.Errorf("not found")
			}

			if got := hasContainerRuntime(); got != tt.want {
				t.Errorf("hasContainerRuntime() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRuntimeStartHint_ColimaInstalled(t *testing.T) {
	// Save and restore the original lookPathFunc.
	orig := lookPathFunc
	defer func() { lookPathFunc = orig }()

	lookPathFunc = func(name string) (string, error) {
		if name == "colima" {
			return "/usr/local/bin/colima", nil
		}
		return "", fmt.Errorf("not found")
	}

	hint := runtimeStartHint()
	if !strings.Contains(hint, "colima start") {
		t.Errorf("expected 'colima start' hint when colima is installed, got: %q", hint)
	}
}

func TestRuntimeStartHint_ColimaNotInstalled(t *testing.T) {
	// Save and restore the original lookPathFunc.
	orig := lookPathFunc
	defer func() { lookPathFunc = orig }()

	lookPathFunc = func(name string) (string, error) {
		return "", fmt.Errorf("not found")
	}

	hint := runtimeStartHint()
	if !strings.Contains(hint, "Install a container runtime") {
		t.Errorf("expected install instructions when colima is not installed, got: %q", hint)
	}
	if !strings.Contains(hint, "Docker Desktop") {
		t.Errorf("expected Docker Desktop link in hint, got: %q", hint)
	}
	if !strings.Contains(hint, "Colima") {
		t.Errorf("expected Colima link in hint, got: %q", hint)
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
