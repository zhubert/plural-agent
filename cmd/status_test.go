package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/workflow"
	"github.com/zhubert/erg/internal/config"
)

// ---- formatAgeAt ----

func TestFormatAgeAt_ZeroTime(t *testing.T) {
	got := formatAgeAt(time.Time{}, time.Now())
	if got != "—" {
		t.Errorf("expected '—' for zero time, got %q", got)
	}
}

func TestFormatAgeAt_Seconds(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	got := formatAgeAt(now.Add(-45*time.Second), now)
	if got != "45s" {
		t.Errorf("expected '45s', got %q", got)
	}
}

func TestFormatAgeAt_Minutes(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	got := formatAgeAt(now.Add(-7*time.Minute), now)
	if got != "7m" {
		t.Errorf("expected '7m', got %q", got)
	}
}

func TestFormatAgeAt_Hours(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	got := formatAgeAt(now.Add(-3*time.Hour), now)
	if got != "3h" {
		t.Errorf("expected '3h', got %q", got)
	}
}

func TestFormatAgeAt_Days(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	got := formatAgeAt(now.Add(-50*time.Hour), now)
	if got != "2d" {
		t.Errorf("expected '2d', got %q", got)
	}
}

func TestFormatAgeAt_BoundaryMinute(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	// Exactly 1 minute → "1m"
	got := formatAgeAt(now.Add(-time.Minute), now)
	if got != "1m" {
		t.Errorf("expected '1m' at boundary, got %q", got)
	}
}

func TestFormatAgeAt_BoundaryHour(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	// Exactly 1 hour → "1h"
	got := formatAgeAt(now.Add(-time.Hour), now)
	if got != "1h" {
		t.Errorf("expected '1h' at boundary, got %q", got)
	}
}

// ---- formatIssue ----

func TestFormatIssue_GitHub(t *testing.T) {
	item := &daemonstate.WorkItem{
		IssueRef: config.IssueRef{Source: "github", ID: "42", Title: "Fix the login bug"},
	}
	got := formatIssue(item)
	if got != "#42 Fix the login bug" {
		t.Errorf("unexpected issue format: %q", got)
	}
}

func TestFormatIssue_OtherProvider(t *testing.T) {
	item := &daemonstate.WorkItem{
		IssueRef: config.IssueRef{Source: "asana", ID: "ABC123", Title: "Add dark mode"},
	}
	got := formatIssue(item)
	if got != "ABC123 Add dark mode" {
		t.Errorf("unexpected issue format: %q", got)
	}
}

func TestFormatIssue_NoSource(t *testing.T) {
	item := &daemonstate.WorkItem{
		ID: "work-item-uuid-123",
	}
	got := formatIssue(item)
	if got != "work-item-uuid-123" {
		t.Errorf("expected item ID when no source, got %q", got)
	}
}

