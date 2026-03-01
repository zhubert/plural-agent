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

			if !strings.Contains(out, "notify_failed:") {
				t.Errorf("expected notify_failed state, got:\n%s", out)
			}

			// Check the notify_failed action matches provider
			// Find the notify_failed block and check its action
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

// TestGenerateWizardYAML_ReviewFeedbackLoop verifies the review feedback loop:
// check_review_result → address_review → push_review_fix → await_review
func TestGenerateWizardYAML_ReviewFeedbackLoop(t *testing.T) {
	cfg := defaultGitHubWizardConfig()
	out := GenerateWizardYAML(cfg)

	errs := mustParseAndValidate(t, out)
	if len(errs) > 0 {
		t.Errorf("generated YAML failed validation:")
		for _, e := range errs {
			t.Errorf("  %s: %s", e.Field, e.Message)
		}
	}

	if !strings.Contains(out, "check_review_result:") {
		t.Errorf("expected check_review_result state")
	}
	if !strings.Contains(out, "address_review:") {
		t.Errorf("expected address_review state")
	}
	if !strings.Contains(out, "action: ai.address_review") {
		t.Errorf("expected ai.address_review action")
	}
	if !strings.Contains(out, "push_review_fix:") {
		t.Errorf("expected push_review_fix state")
	}
	// check_review_result routes changes_requested to address_review
	if !strings.Contains(out, "next: address_review") {
		t.Errorf("expected changes_requested to route to address_review")
	}
}

// TestGenerateWizardYAML_ReviewTimeout verifies await_review has timeout and timeout_next.
func TestGenerateWizardYAML_ReviewTimeout(t *testing.T) {
	cfg := defaultGitHubWizardConfig()
	out := GenerateWizardYAML(cfg)

	errs := mustParseAndValidate(t, out)
	if len(errs) > 0 {
		t.Errorf("generated YAML failed validation:")
		for _, e := range errs {
			t.Errorf("  %s: %s", e.Field, e.Message)
		}
	}

	// Find the await_review block
	idx := strings.Index(out, "  await_review:")
	if idx < 0 {
		t.Fatalf("await_review state not found")
	}
	block := out[idx:]
	if !strings.Contains(block, "timeout: 48h") {
		t.Errorf("expected timeout: 48h in await_review")
	}
	if !strings.Contains(block, "timeout_next: review_overdue") {
		t.Errorf("expected timeout_next: review_overdue in await_review")
	}
	if !strings.Contains(out, "review_overdue:") {
		t.Errorf("expected review_overdue state")
	}
}

// TestGenerateWizardYAML_PRMergedExternally verifies check_review_result routes
// pr_merged_externally to the post-merge state.
func TestGenerateWizardYAML_PRMergedExternally(t *testing.T) {
	cfg := defaultGitHubWizardConfig()
	out := GenerateWizardYAML(cfg)

	errs := mustParseAndValidate(t, out)
	if len(errs) > 0 {
		t.Errorf("generated YAML failed validation:")
		for _, e := range errs {
			t.Errorf("  %s: %s", e.Field, e.Message)
		}
	}

	if !strings.Contains(out, "variable: pr_merged_externally") {
		t.Errorf("expected pr_merged_externally variable in check_review_result")
	}

	// Parse the YAML and check the check_review_result choice that has
	// pr_merged_externally routes to done (no completion section set)
	var parsed Config
	if err := yaml.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("failed to parse YAML: %v", err)
	}
	state, ok := parsed.States["check_review_result"]
	if !ok {
		t.Fatalf("check_review_result state not found")
	}
	found := false
	for _, choice := range state.Choices {
		if choice.Variable == "pr_merged_externally" {
			if choice.Next != "done" {
				t.Errorf("expected pr_merged_externally to route to done, got %q", choice.Next)
			}
			found = true
		}
	}
	if !found {
		t.Errorf("pr_merged_externally choice not found in check_review_result")
	}
}

// TestGenerateWizardYAML_CITimedOut verifies await_ci timeout routes to ci_timed_out.
func TestGenerateWizardYAML_CITimedOut(t *testing.T) {
	cfg := defaultGitHubWizardConfig()
	out := GenerateWizardYAML(cfg)

	errs := mustParseAndValidate(t, out)
	if len(errs) > 0 {
		t.Errorf("generated YAML failed validation:")
		for _, e := range errs {
			t.Errorf("  %s: %s", e.Field, e.Message)
		}
	}

	// await_ci should have timeout_next: ci_timed_out
	idx := strings.Index(out, "  await_ci:")
	if idx < 0 {
		t.Fatalf("await_ci state not found")
	}
	block := out[idx:]
	if !strings.Contains(block, "timeout_next: ci_timed_out") {
		t.Errorf("expected timeout_next: ci_timed_out in await_ci")
	}
	if !strings.Contains(out, "ci_timed_out:") {
		t.Errorf("expected ci_timed_out state")
	}
	if !strings.Contains(out, "CI has been running for over 2 hours") {
		t.Errorf("expected CI timeout message in ci_timed_out")
	}
}

