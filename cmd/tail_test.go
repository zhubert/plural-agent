package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhubert/erg/internal/claude"
	"github.com/zhubert/erg/internal/config"
	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/paths"
)

// ---- claude.FormatToolIcon ----

func TestFormatToolIcon_KnownTools(t *testing.T) {
	cases := []struct {
		tool string
		want string
	}{
		{"Read", "Reading"},
		{"Edit", "Editing"},
		{"Write", "Writing"},
		{"Glob", "Searching"},
		{"Grep", "Searching"},
		{"Bash", "Running"},
		{"Task", "Delegating"},
		{"WebFetch", "Fetching"},
		{"WebSearch", "Searching"},
		{"TodoWrite", "Updating todos"},
	}
	for _, c := range cases {
		got := claude.FormatToolIcon(c.tool)
		if got != c.want {
			t.Errorf("claude.FormatToolIcon(%q) = %q, want %q", c.tool, got, c.want)
		}
	}
}

func TestFormatToolIcon_Unknown(t *testing.T) {
	got := claude.FormatToolIcon("MyCustomTool")
	if !strings.HasPrefix(got, "Using") {
		t.Errorf("expected 'Using ...' for unknown tool, got %q", got)
	}
}

// ---- tailToolDesc ----

func TestTailToolDesc_ReadTool(t *testing.T) {
	input := json.RawMessage(`{"file_path": "/workspace/internal/foo/bar.go"}`)
	got := tailToolDesc("Read", input)
	if got != "bar.go" {
		t.Errorf("expected 'bar.go', got %q", got)
	}
}

func TestTailToolDesc_BashTool(t *testing.T) {
	input := json.RawMessage(`{"command": "go test ./..."}`)
	got := tailToolDesc("Bash", input)
	if got != "go test ./..." {
		t.Errorf("expected 'go test ./...', got %q", got)
	}
}

func TestTailToolDesc_GrepTool(t *testing.T) {
	input := json.RawMessage(`{"pattern": "func main"}`)
	got := tailToolDesc("Grep", input)
	if got != "func main" {
		t.Errorf("expected 'func main', got %q", got)
	}
}

func TestTailToolDesc_LongValue_Truncated(t *testing.T) {
	longVal := strings.Repeat("x", 50)
	input := json.RawMessage(`{"command": "` + longVal + `"}`)
	got := tailToolDesc("Bash", input)
	if len([]rune(got)) > 35 {
		t.Errorf("expected truncation to 35 chars, got %d: %q", len(got), got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("expected truncated string to end with '...', got %q", got)
	}
}

func TestTailToolDesc_EmptyInput(t *testing.T) {
	got := tailToolDesc("Read", json.RawMessage{})
	if got != "" {
		t.Errorf("expected empty string for empty input, got %q", got)
	}
}

func TestTailToolDesc_InvalidJSON(t *testing.T) {
	got := tailToolDesc("Read", json.RawMessage(`not-json`))
	if got != "" {
		t.Errorf("expected empty string for invalid JSON, got %q", got)
	}
}

func TestTailToolDesc_UnknownTool(t *testing.T) {
	input := json.RawMessage(`{"something": "value"}`)
	got := tailToolDesc("MyTool", input)
	// Unknown tool has no field mapping, returns ""
	if got != "" {
		t.Errorf("expected '' for unknown tool, got %q", got)
	}
}

// ---- fitLine ----

func TestFitLine_ExactWidth(t *testing.T) {
	got := fitLine("hello", 5)
	if got != "hello" {
		t.Errorf("expected 'hello', got %q", got)
	}
}

func TestFitLine_ShortPadded(t *testing.T) {
	got := fitLine("hi", 5)
	if got != "hi   " {
		t.Errorf("expected 'hi   ', got %q", got)
	}
	if len(got) != 5 {
		t.Errorf("expected length 5, got %d", len(got))
	}
}

func TestFitLine_LongTruncated(t *testing.T) {
	got := fitLine("hello world", 8)
	if len(got) != 8 {
		t.Errorf("expected length 8, got %d: %q", len(got), got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("expected truncated string to end with '...', got %q", got)
	}
}

func TestFitLine_VeryNarrow(t *testing.T) {
	// width < 3 → truncate without ellipsis
	got := fitLine("hello", 2)
	if len(got) != 2 {
		t.Errorf("expected length 2, got %d: %q", len(got), got)
	}
}

func TestFitLine_Empty(t *testing.T) {
	got := fitLine("", 5)
	if got != "     " {
		t.Errorf("expected 5 spaces, got %q", got)
	}
}

// ---- readStreamLogLines ----

// writeStreamLog writes a fake stream log to a temp directory and returns cleanup.
func writeStreamLog(t *testing.T, sessionID string, entries []map[string]any) func() {
	t.Helper()

	// Set up a temp dir for logs.
	// HOME must also be overridden so ~/.erg/ doesn't exist,
	// allowing the XDG_STATE_HOME override to take effect.
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_STATE_HOME", tmpDir)
	paths.Reset()

	logsDir := filepath.Join(tmpDir, "erg", "logs")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		t.Fatalf("failed to create logs dir: %v", err)
	}

	logPath := filepath.Join(logsDir, "stream-"+sessionID+".log")
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("failed to create stream log: %v", err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	for _, entry := range entries {
		if err := enc.Encode(entry); err != nil {
			t.Fatalf("failed to write log entry: %v", err)
		}
	}

	return func() {
		paths.Reset()
	}
}

func TestReadStreamLogLines_TextContent(t *testing.T) {
	cleanup := writeStreamLog(t, "sess-001", []map[string]any{
		{
			"type": "assistant",
			"message": map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "Hello, world!\nSecond line."},
				},
			},
		},
	})
	defer cleanup()

	lines, err := readStreamLogLines("sess-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 lines, got %d: %v", len(lines), lines)
	}
	if lines[0] != "Hello, world!" {
		t.Errorf("expected first line 'Hello, world!', got %q", lines[0])
	}
	if lines[1] != "Second line." {
		t.Errorf("expected second line 'Second line.', got %q", lines[1])
	}
}

