package daemonstate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhubert/erg/internal/paths"
)

func TestRepoKeyFromLock_WithStateFile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	paths.Reset()

	repoPath := "/Users/test/myrepo"
	state := NewDaemonState(repoPath)
	statePath := StateFilePath(repoPath)
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.MarshalIndent(state, "", "  ")
	if err := os.WriteFile(statePath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	lockPath := LockFilePath(repoPath)

	key, err := RepoKeyFromLock(lockPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != repoPath {
		t.Errorf("expected %q, got %q", repoPath, key)
	}
}

func TestRepoKeyFromLock_NoStateFile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	paths.Reset()

	lockPath := filepath.Join(tmpDir, ".erg", "daemon-abcdef123456.lock")
	key, err := RepoKeyFromLock(lockPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "abcdef123456" {
		t.Errorf("expected hash fallback, got %q", key)
	}
}

func TestRepoKeyFromLock_BadName(t *testing.T) {
	_, err := RepoKeyFromLock("/some/path/daemon-.lock")
	if err == nil {
		t.Error("expected error for empty hash")
	}
}

func TestDiscoverRunning_NoDaemons(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	paths.Reset()

	// Create the .erg dir but no lock files
	if err := os.MkdirAll(filepath.Join(tmpDir, ".erg"), 0o755); err != nil {
		t.Fatal(err)
	}

	daemons, err := DiscoverRunning()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(daemons) != 0 {
		t.Errorf("expected 0 daemons, got %d", len(daemons))
	}
}

func TestReadLockFile(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("valid lock file with dead PID", func(t *testing.T) {
		lockPath := filepath.Join(tmpDir, "test.lock")
		os.WriteFile(lockPath, []byte("99999999"), 0o644)
		pid, alive := readLockFile(lockPath)
		if pid != 99999999 {
			t.Errorf("expected pid 99999999, got %d", pid)
		}
		if alive {
			t.Error("expected dead process")
		}
	})

	t.Run("missing lock file", func(t *testing.T) {
		pid, alive := readLockFile(filepath.Join(tmpDir, "nonexistent.lock"))
		if pid != 0 || alive {
			t.Error("expected 0/false for missing file")
		}
	})

	t.Run("invalid content", func(t *testing.T) {
		lockPath := filepath.Join(tmpDir, "bad.lock")
		os.WriteFile(lockPath, []byte("not-a-pid"), 0o644)
		pid, alive := readLockFile(lockPath)
		if pid != 0 || alive {
			t.Error("expected 0/false for invalid content")
		}
	})
}

func TestDiscoverRunning_StaleLock(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	paths.Reset()

	ergDir := filepath.Join(tmpDir, ".erg")
	if err := os.MkdirAll(ergDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a lock file with a PID that doesn't exist
	lockPath := filepath.Join(ergDir, "daemon-aabbccddee11.lock")
	if err := os.WriteFile(lockPath, fmt.Appendf(nil, "%d", 99999999), 0o644); err != nil {
		t.Fatal(err)
	}

	daemons, err := DiscoverRunning()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(daemons) != 0 {
		t.Errorf("expected 0 running daemons (stale lock), got %d", len(daemons))
	}
}
