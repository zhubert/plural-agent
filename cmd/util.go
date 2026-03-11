package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/zhubert/erg/internal/config"
	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/paths"
)

// lookPathFunc is the function used to look up binaries on PATH.
// Overridden in tests to control behavior.
var lookPathFunc = exec.LookPath

// hasContainerRuntime checks whether a container runtime binary (docker or colima)
// is available on PATH. Returns true if either is found.
func hasContainerRuntime() bool {
	if _, err := lookPathFunc("docker"); err == nil {
		return true
	}
	if _, err := lookPathFunc("colima"); err == nil {
		return true
	}
	return false
}

// checkDockerDaemon verifies a container runtime daemon is reachable, not just
// that the binary exists. This catches the case where a Docker-compatible container
// runtime is installed but not running, which would otherwise cause silent per-session failures.
// Works with OrbStack, Docker Desktop, and Colima since all expose a Docker-compatible API.
func checkDockerDaemon() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "docker", "info").Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			hint := runtimeStartHint()
			return fmt.Errorf("docker CLI not found on PATH — install a container runtime that provides it%s", hint)
		}
		hint := runtimeStartHint()
		return fmt.Errorf("container runtime is not reachable (is OrbStack, Docker Desktop, or Colima running?)%s", hint)
	}
	return nil
}

// runtimeStartHint returns a help message suggesting how to start a container runtime.
// It checks whether colima is installed to tailor the suggestion.
func runtimeStartHint() string {
	if _, err := lookPathFunc("colima"); err == nil {
		return "\n\nStart with: colima start"
	}
	if runtime.GOOS == "darwin" {
		return "\n\nInstall a container runtime:\n  OrbStack (macOS, recommended on macOS): https://orbstack.dev\n  Docker Desktop:                         https://docs.docker.com/get-docker/\n  Colima:                                 https://github.com/abiosoft/colima"
	}
	return "\n\nInstall a container runtime:\n  Docker Desktop: https://docs.docker.com/get-docker/\n  Colima:         https://github.com/abiosoft/colima"
}

// findSingleRunningDaemon scans lock files to find a running daemon when
// no --repo flag was provided and we're not inside a git repo.
// Returns the repo/daemon key if exactly one is running, or an error.
func findSingleRunningDaemon() (string, error) {
	locks, err := daemonstate.FindLocks()
	if err != nil || len(locks) == 0 {
		return "", fmt.Errorf("no running orchestrator found\n\nStart one with 'erg start' or specify --repo")
	}

	var running []string
	for _, lockPath := range locks {
		data, err := readLockFileKey(lockPath)
		if err != nil {
			continue
		}
		if _, alive := daemonstate.ReadLockStatus(data); alive {
			running = append(running, data)
		}
	}

	switch len(running) {
	case 0:
		return "", fmt.Errorf("no running orchestrator found\n\nStart one with 'erg start' or specify --repo")
	case 1:
		return running[0], nil
	default:
		return "", fmt.Errorf("multiple orchestrators running — specify --repo:\n  %s", strings.Join(running, "\n  "))
	}
}

// readLockFileKey reads the lock file and returns the repo key it corresponds to.
// Lock files don't store the repo key directly, so we use state files to reverse-map.
// For now, we scan state files to find which repo key each lock file corresponds to.
var readLockFileKey = readLockFileKeyDefault

func readLockFileKeyDefault(lockPath string) (string, error) {
	// Lock files are named daemon-<hash>.lock. State files are daemon-state-<hash>.json.
	// Extract the hash from the lock file name and find the matching state file.
	// The state file contains the repo_path.
	base := strings.TrimSuffix(filepath.Base(lockPath), ".lock")
	hash := strings.TrimPrefix(base, "daemon-")
	if hash == "" {
		return "", fmt.Errorf("unexpected lock file name: %s", lockPath)
	}

	// Look for the corresponding state file
	dir := filepath.Dir(lockPath)
	stateDir, err := paths.DataDir()
	if err != nil {
		stateDir = dir
	}

	statePath := filepath.Join(stateDir, fmt.Sprintf("daemon-state-%s.json", hash))
	state, err := loadStateRepoPath(statePath)
	if err != nil {
		// If no state file, the hash itself is the best we have
		return hash, nil
	}
	return state, nil
}

// loadStateRepoPath reads just the repo_path from a daemon state file.
func loadStateRepoPath(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	// Quick extract of repo_path without full JSON unmarshal
	var partial struct {
		RepoPath string `json:"repo_path"`
	}
	if err := json.Unmarshal(data, &partial); err != nil {
		return "", err
	}
	if partial.RepoPath == "" {
		return "", fmt.Errorf("no repo_path in state file")
	}
	return partial.RepoPath, nil
}

// issueLabel formats an issue reference into a display label, truncated to maxWidth runes.
// GitHub issues render as "#42 Title"; others as "ID Title"; no-source as the item ID.
func issueLabel(ref config.IssueRef, itemID string, maxWidth int) string {
	var s string
	switch ref.Source {
	case "github", "GitHub":
		s = fmt.Sprintf("#%s %s", ref.ID, ref.Title)
	case "":
		s = itemID
	default:
		s = fmt.Sprintf("%s %s", ref.ID, ref.Title)
	}
	runes := []rune(s)
	if len(runes) > maxWidth {
		s = string(runes[:maxWidth-3]) + "..."
	}
	return s
}

// confirm prompts the user for y/n confirmation
func confirm(input io.Reader, prompt string) bool {
	reader := bufio.NewReader(input)
	fmt.Printf("%s [y/N]: ", prompt)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	response = strings.ToLower(strings.TrimSpace(response))
	return response == "y" || response == "yes"
}
