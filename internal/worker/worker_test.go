package worker

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/zhubert/erg/internal/agentconfig"
	"github.com/zhubert/erg/internal/claude"
	"github.com/zhubert/erg/internal/config"
	"github.com/zhubert/erg/internal/exec"
	"github.com/zhubert/erg/internal/git"
	"github.com/zhubert/erg/internal/mcp"
	"github.com/zhubert/erg/internal/testutil"
)

// mockHost implements worker.Host for unit testing.
type mockHost struct {
	cfg        *config.Config
	gitService *git.GitService
	logger     *slog.Logger

	maxTurns              int
	maxDuration           int
	autoMerge             bool
	mergeMethod           string
	autoAddressPRComments bool

	cleanupCalled        map[string]bool
	recordedCostUSD      float64
	recordedOutputTokens int
	recordedInputTokens  int

	pendingMu       sync.Mutex
	pendingMessages map[string]string

	workItemData        map[string]map[string]any // sessionID -> key -> value
	commentOnIssueErr   error                     // error to return from CommentOnIssue
	commentOnIssueCalls []commentOnIssueCall       // recorded calls
}

type commentOnIssueCall struct {
	SessionID string
	Body      string
}

func newMockHost(mockExec *exec.MockExecutor) *mockHost {
	cfg := &config.Config{
		AutoMaxTurns:       50,
		AutoMaxDurationMin: 30,
	}
	gitSvc := git.NewGitServiceWithExecutor(mockExec)

	return &mockHost{
		cfg:             cfg,
		gitService:      gitSvc,
		logger:          testutil.DiscardLogger(),
		maxTurns:        50,
		maxDuration:     30,
		autoMerge:       true,
		mergeMethod:     "rebase",
		cleanupCalled:   make(map[string]bool),
		pendingMessages: make(map[string]string),
	}
}

// Compile-time check that mockHost implements Host.
var _ Host = (*mockHost)(nil)

func (h *mockHost) Config() agentconfig.Config  { return h.cfg }
func (h *mockHost) GitService() *git.GitService { return h.gitService }
func (h *mockHost) Logger() *slog.Logger        { return h.logger }

func (h *mockHost) GetPendingMessage(sessionID string) string {
	h.pendingMu.Lock()
	defer h.pendingMu.Unlock()
	msg := h.pendingMessages[sessionID]
	delete(h.pendingMessages, sessionID)
	return msg
}

func (h *mockHost) SetPendingMessage(sessionID, msg string) {
	h.pendingMu.Lock()
	defer h.pendingMu.Unlock()
	h.pendingMessages[sessionID] = msg
}
func (h *mockHost) MaxTurns() int               { return h.maxTurns }
func (h *mockHost) MaxDuration() int            { return h.maxDuration }
func (h *mockHost) AutoMerge() bool             { return h.autoMerge }
func (h *mockHost) MergeMethod() string         { return h.mergeMethod }
func (h *mockHost) AutoAddressPRComments() bool { return h.autoAddressPRComments }

func (h *mockHost) CleanupSession(ctx context.Context, sessionID string) error {
	h.cleanupCalled[sessionID] = true
	return nil
}

func (h *mockHost) SaveRunnerMessages(sessionID string, runner claude.RunnerSession) {}

func (h *mockHost) IsWorkerRunning(sessionID string) bool { return false }

func (h *mockHost) RecordSpend(costUSD float64, outputTokens, inputTokens int) {
	h.recordedCostUSD += costUSD
	h.recordedOutputTokens += outputTokens
	h.recordedInputTokens += inputTokens
}

func (h *mockHost) CommentOnIssue(ctx context.Context, sessionID, body string) error {
	h.commentOnIssueCalls = append(h.commentOnIssueCalls, commentOnIssueCall{SessionID: sessionID, Body: body})
	return h.commentOnIssueErr
}

func (h *mockHost) SetWorkItemData(sessionID, key string, value any) error {
	if h.workItemData == nil {
		h.workItemData = make(map[string]map[string]any)
	}
	if h.workItemData[sessionID] == nil {
		h.workItemData[sessionID] = make(map[string]any)
	}
	h.workItemData[sessionID][key] = value
	return nil
}

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

	sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
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

	ctx := t.Context()

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

