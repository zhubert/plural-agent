package workflow

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"time"
)

// EventChecker checks whether an external event has fired.
type EventChecker interface {
	// CheckEvent returns whether the event has fired, along with any data.
	CheckEvent(ctx context.Context, event string, params *ParamHelper, item *WorkItemView) (fired bool, data map[string]any, err error)
}

// WorkItemView is a read-only view of a work item for the engine.
type WorkItemView struct {
	ID            string
	SessionID     string
	RepoPath      string
	Branch        string
	PRURL         string
	CurrentStep   string
	Phase         string
	StepData      map[string]any
	FeedbackRounds int
	CommentsAddressed int
	StepEnteredAt time.Time

	// Extra is an opaque map for passing implementation-specific data.
	Extra map[string]any
}

// StepResult is the outcome of processing a step.
type StepResult struct {
	NewStep     string         // The step to move to (empty if no change)
	NewPhase    string         // The phase within the new step
	Terminal    bool           // True if the workflow has reached a terminal state
	TerminalOK  bool           // True if terminal state is succeed (false = fail)
	Data        map[string]any // Data to merge into step data
	BeforeHooks []HookConfig   // Before-hooks to run before the step executes
	Hooks       []HookConfig   // After-hooks to run
}

// Engine is the core workflow orchestrator.
type Engine struct {
	config       *Config
	actions      *ActionRegistry
	eventChecker EventChecker
	logger       *slog.Logger
}

// NewEngine creates a new workflow engine.
func NewEngine(cfg *Config, actions *ActionRegistry, checker EventChecker, logger *slog.Logger) *Engine {
	return &Engine{
		config:       cfg,
		actions:      actions,
		eventChecker: checker,
		logger:       logger,
	}
}

// GetStartState returns the start state name from the config.
func (e *Engine) GetStartState() string {
	return e.config.Start
}

// GetConfig returns the engine's workflow config.
func (e *Engine) GetConfig() *Config {
	return e.config
}

// ProcessStep processes the current step for a work item.
// It dispatches based on state type: succeed/fail → terminal,
// task → execute action, wait → check event.
func (e *Engine) ProcessStep(ctx context.Context, item *WorkItemView) (*StepResult, error) {
	state, ok := e.config.States[item.CurrentStep]
	if !ok {
		return nil, fmt.Errorf("unknown state %q", item.CurrentStep)
	}

	switch state.Type {
	case StateTypeSucceed:
		return &StepResult{
			Terminal:   true,
			TerminalOK: true,
			Hooks:      state.After,
		}, nil

	case StateTypeFail:
		return &StepResult{
			Terminal:   true,
			TerminalOK: false,
			Hooks:      state.After,
		}, nil

	case StateTypeTask:
		return e.processTaskState(ctx, item, state)

	case StateTypeWait:
		return e.processWaitState(ctx, item, state)

	case StateTypeChoice:
		return e.processChoiceState(item, state)

	case StateTypePass:
		return e.processPassState(item, state)

	default:
		return nil, fmt.Errorf("unsupported state type %q", state.Type)
	}
}

// processTaskState handles task state execution.
func (e *Engine) processTaskState(ctx context.Context, item *WorkItemView, state *State) (*StepResult, error) {
	action := e.actions.Get(state.Action)
	if action == nil {
		return nil, fmt.Errorf("no action registered for %q", state.Action)
	}

	params := NewParamHelper(state.Params)
	ac := &ActionContext{
		WorkItemID: item.ID,
		SessionID:  item.SessionID,
		RepoPath:   item.RepoPath,
		Branch:     item.Branch,
		Params:     params,
		Logger:     e.logger,
		Extra:      item.Extra,
	}

	result := action.Execute(ctx, ac)

	if result.Async {
		// Action spawned async work — stay on current step with async_pending phase
		return &StepResult{
			NewStep:  item.CurrentStep,
			NewPhase: "async_pending",
			Data:     result.Data,
			Hooks:    nil, // hooks run after async completes
		}, nil
	}

	if !result.Success {
		errStr := ""
		if result.Error != nil {
			errStr = result.Error.Error()
		}
		return e.handleFailure(item, state, errStr, result.Data)
	}

	// Success — reset retry count on success
	data := result.Data
	if getRetryCount(item.StepData) > 0 {
		if data == nil {
			data = make(map[string]any)
		}
		data["_retry_count"] = 0
	}

	// Follow next edge, allowing the action to override it
	nextStep := state.Next
	if result.OverrideNext != "" {
		nextStep = result.OverrideNext
	}

	return &StepResult{
		NewStep:  nextStep,
		NewPhase: "idle",
		Data:     data,
		Hooks:    state.After,
	}, nil
}

