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
	PlanFirst     bool   // Add ai.plan stage before coding (human reviews plan before work starts)
	FixCI         bool   // Add ai.fix_ci loop for failing CI
	AutoReview    bool   // Auto-address PR review comments
	Reviewer      string // GitHub username to request review from (empty = skip)
	MergeMethod   string // "rebase", "squash", or "merge"
	Containerized bool   // Run sessions in Docker containers

	// Completion tracking (provider-specific, both optional)
	CompletionSection string // Asana: move to this section when work is done
	CompletionState   string // Linear: move to this state when work is done
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
// The generated YAML can be loaded by workflow.Load and passes workflow.Validate.
func GenerateWizardYAML(cfg WizardConfig) string {
	var b strings.Builder

	// Resolve workflow name and start state
	workflowName := "ci-gate"
	startState := "coding"
	if cfg.PlanFirst {
		workflowName = "plan-then-code"
		startState = "planning"
	}

	// Resolve the state reached after CI passes
	afterCI := "await_review"
	if cfg.Reviewer != "" {
		afterCI = "request_reviewer"
	}

	// Resolve the state reached after merge
	afterMerge := "done"
	if cfg.Provider == "asana" && cfg.CompletionSection != "" {
		afterMerge = "move_complete"
	} else if cfg.Provider == "linear" && cfg.CompletionState != "" {
		afterMerge = "move_complete"
	}

	// Resolve CI failure routing
	ciFailNext := "failed"
	if cfg.FixCI {
		ciFailNext = "fix_ci"
	}

	// Resolve merge method
	mergeMethod := cfg.MergeMethod
	if mergeMethod != "squash" && mergeMethod != "merge" {
		mergeMethod = "rebase"
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
		fmt.Fprintf(&b, "    label: %s\n", cfg.Label)
		if cfg.Section != "" {
			fmt.Fprintf(&b, "    section: %q\n", cfg.Section)
		}
	case "linear":
		fmt.Fprintf(&b, "    team: %q\n", cfg.Team)
		fmt.Fprintf(&b, "    label: %s\n", cfg.Label)
	default: // github
		fmt.Fprintf(&b, "    label: %s\n", cfg.Label)
	}
	b.WriteByte('\n')

	b.WriteString("states:\n")

	// Planning states (when PlanFirst is enabled)
	if cfg.PlanFirst {
		b.WriteString("  planning:\n")
		b.WriteString("    type: task\n")
		b.WriteString("    action: ai.plan\n")
		b.WriteString("    params:\n")
		b.WriteString("      max_turns: 30\n")
		b.WriteString("      max_duration: 15m\n")
		if cfg.Containerized {
			b.WriteString("      containerized: true\n")
		}
		b.WriteString("    next: await_plan_feedback\n")
		b.WriteString("    error: failed\n")
		b.WriteByte('\n')

		b.WriteString("  await_plan_feedback:\n")
		b.WriteString("    type: wait\n")
		b.WriteString("    event: plan.user_replied\n")
		b.WriteString("    timeout: 72h\n")
		b.WriteString("    timeout_next: failed\n")
		b.WriteString("    params:\n")
		b.WriteString("      approval_pattern: '(?i)(LGTM|looks good|approved?|proceed|go ahead|ship it)'\n")
		b.WriteString("    next: check_plan_feedback\n")
		b.WriteString("    error: failed\n")
		b.WriteByte('\n')

		b.WriteString("  check_plan_feedback:\n")
		b.WriteString("    type: choice\n")
		b.WriteString("    choices:\n")
		b.WriteString("      - variable: plan_approved\n")
		b.WriteString("        equals: true\n")
		b.WriteString("        next: coding\n")
		b.WriteString("      - variable: plan_approved\n")
		b.WriteString("        equals: false\n")
		b.WriteString("        next: planning\n")
		b.WriteString("    default: failed\n")
		b.WriteByte('\n')
	}

	// Coding state
	b.WriteString("  coding:\n")
	b.WriteString("    type: task\n")
	b.WriteString("    action: ai.code\n")
	b.WriteString("    params:\n")
	b.WriteString("      max_turns: 50\n")
	b.WriteString("      max_duration: 30m\n")
	if cfg.Containerized {
		b.WriteString("      containerized: true\n")
	}
	b.WriteString("    next: open_pr\n")
	b.WriteString("    error: failed\n")
	b.WriteByte('\n')

	// Open PR
	b.WriteString("  open_pr:\n")
	b.WriteString("    type: task\n")
	b.WriteString("    action: github.create_pr\n")
	b.WriteString("    params:\n")
	b.WriteString("      link_issue: true\n")
	b.WriteString("    next: await_ci\n")
	b.WriteString("    error: failed\n")
	b.WriteByte('\n')

	// CI wait
	b.WriteString("  await_ci:\n")
	b.WriteString("    type: wait\n")
	b.WriteString("    event: ci.complete\n")
	b.WriteString("    timeout: 2h\n")
	b.WriteString("    timeout_next: failed\n")
	b.WriteString("    params:\n")
	b.WriteString("      on_failure: fix\n")
	b.WriteString("    next: check_ci\n")
	b.WriteString("    error: failed\n")
	b.WriteByte('\n')

	// CI routing choice
	b.WriteString("  check_ci:\n")
	b.WriteString("    type: choice\n")
	b.WriteString("    choices:\n")
	b.WriteString("      - variable: conflicting\n")
	b.WriteString("        equals: true\n")
	b.WriteString("        next: rebase\n")
	b.WriteString("      - variable: ci_passed\n")
	b.WriteString("        equals: true\n")
	fmt.Fprintf(&b, "        next: %s\n", afterCI)
	b.WriteString("      - variable: ci_failed\n")
	b.WriteString("        equals: true\n")
	fmt.Fprintf(&b, "        next: %s\n", ciFailNext)
	b.WriteString("    default: failed\n")
	b.WriteByte('\n')

	// Rebase states (always included — merge conflicts can happen regardless)
	b.WriteString("  rebase:\n")
	b.WriteString("    type: task\n")
	b.WriteString("    action: git.rebase\n")
	b.WriteString("    params:\n")
	b.WriteString("      max_rebase_rounds: 3\n")
	b.WriteString("    next: await_ci\n")
	b.WriteString("    error: resolve_conflicts\n")
	b.WriteByte('\n')

	b.WriteString("  resolve_conflicts:\n")
	b.WriteString("    type: task\n")
	b.WriteString("    action: ai.resolve_conflicts\n")
	b.WriteString("    params:\n")
	b.WriteString("      max_conflict_rounds: 3\n")
	b.WriteString("    next: push_conflict_fix\n")
	b.WriteString("    error: failed\n")
	b.WriteByte('\n')

	b.WriteString("  push_conflict_fix:\n")
	b.WriteString("    type: task\n")
	b.WriteString("    action: github.push\n")
	b.WriteString("    next: await_ci\n")
	b.WriteString("    error: failed\n")
	b.WriteByte('\n')

	// CI fix loop (optional)
	if cfg.FixCI {
		b.WriteString("  fix_ci:\n")
		b.WriteString("    type: task\n")
		b.WriteString("    action: ai.fix_ci\n")
		b.WriteString("    params:\n")
		b.WriteString("      max_ci_fix_rounds: 3\n")
		b.WriteString("    next: push_ci_fix\n")
		b.WriteString("    error: failed\n")
		b.WriteByte('\n')

		b.WriteString("  push_ci_fix:\n")
		b.WriteString("    type: task\n")
		b.WriteString("    action: github.push\n")
		b.WriteString("    next: await_ci\n")
		b.WriteString("    error: failed\n")
		b.WriteByte('\n')
	}

	// Request reviewer (optional)
	if cfg.Reviewer != "" {
		b.WriteString("  request_reviewer:\n")
		b.WriteString("    type: task\n")
		b.WriteString("    action: github.request_review\n")
		b.WriteString("    params:\n")
		fmt.Fprintf(&b, "      reviewer: %s\n", cfg.Reviewer)
		b.WriteString("    next: await_review\n")
		b.WriteString("    error: await_review\n")
		b.WriteByte('\n')
	}

	// Await review
	b.WriteString("  await_review:\n")
	b.WriteString("    type: wait\n")
	b.WriteString("    event: pr.reviewed\n")
	b.WriteString("    params:\n")
	if cfg.AutoReview {
		b.WriteString("      auto_address: true\n")
		b.WriteString("      max_feedback_rounds: 3\n")
	} else {
		b.WriteString("      auto_address: false\n")
	}
	b.WriteString("    next: merge\n")
	b.WriteString("    error: failed\n")
	b.WriteByte('\n')

	// Merge
	b.WriteString("  merge:\n")
	b.WriteString("    type: task\n")
	b.WriteString("    action: github.merge\n")
	b.WriteString("    params:\n")
	fmt.Fprintf(&b, "      method: %s\n", mergeMethod)
	b.WriteString("      cleanup: true\n")
	fmt.Fprintf(&b, "    next: %s\n", afterMerge)
	b.WriteString("    error: rebase\n")
	b.WriteByte('\n')

	// Completion tracking (optional, provider-specific)
	if afterMerge == "move_complete" {
		b.WriteString("  move_complete:\n")
		b.WriteString("    type: task\n")
		switch cfg.Provider {
		case "asana":
			b.WriteString("    action: asana.move_to_section\n")
			b.WriteString("    params:\n")
			fmt.Fprintf(&b, "      section: %q\n", cfg.CompletionSection)
		case "linear":
			b.WriteString("    action: linear.move_to_state\n")
			b.WriteString("    params:\n")
			fmt.Fprintf(&b, "      state: %q\n", cfg.CompletionState)
		}
		b.WriteString("    next: done\n")
		b.WriteString("    error: done\n")
		b.WriteByte('\n')
	}

	// Terminal states
	b.WriteString("  done:\n")
	b.WriteString("    type: succeed\n")
	b.WriteByte('\n')
	b.WriteString("  failed:\n")
	b.WriteString("    type: fail\n")

	return b.String()
}
