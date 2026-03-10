package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WizardConfig holds the answers collected during the interactive workflow wizard.
type WizardConfig struct {
	// Source configuration
	Provider string // "github", "asana", "linear"
	Label    string // issue label/tag to watch
	Project  string // Asana project GID (required for asana)
	Team     string // Linear team ID (required for linear)
	Section  string // Asana board section filter — watch only tasks in this section (optional)

	// Workflow behavior
	PlanFirst     bool   // Add planning stage before coding (human reviews plan before work starts)
	AutoMerge     bool   // Auto-merge approved PRs
	MergeMethod   string // "rebase", "squash", or "merge" (used when AutoMerge=true)
	Reviewer      string // GitHub username to request review from (empty = skip)
	Containerized bool   // Run sessions in Docker containers

	// Board-based workflow (kanban)
	Kanban bool // Board-based workflow (section/state transitions)

	// Completion tracking (provider-specific, both optional)
	CompletionSection string // Asana: move to this section when work is done
	CompletionState   string // Linear: move to this state when work is done
}

// commentAction returns the provider-specific comment action string.
func commentAction(provider string) string {
	switch provider {
	case "asana":
		return "asana.comment"
	case "linear":
		return "linear.comment"
	default:
		return "github.comment_issue"
	}
}

// hasCompletionTracking returns true if the config has completion tracking configured.
func hasCompletionTracking(cfg WizardConfig) bool {
	return (cfg.Provider == "asana" && cfg.CompletionSection != "") ||
		(cfg.Provider == "linear" && cfg.CompletionState != "")
}

// WriteFromWizard generates a workflow.yaml from the wizard config and writes it
// to repoPath/.erg/workflow.yaml. Returns the path written and any error.
func WriteFromWizard(repoPath string, cfg WizardConfig) (string, error) {
	dir := filepath.Join(repoPath, workflowDir)
	fp := filepath.Join(dir, workflowFileName)

	if _, err := os.Stat(fp); err == nil {
		return fp, fmt.Errorf("%s already exists", fp)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fp, fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	content := GenerateWizardYAML(cfg)
	if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
		return fp, fmt.Errorf("failed to write %s: %w", fp, err)
	}

	return fp, nil
}