func TestFormatIssue_Truncation(t *testing.T) {
	item := &daemonstate.WorkItem{
		IssueRef: config.IssueRef{
			Source: "github",
			ID:     "99",
			Title:  "This is a very long issue title that exceeds thirty characters",
		},
	}
	got := formatIssue(item)
	if len(got) > 30 {
		t.Errorf("expected truncation to 30 chars, got %d chars: %q", len(got), got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("expected truncated string to end with '...', got %q", got)
	}
}

// ---- formatStep ----

func TestFormatStep_Active(t *testing.T) {
	item := &daemonstate.WorkItem{
		State:       daemonstate.WorkItemActive,
		CurrentStep: "coding",
	}
	got := formatStep(item)
	if got != "coding" {
		t.Errorf("expected 'coding', got %q", got)
	}
}

func TestFormatStep_Failed(t *testing.T) {
	item := &daemonstate.WorkItem{
		State:       daemonstate.WorkItemFailed,
		CurrentStep: "coding",
	}
	got := formatStep(item)
	if got != "(failed)" {
		t.Errorf("expected '(failed)', got %q", got)
	}
}

func TestFormatStep_Completed(t *testing.T) {
	item := &daemonstate.WorkItem{
		State:       daemonstate.WorkItemCompleted,
		CurrentStep: "done",
	}
	got := formatStep(item)
	if got != "done" {
		t.Errorf("expected 'done', got %q", got)
	}
}

func TestFormatStep_QueuedNoStep(t *testing.T) {
	item := &daemonstate.WorkItem{
		State: daemonstate.WorkItemQueued,
	}
	got := formatStep(item)
	if got != "(queued)" {
		t.Errorf("expected '(queued)', got %q", got)
	}
}

func TestFormatStep_QueuedWithStep(t *testing.T) {
	item := &daemonstate.WorkItem{
		State:       daemonstate.WorkItemQueued,
		CurrentStep: "coding",
	}
	got := formatStep(item)
	if got != "coding" {
		t.Errorf("expected 'coding' for queued item with step, got %q", got)
	}
}

// ---- printFooter ----

func TestPrintFooter_WithMaxConcurrent(t *testing.T) {
	var buf bytes.Buffer
	printFooter(&buf, 2, 3, 1, 48291, true)
	out := buf.String()
	if !strings.Contains(out, "Slots: 2/3 active") {
		t.Errorf("expected 'Slots: 2/3 active' in output: %q", out)
	}
	if !strings.Contains(out, "Queued: 1") {
		t.Errorf("expected 'Queued: 1' in output: %q", out)
	}
	if !strings.Contains(out, "Daemon PID: 48291 (running)") {
		t.Errorf("expected daemon PID in output: %q", out)
	}
}

func TestPrintFooter_NoMaxConcurrent(t *testing.T) {
	var buf bytes.Buffer
	printFooter(&buf, 1, 0, 0, 0, false)
	out := buf.String()
	if !strings.Contains(out, "Slots: 1 active") {
		t.Errorf("expected 'Slots: 1 active' in output: %q", out)
	}
	if !strings.Contains(out, "Daemon: not running") {
		t.Errorf("expected 'Daemon: not running' in output: %q", out)
	}
}

func TestPrintFooter_DeadDaemon(t *testing.T) {
	var buf bytes.Buffer
	printFooter(&buf, 0, 0, 0, 12345, false)
	out := buf.String()
	if !strings.Contains(out, "Daemon PID: 12345 (dead)") {
		t.Errorf("expected '(dead)' indicator in output: %q", out)
	}
}

// ---- printTableView ----

func TestPrintTableView_Basic(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	items := []*daemonstate.WorkItem{
		{
			IssueRef:      config.IssueRef{Source: "github", ID: "42", Title: "Fix login bug"},
			State:         daemonstate.WorkItemActive,
			CurrentStep:   "await_review",
			Phase:         "idle",
			StepEnteredAt: now.Add(-12 * time.Minute),
			PRURL:         "https://github.com/owner/repo/pull/87",
		},
		{
			IssueRef:      config.IssueRef{Source: "github", ID: "38", Title: "Add dark mode"},
			State:         daemonstate.WorkItemActive,
			CurrentStep:   "coding",
			Phase:         "async_pending",
			StepEnteredAt: now.Add(-3 * time.Minute),
		},
	}

	var buf bytes.Buffer
	printTableView(&buf, items)
	out := buf.String()

	if !strings.Contains(out, "ISSUE") {
		t.Error("expected ISSUE header in table output")
	}
	if !strings.Contains(out, "STEP") {
		t.Error("expected STEP header in table output")
	}
	if !strings.Contains(out, "#42 Fix login bug") {
		t.Errorf("expected issue #42 in output: %q", out)
	}
	if !strings.Contains(out, "await_review") {
		t.Errorf("expected step 'await_review' in output: %q", out)
	}
	if !strings.Contains(out, "https://github.com/owner/repo/pull/87") {
		t.Errorf("expected PR URL in output: %q", out)
	}
	if !strings.Contains(out, "—") {
		t.Errorf("expected '—' for missing PR URL in output: %q", out)
	}
}

func TestPrintTableView_FailedItem(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	items := []*daemonstate.WorkItem{
		{
			IssueRef:      config.IssueRef{Source: "github", ID: "10", Title: "Bad issue"},
			State:         daemonstate.WorkItemFailed,
			CurrentStep:   "coding",
			Phase:         "idle",
			StepEnteredAt: now.Add(-1 * time.Hour),
		},
	}

	var buf bytes.Buffer
	printTableView(&buf, items)
	out := buf.String()

	if !strings.Contains(out, "(failed)") {
		t.Errorf("expected '(failed)' in table output for failed item: %q", out)
	}
}

func TestPrintTableView_EmptyPhase(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	items := []*daemonstate.WorkItem{
		{
			IssueRef:      config.IssueRef{Source: "github", ID: "5", Title: "Test"},
			State:         daemonstate.WorkItemActive,
			CurrentStep:   "coding",
			Phase:         "", // empty phase should default to "idle"
			StepEnteredAt: now.Add(-2 * time.Minute),
		},
	}

	var buf bytes.Buffer
	printTableView(&buf, items)
	out := buf.String()

	if !strings.Contains(out, "idle") {
		t.Errorf("expected 'idle' for empty phase in output: %q", out)
	}
}

// ---- primaryWorkflowPath ----

func TestPrimaryWorkflowPath_Default(t *testing.T) {
	cfg := workflow.DefaultWorkflowConfig()
	path := primaryWorkflowPath(cfg)

	// Default happy path: coding → open_pr → await_ci → check_ci_result → await_review → merge → done
	expectedStart := "coding"
	if len(path) == 0 || path[0] != expectedStart {
		t.Errorf("expected path to start with %q, got %v", expectedStart, path)
	}

	// Verify key states appear in order
	stateIndex := make(map[string]int, len(path))
	for i, s := range path {
		stateIndex[s] = i
	}

	checkOrder := []struct{ before, after string }{
		{"coding", "open_pr"},
		{"open_pr", "await_ci"},
		{"await_ci", "check_ci_result"},
		{"check_ci_result", "await_review"},
		{"await_review", "merge"},
		{"merge", "done"},
	}
	for _, c := range checkOrder {
		bi, bOK := stateIndex[c.before]
		ai, aOK := stateIndex[c.after]
		if !bOK {
			t.Errorf("expected state %q in path, not found", c.before)
			continue
		}
		if !aOK {
			t.Errorf("expected state %q in path, not found", c.after)
			continue
		}
		if bi >= ai {
			t.Errorf("expected %q (index %d) before %q (index %d)", c.before, bi, c.after, ai)
		}
	}
}

func TestPrimaryWorkflowPath_NilConfig(t *testing.T) {
	path := primaryWorkflowPath(nil)
	if path != nil {
		t.Errorf("expected nil path for nil config, got %v", path)
	}
}

func TestPrimaryWorkflowPath_EmptyStart(t *testing.T) {
	cfg := &workflow.Config{
		Start:  "",
		States: map[string]*workflow.State{},
	}
	path := primaryWorkflowPath(cfg)
	if path != nil {
		t.Errorf("expected nil path for empty start, got %v", path)
	}
}

func TestPrimaryWorkflowPath_NoLoop(t *testing.T) {
	// Ensure cycle detection prevents infinite loop
	cfg := &workflow.Config{
		Start: "a",
		States: map[string]*workflow.State{
			"a": {Type: workflow.StateTypeTask, Next: "b"},
			"b": {Type: workflow.StateTypeTask, Next: "a"}, // cycle
		},
	}
	path := primaryWorkflowPath(cfg)
	if len(path) != 2 {
		t.Errorf("expected path length 2 with cycle detection, got %d: %v", len(path), path)
	}
}

func TestPrimaryWorkflowPath_TerminalState(t *testing.T) {
	cfg := &workflow.Config{
		Start: "start",
		States: map[string]*workflow.State{
			"start": {Type: workflow.StateTypeTask, Next: "done"},
			"done":  {Type: workflow.StateTypeSucceed},
		},
	}
	path := primaryWorkflowPath(cfg)
	if len(path) != 2 || path[0] != "start" || path[1] != "done" {
		t.Errorf("expected [start done], got %v", path)
	}
}

// ---- printMapView ----

func TestPrintMapView_DefaultConfig(t *testing.T) {
	cfg := workflow.DefaultWorkflowConfig()
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	items := []*daemonstate.WorkItem{
		{
			IssueRef:      config.IssueRef{Source: "github", ID: "38", Title: "Add dark mode"},
			State:         daemonstate.WorkItemActive,
			CurrentStep:   "coding",
			Phase:         "async_pending",
			StepEnteredAt: now.Add(-3 * time.Minute),
		},
		{
			IssueRef:      config.IssueRef{Source: "github", ID: "42", Title: "Fix login bug"},
			State:         daemonstate.WorkItemActive,
			CurrentStep:   "await_review",
			Phase:         "idle",
			StepEnteredAt: now.Add(-12 * time.Minute),
		},
	}

	var buf bytes.Buffer
	printMapView(&buf, items, cfg)
	out := buf.String()

	// All primary path states should appear
	for _, state := range []string{"coding", "open_pr", "await_ci", "await_review", "merge", "done"} {
		if !strings.Contains(out, state) {
			t.Errorf("expected state %q in map output: %q", state, out)
		}
	}

	// Items should be annotated at their step
	if !strings.Contains(out, "#38 Add dark mode") {
		t.Errorf("expected item #38 in map output: %q", out)
	}
	if !strings.Contains(out, "#42 Fix login bug") {
		t.Errorf("expected item #42 in map output: %q", out)
	}
}

func TestPrintMapView_EmptyItems(t *testing.T) {
	cfg := workflow.DefaultWorkflowConfig()
	var buf bytes.Buffer
	printMapView(&buf, nil, cfg)
	out := buf.String()

	// States should still appear even with no active items
	if !strings.Contains(out, "coding") {
		t.Errorf("expected states in map output even with no items: %q", out)
	}
}

func TestPrintMapView_NilConfig(t *testing.T) {
	var buf bytes.Buffer
	printMapView(&buf, nil, nil)
	out := buf.String()
	if !strings.Contains(out, "no workflow config available") {
		t.Errorf("expected fallback message for nil config: %q", out)
	}
}

func TestPrintMapView_OffPathItem(t *testing.T) {
	cfg := workflow.DefaultWorkflowConfig()
	items := []*daemonstate.WorkItem{
		{
			IssueRef:    config.IssueRef{Source: "github", ID: "99", Title: "CI fix"},
			State:       daemonstate.WorkItemActive,
			CurrentStep: "fix_ci", // off the primary happy path
			Phase:       "async_pending",
		},
	}

	var buf bytes.Buffer
	printMapView(&buf, items, cfg)
	out := buf.String()

	// Off-path step with items should still appear
	if !strings.Contains(out, "fix_ci") {
		t.Errorf("expected off-path step 'fix_ci' to appear in map output: %q", out)
	}
	if !strings.Contains(out, "#99 CI fix") {
		t.Errorf("expected off-path item label in map output: %q", out)
	}
}

// ---- runStatus edge cases ----

func TestRunStatus_NoStateFile(t *testing.T) {
	setupAgentCleanTest(t)

	// Use a repo path that has no state file
	statusRepo = "/nonexistent/test/repo"
	defer func() {
		statusRepo = ""
	}()

	cmd := statusCmd
	err := runStatus(cmd, nil)
	if err != nil {
		t.Fatalf("expected no error for missing state file, got: %v", err)
	}
}

// ---- formatCellInfo ----

func TestFormatCellInfo_ActiveWithPhase(t *testing.T) {
	item := &daemonstate.WorkItem{
		State:         daemonstate.WorkItemActive,
		Phase:         "async_pending",
		StepEnteredAt: time.Now().Add(-5 * time.Minute),
	}
	got := formatCellInfo(item)
	if !strings.Contains(got, "async_pending") {
		t.Errorf("expected phase in cell info, got %q", got)
	}
	if !strings.Contains(got, "5m") {
		t.Errorf("expected age in cell info, got %q", got)
	}
}

func TestFormatCellInfo_EmptyPhaseDefaultsToIdle(t *testing.T) {
	item := &daemonstate.WorkItem{
		State:         daemonstate.WorkItemActive,
		Phase:         "",
		StepEnteredAt: time.Now().Add(-10 * time.Minute),
	}
	got := formatCellInfo(item)
	if !strings.Contains(got, "idle") {
		t.Errorf("expected 'idle' for empty phase, got %q", got)
	}
}

func TestFormatCellInfo_FailedItem(t *testing.T) {
	item := &daemonstate.WorkItem{
		State: daemonstate.WorkItemFailed,
		Phase: "idle",
	}
	got := formatCellInfo(item)
	if got != "(failed)" {
		t.Errorf("expected '(failed)' for failed item, got %q", got)
	}
}

func TestFormatCellInfo_ZeroStepTime(t *testing.T) {
	item := &daemonstate.WorkItem{
		State: daemonstate.WorkItemActive,
		Phase: "idle",
		// StepEnteredAt is zero
	}
	got := formatCellInfo(item)
	if !strings.Contains(got, "—") {
		t.Errorf("expected '—' for zero step time, got %q", got)
	}
}

// ---- printMatrixView ----

func TestPrintMatrixView_NilConfig(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	items := []*daemonstate.WorkItem{
		{
			IssueRef:      config.IssueRef{Source: "github", ID: "1", Title: "Test"},
			State:         daemonstate.WorkItemActive,
			CurrentStep:   "coding",
			Phase:         "async_pending",
			StepEnteredAt: now.Add(-2 * time.Minute),
		},
	}
	var buf bytes.Buffer
	printMatrixView(&buf, items, nil)
	out := buf.String()
	// Falls back to table view with "no workflow config available" message
	if !strings.Contains(out, "no workflow config available") {
		t.Errorf("expected fallback message for nil config: %q", out)
	}
}

func TestPrintMatrixView_HeaderAndSeparator(t *testing.T) {
	cfg := workflow.DefaultWorkflowConfig()
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	items := []*daemonstate.WorkItem{
		{
			IssueRef:      config.IssueRef{Source: "github", ID: "42", Title: "Fix login"},
			State:         daemonstate.WorkItemActive,
			CurrentStep:   "coding",
			Phase:         "async_pending",
			StepEnteredAt: now.Add(-3 * time.Minute),
		},
	}
	var buf bytes.Buffer
	printMatrixView(&buf, items, cfg)
	out := buf.String()

	// Header row should contain STATE and the issue label
	if !strings.Contains(out, "STATE") {
		t.Errorf("expected 'STATE' in matrix header: %q", out)
	}
	if !strings.Contains(out, "#42 Fix login") {
		t.Errorf("expected issue label in matrix header: %q", out)
	}
	// Separator line should contain dashes
	if !strings.Contains(out, "─") {
		t.Errorf("expected separator line in matrix: %q", out)
	}
}

func TestPrintMatrixView_CellPopulatedAtCorrectState(t *testing.T) {
	cfg := workflow.DefaultWorkflowConfig()
	items := []*daemonstate.WorkItem{
		{
			IssueRef:      config.IssueRef{Source: "github", ID: "10", Title: "Issue A"},
			State:         daemonstate.WorkItemActive,
			CurrentStep:   "await_review",
			Phase:         "idle",
			StepEnteredAt: time.Now().Add(-20 * time.Minute),
		},
	}
	var buf bytes.Buffer
	printMatrixView(&buf, items, cfg)
	out := buf.String()

	// The cell should show up on the await_review row
	lines := strings.Split(out, "\n")
	var reviewLine string
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "await_review") {
			reviewLine = line
			break
		}
	}
	if reviewLine == "" {
		t.Fatalf("could not find await_review row in output: %q", out)
	}
	if !strings.Contains(reviewLine, "idle") {
		t.Errorf("expected cell info 'idle' on await_review row: %q", reviewLine)
	}
	if !strings.Contains(reviewLine, "20m") {
		t.Errorf("expected age '20m' on await_review row: %q", reviewLine)
	}
}

