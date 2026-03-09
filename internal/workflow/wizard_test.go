package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// defaultGitHubWizardConfig returns a baseline wizard config for GitHub.
func defaultGitHubWizardConfig() WizardConfig {
	return WizardConfig{
		Provider:    "github",
		Label:       "queued",
		AutoMerge:   true,
		MergeMethod: "rebase",
	}
}

// mustParseAndValidate parses the YAML and runs Validate, returning any errors.
func mustParseAndValidate(t *testing.T, yamlStr string) []ValidationError {
	t.Helper()
	var cfg Config
	if err := yaml.Unmarshal([]byte(yamlStr), &cfg); err != nil {
		t.Fatalf("failed to parse generated YAML: %v\nYAML:\n%s", err, yamlStr)
	}
	return Validate(&cfg)
}

// TestGenerateWizardYAML_GitHub_Defaults verifies the simplest GitHub config
// generates valid YAML with expected structure.
func TestGenerateWizardYAML_GitHub_Defaults(t *testing.T) {
	cfg := defaultGitHubWizardConfig()
	out := GenerateWizardYAML(cfg)

	errs := mustParseAndValidate(t, out)
	if len(errs) > 0 {
		t.Errorf("generated YAML failed validation:")
		for _, e := range errs {
			t.Errorf("  %s: %s", e.Field, e.Message)
		}
	}

	if !strings.Contains(out, "workflow: ci-gate") {
		t.Errorf("expected workflow name ci-gate, got:\n%s", out)
	}
	if !strings.Contains(out, "start: code_phase") {
		t.Errorf("expected start: code_phase, got:\n%s", out)
	}
	if !strings.Contains(out, "provider: github") {
		t.Errorf("expected provider: github, got:\n%s", out)
	}
	if !strings.Contains(out, "label: queued") {
		t.Errorf("expected label: queued, got:\n%s", out)
	}
	if !strings.Contains(out, "use: builtin:code") {
		t.Errorf("expected builtin:code template, got:\n%s", out)
	}
	if !strings.Contains(out, "use: builtin:ci") {
		t.Errorf("expected builtin:ci template, got:\n%s", out)
	}
	if !strings.Contains(out, "use: builtin:review") {
		t.Errorf("expected builtin:review template, got:\n%s", out)
	}
	if !strings.Contains(out, "use: builtin:merge") {
		t.Errorf("expected builtin:merge template (AutoMerge=true), got:\n%s", out)
	}
	if !strings.Contains(out, "method: rebase") {
		t.Errorf("expected method: rebase in merge_phase params, got:\n%s", out)
	}
	// No reviewer requested
	if strings.Contains(out, "request_reviewer") {
		t.Errorf("should not have request_reviewer state when Reviewer is empty, got:\n%s", out)
	}
	// No planning
	if strings.Contains(out, "plan_phase") {
		t.Errorf("should not have plan_phase (PlanFirst=false), got:\n%s", out)
	}
}

// TestGenerateWizardYAML_GitHub_PlanFirst verifies planning states are included.
func TestGenerateWizardYAML_GitHub_PlanFirst(t *testing.T) {
	cfg := defaultGitHubWizardConfig()
	cfg.PlanFirst = true

	out := GenerateWizardYAML(cfg)

	errs := mustParseAndValidate(t, out)
	if len(errs) > 0 {
		t.Errorf("generated YAML failed validation:")
		for _, e := range errs {
			t.Errorf("  %s: %s", e.Field, e.Message)
		}
	}

	if !strings.Contains(out, "workflow: plan-then-code") {
		t.Errorf("expected workflow name plan-then-code, got:\n%s", out)
	}
	if !strings.Contains(out, "start: plan_phase") {
		t.Errorf("expected start: plan_phase, got:\n%s", out)
	}
	if !strings.Contains(out, "use: builtin:plan") {
		t.Errorf("expected builtin:plan template, got:\n%s", out)
	}
	if !strings.Contains(out, "plan_phase:") {
		t.Errorf("expected plan_phase state, got:\n%s", out)
	}
}

