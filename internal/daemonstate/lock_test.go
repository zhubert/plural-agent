package daemonstate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAcquireLock_Success(t *testing.T) {
	tmpDir := t.TempDir()
	repoPath := filepath.Join(tmpDir, "test-repo-lock")

	// Override the lock file path by using a unique repo path
	lock, err := AcquireLock(repoPath)
	if err != nil {
		t.Fatalf("failed to acquire lock: %v", err)
	}
	defer lock.Release()

	// Lock file should exist
	if _, err := os.Stat(lock.path); os.IsNotExist(err) {
		t.Error("lock file should exist")
	}
}

func TestAcquireLock_AlreadyHeld(t *testing.T) {
	tmpDir := t.TempDir()
	repoPath := filepath.Join(tmpDir, "test-repo-contention")

	lock1, err := AcquireLock(repoPath)
	if err != nil {
		t.Fatalf("failed to acquire first lock: %v", err)
	}
	defer lock1.Release()

	// Second acquire should fail with descriptive error containing PID
	_, err = AcquireLock(repoPath)
	if err == nil {
		t.Fatal("expected error when lock already held")
	}
	if !strings.Contains(err.Error(), "daemon lock already held") {
		t.Errorf("expected 'daemon lock already held' in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "PID") {
		t.Errorf("expected PID in error message, got: %v", err)
	}
}

func TestAcquireLock_ReleaseAndReacquire(t *testing.T) {
	tmpDir := t.TempDir()
	repoPath := filepath.Join(tmpDir, "test-repo-reacquire")

	lock1, err := AcquireLock(repoPath)
	if err != nil {
		t.Fatalf("failed to acquire first lock: %v", err)
	}

	// Release
	if err := lock1.Release(); err != nil {
		t.Fatalf("failed to release lock: %v", err)
	}

	// Re-acquire should succeed
	lock2, err := AcquireLock(repoPath)
	if err != nil {
		t.Fatalf("failed to reacquire lock: %v", err)
	}
	defer lock2.Release()
}

func TestClearLocks_RemovesAllLocks(t *testing.T) {
	tmpDir := t.TempDir()

	// Acquire multiple locks
	lock1, err := AcquireLock(filepath.Join(tmpDir, "repo-1"))
	if err != nil {
		t.Fatalf("failed to acquire lock 1: %v", err)
	}
	lock2, err := AcquireLock(filepath.Join(tmpDir, "repo-2"))
	if err != nil {
		t.Fatalf("failed to acquire lock 2: %v", err)
	}

	// Close files before clear (to avoid file handle issues on some OS)
	lock1.file.Close()
	lock2.file.Close()

	removed, err := ClearLocks()
	if err != nil {
		t.Fatalf("ClearLocks failed: %v", err)
	}
	if removed < 2 {
		t.Errorf("expected at least 2 locks removed, got %d", removed)
	}
}

func TestClearLocks_EmptyDir(t *testing.T) {
	// ClearLocks on an empty state dir — should return 0
	// The actual state dir might have existing locks from other tests,
	// so we can only verify it doesn't error.
	_, err := ClearLocks()
	if err != nil {
		t.Fatalf("ClearLocks on empty dir should not error: %v", err)
	}
}

func TestFindLocks_FindsLockFiles(t *testing.T) {
	tmpDir := t.TempDir()

	lock, err := AcquireLock(filepath.Join(tmpDir, "repo-findtest"))
	if err != nil {
		t.Fatalf("failed to acquire lock: %v", err)
	}
	defer lock.Release()

	locks, err := FindLocks()
	if err != nil {
		t.Fatalf("FindLocks failed: %v", err)
	}

	// Should find at least our lock
	found := false
	for _, l := range locks {
		if l == lock.path {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected FindLocks to discover our lock file")
	}
}

func TestLockFilePath_Deterministic(t *testing.T) {
	path1 := LockFilePath("/some/repo")
	path2 := LockFilePath("/some/repo")
	if path1 != path2 {
		t.Error("expected same path for same repo")
	}

	path3 := LockFilePath("/other/repo")
	if path1 == path3 {
		t.Error("expected different paths for different repos")
	}
}

func TestAcquireLock_StaleLockCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	repoPath := filepath.Join(tmpDir, "test-repo-stale")

	// Manually create a lock file with a PID that doesn't exist
	fp := LockFilePath(repoPath)
	if err := os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
		t.Fatalf("failed to create lock directory: %v", err)
	}
	if err := os.WriteFile(fp, []byte("999999999"), 0o644); err != nil {
		t.Fatalf("failed to write stale lock file: %v", err)
	}

	// AcquireLock should detect the stale lock, remove it, and succeed
	lock, err := AcquireLock(repoPath)
	if err != nil {
		t.Fatalf("expected AcquireLock to succeed after stale lock cleanup, got: %v", err)
	}
	defer lock.Release()

	// Verify the lock file exists and contains our current PID
	data, err := os.ReadFile(fp)
	if err != nil {
		t.Fatalf("failed to read lock file: %v", err)
	}
	pidStr := strings.TrimSpace(string(data))
	expectedPID := fmt.Sprintf("%d", os.Getpid())
	if pidStr != expectedPID {
		t.Errorf("expected lock file to contain PID %s, got %s", expectedPID, pidStr)
	}
}