// processWaitState handles wait state event checking.
func (e *Engine) processWaitState(ctx context.Context, item *WorkItemView, state *State) (*StepResult, error) {
	if e.eventChecker == nil {
		return nil, fmt.Errorf("no event checker configured")
	}

	// Enforce timeout if configured and StepEnteredAt is set
	if state.Timeout != nil && !item.StepEnteredAt.IsZero() {
		elapsed := time.Since(item.StepEnteredAt)
		if elapsed >= state.Timeout.Duration {
			e.logger.Info("wait state timed out",
				"state", item.CurrentStep,
				"timeout", state.Timeout.Duration,
				"elapsed", elapsed,
			)

			// Use timeout_next edge if available, otherwise fall back to error edge
			if state.TimeoutNext != "" {
				return &StepResult{
					NewStep:  state.TimeoutNext,
					NewPhase: "idle",
					Data:     map[string]any{"timeout": true, "timeout_elapsed": elapsed.String()},
					Hooks:    state.After,
				}, nil
			}
			if state.Error != "" {
				return &StepResult{
					NewStep:  state.Error,
					NewPhase: "idle",
					Data:     map[string]any{"timeout": true, "timeout_elapsed": elapsed.String()},
					Hooks:    state.After,
				}, nil
			}
			return nil, fmt.Errorf("wait state %q timed out after %s with no timeout_next or error edge", item.CurrentStep, elapsed)
		}
	}

	params := NewParamHelper(state.Params)
	fired, data, err := e.eventChecker.CheckEvent(ctx, state.Event, params, item)
	if err != nil {
		e.logger.Debug("event check error", "event", state.Event, "error", err)
		// Don't advance on error — stay in current state
		return &StepResult{
			NewStep:  item.CurrentStep,
			NewPhase: item.Phase,
		}, nil
	}

	if !fired {
		// Event hasn't fired — no change
		return &StepResult{
			NewStep:  item.CurrentStep,
			NewPhase: item.Phase,
		}, nil
	}

	// Event fired — follow next edge
	return &StepResult{
		NewStep:  state.Next,
		NewPhase: "idle",
		Data:     data,
		Hooks:    state.After,
	}, nil
}

// processChoiceState evaluates choice rules against step data and transitions accordingly.
func (e *Engine) processChoiceState(item *WorkItemView, state *State) (*StepResult, error) {
	for _, rule := range state.Choices {
		if evaluateChoiceRule(rule, item.StepData) {
			return &StepResult{
				NewStep:  rule.Next,
				NewPhase: "idle",
				Hooks:    state.After,
			}, nil
		}
	}

	// No rule matched — use default
	if state.Default != "" {
		return &StepResult{
			NewStep:  state.Default,
			NewPhase: "idle",
			Hooks:    state.After,
		}, nil
	}

	return nil, fmt.Errorf("choice state %q: no rule matched and no default defined", item.CurrentStep)
}

// evaluateChoiceRule checks if a single choice rule matches against step data.
func evaluateChoiceRule(rule ChoiceRule, data map[string]any) bool {
	val, exists := lookupVariable(rule.Variable, data)

	// Handle is_present check
	if rule.IsPresent != nil {
		return exists == *rule.IsPresent
	}

	if !exists {
		// Variable doesn't exist — only not_equals can match
		if rule.NotEquals != nil {
			return true // non-existent != any value
		}
		return false
	}

	// Handle equals
	if rule.Equals != nil {
		return valuesEqual(val, rule.Equals)
	}

	// Handle not_equals
	if rule.NotEquals != nil {
		return !valuesEqual(val, rule.NotEquals)
	}

	return false
}

// lookupVariable retrieves a value from step data by key.
func lookupVariable(key string, data map[string]any) (any, bool) {
	if data == nil {
		return nil, false
	}
	val, ok := data[key]
	return val, ok
}

// valuesEqual compares two values for equality, handling numeric type coercion
// between int and float64 (common when values come from JSON unmarshaling).
func valuesEqual(a, b any) bool {
	af, aIsNum := toFloat64(a)
	bf, bIsNum := toFloat64(b)
	if aIsNum && bIsNum {
		return af == bf
	}
	return reflect.DeepEqual(a, b)
}

// toFloat64 converts numeric types to float64, returning false if v is not numeric.
func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	default:
		return 0, false
	}
}

// processPassState injects data into step data and immediately transitions to the next state.
func (e *Engine) processPassState(item *WorkItemView, state *State) (*StepResult, error) {
	return &StepResult{
		NewStep:  state.Next,
		NewPhase: "idle",
		Data:     state.Data,
		Hooks:    state.After,
	}, nil
}

