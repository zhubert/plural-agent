package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/zhubert/erg/internal/logger"
)

var (
	quietMode             bool
	version, commit, date string
)

// SetVersionInfo sets version information from ldflags
func SetVersionInfo(v, c, d string) {
	version, commit, date = v, c, d
}

var rootCmd = &cobra.Command{
	Use:   "erg",
	Short: "Autonomous coding agent that turns issues into merged PRs",
	Long: `erg watches your issue tracker for labeled issues, spins up Claude Code
sessions to implement them, creates PRs, responds to review feedback,
and merges when CI passes and reviewers approve.

Configure with .erg/workflow.yaml in your repository.
State is persisted to ~/.erg/ and survives restarts.`,
	Example: `  erg start                        # Start daemon for current repo
  erg start --repo owner/repo      # Start daemon for specific repo
  erg start -f --repo owner/repo   # Foreground with live status display
  erg status                       # Show daemon status summary
  erg stop                         # Stop the daemon gracefully`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	cobra.OnInitialize(initConfig)
	rootCmd.PersistentFlags().BoolVarP(&quietMode, "quiet", "q", false, "Reduce logging to info level only")

	// Command groups
	rootCmd.AddGroup(
		&cobra.Group{ID: "daemon", Title: "Daemon Commands:"},
		&cobra.Group{ID: "setup", Title: "Setup Commands:"},
	)

	// Hide the auto-generated completion command
	rootCmd.CompletionOptions.HiddenDefaultCmd = true
}

func initConfig() {
	if quietMode {
		logger.SetDebug(false)
	} else {
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
