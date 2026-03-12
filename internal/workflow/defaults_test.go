package workflow

import (
	"testing"
	"time"
)

func TestDefaultWorkflowConfig(t *testing.T) {
	cfg := DefaultWorkflowConfig()

	if cfg.Workflow != "issue-to-merge" {
		t.Errorf("default workflow: got %q", cfg.Workflow)
	}
	if cfg.Start != "coding" {
		t.Errorf("default start: got %q", cfg.Start)
	}
	if cfg.Source.Provider != "github" {
		t.Errorf("default provider: got %q", cfg.Source.Provider)
	}
	if cfg.Source.Filter.Label != "queued" {
		t.Errorf("default label: got %q", cfg.Source.Filter.Label)
	}

	// Verify expected states exist
	expectedStates := []string{"coding", "open_pr", "await_ci", "check_ci_result", "rebase", "resolve_conflicts", "push_conflict_fix", "fix_ci", "push_ci_fix", "await_review", "merge", "done", "failed"}
	for _, name := range expectedStates {
		if _, ok := cfg.States[name]; !ok {
			t.Errorf("expected state %q to exist", name)
		}
	}

	// Coding params
	coding := cfg.States["coding"]
	p := NewParamHelper(coding.Params)
	if p.Int("max_turns", 0) != 50 {
		t.Error("coding max_turns: expected 50")
	}
	if p.Duration("max_duration", 0) != 30*time.Minute {
		t.Error("coding max_duration: expected 30m")
	}
	if !p.Bool("containerized", false) {
		t.Error("coding containerized: expected true")
	}

	// await_review uses explicit address_review state (auto_address=false)
	review := cfg.States["await_review"]
	rp := NewParamHelper(review.Params)
	if rp.Bool("auto_address", true) {
		t.Error("review auto_address: expected false (explicit address_review state used instead)")
	}
	if review.Next != "check_review_result" {
		t.Errorf("await_review next: expected check_review_result, got %s", review.Next)
	}

	// check_review_result routes review_approved → merge and changes_requested → address_review
	checkReview := cfg.States["check_review_result"]
	if checkReview == nil {
		t.Fatal("expected check_review_result state")
	}
	if checkReview.Type != StateTypeChoice {
		t.Errorf("check_review_result type: expected choice, got %s", checkReview.Type)
	}

	// address_review state
	addressReview := cfg.States["address_review"]
	if addressReview == nil {
		t.Fatal("expected address_review state")
	}
	if addressReview.Action != "ai.address_review" {
		t.Errorf("address_review action: expected ai.address_review, got %s", addressReview.Action)
	}
	arp := NewParamHelper(addressReview.Params)
	if arp.Int("max_review_rounds", 0) != 3 {
		t.Error("address_review max_review_rounds: expected 3")
	}
	if addressReview.Next != "push_review_fix" {
		t.Errorf("address_review next: expected push_review_fix, got %s", addressReview.Next)
	}

	// push_review_fix loops back to await_review
	pushReviewFix := cfg.States["push_review_fix"]
	if pushReviewFix == nil {
		t.Fatal("expected push_review_fix state")
	}
	if pushReviewFix.Action != "github.push" {
		t.Errorf("push_review_fix action: expected github.push, got %s", pushReviewFix.Action)
	}
	if pushReviewFix.Next != "await_review" {
		t.Errorf("push_review_fix next: expected await_review, got %s", pushReviewFix.Next)
	}

	// CI params — on_failure should be "fix" for the CI fix loop
	ci := cfg.States["await_ci"]
	cp := NewParamHelper(ci.Params)
	if cp.String("on_failure", "") != "fix" {
		t.Errorf("ci on_failure: got %q", cp.String("on_failure", ""))
	}

	// check_ci_result choice state
	checkCI := cfg.States["check_ci_result"]
	if checkCI.Type != StateTypeChoice {
		t.Errorf("check_ci_result type: expected choice, got %s", checkCI.Type)
	}
	if len(checkCI.Choices) != 3 {
		t.Errorf("check_ci_result choices: expected 3, got %d", len(checkCI.Choices))
	}
	// First choice should be conflicting→rebase
	if len(checkCI.Choices) >= 1 {
		first := checkCI.Choices[0]
		if first.Variable != "conflicting" {
			t.Errorf("first choice variable: expected conflicting, got %s", first.Variable)
		}
		if first.Next != "rebase" {
			t.Errorf("first choice next: expected rebase, got %s", first.Next)
		}
	}

	// rebase state
	rebase := cfg.States["rebase"]
	if rebase.Type != StateTypeTask {
		t.Errorf("rebase type: expected task, got %s", rebase.Type)
	}
	if rebase.Action != "git.rebase" {
		t.Errorf("rebase action: expected git.rebase, got %s", rebase.Action)
	}
	if rebase.Next != "await_ci" {
		t.Errorf("rebase next: expected await_ci, got %s", rebase.Next)
	}
	if rebase.Error != "resolve_conflicts" {
		t.Errorf("rebase error: expected resolve_conflicts, got %s", rebase.Error)
	}
	rbp := NewParamHelper(rebase.Params)
	if rbp.Int("max_rebase_rounds", 0) != 3 {
		t.Error("rebase max_rebase_rounds: expected 3")
	}

	// resolve_conflicts state
	resolveConflicts := cfg.States["resolve_conflicts"]
	if resolveConflicts.Type != StateTypeTask {
		t.Errorf("resolve_conflicts type: expected task, got %s", resolveConflicts.Type)
	}
	if resolveConflicts.Action != "ai.resolve_conflicts" {
		t.Errorf("resolve_conflicts action: expected ai.resolve_conflicts, got %s", resolveConflicts.Action)
	}
	if resolveConflicts.Next != "push_conflict_fix" {
		t.Errorf("resolve_conflicts next: expected push_conflict_fix, got %s", resolveConflicts.Next)
	}
	if resolveConflicts.Error != "failed" {
		t.Errorf("resolve_conflicts error: expected failed, got %s", resolveConflicts.Error)
	}
	rcp := NewParamHelper(resolveConflicts.Params)
	if rcp.Int("max_conflict_rounds", 0) != 3 {
		t.Error("resolve_conflicts max_conflict_rounds: expected 3")
	}

	// push_conflict_fix loops back to await_ci
	pushConflictFix := cfg.States["push_conflict_fix"]
	if pushConflictFix.Action != "github.push" {
		t.Errorf("push_conflict_fix action: expected github.push, got %s", pushConflictFix.Action)
	}
	if pushConflictFix.Next != "await_ci" {
		t.Errorf("push_conflict_fix next: expected await_ci, got %s", pushConflictFix.Next)
	}

	// fix_ci params
	fixCI := cfg.States["fix_ci"]
	fp := NewParamHelper(fixCI.Params)
	if fp.Int("max_ci_fix_rounds", 0) != 3 {
		t.Error("fix_ci max_ci_fix_rounds: expected 3")
	}

	// push_ci_fix loops back to await_ci
	pushCIFix := cfg.States["push_ci_fix"]
	if pushCIFix.Next != "await_ci" {
		t.Errorf("push_ci_fix next: expected await_ci, got %s", pushCIFix.Next)
	}

	// open_pr now goes to await_ci (not await_review)
	openPR := cfg.States["open_pr"]
	if openPR.Next != "await_ci" {
		t.Errorf("open_pr next: expected await_ci, got %s", openPR.Next)
	}

	// Merge params
	merge := cfg.States["merge"]
	mp := NewParamHelper(merge.Params)
	if mp.String("method", "") != "rebase" {
		t.Errorf("merge method: got %q", mp.String("method", ""))
	}
	if merge.Error != "rebase" {
		t.Errorf("merge error: expected rebase, got %s", merge.Error)
	}

	// Default should pass validation (including the choice-gated cycle)
	errs := Validate(cfg)
	if len(errs) > 0 {
		t.Errorf("default config should be valid, got errors: %v", errs)
	}
}

