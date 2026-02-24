package cmd

import (
	"github.com/spf13/cobra"
)

var (
	startRepo       string
	startForeground bool
	startOnce       bool
)

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the daemon",
	Long: `Start the erg daemon for the given repository.

By default, forks into the background and detaches from the terminal.
Use -f/--foreground to stay attached with a live status display.

Examples:
  erg start                           # Start daemon for current repo
  erg start --repo owner/repo         # Start daemon for specific repo
  erg start -f --repo owner/repo      # Foreground with live status display
  erg start --once --repo owner/repo  # Run one tick, then exit`,
	RunE: runStart,
}

func init() {
	startCmd.Flags().StringVar(&startRepo, "repo", "", "Repo to poll (owner/repo or filesystem path)")
	startCmd.Flags().BoolVarP(&startForeground, "foreground", "f", false, "Stay in foreground with live status display")
	startCmd.Flags().BoolVar(&startOnce, "once", false, "Run one tick and exit (vs continuous daemon)")
	rootCmd.AddCommand(startCmd)
}

func runStart(cmd *cobra.Command, args []string) error {
	// Set package-level vars used by daemonize/runForeground/runDaemonWithLogger
	agentRepo = startRepo
	agentForeground = startForeground
	agentOnce = startOnce

	// --once implies foreground
	if agentOnce {
		agentForeground = true
	}

	if agentForeground {
		return runForeground(cmd, args)
	}
	return daemonize(cmd, args)
}
