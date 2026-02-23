package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/zhubert/plural-core/logger"
)

var (
	debugMode             bool
	quietMode             bool
	version, commit, date string
)

// SetVersionInfo sets version information from ldflags
func SetVersionInfo(v, c, d string) {
	version, commit, date = v, c, d
}

var rootCmd = &cobra.Command{
	Use:   "erg",
	Short: "Headless autonomous agent daemon for managing Claude Code sessions",
	Long: `Persistent orchestrator daemon that manages the full lifecycle of work items:
picking up issues, coding, PR creation, review feedback cycles, and final merge.

The daemon is stoppable and restartable without losing track of in-flight work.
State is persisted to ~/.plural/daemon-state.json.

If --repo is not specified and the current directory is inside a git repository,
that repository is used as the default.

Behavior is configured via .plural/workflow.yaml in your repository. Settings such
as max_turns, max_duration, merge_method, and auto_merge can all be specified there.

All sessions are containerized (container = sandbox).

Examples:
  erg                          # Use current git repo as default
  erg --repo owner/repo        # Run daemon (long-running)
  erg --repo owner/repo --once # Process one tick and exit
  erg --repo /path/to/repo     # Use filesystem path instead`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	cobra.OnInitialize(initConfig)
	rootCmd.PersistentFlags().BoolVar(&debugMode, "debug", true, "Enable debug logging (on by default)")
	rootCmd.PersistentFlags().BoolVarP(&quietMode, "quiet", "q", false, "Reduce logging to info level only")
}

func initConfig() {
	if quietMode {
		logger.SetDebug(false)
	} else if debugMode {
		logger.SetDebug(true)
	}
}

// Execute runs the root command
func Execute() error {
	rootCmd.Version = version
	rootCmd.SetVersionTemplate(versionTemplate())
	return rootCmd.Execute()
}

func versionTemplate() string {
	if commit != "none" && commit != "" {
		return fmt.Sprintf("erg %s\n  commit: %s\n  built:  %s\n", version, commit, date)
	}
	return fmt.Sprintf("erg %s\n", version)
}