func TestReadStreamLogLines_ToolUse(t *testing.T) {
	cleanup := writeStreamLog(t, "sess-002", []map[string]any{
		{
			"type": "assistant",
			"message": map[string]any{
				"content": []map[string]any{
					{
						"type":  "tool_use",
						"name":  "Read",
						"input": map[string]any{"file_path": "/workspace/main.go"},
					},
				},
			},
		},
	})
	defer cleanup()

	lines, err := readStreamLogLines("sess-002")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "Reading") {
		t.Errorf("expected 'Reading' in tool use line, got %q", lines[0])
	}
	if !strings.Contains(lines[0], "main.go") {
		t.Errorf("expected 'main.go' in tool use line, got %q", lines[0])
	}
}

func TestReadStreamLogLines_SkipsNonAssistant(t *testing.T) {
	cleanup := writeStreamLog(t, "sess-003", []map[string]any{
		{
			"type": "user",
			"message": map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "User message — should be skipped"},
				},
			},
		},
		{
			"type":    "result",
			"subtype": "success",
		},
	})
	defer cleanup()

	lines, err := readStreamLogLines("sess-003")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lines) != 0 {
		t.Errorf("expected 0 lines (non-assistant skipped), got %d: %v", len(lines), lines)
	}
}

func TestReadStreamLogLines_EmptySessionID(t *testing.T) {
	_, err := readStreamLogLines("")
	if err == nil {
		t.Error("expected error for empty session ID, got nil")
	}
}

func TestReadStreamLogLines_MissingFile(t *testing.T) {
	// Use a temp dir with no log files
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_STATE_HOME", tmpDir)
	paths.Reset()
	defer paths.Reset()

	_, err := readStreamLogLines("nonexistent-session")
	if err == nil {
		t.Error("expected error for missing log file, got nil")
	}
}

func TestReadStreamLogLines_SkipsBlankTextLines(t *testing.T) {
	cleanup := writeStreamLog(t, "sess-004", []map[string]any{
		{
			"type": "assistant",
			"message": map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "\n\n  \nActual content\n\n"},
				},
			},
		},
	})
	defer cleanup()

	lines, err := readStreamLogLines("sess-004")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			t.Errorf("expected no blank lines in output, got %q", line)
		}
	}
	if len(lines) != 1 || lines[0] != "Actual content" {
		t.Errorf("expected ['Actual content'], got %v", lines)
	}
}

// ---- renderTailView ----

func newWorkItem(id, title, step, phase, sessionID string) *daemonstate.WorkItem {
	return &daemonstate.WorkItem{
		ID:            id,
		IssueRef:      config.IssueRef{Source: "github", ID: id, Title: title},
		State:         daemonstate.WorkItemActive,
		CurrentStep:   step,
		Phase:         phase,
		SessionID:     sessionID,
		CreatedAt:     time.Now(),
		StepEnteredAt: time.Now(),
	}
}

func TestRenderTailView_NoItems(t *testing.T) {
	var buf bytes.Buffer
	renderTailView(&buf, nil, 24, 80)
	out := buf.String()
	if !strings.Contains(out, "No active") {
		t.Errorf("expected 'No active' message, got %q", out)
	}
}

