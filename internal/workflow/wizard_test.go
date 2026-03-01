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
		FixCI:       true,
		AutoReview:  true,
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
	if !strings.Contains(out, "start: coding") {
		t.Errorf("expected start: coding, got:\n%s", out)
	}
	if !strings.Contains(out, "provider: github") {
		t.Errorf("expected provider: github, got:\n%s", out)
	}
	if !strings.Contains(out, "label: queued") {
		t.Errorf("expected label: queued, got:\n%s", out)
	}
	if !strings.Contains(out, "action: ai.code") {
		t.Errorf("expected ai.code action, got:\n%s", out)
	}
	if !strings.Contains(out, "action: ai.fix_ci") {
		t.Errorf("expected ai.fix_ci action (FixCI=true), got:\n%s", out)
	}
	if !strings.Contains(out, "auto_address: true") {
		t.Errorf("expected auto_address: true (AutoReview=true), got:\n%s", out)
	}
	if !strings.Contains(out, "method: rebase") {
		t.Errorf("expected method: rebase, got:\n%s", out)
	}
	// No reviewer requested
	if strings.Contains(out, "request_reviewer") {
		t.Errorf("should not have request_reviewer state when Reviewer is empty, got:\n%s", out)
	}
	// No planning
	if strings.Contains(out, "ai.plan") {
		t.Errorf("should not have ai.plan (PlanFirst=false), got:\n%s", out)
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
	if !strings.Contains(out, "start: planning") {
		t.Errorf("expected start: planning, got:\n%s", out)
	}
	if !strings.Contains(out, "action: ai.plan") {
		t.Errorf("expected ai.plan action, got:\n%s", out)
	}
	if !strings.Contains(out, "await_plan_feedback") {
		t.Errorf("expected await_plan_feedback state, got:\n%s", out)
	}
	if !strings.Contains(out, "check_plan_feedback") {
		t.Errorf("expected check_plan_feedback state, got:\n%s", out)
	}
	if !strings.Contains(out, "plan.user_replied") {
		t.Errorf("expected plan.user_replied event, got:\n%s", out)
	}
	if !strings.Contains(out, "approval_pattern") {
		t.Errorf("expected approval_pattern param, got:\n%s", out)
	}
}

// TestGenerateWizardYAML_GitHub_NoFixCI verifies CI failures go directly to failed.
func TestGenerateWizardYAML_GitHub_NoFixCI(t *testing.T) {
	cfg := defaultGitHubWizardConfig()
	cfg.FixCI = false

	out := GenerateWizardYAML(cfg)

	errs := mustParseAndValidate(t, out)
	if len(errs) > 0 {
		t.Errorf("generated YAML failed validation:")
		for _, e := range errs {
			t.Errorf("  %s: %s", e.Field, e.Message)
		}
	}

	if strings.Contains(out, "fix_ci") {
		t.Errorf("should not have fix_ci state when FixCI=false, got:\n%s", out)
	}
	// The check_ci state should route ci_failed to failed
	if !strings.Contains(out, "check_ci") {
		t.Errorf("expected check_ci state, got:\n%s", out)
	}
}

// TestGenerateWizardYAML_GitHub_NoAutoReview verifies auto_address is false.
func TestGenerateWizardYAML_GitHub_NoAutoReview(t *testing.T) {
	cfg := defaultGitHubWizardConfig()
	cfg.AutoReview = false

	out := GenerateWizardYAML(cfg)

	errs := mustParseAndValidate(t, out)
	if len(errs) > 0 {
		t.Errorf("generated YAML failed validation:")
		for _, e := range errs {
			t.Errorf("  %s: %s", e.Field, e.Message)
		}
	}

	if !strings.Contains(out, "auto_address: false") {
		t.Errorf("expected auto_address: false, got:\n%s", out)
	}
	if strings.Contains(out, "max_feedback_rounds") {
		t.Errorf("should not include max_feedback_rounds when auto_address=false, got:\n%s", out)
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

	if !strings.Contains(out, "request_reviewer") {
		t.Errorf("expected request_reviewer state, got:\n%s", out)
	}
	if !strings.Contains(out, "reviewer: alice") {
		t.Errorf("expected reviewer: alice param, got:\n%s", out)
	}
	// CI pass should route to request_reviewer, not await_review directly
	if !strings.Contains(out, "next: request_reviewer") {
		t.Errorf("expected ci_passed to route to request_reviewer, got:\n%s", out)
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

// TestGenerateWizardYAML_GitHub_Containerized verifies containerized param is set.
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
		t.Errorf("expected containerized: true in coding state, got:\n%s", out)
	}
}

// TestGenerateWizardYAML_GitHub_ContainerizedWithPlan verifies containerized in
// both planning and coding states when PlanFirst is also set.
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

	// Both ai.plan and ai.code should have containerized
	count := strings.Count(out, "containerized: true")
	if count < 2 {
		t.Errorf("expected containerized: true in both planning and coding states, got %d occurrences:\n%s", count, out)
	}
}

