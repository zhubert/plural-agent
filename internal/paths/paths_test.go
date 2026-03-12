package paths

import (
	"os"
	"path/filepath"
	"testing"
)

// setupTestHome creates a temp directory, sets HOME to it, and resets the path cache.
// Returns the temp home path and a cleanup function.
func setupTestHome(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	Reset()
	t.Cleanup(Reset)
	return tmpDir
}

func TestFreshInstallNoXDG(t *testing.T) {
	home := setupTestHome(t)
	// No ~/.erg/, no XDG vars → default to ~/.erg/
	expected := filepath.Join(home, ".erg")

	configDir, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir: %v", err)
	}
	if configDir != expected {
		t.Errorf("ConfigDir = %q, want %q", configDir, expected)
	}

	dataDir, err := DataDir()
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}
	if dataDir != expected {
		t.Errorf("DataDir = %q, want %q", dataDir, expected)
	}

	stateDir, err := StateDir()
	if err != nil {
		t.Fatalf("StateDir: %v", err)
	}
	if stateDir != expected {
		t.Errorf("StateDir = %q, want %q", stateDir, expected)
	}

	if !IsLegacyLayout() {
		t.Error("IsLegacyLayout should be true for fresh install without XDG")
	}
}

func TestLegacyDirExists(t *testing.T) {
	home := setupTestHome(t)
	legacyDir := filepath.Join(home, ".erg")
	if err := os.MkdirAll(legacyDir, 0755); err != nil {
		t.Fatal(err)
	}

	configDir, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir: %v", err)
	}
	if configDir != legacyDir {
		t.Errorf("ConfigDir = %q, want %q", configDir, legacyDir)
	}

	dataDir, err := DataDir()
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}
	if dataDir != legacyDir {
		t.Errorf("DataDir = %q, want %q", dataDir, legacyDir)
	}

	stateDir, err := StateDir()
	if err != nil {
		t.Fatalf("StateDir: %v", err)
	}
	if stateDir != legacyDir {
		t.Errorf("StateDir = %q, want %q", stateDir, legacyDir)
	}

	if !IsLegacyLayout() {
		t.Error("IsLegacyLayout should be true when ~/.erg/ exists")
	}
}

func TestLegacyTakesPrecedenceOverXDG(t *testing.T) {
	home := setupTestHome(t)
	legacyDir := filepath.Join(home, ".erg")
	if err := os.MkdirAll(legacyDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Set XDG vars — legacy should still win
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	Reset()

	configDir, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir: %v", err)
	}
	if configDir != legacyDir {
		t.Errorf("ConfigDir = %q, want %q (legacy should take precedence)", configDir, legacyDir)
	}

	if !IsLegacyLayout() {
		t.Error("IsLegacyLayout should be true when ~/.erg/ exists, even with XDG vars")
	}
}

func TestXDGAllVarsSet(t *testing.T) {
	home := setupTestHome(t)
	// No ~/.erg/ exists

	xdgConfig := filepath.Join(home, "my-config")
	xdgData := filepath.Join(home, "my-data")
	xdgState := filepath.Join(home, "my-state")

	t.Setenv("XDG_CONFIG_HOME", xdgConfig)
	t.Setenv("XDG_DATA_HOME", xdgData)
	t.Setenv("XDG_STATE_HOME", xdgState)
	Reset()

	configDir, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir: %v", err)
	}
	if want := filepath.Join(xdgConfig, "erg"); configDir != want {
		t.Errorf("ConfigDir = %q, want %q", configDir, want)
	}

	dataDir, err := DataDir()
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}
	if want := filepath.Join(xdgData, "erg"); dataDir != want {
		t.Errorf("DataDir = %q, want %q", dataDir, want)
	}

	stateDir, err := StateDir()
	if err != nil {
		t.Fatalf("StateDir: %v", err)
	}
	if want := filepath.Join(xdgState, "erg"); stateDir != want {
		t.Errorf("StateDir = %q, want %q", stateDir, want)
	}

	if IsLegacyLayout() {
		t.Error("IsLegacyLayout should be false when using XDG")
	}
}