// TestGenerateWizardYAML_AutoMerge_True verifies merge_phase with method param is generated.
func TestGenerateWizardYAML_AutoMerge_True(t *testing.T) {
	cfg := defaultGitHubWizardConfig()
	cfg.AutoMerge = true
	cfg.MergeMethod = "squash"

	out := GenerateWizardYAML(cfg)

	errs := mustParseAndValidate(t, out)
	if len(errs) > 0 {
		t.Errorf("generated YAML failed validation:")
		for _, e := range errs {
			t.Errorf("  %s: %s", e.Field, e.Message)
		}
	}

	if !strings.Contains(out, "merge_phase:") {
		t.Errorf("expected merge_phase state when AutoMerge=true, got:\n%s", out)
	}
	if !strings.Contains(out, "use: builtin:merge") {
		t.Errorf("expected builtin:merge template, got:\n%s", out)
	}
	if !strings.Contains(out, "method: squash") {
		t.Errorf("expected method: squash in merge_phase params, got:\n%s", out)
	}
	// review_phase should exit to merge_phase
	if !strings.Contains(out, "success: merge_phase") {
		t.Errorf("expected review_phase success exit to merge_phase, got:\n%s", out)
	}
}

// TestGenerateWizardYAML_AutoMerge_False verifies no merge_phase, review exits to done.
func TestGenerateWizardYAML_AutoMerge_False(t *testing.T) {
	cfg := defaultGitHubWizardConfig()
	cfg.AutoMerge = false

	out := GenerateWizardYAML(cfg)

	errs := mustParseAndValidate(t, out)
	if len(errs) > 0 {
		t.Errorf("generated YAML failed validation:")
		for _, e := range errs {
			t.Errorf("  %s: %s", e.Field, e.Message)
		}
	}

	if strings.Contains(out, "merge_phase") {
		t.Errorf("should not have merge_phase when AutoMerge=false, got:\n%s", out)
	}
	// review_phase should exit directly to done
	if !strings.Contains(out, "success: done") {
		t.Errorf("expected review_phase success exit to done when AutoMerge=false, got:\n%s", out)
	}
}

// TestGenerateWizardYAML_GitHub_WithReviewer verifies request_reviewer state is generated.
func TestGenerateWizardYAML_GitHub_WithReviewer(t *testing.T) {
	cfg := defaultGitHubWizardConfig()
	cfg.Reviewer = "alice"

	out := GenerateWizardYAML(cfg)

	errs := mustParseAndValidate(t, out)
	if len(errs) > 0 {
		t.Errorf("generated YAML failed validation:")
		for _, e := range errs {
			t.Errorf("  %s: %s", e.Field, e.Message)
		}
	}

	if !strings.Contains(out, "request_reviewer:") {
		t.Errorf("expected request_reviewer state, got:\n%s", out)
	}
	if !strings.Contains(out, "reviewer: alice") {
		t.Errorf("expected reviewer: alice param, got:\n%s", out)
	}
	// CI phase should route to request_reviewer on success
	if !strings.Contains(out, "success: request_reviewer") {
		t.Errorf("expected ci_phase success to route to request_reviewer, got:\n%s", out)
	}
}

// TestGenerateWizardYAML_GitHub_SquashMerge verifies squash merge method is used.
func TestGenerateWizardYAML_GitHub_SquashMerge(t *testing.T) {
	cfg := defaultGitHubWizardConfig()
	cfg.MergeMethod = "squash"

	out := GenerateWizardYAML(cfg)

	errs := mustParseAndValidate(t, out)
	if len(errs) > 0 {
		t.Errorf("generated YAML failed validation:")
		for _, e := range errs {
			t.Errorf("  %s: %s", e.Field, e.Message)
		}
	}

	if !strings.Contains(out, "method: squash") {
		t.Errorf("expected method: squash, got:\n%s", out)
	}
}

// TestGenerateWizardYAML_GitHub_Containerized verifies containerized param is set in templates.
func TestGenerateWizardYAML_GitHub_Containerized(t *testing.T) {
	cfg := defaultGitHubWizardConfig()
	cfg.Containerized = true

	out := GenerateWizardYAML(cfg)

	errs := mustParseAndValidate(t, out)
	if len(errs) > 0 {
		t.Errorf("generated YAML failed validation:")
		for _, e := range errs {
			t.Errorf("  %s: %s", e.Field, e.Message)
		}
	}

	if !strings.Contains(out, "containerized: true") {
		t.Errorf("expected containerized: true in code_phase params, got:\n%s", out)
	}
}