// TestGenerateWizardYAML_Asana_Defaults verifies Asana config generates valid YAML.
func TestGenerateWizardYAML_Asana_Defaults(t *testing.T) {
	cfg := WizardConfig{
		Provider:    "asana",
		Label:       "queued",
		Project:     "1234567890",
		FixCI:       true,
		AutoReview:  true,
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
		FixCI:             true,
		AutoReview:        true,
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

	if !strings.Contains(out, "move_complete") {
		t.Errorf("expected move_complete state, got:\n%s", out)
	}
	if !strings.Contains(out, "asana.move_to_section") {
		t.Errorf("expected asana.move_to_section action, got:\n%s", out)
	}
	if !strings.Contains(out, `section: "Done"`) {
		t.Errorf("expected section: \"Done\" param, got:\n%s", out)
	}
	// merge should go to move_complete instead of done
	if !strings.Contains(out, "next: move_complete") {
		t.Errorf("expected merge to route to move_complete, got:\n%s", out)
	}
}

// TestGenerateWizardYAML_Asana_WithSection verifies the optional section filter is included.
func TestGenerateWizardYAML_Asana_WithSection(t *testing.T) {
	cfg := WizardConfig{
		Provider:    "asana",
		Label:       "queued",
		Project:     "1234567890",
		Section:     "Backlog",
		FixCI:       true,
		AutoReview:  true,
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
		FixCI:       true,
		AutoReview:  true,
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
		FixCI:           true,
		AutoReview:      true,
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

	if !strings.Contains(out, "move_complete") {
		t.Errorf("expected move_complete state, got:\n%s", out)
	}
	if !strings.Contains(out, "linear.move_to_state") {
		t.Errorf("expected linear.move_to_state action, got:\n%s", out)
	}
	if !strings.Contains(out, `state: "Done"`) {
		t.Errorf("expected state: \"Done\" param, got:\n%s", out)
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
		FixCI:             true,
		AutoReview:        true,
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
	if !strings.Contains(out, "action: ai.plan") {
		t.Errorf("expected ai.plan action")
	}
	if !strings.Contains(out, "action: ai.fix_ci") {
		t.Errorf("expected ai.fix_ci action")
	}
	if !strings.Contains(out, "reviewer: bob") {
		t.Errorf("expected reviewer: bob")
	}
	if !strings.Contains(out, "method: squash") {
		t.Errorf("expected method: squash")
	}
	if !strings.Contains(out, "asana.move_to_section") {
		t.Errorf("expected asana.move_to_section")
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

// TestGenerateWizardYAML_RebaseStatesAlwaysPresent verifies rebase states are always
// included since merge conflicts can happen regardless of other settings.
func TestGenerateWizardYAML_RebaseStatesAlwaysPresent(t *testing.T) {
	cfg := defaultGitHubWizardConfig()
	cfg.FixCI = false

	out := GenerateWizardYAML(cfg)

	if !strings.Contains(out, "action: git.rebase") {
		t.Errorf("expected git.rebase action always present, got:\n%s", out)
	}
	if !strings.Contains(out, "action: ai.resolve_conflicts") {
		t.Errorf("expected ai.resolve_conflicts action always present, got:\n%s", out)
	}
	if !strings.Contains(out, "push_conflict_fix") {
		t.Errorf("expected push_conflict_fix state always present, got:\n%s", out)
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
