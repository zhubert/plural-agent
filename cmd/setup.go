package cmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhubert/erg/internal/cli"
)

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Interactive setup wizard for erg",
	Long: `Walks you through configuring erg:

  - Checks required tools (git, claude, gh) and shows install instructions
  - Guides you through setting up your issue tracker (GitHub, Asana, or Linear)
  - Shows how to configure environment variables and workflow settings`,
	RunE: runSetup,
}

func init() {
	rootCmd.AddCommand(setupCmd)
}

func runSetup(cmd *cobra.Command, args []string) error {
	return runSetupWithIO(os.Stdin, os.Stdout, cli.CheckAll)
}

// prereqCheckerFn is the type for the prerequisite check function.
type prereqCheckerFn func([]cli.Prerequisite) []cli.CheckResult

func runSetupWithIO(input io.Reader, output io.Writer, checker prereqCheckerFn) error {
	fmt.Fprintln(output, "=== erg setup ===")
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
		fmt.Fprintln(output, "After installing, run `erg setup` again.")
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

	switch choice {
	case "1":
		printGitHubSetup(output)
	case "2":
		printAsanaSetup(output)
	case "3":
		printLinearSetup(output)
	default:
		fmt.Fprintf(output, "Invalid choice %q. Please run `erg setup` again and enter 1, 2, or 3.\n", choice)
	}

	return nil
}

func printGitHubSetup(w io.Writer) {
	fmt.Fprintln(w, "=== GitHub Issues Setup ===")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "GitHub Issues is the default issue tracker for erg.")
	fmt.Fprintln(w, "It uses the gh CLI (which you already have installed).")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Steps to get started:")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  1. Authenticate with GitHub:")
	fmt.Fprintln(w, "       gh auth login")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  2. Label issues with \"queued\" for erg to pick them up.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  3. Initialize your workflow config (from within your repo):")
	fmt.Fprintln(w, "       erg workflow init")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  4. Start the daemon:")
	fmt.Fprintln(w, "       erg start")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Your erg setup is complete!")
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
	fmt.Fprintln(w, "Configure your workflow (.erg/workflow.yaml):")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  source:")
	fmt.Fprintln(w, "    provider: asana")
	fmt.Fprintln(w, "    filter:")
	fmt.Fprintln(w, "      project: \"YOUR_PROJECT_GID\"  # from the project URL")
	fmt.Fprintln(w, "      label: queued              # Asana tag to filter on")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  # Find your project GID in the Asana project URL:")
	fmt.Fprintln(w, "  # https://app.asana.com/0/PROJECT_GID/list")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Next steps:")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  1. Set ASANA_PAT in your environment")
	fmt.Fprintln(w, "  2. Run: erg workflow init  (from within your repo)")
	fmt.Fprintln(w, "  3. Set your Asana project GID in .erg/workflow.yaml")
	fmt.Fprintln(w, "  4. Start the daemon: erg start")
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
	fmt.Fprintln(w, "Configure your workflow (.erg/workflow.yaml):")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  source:")
	fmt.Fprintln(w, "    provider: linear")
	fmt.Fprintln(w, "    filter:")
	fmt.Fprintln(w, "      team: \"YOUR_TEAM_ID\"  # Linear team ID")
	fmt.Fprintln(w, "      label: queued        # Linear label to filter on")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  # Find your team ID in Linear: Settings → API → or in the team URL.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Next steps:")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  1. Set LINEAR_API_KEY in your environment")
	fmt.Fprintln(w, "  2. Run: erg workflow init  (from within your repo)")
	fmt.Fprintln(w, "  3. Set your Linear team ID in .erg/workflow.yaml")
	fmt.Fprintln(w, "  4. Start the daemon: erg start")
}
