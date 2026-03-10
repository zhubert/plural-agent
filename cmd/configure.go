package cmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhubert/erg/internal/cli"
	"github.com/zhubert/erg/internal/secrets"
	"github.com/zhubert/erg/internal/workflow"
	"golang.org/x/term"
)

var configureCmd = &cobra.Command{
	Use:     "configure",
	Short:   "Interactive configuration wizard for erg",
	GroupID: "setup",
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
	return runConfigureWithIO(os.Stdin, os.Stdout, cli.CheckAll, repoPath, workflow.WriteFromWizard, false)
}

// prereqCheckerFn is the type for the prerequisite check function.
type prereqCheckerFn func([]cli.Prerequisite) []cli.CheckResult

// workflowWriterFn is a function that writes a workflow config based on wizard answers.
type workflowWriterFn func(repoPath string, cfg workflow.WizardConfig) (string, error)

func runConfigureWithIO(input io.Reader, output io.Writer, checker prereqCheckerFn, repoPath string, writer workflowWriterFn, _ bool) error {
	scanner := bufio.NewScanner(input)

	// Phase 1: Prerequisites
	if !checkPrereqs(output, checker) {
		return nil
	}

	// Phase 2: Tracker selection
	fmt.Fprintln(output, "Which issue tracker would you like to use?")
	fmt.Fprintln(output, "  1) GitHub Issues  (uses gh CLI)")
	fmt.Fprintln(output, "  2) Asana Tasks    (requires ASANA_PAT)")
	fmt.Fprintln(output, "  3) Linear Issues  (requires LINEAR_API_KEY)")
	provider := promptSelect(scanner, output, "Choice [1-3]: ", []string{"github", "asana", "linear"})

	// Phase 3: Provider setup + source config
	cfg := workflow.WizardConfig{
		Provider:    provider,
		Label:       "queued",
		AutoMerge:   true,
		MergeMethod: "rebase",
	}

	fmt.Fprintln(output)
	fmt.Fprintln(output, providerSetupTitle(provider))
	fmt.Fprintln(output, buildProviderSetupText(provider))
	fmt.Fprintln(output)

	switch provider {
	case "github":
		cfg.Label = promptStringDefault(scanner, output, "Label to watch for new issues", cfg.Label)
	case "asana":
		collectAsanaConfig(scanner, input, output, &cfg)
	case "linear":
		collectLinearConfig(scanner, input, output, &cfg)
	}

	// Phase 4: Workflow behavior
	fmt.Fprintln(output)
	cfg.PlanFirst = promptYN(scanner, output, "Should Claude plan the approach before coding?", false)
	cfg.Reviewer = promptString(scanner, output, "GitHub username to request as reviewer (Enter to skip)")

	cfg.AutoMerge = promptYN(scanner, output, "Should erg auto-merge approved PRs?", true)
	if cfg.AutoMerge {
		fmt.Fprintln(output, "Merge method:")
		fmt.Fprintln(output, "  1) Rebase")
		fmt.Fprintln(output, "  2) Squash")
		fmt.Fprintln(output, "  3) Merge")
		cfg.MergeMethod = promptSelect(scanner, output, "Choice [1-3]: ", []string{"rebase", "squash", "merge"})
	}

	cfg.Containerized = promptYN(scanner, output, "Run sessions in Docker containers?", false)

	// Phase 5: Summary + confirm
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Configuration Summary")
	fmt.Fprintln(output, buildSummaryText(cfg))

	if !promptYN(scanner, output, "Write configuration?", true) {
		fmt.Fprintln(output, "Configuration cancelled.")
		return nil
	}

	// Write config
	fp, err := writer(repoPath, cfg)
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

// checkPrereqs prints prerequisite status and returns true if all required prereqs are met.
func checkPrereqs(output io.Writer, checker prereqCheckerFn) bool {
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
		return false
	}

	fmt.Fprintln(output, "All prerequisites installed!")
	fmt.Fprintln(output)
	return true
}

func collectAsanaConfig(scanner *bufio.Scanner, input io.Reader, output io.Writer, cfg *workflow.WizardConfig) {
	if secrets.IsKeychainAvailable() {
		fmt.Fprintln(output, "You can store your Asana PAT in the macOS Keychain")
		fmt.Fprintln(output, "so erg works via brew services without shell env vars.")
		fmt.Fprintln(output)
		if promptYN(scanner, output, "Store ASANA_PAT in Keychain?", true) {
			pat := promptSecret(scanner, input, output, "Paste your Asana PAT")
			if pat != "" {
				if err := secrets.Set(secrets.AsanaPATService, pat); err != nil {
					fmt.Fprintf(output, "Warning: failed to store in Keychain: %v\n", err)
				} else {
					fmt.Fprintln(output, "Saved to macOS Keychain.")
				}
			}
		}
		fmt.Fprintln(output)
	}

	cfg.Project = promptString(scanner, output, "Asana project GID (from URL: https://app.asana.com/0/GID/list)")

	fmt.Fprintln(output, "How do you organize work in Asana?")
	fmt.Fprintln(output, "  1) By tags")
	fmt.Fprintln(output, "  2) By board sections (Kanban)")
	orgChoice := promptSelect(scanner, output, "Choice [1-2]: ", []string{"tags", "kanban"})

	if orgChoice == "kanban" {
		cfg.Kanban = true
		cfg.Label = ""
		cfg.Section = promptStringDefault(scanner, output, "Which section has new tasks?", "To do")
		cfg.CompletionSection = promptStringDefault(scanner, output, "Completion section?", "Done")
	} else {
		cfg.Label = promptStringDefault(scanner, output, "Asana tag to watch for new tasks?", cfg.Label)
		cfg.Section = promptString(scanner, output, "Filter to section? (Enter to skip)")
		cfg.CompletionSection = promptString(scanner, output, "Move completed tasks to which section? (Enter to skip)")
	}
}