// TestGenerateWizardYAML_GitHub_ContainerizedWithPlan verifies containerized in
// both planning and coding template states when PlanFirst is also set.
func TestGenerateWizardYAML_GitHub_ContainerizedWithPlan(t *testing.T) {
	cfg := defaultGitHubWizardConfig()
	cfg.Containerized = true
	cfg.PlanFirst = true

	out := GenerateWizardYAML(cfg)

	errs := mustParseAndValidate(t, out)
	if len(errs) > 0 {
		t.Errorf("generated YAML failed validation:")
		for _, e := range errs {
			t.Errorf("  %s: %s", e.Field, e.Message)
		}
	}

	// Both plan_phase and code_phase should have containerized: true
	count := strings.Count(out, "containerized: true")
	if count < 2 {
		t.Errorf("expected containerized: true in both plan_phase and code_phase params, got %d occurrences:\n%s", count, out)
	}
}

// TestGenerateWizardYAML_Asana_Defaults verifies Asana config generates valid YAML.
func TestGenerateWizardYAML_Asana_Defaults(t *testing.T) {
	cfg := WizardConfig{
		Provider:    "asana",
		Label:       "queued",
		Project:     "1234567890",
		AutoMerge:   true,
		MergeMethod: "rebase",
	}

	out := GenerateWizardYAML(cfg)

	errs := mustParseAndValidate(t, out)
	if len(errs) > 0 {
		t.Errorf("generated YAML failed validation:")
		for _, e := range errs {
			t.Errorf("  %s: %s", e.Field, e.Message)
		}
	}

	if !strings.Contains(out, "provider: asana") {
		t.Errorf("expected provider: asana, got:\n%s", out)
	}
	if !strings.Contains(out, `project: "1234567890"`) {
		t.Errorf("expected project GID, got:\n%s", out)
	}
	if !strings.Contains(out, "label: queued") {
		t.Errorf("expected label: queued, got:\n%s", out)
	}
	// No completion section set → no move_complete state
	if strings.Contains(out, "move_complete") {
		t.Errorf("should not have move_complete when CompletionSection is empty, got:\n%s", out)
	}
}

// TestGenerateWizardYAML_Asana_WithCompletion verifies Asana completion section tracking.
func TestGenerateWizardYAML_Asana_WithCompletion(t *testing.T) {
	cfg := WizardConfig{
		Provider:          "asana",
		Label:             "queued",
		Project:           "1234567890",
		AutoMerge:         true,
		MergeMethod:       "rebase",
		CompletionSection: "Done",
	}

	out := GenerateWizardYAML(cfg)

	errs := mustParseAndValidate(t, out)
	if len(errs) > 0 {
		t.Errorf("generated YAML failed validation:")
		for _, e := range errs {
			t.Errorf("  %s: %s", e.Field, e.Message)
		}
	}

	if !strings.Contains(out, "move_complete:") {
		t.Errorf("expected move_complete state, got:\n%s", out)
	}
	if !strings.Contains(out, "use: builtin:asana_move_section") {
		t.Errorf("expected builtin:asana_move_section in move_complete, got:\n%s", out)
	}
	if !strings.Contains(out, `section: "Done"`) {
		t.Errorf("expected section: \"Done\" param, got:\n%s", out)
	}
	// merge_phase or review_phase should route to move_complete
	if !strings.Contains(out, "success: move_complete") {
		t.Errorf("expected success exit to move_complete, got:\n%s", out)
	}
}

// TestGenerateWizardYAML_Asana_WithSection verifies the optional section filter is included.
func TestGenerateWizardYAML_Asana_WithSection(t *testing.T) {
	cfg := WizardConfig{
		Provider:    "asana",
		Label:       "queued",
		Project:     "1234567890",
		Section:     "Backlog",
		AutoMerge:   true,
		MergeMethod: "rebase",
	}

	out := GenerateWizardYAML(cfg)

	errs := mustParseAndValidate(t, out)
	if len(errs) > 0 {
		t.Errorf("generated YAML failed validation:")
		for _, e := range errs {
			t.Errorf("  %s: %s", e.Field, e.Message)
		}
	}

	if !strings.Contains(out, `section: "Backlog"`) {
		t.Errorf("expected section: \"Backlog\" in filter, got:\n%s", out)
	}
}

