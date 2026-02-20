package worker

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/zhubert/plural-core/claude"
	"github.com/zhubert/plural-core/config"
	"github.com/zhubert/plural-core/exec"
	"github.com/zhubert/plural-core/mcp"
)

func TestNewSessionWorker(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)
	h := newMockHost(mockExec)

	sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
	h.cfg.AddSession(*sess)

	runner := claude.NewMockRunner("s1", false, nil)
	w := NewSessionWorker(h, sess, runner, "Hello, world!")

	if w.SessionID() != "s1" {
		t.Errorf("expected sessionID s1, got %s", w.SessionID())
	}
	if w.InitialMsg() != "Hello, world!" {
		t.Errorf("expected initialMsg, got %s", w.InitialMsg())
	}
	if w.Turns() != 0 {
		t.Errorf("expected 0 turns, got %d", w.Turns())
	}
	if w.Done() {
		t.Error("expected Done() == false before start")
	}
}

func TestNewDoneWorker(t *testing.T) {
	w := NewDoneWorker()
	if !w.Done() {
		t.Error("expected NewDoneWorker to be immediately Done()")
	}
}

func TestSessionWorker_Lifecycle(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)
	h := newMockHost(mockExec)
	h.daemonManaged = true // Prevent auto-merge from starting

	sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1", PRCreated: true}
	h.cfg.AddSession(*sess)

	runner := claude.NewMockRunner("s1", false, nil)
	// Queue a complete response
	runner.QueueResponse(
		claude.ResponseChunk{Type: claude.ChunkTypeText, Content: "Hello"},
		claude.ResponseChunk{Done: true},
	)

	w := NewSessionWorker(h, sess, runner, "Do something")

	// Before start, cancel is nil
	w.Cancel() // Should not panic

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w.Start(ctx)

	// Worker should complete after the response
	done := make(chan struct{})
	go func() {
		w.Wait()
		close(done)
	}()
	select {
	case <-done:
		// Good
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not complete in time")
	}

	if !w.Done() {
		t.Error("expected Done() == true after completion")
	}
	if w.Turns() != 1 {
		t.Errorf("expected 1 turn, got %d", w.Turns())
	}
}

func TestSessionWorker_Cancel(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)
	h := newMockHost(mockExec)

	sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
	h.cfg.AddSession(*sess)

	runner := claude.NewMockRunner("s1", false, nil)
	// Don't queue a Done response — the worker will wait forever unless cancelled

	w := NewSessionWorker(h, sess, runner, "Do something")

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)

	// Cancel the context
	cancel()

	// Worker should exit promptly
	done := make(chan struct{})
	go func() {
		w.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Good
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not exit after cancel")
	}
}

func TestSessionWorker_CheckLimits(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)
	h := newMockHost(mockExec)
	h.maxTurns = 5
	h.maxDuration = 1 // 1 minute

	sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
	h.cfg.AddSession(*sess)

	runner := claude.NewMockRunner("s1", false, nil)
	w := NewSessionWorker(h, sess, runner, "test")

	t.Run("under limits", func(t *testing.T) {
		w.SetTurns(0)
		w.SetStartTime(time.Now())
		if w.CheckLimits() {
			t.Error("expected false when under limits")
		}
	})

	t.Run("turn limit hit", func(t *testing.T) {
		w.SetTurns(5)
		w.SetStartTime(time.Now())
		if !w.CheckLimits() {
			t.Error("expected true when turn limit hit")
		}
	})

	t.Run("duration limit hit", func(t *testing.T) {
		w.SetTurns(0)
		w.SetStartTime(time.Now().Add(-2 * time.Minute))
		if !w.CheckLimits() {
			t.Error("expected true when duration limit hit")
		}
	})
}