func collectLinearConfig(scanner *bufio.Scanner, input io.Reader, output io.Writer, cfg *workflow.WizardConfig) {
	if secrets.IsKeychainAvailable() {
		fmt.Fprintln(output, "You can store your Linear API key in the macOS Keychain")
		fmt.Fprintln(output, "so erg works via brew services without shell env vars.")
		fmt.Fprintln(output)
		if promptYN(scanner, output, "Store LINEAR_API_KEY in Keychain?", true) {
			key := promptSecret(scanner, input, output, "Paste your Linear API key")
			if key != "" {
				if err := secrets.Set(secrets.LinearAPIKeyService, key); err != nil {
					fmt.Fprintf(output, "Warning: failed to store in Keychain: %v\n", err)
				} else {
					fmt.Fprintln(output, "Saved to macOS Keychain.")
				}
			}
		}
		fmt.Fprintln(output)
	}

	cfg.Team = promptString(scanner, output, "Linear team ID (from Settings → API)")

	fmt.Fprintln(output, "How do you organize work in Linear?")
	fmt.Fprintln(output, "  1) By labels")
	fmt.Fprintln(output, "  2) By workflow states (Kanban)")
	orgChoice := promptSelect(scanner, output, "Choice [1-2]: ", []string{"labels", "kanban"})

	if orgChoice == "kanban" {
		cfg.Kanban = true
		cfg.Label = promptStringDefault(scanner, output, "Label to identify erg-managed issues?", cfg.Label)
		cfg.CompletionState = promptStringDefault(scanner, output, "Completion state?", "Done")
	} else {
		cfg.Label = promptStringDefault(scanner, output, "Linear label to watch for new issues?", cfg.Label)
		cfg.CompletionState = promptString(scanner, output, "Move completed issues to which state? (Enter to skip)")
	}
}

func providerSetupTitle(provider string) string {
	switch provider {
	case "github":
		return "GitHub Issues Setup"
	case "asana":
		return "Asana Tasks Setup"
	case "linear":
		return "Linear Issues Setup"
	default:
		return "Setup"
	}
}

func buildProviderSetupText(provider string) string {
	var b strings.Builder
	switch provider {
	case "github":
		b.WriteString("GitHub Issues is the default issue tracker for erg.\n")
		b.WriteString("It uses the gh CLI (which you already have installed).\n\n")
		b.WriteString("Before we configure your workflow:\n\n")
		b.WriteString("  1. Authenticate with GitHub (if you haven't already):\n")
		b.WriteString("       gh auth login\n\n")
		b.WriteString("  2. Label issues with your chosen label for erg to pick them up.")
	case "asana":
		b.WriteString("To use Asana Tasks, you need a Personal Access Token (PAT).\n\n")
		b.WriteString("Steps to get your PAT:\n\n")
		b.WriteString("  1. Go to the Asana Developer Console:\n")
		b.WriteString("       https://app.asana.com/0/my-apps\n")
		b.WriteString("  2. Click \"+ New access token\"\n")
		b.WriteString("  3. Give it a description (e.g., \"erg\") and copy the token\n\n")
		if secrets.IsKeychainAvailable() {
			b.WriteString("You can either:\n")
			b.WriteString("  a) Store it in the macOS Keychain (recommended for brew services) —\n")
			b.WriteString("     you'll be prompted in the next step\n")
			b.WriteString("  b) Add it to your shell profile (~/.zshrc or ~/.bashrc):\n")
			b.WriteString("       export ASANA_PAT=\"your-token-here\"\n\n")
		} else {
			b.WriteString("Add the token to your shell profile (~/.zshrc or ~/.bashrc):\n\n")
			b.WriteString("  export ASANA_PAT=\"your-token-here\"\n\n")
		}
		b.WriteString("Find your project GID in the Asana project URL:\n")
		b.WriteString("  https://app.asana.com/0/PROJECT_GID/list")
	case "linear":
		b.WriteString("To use Linear Issues, you need an API key.\n\n")
		b.WriteString("Steps to get your API key:\n\n")
		b.WriteString("  1. Log in at https://linear.app\n")
		b.WriteString("  2. Go to Settings → API → Personal API Keys\n")
		b.WriteString("  3. Click \"New API key\", give it a name (e.g., \"erg\"), and copy the key\n\n")
		if secrets.IsKeychainAvailable() {
			b.WriteString("You can either:\n")
			b.WriteString("  a) Store it in the macOS Keychain (recommended for brew services) —\n")
			b.WriteString("     you'll be prompted in the next step\n")
			b.WriteString("  b) Add it to your shell profile (~/.zshrc or ~/.bashrc):\n")
			b.WriteString("       export LINEAR_API_KEY=\"your-key-here\"\n\n")
		} else {
			b.WriteString("Add the key to your shell profile (~/.zshrc or ~/.bashrc):\n\n")
			b.WriteString("  export LINEAR_API_KEY=\"your-key-here\"\n\n")
		}
		b.WriteString("Find your team ID in Linear: Settings → API → or in the team URL.")
	}
	return b.String()
}

