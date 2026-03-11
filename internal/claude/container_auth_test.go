package claude

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zhubert/erg/internal/paths"
)

// setupAuthTest sets up isolated temp dirs so auth functions use temp state dir.
func setupAuthTest(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()

	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmpDir, "state"))
	paths.Reset()
	t.Cleanup(func() { paths.Reset() })

	stateDir := filepath.Join(tmpDir, "state", "erg")
	os.MkdirAll(stateDir, 0o755)
	return stateDir
}

func TestFindAuthFiles_Empty(t *testing.T) {
	setupAuthTest(t)

	files, err := FindAuthFiles()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected 0 auth files, got %d", len(files))
	}
}

func TestFindAuthFiles_ReturnsMatches(t *testing.T) {
	configDir := setupAuthTest(t)

	// Create auth files
	for _, name := range []string{"erg-auth-abc123", "erg-auth-def456", "erg-auth-ghi789"} {
		if err := os.WriteFile(filepath.Join(configDir, name), []byte("KEY=val"), 0o600); err != nil {
			t.Fatalf("failed to create auth file: %v", err)
		}
	}
	// Create a non-auth file that should not match
	os.WriteFile(filepath.Join(configDir, "config.json"), []byte("{}"), 0o644)

	files, err := FindAuthFiles()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 3 {
		t.Errorf("expected 3 auth files, got %d", len(files))
	}
}

func TestClearAuthFiles_RemovesAll(t *testing.T) {
	configDir := setupAuthTest(t)

	// Create auth files
	for _, name := range []string{"erg-auth-aaa", "erg-auth-bbb"} {
		if err := os.WriteFile(filepath.Join(configDir, name), []byte("KEY=val"), 0o600); err != nil {
			t.Fatalf("failed to create auth file: %v", err)
		}
	}
	// Create a non-auth file that should survive
	otherFile := filepath.Join(configDir, "config.json")
	os.WriteFile(otherFile, []byte("{}"), 0o644)

	count, err := ClearAuthFiles()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 files removed, got %d", count)
	}

	// Verify auth files are gone
	remaining, _ := FindAuthFiles()
	if len(remaining) != 0 {
		t.Errorf("expected 0 remaining auth files, got %d", len(remaining))
	}

	// Verify non-auth file still exists
	if _, err := os.Stat(otherFile); err != nil {
		t.Error("expected config.json to still exist")
	}
}

func TestClearAuthFiles_NothingToRemove(t *testing.T) {
	setupAuthTest(t)

	count, err := ClearAuthFiles()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 files removed, got %d", count)
	}
}
