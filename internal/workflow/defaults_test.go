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
	expectedStates := []string{"coding", "open_pr", "await_ci", "check_ci_result", "fix_ci", "push_ci_fix", "await_review", "merge", "done", "failed"}
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
	if p.Bool("supervisor", true) {
		t.Error("coding supervisor: expected false")
	}

	// Review params
	review := cfg.States["await_review"]
	rp := NewParamHelper(review.Params)
	if rp.Int("max_feedback_rounds", 0) != 3 {
		t.Error("review max_feedback_rounds: expected 3")
	}
	if !rp.Bool("auto_address", false) {
		t.Error("review auto_address: expected true")
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
	if len(checkCI.Choices) != 2 {
		t.Errorf("check_ci_result choices: expected 2, got %d", len(checkCI.Choices))
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

	// Default should pass validation (including the choice-gated cycle)
	errs := Validate(cfg)
	if len(errs) > 0 {
		t.Errorf("default config should be valid, got errors: %v", errs)
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
			Start:   "s",
			States:  map[string]*State{"s": {Type: StateTypeSucceed}},
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

	t.Run("default settings is deep copied not shared", func(t *testing.T) {
		defaults := &Config{
			Workflow: "test",
			Start:   "s",
			States:  map[string]*State{"s": {Type: StateTypeSucceed}},
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