func TestXDGPartialVars(t *testing.T) {
	home := setupTestHome(t)
	// No ~/.erg/ exists

	xdgConfig := filepath.Join(home, "my-config")
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)
	// XDG_DATA_HOME and XDG_STATE_HOME not set — should use XDG defaults
	Reset()

	configDir, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir: %v", err)
	}
	if want := filepath.Join(xdgConfig, "erg"); configDir != want {
		t.Errorf("ConfigDir = %q, want %q", configDir, want)
	}

	dataDir, err := DataDir()
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}
	if want := filepath.Join(home, ".local", "share", "erg"); dataDir != want {
		t.Errorf("DataDir = %q, want %q", dataDir, want)
	}

	stateDir, err := StateDir()
	if err != nil {
		t.Fatalf("StateDir: %v", err)
	}
	if want := filepath.Join(home, ".local", "state", "erg"); stateDir != want {
		t.Errorf("StateDir = %q, want %q", stateDir, want)
	}

	if IsLegacyLayout() {
		t.Error("IsLegacyLayout should be false when using XDG")
	}
}

func TestDerivedPaths(t *testing.T) {
	home := setupTestHome(t)
	legacyDir := filepath.Join(home, ".erg")
	if err := os.MkdirAll(legacyDir, 0755); err != nil {
		t.Fatal(err)
	}

	t.Run("legacy layout", func(t *testing.T) {
		Reset()
		cfgPath, err := ConfigFilePath()
		if err != nil {
			t.Fatalf("ConfigFilePath: %v", err)
		}
		if want := filepath.Join(legacyDir, "config.json"); cfgPath != want {
			t.Errorf("ConfigFilePath = %q, want %q", cfgPath, want)
		}

		sessDir, err := SessionsDir()
		if err != nil {
			t.Fatalf("SessionsDir: %v", err)
		}
		if want := filepath.Join(legacyDir, "sessions"); sessDir != want {
			t.Errorf("SessionsDir = %q, want %q", sessDir, want)
		}

		logsDir, err := LogsDir()
		if err != nil {
			t.Fatalf("LogsDir: %v", err)
		}
		if want := filepath.Join(legacyDir, "logs"); logsDir != want {
			t.Errorf("LogsDir = %q, want %q", logsDir, want)
		}

		sockDir, err := SocketsDir()
		if err != nil {
			t.Fatalf("SocketsDir: %v", err)
		}
		if want := filepath.Join(legacyDir, "sockets"); sockDir != want {
			t.Errorf("SocketsDir = %q, want %q", sockDir, want)
		}
	})

	t.Run("XDG layout", func(t *testing.T) {
		// Remove legacy dir so XDG kicks in
		os.RemoveAll(legacyDir)
		xdgConfig := filepath.Join(home, ".config")
		xdgData := filepath.Join(home, ".local", "share")
		xdgState := filepath.Join(home, ".local", "state")
		t.Setenv("XDG_CONFIG_HOME", xdgConfig)
		t.Setenv("XDG_DATA_HOME", xdgData)
		t.Setenv("XDG_STATE_HOME", xdgState)
		Reset()

		cfgPath, err := ConfigFilePath()
		if err != nil {
			t.Fatalf("ConfigFilePath: %v", err)
		}
		if want := filepath.Join(xdgConfig, "erg", "config.json"); cfgPath != want {
			t.Errorf("ConfigFilePath = %q, want %q", cfgPath, want)
		}

		sessDir, err := SessionsDir()
		if err != nil {
			t.Fatalf("SessionsDir: %v", err)
		}
		if want := filepath.Join(xdgData, "erg", "sessions"); sessDir != want {
			t.Errorf("SessionsDir = %q, want %q", sessDir, want)
		}

		logsDir, err := LogsDir()
		if err != nil {
			t.Fatalf("LogsDir: %v", err)
		}
		if want := filepath.Join(xdgState, "erg", "logs"); logsDir != want {
			t.Errorf("LogsDir = %q, want %q", logsDir, want)
		}

		sockDir, err := SocketsDir()
		if err != nil {
			t.Fatalf("SocketsDir: %v", err)
		}
		if want := filepath.Join(xdgState, "erg", "sockets"); sockDir != want {
			t.Errorf("SocketsDir = %q, want %q", sockDir, want)
		}
	})
}