// TestGenerateWizardYAML_Linear_Defaults verifies Linear config generates valid YAML.
func TestGenerateWizardYAML_Linear_Defaults(t *testing.T) {
	cfg := WizardConfig{
		Provider:    "linear",
		Label:       "queued",
		Team:        "team-abc-123",
		AutoMerge:   true,
		MergeMethod: "rebase",
	}

	out := GenerateWizardYAML(cfg)

	errs := mustParseAndValidate(t, out)
	if len(errs) > 0 {
		t.Errorf("generated YAML failed validation:")
		for _, e := range errs {
			t.Errorf("  %s: %s", e.Field, e.Message)
		}
	}

	if !strings.Contains(out, "provider: linear") {
		t.Errorf("expected provider: linear, got:\n%s", out)
	}
	if !strings.Contains(out, `team: "team-abc-123"`) {
		t.Errorf("expected team ID, got:\n%s", out)
	}
}

// TestGenerateWizardYAML_Linear_WithCompletion verifies Linear completion state tracking.
func TestGenerateWizardYAML_Linear_WithCompletion(t *testing.T) {
	cfg := WizardConfig{
		Provider:        "linear",
		Label:           "queued",
		Team:            "team-abc-123",
		AutoMerge:       true,
		MergeMethod:     "rebase",
		CompletionState: "Done",
	}

	out := GenerateWizardYAML(cfg)

	errs := mustParseAndValidate(t, out)
	if len(errs) > 0 {
		t.Errorf("generated YAML failed validation:")
		for _, e := range errs {
			t.Errorf("  %s: %s", e.Field, e.Message)
		}
	}

	if !strings.Contains(out, "move_complete:") {
		t.Errorf("expected move_complete state, got:\n%s", out)
	}
	if !strings.Contains(out, "use: builtin:linear_move_state") {
		t.Errorf("expected builtin:linear_move_state in move_complete, got:\n%s", out)
	}
	if !strings.Contains(out, `state: "Done"`) {
		t.Errorf("expected state: \"Done\" param, got:\n%s", out)
	}
	// merge_phase or review_phase should route to move_complete
	if !strings.Contains(out, "success: move_complete") {
		t.Errorf("expected success exit to move_complete, got:\n%s", out)
	}
}

// TestGenerateWizardYAML_AllOptions verifies a maximally configured workflow
// (all features enabled) produces valid YAML.
func TestGenerateWizardYAML_AllOptions(t *testing.T) {
	cfg := WizardConfig{
		Provider:          "asana",
		Label:             "ready",
		Project:           "9876543210",
		Section:           "In Progress",
		PlanFirst:         true,
		AutoMerge:         true,
		Reviewer:          "bob",
		MergeMethod:       "squash",
		Containerized:     true,
		CompletionSection: "Shipped",
	}

	out := GenerateWizardYAML(cfg)

	errs := mustParseAndValidate(t, out)
	if len(errs) > 0 {
		t.Errorf("generated YAML failed validation:")
		for _, e := range errs {
			t.Errorf("  %s: %s", e.Field, e.Message)
		}
	}

	// Spot-check key features
	if !strings.Contains(out, "workflow: plan-then-code") {
		t.Errorf("expected plan-then-code workflow name")
	}
	if !strings.Contains(out, "use: builtin:plan") {
		t.Errorf("expected builtin:plan template")
	}
	if !strings.Contains(out, "use: builtin:ci") {
		t.Errorf("expected builtin:ci template")
	}
	if !strings.Contains(out, "reviewer: bob") {
		t.Errorf("expected reviewer: bob")
	}
	if !strings.Contains(out, "method: squash") {
		t.Errorf("expected method: squash")
	}
	if !strings.Contains(out, "use: builtin:asana_move_section") {
		t.Errorf("expected builtin:asana_move_section in move_complete")
	}
}

