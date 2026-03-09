package workflow

import (
	"maps"
	"time"
)

// DefaultWorkflowConfig returns a Config with the default state graph:
//
//	coding → open_pr → await_ci → check_ci_result
//	  → conflicting=true: rebase → await_ci (loop, bounded by max_rebase_rounds)
//	    → rebase error: resolve_conflicts (Claude AI) → push_conflict_fix → await_ci
//	  → ci_passed=true:   await_review → check_review_result
//	    → review_approved=true:    merge → done
//	    → changes_requested=true: address_review → push_review_fix → await_review (loop)
//	  → ci_failed=true:   fix_ci → push_ci_fix → await_ci (loop)
//	  → fix_ci error (max rounds): → failed
//
// CI is checked before review to avoid wasting reviewer time on failing builds.
// The fix loop is bounded by max_ci_fix_rounds (default 3).
// Merge conflicts are first rebased mechanically (bounded by max_rebase_rounds, default 3).
// If rebase fails (real conflicts), Claude resolves them via ai.resolve_conflicts.
// Review feedback is addressed via ai.address_review (bounded by max_review_rounds, default 3).
func DefaultWorkflowConfig() *Config {
	return &Config{
		Workflow: "issue-to-merge",
		Start:    "coding",
		Source: SourceConfig{
			Provider: "github",
			Filter: FilterConfig{
				Label: "queued",
			},
		},
		States: map[string]*State{
			"coding": {
				Type:   StateTypeTask,
				Action: "ai.code",
				Params: map[string]any{
					"max_turns":     50,
					"max_duration":  "30m",
					"containerized": true,
				},
				Next:  "open_pr",
				Error: "failed",
			},
			"open_pr": {
				Type:   StateTypeTask,
				Action: "github.create_pr",
				Params: map[string]any{
					"link_issue": true,
				},
				Next:  "await_ci",
				Error: "failed",
				Retry: []RetryConfig{DefaultRetryConfig()},
			},
			"await_ci": {
				Type:    StateTypeWait,
				Event:   "ci.complete",
				Timeout: &Duration{2 * time.Hour},
				Params: map[string]any{
					"on_failure": "fix",
				},
				Next:        "check_ci_result",
				TimeoutNext: "failed",
				Error:       "failed",
			},
			"check_ci_result": {
				Type: StateTypeChoice,
				Choices: []ChoiceRule{
					{Variable: "conflicting", Equals: true, Next: "rebase"},
					{Variable: "ci_passed", Equals: true, Next: "await_review"},
					{Variable: "ci_failed", Equals: true, Next: "fix_ci"},
				},
				Default: "failed",
			},
			"rebase": {
				Type:   StateTypeTask,
				Action: "git.rebase",
				Params: map[string]any{
					"max_rebase_rounds": 3,
				},
				Next:  "await_ci",
				Error: "resolve_conflicts",
				Retry: []RetryConfig{DefaultRetryConfig()},
			},
			"resolve_conflicts": {
				Type:   StateTypeTask,
				Action: "ai.resolve_conflicts",
				Params: map[string]any{
					"max_conflict_rounds": 3,
				},
				Next:  "push_conflict_fix",
				Error: "failed",
			},
			"push_conflict_fix": {
				Type:   StateTypeTask,
				Action: "github.push",
				Next:   "await_ci",
				Error:  "failed",
				Retry:  []RetryConfig{DefaultRetryConfig()},
			},
			"fix_ci": {
				Type:   StateTypeTask,
				Action: "ai.fix_ci",
				Params: map[string]any{
					"max_ci_fix_rounds": 3,
				},
				Next:  "push_ci_fix",
				Error: "failed",
			},
			"push_ci_fix": {
				Type:   StateTypeTask,
				Action: "github.push",
				Next:   "await_ci",
				Error:  "failed",
				Retry:  []RetryConfig{DefaultRetryConfig()},
			},
			"await_review": {
				Type:  StateTypeWait,
				Event: "pr.reviewed",
				Params: map[string]any{
					"auto_address": false,
				},
				Next:  "check_review_result",
				Error: "failed",
			},
			"check_review_result": {
				Type: StateTypeChoice,
				Choices: []ChoiceRule{
					{Variable: "review_approved", Equals: true, Next: "merge"},
					{Variable: "changes_requested", Equals: true, Next: "address_review"},
					{Variable: "pr_merged_externally", Equals: true, Next: "done"},
				},
				Default: "failed",
			},
			"address_review": {
				Type:   StateTypeTask,
				Action: "ai.address_review",
				Params: map[string]any{
					"max_review_rounds": 3,
				},
				Next:  "push_review_fix",
				Error: "failed",
			},
			"push_review_fix": {
				Type:   StateTypeTask,
				Action: "github.push",
				Next:   "await_review",
				Error:  "failed",
				Retry:  []RetryConfig{DefaultRetryConfig()},
			},
			"merge": {
				Type:   StateTypeTask,
				Action: "github.merge",
				Params: map[string]any{
					"method":  "rebase",
					"cleanup": true,
				},
				Next:  "done",
				Error: "rebase",
				Retry: []RetryConfig{DefaultRetryConfig()},
			},
			"done": {
				Type: StateTypeSucceed,
			},
			"failed": {
				Type: StateTypeFail,
			},
		},
	}
}

