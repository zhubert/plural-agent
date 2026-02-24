package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/zhubert/erg/internal/logger"
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

The daemon polls for issues labeled 'queued', creates containerized Claude Code
sessions, monitors CI and review feedback, and auto-merges approved PRs.

State is persisted to ~/.erg/ and survives restarts. Behavior is configured via
.erg/workflow.yaml in your repository.

Examples:
  erg start                        # Start daemon for current repo
  erg start --repo owner/repo      # Start daemon for specific repo
  erg start -f --repo owner/repo   # Foreground with live status display
  erg status                       # Show daemon status summary
  erg stop                         # Stop the daemon gracefully`,
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