// GenerateWizardYAML produces a complete workflow.yaml string tailored to the wizard config.
// Core phases (plan, code, pr, ci, review, merge) are expressed as builtin template references.
// The generated YAML can be loaded by workflow.Load and passes workflow.Validate.
func GenerateWizardYAML(cfg WizardConfig) string {
	var b strings.Builder

	// Resolve workflow name and start state
	workflowName := "ci-gate"
	startState := "code_phase"
	if cfg.PlanFirst {
		workflowName = "plan-then-code"
		startState = "plan_phase"
	}

	// Resolve merge method
	mergeMethod := cfg.MergeMethod
	if mergeMethod != "squash" && mergeMethod != "merge" {
		mergeMethod = "rebase"
	}

	// Resolve exit targets for each phase
	afterPlanApproved := "code_phase"
	if cfg.Kanban && cfg.PlanFirst {
		switch cfg.Provider {
		case "asana":
			afterPlanApproved = "move_to_doing"
		case "linear":
			afterPlanApproved = "move_to_in_progress"
		}
	}

	afterPR := "ci_phase"
	if cfg.Kanban && (cfg.Provider == "asana" || cfg.Provider == "linear") {
		afterPR = "move_to_in_review"
	}

	afterCI := "review_phase"
	if cfg.Reviewer != "" {
		afterCI = "request_reviewer"
	}

	afterReview := "done"
	if cfg.AutoMerge {
		afterReview = "merge_phase"
	} else if hasCompletionTracking(cfg) {
		afterReview = "move_complete"
	}

	afterMerge := "done"
	if hasCompletionTracking(cfg) {
		afterMerge = "move_complete"
	}

	// Header
	fmt.Fprintf(&b, "workflow: %s\n", workflowName)
	fmt.Fprintf(&b, "start: %s\n", startState)
	b.WriteByte('\n')

	// Source
	b.WriteString("source:\n")
	fmt.Fprintf(&b, "  provider: %s\n", cfg.Provider)
	b.WriteString("  filter:\n")
	switch cfg.Provider {
	case "asana":
		fmt.Fprintf(&b, "    project: %q\n", cfg.Project)
		if cfg.Kanban {
			fmt.Fprintf(&b, "    section: %q\n", cfg.Section)
		} else {
			fmt.Fprintf(&b, "    label: %s\n", cfg.Label)
			if cfg.Section != "" {
				fmt.Fprintf(&b, "    section: %q\n", cfg.Section)
			}
		}
	case "linear":
		fmt.Fprintf(&b, "    team: %q\n", cfg.Team)
		fmt.Fprintf(&b, "    label: %s\n", cfg.Label)
	default: // github
		fmt.Fprintf(&b, "    label: %s\n", cfg.Label)
	}
	b.WriteByte('\n')

	b.WriteString("states:\n")

	// ─── Phase 1: Planning (optional) ───

	if cfg.PlanFirst {
		b.WriteString("  plan_phase:\n")
		b.WriteString("    type: template\n")
		b.WriteString("    use: builtin:plan\n")
		b.WriteString("    params:\n")
		if cfg.Containerized {
			b.WriteString("      containerized: true\n")
		} else {
			b.WriteString("      containerized: false\n")
		}
		b.WriteString("    exits:\n")
		fmt.Fprintf(&b, "      success: %s\n", afterPlanApproved)
		b.WriteString("      failure: notify_failed\n")
		b.WriteByte('\n')

		// Kanban: move to doing section/state after plan approved, then start coding
		if cfg.Kanban {
			switch cfg.Provider {
			case "asana":
				b.WriteString("  move_to_doing:\n")
				b.WriteString("    type: template\n")
				b.WriteString("    use: builtin:asana_move_section\n")
				b.WriteString("    params:\n")
				b.WriteString("      section: \"Doing\"\n")
				b.WriteString("    exits:\n")
				b.WriteString("      success: code_phase\n")
				b.WriteString("      failure: notify_failed\n")
				b.WriteByte('\n')

			case "linear":
				b.WriteString("  move_to_in_progress:\n")
				b.WriteString("    type: template\n")
				b.WriteString("    use: builtin:linear_move_state\n")
				b.WriteString("    params:\n")
				b.WriteString("      state: \"In Progress\"\n")
				b.WriteString("    exits:\n")
				b.WriteString("      success: code_phase\n")
				b.WriteString("      failure: notify_failed\n")
				b.WriteByte('\n')
			}
		}
	}

	// ─── Phase 2: Coding ───

	b.WriteString("  code_phase:\n")
	b.WriteString("    type: template\n")
	b.WriteString("    use: builtin:code\n")
	b.WriteString("    params:\n")
	if cfg.Containerized {
		b.WriteString("      containerized: true\n")
	} else {
		b.WriteString("      containerized: false\n")
	}
	b.WriteString("    exits:\n")
	b.WriteString("      success: pr_phase\n")
	b.WriteString("      failure: notify_failed\n")
	b.WriteByte('\n')

	// ─── Phase 3: PR ───

	b.WriteString("  pr_phase:\n")
	b.WriteString("    type: template\n")
	b.WriteString("    use: builtin:pr\n")
	b.WriteString("    exits:\n")
	fmt.Fprintf(&b, "      success: %s\n", afterPR)
	b.WriteString("      failure: notify_failed\n")
	b.WriteByte('\n')

	// Kanban: move to "In Review" after PR opened
	if cfg.Kanban {
		switch cfg.Provider {
		case "asana":
			b.WriteString("  move_to_in_review:\n")
			b.WriteString("    type: template\n")
			b.WriteString("    use: builtin:asana_move_section\n")
			b.WriteString("    params:\n")
			b.WriteString("      section: \"In Review\"\n")
			b.WriteString("    exits:\n")
			b.WriteString("      success: ci_phase\n")
			b.WriteString("      failure: notify_failed\n")
			b.WriteByte('\n')
		case "linear":
			b.WriteString("  move_to_in_review:\n")
			b.WriteString("    type: template\n")
			b.WriteString("    use: builtin:linear_move_state\n")
			b.WriteString("    params:\n")
			b.WriteString("      state: \"In Review\"\n")
			b.WriteString("    exits:\n")
			b.WriteString("      success: ci_phase\n")
			b.WriteString("      failure: notify_failed\n")
			b.WriteByte('\n')
		}
	}

	// ─── Phase 4: CI ───

	b.WriteString("  ci_phase:\n")
	b.WriteString("    type: template\n")
	b.WriteString("    use: builtin:ci\n")
	b.WriteString("    exits:\n")
	fmt.Fprintf(&b, "      success: %s\n", afterCI)
	b.WriteString("      failure: notify_failed\n")
	b.WriteByte('\n')

	// Optional: request reviewer
	if cfg.Reviewer != "" {
		b.WriteString("  request_reviewer:\n")
		b.WriteString("    type: task\n")
		b.WriteString("    action: github.request_review\n")
		b.WriteString("    params:\n")
		fmt.Fprintf(&b, "      reviewer: %s\n", cfg.Reviewer)
		b.WriteString("    next: review_phase\n")
		b.WriteString("    error: review_phase\n")
		b.WriteByte('\n')
	}

	// ─── Phase 5: Review ───

	b.WriteString("  review_phase:\n")
	b.WriteString("    type: template\n")
	b.WriteString("    use: builtin:review\n")
	b.WriteString("    exits:\n")
	fmt.Fprintf(&b, "      success: %s\n", afterReview)
	b.WriteString("      failure: notify_failed\n")
	b.WriteByte('\n')

	// ─── Phase 6: Merge (optional) ───

	if cfg.AutoMerge {
		b.WriteString("  merge_phase:\n")
		b.WriteString("    type: template\n")
		b.WriteString("    use: builtin:merge\n")
		b.WriteString("    params:\n")
		fmt.Fprintf(&b, "      method: %s\n", mergeMethod)
		b.WriteString("    exits:\n")
		fmt.Fprintf(&b, "      success: %s\n", afterMerge)
		b.WriteString("      failure: notify_failed\n")
		b.WriteByte('\n')
	}

	// Optional: completion tracking
	if hasCompletionTracking(cfg) {
		b.WriteString("  move_complete:\n")
		b.WriteString("    type: template\n")
		switch cfg.Provider {
		case "asana":
			b.WriteString("    use: builtin:asana_move_section\n")
			b.WriteString("    params:\n")
			fmt.Fprintf(&b, "      section: %q\n", cfg.CompletionSection)
		case "linear":
			b.WriteString("    use: builtin:linear_move_state\n")
			b.WriteString("    params:\n")
			fmt.Fprintf(&b, "      state: %q\n", cfg.CompletionState)
		}
		b.WriteString("    exits:\n")
		b.WriteString("      success: done\n")
		b.WriteString("      failure: done\n")
		b.WriteByte('\n')
	}

	// ─── Inline terminal + error states ───

	commentAct := commentAction(cfg.Provider)
	b.WriteString("  notify_failed:\n")
	b.WriteString("    type: task\n")
	fmt.Fprintf(&b, "    action: %s\n", commentAct)
	b.WriteString("    params:\n")
	b.WriteString("      body: \"Task failed. Manual intervention required.\"\n")
	b.WriteString("    next: failed\n")
	b.WriteString("    error: failed\n")
	b.WriteByte('\n')

	b.WriteString("  done:\n")
	b.WriteString("    type: succeed\n")
	b.WriteByte('\n')
	b.WriteString("  failed:\n")
	b.WriteString("    type: fail\n")

	return b.String()
}