// DefaultPlanningWorkflowConfig returns a Config with a plan-then-code state graph:
//
//	planning → await_plan_feedback → check_plan_feedback
//	  → plan_approved=true:  coding → open_pr → await_ci → ... (same as DefaultWorkflowConfig)
//	  → plan_approved=false: planning (re-plan loop with user feedback injected)
//
// The planning phase asks Claude to analyze the issue and post a structured plan
// as an issue comment. The workflow then waits for the user to reply. If the
// reply matches the approval_pattern ("LGTM", "looks good", etc.) it proceeds to
// coding. Any other reply is treated as feedback: Claude revises the plan and
// posts an updated comment, then waits again. The loop continues until approval.
func DefaultPlanningWorkflowConfig() *Config {
	cfg := DefaultWorkflowConfig()
	cfg.Workflow = "plan-then-code"
	cfg.Start = "planning"

	cfg.States["planning"] = &State{
		Type:   StateTypeTask,
		Action: "ai.plan",
		Params: map[string]any{
			"max_turns":     30,
			"max_duration":  "15m",
			"containerized": true,
		},
		Next:  "await_plan_feedback",
		Error: "failed",
	}
	cfg.States["await_plan_feedback"] = &State{
		Type:    StateTypeWait,
		Event:   "plan.user_replied",
		Timeout: &Duration{72 * time.Hour},
		Params: map[string]any{
			"approval_pattern": `(?i)(LGTM|looks good|approved?|proceed|go ahead|ship it)`,
		},
		Next:        "check_plan_feedback",
		TimeoutNext: "failed",
		Error:       "failed",
	}
	cfg.States["check_plan_feedback"] = &State{
		Type: StateTypeChoice,
		Choices: []ChoiceRule{
			{Variable: "plan_approved", Equals: true, Next: "coding"},
			{Variable: "plan_approved", Equals: false, Next: "planning"},
		},
		Default: "failed",
	}

	return cfg
}

// --- Modular builtin templates ---
//
// These are composable building blocks for workflows. Each encapsulates a
// self-contained phase with success/failure exits that callers wire to their
// own states.