func TestPrintMatrixView_MultipleItems(t *testing.T) {
	cfg := workflow.DefaultWorkflowConfig()
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	items := []*daemonstate.WorkItem{
		{
			IssueRef:      config.IssueRef{Source: "github", ID: "1", Title: "First"},
			State:         daemonstate.WorkItemActive,
			CurrentStep:   "coding",
			Phase:         "async_pending",
			StepEnteredAt: now.Add(-5 * time.Minute),
			CreatedAt:     now.Add(-10 * time.Minute),
		},
		{
			IssueRef:      config.IssueRef{Source: "github", ID: "2", Title: "Second"},
			State:         daemonstate.WorkItemActive,
			CurrentStep:   "await_ci",
			Phase:         "idle",
			StepEnteredAt: now.Add(-30 * time.Minute),
			CreatedAt:     now.Add(-40 * time.Minute),
		},
	}
	var buf bytes.Buffer
	printMatrixView(&buf, items, cfg)
	out := buf.String()

	// Both items should appear in the header
	if !strings.Contains(out, "#1 First") {
		t.Errorf("expected '#1 First' in header: %q", out)
	}
	if !strings.Contains(out, "#2 Second") {
		t.Errorf("expected '#2 Second' in header: %q", out)
	}

	// The coding row should have cell for item 1
	lines := strings.Split(out, "\n")
	var codingLine string
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "coding") {
			codingLine = line
			break
		}
	}
	if !strings.Contains(codingLine, "async_pending") {
		t.Errorf("expected async_pending in coding row: %q", codingLine)
	}
}

