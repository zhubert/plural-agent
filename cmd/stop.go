package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/session"
)

var stopRepo string

// findDaemonPIDsFunc is injectable for testing.
var findDaemonPIDsFunc = findDaemonPIDs

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the daemon gracefully",
	Long: `Send SIGTERM to the running daemon to trigger a graceful shutdown.

The daemon will finish in-flight work before exiting.

Examples:
  erg stop                       # Stop daemon for current repo
  erg stop --repo owner/repo     # Stop daemon for specific repo`,
	RunE: runStop,
}

func init() {
	stopCmd.Flags().StringVar(&stopRepo, "repo", "", "Repo whose daemon to stop (owner/repo or filesystem path)")
	rootCmd.AddCommand(stopCmd)
}

func runStop(cmd *cobra.Command, args []string) error {
	repo := stopRepo
	if repo == "" {
		sessSvc := session.NewSessionService()
		var err error
		repo, err = resolveAgentRepo(context.Background(), "", sessSvc)
		if err != nil {
			return err
		}
	}

	pid, running := daemonstate.ReadLockStatus(repo)

	// Happy path: lock file has a valid, running PID
	if pid != 0 && running {
		return signalDaemon(pid)
	}

	// Stale lock: PID exists but process is dead
	if pid != 0 && !running {
		fmt.Printf("Lock file has stale PID %d (process dead), cleaning up\n", pid)
		daemonstate.ClearLockForRepo(repo)
	}

	// Fallback: scan for daemon processes matching this repo
	pids := findDaemonPIDsFunc(repo)
	if len(pids) == 0 {
		fmt.Println("Daemon is not running")
		return nil
	}

	fmt.Printf("Found %d orphaned daemon process(es) via process scan\n", len(pids))
	for _, p := range pids {
		if err := signalDaemon(p); err != nil {
			fmt.Printf("  Warning: failed to signal PID %d: %v\n", p, err)
		}
	}

	// Clean up any stale lock files
	daemonstate.ClearLockForRepo(repo)
	return nil
}

// signalDaemon sends SIGTERM to the given PID and prints confirmation.
func signalDaemon(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("could not find process %d: %w", pid, err)
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("failed to send SIGTERM to PID %d: %w", pid, err)
	}
	fmt.Printf("Sent SIGTERM to daemon (PID %d)\n", pid)
	return nil
}

// findDaemonPIDs uses pgrep to find daemon processes for the given repo.
func findDaemonPIDs(repo string) []int {
	out, err := exec.Command("pgrep", "-f", fmt.Sprintf("--_daemon --repo %s", repo)).Output()
	if err != nil {
		return nil
	}

	var pids []int
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if p, err := strconv.Atoi(strings.TrimSpace(line)); err == nil && p != os.Getpid() {
			pids = append(pids, p)
		}
	}
	return pids
}