// PlanTemplateConfig returns a template for the planning phase:
// planning → await feedback → check (approved → success, rejected → re-plan loop).
func PlanTemplateConfig() *TemplateConfig {
	return &TemplateConfig{
		Template: "plan",
		Entry:    "planning",
		Exits: map[string]string{
			"success": "plan_done",
			"failure": "plan_failed",
		},
		Params: []TemplateParam{
			{Name: "containerized", Default: true},
		},
		States: map[string]*State{
			"planning": {
				Type:   StateTypeTask,
				Action: "ai.plan",
				Params: map[string]any{
					"max_turns":     30,
					"max_duration":  "15m",
					"containerized": "{{containerized}}",
				},
				Next:  "await_plan_feedback",
				Error: "plan_failed",
			},
			"await_plan_feedback": {
				Type:    StateTypeWait,
				Event:   "plan.user_replied",
				Timeout: &Duration{72 * time.Hour},
				Params: map[string]any{
					"approval_pattern": `(?i)(LGTM|looks good|approved?|proceed|go ahead|ship it)`,
				},
				Next:        "check_plan_feedback",
				TimeoutNext: "plan_expired",
				Error:       "plan_failed",
			},
			"check_plan_feedback": {
				Type: StateTypeChoice,
				Choices: []ChoiceRule{
					{Variable: "plan_approved", Equals: true, Next: "plan_done"},
					{Variable: "plan_approved", Equals: false, Next: "planning"},
				},
				Default: "plan_failed",
			},
			"plan_expired": {
				Type:   StateTypeTask,
				Action: "github.comment_issue",
				Params: map[string]any{
					"body": "Plan has been awaiting feedback for 72 hours. Moving to failed.",
				},
				Next:  "plan_failed",
				Error: "plan_failed",
			},
			"plan_done": {
				Type: StateTypeSucceed,
			},
			"plan_failed": {
				Type: StateTypeFail,
			},
		},
	}
}

// CodeTemplateConfig returns a template for the AI coding phase.
func CodeTemplateConfig() *TemplateConfig {
	return &TemplateConfig{
		Template: "code",
		Entry:    "coding",
		Exits: map[string]string{
			"success": "code_done",
			"failure": "code_failed",
		},
		Params: []TemplateParam{
			{Name: "simplify", Default: false},
			{Name: "containerized", Default: true},
		},
		States: map[string]*State{
			"coding": {
				Type:   StateTypeTask,
				Action: "ai.code",
				Params: map[string]any{
					"max_turns":     50,
					"max_duration":  "30m",
					"containerized": "{{containerized}}",
					"simplify":      "{{simplify}}",
				},
				Next:  "code_done",
				Error: "code_failed",
			},
			"code_done": {
				Type: StateTypeSucceed,
			},
			"code_failed": {
				Type: StateTypeFail,
			},
		},
	}
}

// PRTemplateConfig returns a template for creating a pull request.
func PRTemplateConfig() *TemplateConfig {
	return &TemplateConfig{
		Template: "pr",
		Entry:    "open_pr",
		Exits: map[string]string{
			"success": "pr_done",
			"failure": "pr_failed",
		},
		States: map[string]*State{
			"open_pr": {
				Type:   StateTypeTask,
				Action: "github.create_pr",
				Params: map[string]any{
					"link_issue": true,
				},
				Next:  "pr_done",
				Error: "pr_failed",
				Retry: []RetryConfig{DefaultRetryConfig()},
			},
			"pr_done": {
				Type: StateTypeSucceed,
			},
			"pr_failed": {
				Type: StateTypeFail,
			},
		},
	}
}

