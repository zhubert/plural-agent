package cmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhubert/erg/internal/cli"
	"github.com/zhubert/erg/internal/workflow"
)

var configureCmd = &cobra.Command{
	Use:   "configure",
	Short: "Interactive configuration wizard for erg",
	Long: `Walks you through configuring erg:

  - Checks required tools (git, claude, gh) and shows install instructions
  - Guides you through setting up your issue tracker (GitHub, Asana, or Linear)
  - Asks workflow questions and generates .erg/workflow.yaml for your repo`,
	RunE: runConfigure,
}

func init() {
	rootCmd.AddCommand(configureCmd)
}

func runConfigure(cmd *cobra.Command, args []string) error {
	repoPath, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}
	return runConfigureWithIO(os.Stdin, os.Stdout, cli.CheckAll, repoPath, workflow.WriteFromWizard)
}

// prereqCheckerFn is the type for the prerequisite check function.
type prereqCheckerFn func([]cli.Prerequisite) []cli.CheckResult

// workflowWriterFn is a function that writes a workflow config based on wizard answers.
type workflowWriterFn func(repoPath string, cfg workflow.WizardConfig) (string, error)

func runConfigureWithIO(input io.Reader, output io.Writer, checker prereqCheckerFn, repoPath string, writer workflowWriterFn) error {
	fmt.Fprintln(output, "=== erg configure ===")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Checking prerequisites...")
	fmt.Fprintln(output)

	prereqs := cli.DefaultPrerequisites()
	results := checker(prereqs)

	anyRequiredMissing := false
	for _, r := range results {
		var status string
		switch {
		case r.Found:
			status = "✓"
		case r.Prerequisite.Required:
			status = "✗"
			anyRequiredMissing = true
		default:
			status = "○"
		}

		line := fmt.Sprintf("  %s %s", status, r.Prerequisite.Name)
		if r.Found && r.Version != "" {
			line += fmt.Sprintf(" (%s)", r.Version)
		} else if !r.Found {
			line += " [not found]"
		}
		fmt.Fprintln(output, line)
	}

	fmt.Fprintln(output)

	if anyRequiredMissing {
		fmt.Fprintln(output, "Some required tools are missing. Install them to continue:")
		fmt.Fprintln(output)
		for _, r := range results {
			if !r.Found && r.Prerequisite.Required {
				fmt.Fprintf(output, "  %s — %s\n", r.Prerequisite.Name, r.Prerequisite.Description)
				fmt.Fprintf(output, "    Install: %s\n", r.Prerequisite.InstallURL)
				fmt.Fprintln(output)
			}
		}
		fmt.Fprintln(output, "After installing, run `erg configure` again.")
		return nil
	}

	fmt.Fprintln(output, "All prerequisites installed!")
	fmt.Fprintln(output)

	fmt.Fprintln(output, "Which issue tracker would you like to use?")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "  1. GitHub Issues  (uses gh CLI — no extra credentials needed)")
	fmt.Fprintln(output, "  2. Asana Tasks    (requires ASANA_PAT environment variable)")
	fmt.Fprintln(output, "  3. Linear Issues  (requires LINEAR_API_KEY environment variable)")
	fmt.Fprintln(output)
	fmt.Fprint(output, "Enter choice [1-3]: ")

	scanner := bufio.NewScanner(input)
	var choice string
	if scanner.Scan() {
		choice = strings.TrimSpace(scanner.Text())
	}

	fmt.Fprintln(output)

	var provider string
	switch choice {
	case "1":
		printGitHubSetup(output)
		provider = "github"
	case "2":
		printAsanaSetup(output)
		provider = "asana"
	case "3":
		printLinearSetup(output)
		provider = "linear"
	default:
		fmt.Fprintf(output, "Invalid choice %q. Please run `erg configure` again and enter 1, 2, or 3.\n", choice)
		return nil
	}

	// Run the workflow wizard to generate .erg/workflow.yaml
	wizardCfg := runWorkflowWizard(scanner, output, provider)

	fmt.Fprintln(output)
	fp, err := writer(repoPath, wizardCfg)
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			fmt.Fprintf(output, "Note: %s already exists, skipping workflow generation.\n", fp)
			fmt.Fprintln(output, "      Delete it and run `erg configure` again to reconfigure.")
		} else {
			fmt.Fprintf(output, "Failed to write workflow config: %v\n", err)
			return nil
		}
	} else {
		fmt.Fprintf(output, "Created %s\n", fp)
	}

	fmt.Fprintln(output)
	fmt.Fprintln(output, "Setup complete! Start the daemon:")
	fmt.Fprintln(output, "  erg start")

	return nil
}