func TestDefaultPlanningWorkflowConfig(t *testing.T) {
	cfg := DefaultPlanningWorkflowConfig()

	if cfg.Workflow != "plan-then-code" {
		t.Errorf("workflow name: got %q, want plan-then-code", cfg.Workflow)
	}
	if cfg.Start != "planning" {
		t.Errorf("start: got %q, want planning", cfg.Start)
	}
	if cfg.Source.Provider != "github" {
		t.Errorf("source provider: got %q, want github", cfg.Source.Provider)
	}

	// Verify planning states exist
	for _, name := range []string{"planning", "await_plan_feedback", "check_plan_feedback"} {
		if _, ok := cfg.States[name]; !ok {
			t.Errorf("expected planning state %q to exist", name)
		}
	}

	// Verify coding states inherited from DefaultWorkflowConfig
	for _, name := range []string{"coding", "open_pr", "await_ci", "check_ci_result", "merge", "done", "failed"} {
		if _, ok := cfg.States[name]; !ok {
			t.Errorf("expected coding state %q to exist", name)
		}
	}

	// planning state
	planning := cfg.States["planning"]
	if planning.Type != StateTypeTask {
		t.Errorf("planning type: got %s, want task", planning.Type)
	}
	if planning.Action != "ai.plan" {
		t.Errorf("planning action: got %s, want ai.plan", planning.Action)
	}
	if planning.Next != "await_plan_feedback" {
		t.Errorf("planning next: got %s, want await_plan_feedback", planning.Next)
	}
	pp := NewParamHelper(planning.Params)
	if pp.Int("max_turns", 0) != 30 {
		t.Error("planning max_turns: expected 30")
	}
	if pp.Duration("max_duration", 0) != 15*time.Minute {
		t.Error("planning max_duration: expected 15m")
	}

	// await_plan_feedback state
	awaitFeedback := cfg.States["await_plan_feedback"]
	if awaitFeedback.Type != StateTypeWait {
		t.Errorf("await_plan_feedback type: got %s, want wait", awaitFeedback.Type)
	}
	if awaitFeedback.Event != "plan.user_replied" {
		t.Errorf("await_plan_feedback event: got %s, want plan.user_replied", awaitFeedback.Event)
	}
	if awaitFeedback.Next != "check_plan_feedback" {
		t.Errorf("await_plan_feedback next: got %s, want check_plan_feedback", awaitFeedback.Next)
	}
	if awaitFeedback.TimeoutNext != "failed" {
		t.Errorf("await_plan_feedback timeout_next: got %s, want failed", awaitFeedback.TimeoutNext)
	}
	if awaitFeedback.Timeout == nil || awaitFeedback.Timeout.Duration != 72*time.Hour {
		t.Error("await_plan_feedback timeout: expected 72h")
	}
	afp := NewParamHelper(awaitFeedback.Params)
	if afp.String("approval_pattern", "") == "" {
		t.Error("await_plan_feedback approval_pattern: expected non-empty pattern")
	}

	// check_plan_feedback routes plan_approved=true → coding, plan_approved=false → planning
	checkFeedback := cfg.States["check_plan_feedback"]
	if checkFeedback.Type != StateTypeChoice {
		t.Errorf("check_plan_feedback type: got %s, want choice", checkFeedback.Type)
	}
	if len(checkFeedback.Choices) != 2 {
		t.Fatalf("check_plan_feedback choices: got %d, want 2", len(checkFeedback.Choices))
	}
	approved := checkFeedback.Choices[0]
	if approved.Variable != "plan_approved" || approved.Equals != true || approved.Next != "coding" {
		t.Errorf("check_plan_feedback approved choice: got variable=%s equals=%v next=%s", approved.Variable, approved.Equals, approved.Next)
	}
	feedback := checkFeedback.Choices[1]
	if feedback.Variable != "plan_approved" || feedback.Equals != false || feedback.Next != "planning" {
		t.Errorf("check_plan_feedback feedback choice: got variable=%s equals=%v next=%s", feedback.Variable, feedback.Equals, feedback.Next)
	}

	// Config should pass validation (planning loop is choice-gated, like the CI/review loops)
	errs := Validate(cfg)
	if len(errs) > 0 {
		t.Errorf("planning config should be valid, got errors: %v", errs)
	}
}