// TestGenerateWizardYAML_MergeMethodFallback verifies unknown merge methods default to rebase.
func TestGenerateWizardYAML_MergeMethodFallback(t *testing.T) {
	cfg := defaultGitHubWizardConfig()
	cfg.MergeMethod = "unknown"

	out := GenerateWizardYAML(cfg)

	errs := mustParseAndValidate(t, out)
	if len(errs) > 0 {
		t.Errorf("generated YAML failed validation:")
		for _, e := range errs {
			t.Errorf("  %s: %s", e.Field, e.Message)
		}
	}

	if !strings.Contains(out, "method: rebase") {
		t.Errorf("expected fallback to method: rebase for unknown method, got:\n%s", out)
	}
}

// TestGenerateWizardYAML_EmptyMergeMethod verifies empty merge method defaults to rebase.
func TestGenerateWizardYAML_EmptyMergeMethod(t *testing.T) {
	cfg := defaultGitHubWizardConfig()
	cfg.MergeMethod = ""

	out := GenerateWizardYAML(cfg)

	errs := mustParseAndValidate(t, out)
	if len(errs) > 0 {
		t.Errorf("generated YAML failed validation:")
		for _, e := range errs {
			t.Errorf("  %s: %s", e.Field, e.Message)
		}
	}

	if !strings.Contains(out, "method: rebase") {
		t.Errorf("expected default method: rebase for empty method, got:\n%s", out)
	}
}

// TestGenerateWizardYAML_HasTerminalStates verifies done and failed states always present.
func TestGenerateWizardYAML_HasTerminalStates(t *testing.T) {
	cfg := defaultGitHubWizardConfig()
	out := GenerateWizardYAML(cfg)

	if !strings.Contains(out, "  done:\n    type: succeed") {
		t.Errorf("expected done: succeed terminal state, got:\n%s", out)
	}
	if !strings.Contains(out, "  failed:\n    type: fail") {
		t.Errorf("expected failed: fail terminal state, got:\n%s", out)
	}
}

// TestWriteFromWizard_CreatesFile verifies WriteFromWizard writes to the correct path.
func TestWriteFromWizard_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	cfg := defaultGitHubWizardConfig()

	fp, err := WriteFromWizard(dir, cfg)
	if err != nil {
		t.Fatalf("WriteFromWizard failed: %v", err)
	}

	expected := filepath.Join(dir, ".erg", "workflow.yaml")
	if fp != expected {
		t.Errorf("expected path %q, got %q", expected, fp)
	}

	data, err := os.ReadFile(fp)
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}

	if len(data) == 0 {
		t.Error("written file should not be empty")
	}

	// Must be parseable
	var parsed Config
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Errorf("written file is not valid YAML: %v", err)
	}
}

// TestWriteFromWizard_ErrorsIfExists verifies WriteFromWizard does not overwrite.
func TestWriteFromWizard_ErrorsIfExists(t *testing.T) {
	dir := t.TempDir()
	cfg := defaultGitHubWizardConfig()

	// Write once
	if _, err := WriteFromWizard(dir, cfg); err != nil {
		t.Fatalf("first write failed: %v", err)
	}

	// Second write should fail
	_, err := WriteFromWizard(dir, cfg)
	if err == nil {
		t.Error("expected error when file already exists, got nil")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' error, got: %v", err)
	}
}

// TestWriteFromWizard_CreatesDirectory verifies WriteFromWizard creates .erg/ if needed.
func TestWriteFromWizard_CreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	cfg := defaultGitHubWizardConfig()

	fp, err := WriteFromWizard(dir, cfg)
	if err != nil {
		t.Fatalf("WriteFromWizard failed: %v", err)
	}

	if _, err := os.Stat(filepath.Dir(fp)); os.IsNotExist(err) {
		t.Errorf(".erg/ directory was not created")
	}
}

