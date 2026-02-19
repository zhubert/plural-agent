package worker

import (
	"context"
	"testing"
	"time"

	"github.com/zhubert/plural-core/claude"
	"github.com/zhubert/plural-core/config"
	"github.com/zhubert/plural-core/exec"
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