func TestSessionWorker_HandleStreaming_RecordsSpend(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)
	h := newMockHost(mockExec)

	sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
	h.cfg.AddSession(*sess)

	runner := claude.NewMockRunner("s1", false, nil)
	w := NewSessionWorker(h, sess, runner, "test")

	// Intermediate stats chunk (DurationMs == 0) should NOT record spend.
	w.handleStreaming(claude.ResponseChunk{
		Type: claude.ChunkTypeStreamStats,
		Stats: &claude.StreamStats{
			OutputTokens: 100,
			InputTokens:  50,
			TotalCostUSD: 0.01,
			DurationMs:   0, // intermediate chunk
		},
	})
	if h.recordedCostUSD != 0 {
		t.Errorf("expected no spend recorded for intermediate stats chunk, got %v", h.recordedCostUSD)
	}

	// Final stats chunk (DurationMs > 0) should record spend.
	w.handleStreaming(claude.ResponseChunk{
		Type: claude.ChunkTypeStreamStats,
		Stats: &claude.StreamStats{
			OutputTokens: 200,
			InputTokens:  150,
			TotalCostUSD: 0.05,
			DurationMs:   3000, // final result chunk
		},
	})
	if h.recordedCostUSD != 0.05 {
		t.Errorf("expected recorded cost 0.05, got %v", h.recordedCostUSD)
	}
	if h.recordedOutputTokens != 200 {
		t.Errorf("expected output tokens 200, got %d", h.recordedOutputTokens)
	}
	if h.recordedInputTokens != 150 {
		t.Errorf("expected input tokens 150, got %d", h.recordedInputTokens)
	}

	// Second final stats chunk accumulates.
	w.handleStreaming(claude.ResponseChunk{
		Type: claude.ChunkTypeStreamStats,
		Stats: &claude.StreamStats{
			OutputTokens: 50,
			InputTokens:  30,
			TotalCostUSD: 0.01,
			DurationMs:   1000,
		},
	})
	// Use approximate equality for floating-point accumulation.
	if h.recordedCostUSD < 0.0599 || h.recordedCostUSD > 0.0601 {
		t.Errorf("expected accumulated cost ~0.06, got %v", h.recordedCostUSD)
	}
}