func TestResetClearsCache(t *testing.T) {
	home := setupTestHome(t)

	// First resolve: no legacy, no XDG → defaults to ~/.erg/
	dir1, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir: %v", err)
	}
	expectedLegacy := filepath.Join(home, ".erg")
	if dir1 != expectedLegacy {
		t.Errorf("ConfigDir = %q, want %q", dir1, expectedLegacy)
	}

	// Now set XDG and reset
	xdgConfig := filepath.Join(home, "new-config")
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)
	Reset()

	dir2, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir after reset: %v", err)
	}
	expectedXDG := filepath.Join(xdgConfig, "erg")
	if dir2 != expectedXDG {
		t.Errorf("ConfigDir after reset = %q, want %q", dir2, expectedXDG)
	}
}

func TestManifestPath(t *testing.T) {
	home := setupTestHome(t)
	legacyDir := filepath.Join(home, ".erg")
	os.MkdirAll(legacyDir, 0o755)
	Reset()

	p, err := ManifestPath()
	if err != nil {
		t.Fatalf("ManifestPath: %v", err)
	}
	if want := filepath.Join(legacyDir, "daemon.yaml"); p != want {
		t.Errorf("ManifestPath = %q, want %q", p, want)
	}
}

func TestManifestPath_XDG(t *testing.T) {
	home := setupTestHome(t)
	xdgConfig := filepath.Join(home, ".config")
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)
	Reset()

	p, err := ManifestPath()
	if err != nil {
		t.Fatalf("ManifestPath: %v", err)
	}
	if want := filepath.Join(xdgConfig, "erg", "daemon.yaml"); p != want {
		t.Errorf("ManifestPath = %q, want %q", p, want)
	}
}

func TestClaudeConfigDirDefault(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	got, err := ClaudeConfigDir()
	if err != nil {
		t.Fatalf("ClaudeConfigDir: %v", err)
	}
	if want := filepath.Join(home, ".claude"); got != want {
		t.Errorf("ClaudeConfigDir = %q, want %q", got, want)
	}
}

func TestClaudeConfigDirEnvVar(t *testing.T) {
	setupTestHome(t)
	customDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", customDir)

	got, err := ClaudeConfigDir()
	if err != nil {
		t.Fatalf("ClaudeConfigDir: %v", err)
	}

	// The result should be an absolute path equal to customDir
	absCustomDir, err := filepath.Abs(customDir)
	if err != nil {
		t.Fatalf("filepath.Abs(%q): %v", customDir, err)
	}
	if got != absCustomDir {
		t.Errorf("ClaudeConfigDir = %q, want %q", got, absCustomDir)
	}
}

func TestClaudeConfigDirEnvVarRelative(t *testing.T) {
	setupTestHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "relative/path")

	got, err := ClaudeConfigDir()
	if err != nil {
		t.Fatalf("ClaudeConfigDir: %v", err)
	}

	// Should be converted to an absolute path
	if !filepath.IsAbs(got) {
		t.Errorf("ClaudeConfigDir = %q, want an absolute path", got)
	}
}

func TestClaudeConfigDirEnvVarTilde(t *testing.T) {
	home := setupTestHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "~/.claude-alt")

	got, err := ClaudeConfigDir()
	if err != nil {
		t.Fatalf("ClaudeConfigDir: %v", err)
	}

	want := filepath.Join(home, ".claude-alt")
	if got != want {
		t.Errorf("ClaudeConfigDir = %q, want %q (tilde should expand to home dir)", got, want)
	}
}

func TestLegacyFileNotDir(t *testing.T) {
	home := setupTestHome(t)
	// Create ~/.erg as a file, not a directory — should NOT be treated as legacy
	legacyPath := filepath.Join(home, ".erg")
	if err := os.WriteFile(legacyPath, []byte("not a dir"), 0644); err != nil {
		t.Fatal(err)
	}

	xdgConfig := filepath.Join(home, ".config")
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)
	Reset()

	configDir, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir: %v", err)
	}
	if want := filepath.Join(xdgConfig, "erg"); configDir != want {
		t.Errorf("ConfigDir = %q, want %q (file named .erg should not trigger legacy)", configDir, want)
	}
}