func buildSummaryText(cfg workflow.WizardConfig) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Provider:       %s\n", cfg.Provider))
	if cfg.Label != "" {
		b.WriteString(fmt.Sprintf("Label:          %s\n", cfg.Label))
	}
	if cfg.Project != "" {
		b.WriteString(fmt.Sprintf("Project:        %s\n", cfg.Project))
	}
	if cfg.Team != "" {
		b.WriteString(fmt.Sprintf("Team:           %s\n", cfg.Team))
	}
	if cfg.Kanban {
		b.WriteString("Organization:   Kanban\n")
		if cfg.Section != "" {
			b.WriteString(fmt.Sprintf("Section:        %s\n", cfg.Section))
		}
		if cfg.CompletionSection != "" {
			b.WriteString(fmt.Sprintf("Done section:   %s\n", cfg.CompletionSection))
		}
		if cfg.CompletionState != "" {
			b.WriteString(fmt.Sprintf("Done state:     %s\n", cfg.CompletionState))
		}
	}
	b.WriteString(fmt.Sprintf("Plan first:     %v\n", cfg.PlanFirst))
	if cfg.Reviewer != "" {
		b.WriteString(fmt.Sprintf("Reviewer:       %s\n", cfg.Reviewer))
	}
	b.WriteString(fmt.Sprintf("Auto-merge:     %v\n", cfg.AutoMerge))
	if cfg.AutoMerge {
		b.WriteString(fmt.Sprintf("Merge method:   %s\n", cfg.MergeMethod))
	}
	b.WriteString(fmt.Sprintf("Containerized:  %v\n", cfg.Containerized))
	return b.String()
}

// promptString shows a prompt and reads a line of input. Returns empty string on empty input.
func promptString(scanner *bufio.Scanner, output io.Writer, prompt string) string {
	fmt.Fprintf(output, "%s: ", prompt)
	if scanner.Scan() {
		return strings.TrimSpace(scanner.Text())
	}
	return ""
}

// promptStringDefault shows a prompt with a default value. Returns the default if input is empty.
func promptStringDefault(scanner *bufio.Scanner, output io.Writer, prompt, defaultVal string) string {
	fmt.Fprintf(output, "%s [%s]: ", prompt, defaultVal)
	if scanner.Scan() {
		val := strings.TrimSpace(scanner.Text())
		if val != "" {
			return val
		}
	}
	return defaultVal
}

// promptYN asks a yes/no question. Returns the default on empty input.
func promptYN(scanner *bufio.Scanner, output io.Writer, prompt string, defaultYes bool) bool {
	hint := "y/N"
	if defaultYes {
		hint = "Y/n"
	}
	fmt.Fprintf(output, "%s [%s]: ", prompt, hint)
	if scanner.Scan() {
		val := strings.TrimSpace(strings.ToLower(scanner.Text()))
		if val == "y" || val == "yes" {
			return true
		}
		if val == "n" || val == "no" {
			return false
		}
	}
	return defaultYes
}

// promptSelect shows numbered options and returns the value at the chosen index.
// Input "1" returns options[0], "2" returns options[1], etc.
// Returns options[0] on empty/invalid input.
func promptSelect(scanner *bufio.Scanner, output io.Writer, prompt string, options []string) string {
	fmt.Fprint(output, prompt)
	if scanner.Scan() {
		val := strings.TrimSpace(scanner.Text())
		switch val {
		case "1":
			if len(options) > 0 {
				return options[0]
			}
		case "2":
			if len(options) > 1 {
				return options[1]
			}
		case "3":
			if len(options) > 2 {
				return options[2]
			}
		}
	}
	if len(options) > 0 {
		return options[0]
	}
	return ""
}

// promptSecret shows a prompt and reads input without echoing to the terminal.
// Falls back to scanner-based input when stdin is not a terminal (e.g., in tests).
func promptSecret(scanner *bufio.Scanner, input io.Reader, output io.Writer, prompt string) string {
	fmt.Fprintf(output, "%s: ", prompt)

	// Try no-echo input if stdin is a terminal
	if f, ok := input.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		pw, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(output) // newline after hidden input
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(pw))
	}

	// Fallback for tests/pipes
	if scanner.Scan() {
		return strings.TrimSpace(scanner.Text())
	}
	return ""
}