// CITemplateConfig returns a template for the CI phase:
// await CI → check result → fix failures / resolve conflicts (bounded loops) → success or failure.
func CITemplateConfig() *TemplateConfig {
	return &TemplateConfig{
		Template: "ci",
		Entry:    "await_ci",
		Exits: map[string]string{
			"success": "ci_done",
			"failure": "ci_failed",
		},
		Params: []TemplateParam{
			{Name: "simplify", Default: false},
		},
		States: map[string]*State{
			"await_ci": {
				Type:    StateTypeWait,
				Event:   "ci.complete",
				Timeout: &Duration{2 * time.Hour},
				Params: map[string]any{
					"on_failure": "fix",
				},
				Next:        "check_ci_result",
				TimeoutNext: "ci_timed_out",
				Error:       "ci_failed",
			},
			"check_ci_result": {
				Type: StateTypeChoice,
				Choices: []ChoiceRule{
					{Variable: "conflicting", Equals: true, Next: "rebase"},
					{Variable: "ci_passed", Equals: true, Next: "ci_done"},
					{Variable: "ci_failed", Equals: true, Next: "fix_ci"},
				},
				Default: "ci_failed",
			},
			"rebase": {
				Type:   StateTypeTask,
				Action: "git.rebase",
				Params: map[string]any{
					"max_rebase_rounds": 3,
				},
				Next:  "await_ci",
				Error: "resolve_conflicts",
				Retry: []RetryConfig{DefaultRetryConfig()},
			},
			"resolve_conflicts": {
				Type:   StateTypeTask,
				Action: "ai.resolve_conflicts",
				Params: map[string]any{
					"max_conflict_rounds": 3,
					"simplify":            "{{simplify}}",
				},
				Next:  "push_conflict_fix",
				Error: "ci_failed",
			},
			"push_conflict_fix": {
				Type:   StateTypeTask,
				Action: "github.push",
				Next:   "await_ci",
				Error:  "ci_failed",
				Retry:  []RetryConfig{DefaultRetryConfig()},
			},
			"fix_ci": {
				Type:   StateTypeTask,
				Action: "ai.fix_ci",
				Params: map[string]any{
					"max_ci_fix_rounds": 3,
					"simplify":          "{{simplify}}",
				},
				Next:  "push_ci_fix",
				Error: "ci_unfixable",
			},
			"push_ci_fix": {
				Type:   StateTypeTask,
				Action: "github.push",
				Next:   "await_ci",
				Error:  "ci_failed",
				Retry:  []RetryConfig{DefaultRetryConfig()},
			},
			"ci_unfixable": {
				Type:   StateTypeTask,
				Action: "github.comment_pr",
				Params: map[string]any{
					"body": "CI fix exhausted after 3 rounds. Manual intervention required.",
				},
				Next:  "ci_failed",
				Error: "ci_failed",
			},
			"ci_timed_out": {
				Type:   StateTypeTask,
				Action: "github.comment_pr",
				Params: map[string]any{
					"body": "CI has been running for over 2 hours. Manual intervention required.",
				},
				Next:  "ci_failed",
				Error: "ci_failed",
			},
			"ci_done": {
				Type: StateTypeSucceed,
			},
			"ci_failed": {
				Type: StateTypeFail,
			},
		},
	}
}

// ReviewTemplateConfig returns a template for the review phase:
// await review → check result → address feedback (bounded loop) → success or failure.
func ReviewTemplateConfig() *TemplateConfig {
	return &TemplateConfig{
		Template: "review",
		Entry:    "await_review",
		Exits: map[string]string{
			"success": "review_done",
			"failure": "review_failed",
		},
		Params: []TemplateParam{
			{Name: "simplify", Default: false},
		},
		States: map[string]*State{
			"await_review": {
				Type:    StateTypeWait,
				Event:   "pr.reviewed",
				Timeout: &Duration{48 * time.Hour},
				Params: map[string]any{
					"auto_address":        true,
					"max_feedback_rounds": 3,
				},
				Next:        "check_review_result",
				TimeoutNext: "review_overdue",
				Error:       "review_failed",
			},
			"check_review_result": {
				Type: StateTypeChoice,
				Choices: []ChoiceRule{
					{Variable: "review_approved", Equals: true, Next: "review_done"},
					{Variable: "changes_requested", Equals: true, Next: "address_review"},
					{Variable: "pr_merged_externally", Equals: true, Next: "review_done"},
				},
				Default: "review_failed",
			},
			"address_review": {
				Type:   StateTypeTask,
				Action: "ai.address_review",
				Params: map[string]any{
					"max_review_rounds": 3,
					"simplify":          "{{simplify}}",
				},
				Next:  "push_review_fix",
				Error: "review_failed",
			},
			"push_review_fix": {
				Type:   StateTypeTask,
				Action: "github.push",
				Next:   "await_review",
				Error:  "review_failed",
				Retry:  []RetryConfig{DefaultRetryConfig()},
			},
			"review_overdue": {
				Type:   StateTypeTask,
				Action: "github.comment_pr",
				Params: map[string]any{
					"body": "PR has been awaiting review for 48 hours. Manual intervention required.",
				},
				Next:  "review_failed",
				Error: "review_failed",
			},
			"review_done": {
				Type: StateTypeSucceed,
			},
			"review_failed": {
				Type: StateTypeFail,
			},
		},
	}
}