// runWorkflowWizard prompts the user for workflow configuration choices and
// returns a WizardConfig populated from their answers. Pressing Enter accepts
// the default for each question. EOF (e.g. piped input exhausted) also accepts
// the default, so existing tests that only provide tracker-selection input
// still work by getting sensible defaults for all wizard questions.
func runWorkflowWizard(scanner *bufio.Scanner, output io.Writer, provider string) workflow.WizardConfig {
	cfg := workflow.WizardConfig{
		Provider:    provider,
		Label:       "queued",
		FixCI:       true,
		AutoReview:  true,
		MergeMethod: "rebase",
	}

	fmt.Fprintln(output)
	fmt.Fprintln(output, "=== Workflow Configuration ===")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Let's configure your workflow. Press Enter to accept defaults.")
	fmt.Fprintln(output)

	switch provider {
	case "asana":
		fmt.Fprint(output, "Asana project GID (from URL: https://app.asana.com/0/GID/list): ")
		cfg.Project = readLine(scanner)

		fmt.Fprintln(output)
		fmt.Fprintln(output, "How do you organize work in Asana?")
		fmt.Fprintln(output, "  1. By tags (label tasks with a tag like \"queued\")")
		fmt.Fprintln(output, "  2. By board sections (Kanban)")
		fmt.Fprint(output, "Enter choice [1-2]: ")
		orgChoice := readWithDefault(scanner, "1")

		if orgChoice == "2" {
			cfg.Kanban = true
			fmt.Fprint(output, "Which section has new tasks? [To do]: ")
			cfg.Section = readWithDefault(scanner, "To do")
			cfg.Label = ""
			fmt.Fprint(output, "Completion section? [Done]: ")
			cfg.CompletionSection = readWithDefault(scanner, "Done")
		} else {
			fmt.Fprint(output, "Asana tag to watch for new tasks? [queued]: ")
			cfg.Label = readWithDefault(scanner, "queued")
			fmt.Fprint(output, "Filter to section? (press Enter to skip): ")
			cfg.Section = readLine(scanner)
			fmt.Fprint(output, "Move completed tasks to which section? (press Enter to skip): ")
			cfg.CompletionSection = readLine(scanner)
		}

	case "linear":
		fmt.Fprint(output, "Linear team ID (from Settings → API): ")
		cfg.Team = readLine(scanner)

		fmt.Fprintln(output)
		fmt.Fprintln(output, "How do you organize work in Linear?")
		fmt.Fprintln(output, "  1. By labels (label issues with \"queued\")")
		fmt.Fprintln(output, "  2. By workflow states (Kanban)")
		fmt.Fprint(output, "Enter choice [1-2]: ")
		orgChoice := readWithDefault(scanner, "1")

		if orgChoice == "2" {
			cfg.Kanban = true
			fmt.Fprint(output, "Label to identify erg-managed issues? [queued]: ")
			cfg.Label = readWithDefault(scanner, "queued")
			fmt.Fprint(output, "Completion state? [Done]: ")
			cfg.CompletionState = readWithDefault(scanner, "Done")
		} else {
			fmt.Fprint(output, "Linear label to watch for new issues? [queued]: ")
			cfg.Label = readWithDefault(scanner, "queued")
			fmt.Fprint(output, "Move completed issues to which state? (press Enter to skip): ")
			cfg.CompletionState = readLine(scanner)
		}

	default: // github
		fmt.Fprint(output, "Label to watch for new issues? [queued]: ")
		cfg.Label = readWithDefault(scanner, "queued")
	}

	fmt.Fprint(output, "Should Claude plan the approach before coding? [y/N]: ")
	cfg.PlanFirst = readYesNo(scanner, false)

	fmt.Fprint(output, "Should Claude try to fix failing CI? [Y/n]: ")
	cfg.FixCI = readYesNo(scanner, true)

	fmt.Fprint(output, "Should Claude auto-address PR review comments? [Y/n]: ")
	cfg.AutoReview = readYesNo(scanner, true)

	fmt.Fprint(output, "GitHub username to request as reviewer (press Enter to skip): ")
	cfg.Reviewer = readLine(scanner)

	fmt.Fprint(output, "Merge method (rebase/squash/merge) [rebase]: ")
	method := readWithDefault(scanner, "rebase")
	if method == "squash" || method == "merge" {
		cfg.MergeMethod = method
	} else {
		cfg.MergeMethod = "rebase"
	}

	fmt.Fprint(output, "Run sessions in Docker containers? [y/N]: ")
	cfg.Containerized = readYesNo(scanner, false)

	fmt.Fprint(output, "Send Slack notifications on failure? [y/N]: ")
	cfg.NotifySlack = readYesNo(scanner, false)
	if cfg.NotifySlack {
		fmt.Fprint(output, "Slack webhook URL or env var (e.g., $SLACK_WEBHOOK_URL): ")
		cfg.SlackWebhook = readLine(scanner)
		if cfg.SlackWebhook == "" {
			cfg.NotifySlack = false
		}
	}

	return cfg
}

