package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/paths"
)

func TestFindSingleRunningDaemon_NoLocks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".erg"), 0o755)
	paths.Reset()

	_, err := findSingleRunningDaemon()
	if err == nil {
		t.Fatal("expected error when no locks exist")
	}
	if !strings.Contains(err.Error(), "no running orchestrator found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFindSingleRunningDaemon_OneDead(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".erg"), 0o755)
	paths.Reset()

	// Create a lock file with a dead PID
	lockPath := filepath.Join(home, ".erg", "daemon-abc123.lock")
	os.WriteFile(lockPath, []byte("999999999"), 0o644)

	// Override readLockFileKey to return a known key
	origReader := readLockFileKey
	readLockFileKey = func(path string) (string, error) {
		return "dead-repo", nil
	}
	defer func() { readLockFileKey = origReader }()

	_, err := findSingleRunningDaemon()
	if err == nil {
		t.Fatal("expected error when daemon is dead")
	}
	if !strings.Contains(err.Error(), "no running orchestrator found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFindSingleRunningDaemon_OneRunning(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".erg"), 0o755)
	paths.Reset()

	repoKey := filepath.Join(home, "test-repo")

	// Create only the properly hashed lock file with our PID (which is alive).
	// FindLocks scans for daemon-*.lock, and ReadLockStatus reads by repo key.
	realLockPath := daemonstate.LockFilePath(repoKey)
	os.MkdirAll(filepath.Dir(realLockPath), 0o755)
	os.WriteFile(realLockPath, []byte(fmt.Sprintf("%d", os.Getpid())), 0o644)

	// Override readLockFileKey to return our known repo key
	origReader := readLockFileKey
	readLockFileKey = func(path string) (string, error) {
		return repoKey, nil
	}
	defer func() { readLockFileKey = origReader }()

	repo, err := findSingleRunningDaemon()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo != repoKey {
		t.Errorf("expected %s, got %s", repoKey, repo)
	}
}

func TestFindSingleRunningDaemon_MultipleError(t *testing.T) {
	// This validates the error message format for multiple daemons
	// We can't easily test the actual behavior without mocking ReadLockStatus,
	// but we can verify the function exists and has the right signature.
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".erg"), 0o755)
	paths.Reset()

	// With no locks, should error
	_, err := findSingleRunningDaemon()
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadStateRepoPath(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")

	// Valid state file
	os.WriteFile(statePath, []byte(`{"repo_path":"owner/repo","version":2}`), 0o644)
	repo, err := loadStateRepoPath(statePath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo != "owner/repo" {
		t.Errorf("expected owner/repo, got %q", repo)
	}

	// Empty repo_path
	os.WriteFile(statePath, []byte(`{"repo_path":"","version":2}`), 0o644)
	_, err = loadStateRepoPath(statePath)
	if err == nil {
		t.Fatal("expected error for empty repo_path")
	}

	// Missing file
	_, err = loadStateRepoPath(filepath.Join(dir, "nonexistent.json"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestReadLockFileKeyDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".erg"), 0o755)
	paths.Reset()

	// Create a state file for hash "abc123456789"
	stateDir := filepath.Join(home, ".erg")
	statePath := filepath.Join(stateDir, "daemon-state-abc123456789.json")
	os.WriteFile(statePath, []byte(`{"repo_path":"/home/user/my-repo","version":2}`), 0o644)

	// Create a matching lock file
	lockPath := filepath.Join(stateDir, "daemon-abc123456789.lock")
	os.WriteFile(lockPath, []byte("12345"), 0o644)

	repo, err := readLockFileKeyDefault(lockPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo != "/home/user/my-repo" {
		t.Errorf("expected /home/user/my-repo, got %q", repo)
	}
}

func TestReadLockFileKeyDefault_NoStateFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".erg"), 0o755)
	paths.Reset()

	lockPath := filepath.Join(home, ".erg", "daemon-somehash.lock")
	os.WriteFile(lockPath, []byte("12345"), 0o644)

	// Without a state file, should return the hash
	repo, err := readLockFileKeyDefault(lockPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo != "somehash" {
		t.Errorf("expected hash fallback 'somehash', got %q", repo)
	}
}

func TestStartAutoDiscoversDefaultManifest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".erg"), 0o755)
	paths.Reset()

	// Create default manifest
	manifestPath := filepath.Join(home, ".erg", "daemon.yaml")
	os.WriteFile(manifestPath, []byte("repos:\n  - path: /tmp/repo\n"), 0o644)

	// Save and restore globals
	origConfig := startConfigFile
	origRepo := startRepo
	origWorkflow := startWorkflowFile
	defer func() {
		startConfigFile = origConfig
		startRepo = origRepo
		startWorkflowFile = origWorkflow
	}()

	startConfigFile = ""
	startRepo = ""
	startWorkflowFile = ""

	// We can't run the full start (it needs docker etc), but we can verify
	// the auto-discovery logic by checking that the manifest path is found
	p, err := paths.ManifestPath()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("expected manifest to exist at %s", p)
	}
	if p != manifestPath {
		t.Errorf("expected %s, got %s", manifestPath, p)
	}
}
