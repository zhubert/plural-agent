package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"github.com/zhubert/erg/internal/cli"
	"github.com/zhubert/erg/internal/workflow"
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

func runConfigureWithIO(input io.Reader, output io.Writer, checker prereqCheckerFn, repoPath string, writer workflowWriterFn, accessible bool) error {
	// Phase 1: Prerequisites (fmt-based output with ✓/✗/○)
	if !checkPrereqs(output, checker) {
		return nil
	}

	// Clear prereq output so the form starts on a clean screen.
	fmt.Fprint(output, "\033[2J\033[H")

	// Phase 2: Tracker selection
	var provider string
	trackerForm := newForm([]*huh.Group{
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Which issue tracker would you like to use?").
				Options(
					huh.NewOption("GitHub Issues  (uses gh CLI — no extra credentials needed)", "github"),
					huh.NewOption("Asana Tasks    (requires ASANA_PAT environment variable)", "asana"),
					huh.NewOption("Linear Issues  (requires LINEAR_API_KEY environment variable)", "linear"),
				).
				Value(&provider),
		),
	}, input, output, accessible)
	if err := trackerForm.Run(); err != nil {
		return err
	}

	// Phase 3: Provider setup + source config
	cfg := workflow.WizardConfig{
		Provider:    provider,
		Label:       "queued",
		FixCI:       true,
		AutoReview:  true,
		MergeMethod: "rebase",
	}
	if err := runProviderForm(input, output, accessible, provider, &cfg); err != nil {
		return err
	}

	// Phase 4: Workflow behavior
	if err := runBehaviorForm(input, output, accessible, &cfg); err != nil {
		return err
	}

	// Phase 5: Summary + confirm
	var confirmed bool
	summaryForm := newForm([]*huh.Group{
		huh.NewGroup(
			huh.NewNote().
				Title("Configuration Summary").
				Description(buildSummaryText(cfg)),
			huh.NewConfirm().
				Title("Write configuration?").
				WithButtonAlignment(lipgloss.Left).
				Value(&confirmed),
		),
	}, input, output, accessible)
	if err := summaryForm.Run(); err != nil {
		return err
	}
	if !confirmed {
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

// runProviderForm handles phase 3: provider setup instructions + source config.
func runProviderForm(input io.Reader, output io.Writer, accessible bool, provider string, cfg *workflow.WizardConfig) error {
	// Show setup instructions via Note
	setupForm := newForm([]*huh.Group{
		huh.NewGroup(
			huh.NewNote().
				Title(providerSetupTitle(provider)).
				Description(buildProviderSetupText(provider)),
		),
	}, input, output, accessible)
	if err := setupForm.Run(); err != nil {
		return err
	}

	switch provider {
	case "github":
		f := newForm([]*huh.Group{
			huh.NewGroup(
				huh.NewInput().
					Title("Label to watch for new issues?").
					Value(&cfg.Label),
			),
		}, input, output, accessible)
		return f.Run()

	case "asana":
		return runAsanaForm(input, output, accessible, cfg)

	case "linear":
		return runLinearForm(input, output, accessible, cfg)
	}
	return nil
}

// runAsanaForm collects Asana-specific configuration.
func runAsanaForm(input io.Reader, output io.Writer, accessible bool, cfg *workflow.WizardConfig) error {
	var orgChoice string = "tags"
	f := newForm([]*huh.Group{
		huh.NewGroup(
			huh.NewInput().
				Title("Asana project GID (from URL: https://app.asana.com/0/GID/list)").
				Value(&cfg.Project),
			huh.NewSelect[string]().
				Title("How do you organize work in Asana?").
				Options(
					huh.NewOption("By tags (label tasks with a tag like \"queued\")", "tags"),
					huh.NewOption("By board sections (Kanban)", "kanban"),
				).
				Value(&orgChoice),
		),
	}, input, output, accessible)
	if err := f.Run(); err != nil {
		return err
	}

	if orgChoice == "kanban" {
		cfg.Kanban = true
		cfg.Label = ""
		cfg.Section = "To do"
		cfg.CompletionSection = "Done"
		kf := newForm([]*huh.Group{
			huh.NewGroup(
				huh.NewInput().
					Title("Which section has new tasks?").
					Value(&cfg.Section),
				huh.NewInput().
					Title("Completion section?").
					Value(&cfg.CompletionSection),
			),
		}, input, output, accessible)
		return kf.Run()
	}

	// Tags mode
	tf := newForm([]*huh.Group{
		huh.NewGroup(
			huh.NewInput().
				Title("Asana tag to watch for new tasks?").
				Value(&cfg.Label),
			huh.NewInput().
				Title("Filter to section? (press Enter to skip)").
				Value(&cfg.Section),
			huh.NewInput().
				Title("Move completed tasks to which section? (press Enter to skip)").
				Value(&cfg.CompletionSection),
		),
	}, input, output, accessible)
	return tf.Run()
}

// runLinearForm collects Linear-specific configuration.
func runLinearForm(input io.Reader, output io.Writer, accessible bool, cfg *workflow.WizardConfig) error {
	var orgChoice string = "labels"
	f := newForm([]*huh.Group{
		huh.NewGroup(
			huh.NewInput().
				Title("Linear team ID (from Settings → API)").
				Value(&cfg.Team),
			huh.NewSelect[string]().
				Title("How do you organize work in Linear?").
				Options(
					huh.NewOption("By labels (label issues with \"queued\")", "labels"),
					huh.NewOption("By workflow states (Kanban)", "kanban"),
				).
				Value(&orgChoice),
		),
	}, input, output, accessible)
	if err := f.Run(); err != nil {
		return err
	}

	if orgChoice == "kanban" {
		cfg.Kanban = true
		cfg.CompletionState = "Done"
		kf := newForm([]*huh.Group{
			huh.NewGroup(
				huh.NewInput().
					Title("Label to identify erg-managed issues?").
					Value(&cfg.Label),
				huh.NewInput().
					Title("Completion state?").
					Value(&cfg.CompletionState),
			),
		}, input, output, accessible)
		return kf.Run()
	}

	// Labels mode
	lf := newForm([]*huh.Group{
		huh.NewGroup(
			huh.NewInput().
				Title("Linear label to watch for new issues?").
				Value(&cfg.Label),
			huh.NewInput().
				Title("Move completed issues to which state? (press Enter to skip)").
				Value(&cfg.CompletionState),
		),
	}, input, output, accessible)
	return lf.Run()
}

// runBehaviorForm handles phase 4: workflow behavior options.
func runBehaviorForm(input io.Reader, output io.Writer, accessible bool, cfg *workflow.WizardConfig) error {
	f := newForm([]*huh.Group{
		huh.NewGroup(
			huh.NewConfirm().
				Title("Should Claude plan the approach before coding?").
				DescriptionFunc(func() string {
					if cfg.PlanFirst {
						return "Erg adds a planning step where Claude outlines its approach before writing code."
					}
					return ""
				}, &cfg.PlanFirst).
				WithButtonAlignment(lipgloss.Left).
				Value(&cfg.PlanFirst),
			huh.NewConfirm().
				Title("Should Claude try to fix failing CI?").
				DescriptionFunc(func() string {
					if cfg.FixCI {
						return "Erg will re-run Claude to fix code when CI checks fail."
					}
					return ""
				}, &cfg.FixCI).
				WithButtonAlignment(lipgloss.Left).
				Value(&cfg.FixCI),
			huh.NewConfirm().
				Title("Should Claude auto-address PR review comments?").
				DescriptionFunc(func() string {
					if cfg.AutoReview {
						return "Erg will re-run Claude to address reviewer feedback with follow-up commits."
					}
					return ""
				}, &cfg.AutoReview).
				WithButtonAlignment(lipgloss.Left).
				Value(&cfg.AutoReview),
		).Title("Claude Behavior"),
		huh.NewGroup(
			huh.NewInput().
				Title("GitHub username to request as reviewer (press Enter to skip)").
				Value(&cfg.Reviewer),
			huh.NewSelect[string]().
				Title("Merge method").
				Options(
					huh.NewOption("Rebase", "rebase"),
					huh.NewOption("Squash", "squash"),
					huh.NewOption("Merge", "merge"),
				).
				Value(&cfg.MergeMethod),
		).Title("Pull Requests"),
		huh.NewGroup(
			huh.NewConfirm().
				Title("Run sessions in Docker containers?").
				DescriptionFunc(func() string {
					if cfg.Containerized {
						return "Erg will run each Claude session in an isolated Docker container."
					}
					return ""
				}, &cfg.Containerized).
				WithButtonAlignment(lipgloss.Left).
				Value(&cfg.Containerized),
			huh.NewConfirm().
				Title("Send Slack notifications on failure?").
				DescriptionFunc(func() string {
					if cfg.NotifySlack {
						return "Erg will send a Slack message when a session fails or needs attention."
					}
					return ""
				}, &cfg.NotifySlack).
				WithButtonAlignment(lipgloss.Left).
				Value(&cfg.NotifySlack),
		).Title("Infrastructure"),
	}, input, output, accessible)
	if err := f.Run(); err != nil {
		return err
	}

	// Conditionally ask for Slack webhook
	if cfg.NotifySlack {
		sf := newForm([]*huh.Group{
			huh.NewGroup(
				huh.NewInput().
					Title("Slack webhook URL or env var (e.g., $SLACK_WEBHOOK_URL)").
					Value(&cfg.SlackWebhook),
			),
		}, input, output, accessible)
		if err := sf.Run(); err != nil {
			return err
		}
		if cfg.SlackWebhook == "" {
			cfg.NotifySlack = false
		}
	}

	return nil
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
		b.WriteString("Add the token to your shell profile (~/.zshrc or ~/.bashrc):\n\n")
		b.WriteString("  export ASANA_PAT=\"your-token-here\"\n\n")
		b.WriteString("  # Reload your shell:\n")
		b.WriteString("  source ~/.zshrc\n\n")
		b.WriteString("Find your project GID in the Asana project URL:\n")
		b.WriteString("  https://app.asana.com/0/PROJECT_GID/list")
	case "linear":
		b.WriteString("To use Linear Issues, you need an API key.\n\n")
		b.WriteString("Steps to get your API key:\n\n")
		b.WriteString("  1. Log in at https://linear.app\n")
		b.WriteString("  2. Go to Settings → API → Personal API Keys\n")
		b.WriteString("  3. Click \"New API key\", give it a name (e.g., \"erg\"), and copy the key\n\n")
		b.WriteString("Add the key to your shell profile (~/.zshrc or ~/.bashrc):\n\n")
		b.WriteString("  export LINEAR_API_KEY=\"your-key-here\"\n\n")
		b.WriteString("  # Reload your shell:\n")
		b.WriteString("  source ~/.zshrc\n\n")
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
	b.WriteString(fmt.Sprintf("Fix CI:         %v\n", cfg.FixCI))
	b.WriteString(fmt.Sprintf("Auto review:    %v\n", cfg.AutoReview))
	if cfg.Reviewer != "" {
		b.WriteString(fmt.Sprintf("Reviewer:       %s\n", cfg.Reviewer))
	}
	b.WriteString(fmt.Sprintf("Merge method:   %s\n", cfg.MergeMethod))
	b.WriteString(fmt.Sprintf("Containerized:  %v\n", cfg.Containerized))
	if cfg.NotifySlack {
		b.WriteString(fmt.Sprintf("Slack webhook:  %s\n", cfg.SlackWebhook))
	}
	return b.String()
}

// newForm creates a huh.Form configured with the given IO and accessible mode.
func newForm(groups []*huh.Group, input io.Reader, output io.Writer, accessible bool) *huh.Form {
	return huh.NewForm(groups...).
		WithInput(input).
		WithOutput(output).
		WithAccessible(accessible)
}
