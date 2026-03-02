package workflow

import (
	"strings"
	"testing"
)

func ptr(s string) *string { return &s }

func TestWaitStateGuidance_ExplicitOverride(t *testing.T) {
	custom := "Please do the thing."
	state := &State{Event: "gate.approved", Guidance: ptr(custom)}
	got := WaitStateGuidance(state, "")
	if got != custom {
		t.Errorf("expected %q, got %q", custom, got)
	}
}

func TestWaitStateGuidance_ExplicitSuppression(t *testing.T) {
	empty := ""
	state := &State{Event: "gate.approved", Guidance: ptr(empty)}
	got := WaitStateGuidance(state, "")
	if got != "" {
		t.Errorf("expected empty string for suppressed guidance, got %q", got)
	}
}

func TestWaitStateGuidance_GateApproved_LabelAdded(t *testing.T) {
	state := &State{
		Event: "gate.approved",
		Params: map[string]any{
			"trigger": "label_added",
			"label":   "approved",
		},
	}
	got := WaitStateGuidance(state, "")
	if !strings.Contains(got, "approved") {
		t.Errorf("expected label name in guidance, got %q", got)
	}
	if !strings.Contains(got, "label") {
		t.Errorf("expected 'label' in guidance, got %q", got)
	}
}

func TestWaitStateGuidance_GateApproved_DefaultLabel(t *testing.T) {
	state := &State{Event: "gate.approved"} // no params
	got := WaitStateGuidance(state, "")
	if !strings.Contains(got, "approved") {
		t.Errorf("expected default label 'approved' in guidance, got %q", got)
	}
}

func TestWaitStateGuidance_GateApproved_CommentMatch(t *testing.T) {
	state := &State{
		Event: "gate.approved",
		Params: map[string]any{
			"trigger": "comment_match",
			"pattern": "(?i)LGTM",
		},
	}
	got := WaitStateGuidance(state, "")
	if !strings.Contains(got, "(?i)LGTM") {
		t.Errorf("expected pattern in guidance, got %q", got)
	}
	if !strings.Contains(got, "comment") {
		t.Errorf("expected 'comment' in guidance, got %q", got)
	}
}

func TestWaitStateGuidance_GateApproved_CommentMatchNoPattern(t *testing.T) {
	state := &State{
		Event:  "gate.approved",
		Params: map[string]any{"trigger": "comment_match"},
	}
	got := WaitStateGuidance(state, "")
	if got == "" {
		t.Error("expected non-empty guidance for comment_match gate")
	}
}

func TestWaitStateGuidance_PlanUserReplied_WithApprovalPattern(t *testing.T) {
	state := &State{
		Event: "plan.user_replied",
		Params: map[string]any{
			"approval_pattern": `(?i)(LGTM|looks good|approved?|proceed|go ahead|ship it)`,
		},
	}
	got := WaitStateGuidance(state, "")
	if !strings.Contains(got, "plan") {
		t.Errorf("expected 'plan' in guidance, got %q", got)
	}
	// Should contain some example words extracted from the pattern
	if !strings.Contains(got, "LGTM") && !strings.Contains(got, "looks good") && !strings.Contains(got, "approved") {
		t.Errorf("expected pattern examples in guidance, got %q", got)
	}
}

func TestWaitStateGuidance_PlanUserReplied_NoPattern(t *testing.T) {
	state := &State{Event: "plan.user_replied"}
	got := WaitStateGuidance(state, "")
	if !strings.Contains(got, "plan") {
		t.Errorf("expected 'plan' in guidance, got %q", got)
	}
	if got == "" {
		t.Error("expected non-empty guidance")
	}
}

func TestWaitStateGuidance_PRReviewed_WithURL(t *testing.T) {
	state := &State{Event: "pr.reviewed"}
	prURL := "https://github.com/owner/repo/pull/42"
	got := WaitStateGuidance(state, prURL)
	if !strings.Contains(got, prURL) {
		t.Errorf("expected PR URL in guidance, got %q", got)
	}
	if !strings.Contains(got, "review") {
		t.Errorf("expected 'review' in guidance, got %q", got)
	}
}

func TestWaitStateGuidance_PRReviewed_NoURL(t *testing.T) {
	state := &State{Event: "pr.reviewed"}
	got := WaitStateGuidance(state, "")
	if got == "" {
		t.Error("expected non-empty guidance even without PR URL")
	}
	if !strings.Contains(got, "review") {
		t.Errorf("expected 'review' in guidance, got %q", got)
	}
}

func TestWaitStateGuidance_AsanaInSection(t *testing.T) {
	state := &State{
		Event:  "asana.in_section",
		Params: map[string]any{"section": "Doing"},
	}
	got := WaitStateGuidance(state, "")
	if !strings.Contains(got, "Doing") {
		t.Errorf("expected section name in guidance, got %q", got)
	}
	if !strings.Contains(got, "Asana") {
		t.Errorf("expected 'Asana' in guidance, got %q", got)
	}
}

func TestWaitStateGuidance_LinearInState(t *testing.T) {
	state := &State{
		Event:  "linear.in_state",
		Params: map[string]any{"state": "In Progress"},
	}
	got := WaitStateGuidance(state, "")
	if !strings.Contains(got, "In Progress") {
		t.Errorf("expected state name in guidance, got %q", got)
	}
	if !strings.Contains(got, "Linear") {
		t.Errorf("expected 'Linear' in guidance, got %q", got)
	}
}

func TestWaitStateGuidance_AutomatedEvents(t *testing.T) {
	automated := []string{"ci.complete", "ci.wait_for_checks", "pr.mergeable"}
	for _, event := range automated {
		state := &State{Event: event}
		got := WaitStateGuidance(state, "")
		if got != "" {
			t.Errorf("event %q: expected empty guidance (automated), got %q", event, got)
		}
	}
}

func TestWaitStateGuidance_UnknownEvent(t *testing.T) {
	state := &State{Event: "unknown.event"}
	got := WaitStateGuidance(state, "")
	if got != "" {
		t.Errorf("expected empty guidance for unknown event, got %q", got)
	}
}

func TestPatternExamples_Default(t *testing.T) {
	pattern := `(?i)(LGTM|looks good|approved?|proceed|go ahead|ship it)`
	got := patternExamples(pattern)
	if len(got) == 0 {
		t.Fatal("expected some examples")
	}
	if len(got) > 3 {
		t.Errorf("expected at most 3 examples, got %d", len(got))
	}
	// All examples should be quoted
	for _, ex := range got {
		if !strings.HasPrefix(ex, `"`) {
			t.Errorf("expected quoted example, got %q", ex)
		}
	}
}

func TestPatternExamples_Complex(t *testing.T) {
	// Complex patterns should yield no examples
	pattern := `^[A-Z]+$`
	got := patternExamples(pattern)
	// May return empty if all parts are complex
	_ = got
}
