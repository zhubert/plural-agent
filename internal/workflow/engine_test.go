package workflow

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"
)

// mockAction is a test action that returns a preset result.
type mockAction struct {
	result ActionResult
}

func (a *mockAction) Execute(ctx context.Context, ac *ActionContext) ActionResult {
	return a.result
}

// mockEventChecker is a test event checker.
type mockEventChecker struct {
	fired bool
	data  map[string]any
	err   error
}

func (c *mockEventChecker) CheckEvent(ctx context.Context, event string, params *ParamHelper, item *WorkItemView) (bool, map[string]any, error) {
	return c.fired, c.data, c.err
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestEngine_ProcessStep_TerminalSucceed(t *testing.T) {
	cfg := &Config{
		Start: "done",
		States: map[string]*State{
			"done": {Type: StateTypeSucceed},
		},
	}
	engine := NewEngine(cfg, NewActionRegistry(), nil, testLogger())

	view := &WorkItemView{CurrentStep: "done"}
	result, err := engine.ProcessStep(context.Background(), view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Terminal {
		t.Error("expected terminal")
	}
	if !result.TerminalOK {
		t.Error("expected terminal OK (succeed)")
	}
}

func TestEngine_ProcessStep_TerminalFail(t *testing.T) {
	cfg := &Config{
		Start: "failed",
		States: map[string]*State{
			"failed": {Type: StateTypeFail},
		},
	}
	engine := NewEngine(cfg, NewActionRegistry(), nil, testLogger())

	view := &WorkItemView{CurrentStep: "failed"}
	result, err := engine.ProcessStep(context.Background(), view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Terminal {
		t.Error("expected terminal")
	}
	if result.TerminalOK {
		t.Error("expected terminal NOT OK (fail)")
	}
}

func TestEngine_ProcessStep_TaskSync(t *testing.T) {
	registry := NewActionRegistry()
	registry.Register("test.action", &mockAction{
		result: ActionResult{Success: true, Data: map[string]any{"key": "val"}},
	})

	cfg := &Config{
		Start: "step1",
		States: map[string]*State{
			"step1":  {Type: StateTypeTask, Action: "test.action", Next: "done", Error: "failed"},
			"done":   {Type: StateTypeSucceed},
			"failed": {Type: StateTypeFail},
		},
	}
	engine := NewEngine(cfg, registry, nil, testLogger())

	view := &WorkItemView{CurrentStep: "step1"}
	result, err := engine.ProcessStep(context.Background(), view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NewStep != "done" {
		t.Errorf("expected next step 'done', got %q", result.NewStep)
	}
	if result.NewPhase != "idle" {
		t.Errorf("expected phase 'idle', got %q", result.NewPhase)
	}
	if result.Data["key"] != "val" {
		t.Error("expected data to be passed through")
	}
}

func TestEngine_ProcessStep_TaskAsync(t *testing.T) {
	registry := NewActionRegistry()
	registry.Register("async.action", &mockAction{
		result: ActionResult{Success: true, Async: true},
	})

	cfg := &Config{
		Start: "step1",
		States: map[string]*State{
			"step1": {Type: StateTypeTask, Action: "async.action", Next: "done"},
			"done":  {Type: StateTypeSucceed},
		},
	}
	engine := NewEngine(cfg, registry, nil, testLogger())

	view := &WorkItemView{CurrentStep: "step1"}
	result, err := engine.ProcessStep(context.Background(), view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NewStep != "step1" {
		t.Errorf("expected to stay on step1 (async), got %q", result.NewStep)
	}
	if result.NewPhase != "async_pending" {
		t.Errorf("expected phase async_pending, got %q", result.NewPhase)
	}
}

func TestEngine_ProcessStep_TaskFailure(t *testing.T) {
	registry := NewActionRegistry()
	registry.Register("fail.action", &mockAction{
		result: ActionResult{Success: false, Error: nil},
	})

	cfg := &Config{
		Start: "step1",
		States: map[string]*State{
			"step1":  {Type: StateTypeTask, Action: "fail.action", Next: "done", Error: "failed"},
			"done":   {Type: StateTypeSucceed},
			"failed": {Type: StateTypeFail},
		},
	}
	engine := NewEngine(cfg, registry, nil, testLogger())

	view := &WorkItemView{CurrentStep: "step1"}
	result, err := engine.ProcessStep(context.Background(), view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NewStep != "failed" {
		t.Errorf("expected error step 'failed', got %q", result.NewStep)
	}
}

func TestEngine_ProcessStep_WaitFired(t *testing.T) {
	checker := &mockEventChecker{fired: true, data: map[string]any{"approved": true}}

	cfg := &Config{
		Start: "wait",
		States: map[string]*State{
			"wait": {Type: StateTypeWait, Event: "pr.reviewed", Next: "done"},
			"done": {Type: StateTypeSucceed},
		},
	}
	engine := NewEngine(cfg, NewActionRegistry(), checker, testLogger())

	view := &WorkItemView{CurrentStep: "wait", Phase: "idle"}
	result, err := engine.ProcessStep(context.Background(), view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NewStep != "done" {
		t.Errorf("expected next step 'done', got %q", result.NewStep)
	}
}

func TestEngine_ProcessStep_WaitNotFired(t *testing.T) {
	checker := &mockEventChecker{fired: false}

	cfg := &Config{
		Start: "wait",
		States: map[string]*State{
			"wait": {Type: StateTypeWait, Event: "ci.complete", Next: "done"},
			"done": {Type: StateTypeSucceed},
		},
	}
	engine := NewEngine(cfg, NewActionRegistry(), checker, testLogger())

	view := &WorkItemView{CurrentStep: "wait", Phase: "idle"}
	result, err := engine.ProcessStep(context.Background(), view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should stay in current step
	if result.NewStep != "wait" {
		t.Errorf("expected to stay on 'wait', got %q", result.NewStep)
	}
}

func TestEngine_AdvanceAfterAsync_Success(t *testing.T) {
	cfg := &Config{
		Start: "coding",
		States: map[string]*State{
			"coding": {Type: StateTypeTask, Action: "ai.code", Next: "open_pr", Error: "failed"},
			"open_pr": {Type: StateTypeTask, Action: "github.create_pr", Next: "done"},
			"done":   {Type: StateTypeSucceed},
			"failed": {Type: StateTypeFail},
		},
	}
	engine := NewEngine(cfg, NewActionRegistry(), nil, testLogger())

	view := &WorkItemView{CurrentStep: "coding", Phase: "async_pending"}
	result, err := engine.AdvanceAfterAsync(view, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NewStep != "open_pr" {
		t.Errorf("expected open_pr, got %q", result.NewStep)
	}
}

func TestEngine_AdvanceAfterAsync_Failure(t *testing.T) {
	cfg := &Config{
		Start: "coding",
		States: map[string]*State{
			"coding": {Type: StateTypeTask, Action: "ai.code", Next: "done", Error: "failed"},
			"done":   {Type: StateTypeSucceed},
			"failed": {Type: StateTypeFail},
		},
	}
	engine := NewEngine(cfg, NewActionRegistry(), nil, testLogger())

	view := &WorkItemView{CurrentStep: "coding", Phase: "async_pending"}
	result, err := engine.AdvanceAfterAsync(view, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NewStep != "failed" {
		t.Errorf("expected failed, got %q", result.NewStep)
	}
}

func TestEngine_GetStartState(t *testing.T) {
	cfg := &Config{Start: "my_start"}
	engine := NewEngine(cfg, NewActionRegistry(), nil, testLogger())
	if engine.GetStartState() != "my_start" {
		t.Errorf("expected my_start, got %s", engine.GetStartState())
	}
}

func TestEngine_IsTerminalState(t *testing.T) {
	cfg := &Config{
		States: map[string]*State{
			"coding": {Type: StateTypeTask},
			"done":   {Type: StateTypeSucceed},
			"failed": {Type: StateTypeFail},
		},
	}
	engine := NewEngine(cfg, NewActionRegistry(), nil, testLogger())

	if engine.IsTerminalState("coding") {
		t.Error("coding should not be terminal")
	}
	if !engine.IsTerminalState("done") {
		t.Error("done should be terminal")
	}
	if !engine.IsTerminalState("failed") {
		t.Error("failed should be terminal")
	}
	if engine.IsTerminalState("nonexistent") {
		t.Error("nonexistent should not be terminal")
	}
}

func TestEngine_ProcessStep_UnknownState(t *testing.T) {
	cfg := &Config{
		Start: "coding",
		States: map[string]*State{
			"coding": {Type: StateTypeTask, Action: "ai.code", Next: "done"},
			"done":   {Type: StateTypeSucceed},
		},
	}
	engine := NewEngine(cfg, NewActionRegistry(), nil, testLogger())

	// Referencing a state that doesn't exist in the config should error
	view := &WorkItemView{CurrentStep: "nonexistent_step", Phase: "idle"}
	_, err := engine.ProcessStep(context.Background(), view)
	if err == nil {
		t.Fatal("expected error for unknown state")
	}
}

func TestEngine_ProcessStep_UnknownAction(t *testing.T) {
	cfg := &Config{
		Start: "step1",
		States: map[string]*State{
			"step1": {Type: StateTypeTask, Action: "unregistered.action", Next: "done"},
			"done":  {Type: StateTypeSucceed},
		},
	}
	engine := NewEngine(cfg, NewActionRegistry(), nil, testLogger())

	view := &WorkItemView{CurrentStep: "step1", Phase: "idle"}
	_, err := engine.ProcessStep(context.Background(), view)
	if err == nil {
		t.Fatal("expected error for unregistered action")
	}
}

func TestEngine_ProcessStep_UnsupportedStateType(t *testing.T) {
	cfg := &Config{
		Start: "bad",
		States: map[string]*State{
			"bad": {Type: "bogus_type"},
		},
	}
	engine := NewEngine(cfg, NewActionRegistry(), nil, testLogger())

	view := &WorkItemView{CurrentStep: "bad", Phase: "idle"}
	_, err := engine.ProcessStep(context.Background(), view)
	if err == nil {
		t.Fatal("expected error for unsupported state type")
	}
}

func TestEngine_AdvanceAfterAsync_UnknownState(t *testing.T) {
	cfg := &Config{
		Start:  "coding",
		States: map[string]*State{},
	}
	engine := NewEngine(cfg, NewActionRegistry(), nil, testLogger())

	view := &WorkItemView{CurrentStep: "nonexistent", Phase: "async_pending"}
	_, err := engine.AdvanceAfterAsync(view, true)
	if err == nil {
		t.Fatal("expected error for unknown state")
	}
}

func TestEngine_AdvanceAfterAsync_FailureNoErrorEdge(t *testing.T) {
	cfg := &Config{
		Start: "coding",
		States: map[string]*State{
			"coding": {Type: StateTypeTask, Action: "ai.code", Next: "done"},
			"done":   {Type: StateTypeSucceed},
		},
	}
	engine := NewEngine(cfg, NewActionRegistry(), nil, testLogger())

	// Fail with no Error edge configured — should return an error
	view := &WorkItemView{CurrentStep: "coding", Phase: "async_pending"}
	_, err := engine.AdvanceAfterAsync(view, false)
	if err == nil {
		t.Fatal("expected error when async fails with no error edge")
	}
}

func TestEngine_ProcessStep_TaskFailureNoErrorEdge(t *testing.T) {
	registry := NewActionRegistry()
	registry.Register("fail.action", &mockAction{
		result: ActionResult{Success: false, Error: nil},
	})

	cfg := &Config{
		Start: "step1",
		States: map[string]*State{
			// No Error edge defined
			"step1": {Type: StateTypeTask, Action: "fail.action", Next: "done"},
			"done":  {Type: StateTypeSucceed},
		},
	}
	engine := NewEngine(cfg, registry, nil, testLogger())

	view := &WorkItemView{CurrentStep: "step1", Phase: "idle"}
	_, err := engine.ProcessStep(context.Background(), view)
	if err == nil {
		t.Fatal("expected error when task fails with no error edge")
	}
}

func TestEngine_ProcessStep_WaitNoEventChecker(t *testing.T) {
	cfg := &Config{
		Start: "wait",
		States: map[string]*State{
			"wait": {Type: StateTypeWait, Event: "ci.complete", Next: "done"},
			"done": {Type: StateTypeSucceed},
		},
	}
	// nil event checker
	engine := NewEngine(cfg, NewActionRegistry(), nil, testLogger())

	view := &WorkItemView{CurrentStep: "wait", Phase: "idle"}
	_, err := engine.ProcessStep(context.Background(), view)
	if err == nil {
		t.Fatal("expected error when no event checker configured")
	}
}

func TestEngine_ProcessStep_WaitEventCheckError(t *testing.T) {
	checker := &mockEventChecker{err: fmt.Errorf("network error")}

	cfg := &Config{
		Start: "wait",
		States: map[string]*State{
			"wait": {Type: StateTypeWait, Event: "ci.complete", Next: "done"},
			"done": {Type: StateTypeSucceed},
		},
	}
	engine := NewEngine(cfg, NewActionRegistry(), checker, testLogger())

	view := &WorkItemView{CurrentStep: "wait", Phase: "idle"}
	result, err := engine.ProcessStep(context.Background(), view)
	if err != nil {
		t.Fatalf("unexpected error (should be swallowed): %v", err)
	}
	// Should stay in current state on event check error
	if result.NewStep != "wait" {
		t.Errorf("expected to stay on 'wait', got %q", result.NewStep)
	}
	if result.NewPhase != "idle" {
		t.Errorf("expected phase 'idle', got %q", result.NewPhase)
	}
}

func TestEngine_ProcessStep_WaitTimeout_ErrorEdge(t *testing.T) {
	checker := &mockEventChecker{fired: false}

	cfg := &Config{
		Start: "wait",
		States: map[string]*State{
			"wait":   {Type: StateTypeWait, Event: "ci.complete", Timeout: &Duration{1 * time.Hour}, Next: "done", Error: "failed"},
			"done":   {Type: StateTypeSucceed},
			"failed": {Type: StateTypeFail},
		},
	}
	engine := NewEngine(cfg, NewActionRegistry(), checker, testLogger())

	// StepEnteredAt is 2 hours ago — should timeout
	view := &WorkItemView{
		CurrentStep:   "wait",
		Phase:         "idle",
		StepEnteredAt: time.Now().Add(-2 * time.Hour),
	}
	result, err := engine.ProcessStep(context.Background(), view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NewStep != "failed" {
		t.Errorf("expected failed on timeout, got %q", result.NewStep)
	}
	if result.Data["timeout"] != true {
		t.Error("expected timeout=true in data")
	}
}

func TestEngine_ProcessStep_WaitTimeout_TimeoutNextEdge(t *testing.T) {
	checker := &mockEventChecker{fired: false}

	cfg := &Config{
		Start: "wait",
		States: map[string]*State{
			"wait":  {Type: StateTypeWait, Event: "ci.complete", Timeout: &Duration{1 * time.Hour}, TimeoutNext: "nudge", Next: "done", Error: "failed"},
			"nudge": {Type: StateTypeSucceed},
			"done":  {Type: StateTypeSucceed},
			"failed": {Type: StateTypeFail},
		},
	}
	engine := NewEngine(cfg, NewActionRegistry(), checker, testLogger())

	view := &WorkItemView{
		CurrentStep:   "wait",
		Phase:         "idle",
		StepEnteredAt: time.Now().Add(-2 * time.Hour),
	}
	result, err := engine.ProcessStep(context.Background(), view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should use timeout_next over error edge
	if result.NewStep != "nudge" {
		t.Errorf("expected nudge on timeout, got %q", result.NewStep)
	}
}

func TestEngine_ProcessStep_WaitTimeout_NoEdge(t *testing.T) {
	checker := &mockEventChecker{fired: false}

	cfg := &Config{
		Start: "wait",
		States: map[string]*State{
			"wait": {Type: StateTypeWait, Event: "ci.complete", Timeout: &Duration{1 * time.Hour}, Next: "done"},
			"done": {Type: StateTypeSucceed},
		},
	}
	engine := NewEngine(cfg, NewActionRegistry(), checker, testLogger())

	view := &WorkItemView{
		CurrentStep:   "wait",
		Phase:         "idle",
		StepEnteredAt: time.Now().Add(-2 * time.Hour),
	}
	_, err := engine.ProcessStep(context.Background(), view)
	if err == nil {
		t.Fatal("expected error when timeout with no timeout_next or error edge")
	}
}

func TestEngine_ProcessStep_WaitTimeout_NotExpired(t *testing.T) {
	checker := &mockEventChecker{fired: false}

	cfg := &Config{
		Start: "wait",
		States: map[string]*State{
			"wait":   {Type: StateTypeWait, Event: "ci.complete", Timeout: &Duration{2 * time.Hour}, Next: "done", Error: "failed"},
			"done":   {Type: StateTypeSucceed},
			"failed": {Type: StateTypeFail},
		},
	}
	engine := NewEngine(cfg, NewActionRegistry(), checker, testLogger())

	// StepEnteredAt is only 30 minutes ago — should NOT timeout
	view := &WorkItemView{
		CurrentStep:   "wait",
		Phase:         "idle",
		StepEnteredAt: time.Now().Add(-30 * time.Minute),
	}
	result, err := engine.ProcessStep(context.Background(), view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should stay in current state (event not fired, not timed out)
	if result.NewStep != "wait" {
		t.Errorf("expected to stay on wait, got %q", result.NewStep)
	}
}

func TestEngine_ProcessStep_WaitTimeout_ZeroEnteredAt(t *testing.T) {
	checker := &mockEventChecker{fired: false}

	cfg := &Config{
		Start: "wait",
		States: map[string]*State{
			"wait":   {Type: StateTypeWait, Event: "ci.complete", Timeout: &Duration{1 * time.Hour}, Next: "done", Error: "failed"},
			"done":   {Type: StateTypeSucceed},
			"failed": {Type: StateTypeFail},
		},
	}
	engine := NewEngine(cfg, NewActionRegistry(), checker, testLogger())

	// Zero StepEnteredAt should skip timeout check
	view := &WorkItemView{
		CurrentStep: "wait",
		Phase:       "idle",
	}
	result, err := engine.ProcessStep(context.Background(), view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NewStep != "wait" {
		t.Errorf("expected to stay on wait (no entered_at), got %q", result.NewStep)
	}
}

func TestEngine_FullTraversal(t *testing.T) {
	// Test a full workflow traversal with sync actions and event checks.
	// New flow: coding → open_pr → await_ci → check_ci_result → await_review → merge → done
	registry := NewActionRegistry()
	registry.Register("ai.code", &mockAction{result: ActionResult{Success: true, Async: true}})
	registry.Register("github.create_pr", &mockAction{result: ActionResult{Success: true, Data: map[string]any{"pr_url": "https://github.com/test/pr/1"}}})
	registry.Register("github.merge", &mockAction{result: ActionResult{Success: true}})

	cfg := DefaultWorkflowConfig()

	// Phase 1: coding (async)
	engine := NewEngine(cfg, registry, nil, testLogger())
	view := &WorkItemView{CurrentStep: "coding", Phase: "idle"}
	result, err := engine.ProcessStep(context.Background(), view)
	if err != nil {
		t.Fatalf("coding step error: %v", err)
	}
	if result.NewPhase != "async_pending" {
		t.Errorf("expected async_pending, got %q", result.NewPhase)
	}

	// Phase 2: after async, advance to open_pr
	view.Phase = "async_pending"
	result, err = engine.AdvanceAfterAsync(view, true)
	if err != nil {
		t.Fatalf("advance error: %v", err)
	}
	if result.NewStep != "open_pr" {
		t.Errorf("expected open_pr, got %q", result.NewStep)
	}

	// Phase 3: open_pr (sync) → should advance to await_ci
	view.CurrentStep = "open_pr"
	view.Phase = "idle"
	result, err = engine.ProcessStep(context.Background(), view)
	if err != nil {
		t.Fatalf("open_pr step error: %v", err)
	}
	if result.NewStep != "await_ci" {
		t.Errorf("expected await_ci, got %q", result.NewStep)
	}

	// Phase 4: await_ci (event fired with ci_passed=true) → check_ci_result → await_review
	checker := &mockEventChecker{fired: true, data: map[string]any{"ci_passed": true}}
	engine2 := NewEngine(cfg, registry, checker, testLogger())
	view.CurrentStep = "await_ci"
	view.Phase = "idle"
	view.StepData = map[string]any{}
	result, err = engine2.ProcessStep(context.Background(), view)
	if err != nil {
		t.Fatalf("await_ci step error: %v", err)
	}
	// await_ci fires → next is check_ci_result (choice) which evaluates ci_passed=true → await_review
	if result.NewStep != "check_ci_result" {
		t.Errorf("expected check_ci_result, got %q", result.NewStep)
	}
}

func TestEngine_ProcessStep_RetryOnFailure(t *testing.T) {
	registry := NewActionRegistry()
	registry.Register("fail.action", &mockAction{
		result: ActionResult{Success: false, Error: fmt.Errorf("transient error")},
	})

	cfg := &Config{
		Start: "step1",
		States: map[string]*State{
			"step1": {
				Type:   StateTypeTask,
				Action: "fail.action",
				Next:   "done",
				Error:  "failed",
				Retry: []RetryConfig{
					{MaxAttempts: 3, Interval: &Duration{30 * time.Second}, BackoffRate: 2.0},
				},
			},
			"done":   {Type: StateTypeSucceed},
			"failed": {Type: StateTypeFail},
		},
	}
	engine := NewEngine(cfg, registry, nil, testLogger())

	// First attempt — should retry (count goes from 0 to 1)
	view := &WorkItemView{CurrentStep: "step1", Phase: "idle", StepData: map[string]any{}}
	result, err := engine.ProcessStep(context.Background(), view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NewStep != "step1" {
		t.Errorf("expected to stay on step1 for retry, got %q", result.NewStep)
	}
	if result.NewPhase != "retry_pending" {
		t.Errorf("expected retry_pending phase, got %q", result.NewPhase)
	}
	if result.Data["_retry_count"] != 1 {
		t.Errorf("expected retry count 1, got %v", result.Data["_retry_count"])
	}
}

func TestEngine_ProcessStep_RetryExhausted(t *testing.T) {
	registry := NewActionRegistry()
	registry.Register("fail.action", &mockAction{
		result: ActionResult{Success: false, Error: fmt.Errorf("persistent error")},
	})

	cfg := &Config{
		Start: "step1",
		States: map[string]*State{
			"step1": {
				Type:   StateTypeTask,
				Action: "fail.action",
				Next:   "done",
				Error:  "failed",
				Retry: []RetryConfig{
					{MaxAttempts: 2},
				},
			},
			"done":   {Type: StateTypeSucceed},
			"failed": {Type: StateTypeFail},
		},
	}
	engine := NewEngine(cfg, registry, nil, testLogger())

	// Already retried 2 times — should fall through to error edge
	view := &WorkItemView{
		CurrentStep: "step1",
		Phase:       "idle",
		StepData:    map[string]any{"_retry_count": 2},
	}
	result, err := engine.ProcessStep(context.Background(), view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NewStep != "failed" {
		t.Errorf("expected failed after retries exhausted, got %q", result.NewStep)
	}
}

func TestEngine_ProcessStep_CatchError(t *testing.T) {
	registry := NewActionRegistry()
	registry.Register("fail.action", &mockAction{
		result: ActionResult{Success: false, Error: fmt.Errorf("some error")},
	})

	cfg := &Config{
		Start: "step1",
		States: map[string]*State{
			"step1": {
				Type:   StateTypeTask,
				Action: "fail.action",
				Next:   "done",
				Catch: []CatchConfig{
					{Errors: []string{"*"}, Next: "recovery"},
				},
			},
			"done":     {Type: StateTypeSucceed},
			"recovery": {Type: StateTypeSucceed},
		},
	}
	engine := NewEngine(cfg, registry, nil, testLogger())

	view := &WorkItemView{CurrentStep: "step1", Phase: "idle"}
	result, err := engine.ProcessStep(context.Background(), view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NewStep != "recovery" {
		t.Errorf("expected recovery via catch, got %q", result.NewStep)
	}
	if result.Data["_caught_error"] != "some error" {
		t.Errorf("expected caught error in data, got %v", result.Data["_caught_error"])
	}
}

func TestEngine_ProcessStep_CatchSpecificError(t *testing.T) {
	registry := NewActionRegistry()
	registry.Register("fail.action", &mockAction{
		result: ActionResult{Success: false, Error: fmt.Errorf("timeout")},
	})

	cfg := &Config{
		Start: "step1",
		States: map[string]*State{
			"step1": {
				Type:   StateTypeTask,
				Action: "fail.action",
				Next:   "done",
				Error:  "failed",
				Catch: []CatchConfig{
					{Errors: []string{"permission_denied"}, Next: "auth_fix"},
					{Errors: []string{"timeout"}, Next: "retry_later"},
				},
			},
			"done":        {Type: StateTypeSucceed},
			"failed":      {Type: StateTypeFail},
			"auth_fix":    {Type: StateTypeSucceed},
			"retry_later": {Type: StateTypeSucceed},
		},
	}
	engine := NewEngine(cfg, registry, nil, testLogger())

	view := &WorkItemView{CurrentStep: "step1", Phase: "idle"}
	result, err := engine.ProcessStep(context.Background(), view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// "timeout" should match second catch rule
	if result.NewStep != "retry_later" {
		t.Errorf("expected retry_later via specific catch, got %q", result.NewStep)
	}
}

func TestEngine_ProcessStep_RetryThenCatch(t *testing.T) {
	registry := NewActionRegistry()
	registry.Register("fail.action", &mockAction{
		result: ActionResult{Success: false, Error: fmt.Errorf("transient")},
	})

	cfg := &Config{
		Start: "step1",
		States: map[string]*State{
			"step1": {
				Type:   StateTypeTask,
				Action: "fail.action",
				Next:   "done",
				Retry: []RetryConfig{
					{MaxAttempts: 1},
				},
				Catch: []CatchConfig{
					{Errors: []string{"*"}, Next: "recovery"},
				},
			},
			"done":     {Type: StateTypeSucceed},
			"recovery": {Type: StateTypeSucceed},
		},
	}
	engine := NewEngine(cfg, registry, nil, testLogger())

	// Retry exhausted (1 attempt used) — should fall through to catch
	view := &WorkItemView{
		CurrentStep: "step1",
		Phase:       "idle",
		StepData:    map[string]any{"_retry_count": 1},
	}
	result, err := engine.ProcessStep(context.Background(), view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NewStep != "recovery" {
		t.Errorf("expected recovery after retry exhausted, got %q", result.NewStep)
	}
}

func TestEngine_AdvanceAfterAsync_RetryOnFailure(t *testing.T) {
	cfg := &Config{
		Start: "coding",
		States: map[string]*State{
			"coding": {
				Type:   StateTypeTask,
				Action: "ai.code",
				Next:   "done",
				Error:  "failed",
				Retry: []RetryConfig{
					{MaxAttempts: 2},
				},
			},
			"done":   {Type: StateTypeSucceed},
			"failed": {Type: StateTypeFail},
		},
	}
	engine := NewEngine(cfg, NewActionRegistry(), nil, testLogger())

	view := &WorkItemView{
		CurrentStep: "coding",
		Phase:       "async_pending",
		StepData:    map[string]any{},
	}
	result, err := engine.AdvanceAfterAsync(view, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NewStep != "coding" {
		t.Errorf("expected to stay on coding for retry, got %q", result.NewStep)
	}
	if result.NewPhase != "retry_pending" {
		t.Errorf("expected retry_pending phase, got %q", result.NewPhase)
	}
}

func TestEngine_RetryDelay(t *testing.T) {
	tests := []struct {
		name     string
		retry    RetryConfig
		attempt  int
		expected time.Duration
	}{
		{
			name:     "no interval",
			retry:    RetryConfig{MaxAttempts: 3},
			attempt:  1,
			expected: 0,
		},
		{
			name:     "first attempt with interval",
			retry:    RetryConfig{MaxAttempts: 3, Interval: &Duration{10 * time.Second}, BackoffRate: 2.0},
			attempt:  1,
			expected: 10 * time.Second,
		},
		{
			name:     "second attempt with backoff",
			retry:    RetryConfig{MaxAttempts: 3, Interval: &Duration{10 * time.Second}, BackoffRate: 2.0},
			attempt:  2,
			expected: 20 * time.Second,
		},
		{
			name:     "third attempt with backoff",
			retry:    RetryConfig{MaxAttempts: 3, Interval: &Duration{10 * time.Second}, BackoffRate: 2.0},
			attempt:  3,
			expected: 40 * time.Second,
		},
		{
			name:     "no backoff rate defaults to 1.0",
			retry:    RetryConfig{MaxAttempts: 3, Interval: &Duration{10 * time.Second}},
			attempt:  3,
			expected: 10 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := retryDelay(tt.retry, tt.attempt)
			if got != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, got)
			}
		})
	}
}

func TestMatchesErrors(t *testing.T) {
	tests := []struct {
		name     string
		errStr   string
		patterns []string
		want     bool
	}{
		{"empty patterns matches all", "any error", nil, true},
		{"wildcard matches all", "any error", []string{"*"}, true},
		{"exact match", "timeout", []string{"timeout"}, true},
		{"no match", "timeout", []string{"permission_denied"}, false},
		{"multiple patterns with match", "timeout", []string{"permission_denied", "timeout"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesErrors(tt.errStr, tt.patterns)
			if got != tt.want {
				t.Errorf("expected %v, got %v", tt.want, got)
			}
		})
	}
}

func TestGetRetryCount(t *testing.T) {
	tests := []struct {
		name string
		data map[string]any
		want int
	}{
		{"nil data", nil, 0},
		{"no key", map[string]any{}, 0},
		{"int value", map[string]any{"_retry_count": 3}, 3},
		{"float64 value (JSON unmarshal)", map[string]any{"_retry_count": float64(2)}, 2},
		{"string value (invalid)", map[string]any{"_retry_count": "x"}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getRetryCount(tt.data)
			if got != tt.want {
				t.Errorf("expected %d, got %d", tt.want, got)
			}
		})
	}
}

func TestEngine_ProcessStep_ChoiceEquals(t *testing.T) {
	cfg := &Config{
		Start: "check",
		States: map[string]*State{
			"check": {
				Type: StateTypeChoice,
				Choices: []ChoiceRule{
					{Variable: "ci_status", Equals: "passing", Next: "merge"},
					{Variable: "ci_status", Equals: "failing", Next: "fix_ci"},
				},
				Default: "wait_more",
			},
			"merge":     {Type: StateTypeSucceed},
			"fix_ci":    {Type: StateTypeSucceed},
			"wait_more": {Type: StateTypeSucceed},
		},
	}
	engine := NewEngine(cfg, NewActionRegistry(), nil, testLogger())

	// Test matching first rule
	view := &WorkItemView{
		CurrentStep: "check",
		Phase:       "idle",
		StepData:    map[string]any{"ci_status": "passing"},
	}
	result, err := engine.ProcessStep(context.Background(), view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NewStep != "merge" {
		t.Errorf("expected merge, got %q", result.NewStep)
	}

	// Test matching second rule
	view.StepData = map[string]any{"ci_status": "failing"}
	result, err = engine.ProcessStep(context.Background(), view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NewStep != "fix_ci" {
		t.Errorf("expected fix_ci, got %q", result.NewStep)
	}
}

func TestEngine_ProcessStep_ChoiceDefault(t *testing.T) {
	cfg := &Config{
		Start: "check",
		States: map[string]*State{
			"check": {
				Type: StateTypeChoice,
				Choices: []ChoiceRule{
					{Variable: "status", Equals: "done", Next: "finish"},
				},
				Default: "continue",
			},
			"finish":   {Type: StateTypeSucceed},
			"continue": {Type: StateTypeSucceed},
		},
	}
	engine := NewEngine(cfg, NewActionRegistry(), nil, testLogger())

	// No matching rule — should use default
	view := &WorkItemView{
		CurrentStep: "check",
		Phase:       "idle",
		StepData:    map[string]any{"status": "pending"},
	}
	result, err := engine.ProcessStep(context.Background(), view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NewStep != "continue" {
		t.Errorf("expected continue (default), got %q", result.NewStep)
	}
}

func TestEngine_ProcessStep_ChoiceNoMatchNoDefault(t *testing.T) {
	cfg := &Config{
		Start: "check",
		States: map[string]*State{
			"check": {
				Type: StateTypeChoice,
				Choices: []ChoiceRule{
					{Variable: "status", Equals: "done", Next: "finish"},
				},
			},
			"finish": {Type: StateTypeSucceed},
		},
	}
	engine := NewEngine(cfg, NewActionRegistry(), nil, testLogger())

	view := &WorkItemView{
		CurrentStep: "check",
		Phase:       "idle",
		StepData:    map[string]any{"status": "pending"},
	}
	_, err := engine.ProcessStep(context.Background(), view)
	if err == nil {
		t.Fatal("expected error when no rule matches and no default")
	}
}

func TestEngine_ProcessStep_ChoiceNotEquals(t *testing.T) {
	cfg := &Config{
		Start: "check",
		States: map[string]*State{
			"check": {
				Type: StateTypeChoice,
				Choices: []ChoiceRule{
					{Variable: "pr_closed", NotEquals: true, Next: "continue"},
				},
				Default: "abort",
			},
			"continue": {Type: StateTypeSucceed},
			"abort":    {Type: StateTypeFail},
		},
	}
	engine := NewEngine(cfg, NewActionRegistry(), nil, testLogger())

	// pr_closed is false, so not_equals true → matches
	view := &WorkItemView{
		CurrentStep: "check",
		Phase:       "idle",
		StepData:    map[string]any{"pr_closed": false},
	}
	result, err := engine.ProcessStep(context.Background(), view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NewStep != "continue" {
		t.Errorf("expected continue, got %q", result.NewStep)
	}
}

func TestEngine_ProcessStep_ChoiceIsPresent(t *testing.T) {
	boolTrue := true
	boolFalse := false

	cfg := &Config{
		Start: "check",
		States: map[string]*State{
			"check": {
				Type: StateTypeChoice,
				Choices: []ChoiceRule{
					{Variable: "pr_url", IsPresent: &boolTrue, Next: "has_pr"},
					{Variable: "pr_url", IsPresent: &boolFalse, Next: "no_pr"},
				},
			},
			"has_pr": {Type: StateTypeSucceed},
			"no_pr":  {Type: StateTypeSucceed},
		},
	}
	engine := NewEngine(cfg, NewActionRegistry(), nil, testLogger())

	// Variable present
	view := &WorkItemView{
		CurrentStep: "check",
		Phase:       "idle",
		StepData:    map[string]any{"pr_url": "https://github.com/test/pr/1"},
	}
	result, err := engine.ProcessStep(context.Background(), view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NewStep != "has_pr" {
		t.Errorf("expected has_pr, got %q", result.NewStep)
	}

	// Variable not present
	view.StepData = map[string]any{}
	result, err = engine.ProcessStep(context.Background(), view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NewStep != "no_pr" {
		t.Errorf("expected no_pr, got %q", result.NewStep)
	}
}

func TestValuesEqual(t *testing.T) {
	tests := []struct {
		name string
		a, b any
		want bool
	}{
		{"string match", "hello", "hello", true},
		{"string mismatch", "hello", "world", false},
		{"bool match", true, true, true},
		{"bool mismatch", true, false, false},
		{"int match", 42, 42, true},
		{"int vs float64", 42, float64(42), true},
		{"int mismatch", 42, 43, false},
		{"map equal", map[string]any{"a": 1}, map[string]any{"a": 1}, true},
		{"map not equal", map[string]any{"a": 1}, map[string]any{"a": 2}, false},
		{"bool vs string true", true, "true", false},
		{"int vs string", 42, "42", false},
		{"nil vs nil", nil, nil, true},
		{"nil vs string", nil, "nil", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := valuesEqual(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("valuesEqual(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestEngine_ProcessStep_Pass(t *testing.T) {
	cfg := &Config{
		Start: "setup",
		States: map[string]*State{
			"setup": {
				Type: StateTypePass,
				Data: map[string]any{
					"merge_method": "squash",
					"cleanup":     true,
				},
				Next: "coding",
			},
			"coding": {Type: StateTypeSucceed},
		},
	}
	engine := NewEngine(cfg, NewActionRegistry(), nil, testLogger())

	view := &WorkItemView{CurrentStep: "setup", Phase: "idle"}
	result, err := engine.ProcessStep(context.Background(), view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NewStep != "coding" {
		t.Errorf("expected coding, got %q", result.NewStep)
	}
	if result.Data["merge_method"] != "squash" {
		t.Error("expected merge_method=squash in data")
	}
	if result.Data["cleanup"] != true {
		t.Error("expected cleanup=true in data")
	}
}

func TestEngine_ProcessStep_PassNoData(t *testing.T) {
	cfg := &Config{
		Start: "skip",
		States: map[string]*State{
			"skip": {Type: StateTypePass, Next: "done"},
			"done": {Type: StateTypeSucceed},
		},
	}
	engine := NewEngine(cfg, NewActionRegistry(), nil, testLogger())

	view := &WorkItemView{CurrentStep: "skip", Phase: "idle"}
	result, err := engine.ProcessStep(context.Background(), view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NewStep != "done" {
		t.Errorf("expected done, got %q", result.NewStep)
	}
}

func TestEngine_GetBeforeHooks(t *testing.T) {
	cfg := &Config{
		Start: "coding",
		States: map[string]*State{
			"coding": {
				Type:   StateTypeTask,
				Action: "ai.code",
				Next:   "done",
				Before: []HookConfig{{Run: "echo setup"}, {Run: "make lint"}},
			},
			"done":    {Type: StateTypeSucceed},
			"no_hook": {Type: StateTypeSucceed},
		},
	}
	engine := NewEngine(cfg, NewActionRegistry(), nil, testLogger())

	// State with before hooks
	hooks := engine.GetBeforeHooks("coding")
	if len(hooks) != 2 {
		t.Fatalf("expected 2 before hooks, got %d", len(hooks))
	}
	if hooks[0].Run != "echo setup" {
		t.Errorf("expected 'echo setup', got %q", hooks[0].Run)
	}

	// State without before hooks
	hooks = engine.GetBeforeHooks("no_hook")
	if len(hooks) != 0 {
		t.Errorf("expected 0 before hooks, got %d", len(hooks))
	}

	// Non-existent state
	hooks = engine.GetBeforeHooks("nonexistent")
	if hooks != nil {
		t.Errorf("expected nil for non-existent state, got %v", hooks)
	}
}

func TestEngine_ProcessStep_TaskSync_OverrideNext(t *testing.T) {
	// When an action returns Success with OverrideNext set, the engine
	// should use OverrideNext instead of the state's configured Next edge.
	registry := NewActionRegistry()
	registry.Register("skip.action", &mockAction{
		result: ActionResult{Success: true, OverrideNext: "done"},
	})

	cfg := &Config{
		Start: "coding",
		States: map[string]*State{
			"coding":  {Type: StateTypeTask, Action: "skip.action", Next: "open_pr", Error: "failed"},
			"open_pr": {Type: StateTypeSucceed},
			"done":    {Type: StateTypeSucceed},
			"failed":  {Type: StateTypeFail},
		},
	}
	engine := NewEngine(cfg, registry, nil, testLogger())

	view := &WorkItemView{CurrentStep: "coding"}
	result, err := engine.ProcessStep(context.Background(), view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NewStep != "done" {
		t.Errorf("expected OverrideNext 'done', got %q", result.NewStep)
	}
	if result.NewPhase != "idle" {
		t.Errorf("expected phase 'idle', got %q", result.NewPhase)
	}
}

func TestEngine_ProcessStep_TaskSync_NoOverride(t *testing.T) {
	// When OverrideNext is empty, the engine should use the state's Next edge as normal.
	registry := NewActionRegistry()
	registry.Register("normal.action", &mockAction{
		result: ActionResult{Success: true},
	})

	cfg := &Config{
		Start: "coding",
		States: map[string]*State{
			"coding":  {Type: StateTypeTask, Action: "normal.action", Next: "open_pr"},
			"open_pr": {Type: StateTypeSucceed},
		},
	}
	engine := NewEngine(cfg, registry, nil, testLogger())

	view := &WorkItemView{CurrentStep: "coding"}
	result, err := engine.ProcessStep(context.Background(), view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NewStep != "open_pr" {
		t.Errorf("expected normal Next 'open_pr', got %q", result.NewStep)
	}
}
