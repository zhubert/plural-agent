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
	Use:   "plural-agent",
	Short: "Headless autonomous agent daemon for managing Claude Code sessions",
	Long: `Persistent orchestrator daemon that manages the full lifecycle of work items:
picking up issues, coding, PR creation, review feedback cycles, and final merge.

The daemon is stoppable and restartable without losing track of in-flight work.
State is persisted to ~/.plural/daemon-state.json.

If --repo is not specified and the current directory is inside a git repository,
that repository is used as the default. Auto-merge is enabled by default;
use --no-auto-merge to disable.

All sessions are containerized (container = sandbox).

Examples:
  plural-agent                                    # Use current git repo as default
  plural-agent --repo owner/repo                  # Run daemon (long-running, auto-merge on)
  plural-agent --repo owner/repo --once           # Process one tick and exit
  plural-agent --repo /path/to/repo               # Use filesystem path instead
  plural-agent --repo owner/repo --no-auto-merge  # Disable auto-merge
  plural-agent --repo owner/repo --max-turns 100`,
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
		return fmt.Sprintf("plural-agent %s\n  commit: %s\n  built:  %s\n", version, commit, date)
	}
	return fmt.Sprintf("plural-agent %s\n", version)
}
