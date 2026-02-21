package daemonstate

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
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
// Returns an error if the lock is already held.
func AcquireLock(repoPath string) (*DaemonLock, error) {
	fp := LockFilePath(repoPath)

	dir := filepath.Dir(fp)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create lock directory: %w", err)
	}

	// Try to create the lock file exclusively
	f, err := os.OpenFile(fp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			// Check if the lock file is stale (process that created it is gone)
			data, readErr := os.ReadFile(fp)
			if readErr == nil {
				pidStr := strings.TrimSpace(string(data))
				if pid, parseErr := strconv.Atoi(pidStr); parseErr == nil {
					if !processAlive(pid) {
						// Stale lock — owning process is dead. Remove and retry.
						os.Remove(fp)
						return AcquireLock(repoPath)
					}
				}
				return nil, fmt.Errorf("daemon lock already held (PID: %s). Remove %s if the process is not running", pidStr, fp)
			}
			return nil, fmt.Errorf("daemon lock already held at %s", fp)
		}
		return nil, fmt.Errorf("failed to create lock file: %w", err)
	}

	// Write our PID
	fmt.Fprintf(f, "%d", os.Getpid())

	return &DaemonLock{path: fp, file: f}, nil
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
// Uses signal 0 which checks for process existence without sending a signal.
func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil
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
