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
	// Whole-value placeholders preserve the typed value from the override.
	if p.Int("max_turns", 0) != 25 {
		t.Errorf("max_turns param substitution: got %v, want 25", p.Raw("max_turns"))
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

func TestExpandTemplates_TypedParamSubstitution(t *testing.T) {
	dir := t.TempDir()
	templateYAML := `
template: typed
entry: step
exits:
  success: done
  failure: failed
params:
  - name: simplify
    default: false
  - name: count
    default: 10
states:
  step:
    type: task
    action: ai.code
    params:
      simplify: "{{simplify}}"
      count: "{{count}}"
      label: "prefix-{{count}}-suffix"
    next: done
    error: failed
  done:
    type: succeed
  failed:
    type: fail
`
	writeTemplateFile(t, dir, ".erg/templates/typed.yaml", templateYAML)

	t.Run("bool override preserved as typed value", func(t *testing.T) {
		cfg := minimalCfg(map[string]*State{
			"start": {
				Type:   StateTypeTemplate,
				Use:    ".erg/templates/typed.yaml",
				Params: map[string]any{"simplify": true},
				Exits:  map[string]string{"success": "done", "failure": "failed"},
			},
		})
		result, err := ExpandTemplates(cfg, dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		p := NewParamHelper(result.States["_t_start_step"].Params)
		if p.Bool("simplify", false) != true {
			t.Errorf("expected simplify=true (bool), got %v (%T)", p.Raw("simplify"), p.Raw("simplify"))
		}
	})

	t.Run("default bool preserved as typed value", func(t *testing.T) {
		cfg := minimalCfg(map[string]*State{
			"start": {
				Type:  StateTypeTemplate,
				Use:   ".erg/templates/typed.yaml",
				Exits: map[string]string{"success": "done", "failure": "failed"},
			},
		})
		result, err := ExpandTemplates(cfg, dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		p := NewParamHelper(result.States["_t_start_step"].Params)
		if p.Bool("simplify", true) != false {
			t.Errorf("expected simplify=false (default), got %v (%T)", p.Raw("simplify"), p.Raw("simplify"))
		}
	})

	t.Run("embedded placeholder still does string substitution", func(t *testing.T) {
		cfg := minimalCfg(map[string]*State{
			"start": {
				Type:   StateTypeTemplate,
				Use:    ".erg/templates/typed.yaml",
				Params: map[string]any{"count": 42},
				Exits:  map[string]string{"success": "done", "failure": "failed"},
			},
		})
		result, err := ExpandTemplates(cfg, dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		p := NewParamHelper(result.States["_t_start_step"].Params)
		// Whole-value placeholder: typed int preserved
		if p.Int("count", 0) != 42 {
			t.Errorf("expected count=42 (int), got %v (%T)", p.Raw("count"), p.Raw("count"))
		}
		// Embedded placeholder: string substitution
		if p.String("label", "") != "prefix-42-suffix" {
			t.Errorf("expected label=prefix-42-suffix, got %q", p.String("label", ""))
		}
	})
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
				"success":    "done",
				"undeclared": "done", // template doesn't declare this exit
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
			Type:  StateTypeTemplate,
			Use:   ".erg/templates/nonexistent.yaml",
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

func TestExpandTemplates_UnknownBuiltin(t *testing.T) {
	cfg := minimalCfg(map[string]*State{
		"start": {
			Type:  StateTypeTemplate,
			Use:   "builtin:nonexistent",
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
				Use:  "builtin:ci",
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
				Use:  "builtin:ci",
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

// TestExpandTemplates_NestedTemplateRefsRewritten verifies that state references
// introduced by nested template expansion are also correctly prefixed when the
// outer template is inlined into the caller config (comment 2 fix).
func TestExpandTemplates_NestedTemplateRefsRewritten(t *testing.T) {
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
		t.Fatalf("unexpected error: %v", err)
	}

	// The outer template's "outer_step" itself was a template that got expanded
	// into a pass state pointing to the prefixed inner entry.
	outerStep := result.States["_t_start_outer_step"]
	if outerStep == nil {
		t.Fatal("_t_start_outer_step missing")
	}
	if outerStep.Type != StateTypePass {
		t.Errorf("_t_start_outer_step type: got %q, want pass", outerStep.Type)
	}
	// outer_step should now point to the double-prefixed inner entry.
	wantNext := "_t_start__t_outer_step_inner_step"
	if outerStep.Next != wantNext {
		t.Errorf("_t_start_outer_step.next: got %q, want %q", outerStep.Next, wantNext)
	}

	// The inner step and its exit wiring should exist under the double-prefixed names.
	if _, ok := result.States["_t_start__t_outer_step_inner_step"]; !ok {
		t.Error("_t_start__t_outer_step_inner_step missing — nested refs not re-prefixed")
	}
	if _, ok := result.States["_t_start__t_outer_step_inner_done"]; !ok {
		t.Error("_t_start__t_outer_step_inner_done missing — nested refs not re-prefixed")
	}
}

// TestExpandTemplates_CanonicalCycleDetection verifies that the same template
// referenced via different path spellings (e.g. "a.yaml" vs "./a.yaml") is
// treated as the same template for cycle detection (comment 3 fix).
func TestExpandTemplates_CanonicalCycleDetection(t *testing.T) {
	dir := t.TempDir()

	// Template A references template B via a path with "./" prefix.
	// Template B references template A without the "./" prefix.
	// Both spellings should canonicalize to the same file → cycle detected.
	templateA := `
template: a
entry: step
exits:
  success: done
  failure: failed
states:
  step:
    type: template
    use: ".erg/templates/./b.yaml"
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
		t.Fatal("expected error for circular template reference via normalized paths")
	}
	if !strings.Contains(err.Error(), "circular") {
		t.Errorf("expected circular error, got: %v", err)
	}
}

// TestExpandTemplates_AbsolutePathRejected verifies that absolute template paths
// are rejected for security (comment 4 fix).
func TestExpandTemplates_AbsolutePathRejected(t *testing.T) {
	cfg := minimalCfg(map[string]*State{
		"start": {
			Type:  StateTypeTemplate,
			Use:   "/etc/passwd",
			Exits: map[string]string{"success": "done"},
		},
	})

	_, err := ExpandTemplates(cfg, t.TempDir())
	if err == nil {
		t.Fatal("expected error for absolute template path")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Errorf("expected 'absolute' error, got: %v", err)
	}
}

// TestExpandTemplates_PathTraversalRejected verifies that paths escaping the
// base directory via ".." are rejected (comment 4 fix).
func TestExpandTemplates_PathTraversalRejected(t *testing.T) {
	cfg := minimalCfg(map[string]*State{
		"start": {
			Type:  StateTypeTemplate,
			Use:   "../../etc/passwd",
			Exits: map[string]string{"success": "done"},
		},
	})

	_, err := ExpandTemplates(cfg, t.TempDir())
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Errorf("expected 'escapes' error, got: %v", err)
	}
}

// TestExpandTemplates_SanitizedPrefixCollision verifies that two template states
// whose names sanitize to the same identifier (e.g. "a-b" and "a_b") are both
// expanded without error, each getting a distinct prefix (comment 5 fix).
func TestExpandTemplates_SanitizedPrefixCollision(t *testing.T) {
	dir := t.TempDir()
	writeTemplateFile(t, dir, ".erg/templates/simple.yaml", simpleTemplateYAML)

	// "a-b" and "a_b" both sanitize to "a_b", so the second one would collide
	// if we used an error rather than a counter.
	cfg := minimalCfg(map[string]*State{
		"a-b": {
			Type: StateTypeTemplate,
			Use:  ".erg/templates/simple.yaml",
			Exits: map[string]string{
				"success": "done",
				"failure": "failed",
			},
		},
		"a_b": {
			Type: StateTypeTemplate,
			Use:  ".erg/templates/simple.yaml",
			Exits: map[string]string{
				"success": "done",
				"failure": "failed",
			},
		},
	})
	cfg.Start = "a-b"

	result, err := ExpandTemplates(cfg, dir)
	if err != nil {
		t.Fatalf("unexpected error for sanitized prefix collision: %v", err)
	}

	// Both should produce expanded pass states.
	ab := result.States["a-b"]
	if ab == nil || ab.Type != StateTypePass {
		t.Errorf("a-b should be a pass state after expansion, got %+v", ab)
	}
	a_b := result.States["a_b"]
	if a_b == nil || a_b.Type != StateTypePass {
		t.Errorf("a_b should be a pass state after expansion, got %+v", a_b)
	}

	// The two expansions must not clobber each other's prefixed states.
	if ab.Next == a_b.Next {
		t.Errorf("both expanded templates point to the same entry %q — prefixes collided", ab.Next)
	}
}

// TestLoadAndMerge_TemplateExitsToDefaultStates verifies that after the merge-
// before-expand fix (comment 6), template exits can reference default-provided
// states (like "done"/"failed") without having to re-declare them in the user
// workflow YAML.
func TestLoadAndMerge_TemplateExitsToDefaultStates(t *testing.T) {
	dir := t.TempDir()
	ergDir := dir + "/.erg"
	if err := os.MkdirAll(ergDir+"/templates", 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(ergDir+"/templates/simple.yaml", []byte(simpleTemplateYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	// Workflow that maps template exits to "done"/"failed" WITHOUT explicitly
	// declaring those states — they come from the default config via Merge.
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
`
	if err := os.WriteFile(ergDir+"/workflow.yaml", []byte(workflowYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadAndMerge(dir)
	if err != nil {
		t.Fatalf("LoadAndMerge error: %v", err)
	}

	// "impl" should be a pass state after expansion.
	implState := cfg.States["impl"]
	if implState == nil || implState.Type != StateTypePass {
		t.Fatalf("impl should be a pass state, got %+v", implState)
	}

	// Default states "done" and "failed" must be present.
	if _, ok := cfg.States["done"]; !ok {
		t.Error("done state missing — default states should be merged before expansion")
	}
	if _, ok := cfg.States["failed"]; !ok {
		t.Error("failed state missing — default states should be merged before expansion")
	}
}

// --- Modular builtin template tests ---

func TestExpandTemplates_BuiltinPlan(t *testing.T) {
	cfg := minimalCfg(map[string]*State{
		"start": {
			Type: StateTypeTemplate,
			Use:  "builtin:plan",
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

	// start should be a pass → _t_start_planning
	startState := result.States["start"]
	if startState == nil || startState.Type != StateTypePass {
		t.Fatalf("start should be pass, got %+v", startState)
	}
	if startState.Next != "_t_start_planning" {
		t.Errorf("start.next: got %q, want _t_start_planning", startState.Next)
	}

	// Planning state should exist.
	if _, ok := result.States["_t_start_planning"]; !ok {
		t.Error("_t_start_planning missing")
	}

	// Feedback loop: check_plan_feedback should route back to planning.
	check := result.States["_t_start_check_plan_feedback"]
	if check == nil {
		t.Fatal("_t_start_check_plan_feedback missing")
	}
	// plan_approved=false → planning (loop)
	foundLoop := false
	for _, c := range check.Choices {
		if c.Next == "_t_start_planning" {
			foundLoop = true
		}
	}
	if !foundLoop {
		t.Error("check_plan_feedback should loop back to _t_start_planning")
	}

	// Success exit should wire to caller's "done".
	exitDone := result.States["_t_start_plan_done"]
	if exitDone == nil || exitDone.Type != StateTypePass || exitDone.Next != "done" {
		t.Errorf("plan_done exit: expected pass→done, got %+v", exitDone)
	}
}

func TestExpandTemplates_BuiltinCode(t *testing.T) {
	cfg := minimalCfg(map[string]*State{
		"start": {
			Type: StateTypeTemplate,
			Use:  "builtin:code",
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

	coding := result.States["_t_start_coding"]
	if coding == nil {
		t.Fatal("_t_start_coding missing")
	}
	if coding.Action != "ai.code" {
		t.Errorf("coding.action: got %q, want ai.code", coding.Action)
	}
}

func TestExpandTemplates_BuiltinCodeSimplify(t *testing.T) {
	cfg := minimalCfg(map[string]*State{
		"start": {
			Type: StateTypeTemplate,
			Use:  "builtin:code",
			Params: map[string]any{
				"simplify": true,
			},
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

	coding := result.States["_t_start_coding"]
	if coding == nil {
		t.Fatal("_t_start_coding missing")
	}
	p := NewParamHelper(coding.Params)
	if p.Bool("simplify", false) != true {
		t.Errorf("simplify: got %v (%T), want true (bool)", p.Raw("simplify"), p.Raw("simplify"))
	}
}

func TestExpandTemplates_BuiltinCISimplify(t *testing.T) {
	cfg := minimalCfg(map[string]*State{
		"start": {
			Type: StateTypeTemplate,
			Use:  "builtin:ci",
			Params: map[string]any{
				"simplify": true,
			},
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

	// Check ai.fix_ci state
	fixCI := result.States["_t_start_fix_ci"]
	if fixCI == nil {
		t.Fatal("_t_start_fix_ci missing")
	}
	p := NewParamHelper(fixCI.Params)
	if p.Bool("simplify", false) != true {
		t.Errorf("fix_ci simplify: got %v (%T), want true (bool)", p.Raw("simplify"), p.Raw("simplify"))
	}

	// Check ai.resolve_conflicts state
	rc := result.States["_t_start_resolve_conflicts"]
	if rc == nil {
		t.Fatal("_t_start_resolve_conflicts missing")
	}
	p = NewParamHelper(rc.Params)
	if p.Bool("simplify", false) != true {
		t.Errorf("resolve_conflicts simplify: got %v (%T), want true (bool)", p.Raw("simplify"), p.Raw("simplify"))
	}
}

func TestExpandTemplates_BuiltinReviewSimplify(t *testing.T) {
	cfg := minimalCfg(map[string]*State{
		"start": {
			Type: StateTypeTemplate,
			Use:  "builtin:review",
			Params: map[string]any{
				"simplify": true,
			},
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

	ar := result.States["_t_start_address_review"]
	if ar == nil {
		t.Fatal("_t_start_address_review missing")
	}
	p := NewParamHelper(ar.Params)
	if p.Bool("simplify", false) != true {
		t.Errorf("address_review simplify: got %v (%T), want true (bool)", p.Raw("simplify"), p.Raw("simplify"))
	}
}

func TestExpandTemplates_BuiltinPR(t *testing.T) {
	cfg := minimalCfg(map[string]*State{
		"start": {
			Type: StateTypeTemplate,
			Use:  "builtin:pr",
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

	pr := result.States["_t_start_open_pr"]
	if pr == nil {
		t.Fatal("_t_start_open_pr missing")
	}
	if pr.Action != "github.create_pr" {
		t.Errorf("open_pr.action: got %q, want github.create_pr", pr.Action)
	}
	if len(pr.Retry) == 0 {
		t.Error("open_pr should have retry config")
	}
	// Default: draft should be false
	p := NewParamHelper(pr.Params)
	if p.Bool("draft", true) != false {
		t.Errorf("open_pr draft default: got %v, want false", p.Raw("draft"))
	}
}

func TestExpandTemplates_BuiltinPR_Draft(t *testing.T) {
	cfg := minimalCfg(map[string]*State{
		"start": {
			Type: StateTypeTemplate,
			Use:  "builtin:pr",
			Exits: map[string]string{
				"success": "done",
				"failure": "failed",
			},
			Params: map[string]any{
				"draft": true,
			},
		},
	})
	cfg.Start = "start"

	result, err := ExpandTemplates(cfg, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	pr := result.States["_t_start_open_pr"]
	if pr == nil {
		t.Fatal("_t_start_open_pr missing")
	}
	p := NewParamHelper(pr.Params)
	if p.Bool("draft", false) != true {
		t.Errorf("open_pr draft override: got %v (%T), want true (bool)", p.Raw("draft"), p.Raw("draft"))
	}
}

func TestExpandTemplates_BuiltinCI(t *testing.T) {
	cfg := minimalCfg(map[string]*State{
		"start": {
			Type: StateTypeTemplate,
			Use:  "builtin:ci",
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

	// Verify key states exist.
	for _, name := range []string{"await_ci", "check_ci_result", "rebase", "fix_ci", "ci_unfixable", "ci_timed_out"} {
		if _, ok := result.States["_t_start_"+name]; !ok {
			t.Errorf("_t_start_%s missing", name)
		}
	}

	// CI fix loop: fix_ci → push_ci_fix → await_ci
	pushFix := result.States["_t_start_push_ci_fix"]
	if pushFix == nil {
		t.Fatal("_t_start_push_ci_fix missing")
	}
	if pushFix.Next != "_t_start_await_ci" {
		t.Errorf("push_ci_fix.next: got %q, want _t_start_await_ci", pushFix.Next)
	}

	// Success exit wired to caller's "done".
	exitDone := result.States["_t_start_ci_done"]
	if exitDone == nil || exitDone.Type != StateTypePass || exitDone.Next != "done" {
		t.Errorf("ci_done exit: expected pass→done, got %+v", exitDone)
	}
}

func TestExpandTemplates_BuiltinReview(t *testing.T) {
	cfg := minimalCfg(map[string]*State{
		"start": {
			Type: StateTypeTemplate,
			Use:  "builtin:review",
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

	// Verify key states exist.
	for _, name := range []string{"await_review", "check_review_result", "address_review", "push_review_fix", "review_overdue"} {
		if _, ok := result.States["_t_start_"+name]; !ok {
			t.Errorf("_t_start_%s missing", name)
		}
	}

	// Review feedback loop: address_review → push_review_fix → await_review
	pushFix := result.States["_t_start_push_review_fix"]
	if pushFix == nil {
		t.Fatal("_t_start_push_review_fix missing")
	}
	if pushFix.Next != "_t_start_await_review" {
		t.Errorf("push_review_fix.next: got %q, want _t_start_await_review", pushFix.Next)
	}
}

func TestExpandTemplates_BuiltinMerge(t *testing.T) {
	cfg := minimalCfg(map[string]*State{
		"start": {
			Type: StateTypeTemplate,
			Use:  "builtin:merge",
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

	merge := result.States["_t_start_merge"]
	if merge == nil {
		t.Fatal("_t_start_merge missing")
	}
	if merge.Action != "github.merge" {
		t.Errorf("merge.action: got %q, want github.merge", merge.Action)
	}
}

func TestExpandTemplates_BuiltinAsanaMoveSection(t *testing.T) {
	cfg := minimalCfg(map[string]*State{
		"start": {
			Type: StateTypeTemplate,
			Use:  "builtin:asana_move_section",
			Exits: map[string]string{
				"success": "done",
				"failure": "failed",
			},
			Params: map[string]any{
				"section": "In Progress",
			},
		},
	})
	cfg.Start = "start"

	result, err := ExpandTemplates(cfg, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	move := result.States["_t_start_move"]
	if move == nil {
		t.Fatal("_t_start_move missing")
	}
	if move.Action != "asana.move_to_section" {
		t.Errorf("move.action: got %q, want asana.move_to_section", move.Action)
	}
	if move.Params["section"] != "In Progress" {
		t.Errorf("move.params.section: got %v, want In Progress", move.Params["section"])
	}

	exitDone := result.States["_t_start_move_done"]
	if exitDone == nil || exitDone.Type != StateTypePass || exitDone.Next != "done" {
		t.Errorf("move_done exit: expected pass→done, got %+v", exitDone)
	}
	exitFailed := result.States["_t_start_move_failed"]
	if exitFailed == nil || exitFailed.Type != StateTypePass || exitFailed.Next != "failed" {
		t.Errorf("move_failed exit: expected pass→failed, got %+v", exitFailed)
	}
}

func TestExpandTemplates_BuiltinLinearMoveState(t *testing.T) {
	cfg := minimalCfg(map[string]*State{
		"start": {
			Type: StateTypeTemplate,
			Use:  "builtin:linear_move_state",
			Exits: map[string]string{
				"success": "done",
				"failure": "failed",
			},
			Params: map[string]any{
				"state": "In Progress",
			},
		},
	})
	cfg.Start = "start"

	result, err := ExpandTemplates(cfg, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	move := result.States["_t_start_move"]
	if move == nil {
		t.Fatal("_t_start_move missing")
	}
	if move.Action != "linear.move_to_state" {
		t.Errorf("move.action: got %q, want linear.move_to_state", move.Action)
	}
	if move.Params["state"] != "In Progress" {
		t.Errorf("move.params.state: got %v, want In Progress", move.Params["state"])
	}

	exitDone := result.States["_t_start_move_done"]
	if exitDone == nil || exitDone.Type != StateTypePass || exitDone.Next != "done" {
		t.Errorf("move_done exit: expected pass→done, got %+v", exitDone)
	}
	exitFailed := result.States["_t_start_move_failed"]
	if exitFailed == nil || exitFailed.Type != StateTypePass || exitFailed.Next != "failed" {
		t.Errorf("move_failed exit: expected pass→failed, got %+v", exitFailed)
	}
}

func TestExpandTemplates_BuiltinAsanaAwaitSection(t *testing.T) {
	cfg := minimalCfg(map[string]*State{
		"start": {
			Type: StateTypeTemplate,
			Use:  "builtin:asana_await_section",
			Exits: map[string]string{
				"success": "done",
				"failure": "failed",
			},
			Params: map[string]any{
				"section": "Done",
			},
		},
	})
	cfg.Start = "start"

	result, err := ExpandTemplates(cfg, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	await := result.States["_t_start_await"]
	if await == nil {
		t.Fatal("_t_start_await missing")
	}
	if await.Event != "asana.in_section" {
		t.Errorf("await.event: got %q, want asana.in_section", await.Event)
	}
	if await.Params["section"] != "Done" {
		t.Errorf("await.params.section: got %v, want Done", await.Params["section"])
	}
	if await.Timeout == nil {
		t.Error("await should have a timeout")
	}

	exitDone := result.States["_t_start_await_done"]
	if exitDone == nil || exitDone.Type != StateTypePass || exitDone.Next != "done" {
		t.Errorf("await_done exit: expected pass→done, got %+v", exitDone)
	}
	exitFailed := result.States["_t_start_await_failed"]
	if exitFailed == nil || exitFailed.Type != StateTypePass || exitFailed.Next != "failed" {
		t.Errorf("await_failed exit: expected pass→failed, got %+v", exitFailed)
	}
}

func TestExpandTemplates_BuiltinLinearAwaitState(t *testing.T) {
	cfg := minimalCfg(map[string]*State{
		"start": {
			Type: StateTypeTemplate,
			Use:  "builtin:linear_await_state",
			Exits: map[string]string{
				"success": "done",
				"failure": "failed",
			},
			Params: map[string]any{
				"state": "Done",
			},
		},
	})
	cfg.Start = "start"

	result, err := ExpandTemplates(cfg, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	await := result.States["_t_start_await"]
	if await == nil {
		t.Fatal("_t_start_await missing")
	}
	if await.Event != "linear.in_state" {
		t.Errorf("await.event: got %q, want linear.in_state", await.Event)
	}
	if await.Params["state"] != "Done" {
		t.Errorf("await.params.state: got %v, want Done", await.Params["state"])
	}
	if await.Timeout == nil {
		t.Error("await should have a timeout")
	}

	exitDone := result.States["_t_start_await_done"]
	if exitDone == nil || exitDone.Type != StateTypePass || exitDone.Next != "done" {
		t.Errorf("await_done exit: expected pass→done, got %+v", exitDone)
	}
	exitFailed := result.States["_t_start_await_failed"]
	if exitFailed == nil || exitFailed.Type != StateTypePass || exitFailed.Next != "failed" {
		t.Errorf("await_failed exit: expected pass→failed, got %+v", exitFailed)
	}
}

// TestExpandTemplates_ModularComposition verifies that multiple modular templates
// can be composed into a complete workflow (the primary use case).
func TestExpandTemplates_ModularComposition(t *testing.T) {
	cfg := &Config{
		Workflow: "plan-then-code",
		Start:    "plan",
		Source: SourceConfig{
			Provider: "github",
			Filter:   FilterConfig{Label: "plan"},
		},
		States: map[string]*State{
			"plan": {
				Type: StateTypeTemplate,
				Use:  "builtin:plan",
				Exits: map[string]string{
					"success": "code",
					"failure": "notify_failed",
				},
			},
			"code": {
				Type: StateTypeTemplate,
				Use:  "builtin:code",
				Exits: map[string]string{
					"success": "pr",
					"failure": "notify_failed",
				},
			},
			"pr": {
				Type: StateTypeTemplate,
				Use:  "builtin:pr",
				Exits: map[string]string{
					"success": "ci",
					"failure": "notify_failed",
				},
			},
			"ci": {
				Type: StateTypeTemplate,
				Use:  "builtin:ci",
				Exits: map[string]string{
					"success": "review",
					"failure": "notify_failed",
				},
			},
			"review": {
				Type: StateTypeTemplate,
				Use:  "builtin:review",
				Exits: map[string]string{
					"success": "done",
					"failure": "notify_failed",
				},
			},
			"notify_failed": {
				Type:   StateTypeTask,
				Action: "github.comment_issue",
				Params: map[string]any{
					"body": "Task failed. Manual intervention required.",
				},
				Next:  "failed",
				Error: "failed",
			},
			"done": {
				Type: StateTypeSucceed,
			},
			"failed": {
				Type: StateTypeFail,
			},
		},
	}

	result, err := ExpandTemplates(cfg, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// All template states should be pass states after expansion.
	for _, name := range []string{"plan", "code", "pr", "ci", "review"} {
		s := result.States[name]
		if s == nil || s.Type != StateTypePass {
			t.Errorf("%s should be pass state after expansion, got %+v", name, s)
		}
	}

	// Non-template states should be preserved.
	if result.States["notify_failed"] == nil {
		t.Error("notify_failed should be preserved")
	}
	if result.States["done"] == nil {
		t.Error("done should be preserved")
	}

	// Validate the expanded config.
	errs := Validate(result)
	if len(errs) > 0 {
		for _, e := range errs {
			t.Errorf("validation error: %s: %s", e.Field, e.Message)
		}
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
				Use:  "builtin:ci",
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
