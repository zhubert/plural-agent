package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zhubert/erg/internal/testutil"
)

// countingAction tracks how many times Execute is called.
type countingAction struct {
	calls   int
	results []ActionResult // results[i] returned on call i; last element repeats
}

func (a *countingAction) Execute(_ context.Context, _ *ActionContext) ActionResult {
	i := a.calls
	if i >= len(a.results) {
		i = len(a.results) - 1
	}
	a.calls++
	return a.results[i]
}

// captureParamsAction records the ActionContext params for inspection.
type captureParamsAction struct {
	result     ActionResult
	lastParams *ParamHelper
}

func (a *captureParamsAction) Execute(_ context.Context, ac *ActionContext) ActionResult {
	a.lastParams = ac.Params
	return a.result
}

// newRetryRegistry builds an ActionRegistry pre-populated with given actions
// plus workflow.retry itself.
func newRetryRegistry(actions map[string]Action) *ActionRegistry {
	r := NewActionRegistry()
	for name, a := range actions {
		r.Register(name, a)
	}
	r.Register("workflow.retry", NewRetryAction(r))
	return r
}

func makeRetryAC(params map[string]any) *ActionContext {
	return &ActionContext{
		Params: NewParamHelper(params),
		Logger: testutil.DiscardLogger(),
	}
}

func TestRetryAction_MissingActionParam(t *testing.T) {
	r := newRetryRegistry(nil)
	ra := NewRetryAction(r)
	res := ra.Execute(context.Background(), makeRetryAC(map[string]any{}))
	if res.Success {
		t.Fatal("expected failure")
	}
	if res.Error == nil {
		t.Fatal("expected non-nil error")
	}
}

func TestRetryAction_UnknownInnerAction(t *testing.T) {
	r := newRetryRegistry(nil)
	ra := NewRetryAction(r)
	res := ra.Execute(context.Background(), makeRetryAC(map[string]any{"action": "does.not.exist"}))
	if res.Success {
		t.Fatal("expected failure for unknown inner action")
	}
}

func TestRetryAction_SuccessOnFirstAttempt(t *testing.T) {
	inner := &countingAction{results: []ActionResult{{Success: true}}}
	r := newRetryRegistry(map[string]Action{"mock": inner})
	ra := NewRetryAction(r)
	res := ra.Execute(context.Background(), makeRetryAC(map[string]any{
		"action":      "mock",
		"max_attempts": 3,
	}))
	if !res.Success {
		t.Fatalf("expected success, got error: %v", res.Error)
	}
	if inner.calls != 1 {
		t.Errorf("expected 1 call, got %d", inner.calls)
	}
}

func TestRetryAction_RetriesOnFailureThenSucceeds(t *testing.T) {
	inner := &countingAction{
		results: []ActionResult{
			{Error: errors.New("transient")},
			{Error: errors.New("transient")},
			{Success: true},
		},
	}
	r := newRetryRegistry(map[string]Action{"mock": inner})
	ra := NewRetryAction(r)
	res := ra.Execute(context.Background(), makeRetryAC(map[string]any{
		"action":      "mock",
		"max_attempts": 3,
	}))
	if !res.Success {
		t.Fatalf("expected success after retry, got error: %v", res.Error)
	}
	if inner.calls != 3 {
		t.Errorf("expected 3 calls, got %d", inner.calls)
	}
}

func TestRetryAction_ExhaustsAttempts(t *testing.T) {
	errFinal := errors.New("permanent failure")
	inner := &countingAction{
		results: []ActionResult{{Error: errFinal}},
	}
	r := newRetryRegistry(map[string]Action{"mock": inner})
	ra := NewRetryAction(r)
	res := ra.Execute(context.Background(), makeRetryAC(map[string]any{
		"action":      "mock",
		"max_attempts": 3,
	}))
	if res.Success {
		t.Fatal("expected failure after exhausting retries")
	}
	if !errors.Is(res.Error, errFinal) {
		t.Errorf("expected errFinal, got: %v", res.Error)
	}
	if inner.calls != 3 {
		t.Errorf("expected 3 calls, got %d", inner.calls)
	}
}

func TestRetryAction_MaxAttemptsOne_NoRetry(t *testing.T) {
	inner := &countingAction{
		results: []ActionResult{{Error: errors.New("fail")}},
	}
	r := newRetryRegistry(map[string]Action{"mock": inner})
	ra := NewRetryAction(r)
	res := ra.Execute(context.Background(), makeRetryAC(map[string]any{
		"action":      "mock",
		"max_attempts": 1,
	}))
	if res.Success {
		t.Fatal("expected failure")
	}
	if inner.calls != 1 {
		t.Errorf("expected exactly 1 call with max_attempts=1, got %d", inner.calls)
	}
}