func TestPrintMatrixView_OffPathStep(t *testing.T) {
	cfg := workflow.DefaultWorkflowConfig()
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	items := []*daemonstate.WorkItem{
		{
			IssueRef:      config.IssueRef{Source: "github", ID: "99", Title: "CI fail"},
			State:         daemonstate.WorkItemActive,
			CurrentStep:   "fix_ci", // off the primary happy path
			Phase:         "async_pending",
			StepEnteredAt: now.Add(-8 * time.Minute),
		},
	}
	var buf bytes.Buffer
	printMatrixView(&buf, items, cfg)
	out := buf.String()

	// Off-path step should appear in output with dotted separator
	if !strings.Contains(out, "fix_ci") {
		t.Errorf("expected off-path step 'fix_ci' in matrix output: %q", out)
	}
	if !strings.Contains(out, "·") {
		t.Errorf("expected dotted separator before off-path steps: %q", out)
	}
}

func TestPrintMatrixView_EmptyCurrentStepUsesStart(t *testing.T) {
	cfg := workflow.DefaultWorkflowConfig()
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	items := []*daemonstate.WorkItem{
		{
			IssueRef:      config.IssueRef{Source: "github", ID: "5", Title: "Queued"},
			State:         daemonstate.WorkItemQueued,
			CurrentStep:   "", // empty → should use cfg.Start ("coding")
			Phase:         "",
			StepEnteredAt: now.Add(-1 * time.Minute),
		},
	}
	var buf bytes.Buffer
	printMatrixView(&buf, items, cfg)
	out := buf.String()

	// Item should appear in the "coding" row (the start state)
	lines := strings.Split(out, "\n")
	var codingLine string
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "coding") {
			codingLine = line
			break
		}
	}
	if codingLine == "" {
		t.Fatalf("could not find coding row in output: %q", out)
	}
	// Cell should be non-empty (either "idle" or some phase info)
	// The coding row should have content beyond state name
	stateWidth := len("check_ci_result") // longest state in default workflow
	if len(codingLine) <= stateWidth {
		t.Errorf("expected coding row to have content beyond state name: %q", codingLine)
	}
}

