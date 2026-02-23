package daemonstate

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/zhubert/plural-core/paths"
)

// LockFilePath returns the path to the lock file for the given repo path.
func LockFilePath(repoPath string) string {
	dir, err := paths.StateDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".plural")
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(repoPath)))
	return filepath.Join(dir, fmt.Sprintf("daemon-%s.lock", hash[:12]))
}

// DaemonLock manages the lock file to prevent multiple daemons for the same repo.
type DaemonLock struct {
	path string
	file *os.File
}

// AcquireLock attempts to acquire the daemon lock for the given repo path.
// Returns an error if the lock is already held by a living process.
// Stale locks (where the owning process has died) are automatically cleaned up.
// Note: On Windows, stale lock detection is not supported (signal 0 is unavailable),
// so stale locks must be removed manually via "plural-agent clean".
func AcquireLock(repoPath string) (*DaemonLock, error) {
	fp := LockFilePath(repoPath)

	dir := filepath.Dir(fp)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create lock directory: %w", err)
	}

	// Try up to 2 times: once normally, once after stale lock cleanup.
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(fp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			// Successfully created lock file — write our PID
			fmt.Fprintf(f, "%d", os.Getpid())
			return &DaemonLock{path: fp, file: f}, nil
		}

		if !os.IsExist(err) {
			return nil, fmt.Errorf("failed to create lock file: %w", err)
		}

		// Lock file exists — check if it's stale
		if attempt > 0 {
			// Already tried stale cleanup once, don't loop again
			return nil, fmt.Errorf("daemon lock already held at %s (retry after stale cleanup failed)", fp)
		}

		data, readErr := os.ReadFile(fp)
		if readErr != nil {
			return nil, fmt.Errorf("daemon lock already held at %s", fp)
		}

		pidStr := strings.TrimSpace(string(data))
		pid, parseErr := strconv.Atoi(pidStr)
		if parseErr != nil {
			return nil, fmt.Errorf("daemon lock already held (corrupt PID in %s)", fp)
		}

		if processAlive(pid) {
			return nil, fmt.Errorf("daemon lock already held (PID: %s). Remove %s if the process is not running", pidStr, fp)
		}

		// Stale lock — owning process is dead. Remove and retry.
		if removeErr := os.Remove(fp); removeErr != nil {
			return nil, fmt.Errorf("stale daemon lock (dead PID %d) but failed to remove %s: %w", pid, fp, removeErr)
		}
		// Loop back to retry file creation
	}

	return nil, fmt.Errorf("daemon lock already held at %s", fp)
}

// Release releases the daemon lock.
func (l *DaemonLock) Release() error {
	if l.file != nil {
		l.file.Close()
	}
	return os.Remove(l.path)
}

// ClearLocks finds and removes all daemon lock files.
// Returns the number of lock files removed.
func ClearLocks() (int, error) {
	dir, err := paths.StateDir()
	if err != nil {
		return 0, fmt.Errorf("failed to resolve state dir: %w", err)
	}

	matches, err := filepath.Glob(filepath.Join(dir, "daemon-*.lock"))
	if err != nil {
		return 0, fmt.Errorf("failed to glob lock files: %w", err)
	}

	removed := 0
	for _, match := range matches {
		if err := os.Remove(match); err != nil && !os.IsNotExist(err) {
			return removed, fmt.Errorf("failed to remove lock file %s: %w", match, err)
		}
		removed++
	}
	return removed, nil
}

// processAlive returns true if a process with the given PID is running.
// On Unix, uses signal 0 which checks for process existence without sending a signal.
// On Windows, os.FindProcess always succeeds, so we conservatively assume the process is alive.
func processAlive(pid int) bool {
	if runtime.GOOS == "windows" {
		// os.FindProcess on Windows always returns a valid process handle;
		// signal 0 is not supported. Conservatively assume the process is alive
		// to avoid accidentally removing a valid lock.
		return true
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}

// ReadLockStatus reads the daemon lock file for the given repo and returns the
// PID written to it and whether that process is currently running.
// Returns pid=0, running=false if the lock file does not exist or cannot be parsed.
func ReadLockStatus(repoPath string) (pid int, running bool) {
	fp := LockFilePath(repoPath)
	data, err := os.ReadFile(fp)
	if err != nil {
		return 0, false
	}
	pidStr := strings.TrimSpace(string(data))
	p, err := strconv.Atoi(pidStr)
	if err != nil {
		return 0, false
	}
	return p, processAlive(p)
}

// FindLocks returns the paths of all daemon lock files.
func FindLocks() ([]string, error) {
	dir, err := paths.StateDir()
	if err != nil {
		return nil, fmt.Errorf("failed to resolve state dir: %w", err)
	}

	matches, err := filepath.Glob(filepath.Join(dir, "daemon-*.lock"))
	if err != nil {
		return nil, fmt.Errorf("failed to glob lock files: %w", err)
	}
	return matches, nil
}