// readLine reads one line from the scanner and returns the trimmed text,
// or "" if the scanner is exhausted (EOF).
func readLine(scanner *bufio.Scanner) string {
	if !scanner.Scan() {
		return ""
	}
	return strings.TrimSpace(scanner.Text())
}

// readWithDefault reads one line and returns def if the input is empty or EOF.
func readWithDefault(scanner *bufio.Scanner, def string) string {
	if !scanner.Scan() {
		return def
	}
	text := strings.TrimSpace(scanner.Text())
	if text == "" {
		return def
	}
	return text
}

// readYesNo reads one line and interprets "y"/"yes" as true, "n"/"no" as false.
// Empty input or EOF returns defaultYes.
func readYesNo(scanner *bufio.Scanner, defaultYes bool) bool {
	if !scanner.Scan() {
		return defaultYes
	}
	text := strings.ToLower(strings.TrimSpace(scanner.Text()))
	if text == "" {
		return defaultYes
	}
	return text == "y" || text == "yes"
}

func printGitHubSetup(w io.Writer) {
	fmt.Fprintln(w, "=== GitHub Issues Setup ===")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "GitHub Issues is the default issue tracker for erg.")
	fmt.Fprintln(w, "It uses the gh CLI (which you already have installed).")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Before we configure your workflow:")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  1. Authenticate with GitHub (if you haven't already):")
	fmt.Fprintln(w, "       gh auth login")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  2. Label issues with your chosen label for erg to pick them up.")
	fmt.Fprintln(w)
}

func printAsanaSetup(w io.Writer) {
	fmt.Fprintln(w, "=== Asana Tasks Setup ===")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "To use Asana Tasks, you need a Personal Access Token (PAT).")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Steps to get your PAT:")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  1. Go to the Asana Developer Console:")
	fmt.Fprintln(w, "       https://app.asana.com/0/my-apps")
	fmt.Fprintln(w, "  2. Click \"+ New access token\"")
	fmt.Fprintln(w, "  3. Give it a description (e.g., \"erg\") and copy the token")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Add the token to your shell profile (~/.zshrc or ~/.bashrc):")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  export ASANA_PAT=\"your-token-here\"")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  # Reload your shell:")
	fmt.Fprintln(w, "  source ~/.zshrc")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Find your project GID in the Asana project URL:")
	fmt.Fprintln(w, "  https://app.asana.com/0/PROJECT_GID/list")
	fmt.Fprintln(w)
}

func printLinearSetup(w io.Writer) {
	fmt.Fprintln(w, "=== Linear Issues Setup ===")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "To use Linear Issues, you need an API key.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Steps to get your API key:")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  1. Log in at https://linear.app")
	fmt.Fprintln(w, "  2. Go to Settings → API → Personal API Keys")
	fmt.Fprintln(w, "  3. Click \"New API key\", give it a name (e.g., \"erg\"), and copy the key")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Add the key to your shell profile (~/.zshrc or ~/.bashrc):")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  export LINEAR_API_KEY=\"your-key-here\"")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  # Reload your shell:")
	fmt.Fprintln(w, "  source ~/.zshrc")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Find your team ID in Linear: Settings → API → or in the team URL.")
	fmt.Fprintln(w)
}