// TestGenerateWizardYAML_NotifyFailed_ProviderSpecific verifies notify_failed
// uses the correct provider-specific comment action.
func TestGenerateWizardYAML_NotifyFailed_ProviderSpecific(t *testing.T) {
	tests := []struct {
		name           string
		provider       string
		expectedAction string
		// extra fields needed for provider
		project string
		team    string
		label   string
	}{
		{
			name:           "github",
			provider:       "github",
			expectedAction: "github.comment_issue",
			label:          "queued",
		},
		{
			name:           "asana",
			provider:       "asana",
			expectedAction: "asana.comment",
			project:        "123",
			label:          "queued",
		},
		{
			name:           "linear",
			provider:       "linear",
			expectedAction: "linear.comment",
			team:           "team-abc",
			label:          "queued",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := WizardConfig{
				Provider:    tt.provider,
				Label:       tt.label,
				Project:     tt.project,
				Team:        tt.team,
				AutoMerge:   true,
				MergeMethod: "rebase",
			}
			out := GenerateWizardYAML(cfg)

			errs := mustParseAndValidate(t, out)
			if len(errs) > 0 {
				t.Errorf("generated YAML failed validation:")
				for _, e := range errs {
					t.Errorf("  %s: %s", e.Field, e.Message)
				}
			}

			if !strings.Contains(out, "notify_failed:") {
				t.Errorf("expected notify_failed state, got:\n%s", out)
			}

			// Check the notify_failed action matches provider
			idx := strings.Index(out, "notify_failed:")
			if idx < 0 {
				t.Fatalf("notify_failed state not found")
			}
			block := out[idx:]
			if !strings.Contains(block, "action: "+tt.expectedAction) {
				t.Errorf("expected notify_failed action %q for provider %s, got block:\n%s", tt.expectedAction, tt.provider, block)
			}
		})
	}
}

// TestGenerateWizardYAML_Asana_Kanban verifies Asana kanban workflow uses template references.
func TestGenerateWizardYAML_Asana_Kanban(t *testing.T) {
	cfg := WizardConfig{
		Provider:          "asana",
		Project:           "123",
		Section:           "To do",
		Kanban:            true,
		AutoMerge:         true,
		MergeMethod:       "rebase",
		CompletionSection: "Done",
	}

	out := GenerateWizardYAML(cfg)

	errs := mustParseAndValidate(t, out)
	if len(errs) > 0 {
		t.Errorf("generated YAML failed validation:")
		for _, e := range errs {
			t.Errorf("  %s: %s", e.Field, e.Message)
		}
	}

	// Source filter should have section, no label
	if strings.Contains(out, "    label:") {
		t.Errorf("kanban Asana should not have label in source filter, got:\n%s", out)
	}
	if !strings.Contains(out, `section: "To do"`) {
		t.Errorf("expected section: \"To do\" in source filter, got:\n%s", out)
	}

	// Should have move_to_in_review using builtin:asana_move_section
	if !strings.Contains(out, "move_to_in_review:") {
		t.Errorf("expected move_to_in_review state for kanban")
	}
	idx := strings.Index(out, "  move_to_in_review:")
	if idx >= 0 {
		block := out[idx:]
		if !strings.Contains(block, "use: builtin:asana_move_section") {
			t.Errorf("expected use: builtin:asana_move_section in move_to_in_review")
		}
		if !strings.Contains(block, `section: "In Review"`) {
			t.Errorf("expected section: \"In Review\" param in move_to_in_review")
		}
	}
}

// TestGenerateWizardYAML_Asana_Kanban_PlanFirst verifies Asana kanban with planning uses templates.
func TestGenerateWizardYAML_Asana_Kanban_PlanFirst(t *testing.T) {
	cfg := WizardConfig{
		Provider:          "asana",
		Project:           "123",
		Section:           "To do",
		Kanban:            true,
		PlanFirst:         true,
		AutoMerge:         true,
		MergeMethod:       "rebase",
		CompletionSection: "Done",
	}

	out := GenerateWizardYAML(cfg)

	errs := mustParseAndValidate(t, out)
	if len(errs) > 0 {
		t.Errorf("generated YAML failed validation:")
		for _, e := range errs {
			t.Errorf("  %s: %s", e.Field, e.Message)
		}
	}

	// Should have move_to_planned using builtin:asana_move_section
	if !strings.Contains(out, "move_to_planned:") {
		t.Errorf("expected move_to_planned state")
	}
	idx := strings.Index(out, "  move_to_planned:")
	if idx >= 0 {
		block := out[idx:]
		if !strings.Contains(block, "use: builtin:asana_move_section") {
			t.Errorf("expected use: builtin:asana_move_section in move_to_planned")
		}
		if !strings.Contains(block, `section: "Planned"`) {
			t.Errorf("expected section: \"Planned\" param in move_to_planned")
		}
	}

	// Should have await_doing using builtin:asana_await_section
	if !strings.Contains(out, "await_doing:") {
		t.Errorf("expected await_doing state")
	}
	idx = strings.Index(out, "  await_doing:")
	if idx >= 0 {
		block := out[idx:]
		if !strings.Contains(block, "use: builtin:asana_await_section") {
			t.Errorf("expected use: builtin:asana_await_section in await_doing")
		}
		if !strings.Contains(block, `section: "Doing"`) {
			t.Errorf("expected section: \"Doing\" param in await_doing")
		}
	}
}

