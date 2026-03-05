package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	startRepo         string
	startForeground   bool
	startOnce         bool
	startWorkflowFile string
	startConfigFile   string
)

var startCmd = &cobra.Command{
	Use:     "start",
	Short:   "Start the daemon",
	GroupID: "daemon",
	Long: `Start the erg daemon for the given repository or set of repositories.

By default, forks into the background and detaches from the terminal.
Use -f/--foreground to stay attached with a live status display.
Use --config to watch multiple repos with a manifest file.

Examples:
  erg start                           # Start daemon for current repo
  erg start --repo owner/repo         # Start daemon for specific repo
  erg start -f --repo owner/repo      # Foreground with live status display
  erg start --once --repo owner/repo  # Run one tick, then exit
  erg start --config manifest.yaml    # Watch multiple repos`,
	RunE: runStart,
}

func init() {
	startCmd.Flags().StringVar(&startRepo, "repo", "", "Repo to poll (owner/repo or filesystem path)")
	startCmd.Flags().BoolVarP(&startForeground, "foreground", "f", false, "Stay in foreground with live status display")
	startCmd.Flags().BoolVar(&startOnce, "once", false, "Run one tick and exit (vs continuous daemon)")
	startCmd.Flags().StringVar(&startWorkflowFile, "workflow", "", "Path to workflow config file (default: <repo>/.erg/workflow.yaml)")
	startCmd.Flags().StringVar(&startConfigFile, "config", "", "Path to manifest file for multi-repo mode")
	rootCmd.AddCommand(startCmd)
}

func runStart(cmd *cobra.Command, args []string) error {
	if startConfigFile != "" && (startRepo != "" || startWorkflowFile != "") {
		return fmt.Errorf("--config cannot be used with --repo or --workflow")
	}

	// Set package-level vars used by daemonize/runForeground/runDaemonWithLogger
	agentRepo = startRepo
	agentForeground = startForeground
	agentOnce = startOnce
	agentWorkflowFile = startWorkflowFile
	agentConfigFile = startConfigFile

	// --once implies foreground
	if agentOnce {
		agentForeground = true
	}

	if agentForeground {
		return runForeground(cmd, args)
	}
	return daemonize(cmd, args)
}
