package cmd

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/zhubert/erg/internal/claude"
	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/logger"
	"github.com/zhubert/erg/internal/paths"
)

var agentCleanSkipConfirm bool

var agentCleanCmd = &cobra.Command{
	Use:   "clean",
	Short: "Remove agent daemon state, lock files, worktrees, auth files, MCP configs, and logs",
	Long: `Clears daemon state (work item tracking), removes lock files, worktrees,
container auth files (erg-auth-*), MCP config files (erg-mcp-*.json),
and log files (erg.log, mcp-*.log, stream-*.log).

This is useful when the daemon state becomes stale or corrupted,
when a lock file is left behind after an unclean shutdown,
or when orphaned auth/log/config files accumulate over time.

It will prompt for confirmation before proceeding unless the --yes flag is used.`,
	RunE: runAgentClean,
}

func init() {
	agentCleanCmd.Flags().BoolVarP(&agentCleanSkipConfirm, "yes", "y", false, "Skip confirmation prompt")
	rootCmd.AddCommand(agentCleanCmd)
}

func runAgentClean(cmd *cobra.Command, args []string) error {
	return runAgentCleanWithReader(os.Stdin)
}

// worktreeEntries returns the names of entries in the worktrees directory.
// Returns nil if the directory does not exist or is empty.
func worktreeEntries() []string {
	wtDir, err := paths.WorktreesDir()
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(wtDir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func runAgentCleanWithReader(input io.Reader) error {
	// Check what exists
	stateExists := daemonstate.StateExists()
	lockFiles, err := daemonstate.FindLocks()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: error finding lock files: %v\n", err)
	}
	wtEntries := worktreeEntries()
	authFiles, err := claude.FindAuthFiles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: error finding auth files: %v\n", err)
	}
	mcpConfigFiles, err := claude.FindMCPConfigFiles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: error finding MCP config files: %v\n", err)
	}
	logFileCount, err := logger.FindLogFiles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: error finding log files: %v\n", err)
	}

	if !stateExists && len(lockFiles) == 0 && len(wtEntries) == 0 && len(authFiles) == 0 && len(mcpConfigFiles) == 0 && logFileCount == 0 {
		fmt.Println("Nothing to clean.")
		return nil
	}

	// Print summary
	fmt.Println("This will clean:")
	if stateExists {
		fmt.Println("  - Daemon state file (daemon-state.json)")
	}
	if len(lockFiles) > 0 {
		fmt.Printf("  - %d daemon lock file(s)\n", len(lockFiles))
		for _, lf := range lockFiles {
			fmt.Printf("      %s\n", lf)
		}
		fmt.Println()
		fmt.Println("  Warning: lock files indicate a daemon may be running.")
		fmt.Println("  Cleaning while a daemon is active can cause issues.")
	}
	if len(wtEntries) > 0 {
		fmt.Printf("  - %d worktree(s)\n", len(wtEntries))
	}
	if len(authFiles) > 0 {
		fmt.Printf("  - %d container auth file(s)\n", len(authFiles))
	}
	if len(mcpConfigFiles) > 0 {
		fmt.Printf("  - %d MCP config file(s)\n", len(mcpConfigFiles))
	}
	if logFileCount > 0 {
		fmt.Printf("  - %d log file(s)\n", logFileCount)
	}

	// Confirm
	if !agentCleanSkipConfirm {
		if !confirm(input, "Continue?") {
			fmt.Println("Aborted.")
			return nil
		}
	}

	// Clean state file
	var stateRemoved bool
	if stateExists {
		if err := daemonstate.ClearState(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: error removing state file: %v\n", err)
		} else {
			stateRemoved = true
		}
	}

	// Clean lock files
	locksRemoved, err := daemonstate.ClearLocks()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: error removing lock files: %v\n", err)
	}

	// Clean worktrees
	var worktreesRemoved int
	if len(wtEntries) > 0 {
		wtDir, _ := paths.WorktreesDir()
		if err := os.RemoveAll(wtDir); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: error removing worktrees directory: %v\n", err)
		} else {
			worktreesRemoved = len(wtEntries)
		}
	}

	// Clean auth files
	authRemoved, err := claude.ClearAuthFiles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: error removing auth files: %v\n", err)
	}

	// Clean MCP config files
	mcpRemoved, err := claude.ClearMCPConfigFiles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: error removing MCP config files: %v\n", err)
	}

	// Clean log files
	logsRemoved, err := logger.ClearLogs()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: error removing log files: %v\n", err)
	}

	// Print results
	fmt.Println()
	fmt.Println("Cleaned:")
	if stateRemoved {
		fmt.Println("  - Daemon state file removed")
	}
	if locksRemoved > 0 {
		fmt.Printf("  - %d lock file(s) removed\n", locksRemoved)
	}
	if worktreesRemoved > 0 {
		fmt.Printf("  - %d worktree(s) removed\n", worktreesRemoved)
	}
	if authRemoved > 0 {
		fmt.Printf("  - %d auth file(s) removed\n", authRemoved)
	}
	if mcpRemoved > 0 {
		fmt.Printf("  - %d MCP config file(s) removed\n", mcpRemoved)
	}
	if logsRemoved > 0 {
		fmt.Printf("  - %d log file(s) removed\n", logsRemoved)
	}

	return nil
}