// ---- printMatrixRow ----

func TestPrintMatrixRow_ItemAtState(t *testing.T) {
	cfg := workflow.DefaultWorkflowConfig()
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	items := []*daemonstate.WorkItem{
		{
			IssueRef:      config.IssueRef{Source: "github", ID: "7", Title: "Test"},
			State:         daemonstate.WorkItemActive,
			CurrentStep:   "coding",
			Phase:         "async_pending",
			StepEnteredAt: now.Add(-4 * time.Minute),
		},
	}
	var buf bytes.Buffer
	printMatrixRow(&buf, "coding", items, cfg, 20, 16)
	out := buf.String()
	if !strings.Contains(out, "async_pending") {
		t.Errorf("expected cell info when item is at state: %q", out)
	}
}

func TestPrintMatrixRow_ItemNotAtState(t *testing.T) {
	cfg := workflow.DefaultWorkflowConfig()
	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	items := []*daemonstate.WorkItem{
		{
			IssueRef:      config.IssueRef{Source: "github", ID: "7", Title: "Test"},
			State:         daemonstate.WorkItemActive,
			CurrentStep:   "coding",
			Phase:         "async_pending",
			StepEnteredAt: now.Add(-4 * time.Minute),
		},
	}
	var buf bytes.Buffer
	printMatrixRow(&buf, "await_review", items, cfg, 20, 16)
	out := buf.String()
	// Row should start with state name but cell should be blank (just spaces)
	if !strings.HasPrefix(strings.TrimRight(out, "\n"), "await_review") {
		t.Errorf("expected row to start with state name: %q", out)
	}
	if strings.Contains(out, "async_pending") {
		t.Errorf("expected empty cell when item is NOT at this state: %q", out)
	}
}