func TestDefaultPlanningWorkflowConfig_DoesNotModifyDefaultWorkflowConfig(t *testing.T) {
	_ = DefaultPlanningWorkflowConfig()
	// Calling DefaultPlanningWorkflowConfig should not contaminate subsequent calls
	// to DefaultWorkflowConfig (states are added to a fresh copy, not the global default).
	base := DefaultWorkflowConfig()
	if _, hasPlanningState := base.States["planning"]; hasPlanningState {
		t.Error("DefaultPlanningWorkflowConfig should not mutate DefaultWorkflowConfig result")
	}
}

func TestDefaultWorkflowConfig_RetryOnNetworkStates(t *testing.T) {
	cfg := DefaultWorkflowConfig()

	// States that should have retry configured
	retryStates := []string{"open_pr", "push_ci_fix", "push_conflict_fix", "push_review_fix", "rebase", "merge"}
	for _, name := range retryStates {
		state, ok := cfg.States[name]
		if !ok {
			t.Errorf("expected state %q to exist", name)
			continue
		}
		if len(state.Retry) == 0 {
			t.Errorf("state %q should have retry configured", name)
			continue
		}
		r := state.Retry[0]
		if r.MaxAttempts != 3 {
			t.Errorf("state %q retry max_attempts: got %d, want 3", name, r.MaxAttempts)
		}
		if r.Interval == nil || r.Interval.Duration != 15*time.Second {
			t.Errorf("state %q retry interval: expected 15s", name)
		}
		if r.BackoffRate != 2.0 {
			t.Errorf("state %q retry backoff_rate: got %f, want 2.0", name, r.BackoffRate)
		}
	}

	// States that should NOT have retry configured
	noRetryStates := []string{"coding", "fix_ci"}
	for _, name := range noRetryStates {
		state, ok := cfg.States[name]
		if !ok {
			t.Errorf("expected state %q to exist", name)
			continue
		}
		if len(state.Retry) > 0 {
			t.Errorf("state %q should NOT have retry configured (expensive action)", name)
		}
	}
}

