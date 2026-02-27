package workflow

import (
	"context"
	"fmt"
	"time"
)

// RetryAction is a meta-action that wraps another registered action with
// synchronous retry logic. On failure it sleeps for the configured interval
// (scaled by backoff_rate on each subsequent retry) and re-executes the inner
// action, for up to max_attempts total attempts.
//
// Async inner actions (those that spawn a worker) are passed through unchanged
// on the first attempt, since they cannot be retried synchronously.
//
// Params:
//
//	action       string   (required) Name of the inner action to execute.
//	max_attempts int      (default 3) Total number of attempts (1 = no retry).
//	interval     duration (default none) Base delay between retries, e.g. "10s".
//	backoff_rate float64  (default 1.0) Multiplier applied to interval after each
//	                       failed attempt. 1.0 = fixed delay; 2.0 = double each time.
//	params       map      (optional) Parameters forwarded to the inner action.
type RetryAction struct {
	registry *ActionRegistry
}

// NewRetryAction creates a RetryAction backed by the given registry.
// The registry is queried at Execute() time so it may be populated after
// NewRetryAction is called.
func NewRetryAction(registry *ActionRegistry) *RetryAction {
	return &RetryAction{registry: registry}
}

// Execute runs the inner action with retry logic.
func (a *RetryAction) Execute(ctx context.Context, ac *ActionContext) ActionResult {
	innerName := ac.Params.String("action", "")
	if innerName == "" {
		return ActionResult{Error: fmt.Errorf("workflow.retry: action param is required")}
	}

	inner := a.registry.Get(innerName)
	if inner == nil {
		return ActionResult{Error: fmt.Errorf("workflow.retry: unknown action %q", innerName)}
	}

	maxAttempts := ac.Params.Int("max_attempts", 3)
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	interval := ac.Params.Duration("interval", 0)
	backoffRate := ac.Params.Float64("backoff_rate", 1.0)
	if backoffRate <= 0 {
		backoffRate = 1.0
	}

	// Build inner action context, forwarding nested params if provided.
	var innerParamMap map[string]any
	if raw := ac.Params.Raw("params"); raw != nil {
		if m, ok := raw.(map[string]any); ok {
			innerParamMap = m
		}
	}
	innerAC := &ActionContext{
		WorkItemID: ac.WorkItemID,
		SessionID:  ac.SessionID,
		RepoPath:   ac.RepoPath,
		Branch:     ac.Branch,
		Params:     NewParamHelper(innerParamMap),
		Logger:     ac.Logger,
		Extra:      ac.Extra,
	}

	var lastResult ActionResult
	delay := interval

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return ActionResult{Error: err}
		}

		result := inner.Execute(ctx, innerAC)

		// Async actions cannot be retried synchronously — pass through immediately.
		if result.Async {
			return result
		}

		if result.Success {
			return result
		}

		lastResult = result

		if attempt < maxAttempts {
			errMsg := ""
			if result.Error != nil {
				errMsg = result.Error.Error()
			}
			ac.Logger.Info("workflow.retry: action failed, retrying",
				"action", innerName,
				"attempt", attempt,
				"max_attempts", maxAttempts,
				"error", errMsg,
			)

			if delay > 0 {
				select {
				case <-time.After(delay):
				case <-ctx.Done():
					return ActionResult{Error: ctx.Err()}
				}
				delay = time.Duration(float64(delay) * backoffRate)
			}
		}
	}

	return lastResult
}