// ---- displaySummary ----

func TestDisplaySummary_NotRunning(t *testing.T) {
	setupAgentCleanTest(t)

	// Capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := displaySummary("/nonexistent/repo/for/test")

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	out := buf.String()

	if !strings.Contains(out, "Daemon: not running") {
		t.Errorf("expected 'Daemon: not running' in output, got: %q", out)
	}
}

func TestDisplaySummary_Running(t *testing.T) {
	_, stateDir := setupAgentCleanTest(t)

	// Create a fake lock file with our PID (which is definitely running)
	repo := "test/repo"
	lockPath := daemonstate.LockFilePath(repo)
	os.MkdirAll(filepath.Dir(lockPath), 0o755)
	os.WriteFile(lockPath, []byte(fmt.Sprintf("%d", os.Getpid())), 0o644)
	defer os.Remove(lockPath)

	// Create a state file
	stateFilePath := daemonstate.StateFilePath(repo)
	os.MkdirAll(filepath.Dir(stateFilePath), 0o755)
	os.WriteFile(stateFilePath, []byte(`{"version":1,"repo":"test/repo","work_items":{}}`), 0o644)
	defer os.Remove(stateFilePath)
	_ = stateDir

	// Capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := displaySummary(repo)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	out := buf.String()

	if !strings.Contains(out, "Daemon: running") {
		t.Errorf("expected 'Daemon: running' in output, got: %q", out)
	}
	if !strings.Contains(out, fmt.Sprintf("PID %d", os.Getpid())) {
		t.Errorf("expected PID in output, got: %q", out)
	}
	if !strings.Contains(out, "Repo:") {
		t.Errorf("expected 'Repo:' in output, got: %q", out)
	}
	if !strings.Contains(out, "Logs:") {
		t.Errorf("expected 'Logs:' in output, got: %q", out)
	}
}

// ---- formatUptime ----

func TestFormatUptime(t *testing.T) {
	tests := []struct {
		dur  time.Duration
		want string
	}{
		{30 * time.Second, "30s"},
		{5 * time.Minute, "5m"},
		{90 * time.Minute, "1h 30m"},
		{2*time.Hour + 15*time.Minute, "2h 15m"},
	}
	for _, tt := range tests {
		got := formatUptime(tt.dur)
		if got != tt.want {
			t.Errorf("formatUptime(%v) = %q, want %q", tt.dur, got, tt.want)
		}
	}
}