// MergeTemplateConfig returns a template for the merge phase.
func MergeTemplateConfig() *TemplateConfig {
	return &TemplateConfig{
		Template: "merge",
		Entry:    "merge",
		Exits: map[string]string{
			"success": "merge_done",
			"failure": "merge_failed",
		},
		Params: []TemplateParam{
			{Name: "method", Default: "rebase"},
		},
		States: map[string]*State{
			"merge": {
				Type:   StateTypeTask,
				Action: "github.merge",
				Params: map[string]any{
					"method":  "{{method}}",
					"cleanup": true,
				},
				Next:  "merge_done",
				Error: "merge_failed",
				Retry: []RetryConfig{DefaultRetryConfig()},
			},
			"merge_done": {
				Type: StateTypeSucceed,
			},
			"merge_failed": {
				Type: StateTypeFail,
			},
		},
	}
}

// AsanaMoveSectionTemplateConfig returns a template for moving an Asana task to a section.
func AsanaMoveSectionTemplateConfig() *TemplateConfig {
	return &TemplateConfig{
		Template: "asana_move_section",
		Entry:    "move",
		Exits: map[string]string{
			"success": "move_done",
			"failure": "move_failed",
		},
		Params: []TemplateParam{
			{Name: "section", Default: ""},
		},
		States: map[string]*State{
			"move": {
				Type:   StateTypeTask,
				Action: "asana.move_to_section",
				Params: map[string]any{
					"section": "{{section}}",
				},
				Next:  "move_done",
				Error: "move_failed",
			},
			"move_done": {
				Type: StateTypeSucceed,
			},
			"move_failed": {
				Type: StateTypeFail,
			},
		},
	}
}

// LinearMoveStateTemplateConfig returns a template for moving a Linear issue to a state.
func LinearMoveStateTemplateConfig() *TemplateConfig {
	return &TemplateConfig{
		Template: "linear_move_state",
		Entry:    "move",
		Exits: map[string]string{
			"success": "move_done",
			"failure": "move_failed",
		},
		Params: []TemplateParam{
			{Name: "state", Default: ""},
		},
		States: map[string]*State{
			"move": {
				Type:   StateTypeTask,
				Action: "linear.move_to_state",
				Params: map[string]any{
					"state": "{{state}}",
				},
				Next:  "move_done",
				Error: "move_failed",
			},
			"move_done": {
				Type: StateTypeSucceed,
			},
			"move_failed": {
				Type: StateTypeFail,
			},
		},
	}
}

// AsanaAwaitSectionTemplateConfig returns a template for waiting until an Asana task
// is in a specific section. Times out after 7 days.
func AsanaAwaitSectionTemplateConfig() *TemplateConfig {
	return &TemplateConfig{
		Template: "asana_await_section",
		Entry:    "await",
		Exits: map[string]string{
			"success": "await_done",
			"failure": "await_failed",
		},
		Params: []TemplateParam{
			{Name: "section", Default: ""},
		},
		States: map[string]*State{
			"await": {
				Type:        StateTypeWait,
				Event:       "asana.in_section",
				Timeout:     &Duration{7 * 24 * time.Hour},
				TimeoutNext: "await_failed",
				Params: map[string]any{
					"section": "{{section}}",
				},
				Next:  "await_done",
				Error: "await_failed",
			},
			"await_done": {
				Type: StateTypeSucceed,
			},
			"await_failed": {
				Type: StateTypeFail,
			},
		},
	}
}

