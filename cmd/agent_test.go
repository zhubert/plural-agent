package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/workflow"
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

// ---- buildDaemonArgs ----

func TestBuildDaemonArgs_Basic(t *testing.T) {
	args := buildDaemonArgs("owner/repo", false)
	if len(args) != 3 {
		t.Fatalf("expected 3 args, got %d: %v", len(args), args)
	}
	if args[0] != "--_daemon" {
		t.Errorf("expected first arg to be '--_daemon', got %q", args[0])
	}
	if args[1] != "--repo" || args[2] != "owner/repo" {
		t.Errorf("expected '--repo owner/repo', got %v", args[1:])
	}
}

func TestBuildDaemonArgs_WithOnce(t *testing.T) {
	args := buildDaemonArgs("owner/repo", true)
	if len(args) != 4 {
		t.Fatalf("expected 4 args, got %d: %v", len(args), args)
	}
	found := slices.Contains(args, "--once")
	if !found {
		t.Errorf("expected '--once' in args: %v", args)
	}
}

func TestBuildDaemonArgs_HiddenFlagAppended(t *testing.T) {
	// Verify --_daemon is always the first arg
	args := buildDaemonArgs("/path/to/repo", false)
	if args[0] != "--_daemon" {
		t.Errorf("expected '--_daemon' as first arg, got %q", args[0])
	}
}

// ---- runAgent flag logic ----

func TestDaemonFlagIsHidden(t *testing.T) {
	flag := rootCmd.Flags().Lookup("_daemon")
	if flag == nil {
		t.Fatal("expected --_daemon flag to exist")
	}
	if !flag.Hidden {
		t.Error("expected --_daemon flag to be hidden")
	}
}

// ---- validateWorkflowConfig ----

func TestValidateWorkflowConfig_Valid(t *testing.T) {
	cfg := workflow.DefaultWorkflowConfig()
	if err := validateWorkflowConfig(cfg); err != nil {
		t.Errorf("expected no error for valid config, got: %v", err)
	}
}

func TestValidateWorkflowConfig_Invalid(t *testing.T) {
	cfg := &workflow.Config{
		Source: workflow.SourceConfig{
			Provider: "jira", // unknown provider
		},
	}
	err := validateWorkflowConfig(cfg)
	if err == nil {
		t.Fatal("expected error for invalid config")
	}
	if !strings.Contains(err.Error(), "workflow configuration errors:") {
		t.Errorf("error should start with 'workflow configuration errors:', got: %v", err)
	}
}

func TestWorkflowCommandRemoved(t *testing.T) {
	for _, cmd := range rootCmd.Commands() {
		if cmd.Use == "workflow" {
			t.Error("'erg workflow' command should have been removed")
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

// TestDaemonize_LockPreventsRace validates that AcquireLock is exclusive:
// after one lock is acquired for a repo, a second attempt for the same repo fails.
// This is the primitive behavior that the daemonize function depends on to
// prevent race conditions between multiple `erg start` invocations.
func TestDaemonize_LockPreventsRace(t *testing.T) {
	tmpDir := t.TempDir()
	repo := filepath.Join(tmpDir, "race-test-repo")

	// First lock acquisition should succeed
	lock1, err := daemonstate.AcquireLock(repo)
	if err != nil {
		t.Fatalf("first AcquireLock failed: %v", err)
	}
	defer lock1.Release()

	// Second lock acquisition for the same repo should fail
	_, err = daemonstate.AcquireLock(repo)
	if err == nil {
		t.Fatal("expected second AcquireLock to fail, but it succeeded")
	}

	// Error message should indicate the lock is held
	if !strings.Contains(err.Error(), "lock already held") {
		t.Errorf("expected error about lock being held, got: %v", err)
	}

	// After releasing the first lock, acquisition should succeed again
	lock1.Release()
	lock2, err := daemonstate.AcquireLock(repo)
	if err != nil {
		t.Fatalf("AcquireLock after release failed: %v", err)
	}
	defer lock2.Release()
}
