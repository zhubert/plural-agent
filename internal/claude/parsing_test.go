package claude

import (
	"log/slog"
	"os"
	"testing"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestParseStreamMessage_AssistantText(t *testing.T) {
	msg := `{"type":"assistant","message":{"content":[{"type":"text","text":"Hello, world!"}]}}`
	chunks := parseStreamMessage(msg, testLogger())

	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].Type != ChunkTypeText {
		t.Errorf("expected ChunkTypeText, got %v", chunks[0].Type)
	}
	if chunks[0].Content != "Hello, world!" {
		t.Errorf("expected 'Hello, world!', got %q", chunks[0].Content)
	}
}

func TestParseStreamMessage_ToolUse(t *testing.T) {
	msg := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_123","name":"Read","input":{"file_path":"/path/to/file.go"}}]}}`
	chunks := parseStreamMessage(msg, testLogger())

	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].Type != ChunkTypeToolUse {
		t.Errorf("expected ChunkTypeToolUse, got %v", chunks[0].Type)
	}
	if chunks[0].ToolName != "Read" {
		t.Errorf("expected tool name 'Read', got %q", chunks[0].ToolName)
	}
}

func TestParseStreamMessage_TextAndToolUse(t *testing.T) {
	msg := `{"type":"assistant","message":{"content":[{"type":"text","text":"Let me read that."},{"type":"tool_use","id":"toolu_123","name":"Read","input":{"file_path":"/file.go"}}]}}`
	chunks := parseStreamMessage(msg, testLogger())

	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	if chunks[0].Type != ChunkTypeText {
		t.Errorf("expected ChunkTypeText, got %v", chunks[0].Type)
	}
	if chunks[1].Type != ChunkTypeToolUse {
		t.Errorf("expected ChunkTypeToolUse, got %v", chunks[1].Type)
	}
}

func TestParseStreamMessage_UserToolResult(t *testing.T) {
	msg := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_123"}]}}`
	chunks := parseStreamMessage(msg, testLogger())

	// User messages (tool results) are logged but not emitted as chunks
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for user tool result, got %d", len(chunks))
	}
}

func TestParseStreamMessage_SystemInit(t *testing.T) {
	msg := `{"type":"system","subtype":"init","session_id":"test-123"}`
	chunks := parseStreamMessage(msg, testLogger())

	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for system init, got %d", len(chunks))
	}
}

func TestParseStreamMessage_Result(t *testing.T) {
	msg := `{"type":"result","subtype":"success","result":"Task completed"}`
	chunks := parseStreamMessage(msg, testLogger())

	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for result, got %d", len(chunks))
	}
}

func TestParseStreamMessage_StreamEventIgnored(t *testing.T) {
	// stream_event type is no longer handled — should produce 0 chunks
	line := `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}}`
	chunks := parseStreamMessage(line, testLogger())
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for stream_event, got %d", len(chunks))
	}
}

func TestParseStreamMessage_NonJSONLineSkipped(t *testing.T) {
	lines := []string{
		"Loading configuration...",
		"Warning: some deprecation notice",
		"  indented non-JSON line",
	}
	for _, line := range lines {
		chunks := parseStreamMessage(line, testLogger())
		if len(chunks) != 0 {
			t.Errorf("expected 0 chunks for non-JSON line %q, got %d", line, len(chunks))
		}
	}
}

func TestParseStreamMessage_InvalidJSONSkipped(t *testing.T) {
	chunks := parseStreamMessage("{malformed json}", testLogger())
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for malformed JSON, got %d", len(chunks))
	}
}

func TestParseStreamMessage_EmptyTypeSkipped(t *testing.T) {
	chunks := parseStreamMessage(`{"data":"something"}`, testLogger())
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for empty type JSON, got %d", len(chunks))
	}
}

func TestTruncateString_IncludesEllipsis(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{"short string unchanged", "hi", 10, "hi"},
		{"exact length unchanged", "hello", 5, "hello"},
		{"truncated with ellipsis", "hello world", 8, "hello..."},
		{"very short maxLen", "hello", 2, "he"},
		{"maxLen 3", "hello", 3, "hel"},
		{"maxLen 4", "hello", 4, "h..."},
		{"empty string", "", 5, ""},
		{"zero maxLen means no limit", "hello", 0, "hello"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateString(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateString(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
			if tt.maxLen > 0 && len(got) > tt.maxLen {
				t.Errorf("output length %d exceeds maxLen %d", len(got), tt.maxLen)
			}
		})
	}
}

func TestFormatToolIcon(t *testing.T) {
	tests := []struct {
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
	}
	for _, tt := range tests {
		got := FormatToolIcon(tt.tool)
		if got != tt.want {
			t.Errorf("FormatToolIcon(%q) = %q, want %q", tt.tool, got, tt.want)
		}
	}
}

func TestFormatToolIcon_Unknown(t *testing.T) {
	got := FormatToolIcon("MyCustomTool")
	if got != "Using MyCustomTool" {
		t.Errorf("FormatToolIcon(MyCustomTool) = %q, want 'Using MyCustomTool'", got)
	}
}