// AdvanceAfterAsync is called when an async action (e.g., Claude worker) completes.
// It determines the next step based on success/failure.
func (e *Engine) AdvanceAfterAsync(item *WorkItemView, success bool) (*StepResult, error) {
	state, ok := e.config.States[item.CurrentStep]
	if !ok {
		return nil, fmt.Errorf("unknown state %q", item.CurrentStep)
	}

	if !success {
		return e.handleFailure(item, state, "async action failed", nil)
	}

	// Reset retry count on success
	var data map[string]any
	if getRetryCount(item.StepData) > 0 {
		data = map[string]any{"_retry_count": 0}
	}

	return &StepResult{
		NewStep:  state.Next,
		NewPhase: "idle",
		Data:     data,
		Hooks:    state.After,
	}, nil
}

// handleFailure processes a failure in a task state, checking retry and catch rules
// before falling back to the error edge.
func (e *Engine) handleFailure(item *WorkItemView, state *State, errStr string, data map[string]any) (*StepResult, error) {
	// Check retry rules first
	retryCount := getRetryCount(item.StepData)
	for _, retry := range state.Retry {
		if !matchesErrors(errStr, retry.Errors) {
			continue
		}
		if retryCount < retry.MaxAttempts {
			newCount := retryCount + 1
			retryData := mergeData(data, map[string]any{
				"_retry_count": newCount,
				"_last_error":  errStr,
			})

			e.logger.Info("retrying action",
				"state", item.CurrentStep,
				"attempt", newCount,
				"max_attempts", retry.MaxAttempts,
				"error", errStr,
			)

			// Calculate retry delay and store it for the daemon to enforce
			delay := retryDelay(retry, newCount)
			if delay > 0 {
				retryData["_retry_after"] = time.Now().Add(delay).Format(time.RFC3339)
			}

			return &StepResult{
				NewStep:  item.CurrentStep,
				NewPhase: "retry_pending",
				Data:     retryData,
			}, nil
		}
	}

	// Check catch rules
	for _, catch := range state.Catch {
		if matchesErrors(errStr, catch.Errors) {
			catchData := mergeData(data, map[string]any{
				"_caught_error": errStr,
				"_retry_count":  0,
			})
			return &StepResult{
				NewStep:  catch.Next,
				NewPhase: "idle",
				Data:     catchData,
				Hooks:    state.After,
			}, nil
		}
	}

	// Fall back to error edge
	if state.Error != "" {
		errorData := mergeData(data, map[string]any{
			"_last_error": errStr,
		})
		return &StepResult{
			NewStep:  state.Error,
			NewPhase: "idle",
			Data:     errorData,
			Hooks:    state.After,
		}, nil
	}

	return nil, fmt.Errorf("action %q failed with no retry, catch, or error edge: %s", state.Action, errStr)
}

// getRetryCount extracts the retry count from step data.
func getRetryCount(stepData map[string]any) int {
	if stepData == nil {
		return 0
	}
	v, ok := stepData["_retry_count"]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	default:
		return 0
	}
}

// retryDelay calculates the delay for the given retry attempt using exponential backoff.
func retryDelay(retry RetryConfig, attempt int) time.Duration {
	if retry.Interval == nil {
		return 0
	}
	base := retry.Interval.Duration
	rate := retry.BackoffRate
	if rate <= 0 {
		rate = 1.0
	}
	// Delay = interval * backoff_rate^(attempt-1)
	delay := base
	for i := 1; i < attempt; i++ {
		delay = time.Duration(float64(delay) * rate)
	}
	return delay
}

// matchesErrors checks if an error string matches any of the given patterns.
// An empty patterns list or a "*" pattern matches everything.
func matchesErrors(errStr string, patterns []string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if p == "*" {
			return true
		}
		if errStr == p {
			return true
		}
	}
	return false
}

// mergeData merges overlay into base, returning a new map.
func mergeData(base, overlay map[string]any) map[string]any {
	result := make(map[string]any)
	for k, v := range base {
		result[k] = v
	}
	for k, v := range overlay {
		result[k] = v
	}
	return result
}

// GetState returns the state definition for a given state name.
func (e *Engine) GetState(name string) *State {
	if e.config.States == nil {
		return nil
	}
	return e.config.States[name]
}

// GetBeforeHooks returns the before hooks for a given state name.
// The daemon should call this when entering a new state and run the hooks
// before processing the step. If a before hook fails, the daemon should
// follow the error edge.
func (e *Engine) GetBeforeHooks(stateName string) []HookConfig {
	state := e.GetState(stateName)
	if state == nil {
		return nil
	}
	return state.Before
}

// IsTerminalState returns true if the named state is a terminal state.
func (e *Engine) IsTerminalState(name string) bool {
	state, ok := e.config.States[name]
	if !ok {
		return false
	}
	return state.Type == StateTypeSucceed || state.Type == StateTypeFail
}
