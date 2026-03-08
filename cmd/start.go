package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/zhubert/erg/internal/paths"
)

var (
	startRepo          string
	startForeground    bool
	startOnce          bool
	startWorkflowFile  string
	startConfigFile    string
	startDashboardAddr string
	startDashboard     bool
)

var startCmd = &cobra.Command{
	Use:     "start",
	Short:   "Start the daemon",
	GroupID: "daemon",
	Long: `Start the erg daemon for the given repository or set of repositories.

By default, forks into the background and detaches from the terminal.
Use -f/--foreground to stay attached with a live status display.
Use --config to watch multiple repos with a config file.
Use --dashboard to also start the embedded web dashboard at localhost:21122.

If no --repo or --config is provided, looks for a default config at
~/.erg/daemon.yaml and uses it automatically.

If installed via Homebrew, use 'brew services start erg' for persistent
service management (includes the dashboard automatically).

Examples:
  erg start                           # Start using default config or current repo
  erg start --repo owner/repo         # Start daemon for specific repo
  erg start -f --repo owner/repo      # Foreground with live status display
  erg start --once --repo owner/repo  # Run one tick, then exit
  erg start --config config.yaml       # Watch multiple repos
  erg start --dashboard               # Start daemon with embedded web dashboard`,
	RunE: runStart,
}

func init() {
	startCmd.Flags().StringVar(&startRepo, "repo", "", "Repo to poll (owner/repo or filesystem path)")
	startCmd.Flags().BoolVarP(&startForeground, "foreground", "f", false, "Stay in foreground with live status display")
	startCmd.Flags().BoolVar(&startOnce, "once", false, "Run one tick and exit (vs continuous daemon)")
	startCmd.Flags().StringVar(&startWorkflowFile, "workflow", "", "Path to workflow config file (default: <repo>/.erg/workflow.yaml)")
	startCmd.Flags().StringVar(&startConfigFile, "config", "", "Path to config file for multi-repo mode")
	startCmd.Flags().StringVar(&startDashboardAddr, "dashboard-addr", "", "Start an embedded dashboard server at this address (e.g. localhost:21122)")
	startCmd.Flags().BoolVar(&startDashboard, "dashboard", false, "Start an embedded dashboard at localhost:21122")
	rootCmd.AddCommand(startCmd)
}

func runStart(cmd *cobra.Command, args []string) error {
	if startConfigFile != "" && (startRepo != "" || startWorkflowFile != "") {
		return fmt.Errorf("--config cannot be used with --repo or --workflow")
	}

	// Auto-discover default manifest when no flags are provided
	if startConfigFile == "" && startRepo == "" && startWorkflowFile == "" {
		if p, err := paths.ManifestPath(); err == nil {
			if _, err := os.Stat(p); err == nil {
				startConfigFile = p
				fmt.Printf("Using default config: %s\n", p)
			}
		}
	}

	// Set package-level vars used by daemonize/runForeground/runDaemonWithLogger
	agentRepo = startRepo
	agentForeground = startForeground
	agentOnce = startOnce
	agentWorkflowFile = startWorkflowFile
	agentConfigFile = startConfigFile
	agentDashboardAddr = startDashboardAddr
	if startDashboard && agentDashboardAddr == "" {
		agentDashboardAddr = "localhost:21122"
	}

	// --once implies foreground
	if agentOnce {
		agentForeground = true
	}

	if agentForeground {
		return runForeground(cmd, args)
	}
	return daemonize(cmd, args)
}
