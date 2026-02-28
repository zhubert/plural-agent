package workflow

import "maps"

import "time"

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
	if result.Source.Filter.Label == "" {
		result.Source.Filter.Label = defaults.Source.Filter.Label
	}
	if result.Source.Filter.Project == "" {
		result.Source.Filter.Project = defaults.Source.Filter.Project
	}
	if result.Source.Filter.Team == "" {
		result.Source.Filter.Team = defaults.Source.Filter.Team
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