func TestMerge(t *testing.T) {
	t.Run("empty partial gets all defaults", func(t *testing.T) {
		partial := &Config{}
		defaults := DefaultWorkflowConfig()
		result := Merge(partial, defaults)

		if result.Source.Provider != "github" {
			t.Errorf("provider: got %q", result.Source.Provider)
		}
		if result.Start != "coding" {
			t.Errorf("start: got %q", result.Start)
		}
		if len(result.States) != len(defaults.States) {
			t.Errorf("expected %d states, got %d", len(defaults.States), len(result.States))
		}
	})

	t.Run("partial provider preserved", func(t *testing.T) {
		partial := &Config{
			Source: SourceConfig{Provider: "asana"},
		}
		defaults := DefaultWorkflowConfig()
		result := Merge(partial, defaults)

		if result.Source.Provider != "asana" {
			t.Errorf("provider: got %q", result.Source.Provider)
		}
	})

	t.Run("asana config without label does not inherit github queued label", func(t *testing.T) {
		// Regression: Merge used to blindly copy the default label ("queued") into
		// non-GitHub configs that had no label set. This caused the Asana provider
		// to filter by the tag "queued", silently dropping tasks without that tag.
		partial := &Config{
			Source: SourceConfig{
				Provider: "asana",
				Filter: FilterConfig{
					Project: "1213476871486785",
					Section: "To do",
				},
			},
		}
		defaults := DefaultWorkflowConfig() // provider=github, label=queued
		result := Merge(partial, defaults)

		if result.Source.Filter.Label != "" {
			t.Errorf("label should be empty for asana config without explicit label, got %q", result.Source.Filter.Label)
		}
	})

	t.Run("partial state replaces default entirely", func(t *testing.T) {
		partial := &Config{
			States: map[string]*State{
				"coding": {
					Type:   StateTypeTask,
					Action: "ai.code",
					Params: map[string]any{
						"max_turns": 100,
					},
					Next:  "open_pr",
					Error: "failed",
				},
			},
		}
		defaults := DefaultWorkflowConfig()
		result := Merge(partial, defaults)

		coding := result.States["coding"]
		p := NewParamHelper(coding.Params)
		if p.Int("max_turns", 0) != 100 {
			t.Error("expected overridden max_turns of 100")
		}
		// Since we replaced the entire state, max_duration should not be set
		if p.Has("max_duration") {
			t.Error("expected max_duration to be absent (full state replacement)")
		}

		// Other states from defaults should still exist
		if _, ok := result.States["open_pr"]; !ok {
			t.Error("expected open_pr state from defaults")
		}
	})

	t.Run("partial merge method preserved", func(t *testing.T) {
		partial := &Config{
			States: map[string]*State{
				"merge": {
					Type:   StateTypeTask,
					Action: "github.merge",
					Params: map[string]any{
						"method": "squash",
					},
					Next: "done",
				},
			},
		}
		defaults := DefaultWorkflowConfig()
		result := Merge(partial, defaults)

		mp := NewParamHelper(result.States["merge"].Params)
		if mp.String("method", "") != "squash" {
			t.Errorf("merge method: got %q", mp.String("method", ""))
		}
	})

	t.Run("default states preserved when not overridden", func(t *testing.T) {
		partial := &Config{}
		defaults := DefaultWorkflowConfig()
		result := Merge(partial, defaults)

		// Verify coding params from defaults are preserved
		coding := result.States["coding"]
		p := NewParamHelper(coding.Params)
		if p.Int("max_turns", 0) != 50 {
			t.Error("expected default max_turns of 50")
		}
	})

	t.Run("partial settings override defaults", func(t *testing.T) {
		cleanup := true
		partial := &Config{
			Settings: &SettingsConfig{
				ContainerImage: "custom:latest",
				MaxConcurrent:  5,
				CleanupMerged:  &cleanup,
			},
		}
		defaults := DefaultWorkflowConfig()
		result := Merge(partial, defaults)

		if result.Settings == nil {
			t.Fatal("expected settings to be present")
		}
		if result.Settings.ContainerImage != "custom:latest" {
			t.Errorf("container_image: got %q", result.Settings.ContainerImage)
		}
		if result.Settings.MaxConcurrent != 5 {
			t.Errorf("max_concurrent: got %d", result.Settings.MaxConcurrent)
		}
		if result.Settings.CleanupMerged == nil || !*result.Settings.CleanupMerged {
			t.Error("cleanup_merged: expected true")
		}
	})

	t.Run("nil partial settings keeps default settings", func(t *testing.T) {
		defaults := &Config{
			Workflow: "test",
			Start:    "s",
			States:   map[string]*State{"s": {Type: StateTypeSucceed}},
			Settings: &SettingsConfig{
				BranchPrefix: "default/",
			},
		}
		partial := &Config{}
		result := Merge(partial, defaults)

		if result.Settings == nil {
			t.Fatal("expected settings from defaults")
		}
		if result.Settings.BranchPrefix != "default/" {
			t.Errorf("branch_prefix: got %q", result.Settings.BranchPrefix)
		}
	})

	t.Run("both nil settings produces nil", func(t *testing.T) {
		partial := &Config{}
		defaults := DefaultWorkflowConfig() // no Settings
		result := Merge(partial, defaults)

		if result.Settings != nil {
			t.Error("expected nil settings when both are nil")
		}
	})

	t.Run("default retry configs deep copied not shared", func(t *testing.T) {
		defaults := &Config{
			Workflow: "test",
			Start:    "s",
			States: map[string]*State{
				"s": {
					Type:   StateTypeTask,
					Action: "github.push",
					Next:   "done",
					Retry:  []RetryConfig{DefaultRetryConfig()},
				},
				"done": {Type: StateTypeSucceed},
			},
		}
		partial := &Config{}
		result := Merge(partial, defaults)

		// Mutate the result's retry interval; should not affect defaults
		result.States["s"].Retry[0].Interval.Duration = 99 * time.Second
		if defaults.States["s"].Retry[0].Interval.Duration != 15*time.Second {
			t.Error("merge should deep-copy retry interval from defaults")
		}

		// Mutate max_attempts
		result.States["s"].Retry[0].MaxAttempts = 99
		if defaults.States["s"].Retry[0].MaxAttempts != 3 {
			t.Error("merge should deep-copy retry config from defaults")
		}
	})

	t.Run("default catch configs deep copied not shared", func(t *testing.T) {
		defaults := &Config{
			Workflow: "test",
			Start:    "s",
			States: map[string]*State{
				"s": {
					Type:   StateTypeTask,
					Action: "github.push",
					Next:   "done",
					Catch: []CatchConfig{
						{Errors: []string{"*"}, Next: "recovery"},
					},
				},
				"done":     {Type: StateTypeSucceed},
				"recovery": {Type: StateTypeSucceed},
			},
		}
		partial := &Config{}
		result := Merge(partial, defaults)

		// Mutate the result's catch errors; should not affect defaults
		result.States["s"].Catch[0].Errors[0] = "mutated"
		if defaults.States["s"].Catch[0].Errors[0] != "*" {
			t.Error("merge should deep-copy catch errors from defaults")
		}
	})

	t.Run("default settings is deep copied not shared", func(t *testing.T) {
		defaults := &Config{
			Workflow: "test",
			Start:    "s",
			States:   map[string]*State{"s": {Type: StateTypeSucceed}},
			Settings: &SettingsConfig{
				BranchPrefix: "original/",
			},
		}
		partial := &Config{}
		result := Merge(partial, defaults)

		// Mutate the result; should not affect defaults
		result.Settings.BranchPrefix = "mutated/"
		if defaults.Settings.BranchPrefix != "original/" {
			t.Error("merge should deep-copy settings from defaults")
		}
	})
}
