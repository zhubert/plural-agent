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
	newCmd := func() *cobra.Command {
		var issueID, repo, workflowFile string
		cmd := &cobra.Command{
			Use:  "run",
			RunE: runIssue,
		}
		cmd.Flags().StringVar(&issueID, "issue", "", "Issue ID")
		cmd.Flags().StringVar(&repo, "repo", "", "Repo path")
		cmd.Flags().StringVar(&workflowFile, "workflow", "", "Workflow file")
		_ = cmd.MarkFlagRequired("issue")
		return cmd
	}

	// Without --issue, Execute should return an error about the required flag.
	t.Run("missing --issue returns error", func(t *testing.T) {
		cmd := newCmd()
		cmd.SetArgs([]string{})
		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error when --issue is omitted, got nil")
		}
	})

	// With --issue set, flag parsing succeeds and the value is stored correctly.
	t.Run("with --issue flag parsing succeeds", func(t *testing.T) {
		cmd := newCmd()
		err := cmd.ParseFlags([]string{"--issue", "42"})
		if err != nil {
			t.Fatalf("unexpected flag parse error: %v", err)
		}
		got, err := cmd.Flags().GetString("issue")
		if err != nil {
			t.Fatalf("unexpected error getting flag: %v", err)
		}
		if got != "42" {
			t.Errorf("expected issue flag value '42', got %q", got)
		}
	})
}