func TestSessionWorker_SetLimits(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)
	h := newMockHost(mockExec)
	h.maxTurns = 50
	h.maxDuration = 30 // 30 minutes

	sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
	h.cfg.AddSession(*sess)

	runner := claude.NewMockRunner("s1", false, nil)
	w := NewSessionWorker(h, sess, runner, "test")

	t.Run("zero values fall back to host defaults", func(t *testing.T) {
		w.SetLimits(0, 0)
		w.SetTurns(2)
		w.SetStartTime(time.Now())
		if w.CheckLimits() {
			t.Error("expected false - under host limits (50 turns)")
		}
	})

	t.Run("override max turns", func(t *testing.T) {
		w.SetLimits(2, 0) // 2 turn override
		w.SetTurns(2)     // at the limit
		w.SetStartTime(time.Now())
		if !w.CheckLimits() {
			t.Error("expected true when override turn limit hit")
		}
	})

	t.Run("override max turns under limit", func(t *testing.T) {
		w.SetLimits(5, 0)
		w.SetTurns(2)
		w.SetStartTime(time.Now())
		if w.CheckLimits() {
			t.Error("expected false - under override turn limit")
		}
	})

	t.Run("override max duration", func(t *testing.T) {
		w.SetLimits(0, 5*time.Minute)
		w.SetTurns(0)
		w.SetStartTime(time.Now().Add(-10 * time.Minute)) // elapsed past limit
		if !w.CheckLimits() {
			t.Error("expected true when override duration limit exceeded")
		}
	})

	t.Run("override max duration under limit", func(t *testing.T) {
		w.SetLimits(0, 60*time.Minute)
		w.SetTurns(0)
		w.SetStartTime(time.Now())
		if w.CheckLimits() {
			t.Error("expected false - under override duration limit")
		}
	})

	t.Run("both overrides set", func(t *testing.T) {
		w.SetLimits(3, 10*time.Minute)
		w.SetTurns(3) // at turn limit
		w.SetStartTime(time.Now())
		if !w.CheckLimits() {
			t.Error("expected true when override turn limit hit with both set")
		}
	})
}

func TestSessionWorker_HandleStreaming(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)
	h := newMockHost(mockExec)

	sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
	h.cfg.AddSession(*sess)

	runner := claude.NewMockRunner("s1", false, nil)
	w := NewSessionWorker(h, sess, runner, "test")

	// Should not panic for text chunk
	w.handleStreaming(claude.ResponseChunk{
		Type:    claude.ChunkTypeText,
		Content: "some text content",
	})

	// Should not panic for non-text chunk
	w.handleStreaming(claude.ResponseChunk{
		Type:    claude.ChunkTypeToolUse,
		Content: "",
	})

	// Should not panic for long content (triggers truncation)
	longContent := make([]byte, 200)
	for i := range longContent {
		longContent[i] = 'a'
	}
	w.handleStreaming(claude.ResponseChunk{
		Type:    claude.ChunkTypeText,
		Content: string(longContent),
	})
}

func TestSessionWorker_SetTurns(t *testing.T) {
	w := &SessionWorker{}
	w.SetTurns(42)
	if w.Turns() != 42 {
		t.Errorf("expected 42, got %d", w.Turns())
	}
}

func TestSessionWorker_SetStartTime(t *testing.T) {
	w := &SessionWorker{}
	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	w.SetStartTime(past)
	if w.startTime != past {
		t.Error("expected start time to be set")
	}
}

func TestSessionWorker_ExitError_NilOnSuccess(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)
	h := newMockHost(mockExec)
	h.daemonManaged = true

	sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1", PRCreated: true}
	h.cfg.AddSession(*sess)

	runner := claude.NewMockRunner("s1", false, nil)
	runner.QueueResponse(
		claude.ResponseChunk{Type: claude.ChunkTypeText, Content: "All done"},
		claude.ResponseChunk{Done: true},
	)

	w := NewSessionWorker(h, sess, runner, "Do something")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w.Start(ctx)
	w.Wait()

	if w.ExitError() != nil {
		t.Errorf("expected nil ExitError, got %v", w.ExitError())
	}
}