func TestRenderTailView_SingleItem(t *testing.T) {
	item := newWorkItem("42", "Fix login bug", "coding", "async_pending", "")
	var buf bytes.Buffer
	renderTailView(&buf, []*daemonstate.WorkItem{item}, 24, 80)
	out := buf.String()

	// Header should contain the issue
	if !strings.Contains(out, "#42") {
		t.Errorf("expected '#42' in output, got %q", out)
	}
	// Subheader should contain step and phase
	if !strings.Contains(out, "coding") {
		t.Errorf("expected 'coding' in output, got %q", out)
	}
	if !strings.Contains(out, "async_pending") {
		t.Errorf("expected 'async_pending' in output, got %q", out)
	}
}

func TestRenderTailView_MultipleItems_HasSeparators(t *testing.T) {
	items := []*daemonstate.WorkItem{
		newWorkItem("1", "First issue", "coding", "idle", ""),
		newWorkItem("2", "Second issue", "await_review", "idle", ""),
	}
	var buf bytes.Buffer
	renderTailView(&buf, items, 24, 80)
	out := buf.String()

	// Should have column separators
	if !strings.Contains(out, "│") {
		t.Errorf("expected column separator '│' in output, got %q", out)
	}
	// Should have both issue headers
	if !strings.Contains(out, "#1") {
		t.Errorf("expected '#1' in output, got %q", out)
	}
	if !strings.Contains(out, "#2") {
		t.Errorf("expected '#2' in output, got %q", out)
	}
}

func TestRenderTailView_HasHorizontalSeparator(t *testing.T) {
	item := newWorkItem("5", "Test issue", "coding", "idle", "")
	var buf bytes.Buffer
	renderTailView(&buf, []*daemonstate.WorkItem{item}, 24, 80)
	out := buf.String()

	if !strings.Contains(out, "─") {
		t.Errorf("expected horizontal separator '─' in output, got %q", out)
	}
	if !strings.Contains(out, "┼") || true {
		// ┼ only appears when there are multiple columns — skip check for single column
	}
}

func TestRenderTailView_NoSessionID_ShowsWaiting(t *testing.T) {
	item := newWorkItem("7", "Queued item", "coding", "idle", "")
	item.SessionID = "" // ensure no session ID
	var buf bytes.Buffer
	renderTailView(&buf, []*daemonstate.WorkItem{item}, 24, 80)
	out := buf.String()

	if !strings.Contains(out, "waiting for session") {
		t.Errorf("expected 'waiting for session' for item without session ID, got %q", out)
	}
}

func TestRenderTailView_LineWidths_ConsistentPerRow(t *testing.T) {
	const (
		termCols = 90
		n        = 3
	)
	items := []*daemonstate.WorkItem{
		newWorkItem("1", "Alpha", "step_one", "idle", ""),
		newWorkItem("2", "Beta", "step_two", "idle", ""),
		newWorkItem("3", "Gamma", "step_three", "idle", ""),
	}
	var buf bytes.Buffer
	renderTailView(&buf, items, 10, termCols)
	out := buf.String()

	// Column width = (termCols - (n-1)) / n (integer division)
	// Total rendered width = colWidth*n + (n-1) separators
	colWidth := (termCols - (n - 1)) / n
	expectedWidth := colWidth*n + (n - 1)

	// Every non-empty line should have exactly the computed width
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if line == "" {
			continue
		}
		if len([]rune(line)) != expectedWidth {
			t.Errorf("line %d width = %d, expected %d: %q", i, len([]rune(line)), expectedWidth, line)
		}
	}
}

func TestRenderTailView_WithStreamLog(t *testing.T) {
	cleanup := writeStreamLog(t, "active-session", []map[string]any{
		{
			"type": "assistant",
			"message": map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "Implementing the feature now."},
					{
						"type":  "tool_use",
						"name":  "Bash",
						"input": map[string]any{"command": "go test ./..."},
					},
				},
			},
		},
	})
	defer cleanup()

	item := newWorkItem("10", "Add feature", "coding", "async_pending", "active-session")
	var buf bytes.Buffer
	renderTailView(&buf, []*daemonstate.WorkItem{item}, 24, 80)
	out := buf.String()

	if !strings.Contains(out, "Implementing the feature now.") {
		t.Errorf("expected text content in output, got %q", out)
	}
	if !strings.Contains(out, "Running") {
		t.Errorf("expected 'Running' tool verb in output, got %q", out)
	}
	if !strings.Contains(out, "go test") {
		t.Errorf("expected 'go test' in output, got %q", out)
	}
}

// ---- writeFrameFlickerFree ----

// captureStdout redirects os.Stdout to a pipe, calls f, and returns captured output.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	f()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	buf.ReadFrom(r)
	return buf.String()
}

