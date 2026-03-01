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

	// Board-based workflow (kanban)
	Kanban bool // Board-based workflow (section/state transitions)

	// Notifications
	NotifySlack  bool   // Send Slack notifications on failure
	SlackWebhook string // Slack webhook URL or $ENV_VAR (e.g., $SLACK_WEBHOOK_URL)

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
	ciFailNext := "notify_failed"
	if cfg.FixCI {
		ciFailNext = "fix_ci"
	}

	// Resolve merge method
	mergeMethod := cfg.MergeMethod
	if mergeMethod != "squash" && mergeMethod != "merge" {
		mergeMethod = "rebase"
	}

	// Resolve the state after open_pr (kanban adds move_to_in_review)
	afterOpenPR := "await_ci"
	if cfg.Kanban && (cfg.Provider == "asana" || cfg.Provider == "linear") {
		afterOpenPR = "move_to_in_review"
	}

	// Resolve notify_failed next target
	notifyFailedNext := "failed"
	if cfg.NotifySlack {
		notifyFailedNext = "notify_slack"
	}

	// Resolve the next state after plan feedback check approves coding
	afterPlanApproved := "coding"
	if cfg.Kanban && cfg.PlanFirst && (cfg.Provider == "asana" || cfg.Provider == "linear") {
		afterPlanApproved = "move_to_planned"
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
			// Kanban uses section as source filter, no label
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

	// ─── Phase 1: Planning ───

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
		b.WriteString("    error: notify_failed\n")
		b.WriteByte('\n')

		b.WriteString("  await_plan_feedback:\n")
		b.WriteString("    type: wait\n")
		b.WriteString("    event: plan.user_replied\n")
		if cfg.Kanban {
			b.WriteString("    timeout: 7d\n")
		} else {
			b.WriteString("    timeout: 72h\n")
		}
		b.WriteString("    timeout_next: plan_expired\n")
		b.WriteString("    params:\n")
		b.WriteString("      approval_pattern: '(?i)(LGTM|looks good|approved?|proceed|go ahead|ship it)'\n")
		b.WriteString("    next: check_plan_feedback\n")
		b.WriteString("    error: notify_failed\n")
		b.WriteByte('\n')

		b.WriteString("  check_plan_feedback:\n")
		b.WriteString("    type: choice\n")
		b.WriteString("    choices:\n")
		b.WriteString("      - variable: plan_approved\n")
		b.WriteString("        equals: true\n")
		fmt.Fprintf(&b, "        next: %s\n", afterPlanApproved)
		b.WriteString("      - variable: plan_approved\n")
		b.WriteString("        equals: false\n")
		b.WriteString("        next: planning\n")
		b.WriteString("    default: notify_failed\n")
		b.WriteByte('\n')

		// Kanban: move to planned section/state after plan approved
		if cfg.Kanban {
			switch cfg.Provider {
			case "asana":
				b.WriteString("  move_to_planned:\n")
				b.WriteString("    type: task\n")
				b.WriteString("    action: asana.move_to_section\n")
				b.WriteString("    params:\n")
				b.WriteString("      section: \"Planned\"\n")
				b.WriteString("    next: await_doing\n")
				b.WriteString("    error: notify_failed\n")
				b.WriteByte('\n')

				b.WriteString("  await_doing:\n")
				b.WriteString("    type: wait\n")
				b.WriteString("    event: asana.in_section\n")
				b.WriteString("    params:\n")
				b.WriteString("      section: \"Doing\"\n")
				b.WriteString("    timeout: 7d\n")
				b.WriteString("    timeout_next: plan_expired\n")
				b.WriteString("    next: coding\n")
				b.WriteString("    error: notify_failed\n")
				b.WriteByte('\n')

			case "linear":
				b.WriteString("  move_to_planned:\n")
				b.WriteString("    type: task\n")
				b.WriteString("    action: linear.move_to_state\n")
				b.WriteString("    params:\n")
				b.WriteString("      state: \"Planned\"\n")
				b.WriteString("    next: await_in_progress\n")
				b.WriteString("    error: notify_failed\n")
				b.WriteByte('\n')

				b.WriteString("  await_in_progress:\n")
				b.WriteString("    type: wait\n")
				b.WriteString("    event: linear.in_state\n")
				b.WriteString("    params:\n")
				b.WriteString("      state: \"In Progress\"\n")
				b.WriteString("    timeout: 7d\n")
				b.WriteString("    timeout_next: plan_expired\n")
				b.WriteString("    next: coding\n")
				b.WriteString("    error: notify_failed\n")
				b.WriteByte('\n')
			}
		}

		// plan_expired state
		commentAct := commentAction(cfg.Provider)
		b.WriteString("  plan_expired:\n")
		b.WriteString("    type: task\n")
		fmt.Fprintf(&b, "    action: %s\n", commentAct)
		b.WriteString("    params:\n")
		if cfg.Kanban {
			b.WriteString("      body: \"Plan has been waiting for 7 days with no action. Moving to failed.\"\n")
		} else {
			b.WriteString("      body: \"Plan has been awaiting feedback for 72 hours. Moving to failed.\"\n")
		}
		b.WriteString("    next: notify_failed\n")
		b.WriteString("    error: notify_failed\n")
		b.WriteByte('\n')
	}

	// ─── Phase 2: Coding ───

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
	b.WriteString("    error: notify_failed\n")
	b.WriteByte('\n')

	b.WriteString("  open_pr:\n")
	b.WriteString("    type: task\n")
	b.WriteString("    action: github.create_pr\n")
	b.WriteString("    params:\n")
	b.WriteString("      link_issue: true\n")
	fmt.Fprintf(&b, "    next: %s\n", afterOpenPR)
	b.WriteString("    error: notify_failed\n")
	b.WriteByte('\n')

	// Kanban: move to "In Review" section/state after PR opened
	if cfg.Kanban {
		switch cfg.Provider {
		case "asana":
			b.WriteString("  move_to_in_review:\n")
			b.WriteString("    type: task\n")
			b.WriteString("    action: asana.move_to_section\n")
			b.WriteString("    params:\n")
			b.WriteString("      section: \"In Review\"\n")
			b.WriteString("    next: await_ci\n")
			b.WriteString("    error: notify_failed\n")
			b.WriteByte('\n')
		case "linear":
			b.WriteString("  move_to_in_review:\n")
			b.WriteString("    type: task\n")
			b.WriteString("    action: linear.move_to_state\n")
			b.WriteString("    params:\n")
			b.WriteString("      state: \"In Review\"\n")
			b.WriteString("    next: await_ci\n")
			b.WriteString("    error: notify_failed\n")
			b.WriteByte('\n')
		}
	}

	// ─── Phase 3: CI ───

	b.WriteString("  await_ci:\n")
	b.WriteString("    type: wait\n")
	b.WriteString("    event: ci.complete\n")
	b.WriteString("    timeout: 2h\n")
	b.WriteString("    timeout_next: ci_timed_out\n")
	b.WriteString("    params:\n")
	b.WriteString("      on_failure: fix\n")
	b.WriteString("    next: check_ci\n")
	b.WriteString("    error: notify_failed\n")
	b.WriteByte('\n')

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
	b.WriteString("    default: notify_failed\n")
	b.WriteByte('\n')

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
	b.WriteString("    error: notify_failed\n")
	b.WriteByte('\n')

	b.WriteString("  push_conflict_fix:\n")
	b.WriteString("    type: task\n")
	b.WriteString("    action: github.push\n")
	b.WriteString("    next: await_ci\n")
	b.WriteString("    error: notify_failed\n")
	b.WriteByte('\n')

	if cfg.FixCI {
		b.WriteString("  fix_ci:\n")
		b.WriteString("    type: task\n")
		b.WriteString("    action: ai.fix_ci\n")
		b.WriteString("    params:\n")
		b.WriteString("      max_ci_fix_rounds: 3\n")
		b.WriteString("    next: push_ci_fix\n")
		b.WriteString("    error: ci_unfixable\n")
		b.WriteByte('\n')

		b.WriteString("  push_ci_fix:\n")
		b.WriteString("    type: task\n")
		b.WriteString("    action: github.push\n")
		b.WriteString("    next: await_ci\n")
		b.WriteString("    error: notify_failed\n")
		b.WriteByte('\n')

		b.WriteString("  ci_unfixable:\n")
		b.WriteString("    type: task\n")
		b.WriteString("    action: github.comment_pr\n")
		b.WriteString("    params:\n")
		b.WriteString("      body: \"CI fix exhausted after 3 rounds. Manual intervention required.\"\n")
		b.WriteString("    next: notify_failed\n")
		b.WriteString("    error: notify_failed\n")
		b.WriteByte('\n')
	}

	b.WriteString("  ci_timed_out:\n")
	b.WriteString("    type: task\n")
	b.WriteString("    action: github.comment_pr\n")
	b.WriteString("    params:\n")
	b.WriteString("      body: \"CI has been running for over 2 hours. Manual intervention required.\"\n")
	b.WriteString("    next: notify_failed\n")
	b.WriteString("    error: notify_failed\n")
	b.WriteByte('\n')

	// ─── Phase 4: Review ───

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

	b.WriteString("  await_review:\n")
	b.WriteString("    type: wait\n")
	b.WriteString("    event: pr.reviewed\n")
	b.WriteString("    timeout: 48h\n")
	b.WriteString("    timeout_next: review_overdue\n")
	b.WriteString("    params:\n")
	if cfg.AutoReview {
		b.WriteString("      auto_address: true\n")
		b.WriteString("      max_feedback_rounds: 3\n")
	} else {
		b.WriteString("      auto_address: false\n")
	}
	b.WriteString("    next: check_review_result\n")
	b.WriteString("    error: notify_failed\n")
	b.WriteByte('\n')

	b.WriteString("  check_review_result:\n")
	b.WriteString("    type: choice\n")
	b.WriteString("    choices:\n")
	b.WriteString("      - variable: review_approved\n")
	b.WriteString("        equals: true\n")
	b.WriteString("        next: merge\n")
	b.WriteString("      - variable: changes_requested\n")
	b.WriteString("        equals: true\n")
	b.WriteString("        next: address_review\n")
	b.WriteString("      - variable: pr_merged_externally\n")
	b.WriteString("        equals: true\n")
	fmt.Fprintf(&b, "        next: %s\n", afterMerge)
	b.WriteString("    default: notify_failed\n")
	b.WriteByte('\n')

	b.WriteString("  address_review:\n")
	b.WriteString("    type: task\n")
	b.WriteString("    action: ai.address_review\n")
	b.WriteString("    params:\n")
	b.WriteString("      max_feedback_rounds: 3\n")
	b.WriteString("    next: push_review_fix\n")
	b.WriteString("    error: notify_failed\n")
	b.WriteByte('\n')

	b.WriteString("  push_review_fix:\n")
	b.WriteString("    type: task\n")
	b.WriteString("    action: github.push\n")
	b.WriteString("    next: await_review\n")
	b.WriteString("    error: notify_failed\n")
	b.WriteByte('\n')

	b.WriteString("  review_overdue:\n")
	b.WriteString("    type: task\n")
	b.WriteString("    action: github.comment_pr\n")
	b.WriteString("    params:\n")
	b.WriteString("      body: \"PR has been awaiting review for 48 hours. Manual intervention required.\"\n")
	b.WriteString("    next: notify_failed\n")
	b.WriteString("    error: notify_failed\n")
	b.WriteByte('\n')

	// ─── Phase 5: Merge ───

	b.WriteString("  merge:\n")
	b.WriteString("    type: task\n")
	b.WriteString("    action: github.merge\n")
	b.WriteString("    params:\n")
	fmt.Fprintf(&b, "      method: %s\n", mergeMethod)
	b.WriteString("      cleanup: true\n")
	fmt.Fprintf(&b, "    next: %s\n", afterMerge)
	b.WriteString("    error: rebase\n")
	b.WriteByte('\n')

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

	// ─── Phase 6: Notification + Terminal ───

	commentAct := commentAction(cfg.Provider)
	b.WriteString("  notify_failed:\n")
	b.WriteString("    type: task\n")
	fmt.Fprintf(&b, "    action: %s\n", commentAct)
	b.WriteString("    params:\n")
	b.WriteString("      body: \"Task failed. Manual intervention required.\"\n")
	fmt.Fprintf(&b, "    next: %s\n", notifyFailedNext)
	fmt.Fprintf(&b, "    error: %s\n", notifyFailedNext)
	b.WriteByte('\n')

	if cfg.NotifySlack {
		b.WriteString("  notify_slack:\n")
		b.WriteString("    type: task\n")
		b.WriteString("    action: slack.notify\n")
		b.WriteString("    params:\n")
		fmt.Fprintf(&b, "      webhook_url: %s\n", cfg.SlackWebhook)
		b.WriteString("      message: \"Task {{.IssueID}}: {{.Title}} failed — manual intervention required\"\n")
		b.WriteString("    next: failed\n")
		b.WriteString("    error: failed\n")
		b.WriteByte('\n')
	}

	b.WriteString("  done:\n")
	b.WriteString("    type: succeed\n")
	b.WriteByte('\n')
	b.WriteString("  failed:\n")
	b.WriteString("    type: fail\n")

	return b.String()
}
