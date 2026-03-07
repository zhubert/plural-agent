package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// minimalCfg returns a valid minimal config with the given states map plus
// standard done/failed terminal states, useful for template expansion tests.
func minimalCfg(states map[string]*State) *Config {
	cfg := &Config{
		Start: "start",
		Source: SourceConfig{
			Provider: "github",
			Filter:   FilterConfig{Label: "queued"},
		},
		States: states,
	}
	if cfg.States == nil {
		cfg.States = make(map[string]*State)
	}
	if _, ok := cfg.States["done"]; !ok {
		cfg.States["done"] = &State{Type: StateTypeSucceed}
	}
	if _, ok := cfg.States["failed"]; !ok {
		cfg.States["failed"] = &State{Type: StateTypeFail}
	}
	return cfg
}

// writeTemplateFile writes a template YAML to baseDir/<relPath>.
func writeTemplateFile(t *testing.T, baseDir, relPath, content string) {
	t.Helper()
	full := filepath.Join(baseDir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// simpleTemplateYAML is a minimal valid template for reuse across tests.
const simpleTemplateYAML = `
template: simple
entry: step1
exits:
  success: done
  failure: failed
states:
  step1:
    type: task
    action: ai.code
    next: done
    error: failed
  done:
    type: succeed
  failed:
    type: fail
`

func TestExpandTemplates_BasicExpansion(t *testing.T) {
	dir := t.TempDir()
	writeTemplateFile(t, dir, ".erg/templates/simple.yaml", simpleTemplateYAML)

	cfg := minimalCfg(map[string]*State{
		"start": {
			Type: StateTypeTemplate,
			Use:  ".erg/templates/simple.yaml",
			Exits: map[string]string{
				"success": "done",
				"failure": "failed",
			},
		},
	})

	result, err := ExpandTemplates(cfg, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// "start" should now be a pass state pointing to the prefixed entry.
	startState := result.States["start"]
	if startState == nil {
		t.Fatal("start state missing after expansion")
	}
	if startState.Type != StateTypePass {
		t.Errorf("start state type: got %q, want pass", startState.Type)
	}
	expectedEntry := "_t_start_step1"
	if startState.Next != expectedEntry {
		t.Errorf("start.next: got %q, want %q", startState.Next, expectedEntry)
	}

	// Prefixed step1 should exist.
	step1 := result.States["_t_start_step1"]
	if step1 == nil {
		t.Fatal("_t_start_step1 missing after expansion")
	}
	if step1.Type != StateTypeTask {
		t.Errorf("_t_start_step1.type: got %q, want task", step1.Type)
	}

	// Exit wiring: _t_start_done should be a pass to caller's "done".
	exitState := result.States["_t_start_done"]
	if exitState == nil {
		t.Fatal("_t_start_done missing after expansion")
	}
	if exitState.Type != StateTypePass {
		t.Errorf("_t_start_done.type: got %q, want pass", exitState.Type)
	}
	if exitState.Next != "done" {
		t.Errorf("_t_start_done.next: got %q, want done", exitState.Next)
	}

	// Prefixed step1's internal refs should use the prefix.
	if step1.Next != "_t_start_done" {
		t.Errorf("_t_start_step1.next: got %q, want _t_start_done", step1.Next)
	}
	if step1.Error != "_t_start_failed" {
		t.Errorf("_t_start_step1.error: got %q, want _t_start_failed", step1.Error)
	}
}

func TestExpandTemplates_ParamSubstitution(t *testing.T) {
	dir := t.TempDir()
	templateYAML := `
template: paramtest
entry: coding
exits:
  success: done
  failure: failed
params:
  - name: max_turns
    default: 50
  - name: merge_method
    default: rebase
states:
  coding:
    type: task
    action: ai.code
    params:
      max_turns: "{{max_turns}}"
      method: "{{merge_method}}"
    next: done
    error: failed
  done:
    type: succeed
  failed:
    type: fail
`
	writeTemplateFile(t, dir, ".erg/templates/paramtest.yaml", templateYAML)

	cfg := minimalCfg(map[string]*State{
		"start": {
			Type: StateTypeTemplate,
			Use:  ".erg/templates/paramtest.yaml",
			Params: map[string]any{
				"max_turns":    25,
				"merge_method": "squash",
			},
			Exits: map[string]string{
				"success": "done",
				"failure": "failed",
			},
		},
	})

	result, err := ExpandTemplates(cfg, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	codingState := result.States["_t_start_coding"]
	if codingState == nil {
		t.Fatal("_t_start_coding missing")
	}
	p := NewParamHelper(codingState.Params)
	if p.String("max_turns", "") != "25" {
		t.Errorf("max_turns param substitution: got %q, want 25", p.String("max_turns", ""))
	}
	if p.String("method", "") != "squash" {
		t.Errorf("method param substitution: got %q, want squash", p.String("method", ""))
	}
}

func TestExpandTemplates_DefaultParams(t *testing.T) {
	dir := t.TempDir()
	templateYAML := `
template: defaults
entry: step
exits:
  success: done
  failure: failed
params:
  - name: my_param
    default: default_value
states:
  step:
    type: task
    action: ai.code
    params:
      label: "{{my_param}}"
    next: done
    error: failed
  done:
    type: succeed
  failed:
    type: fail
`
	writeTemplateFile(t, dir, ".erg/templates/defaults.yaml", templateYAML)

	cfg := minimalCfg(map[string]*State{
		"start": {
			Type: StateTypeTemplate,
			Use:  ".erg/templates/defaults.yaml",
			// No params override — should use default.
			Exits: map[string]string{
				"success": "done",
				"failure": "failed",
			},
		},
	})

	result, err := ExpandTemplates(cfg, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	stepState := result.States["_t_start_step"]
	if stepState == nil {
		t.Fatal("_t_start_step missing")
	}
	p := NewParamHelper(stepState.Params)
	if p.String("label", "") != "default_value" {
		t.Errorf("default param: got %q, want default_value", p.String("label", ""))
	}
}

func TestExpandTemplates_StateNamespacing(t *testing.T) {
	dir := t.TempDir()
	writeTemplateFile(t, dir, ".erg/templates/simple.yaml", simpleTemplateYAML)

	// Two different template instances should not collide.
	cfg := minimalCfg(map[string]*State{
		"first": {
			Type: StateTypeTemplate,
			Use:  ".erg/templates/simple.yaml",
			Exits: map[string]string{
				"success": "done",
				"failure": "failed",
			},
		},
		"second": {
			Type: StateTypeTemplate,
			Use:  ".erg/templates/simple.yaml",
			Exits: map[string]string{
				"success": "done",
				"failure": "failed",
			},
		},
	})
	// Adjust start for this test.
	cfg.Start = "first"

	result, err := ExpandTemplates(cfg, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Both should have distinct prefixed states.
	if _, ok := result.States["_t_first_step1"]; !ok {
		t.Error("_t_first_step1 missing")
	}
	if _, ok := result.States["_t_second_step1"]; !ok {
		t.Error("_t_second_step1 missing")
	}
}

func TestExpandTemplates_ExitWiring(t *testing.T) {
	dir := t.TempDir()
	templateYAML := `
template: exitwire
entry: work
exits:
  ok: terminal_ok
  err: terminal_err
states:
  work:
    type: task
    action: ai.code
    next: terminal_ok
    error: terminal_err
  terminal_ok:
    type: succeed
  terminal_err:
    type: fail
`
	writeTemplateFile(t, dir, ".erg/templates/exitwire.yaml", templateYAML)

	cfg := minimalCfg(map[string]*State{
		"start": {
			Type: StateTypeTemplate,
			Use:  ".erg/templates/exitwire.yaml",
			Exits: map[string]string{
				"ok":  "done",
				"err": "failed",
			},
		},
	})

	result, err := ExpandTemplates(cfg, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	okExit := result.States["_t_start_terminal_ok"]
	if okExit == nil {
		t.Fatal("_t_start_terminal_ok missing")
	}
	if okExit.Type != StateTypePass || okExit.Next != "done" {
		t.Errorf("ok exit: type=%q next=%q, want pass/done", okExit.Type, okExit.Next)
	}

	errExit := result.States["_t_start_terminal_err"]
	if errExit == nil {
		t.Fatal("_t_start_terminal_err missing")
	}
	if errExit.Type != StateTypePass || errExit.Next != "failed" {
		t.Errorf("err exit: type=%q next=%q, want pass/failed", errExit.Type, errExit.Next)
	}
}

func TestExpandTemplates_CircularDetection(t *testing.T) {
	dir := t.TempDir()
	// template A uses template B which uses template A.
	templateA := `
template: a
entry: step
exits:
  success: done
  failure: failed
states:
  step:
    type: template
    use: ".erg/templates/b.yaml"
    exits:
      success: done
      failure: failed
  done:
    type: succeed
  failed:
    type: fail
`
	templateB := `
template: b
entry: step
exits:
  success: done
  failure: failed
states:
  step:
    type: template
    use: ".erg/templates/a.yaml"
    exits:
      success: done
      failure: failed
  done:
    type: succeed
  failed:
    type: fail
`
	writeTemplateFile(t, dir, ".erg/templates/a.yaml", templateA)
	writeTemplateFile(t, dir, ".erg/templates/b.yaml", templateB)

	cfg := minimalCfg(map[string]*State{
		"start": {
			Type: StateTypeTemplate,
			Use:  ".erg/templates/a.yaml",
			Exits: map[string]string{
				"success": "done",
				"failure": "failed",
			},
		},
	})

	_, err := ExpandTemplates(cfg, dir)
	if err == nil {
		t.Fatal("expected error for circular template reference")
	}
	if !strings.Contains(err.Error(), "circular") {
		t.Errorf("expected circular error message, got: %v", err)
	}
}

func TestExpandTemplates_MissingUse(t *testing.T) {
	cfg := minimalCfg(map[string]*State{
		"start": {
			Type: StateTypeTemplate,
			// No Use field.
			Exits: map[string]string{"success": "done"},
		},
	})

	_, err := ExpandTemplates(cfg, t.TempDir())
	if err == nil {
		t.Fatal("expected error for missing use field")
	}
	if !strings.Contains(err.Error(), "use is required") {
		t.Errorf("expected 'use is required' error, got: %v", err)
	}
}

func TestExpandTemplates_MissingExits(t *testing.T) {
	dir := t.TempDir()
	writeTemplateFile(t, dir, ".erg/templates/simple.yaml", simpleTemplateYAML)

	cfg := minimalCfg(map[string]*State{
		"start": {
			Type: StateTypeTemplate,
			Use:  ".erg/templates/simple.yaml",
			// No Exits field.
		},
	})

	_, err := ExpandTemplates(cfg, dir)
	if err == nil {
		t.Fatal("expected error for missing exits field")
	}
	if !strings.Contains(err.Error(), "exits is required") {
		t.Errorf("expected 'exits is required' error, got: %v", err)
	}
}

func TestExpandTemplates_ExitReferencesNonExistentState(t *testing.T) {
	dir := t.TempDir()
	writeTemplateFile(t, dir, ".erg/templates/simple.yaml", simpleTemplateYAML)

	cfg := minimalCfg(map[string]*State{
		"start": {
			Type: StateTypeTemplate,
			Use:  ".erg/templates/simple.yaml",
			Exits: map[string]string{
				"success": "nonexistent", // does not exist in caller
				"failure": "failed",
			},
		},
	})

	_, err := ExpandTemplates(cfg, dir)
	if err == nil {
		t.Fatal("expected error for exit referencing non-existent state")
	}
	if !strings.Contains(err.Error(), "non-existent local state") {
		t.Errorf("expected non-existent state error, got: %v", err)
	}
}

func TestExpandTemplates_CallerMapsUndeclaredExit(t *testing.T) {
	dir := t.TempDir()
	writeTemplateFile(t, dir, ".erg/templates/simple.yaml", simpleTemplateYAML)

	cfg := minimalCfg(map[string]*State{
		"start": {
			Type: StateTypeTemplate,
			Use:  ".erg/templates/simple.yaml",
			Exits: map[string]string{
				"success":     "done",
				"undeclared":  "done", // template doesn't declare this exit
			},
		},
	})

	_, err := ExpandTemplates(cfg, dir)
	if err == nil {
		t.Fatal("expected error for caller mapping undeclared exit")
	}
	if !strings.Contains(err.Error(), "not declared by template") {
		t.Errorf("expected 'not declared by template' error, got: %v", err)
	}
}

func TestExpandTemplates_FileNotFound(t *testing.T) {
	cfg := minimalCfg(map[string]*State{
		"start": {
			Type: StateTypeTemplate,
			Use:  ".erg/templates/nonexistent.yaml",
			Exits: map[string]string{"success": "done"},
		},
	})

	_, err := ExpandTemplates(cfg, t.TempDir())
	if err == nil {
		t.Fatal("expected error for missing template file")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}

func TestExpandTemplates_BuiltinIssueToMerge(t *testing.T) {
	cfg := minimalCfg(map[string]*State{
		"start": {
			Type: StateTypeTemplate,
			Use:  "builtin:issue-to-merge",
			Exits: map[string]string{
				"success": "done",
				"failure": "failed",
			},
		},
	})
	cfg.Start = "start"

	result, err := ExpandTemplates(cfg, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// "start" should be a pass state pointing to the prefixed entry "coding".
	startState := result.States["start"]
	if startState == nil || startState.Type != StateTypePass {
		t.Fatalf("start state should be a pass state, got %+v", startState)
	}
	if startState.Next != "_t_start_coding" {
		t.Errorf("start.next: got %q, want _t_start_coding", startState.Next)
	}

	// The prefixed coding state should exist.
	if _, ok := result.States["_t_start_coding"]; !ok {
		t.Error("_t_start_coding not found after builtin expansion")
	}

	// Exit wiring: _t_start_done → done.
	doneExit := result.States["_t_start_done"]
	if doneExit == nil || doneExit.Type != StateTypePass || doneExit.Next != "done" {
		t.Errorf("_t_start_done: expected pass→done, got %+v", doneExit)
	}
}

func TestExpandTemplates_BuiltinPlanThenCode(t *testing.T) {
	cfg := minimalCfg(map[string]*State{
		"start": {
			Type: StateTypeTemplate,
			Use:  "builtin:plan-then-code",
			Exits: map[string]string{
				"success": "done",
				"failure": "failed",
			},
		},
	})
	cfg.Start = "start"

	result, err := ExpandTemplates(cfg, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Entry point for plan-then-code is "planning".
	startState := result.States["start"]
	if startState == nil || startState.Type != StateTypePass {
		t.Fatalf("start should be pass state")
	}
	if startState.Next != "_t_start_planning" {
		t.Errorf("start.next: got %q, want _t_start_planning", startState.Next)
	}
}

func TestExpandTemplates_UnknownBuiltin(t *testing.T) {
	cfg := minimalCfg(map[string]*State{
		"start": {
			Type: StateTypeTemplate,
			Use:  "builtin:nonexistent",
			Exits: map[string]string{"success": "done"},
		},
	})

	_, err := ExpandTemplates(cfg, t.TempDir())
	if err == nil {
		t.Fatal("expected error for unknown builtin")
	}
	if !strings.Contains(err.Error(), "unknown built-in template") {
		t.Errorf("expected 'unknown built-in template' error, got: %v", err)
	}
}

func TestExpandTemplates_NoTemplates(t *testing.T) {
	// Config with no template states — should pass through unchanged.
	cfg := &Config{
		Start: "coding",
		Source: SourceConfig{
			Provider: "github",
			Filter:   FilterConfig{Label: "queued"},
		},
		States: map[string]*State{
			"coding": {Type: StateTypeTask, Action: "ai.code", Next: "done", Error: "failed"},
			"done":   {Type: StateTypeSucceed},
			"failed": {Type: StateTypeFail},
		},
	}

	result, err := ExpandTemplates(cfg, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.States) != 3 {
		t.Errorf("expected 3 states, got %d", len(result.States))
	}
	if result.States["coding"].Type != StateTypeTask {
		t.Error("coding state should remain unchanged")
	}
}

func TestExpandTemplates_ChoiceStateRefsRewritten(t *testing.T) {
	dir := t.TempDir()
	templateYAML := `
template: choice
entry: check
exits:
  yes: yes_terminal
  no: no_terminal
states:
  check:
    type: choice
    choices:
      - variable: flag
        equals: true
        next: yes_terminal
      - variable: flag
        equals: false
        next: no_terminal
    default: no_terminal
  yes_terminal:
    type: succeed
  no_terminal:
    type: fail
`
	writeTemplateFile(t, dir, ".erg/templates/choice.yaml", templateYAML)

	cfg := minimalCfg(map[string]*State{
		"start": {
			Type: StateTypeTemplate,
			Use:  ".erg/templates/choice.yaml",
			Exits: map[string]string{
				"yes": "done",
				"no":  "failed",
			},
		},
	})

	result, err := ExpandTemplates(cfg, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	checkState := result.States["_t_start_check"]
	if checkState == nil {
		t.Fatal("_t_start_check missing")
	}
	for i, rule := range checkState.Choices {
		if !strings.HasPrefix(rule.Next, "_t_start_") {
			t.Errorf("choice[%d].next: got %q, expected prefixed name", i, rule.Next)
		}
	}
	if !strings.HasPrefix(checkState.Default, "_t_start_") {
		t.Errorf("check.default: got %q, expected prefixed name", checkState.Default)
	}
}

func TestExpandTemplates_NestedTemplate(t *testing.T) {
	dir := t.TempDir()
	innerYAML := `
template: inner
entry: inner_step
exits:
  done: inner_done
states:
  inner_step:
    type: task
    action: ai.code
    next: inner_done
    error: inner_done
  inner_done:
    type: succeed
`
	outerYAML := `
template: outer
entry: outer_step
exits:
  success: outer_done
  failure: outer_failed
states:
  outer_step:
    type: template
    use: ".erg/templates/inner.yaml"
    exits:
      done: outer_done
  outer_done:
    type: succeed
  outer_failed:
    type: fail
`
	writeTemplateFile(t, dir, ".erg/templates/inner.yaml", innerYAML)
	writeTemplateFile(t, dir, ".erg/templates/outer.yaml", outerYAML)

	cfg := minimalCfg(map[string]*State{
		"start": {
			Type: StateTypeTemplate,
			Use:  ".erg/templates/outer.yaml",
			Exits: map[string]string{
				"success": "done",
				"failure": "failed",
			},
		},
	})

	result, err := ExpandTemplates(cfg, dir)
	if err != nil {
		t.Fatalf("unexpected error for nested template: %v", err)
	}

	// start → pass → _t_start_outer_step
	startState := result.States["start"]
	if startState == nil || startState.Type != StateTypePass {
		t.Fatal("start should be a pass state")
	}
	if startState.Next != "_t_start_outer_step" {
		t.Errorf("start.next: got %q, want _t_start_outer_step", startState.Next)
	}

	// outer_step itself was a template that got expanded — it should be a pass state.
	outerStep := result.States["_t_start_outer_step"]
	if outerStep == nil {
		t.Fatal("_t_start_outer_step missing")
	}
	if outerStep.Type != StateTypePass {
		t.Errorf("_t_start_outer_step should be a pass state (expanded template), got %q", outerStep.Type)
	}
}

func TestExpandTemplates_SanitizedPrefix(t *testing.T) {
	dir := t.TempDir()
	writeTemplateFile(t, dir, ".erg/templates/simple.yaml", simpleTemplateYAML)

	// State name with hyphens — should be sanitized in prefix.
	cfg := minimalCfg(map[string]*State{
		"my-template-state": {
			Type: StateTypeTemplate,
			Use:  ".erg/templates/simple.yaml",
			Exits: map[string]string{
				"success": "done",
				"failure": "failed",
			},
		},
	})
	cfg.Start = "my-template-state"

	result, err := ExpandTemplates(cfg, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Prefix should be _t_my_template_state_ (hyphens → underscores).
	if _, ok := result.States["_t_my_template_state_step1"]; !ok {
		t.Error("_t_my_template_state_step1 not found — prefix sanitization may be wrong")
	}
}

// TestExpandTemplates_LoaderIntegration verifies that LoadAndMergeWithFile
// expands template states when loading a workflow YAML file.
func TestExpandTemplates_LoaderIntegration(t *testing.T) {
	dir := t.TempDir()
	ergDir := filepath.Join(dir, ".erg")
	if err := os.MkdirAll(ergDir, 0o755); err != nil {
		t.Fatal(err)
	}
	tmplDir := filepath.Join(ergDir, "templates")
	if err := os.MkdirAll(tmplDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write template file.
	if err := os.WriteFile(filepath.Join(tmplDir, "simple.yaml"), []byte(simpleTemplateYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write workflow that uses the template.
	workflowYAML := `
workflow: test
start: impl

source:
  provider: github
  filter:
    label: queued

states:
  impl:
    type: template
    use: ".erg/templates/simple.yaml"
    exits:
      success: done
      failure: failed
  done:
    type: succeed
  failed:
    type: fail
`
	if err := os.WriteFile(filepath.Join(ergDir, "workflow.yaml"), []byte(workflowYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadAndMerge(dir)
	if err != nil {
		t.Fatalf("LoadAndMerge error: %v", err)
	}

	// impl should be a pass state after expansion.
	implState := cfg.States["impl"]
	if implState == nil {
		t.Fatal("impl state missing")
	}
	if implState.Type != StateTypePass {
		t.Errorf("impl.type after expansion: got %q, want pass", implState.Type)
	}

	// Validate the expanded config — should have no errors.
	errs := Validate(cfg)
	if len(errs) > 0 {
		for _, e := range errs {
			t.Errorf("validation error: %s: %s", e.Field, e.Message)
		}
	}
}

func TestValidate_TemplateStateValid(t *testing.T) {
	cfg := &Config{
		Start: "impl",
		Source: SourceConfig{
			Provider: "github",
			Filter:   FilterConfig{Label: "queued"},
		},
		States: map[string]*State{
			"impl": {
				Type: StateTypeTemplate,
				Use:  "builtin:issue-to-merge",
				Exits: map[string]string{
					"success": "done",
					"failure": "failed",
				},
			},
			"done":   {Type: StateTypeSucceed},
			"failed": {Type: StateTypeFail},
		},
	}

	errs := Validate(cfg)
	for _, e := range errs {
		// Only template-specific fields should be clean; other errors (like cycle
		// detection on unexpanded templates) are OK to ignore in this context.
		if strings.HasPrefix(e.Field, "states.impl.use") || strings.HasPrefix(e.Field, "states.impl.exits") {
			t.Errorf("unexpected validation error: %s: %s", e.Field, e.Message)
		}
	}
}

func TestValidate_TemplateStateMissingUse(t *testing.T) {
	cfg := &Config{
		Start: "impl",
		Source: SourceConfig{
			Provider: "github",
			Filter:   FilterConfig{Label: "queued"},
		},
		States: map[string]*State{
			"impl": {
				Type:  StateTypeTemplate,
				Exits: map[string]string{"success": "done"},
			},
			"done":   {Type: StateTypeSucceed},
			"failed": {Type: StateTypeFail},
		},
	}

	errs := Validate(cfg)
	var found bool
	for _, e := range errs {
		if e.Field == "states.impl.use" {
			found = true
		}
	}
	if !found {
		t.Error("expected validation error for missing use field")
	}
}

func TestValidate_TemplateStateMissingExits(t *testing.T) {
	cfg := &Config{
		Start: "impl",
		Source: SourceConfig{
			Provider: "github",
			Filter:   FilterConfig{Label: "queued"},
		},
		States: map[string]*State{
			"impl": {
				Type: StateTypeTemplate,
				Use:  "builtin:issue-to-merge",
				// No exits.
			},
			"done":   {Type: StateTypeSucceed},
			"failed": {Type: StateTypeFail},
		},
	}

	errs := Validate(cfg)
	var found bool
	for _, e := range errs {
		if e.Field == "states.impl.exits" {
			found = true
		}
	}
	if !found {
		t.Error("expected validation error for missing exits field")
	}
}

func TestValidate_TemplateStateExitBadRef(t *testing.T) {
	cfg := &Config{
		Start: "impl",
		Source: SourceConfig{
			Provider: "github",
			Filter:   FilterConfig{Label: "queued"},
		},
		States: map[string]*State{
			"impl": {
				Type: StateTypeTemplate,
				Use:  "builtin:issue-to-merge",
				Exits: map[string]string{
					"success": "nonexistent",
				},
			},
			"done":   {Type: StateTypeSucceed},
			"failed": {Type: StateTypeFail},
		},
	}

	errs := Validate(cfg)
	var found bool
	for _, e := range errs {
		if strings.HasPrefix(e.Field, "states.impl.exits.") {
			found = true
		}
	}
	if !found {
		t.Error("expected validation error for exit referencing non-existent state")
	}
}