func TestSessionWorker_ExitError_SetOnChunkError(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)
	h := newMockHost(mockExec)

	sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
	h.cfg.AddSession(*sess)

	runner := claude.NewMockRunner("s1", false, nil)
	runner.QueueResponse(
		claude.ResponseChunk{Error: fmt.Errorf("connection failed")},
	)

	w := NewSessionWorker(h, sess, runner, "Do something")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w.Start(ctx)
	w.Wait()

	if w.ExitError() == nil {
		t.Fatal("expected ExitError to be set")
	}
	if w.ExitError().Error() != "claude error: connection failed" {
		t.Errorf("unexpected error: %v", w.ExitError())
	}
}

func TestSessionWorker_ExitError_APIErrorInStream(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)
	h := newMockHost(mockExec)
	h.daemonManaged = true

	sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1", PRCreated: true}
	h.cfg.AddSession(*sess)

	runner := claude.NewMockRunner("s1", false, nil)
	// Simulate an API 500 error emitted as text content
	runner.QueueResponse(
		claude.ResponseChunk{Type: claude.ChunkTypeText, Content: "Working on the task..."},
		claude.ResponseChunk{Type: claude.ChunkTypeText, Content: `API Error: 500 {"type":"error","error":{"type":"api_error","message":"Internal server error"}}`},
		claude.ResponseChunk{Done: true},
	)

	w := NewSessionWorker(h, sess, runner, "Do something")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w.Start(ctx)
	w.Wait()

	if w.ExitError() == nil {
		t.Fatal("expected ExitError to be set for API error in stream")
	}
	if w.ExitError().Error() != "API error detected in response stream" {
		t.Errorf("unexpected error: %v", w.ExitError())
	}
}

func TestIsAPIErrorContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"API Error 500 with JSON", `API Error: 500 {"type":"error","error":{"type":"api_error","message":"Internal server error"}}`, true},
		{"API error lowercase with JSON", `API error: 429 {"type":"error","error":{"type":"rate_limit_error"}}`, true},
		{"API Error with spaced JSON", `API Error: 500 {"type": "error", "message": "fail"}`, true},
		{"API Error without JSON structure", `API Error: The user encountered a 500`, false},
		{"normal content", "Here is the code I wrote", false},
		{"error in middle", `The API Error: 500 {"type":"error"} happened`, false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAPIErrorContent(tt.content); got != tt.want {
				t.Errorf("isAPIErrorContent(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}

func TestNewDoneWorkerWithError(t *testing.T) {
	err := fmt.Errorf("something broke")
	w := NewDoneWorkerWithError(err)

	if !w.Done() {
		t.Error("expected Done() to be true")
	}
	if w.ExitError() != err {
		t.Errorf("expected error %v, got %v", err, w.ExitError())
	}
}

func TestSessionWorker_HandleCreatePR_DaemonManaged(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)
	h := newMockHost(mockExec)
	h.daemonManaged = true

	sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
	h.cfg.AddSession(*sess)

	runner := claude.NewMockRunner("s1", false, nil)
	runner.SetHostTools(true)
	w := NewSessionWorker(h, sess, runner, "test")

	// Call handleCreatePR — should be rejected in daemon-managed mode.
	// SendCreatePRResponse is non-blocking so this won't hang.
	w.handleCreatePR(mcp.CreatePRRequest{ID: 1, Title: "My PR"})

	// Verify the session was NOT marked as PR created
	updated := h.cfg.GetSession("s1")
	if updated != nil && updated.PRCreated {
		t.Error("expected session NOT to be marked as PR created in daemon-managed mode")
	}
}

func TestSessionWorker_HandlePushBranch_DaemonManaged(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)
	h := newMockHost(mockExec)
	h.daemonManaged = true

	sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
	h.cfg.AddSession(*sess)

	runner := claude.NewMockRunner("s1", false, nil)
	runner.SetHostTools(true)
	w := NewSessionWorker(h, sess, runner, "test")

	// Call handlePushBranch — should be rejected in daemon-managed mode.
	// SendPushBranchResponse is non-blocking so this won't hang.
	w.handlePushBranch(mcp.PushBranchRequest{ID: 1, CommitMessage: "push changes"})

	// If we got here without hanging, the rejection worked.
	// No further state to verify since push doesn't mark any session fields.
}