func TestRetryAction_DefaultMaxAttempts(t *testing.T) {
	inner := &countingAction{
		results: []ActionResult{{Error: errors.New("fail")}},
	}
	r := newRetryRegistry(map[string]Action{"mock": inner})
	ra := NewRetryAction(r)
	// No max_attempts → default is 3
	ra.Execute(context.Background(), makeRetryAC(map[string]any{"action": "mock"}))
	if inner.calls != 3 {
		t.Errorf("expected 3 calls (default max_attempts), got %d", inner.calls)
	}
}

func TestRetryAction_PassesThroughAsync(t *testing.T) {
	inner := &countingAction{
		results: []ActionResult{{Async: true, Success: false}},
	}
	r := newRetryRegistry(map[string]Action{"mock": inner})
	ra := NewRetryAction(r)
	res := ra.Execute(context.Background(), makeRetryAC(map[string]any{
		"action":      "mock",
		"max_attempts": 5,
	}))
	if !res.Async {
		t.Fatal("expected async result to pass through unchanged")
	}
	if inner.calls != 1 {
		t.Errorf("expected 1 call for async pass-through, got %d", inner.calls)
	}
}

func TestRetryAction_ContextCancellationDuringDelay(t *testing.T) {
	inner := &countingAction{
		results: []ActionResult{{Error: errors.New("fail")}},
	}
	r := newRetryRegistry(map[string]Action{"mock": inner})
	ra := NewRetryAction(r)
	ac := makeRetryAC(map[string]any{
		"action":      "mock",
		"max_attempts": 5,
		"interval":    "1h", // very long delay so cancellation fires
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	res := ra.Execute(ctx, ac)
	if res.Success {
		t.Fatal("expected failure due to cancellation")
	}
	if !errors.Is(res.Error, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", res.Error)
	}
}

func TestRetryAction_ContextAlreadyCancelled(t *testing.T) {
	inner := &countingAction{
		results: []ActionResult{{Error: errors.New("fail")}},
	}
	r := newRetryRegistry(map[string]Action{"mock": inner})
	ra := NewRetryAction(r)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	res := ra.Execute(ctx, makeRetryAC(map[string]any{
		"action":      "mock",
		"max_attempts": 3,
	}))
	if res.Success {
		t.Fatal("expected failure for already-cancelled context")
	}
}

func TestRetryAction_ForwardsInnerParams(t *testing.T) {
	capture := &captureParamsAction{result: ActionResult{Success: true}}
	r := newRetryRegistry(map[string]Action{"capture": capture})
	ra := NewRetryAction(r)
	res := ra.Execute(context.Background(), makeRetryAC(map[string]any{
		"action": "capture",
		"params": map[string]any{"method": "rebase", "cleanup": true},
	}))
	if !res.Success {
		t.Fatalf("expected success: %v", res.Error)
	}
	if capture.lastParams == nil {
		t.Fatal("expected params to be forwarded to inner action")
	}
	if capture.lastParams.String("method", "") != "rebase" {
		t.Errorf("expected method=rebase, got %q", capture.lastParams.String("method", ""))
	}
	if !capture.lastParams.Bool("cleanup", false) {
		t.Error("expected cleanup=true")
	}
}

func TestRetryAction_NoDelayByDefault(t *testing.T) {
	inner := &countingAction{
		results: []ActionResult{
			{Error: errors.New("fail")},
			{Success: true},
		},
	}
	r := newRetryRegistry(map[string]Action{"mock": inner})
	ra := NewRetryAction(r)

	start := time.Now()
	res := ra.Execute(context.Background(), makeRetryAC(map[string]any{
		"action":      "mock",
		"max_attempts": 2,
		// No interval → no sleep
	}))
	elapsed := time.Since(start)

	if !res.Success {
		t.Fatalf("expected success: %v", res.Error)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("expected near-instant execution without interval, took %v", elapsed)
	}
}

func TestRetryAction_FixedBackoff(t *testing.T) {
	inner := &countingAction{
		results: []ActionResult{
			{Error: errors.New("fail")},
			{Error: errors.New("fail")},
			{Success: true},
		},
	}
	r := newRetryRegistry(map[string]Action{"mock": inner})
	ra := NewRetryAction(r)

	start := time.Now()
	res := ra.Execute(context.Background(), makeRetryAC(map[string]any{
		"action":       "mock",
		"max_attempts": 3,
		"interval":     "20ms",
		"backoff_rate": 1.0, // fixed
	}))
	elapsed := time.Since(start)

	if !res.Success {
		t.Fatalf("expected success: %v", res.Error)
	}
	// Two retries × 20ms = 40ms minimum
	if elapsed < 30*time.Millisecond {
		t.Errorf("expected at least 30ms with two 20ms fixed delays, got %v", elapsed)
	}
	if inner.calls != 3 {
		t.Errorf("expected 3 calls, got %d", inner.calls)
	}
}

func TestRetryAction_ExponentialBackoff(t *testing.T) {
	inner := &countingAction{
		results: []ActionResult{
			{Error: errors.New("fail")},
			{Error: errors.New("fail")},
			{Success: true},
		},
	}
	r := newRetryRegistry(map[string]Action{"mock": inner})
	ra := NewRetryAction(r)

	start := time.Now()
	res := ra.Execute(context.Background(), makeRetryAC(map[string]any{
		"action":       "mock",
		"max_attempts": 3,
		"interval":     "10ms",
		"backoff_rate": 2.0, // 10ms then 20ms
	}))
	elapsed := time.Since(start)

	if !res.Success {
		t.Fatalf("expected success: %v", res.Error)
	}
	// 10ms + 20ms = 30ms minimum
	if elapsed < 20*time.Millisecond {
		t.Errorf("expected at least 20ms with exponential backoff, got %v", elapsed)
	}
}

func TestRetryAction_InvalidMaxAttemptsClampedToOne(t *testing.T) {
	inner := &countingAction{
		results: []ActionResult{{Success: true}},
	}
	r := newRetryRegistry(map[string]Action{"mock": inner})
	ra := NewRetryAction(r)
	// max_attempts=0 should be clamped to 1
	res := ra.Execute(context.Background(), makeRetryAC(map[string]any{
		"action":      "mock",
		"max_attempts": 0,
	}))
	if !res.Success {
		t.Fatalf("expected success: %v", res.Error)
	}
	if inner.calls != 1 {
		t.Errorf("expected 1 call when max_attempts=0 (clamped), got %d", inner.calls)
	}
}

// TestValidate_RetryActionParams tests workflow.retry param validation.
func TestValidate_RetryActionParams(t *testing.T) {
	mkCfg := func(params map[string]any) *Config {
		return &Config{
			Start:  "step",
			Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
			States: map[string]*State{
				"step": {Type: StateTypeTask, Action: "workflow.retry", Next: "done", Params: params},
				"done": {Type: StateTypeSucceed},
			},
		}
	}

	tests := []struct {
		name       string
		params     map[string]any
		wantFields []string
	}{
		{
			name:       "valid: action only",
			params:     map[string]any{"action": "github.push"},
			wantFields: nil,
		},
		{
			name: "valid: all params",
			params: map[string]any{
				"action":       "github.push",
				"max_attempts": 5,
				"interval":     "10s",
				"backoff_rate": 2.0,
				"params":       map[string]any{"cleanup": true},
			},
			wantFields: nil,
		},
		{
			name:       "nil params",
			params:     nil,
			wantFields: []string{"states.step.params.action"},
		},
		{
			name:       "missing action",
			params:     map[string]any{"max_attempts": 3},
			wantFields: []string{"states.step.params.action"},
		},
		{
			name:       "empty action string",
			params:     map[string]any{"action": ""},
			wantFields: []string{"states.step.params.action"},
		},
		{
			name:       "unknown inner action",
			params:     map[string]any{"action": "does.not.exist"},
			wantFields: []string{"states.step.params.action"},
		},
		{
			name:       "self-referential action",
			params:     map[string]any{"action": "workflow.retry"},
			wantFields: []string{"states.step.params.action"},
		},
		{
			name:       "zero max_attempts",
			params:     map[string]any{"action": "github.push", "max_attempts": 0},
			wantFields: []string{"states.step.params.max_attempts"},
		},
		{
			name:       "negative max_attempts",
			params:     map[string]any{"action": "github.push", "max_attempts": -1},
			wantFields: []string{"states.step.params.max_attempts"},
		},
		{
			name:       "zero backoff_rate",
			params:     map[string]any{"action": "github.push", "backoff_rate": 0.0},
			wantFields: []string{"states.step.params.backoff_rate"},
		},
		{
			name:       "negative backoff_rate",
			params:     map[string]any{"action": "github.push", "backoff_rate": -1.0},
			wantFields: []string{"states.step.params.backoff_rate"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := Validate(mkCfg(tt.params))
			fieldSet := make(map[string]bool)
			for _, e := range errs {
				fieldSet[e.Field] = true
			}
			for _, wf := range tt.wantFields {
				if !fieldSet[wf] {
					t.Errorf("expected error for field %q, got errors: %v", wf, errs)
				}
			}
			if len(tt.wantFields) == 0 {
				// Filter out unrelated validation errors (e.g. retry[0] checks)
				var retryErrs []ValidationError
				for _, e := range errs {
					// Only surface errors from our state "step"
					retryErrs = append(retryErrs, e)
				}
				if len(retryErrs) > 0 {
					t.Errorf("expected no errors, got: %v", retryErrs)
				}
			}
		})
	}
}
