package daemonstate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/zhubert/erg/internal/paths"
)

// RunningDaemon holds the key and PID of a running daemon.
type RunningDaemon struct {
	Key string // repo path or multi-repo DaemonID
	PID int
}

// DiscoverRunning finds all running daemons by scanning lock files.
// Returns the repo key (or DaemonID) and PID for each living daemon.
func DiscoverRunning() ([]RunningDaemon, error) {
	locks, err := FindLocks()
	if err != nil {
		return nil, err
	}

	var result []RunningDaemon
	for _, lockPath := range locks {
		key, err := RepoKeyFromLock(lockPath)
		if err != nil {
			continue
		}
		// Read the lock file directly instead of going through
		// ReadLockStatus, which would re-hash the key and miss
		// daemons whose state file couldn't be read.
		pid, alive := readLockFile(lockPath)
		if alive {
			result = append(result, RunningDaemon{Key: key, PID: pid})
		}
	}
	return result, nil
}

// RepoKeyFromLock extracts the repo key from a lock file path by finding
// the corresponding state file and reading its repo_path field.
func RepoKeyFromLock(lockPath string) (string, error) {
	base := strings.TrimSuffix(filepath.Base(lockPath), ".lock")
	hash := strings.TrimPrefix(base, "daemon-")
	if hash == "" {
		return "", fmt.Errorf("unexpected lock file name: %s", lockPath)
	}

	stateDir, err := paths.DataDir()
	if err != nil {
		stateDir = filepath.Dir(lockPath)
	}

	statePath := filepath.Join(stateDir, fmt.Sprintf("daemon-state-%s.json", hash))
	data, err := os.ReadFile(statePath)
	if err != nil {
		return hash, nil
	}

	var partial struct {
		RepoPath string `json:"repo_path"`
	}
	if err := json.Unmarshal(data, &partial); err != nil || partial.RepoPath == "" {
		return hash, nil
	}
	return partial.RepoPath, nil
}

// readLockFile reads a PID from a lock file path and checks if it's alive.
func readLockFile(lockPath string) (pid int, running bool) {
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return 0, false
	}
	p, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false
	}
	return p, processAlive(p)
}