// TestGenerateWizardYAML_CIUnfixable verifies ci_unfixable exists when FixCI=true
// and is absent when FixCI=false.
func TestGenerateWizardYAML_CIUnfixable(t *testing.T) {
	// FixCI=true → ci_unfixable should exist
	cfg := defaultGitHubWizardConfig()
	cfg.FixCI = true
	out := GenerateWizardYAML(cfg)

	errs := mustParseAndValidate(t, out)
	if len(errs) > 0 {
		t.Errorf("FixCI=true: generated YAML failed validation:")
		for _, e := range errs {
			t.Errorf("  %s: %s", e.Field, e.Message)
		}
	}

	if !strings.Contains(out, "ci_unfixable:") {
		t.Errorf("expected ci_unfixable state when FixCI=true")
	}
	if !strings.Contains(out, "CI fix exhausted after 3 rounds") {
		t.Errorf("expected CI fix exhausted message")
	}

	// FixCI=false → ci_unfixable should not exist
	cfg.FixCI = false
	out = GenerateWizardYAML(cfg)

	errs = mustParseAndValidate(t, out)
	if len(errs) > 0 {
		t.Errorf("FixCI=false: generated YAML failed validation:")
		for _, e := range errs {
			t.Errorf("  %s: %s", e.Field, e.Message)
		}
	}

	if strings.Contains(out, "ci_unfixable") {
		t.Errorf("should not have ci_unfixable when FixCI=false")
	}
}

// TestGenerateWizardYAML_PlanExpired verifies plan_expired state uses provider-specific action.
func TestGenerateWizardYAML_PlanExpired(t *testing.T) {
	tests := []struct {
		name           string
		provider       string
		expectedAction string
		project        string
		team           string
		label          string
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
				PlanFirst:   true,
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

			if !strings.Contains(out, "plan_expired:") {
				t.Errorf("expected plan_expired state when PlanFirst=true")
			}
			// Check plan_expired uses the right action
			idx := strings.Index(out, "  plan_expired:")
			if idx < 0 {
				t.Fatalf("plan_expired state not found")
			}
			block := out[idx:]
			if !strings.Contains(block, "action: "+tt.expectedAction) {
				t.Errorf("expected plan_expired action %q for %s", tt.expectedAction, tt.provider)
			}
		})
	}
}