// TestGenerateWizardYAML_Linear_Kanban verifies Linear kanban workflow uses template references.
func TestGenerateWizardYAML_Linear_Kanban(t *testing.T) {
	cfg := WizardConfig{
		Provider:        "linear",
		Label:           "queued",
		Team:            "team-abc",
		Kanban:          true,
		AutoMerge:       true,
		MergeMethod:     "rebase",
		CompletionState: "Done",
	}

	out := GenerateWizardYAML(cfg)

	errs := mustParseAndValidate(t, out)
	if len(errs) > 0 {
		t.Errorf("generated YAML failed validation:")
		for _, e := range errs {
			t.Errorf("  %s: %s", e.Field, e.Message)
		}
	}

	// Should still have label in source (Linear has no state-based source)
	if !strings.Contains(out, "label: queued") {
		t.Errorf("expected label in Linear kanban source filter")
	}

	// Should have move_to_in_review using builtin:linear_move_state
	if !strings.Contains(out, "move_to_in_review:") {
		t.Errorf("expected move_to_in_review state for kanban")
	}
	idx := strings.Index(out, "  move_to_in_review:")
	if idx >= 0 {
		block := out[idx:]
		if !strings.Contains(block, "use: builtin:linear_move_state") {
			t.Errorf("expected use: builtin:linear_move_state in move_to_in_review")
		}
		if !strings.Contains(block, `state: "In Review"`) {
			t.Errorf("expected state: \"In Review\" param in move_to_in_review")
		}
	}
}

// TestGenerateWizardYAML_Linear_Kanban_PlanFirst verifies Linear kanban with planning uses templates.
func TestGenerateWizardYAML_Linear_Kanban_PlanFirst(t *testing.T) {
	cfg := WizardConfig{
		Provider:        "linear",
		Label:           "queued",
		Team:            "team-abc",
		Kanban:          true,
		PlanFirst:       true,
		AutoMerge:       true,
		MergeMethod:     "rebase",
		CompletionState: "Done",
	}

	out := GenerateWizardYAML(cfg)

	errs := mustParseAndValidate(t, out)
	if len(errs) > 0 {
		t.Errorf("generated YAML failed validation:")
		for _, e := range errs {
			t.Errorf("  %s: %s", e.Field, e.Message)
		}
	}

	// Should have move_to_planned using builtin:linear_move_state "Planned"
	if !strings.Contains(out, "move_to_planned:") {
		t.Errorf("expected move_to_planned state")
	}
	idx := strings.Index(out, "  move_to_planned:")
	if idx >= 0 {
		block := out[idx:]
		if !strings.Contains(block, "use: builtin:linear_move_state") {
			t.Errorf("expected use: builtin:linear_move_state in move_to_planned")
		}
		if !strings.Contains(block, `state: "Planned"`) {
			t.Errorf("expected state: \"Planned\" param in move_to_planned")
		}
	}

	// Should have await_in_progress using builtin:linear_await_state "In Progress"
	if !strings.Contains(out, "await_in_progress:") {
		t.Errorf("expected await_in_progress state")
	}
	idx = strings.Index(out, "  await_in_progress:")
	if idx >= 0 {
		block := out[idx:]
		if !strings.Contains(block, "use: builtin:linear_await_state") {
			t.Errorf("expected use: builtin:linear_await_state in await_in_progress")
		}
		if !strings.Contains(block, `state: "In Progress"`) {
			t.Errorf("expected state: \"In Progress\" param in await_in_progress")
		}
	}
}
