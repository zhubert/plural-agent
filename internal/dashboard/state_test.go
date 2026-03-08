package dashboard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhubert/erg/internal/paths"
)

func TestCollectAll_NoDaemons(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	paths.Reset()

	// Create .erg dir so glob doesn't fail
	if err := os.MkdirAll(filepath.Join(tmpDir, ".erg"), 0o755); err != nil {
		t.Fatal(err)
	}

	snap, err := CollectAll()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if snap == nil {
		t.Fatal("expected non-nil snapshot")
	}
	if len(snap.Daemons) != 0 {
		t.Errorf("expected 0 daemons, got %d", len(snap.Daemons))
	}
	if snap.Timestamp.IsZero() {
		t.Error("expected non-zero timestamp")
	}
}

func TestToolDesc(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "Read",
			input: `{"file_path":"/Users/test/project/main.go"}`,
			want:  "main.go",
		},
		{
			name:  "Bash",
			input: `{"command":"go test ./..."}`,
			want:  "go test ./...",
		},
		{
			name:  "Grep",
			input: `{"pattern":"func main"}`,
			want:  "func main",
		},
		{
			name:  "Edit",
			input: `{"file_path":"/very/long/path/to/some/deeply/nested/file.go"}`,
			want:  "file.go",
		},
		{
			name:  "Unknown",
			input: `{"something":"else"}`,
			want:  "",
		},
		{
			name:  "Bash",
			input: `{"command":"this is a very long command that should be truncated at fifty characters please"}`,
			want:  "this is a very long command that should be trun...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toolDesc(tt.name, json.RawMessage(tt.input))
			if got != tt.want {
				t.Errorf("toolDesc(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestToolDesc_EmptyInput(t *testing.T) {
	got := toolDesc("Bash", nil)
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestToolDesc_InvalidJSON(t *testing.T) {
	got := toolDesc("Bash", json.RawMessage(`{invalid}`))
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestReadSessionLog_EmptyID(t *testing.T) {
	_, err := ReadSessionLog("", 100)
	if err == nil {
		t.Error("expected error for empty session ID")
	}
}

func TestReadSessionLog_NoFile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	paths.Reset()

	_, err := ReadSessionLog("nonexistent-session", 100)
	if err == nil {
		t.Error("expected error for missing log file")
	}
}

func TestReadSessionLog_WithFile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	paths.Reset()

	logDir := filepath.Join(tmpDir, ".erg", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sessionID := "test-session-123"
	logContent := `{"type":"assistant","message":{"content":[{"type":"text","text":"Hello world\nSecond line"}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/foo/bar.go"}}]}}
{"type":"system","message":{}}
`
	logPath := filepath.Join(logDir, "stream-"+sessionID+".log")
	if err := os.WriteFile(logPath, []byte(logContent), 0o644); err != nil {
		t.Fatal(err)
	}

	lines, err := ReadSessionLog(sessionID, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %+v", len(lines), lines)
	}
	if lines[0].Type != "text" || lines[0].Text != "Hello world" {
		t.Errorf("line 0: %+v", lines[0])
	}
	if lines[1].Type != "text" || lines[1].Text != "Second line" {
		t.Errorf("line 1: %+v", lines[1])
	}
	if lines[2].Type != "tool" || lines[2].Text != "Read: bar.go" {
		t.Errorf("line 2: %+v", lines[2])
	}
}

func TestReadSessionLog_Tail(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	paths.Reset()

	logDir := filepath.Join(tmpDir, ".erg", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sessionID := "tail-test"
	logContent := `{"type":"assistant","message":{"content":[{"type":"text","text":"line1"}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"line2"}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"line3"}]}}
`
	logPath := filepath.Join(logDir, "stream-"+sessionID+".log")
	if err := os.WriteFile(logPath, []byte(logContent), 0o644); err != nil {
		t.Fatal(err)
	}

	lines, err := ReadSessionLog(sessionID, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines (tail=2), got %d", len(lines))
	}
	if lines[0].Text != "line2" {
		t.Errorf("expected 'line2', got %q", lines[0].Text)
	}
	if lines[1].Text != "line3" {
		t.Errorf("expected 'line3', got %q", lines[1].Text)
	}
}
