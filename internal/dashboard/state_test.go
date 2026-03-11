package dashboard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhubert/erg/internal/config"
	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/paths"
)

// writeFakeLockAndState writes a lock file (with the current process PID so it
// appears alive) and a matching state file for the given repoKey using
// daemonstate's path helpers, with the repoKey embedded in the state.
func writeFakeLockAndState(t *testing.T, tmpDir, repoKey string, mutate func(*daemonstate.DaemonState)) {
	t.Helper()

	ergDir := filepath.Join(tmpDir, ".erg")
	if err := os.MkdirAll(ergDir, 0o755); err != nil {
		t.Fatal(err)
	}

	state := daemonstate.NewDaemonState(repoKey)
	state.StartedAt = time.Now()
	if mutate != nil {
		mutate(state)
	}

	statePath := daemonstate.StateFilePath(repoKey)
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	lockPath := daemonstate.LockFilePath(repoKey)
	if err := os.WriteFile(lockPath, fmt.Appendf(nil, "%d", os.Getpid()), 0o644); err != nil {
		t.Fatal(err)
	}
}

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
	if lines[2].Type != "tool" || lines[2].Name != "Read" || lines[2].Text != "bar.go" {
		t.Errorf("line 2: %+v", lines[2])
	}
}

func TestReadSessionLog_UnknownToolEmptyDesc(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	paths.Reset()

	logDir := filepath.Join(tmpDir, ".erg", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sessionID := "unknown-tool-test"
	// UnknownTool has no recognised field, so toolDesc returns ""
	logContent := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"UnknownTool","input":{"something":"value"}}]}}
`
	logPath := filepath.Join(logDir, "stream-"+sessionID+".log")
	if err := os.WriteFile(logPath, []byte(logContent), 0o644); err != nil {
		t.Fatal(err)
	}

	lines, err := ReadSessionLog(sessionID, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d: %+v", len(lines), lines)
	}
	if lines[0].Type != "tool" || lines[0].Name != "UnknownTool" || lines[0].Text != "" {
		t.Errorf("expected tool line with Name='UnknownTool' and empty Text, got %+v", lines[0])
	}
}

func TestReadSessionLog_NonJSONLines(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	paths.Reset()

	logDir := filepath.Join(tmpDir, ".erg", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sessionID := "nonjson-test"
	logContent := `{"type":"assistant","message":{"content":[{"type":"text","text":"before"}]}}