func TestSessionWorker_HandleStreaming_RecordsSpend_IncludesCacheTokens(t *testing.T) {
	// Verify that cache creation and cache read tokens are included in the recorded
	// input token total. Previously only InputTokens (direct, non-cached) was counted,
	// leaving the bulk of token usage untracked.
	mockExec := exec.NewMockExecutor(nil)
	h := newMockHost(mockExec)

	sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
	h.cfg.AddSession(*sess)

	runner := claude.NewMockRunner("s1", false, nil)
	w := NewSessionWorker(h, sess, runner, "test")

	w.handleStreaming(claude.ResponseChunk{
		Type: claude.ChunkTypeStreamStats,
		Stats: &claude.StreamStats{
			OutputTokens:        5000,
			InputTokens:         27,    // tiny direct input
			CacheCreationTokens: 41000, // large cache write
			CacheReadTokens:     90000, // large cache read
			TotalCostUSD:        0.617,
			DurationMs:          60000, // final result chunk
		},
	})

	expectedInput := 27 + 41000 + 90000 // 131027
	if h.recordedInputTokens != expectedInput {
		t.Errorf("expected input tokens %d (direct+cacheCreation+cacheRead), got %d",
			expectedInput, h.recordedInputTokens)
	}
	if h.recordedOutputTokens != 5000 {
		t.Errorf("expected output tokens 5000, got %d", h.recordedOutputTokens)
	}
	if h.recordedCostUSD < 0.6169 || h.recordedCostUSD > 0.6171 {
		t.Errorf("expected cost ~0.617, got %v", h.recordedCostUSD)
	}
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

	sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
	h.cfg.AddSession(*sess)

	runner := claude.NewMockRunner("s1", false, nil)
	runner.QueueResponse(
		claude.ResponseChunk{Type: claude.ChunkTypeText, Content: "All done"},
		claude.ResponseChunk{Done: true},
	)

	w := NewSessionWorker(h, sess, runner, "Do something")
	ctx := t.Context()

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
	ctx := t.Context()

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

	sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
	h.cfg.AddSession(*sess)

	runner := claude.NewMockRunner("s1", false, nil)
	// Simulate an API 500 error emitted as text content
	runner.QueueResponse(
		claude.ResponseChunk{Type: claude.ChunkTypeText, Content: "Working on the task..."},
		claude.ResponseChunk{Type: claude.ChunkTypeText, Content: `API Error: 500 {"type":"error","error":{"type":"api_error","message":"Internal server error"}}`},
		claude.ResponseChunk{Done: true},
	)

	w := NewSessionWorker(h, sess, runner, "Do something")
	ctx := t.Context()

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

func TestIsSessionNotFoundContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"exact match", "\n[Error: No conversation found with session ID: 65832c71-192c-43fa-b2c1-c849eb38b4b6]\n", true},
		{"without newlines", "[Error: No conversation found with session ID: abc123]", true},
		{"normal content", "Here is the code I wrote", false},
		{"similar but different", "No conversation found", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSessionNotFoundContent(tt.content); got != tt.want {
				t.Errorf("isSessionNotFoundContent(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}

func TestSessionWorker_ExitError_SessionNotFound(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)
	h := newMockHost(mockExec)

	sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
	h.cfg.AddSession(*sess)

	runner := claude.NewMockRunner("s1", false, nil)
	// Simulate the "No conversation found" error emitted as text content
	runner.QueueResponse(
		claude.ResponseChunk{Type: claude.ChunkTypeText, Content: "\n[Error: No conversation found with session ID: s1]\n"},
		claude.ResponseChunk{Done: true},
	)

	w := NewSessionWorker(h, sess, runner, "Address review feedback")
	ctx := t.Context()

	w.Start(ctx)
	w.Wait()

	if w.ExitError() == nil {
		t.Fatal("expected ExitError to be set for session-not-found error")
	}
	if w.ExitError().Error() != "API error detected in response stream" {
		t.Errorf("unexpected error: %v", w.ExitError())
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

func TestSessionWorker_HandleCreatePR_Rejected(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)
	h := newMockHost(mockExec)

	sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
	h.cfg.AddSession(*sess)

	runner := claude.NewMockRunner("s1", false, nil)
	runner.SetHostTools(true)
	w := NewSessionWorker(h, sess, runner, "test")

	// Call handleCreatePR — should be rejected since the workflow manages PRs.
	// SendCreatePRResponse is non-blocking so this won't hang.
	w.handleCreatePR(mcp.CreatePRRequest{ID: 1, Title: "My PR"})

	// Verify the session was NOT marked as PR created
	updated := h.cfg.GetSession("s1")
	if updated != nil && updated.PRCreated {
		t.Error("expected session NOT to be marked as PR created")
	}
}

func TestSessionWorker_HandlePushBranch_Rejected(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)
	h := newMockHost(mockExec)

	sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
	h.cfg.AddSession(*sess)

	runner := claude.NewMockRunner("s1", false, nil)
	runner.SetHostTools(true)
	w := NewSessionWorker(h, sess, runner, "test")

	// Call handlePushBranch — should be rejected since the workflow manages pushing.
	// SendPushBranchResponse is non-blocking so this won't hang.
	w.handlePushBranch(mcp.PushBranchRequest{ID: 1, CommitMessage: "push changes"})

	// If we got here without hanging, the rejection worked.
	// No further state to verify since push doesn't mark any session fields.
}

func TestSessionWorker_DoneChan_ClosedWhenDone(t *testing.T) {
	w := NewDoneWorker()

	select {
	case <-w.DoneChan():
		// expected: channel is already closed for a done worker
	default:
		t.Fatal("DoneChan() should be closed for a done worker")
	}
}

func TestSessionWorker_DoneChan_BlocksUntilDone(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)
	h := newMockHost(mockExec)
	sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
	h.cfg.AddSession(*sess)
	runner := claude.NewMockRunner("s1", false, nil)
	w := NewSessionWorker(h, sess, runner, "test")

	select {
	case <-w.DoneChan():
		t.Fatal("DoneChan() should not be closed for a running worker")
	default:
		// expected: channel is open while worker has not finished
	}
}

// TestSessionWorker_Turns_ConcurrentAccess verifies that Turns() can be read
// concurrently while the worker goroutine is incrementing it without a data
// race.  Run with -race to exercise the race detector.
func TestSessionWorker_Turns_ConcurrentAccess(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)
	h := newMockHost(mockExec)
	h.maxTurns = 1000

	sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
	h.cfg.AddSession(*sess)

	runner := claude.NewMockRunner("s1", false, nil)
	// Queue enough responses for several turns so the worker stays alive long
	// enough for the concurrent reader goroutine to race.
	for range 10 {
		runner.QueueResponse(
			claude.ResponseChunk{Type: claude.ChunkTypeText, Content: "thinking"},
			claude.ResponseChunk{Done: true},
		)
	}

	w := NewSessionWorker(h, sess, runner, "Do something")
	ctx := t.Context()

	w.Start(ctx)

	// Read Turns() concurrently while the worker goroutine may be writing it.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_ = w.Turns()
			select {
			case <-w.DoneChan():
				return
			default:
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not complete in time")
	}
}

