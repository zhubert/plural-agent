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

func TestReadLockStatus_NoLockFile(t *testing.T) {
	tmpDir := t.TempDir()
	repoPath := filepath.Join(tmpDir, "no-lock-repo")

	pid, running := ReadLockStatus(repoPath)
	if pid != 0 {
		t.Errorf("expected pid=0 for missing lock file, got %d", pid)
	}
	if running {
		t.Error("expected running=false for missing lock file")
	}
}

func TestReadLockStatus_RunningProcess(t *testing.T) {
	tmpDir := t.TempDir()
	repoPath := filepath.Join(tmpDir, "running-repo")

	// Acquire a real lock (writes current PID)
	lock, err := AcquireLock(repoPath)
	if err != nil {
		t.Fatalf("failed to acquire lock: %v", err)
	}
	defer lock.Release()

	pid, running := ReadLockStatus(repoPath)
	if pid != os.Getpid() {
		t.Errorf("expected current PID %d, got %d", os.Getpid(), pid)
	}
	if !running {
		t.Error("expected running=true for current process lock")
	}
}

func TestReadLockStatus_StaleLock(t *testing.T) {
	tmpDir := t.TempDir()
	repoPath := filepath.Join(tmpDir, "stale-repo")

	// Write a lock file with a PID that almost certainly doesn't exist
	fp := LockFilePath(repoPath)
	if err := os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
		t.Fatalf("failed to create lock dir: %v", err)
	}
	if err := os.WriteFile(fp, []byte("999999999"), 0o644); err != nil {
		t.Fatalf("failed to write stale lock: %v", err)
	}
	defer os.Remove(fp)

	pid, running := ReadLockStatus(repoPath)
	if pid != 999999999 {
		t.Errorf("expected pid=999999999, got %d", pid)
	}
	if running {
		t.Error("expected running=false for stale lock with dead PID")
	}
}

func TestReadLockStatus_CorruptLock(t *testing.T) {
	tmpDir := t.TempDir()
	repoPath := filepath.Join(tmpDir, "corrupt-repo")

	fp := LockFilePath(repoPath)
	if err := os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
		t.Fatalf("failed to create lock dir: %v", err)
	}
	if err := os.WriteFile(fp, []byte("not-a-pid"), 0o644); err != nil {
		t.Fatalf("failed to write corrupt lock: %v", err)
	}
	defer os.Remove(fp)

	pid, running := ReadLockStatus(repoPath)
	if pid != 0 {
		t.Errorf("expected pid=0 for corrupt lock, got %d", pid)
	}
	if running {
		t.Error("expected running=false for corrupt lock")
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

func TestDaemonLock_UpdatePID(t *testing.T) {
	tmpDir := t.TempDir()
	repoPath := filepath.Join(tmpDir, "test-repo-updatepid")

	lock, err := AcquireLock(repoPath)
	if err != nil {
		t.Fatalf("failed to acquire lock: %v", err)
	}
	defer lock.Release()

	newPID := 99999
	if err := lock.UpdatePID(newPID); err != nil {
		t.Fatalf("UpdatePID failed: %v", err)
	}

	// Verify lock file contains new PID
	data, err := os.ReadFile(lock.path)
	if err != nil {
		t.Fatalf("failed to read lock file: %v", err)
	}
	if strings.TrimSpace(string(data)) != fmt.Sprintf("%d", newPID) {
		t.Errorf("expected PID %d in lock file, got %s", newPID, string(data))
	}
}

func TestDaemonLock_Detach(t *testing.T) {
	tmpDir := t.TempDir()
	repoPath := filepath.Join(tmpDir, "test-repo-detach")

	lock, err := AcquireLock(repoPath)
	if err != nil {
		t.Fatalf("failed to acquire lock: %v", err)
	}

	lock.Detach()

	// Lock file should still exist after Detach
	if _, err := os.Stat(lock.path); os.IsNotExist(err) {
		t.Error("lock file should still exist after Detach")
	}

	// Clean up manually since Release won't work after Detach
	os.Remove(lock.path)
}

func TestClearLockForRepo(t *testing.T) {
	tmpDir := t.TempDir()
	repoPath := filepath.Join(tmpDir, "test-repo-clear")

	lock, err := AcquireLock(repoPath)
	if err != nil {
		t.Fatalf("failed to acquire lock: %v", err)
	}
	lock.file.Close() // Close handle so we can remove

	if err := ClearLockForRepo(repoPath); err != nil {
		t.Fatalf("ClearLockForRepo failed: %v", err)
	}

	// Lock file should be gone
	fp := LockFilePath(repoPath)
	if _, err := os.Stat(fp); !os.IsNotExist(err) {
		t.Error("lock file should be removed after ClearLockForRepo")
	}
}

func TestClearLockForRepo_NoFile(t *testing.T) {
	// Should not error when no lock file exists
	if err := ClearLockForRepo("/nonexistent/repo/path"); err != nil {
		t.Fatalf("ClearLockForRepo should not error on missing file: %v", err)
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
