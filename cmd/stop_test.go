package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestStopCmdRepoFlagExists(t *testing.T) {
	flag := stopCmd.Flags().Lookup("repo")
	if flag == nil {
		t.Fatal("expected --repo flag on stop command")
	}
}

func TestStopCmdRegisteredOnRoot(t *testing.T) {
	found := false
	for _, sub := range rootCmd.Commands() {
		if sub.Use == "stop" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'stop' subcommand to be registered on rootCmd")
	}
}

// TestRunStop_NoDaemonRunning verifies that runStop prints "not running" and
// returns no error when no lock file exists for the repo.
func TestRunStop_NoDaemonRunning(t *testing.T) {
	origRepo := stopRepo
	origFindPIDs := findDaemonPIDsFunc
	defer func() {
		stopRepo = origRepo
		findDaemonPIDsFunc = origFindPIDs
	}()

	// Use a path that will never have a lock file
	stopRepo = filepath.Join(t.TempDir(), "no-daemon-repo")
	findDaemonPIDsFunc = func(repo string) []int { return nil }

	out := captureStdoutStop(t, func() {
		if err := runStop(&cobra.Command{}, nil); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	if out == "" {
		t.Error("expected output when daemon is not running")
	}
}

// TestRunStop_ExplicitRepoNoDaemon verifies runStop works with an explicit
// --repo path that has no running daemon.
func TestRunStop_ExplicitRepoNoDaemon(t *testing.T) {
	origRepo := stopRepo
	origFindPIDs := findDaemonPIDsFunc
	defer func() {
		stopRepo = origRepo
		findDaemonPIDsFunc = origFindPIDs
	}()

	stopRepo = "/nonexistent/path/to/repo/with/no/daemon"
	findDaemonPIDsFunc = func(repo string) []int { return nil }

	out := captureStdoutStop(t, func() {
		if err := runStop(&cobra.Command{}, nil); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	if out == "" {
		t.Error("expected 'Daemon is not running' output")
	}
}

func TestRunStop_StaleProcess_FallsBackToPgrep(t *testing.T) {
	origRepo := stopRepo
	origFindPIDs := findDaemonPIDsFunc
	defer func() {
		stopRepo = origRepo
		findDaemonPIDsFunc = origFindPIDs
	}()

	// Use a path with no lock file
	stopRepo = filepath.Join(t.TempDir(), "orphan-repo")

	// Mock pgrep to return no PIDs (no orphans found)
	findDaemonPIDsFunc = func(repo string) []int {
		return []int{}
	}

	out := captureStdoutStop(t, func() {
		err := runStop(&cobra.Command{}, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	if !strings.Contains(out, "not running") {
		t.Errorf("expected 'not running' output, got: %s", out)
	}
}

func TestRunStop_OrphanedProcessFound(t *testing.T) {
	origRepo := stopRepo
	origFindPIDs := findDaemonPIDsFunc
	defer func() {
		stopRepo = origRepo
		findDaemonPIDsFunc = origFindPIDs
	}()

	stopRepo = filepath.Join(t.TempDir(), "orphan-repo-2")

	// Mock pgrep to find an orphaned process with a PID that doesn't exist.
	// signalDaemon will return an error but runStop handles it gracefully
	// with a warning message.
	findDaemonPIDsFunc = func(repo string) []int {
		return []int{999999999}
	}

	out := captureStdoutStop(t, func() {
		err := runStop(&cobra.Command{}, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	if !strings.Contains(out, "orphaned daemon process") {
		t.Errorf("expected orphaned process message, got: %s", out)
	}
}

// captureStdoutStop captures os.Stdout output during f().
func captureStdoutStop(t *testing.T, f func()) string {
	t.Helper()
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	os.Stdout = w

	f()

	w.Close()
	os.Stdout = origStdout

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	return string(buf[:n])
}