some random non-json output from claude process
ERROR: something went wrong
{"type":"assistant","message":{"content":[{"type":"text","text":"after"}]}}
`
	logPath := filepath.Join(logDir, "stream-"+sessionID+".log")
	if err := os.WriteFile(logPath, []byte(logContent), 0o644); err != nil {
		t.Fatal(err)
	}

	lines, err := ReadSessionLog(sessionID, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines (skipping non-JSON), got %d: %+v", len(lines), lines)
	}
	if lines[0].Text != "before" {
		t.Errorf("expected 'before', got %q", lines[0].Text)
	}
	if lines[1].Text != "after" {
		t.Errorf("expected 'after', got %q", lines[1].Text)
	}
}

func TestReadSessionLog_PrettyPrintedJSON(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	paths.Reset()

	logDir := filepath.Join(tmpDir, ".erg", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sessionID := "pretty-json-test"
	// Simulate the pretty-printed JSON that streaming.go actually writes
	logContent := `{
  "type": "assistant",
  "message": {
    "content": [
      {
        "type": "text",
        "text": "Hello from pretty JSON"
      }
    ]
  }
}
{
  "type": "assistant",
  "message": {
    "content": [
      {
        "type": "tool_use",
        "name": "Bash",
        "input": {
          "command": "go test ./..."
        }
      }
    ]
  }
}
{
  "type": "system",
  "message": {}
}
`
	logPath := filepath.Join(logDir, "stream-"+sessionID+".log")
	if err := os.WriteFile(logPath, []byte(logContent), 0o644); err != nil {
		t.Fatal(err)
	}

	lines, err := ReadSessionLog(sessionID, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %+v", len(lines), lines)
	}
	if lines[0].Type != "text" || lines[0].Text != "Hello from pretty JSON" {
		t.Errorf("line 0: %+v", lines[0])
	}
	if lines[1].Type != "tool" || lines[1].Name != "Bash" || lines[1].Text != "go test ./..." {
		t.Errorf("line 1: %+v", lines[1])
	}
}

func TestReadSessionLog_BracesInStringValues(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	paths.Reset()

	logDir := filepath.Join(tmpDir, ".erg", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sessionID := "braces-in-strings"
	// Bash command contains braces inside the string value
	logContent := `{
  "type": "assistant",
  "message": {
    "content": [
      {
        "type": "tool_use",
        "name": "Bash",
        "input": {
          "command": "for f in $(ls); do echo {}; done"
        }
      }
    ]
  }
}
{
  "type": "assistant",
  "message": {
    "content": [
      {
        "type": "text",
        "text": "Done running the loop"
      }
    ]
  }
}
`
	logPath := filepath.Join(logDir, "stream-"+sessionID+".log")
	if err := os.WriteFile(logPath, []byte(logContent), 0o644); err != nil {
		t.Fatal(err)
	}

	lines, err := ReadSessionLog(sessionID, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %+v", len(lines), lines)
	}
	if lines[0].Type != "tool" || lines[0].Name != "Bash" {
		t.Errorf("line 0: %+v", lines[0])
	}
	if lines[1].Type != "text" || lines[1].Text != "Done running the loop" {
		t.Errorf("line 1: %+v", lines[1])
	}
}

func TestCollectAll_UsesRepoLabels(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	paths.Reset()

	repoKey := "multi-abc123"
	writeFakeLockAndState(t, tmpDir, repoKey, func(s *daemonstate.DaemonState) {
		s.SetRepoLabels(
			[]string{"zhubert/erg", "zhubert/plural"},
			map[string]string{
				"/home/user/code/erg":    "zhubert/erg",
				"/home/user/code/plural": "zhubert/plural",
			},
		)
	})

	snap, err := CollectAll()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(snap.Daemons) != 1 {
		t.Fatalf("expected 1 daemon, got %d", len(snap.Daemons))
	}

	d := snap.Daemons[0]
	want := "zhubert/erg, zhubert/plural"
	if d.Repo != want {
		t.Errorf("DaemonInfo.Repo = %q, want %q", d.Repo, want)
	}
}

func TestCollectAll_FallsBackToRepoPath(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	paths.Reset()

	repoKey := "/home/user/code/erg"
	writeFakeLockAndState(t, tmpDir, repoKey, nil /* no labels set */)

	snap, err := CollectAll()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(snap.Daemons) != 1 {
		t.Fatalf("expected 1 daemon, got %d", len(snap.Daemons))
	}

	d := snap.Daemons[0]
	if d.Repo != repoKey {
		t.Errorf("DaemonInfo.Repo = %q, want %q (fallback to RepoPath)", d.Repo, repoKey)
	}
}

func TestCollectAll_WorkItemRepo(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	paths.Reset()

	repoKey := "multi-abc123"
	writeFakeLockAndState(t, tmpDir, repoKey, func(s *daemonstate.DaemonState) {
		s.SetRepoLabels(
			[]string{"zhubert/erg"},
			map[string]string{"/home/user/code/erg": "zhubert/erg"},
		)
		s.AddWorkItem(&daemonstate.WorkItem{
			ID:       "wi-1",
			IssueRef: config.IssueRef{Source: "github", ID: "42", Title: "Test issue"},
			StepData: map[string]any{"_repo_path": "/home/user/code/erg"},
		})
		// Item without _repo_path should have empty Repo
		s.AddWorkItem(&daemonstate.WorkItem{
			ID:       "wi-2",
			IssueRef: config.IssueRef{Source: "github", ID: "43", Title: "No repo"},
		})
	})

	snap, err := CollectAll()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(snap.Daemons) != 1 {
		t.Fatalf("expected 1 daemon, got %d", len(snap.Daemons))
	}

	items := snap.Daemons[0].WorkItems
	if len(items) != 2 {
		t.Fatalf("expected 2 work items, got %d", len(items))
	}

	// Find items by ID
	itemsByID := make(map[string]WorkItemInfo)
	for _, item := range items {
		itemsByID[item.ID] = item
	}

	wi1 := itemsByID["wi-1"]
	if wi1.Repo != "zhubert/erg" {
		t.Errorf("wi-1 Repo = %q, want %q", wi1.Repo, "zhubert/erg")
	}

	wi2 := itemsByID["wi-2"]
	if wi2.Repo != "" {
		t.Errorf("wi-2 Repo = %q, want empty (no _repo_path)", wi2.Repo)
	}
}

func TestCollectAll_WorkItemRepo_FallbackToRawPath(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	paths.Reset()

	repoKey := "multi-abc123"
	writeFakeLockAndState(t, tmpDir, repoKey, func(s *daemonstate.DaemonState) {
		// No path labels set → fallback to raw path
		s.AddWorkItem(&daemonstate.WorkItem{
			ID:       "wi-1",
			IssueRef: config.IssueRef{Source: "github", ID: "42"},
			StepData: map[string]any{"_repo_path": "/home/user/code/erg"},
		})
	})

	snap, err := CollectAll()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(snap.Daemons) != 1 {
		t.Fatalf("expected 1 daemon, got %d", len(snap.Daemons))
	}

	items := snap.Daemons[0].WorkItems
	if len(items) != 1 {
		t.Fatalf("expected 1 work item, got %d", len(items))
	}
	if items[0].Repo != "/home/user/code/erg" {
		t.Errorf("Repo = %q, want %q (raw path fallback)", items[0].Repo, "/home/user/code/erg")
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