// LinearAwaitStateTemplateConfig returns a template for waiting until a Linear issue
// is in a specific state. Times out after 7 days.
func LinearAwaitStateTemplateConfig() *TemplateConfig {
	return &TemplateConfig{
		Template: "linear_await_state",
		Entry:    "await",
		Exits: map[string]string{
			"success": "await_done",
			"failure": "await_failed",
		},
		Params: []TemplateParam{
			{Name: "state", Default: ""},
		},
		States: map[string]*State{
			"await": {
				Type:        StateTypeWait,
				Event:       "linear.in_state",
				Timeout:     &Duration{7 * 24 * time.Hour},
				TimeoutNext: "await_failed",
				Params: map[string]any{
					"state": "{{state}}",
				},
				Next:  "await_done",
				Error: "await_failed",
			},
			"await_done": {
				Type: StateTypeSucceed,
			},
			"await_failed": {
				Type: StateTypeFail,
			},
		},
	}
}

// Merge overlays partial onto defaults. States present in partial replace the
// corresponding default state entirely. States in defaults but not in partial
// are preserved. Top-level fields (Workflow, Start) use partial if non-empty.
// Source fields use partial if non-empty.
func Merge(partial, defaults *Config) *Config {
	result := &Config{
		Workflow: partial.Workflow,
		Start:    partial.Start,
		Source:   partial.Source,
		States:   make(map[string]*State),
	}

	// Fill empty top-level fields from defaults
	if result.Workflow == "" {
		result.Workflow = defaults.Workflow
	}
	if result.Start == "" {
		result.Start = defaults.Start
	}

	// Source
	if result.Source.Provider == "" {
		result.Source.Provider = defaults.Source.Provider
	}
	if result.Source.Filter.Label == "" && result.Source.Provider == defaults.Source.Provider {
		result.Source.Filter.Label = defaults.Source.Filter.Label
	}
	if result.Source.Filter.Project == "" {
		result.Source.Filter.Project = defaults.Source.Filter.Project
	}
	if result.Source.Filter.Team == "" {
		result.Source.Filter.Team = defaults.Source.Filter.Team
	}
	if result.Source.Filter.Section == "" {
		result.Source.Filter.Section = defaults.Source.Filter.Section
	}

	// Copy defaults first
	for name, state := range defaults.States {
		s := *state
		if state.Params != nil {
			s.Params = make(map[string]any, len(state.Params))
			maps.Copy(s.Params, state.Params)
		}
		if state.Before != nil {
			s.Before = make([]HookConfig, len(state.Before))
			copy(s.Before, state.Before)
		}
		if state.After != nil {
			s.After = make([]HookConfig, len(state.After))
			copy(s.After, state.After)
		}
		if state.Retry != nil {
			s.Retry = make([]RetryConfig, len(state.Retry))
			for i, r := range state.Retry {
				s.Retry[i] = r
				if r.Interval != nil {
					intervalCopy := *r.Interval
					s.Retry[i].Interval = &intervalCopy
				}
			}
		}
		if state.Catch != nil {
			s.Catch = make([]CatchConfig, len(state.Catch))
			for i, c := range state.Catch {
				s.Catch[i] = c
				if c.Errors != nil {
					s.Catch[i].Errors = make([]string, len(c.Errors))
					copy(s.Catch[i].Errors, c.Errors)
				}
			}
		}
		result.States[name] = &s
	}

	// Overlay partial states (full replacement per state)
	maps.Copy(result.States, partial.States)

	// Settings: partial wins if present, otherwise keep defaults
	if partial.Settings != nil {
		result.Settings = partial.Settings
	} else if defaults.Settings != nil {
		s := *defaults.Settings
		result.Settings = &s
	}

	return result
}
