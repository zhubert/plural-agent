package cmd

import (
	"context"
	"fmt"
	"os"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/session"
)

var stopRepo string

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
	if pid == 0 {
		fmt.Println("Daemon is not running")
		return nil
	}

	if !running {
		fmt.Printf("Daemon process (PID %d) is no longer alive\n", pid)
		fmt.Println("Use 'erg clean' to remove stale lock files")
		return nil
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("could not find process %d: %w", pid, err)
	}

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("failed to send SIGTERM to daemon (PID %d): %w", pid, err)
	}

	fmt.Printf("Sent SIGTERM to daemon (PID %d)\n", pid)
	fmt.Println("Use 'erg status' to verify shutdown")
	return nil
}
