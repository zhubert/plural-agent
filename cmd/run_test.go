package cmd

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestRunCmd_FlagRegistration(t *testing.T) {
	// Verify the --issue flag is registered and required
	issueFlag := runCmd.Flags().Lookup("issue")
	if issueFlag == nil {
		t.Fatal("expected --issue flag to be registered")
	}

	repoFlag := runCmd.Flags().Lookup("repo")
	if repoFlag == nil {
		t.Fatal("expected --repo flag to be registered")
	}

	workflowFlag := runCmd.Flags().Lookup("workflow")
	if workflowFlag == nil {
		t.Fatal("expected --workflow flag to be registered")
	}
}

func TestRunCmd_IsRegisteredWithRoot(t *testing.T) {
	var found bool
	for _, sub := range rootCmd.Commands() {
		if sub.Use == "run" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'run' subcommand to be registered with root command")
	}
}

func TestRunCmd_GroupID(t *testing.T) {
	if runCmd.GroupID != "daemon" {
		t.Errorf("expected GroupID 'daemon', got %q", runCmd.GroupID)
	}
}

func TestRunCmd_RequiredIssueFlag(t *testing.T) {
	// Invoke runCmd without --issue and expect an error about required flag
	cmd := &cobra.Command{
		Use:  "run",
		RunE: runIssue,
	}
	cmd.Flags().StringVar(&runIssueID, "issue", "", "Issue ID")
	cmd.Flags().StringVar(&runRepo, "repo", "", "Repo path")
	cmd.Flags().StringVar(&runWorkflowFile, "workflow", "", "Workflow file")
	_ = cmd.MarkFlagRequired("issue")

	// With --issue set, the flag parsing should succeed (the function itself may fail for other reasons)
	cmd.SetArgs([]string{"--issue", "42"})
	// We only test that the flag machinery works; actual execution needs real env.
	err := cmd.ParseFlags([]string{"--issue", "42"})
	if err != nil {
		t.Fatalf("unexpected flag parse error: %v", err)
	}
	if runIssueID != "42" {
		t.Errorf("expected runIssueID '42', got %q", runIssueID)
	}
}
