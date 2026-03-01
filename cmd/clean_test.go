package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhubert/erg/internal/logger"
	"github.com/zhubert/erg/internal/paths"
)

// setupAgentCleanTest sets up isolated temp dirs for paths resolution.
// HOME is set to a temp dir (no ~/.erg/) so XDG vars are respected.
func setupAgentCleanTest(t *testing.T) (dataDir, stateDir string) {
	t.Helper()
	tmpDir := t.TempDir()

	// Set HOME to tmpDir so ~/.erg doesn't exist → XDG vars take effect
	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmpDir, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))
	paths.Reset()
	t.Cleanup(func() { paths.Reset() })

	dataDir = filepath.Join(tmpDir, "data", "erg")
	stateDir = filepath.Join(tmpDir, "state", "erg")
	os.MkdirAll(dataDir, 0o755)
	os.MkdirAll(stateDir, 0o755)
	return dataDir, stateDir
}

func TestRunAgentClean_NothingToClean(t *testing.T) {
	setupAgentCleanTest(t)

	agentCleanSkipConfirm = true
	defer func() { agentCleanSkipConfirm = false }()

	err := runAgentCleanWithReader(strings.NewReader(""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunAgentClean_RemovesStateAndLocks(t *testing.T) {
	dataDir, stateDir := setupAgentCleanTest(t)

	// Create state file
	stateFile := filepath.Join(dataDir, "daemon-state.json")
	if err := os.WriteFile(stateFile, []byte(`{"version":1}`), 0o644); err != nil {
		t.Fatalf("failed to create state file: %v", err)
	}

	// Create lock files
	for _, name := range []string{"daemon-abc123.lock", "daemon-def456.lock"} {
		if err := os.WriteFile(filepath.Join(stateDir, name), []byte("12345"), 0o644); err != nil {
			t.Fatalf("failed to create lock file: %v", err)
		}
	}

	agentCleanSkipConfirm = true
	defer func() { agentCleanSkipConfirm = false }()

	err := runAgentCleanWithReader(strings.NewReader(""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify state file removed
	if _, err := os.Stat(stateFile); !os.IsNotExist(err) {
		t.Error("expected state file to be removed")
	}

	// Verify lock files removed
	locks, _ := filepath.Glob(filepath.Join(stateDir, "daemon-*.lock"))
	if len(locks) != 0 {
		t.Errorf("expected 0 lock files, got %d", len(locks))
	}
}

func TestRunAgentClean_AbortOnNo(t *testing.T) {
	dataDir, _ := setupAgentCleanTest(t)

	// Create state file
	stateFile := filepath.Join(dataDir, "daemon-state.json")
	if err := os.WriteFile(stateFile, []byte(`{"version":1}`), 0o644); err != nil {
		t.Fatalf("failed to create state file: %v", err)
	}

	agentCleanSkipConfirm = false

	err := runAgentCleanWithReader(strings.NewReader("n\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// State file should still exist
	if _, err := os.Stat(stateFile); err != nil {
		t.Error("expected state file to still exist after abort")
	}
}

func TestRunAgentClean_ConfirmYes(t *testing.T) {
	dataDir, _ := setupAgentCleanTest(t)

	// Create state file
	stateFile := filepath.Join(dataDir, "daemon-state.json")
	if err := os.WriteFile(stateFile, []byte(`{"version":1}`), 0o644); err != nil {
		t.Fatalf("failed to create state file: %v", err)
	}

	agentCleanSkipConfirm = false

	err := runAgentCleanWithReader(strings.NewReader("y\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// State file should be removed
	if _, err := os.Stat(stateFile); !os.IsNotExist(err) {
		t.Error("expected state file to be removed after confirmation")
	}
}

func TestRunAgentClean_RemovesWorktrees(t *testing.T) {
	dataDir, _ := setupAgentCleanTest(t)

	// Create worktree directories (simulating leftover session worktrees)
	wtDir := filepath.Join(dataDir, "worktrees")
	for _, name := range []string{"session-abc", "session-def"} {
		if err := os.MkdirAll(filepath.Join(wtDir, name), 0o755); err != nil {
			t.Fatalf("failed to create worktree dir: %v", err)
		}
	}

	agentCleanSkipConfirm = true
	defer func() { agentCleanSkipConfirm = false }()

	err := runAgentCleanWithReader(strings.NewReader(""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify worktrees directory removed
	if _, err := os.Stat(wtDir); !os.IsNotExist(err) {
		t.Error("expected worktrees directory to be removed")
	}
}

func TestRunAgentClean_NothingToClean_NoWorktrees(t *testing.T) {
	setupAgentCleanTest(t)

	// No state, no locks, no worktrees — should report nothing to clean
	agentCleanSkipConfirm = true
	defer func() { agentCleanSkipConfirm = false }()

	err := runAgentCleanWithReader(strings.NewReader(""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunAgentClean_OnlyWorktrees(t *testing.T) {
	dataDir, _ := setupAgentCleanTest(t)

	// Only create worktrees, no state file or lock files
	wtDir := filepath.Join(dataDir, "worktrees")
	if err := os.MkdirAll(filepath.Join(wtDir, "session-xyz"), 0o755); err != nil {
		t.Fatalf("failed to create worktree dir: %v", err)
	}

	agentCleanSkipConfirm = true
	defer func() { agentCleanSkipConfirm = false }()

	err := runAgentCleanWithReader(strings.NewReader(""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Worktrees should be removed
	if _, err := os.Stat(wtDir); !os.IsNotExist(err) {
		t.Error("expected worktrees directory to be removed")
	}
}

func TestRunAgentClean_OnlyLocks(t *testing.T) {
	_, stateDir := setupAgentCleanTest(t)

	// Only create a lock file, no state file
	if err := os.WriteFile(filepath.Join(stateDir, "daemon-abc.lock"), []byte("99"), 0o644); err != nil {
		t.Fatalf("failed to create lock file: %v", err)
	}

	agentCleanSkipConfirm = true
	defer func() { agentCleanSkipConfirm = false }()

	err := runAgentCleanWithReader(strings.NewReader(""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Lock file should be removed
	locks, _ := filepath.Glob(filepath.Join(stateDir, "daemon-*.lock"))
	if len(locks) != 0 {
		t.Errorf("expected 0 lock files, got %d", len(locks))
	}
}

func TestRunAgentClean_RemovesAuthFilesAndLogs(t *testing.T) {
	dataDir, stateDir := setupAgentCleanTest(t)

	// Reset logger so it picks up the new paths
	logger.Reset()
	t.Cleanup(func() { logger.Reset() })

	// dataDir is tmpDir/data/erg, so go up two levels to get tmpDir
	tmpDir := filepath.Dir(filepath.Dir(dataDir))
	configDir := filepath.Join(tmpDir, "config", "erg")
	os.MkdirAll(configDir, 0o755)

	// Create auth files in config dir
	for _, name := range []string{"erg-auth-sess1", "erg-auth-sess2", "erg-auth-sess3"} {
		if err := os.WriteFile(filepath.Join(configDir, name), []byte("KEY=val"), 0o600); err != nil {
			t.Fatalf("failed to create auth file: %v", err)
		}
	}

	// Create MCP config files in config dir
	for _, name := range []string{"erg-mcp-sess1.json", "erg-mcp-sess2.json"} {
		if err := os.WriteFile(filepath.Join(configDir, name), []byte("{}"), 0o600); err != nil {
			t.Fatalf("failed to create MCP config file: %v", err)
		}
	}

	// Create log files in state dir (logs subdir)
	logsDir := filepath.Join(stateDir, "logs")
	os.MkdirAll(logsDir, 0o755)
	for _, name := range []string{"erg.log", "mcp-aaa.log", "mcp-bbb.log", "stream-ccc.log"} {
		if err := os.WriteFile(filepath.Join(logsDir, name), []byte("log data"), 0o644); err != nil {
			t.Fatalf("failed to create log file: %v", err)
		}
	}

	agentCleanSkipConfirm = true
	defer func() { agentCleanSkipConfirm = false }()

	err := runAgentCleanWithReader(strings.NewReader(""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify auth files removed
	authFiles, _ := filepath.Glob(filepath.Join(configDir, "erg-auth-*"))
	if len(authFiles) != 0 {
		t.Errorf("expected 0 auth files, got %d", len(authFiles))
	}

	// Verify MCP config files removed
	mcpFiles, _ := filepath.Glob(filepath.Join(configDir, "erg-mcp-*.json"))
	if len(mcpFiles) != 0 {
		t.Errorf("expected 0 MCP config files, got %d", len(mcpFiles))
	}

	// Verify log files removed
	logFiles, _ := filepath.Glob(filepath.Join(logsDir, "*.log"))
	if len(logFiles) != 0 {
		t.Errorf("expected 0 log files, got %d", len(logFiles))
	}
}