// TestSessionWorker_ExitError_ConcurrentAccess verifies that ExitError() can
// be read concurrently with the worker goroutine writing it without a data
// race.  Run with -race to exercise the race detector.
func TestSessionWorker_ExitError_ConcurrentAccess(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)
	h := newMockHost(mockExec)

	sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
	h.cfg.AddSession(*sess)

	runner := claude.NewMockRunner("s1", false, nil)
	runner.QueueResponse(
		claude.ResponseChunk{Error: fmt.Errorf("injected error")},
	)

	w := NewSessionWorker(h, sess, runner, "Do something")
	ctx := t.Context()

	w.Start(ctx)

	// Poll ExitError() from a separate goroutine while the worker goroutine
	// may simultaneously be writing exitErr.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_ = w.ExitError()
			select {
			case <-w.DoneChan():
				return
			default:
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not complete in time")
	}

	if w.ExitError() == nil {
		t.Fatal("expected ExitError to be set")
	}
}

func TestSessionWorker_HandleCommentIssue_Success(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)
	h := newMockHost(mockExec)

	sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
	h.cfg.AddSession(*sess)

	runner := claude.NewMockRunner("s1", false, nil)
	runner.SetHostTools(true)
	w := NewSessionWorker(h, sess, runner, "test")
	w.ctx = context.Background()

	w.handleCommentIssue(mcp.CommentIssueRequest{ID: 1, Body: "Here is the plan"})

	// Verify the call was delegated to the host.
	if len(h.commentOnIssueCalls) != 1 {
		t.Fatalf("expected 1 CommentOnIssue call, got %d", len(h.commentOnIssueCalls))
	}
	call := h.commentOnIssueCalls[0]
	if call.SessionID != "s1" {
		t.Errorf("expected sessionID 's1', got %q", call.SessionID)
	}
	if call.Body != "Here is the plan" {
		t.Errorf("expected body 'Here is the plan', got %q", call.Body)
	}
}

func TestSessionWorker_HandleCommentIssue_HostError(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)
	h := newMockHost(mockExec)
	h.commentOnIssueErr = fmt.Errorf("provider not registered")

	sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
	h.cfg.AddSession(*sess)

	runner := claude.NewMockRunner("s1", false, nil)
	runner.SetHostTools(true)
	w := NewSessionWorker(h, sess, runner, "test")
	w.ctx = context.Background()

	w.handleCommentIssue(mcp.CommentIssueRequest{ID: 1, Body: "Hello"})
	// Should not panic; error response sent to runner.
}

func TestSessionWorker_HandleSubmitReview_StoresInStepData(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)
	h := newMockHost(mockExec)

	sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
	h.cfg.AddSession(*sess)

	runner := claude.NewMockRunner("s1", false, nil)
	runner.SetHostTools(true)
	w := NewSessionWorker(h, sess, runner, "test")

	w.handleSubmitReview(mcp.SubmitReviewRequest{ID: 1, Passed: false, Summary: "missing tests"})

	// Verify the data was stored via SetWorkItemData
	data := h.workItemData["s1"]
	if data == nil {
		t.Fatal("expected work item data to be set for session s1")
	}
	if passed, ok := data["review_passed"].(bool); !ok || passed {
		t.Errorf("expected review_passed=false, got %v", data["review_passed"])
	}
	if summary, ok := data["ai_review_summary"].(string); !ok || summary != "missing tests" {
		t.Errorf("expected ai_review_summary='missing tests', got %v", data["ai_review_summary"])
	}
}

func TestSessionWorker_HandleSubmitReview_Passed(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)
	h := newMockHost(mockExec)

	sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
	h.cfg.AddSession(*sess)

	runner := claude.NewMockRunner("s1", false, nil)
	runner.SetHostTools(true)
	w := NewSessionWorker(h, sess, runner, "test")

	w.handleSubmitReview(mcp.SubmitReviewRequest{ID: 1, Passed: true, Summary: "looks good"})

	data := h.workItemData["s1"]
	if data == nil {
		t.Fatal("expected work item data to be set for session s1")
	}
	if passed, ok := data["review_passed"].(bool); !ok || !passed {
		t.Errorf("expected review_passed=true, got %v", data["review_passed"])
	}
	if summary, ok := data["ai_review_summary"].(string); !ok || summary != "looks good" {
		t.Errorf("expected ai_review_summary='looks good', got %v", data["ai_review_summary"])
	}
}