func TestWriteFrameFlickerFree_StartsWithCursorHome(t *testing.T) {
	out := captureStdout(t, func() {
		writeFrameFlickerFree("hello\nworld\n")
	})
	// Must begin with the cursor-home escape, not a clear-screen escape.
	if !strings.HasPrefix(out, "\033[H") {
		t.Errorf("expected output to start with cursor-home \\033[H, got: %q", out)
	}
	// Must NOT contain a full clear-screen sequence.
	if strings.Contains(out, "\033[2J") {
		t.Errorf("expected no full clear-screen \\033[2J in flicker-free output: %q", out)
	}
}

func TestWriteFrameFlickerFree_ErasesToEndOfLine(t *testing.T) {
	out := captureStdout(t, func() {
		writeFrameFlickerFree("line one\nline two\n")
	})
	// Each newline should be preceded by an erase-to-EOL escape.
	if !strings.Contains(out, "\033[K\n") {
		t.Errorf("expected erase-to-EOL \\033[K before each newline, got: %q", out)
	}
}

func TestWriteFrameFlickerFree_ErasesTrailingScreen(t *testing.T) {
	out := captureStdout(t, func() {
		writeFrameFlickerFree("content\n")
	})
	// Must end with erase-to-end-of-screen so leftover lines from taller
	// previous frames are cleared.
	if !strings.HasSuffix(out, "\033[J") {
		t.Errorf("expected output to end with erase-to-end-of-screen \\033[J, got: %q", out)
	}
}

func TestWriteFrameFlickerFree_ContentPreserved(t *testing.T) {
	out := captureStdout(t, func() {
		writeFrameFlickerFree("alpha\nbeta\n")
	})
	if !strings.Contains(out, "alpha") {
		t.Errorf("expected 'alpha' in output, got: %q", out)
	}
	if !strings.Contains(out, "beta") {
		t.Errorf("expected 'beta' in output, got: %q", out)
	}
}

// ---- drawTailFrame ----

func TestDrawTailFrame_NoStateFile(t *testing.T) {
	setupAgentCleanTest(t)

	out := captureStdout(t, func() {
		_ = drawTailFrame("/nonexistent/test/repo")
	})

	// When state file doesn't exist, LoadDaemonState returns a new empty state —
	// drawTailFrame should show "No active work items."
	if !strings.Contains(out, "No active work items") {
		t.Errorf("expected 'No active work items' for missing state file, got: %q", out)
	}
}

func TestDrawTailFrame_NoActiveItems(t *testing.T) {
	setupAgentCleanTest(t)

	repo := "test/draw-tail-repo"
	stateFilePath := daemonstate.StateFilePath(repo)
	if err := os.MkdirAll(filepath.Dir(stateFilePath), 0o755); err != nil {
		t.Fatalf("failed to create state dir: %v", err)
	}
	stateJSON := `{"version":2,"repo_path":"test/draw-tail-repo","work_items":{}}`
	if err := os.WriteFile(stateFilePath, []byte(stateJSON), 0o644); err != nil {
		t.Fatalf("failed to write state file: %v", err)
	}

	out := captureStdout(t, func() {
		_ = drawTailFrame(repo)
	})

	if !strings.Contains(out, "No active work items") {
		t.Errorf("expected 'No active work items' for empty state, got: %q", out)
	}
	if !strings.Contains(out, "Updated") {
		t.Errorf("expected 'Updated' timestamp in output, got: %q", out)
	}
}

func TestDrawTailFrame_WithActiveItem(t *testing.T) {
	setupAgentCleanTest(t)

	repo := "test/draw-tail-active"
	stateJSON := `{
		"version": 2,
		"repo_path": "test/draw-tail-active",
		"work_items": {
			"item-001": {
				"id": "item-001",
				"issue_ref": {"source": "github", "id": "55", "title": "Active issue"},
				"state": "active",
				"current_step": "coding",
				"phase": "async_pending",
				"created_at": "2024-01-01T12:00:00Z",
				"step_entered_at": "2024-01-01T12:00:00Z"
			}
		}
	}`

	stateFilePath := daemonstate.StateFilePath(repo)
	if err := os.MkdirAll(filepath.Dir(stateFilePath), 0o755); err != nil {
		t.Fatalf("failed to create state dir: %v", err)
	}
	if err := os.WriteFile(stateFilePath, []byte(stateJSON), 0o644); err != nil {
		t.Fatalf("failed to write state file: %v", err)
	}

	out := captureStdout(t, func() {
		_ = drawTailFrame(repo)
	})

	if !strings.Contains(out, "#55") {
		t.Errorf("expected issue '#55' in output, got: %q", out)
	}
	if !strings.Contains(out, "coding") {
		t.Errorf("expected step 'coding' in output, got: %q", out)
	}
}