// TestGenerateWizardYAML_Asana_Kanban verifies Asana kanban workflow.
func TestGenerateWizardYAML_Asana_Kanban(t *testing.T) {
	cfg := WizardConfig{
		Provider:          "asana",
		Project:           "123",
		Section:           "To do",
		Kanban:            true,
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

	// Source filter should have section, no label
	if strings.Contains(out, "    label:") {
		t.Errorf("kanban Asana should not have label in source filter, got:\n%s", out)
	}
	if !strings.Contains(out, `section: "To do"`) {
		t.Errorf("expected section: \"To do\" in source filter, got:\n%s", out)
	}

	// Should have move_to_in_review with asana.move_to_section
	if !strings.Contains(out, "move_to_in_review:") {
		t.Errorf("expected move_to_in_review state for kanban")
	}
	idx := strings.Index(out, "  move_to_in_review:")
	if idx >= 0 {
		block := out[idx:]
		if !strings.Contains(block, "asana.move_to_section") {
			t.Errorf("expected asana.move_to_section in move_to_in_review")
		}
		if !strings.Contains(block, `section: "In Review"`) {
			t.Errorf("expected section: \"In Review\" in move_to_in_review")
		}
	}
}

// TestGenerateWizardYAML_Asana_Kanban_PlanFirst verifies Asana kanban with planning.
func TestGenerateWizardYAML_Asana_Kanban_PlanFirst(t *testing.T) {
	cfg := WizardConfig{
		Provider:          "asana",
		Project:           "123",
		Section:           "To do",
		Kanban:            true,
		PlanFirst:         true,
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

	// Should have move_to_planned with asana.move_to_section "Planned"
	if !strings.Contains(out, "move_to_planned:") {
		t.Errorf("expected move_to_planned state")
	}
	idx := strings.Index(out, "  move_to_planned:")
	if idx >= 0 {
		block := out[idx:]
		if !strings.Contains(block, "asana.move_to_section") {
			t.Errorf("expected asana.move_to_section in move_to_planned")
		}
		if !strings.Contains(block, `section: "Planned"`) {
			t.Errorf("expected section: \"Planned\" in move_to_planned")
		}
	}

	// Should have await_doing with asana.in_section "Doing" and 7d timeout
	if !strings.Contains(out, "await_doing:") {
		t.Errorf("expected await_doing state")
	}
	idx = strings.Index(out, "  await_doing:")
	if idx >= 0 {
		block := out[idx:]
		if !strings.Contains(block, "asana.in_section") {
			t.Errorf("expected asana.in_section event in await_doing")
		}
		if !strings.Contains(block, `section: "Doing"`) {
			t.Errorf("expected section: \"Doing\" in await_doing")
		}
		if !strings.Contains(block, "timeout: 7d") {
			t.Errorf("expected timeout: 7d in await_doing")
		}
	}
}

// TestGenerateWizardYAML_Linear_Kanban verifies Linear kanban workflow.
func TestGenerateWizardYAML_Linear_Kanban(t *testing.T) {
	cfg := WizardConfig{
		Provider:        "linear",
		Label:           "queued",
		Team:            "team-abc",
		Kanban:          true,
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

	// Should still have label in source (Linear has no state-based source)
	if !strings.Contains(out, "label: queued") {
		t.Errorf("expected label in Linear kanban source filter")
	}

	// Should have move_to_in_review with linear.move_to_state
	if !strings.Contains(out, "move_to_in_review:") {
		t.Errorf("expected move_to_in_review state for kanban")
	}
	idx := strings.Index(out, "  move_to_in_review:")
	if idx >= 0 {
		block := out[idx:]
		if !strings.Contains(block, "linear.move_to_state") {
			t.Errorf("expected linear.move_to_state in move_to_in_review")
		}
		if !strings.Contains(block, `state: "In Review"`) {
			t.Errorf("expected state: \"In Review\" in move_to_in_review")
		}
	}
}

// TestGenerateWizardYAML_Linear_Kanban_PlanFirst verifies Linear kanban with planning.
func TestGenerateWizardYAML_Linear_Kanban_PlanFirst(t *testing.T) {
	cfg := WizardConfig{
		Provider:        "linear",
		Label:           "queued",
		Team:            "team-abc",
		Kanban:          true,
		PlanFirst:       true,
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

	// Should have move_to_planned with linear.move_to_state "Planned"
	if !strings.Contains(out, "move_to_planned:") {
		t.Errorf("expected move_to_planned state")
	}
	idx := strings.Index(out, "  move_to_planned:")
	if idx >= 0 {
		block := out[idx:]
		if !strings.Contains(block, "linear.move_to_state") {
			t.Errorf("expected linear.move_to_state in move_to_planned")
		}
		if !strings.Contains(block, `state: "Planned"`) {
			t.Errorf("expected state: \"Planned\" in move_to_planned")
		}
	}

	// Should have await_in_progress with linear.in_state "In Progress" and 7d timeout
	if !strings.Contains(out, "await_in_progress:") {
		t.Errorf("expected await_in_progress state")
	}
	idx = strings.Index(out, "  await_in_progress:")
	if idx >= 0 {
		block := out[idx:]
		if !strings.Contains(block, "linear.in_state") {
			t.Errorf("expected linear.in_state event in await_in_progress")
		}
		if !strings.Contains(block, `state: "In Progress"`) {
			t.Errorf("expected state: \"In Progress\" in await_in_progress")
		}
		if !strings.Contains(block, "timeout: 7d") {
			t.Errorf("expected timeout: 7d in await_in_progress")
		}
	}
}

// TestGenerateWizardYAML_SlackNotification verifies Slack notification states.
func TestGenerateWizardYAML_SlackNotification(t *testing.T) {
	cfg := defaultGitHubWizardConfig()
	cfg.NotifySlack = true
	cfg.SlackWebhook = "$SLACK_WEBHOOK_URL"

	out := GenerateWizardYAML(cfg)

	errs := mustParseAndValidate(t, out)
	if len(errs) > 0 {
		t.Errorf("generated YAML failed validation:")
		for _, e := range errs {
			t.Errorf("  %s: %s", e.Field, e.Message)
		}
	}

	if !strings.Contains(out, "notify_slack:") {
		t.Errorf("expected notify_slack state")
	}
	if !strings.Contains(out, "action: slack.notify") {
		t.Errorf("expected slack.notify action")
	}
	if !strings.Contains(out, "webhook_url: $SLACK_WEBHOOK_URL") {
		t.Errorf("expected webhook_url param")
	}

	// notify_failed should route to notify_slack, not failed
	idx := strings.Index(out, "  notify_failed:")
	if idx < 0 {
		t.Fatalf("notify_failed state not found")
	}
	block := out[idx:]
	if !strings.Contains(block, "next: notify_slack") {
		t.Errorf("expected notify_failed to route to notify_slack")
	}
}

// TestGenerateWizardYAML_NoSlack verifies no notify_slack state when Slack disabled.
func TestGenerateWizardYAML_NoSlack(t *testing.T) {
	cfg := defaultGitHubWizardConfig()
	out := GenerateWizardYAML(cfg)

	errs := mustParseAndValidate(t, out)
	if len(errs) > 0 {
		t.Errorf("generated YAML failed validation:")
		for _, e := range errs {
			t.Errorf("  %s: %s", e.Field, e.Message)
		}
	}

	if strings.Contains(out, "notify_slack") {
		t.Errorf("should not have notify_slack state when NotifySlack=false")
	}

	// notify_failed should route to failed
	idx := strings.Index(out, "  notify_failed:")
	if idx < 0 {
		t.Fatalf("notify_failed state not found")
	}
	block := out[idx:]
	if !strings.Contains(block, "next: failed") {
		t.Errorf("expected notify_failed to route to failed when no Slack")
	}
}
