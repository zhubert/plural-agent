package workflow

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ValidationError describes a single validation problem.
type ValidationError struct {
	Field   string
	Message string
}

// Validate checks a Config for errors and returns all problems found.
func Validate(cfg *Config) []ValidationError {
	var errs []ValidationError

	// Start state must exist
	if cfg.Start == "" {
		errs = append(errs, ValidationError{
			Field:   "start",
			Message: "start state is required",
		})
	} else if cfg.States != nil {
		if _, ok := cfg.States[cfg.Start]; !ok {
			errs = append(errs, ValidationError{
				Field:   "start",
				Message: fmt.Sprintf("start state %q does not exist", cfg.Start),
			})
		}
	}

	// States must exist
	if len(cfg.States) == 0 {
		errs = append(errs, ValidationError{
			Field:   "states",
			Message: "at least one state is required",
		})
	}

	// Validate each state
	for name, state := range cfg.States {
		errs = append(errs, validateState(name, state, cfg.States)...)
	}

	// Cycle detection
	errs = append(errs, detectCycles(cfg)...)

	// Provider validation
	errs = append(errs, validateSource(cfg)...)

	// Settings validation
	errs = append(errs, validateSettings(cfg.Settings)...)

	return errs
}

// validateState validates a single state definition.
func validateState(name string, state *State, allStates map[string]*State) []ValidationError {
	var errs []ValidationError
	prefix := fmt.Sprintf("states.%s", name)

	// Type validation
	if !ValidStateTypes[state.Type] {
		errs = append(errs, ValidationError{
			Field:   prefix + ".type",
			Message: fmt.Sprintf("unknown state type %q (must be task, wait, choice, pass, succeed, or fail)", state.Type),
		})
		return errs // Can't validate further without valid type
	}

	switch state.Type {
	case StateTypeTask:
		// Task states require action
		if state.Action == "" {
			errs = append(errs, ValidationError{
				Field:   prefix + ".action",
				Message: "action is required for task states",
			})
		} else if !ValidActions[state.Action] {
			errs = append(errs, ValidationError{
				Field:   prefix + ".action",
				Message: fmt.Sprintf("unknown action %q", state.Action),
			})
		}

		// Non-terminal states must have next
		if state.Next == "" {
			errs = append(errs, ValidationError{
				Field:   prefix + ".next",
				Message: "next is required for task states",
			})
		}

		// Validate params for ai.code action
		if state.Action == "ai.code" {
			errs = append(errs, validateCodingParams(prefix, state.Params)...)
		}

		// Validate params for github.merge action
		if state.Action == "github.merge" {
			errs = append(errs, validateMergeParams(prefix, state.Params)...)
		}

		// Validate params for github.comment_issue action
		if state.Action == "github.comment_issue" {
			errs = append(errs, validateCommentIssueParams(prefix, state.Params)...)
		}

		// Validate params for github.comment_pr action
		if state.Action == "github.comment_pr" {
			errs = append(errs, validateCommentIssueParams(prefix, state.Params)...)
		}

		// Validate params for github.add_label and github.remove_label actions
		if state.Action == "github.add_label" || state.Action == "github.remove_label" {
			errs = append(errs, validateLabelParams(prefix, state.Params)...)
		}

		// Validate params for github.request_review action
		if state.Action == "github.request_review" {
			errs = append(errs, validateRequestReviewParams(prefix, state.Params)...)
		}

		// Validate params for github.assign_pr action
		if state.Action == "github.assign_pr" {
			errs = append(errs, validateAssignPRParams(prefix, state.Params)...)
		}

		// Validate params for git.format action
		if state.Action == "git.format" {
			errs = append(errs, validateFormatParams(prefix, state.Params)...)
		}

		// Validate params for git.rebase action
		if state.Action == "git.rebase" {
			errs = append(errs, validateRebaseParams(prefix, state.Params)...)
		}

		// Validate params for git.validate_diff action
		if state.Action == "git.validate_diff" {
			errs = append(errs, validateDiffParams(prefix, state.Params)...)
		}

		// Validate params for ai.resolve_conflicts action
		if state.Action == "ai.resolve_conflicts" {
			errs = append(errs, validateResolveConflictsParams(prefix, state.Params)...)
		}

		// Validate params for workflow.retry action
		if state.Action == "workflow.retry" {
			errs = append(errs, validateRetryActionParams(prefix, state.Params)...)
		}

	case StateTypeWait:
		// Wait states require event
		if state.Event == "" {
			errs = append(errs, ValidationError{
				Field:   prefix + ".event",
				Message: "event is required for wait states",
			})
		} else if !ValidEvents[state.Event] {
			errs = append(errs, ValidationError{
				Field:   prefix + ".event",
				Message: fmt.Sprintf("unknown event %q", state.Event),
			})
		}

		// Non-terminal states must have next
		if state.Next == "" {
			errs = append(errs, ValidationError{
				Field:   prefix + ".next",
				Message: "next is required for wait states",
			})
		}

		// timeout_next requires timeout
		if state.TimeoutNext != "" && state.Timeout == nil {
			errs = append(errs, ValidationError{
				Field:   prefix + ".timeout_next",
				Message: "timeout_next requires timeout to be set",
			})
		}

		// Validate params for ci.complete event
		if state.Event == "ci.complete" {
			errs = append(errs, validateCIParams(prefix, state.Params)...)
		}

	case StateTypeChoice:
		// Choice states require at least one choice rule
		if len(state.Choices) == 0 {
			errs = append(errs, ValidationError{
				Field:   prefix + ".choices",
				Message: "at least one choice rule is required for choice states",
			})
		}
		for i, rule := range state.Choices {
			rulePrefix := fmt.Sprintf("%s.choices[%d]", prefix, i)
			if rule.Variable == "" {
				errs = append(errs, ValidationError{
					Field:   rulePrefix + ".variable",
					Message: "variable is required for choice rules",
				})
			}
			if rule.Next == "" {
				errs = append(errs, ValidationError{
					Field:   rulePrefix + ".next",
					Message: "next is required for choice rules",
				})
			} else if _, ok := allStates[rule.Next]; !ok {
				errs = append(errs, ValidationError{
					Field:   rulePrefix + ".next",
					Message: fmt.Sprintf("references non-existent state %q", rule.Next),
				})
			}
			// Must have at least one condition
			if rule.Equals == nil && rule.NotEquals == nil && rule.IsPresent == nil {
				errs = append(errs, ValidationError{
					Field:   rulePrefix,
					Message: "choice rule must have at least one condition (equals, not_equals, or is_present)",
				})
			}
		}
		// Validate default state reference if present
		if state.Default != "" {
			if _, ok := allStates[state.Default]; !ok {
				errs = append(errs, ValidationError{
					Field:   prefix + ".default",
					Message: fmt.Sprintf("references non-existent state %q", state.Default),
				})
			}
		}

	case StateTypePass:
		// Pass states require next
		if state.Next == "" {
			errs = append(errs, ValidationError{
				Field:   prefix + ".next",
				Message: "next is required for pass states",
			})
		}

	case StateTypeSucceed, StateTypeFail:
		// Terminal states must not have next
		if state.Next != "" {
			errs = append(errs, ValidationError{
				Field:   prefix + ".next",
				Message: "terminal states must not have next",
			})
		}
	}

	// Validate retry configs
	for i, retry := range state.Retry {
		retryPrefix := fmt.Sprintf("%s.retry[%d]", prefix, i)
		if retry.MaxAttempts <= 0 {
			errs = append(errs, ValidationError{
				Field:   retryPrefix + ".max_attempts",
				Message: "max_attempts must be greater than 0",
			})
		}
		if retry.BackoffRate < 0 {
			errs = append(errs, ValidationError{
				Field:   retryPrefix + ".backoff_rate",
				Message: "backoff_rate must not be negative",
			})
		}
	}

	// Validate catch configs
	for i, catch := range state.Catch {
		catchPrefix := fmt.Sprintf("%s.catch[%d]", prefix, i)
		if catch.Next == "" {
			errs = append(errs, ValidationError{
				Field:   catchPrefix + ".next",
				Message: "next is required for catch rules",
			})
		} else if _, ok := allStates[catch.Next]; !ok {
			errs = append(errs, ValidationError{
				Field:   catchPrefix + ".next",
				Message: fmt.Sprintf("references non-existent state %q", catch.Next),
			})
		}
	}

	// Validate next/error references exist
	if state.Next != "" {
		if _, ok := allStates[state.Next]; !ok {
			errs = append(errs, ValidationError{
				Field:   prefix + ".next",
				Message: fmt.Sprintf("references non-existent state %q", state.Next),
			})
		}
	}
	if state.Error != "" {
		if _, ok := allStates[state.Error]; !ok {
			errs = append(errs, ValidationError{
				Field:   prefix + ".error",
				Message: fmt.Sprintf("references non-existent state %q", state.Error),
			})
		}
	}
	if state.TimeoutNext != "" {
		if _, ok := allStates[state.TimeoutNext]; !ok {
			errs = append(errs, ValidationError{
				Field:   prefix + ".timeout_next",
				Message: fmt.Sprintf("references non-existent state %q", state.TimeoutNext),
			})
		}
	}

	return errs
}

// validateCodingParams validates params for ai.code actions.
func validateCodingParams(prefix string, params map[string]any) []ValidationError {
	var errs []ValidationError
	if params == nil {
		return errs
	}

	// Validate system_prompt path if present
	if sp, ok := params["system_prompt"]; ok {
		if s, ok := sp.(string); ok {
			errs = append(errs, validatePromptPath(prefix+".params.system_prompt", s)...)
		}
	}

	// Validate format_command if present (must be a non-empty string)
	if fc, ok := params["format_command"]; ok {
		switch v := fc.(type) {
		case string:
			if v == "" {
				errs = append(errs, ValidationError{
					Field:   prefix + ".params.format_command",
					Message: "format_command must not be empty",
				})
			}
		default:
			errs = append(errs, ValidationError{
				Field:   prefix + ".params.format_command",
				Message: "format_command must be a string",
			})
		}
	}

	return errs
}

// validateMergeParams validates params for github.merge actions.
func validateMergeParams(prefix string, params map[string]any) []ValidationError {
	var errs []ValidationError
	if params == nil {
		return errs
	}

	if method, ok := params["method"]; ok {
		if s, ok := method.(string); ok {
			switch s {
			case "rebase", "squash", "merge":
				// valid
			default:
				errs = append(errs, ValidationError{
					Field:   prefix + ".params.method",
					Message: fmt.Sprintf("unknown merge method %q (must be rebase, squash, or merge)", s),
				})
			}
		}
	}

	return errs
}

// validateCommentIssueParams validates params for github.comment_issue actions.
func validateCommentIssueParams(prefix string, params map[string]any) []ValidationError {
	var errs []ValidationError

	if params == nil {
		errs = append(errs, ValidationError{
			Field:   prefix + ".params.body",
			Message: "body is required for github.comment_issue action",
		})
		return errs
	}

	body, ok := params["body"]
	if !ok || body == nil {
		errs = append(errs, ValidationError{
			Field:   prefix + ".params.body",
			Message: "body is required for github.comment_issue action",
		})
		return errs
	}

	if s, ok := body.(string); ok {
		if s == "" {
			errs = append(errs, ValidationError{
				Field:   prefix + ".params.body",
				Message: "body must not be empty",
			})
		} else {
			errs = append(errs, validatePromptPath(prefix+".params.body", s)...)
		}
	}

	return errs
}

// validateLabelParams validates params for github.add_label and github.remove_label actions.
func validateLabelParams(prefix string, params map[string]any) []ValidationError {
	var errs []ValidationError

	if params == nil {
		errs = append(errs, ValidationError{
			Field:   prefix + ".params.label",
			Message: "label is required for label actions",
		})
		return errs
	}

	label, ok := params["label"]
	if !ok || label == nil {
		errs = append(errs, ValidationError{
			Field:   prefix + ".params.label",
			Message: "label is required for label actions",
		})
		return errs
	}

	if s, ok := label.(string); ok && s == "" {
		errs = append(errs, ValidationError{
			Field:   prefix + ".params.label",
			Message: "label must not be empty",
		})
	}

	return errs
}

// validateRequestReviewParams validates params for github.request_review actions.
func validateRequestReviewParams(prefix string, params map[string]any) []ValidationError {
	var errs []ValidationError

	if params == nil {
		errs = append(errs, ValidationError{
			Field:   prefix + ".params.reviewer",
			Message: "reviewer is required for github.request_review action",
		})
		return errs
	}

	reviewer, ok := params["reviewer"]
	if !ok || reviewer == nil {
		errs = append(errs, ValidationError{
			Field:   prefix + ".params.reviewer",
			Message: "reviewer is required for github.request_review action",
		})
		return errs
	}

	if s, ok := reviewer.(string); ok && s == "" {
		errs = append(errs, ValidationError{
			Field:   prefix + ".params.reviewer",
			Message: "reviewer must not be empty",
		})
	}

	return errs
}

// validateAssignPRParams validates params for github.assign_pr actions.
func validateAssignPRParams(prefix string, params map[string]any) []ValidationError {
	var errs []ValidationError

	if params == nil {
		errs = append(errs, ValidationError{
			Field:   prefix + ".params.assignee",
			Message: "assignee is required for github.assign_pr action",
		})
		return errs
	}

	assignee, ok := params["assignee"]
	if !ok || assignee == nil {
		errs = append(errs, ValidationError{
			Field:   prefix + ".params.assignee",
			Message: "assignee is required for github.assign_pr action",
		})
		return errs
	}

	if s, ok := assignee.(string); ok && s == "" {
		errs = append(errs, ValidationError{
			Field:   prefix + ".params.assignee",
			Message: "assignee must not be empty",
		})
	}

	return errs
}

// validateFormatParams validates params for git.format actions.
func validateFormatParams(prefix string, params map[string]any) []ValidationError {
	var errs []ValidationError

	if params == nil {
		errs = append(errs, ValidationError{
			Field:   prefix + ".params.command",
			Message: "command is required for git.format action",
		})
		return errs
	}

	command, ok := params["command"]
	if !ok || command == nil {
		errs = append(errs, ValidationError{
			Field:   prefix + ".params.command",
			Message: "command is required for git.format action",
		})
		return errs
	}

	if s, ok := command.(string); ok && s == "" {
		errs = append(errs, ValidationError{
			Field:   prefix + ".params.command",
			Message: "command must not be empty",
		})
	}

	return errs
}

// validateRebaseParams validates params for git.rebase actions.
func validateRebaseParams(prefix string, params map[string]any) []ValidationError {
	var errs []ValidationError
	if params == nil {
		return errs
	}

	if v, ok := params["max_rebase_rounds"]; ok {
		switch n := v.(type) {
		case int:
			if n <= 0 {
				errs = append(errs, ValidationError{
					Field:   prefix + ".params.max_rebase_rounds",
					Message: "max_rebase_rounds must be greater than 0",
				})
			}
		case float64:
			if n <= 0 {
				errs = append(errs, ValidationError{
					Field:   prefix + ".params.max_rebase_rounds",
					Message: "max_rebase_rounds must be greater than 0",
				})
			}
		}
	}

	return errs
}

// validateResolveConflictsParams validates params for ai.resolve_conflicts actions.
func validateResolveConflictsParams(prefix string, params map[string]any) []ValidationError {
	var errs []ValidationError
	if params == nil {
		return errs
	}

	if v, ok := params["max_conflict_rounds"]; ok {
		switch n := v.(type) {
		case int:
			if n <= 0 {
				errs = append(errs, ValidationError{
					Field:   prefix + ".params.max_conflict_rounds",
					Message: "max_conflict_rounds must be greater than 0",
				})
			}
		case float64:
			if n <= 0 {
				errs = append(errs, ValidationError{
					Field:   prefix + ".params.max_conflict_rounds",
					Message: "max_conflict_rounds must be greater than 0",
				})
			}
		}
	}

	return errs
}

// validateCIParams validates params for ci.complete events.
func validateCIParams(prefix string, params map[string]any) []ValidationError {
	var errs []ValidationError
	if params == nil {
		return errs
	}

	if onFailure, ok := params["on_failure"]; ok {
		if s, ok := onFailure.(string); ok {
			switch s {
			case "abandon", "retry", "notify", "fix":
				// valid
			default:
				errs = append(errs, ValidationError{
					Field:   prefix + ".params.on_failure",
					Message: fmt.Sprintf("unknown on_failure policy %q (must be abandon, retry, notify, or fix)", s),
				})
			}
		}
	}

	return errs
}

// validateSource validates the source configuration.
func validateSource(cfg *Config) []ValidationError {
	var errs []ValidationError

	switch cfg.Source.Provider {
	case "github", "asana", "linear":
		// valid
	case "":
		errs = append(errs, ValidationError{
			Field:   "source.provider",
			Message: "provider is required",
		})
	default:
		errs = append(errs, ValidationError{
			Field:   "source.provider",
			Message: fmt.Sprintf("unknown provider %q (must be github, asana, or linear)", cfg.Source.Provider),
		})
	}

	// Provider-specific filter requirements
	switch cfg.Source.Provider {
	case "github":
		if cfg.Source.Filter.Label == "" {
			errs = append(errs, ValidationError{
				Field:   "source.filter.label",
				Message: "label is required for github provider",
			})
		}
	case "asana":
		if cfg.Source.Filter.Project == "" {
			errs = append(errs, ValidationError{
				Field:   "source.filter.project",
				Message: "project is required for asana provider",
			})
		}
	case "linear":
		if cfg.Source.Filter.Team == "" {
			errs = append(errs, ValidationError{
				Field:   "source.filter.team",
				Message: "team is required for linear provider",
			})
		}
	}

	return errs
}

// validateSettings validates the optional settings section.
func validateSettings(s *SettingsConfig) []ValidationError {
	if s == nil {
		return nil
	}
	var errs []ValidationError
	if s.MaxConcurrent < 0 {
		errs = append(errs, ValidationError{
			Field:   "settings.max_concurrent",
			Message: "max_concurrent must not be negative",
		})
	}
	return errs
}

// detectCycles performs DFS-based cycle detection on the state graph.
// Only non-terminal forward edges (next, error, timeout_next) are checked;
// retry loops (which stay on the same step) are intentional and excluded.
func detectCycles(cfg *Config) []ValidationError {
	var errs []ValidationError
	if len(cfg.States) == 0 || cfg.Start == "" {
		return errs
	}

	// Build adjacency list from all transition edges
	edges := make(map[string][]string)
	for name, state := range cfg.States {
		var targets []string
		if state.Next != "" {
			targets = append(targets, state.Next)
		}
		if state.Error != "" {
			targets = append(targets, state.Error)
		}
		if state.TimeoutNext != "" {
			targets = append(targets, state.TimeoutNext)
		}
		if state.Default != "" {
			targets = append(targets, state.Default)
		}
		for _, rule := range state.Choices {
			if rule.Next != "" {
				targets = append(targets, rule.Next)
			}
		}
		for _, catch := range state.Catch {
			if catch.Next != "" {
				targets = append(targets, catch.Next)
			}
		}
		edges[name] = targets
	}

	// DFS with coloring: 0=white (unvisited), 1=gray (in stack), 2=black (done)
	color := make(map[string]int)
	var path []string

	var dfs func(node string) bool
	dfs = func(node string) bool {
		color[node] = 1 // gray
		path = append(path, node)

		for _, next := range edges[node] {
			if next == node {
				continue // self-loops from retry are intentional
			}
			switch color[next] {
			case 1: // back edge → cycle
				// Find the cycle start in path
				cycleStart := -1
				for i, n := range path {
					if n == next {
						cycleStart = i
						break
					}
				}
				// Allow cycles that pass through a choice state (bounded loops).
				// Choice states provide conditional exits, making such loops intentional.
				hasChoice := false
				for _, n := range path[cycleStart:] {
					if s, ok := cfg.States[n]; ok && s.Type == StateTypeChoice {
						hasChoice = true
						break
					}
				}
				if !hasChoice {
					cycle := strings.Join(path[cycleStart:], " → ") + " → " + next
					errs = append(errs, ValidationError{
						Field:   "states",
						Message: fmt.Sprintf("cycle detected: %s", cycle),
					})
					return true
				}
				// Allowed cycle (passes through choice state) — continue
				// exploring other edges from this node so that all paths
				// through the choice are visited.
			case 0: // unvisited
				if dfs(next) {
					return true
				}
			}
		}

		path = path[:len(path)-1]
		color[node] = 2 // black
		return false
	}

	// Start from the start state
	if _, ok := cfg.States[cfg.Start]; ok {
		dfs(cfg.Start)
	}

	// Also check any unreachable states for cycles
	for name := range cfg.States {
		if color[name] == 0 {
			dfs(name)
		}
	}

	return errs
}

// validateDiffParams validates params for git.validate_diff actions.
func validateDiffParams(prefix string, params map[string]any) []ValidationError {
	var errs []ValidationError
	if params == nil {
		return errs
	}

	// max_diff_lines must be a positive integer when present.
	if v, ok := params["max_diff_lines"]; ok {
		switch n := v.(type) {
		case int:
			if n <= 0 {
				errs = append(errs, ValidationError{
					Field:   prefix + ".params.max_diff_lines",
					Message: "max_diff_lines must be greater than 0",
				})
			}
		case float64:
			if n <= 0 {
				errs = append(errs, ValidationError{
					Field:   prefix + ".params.max_diff_lines",
					Message: "max_diff_lines must be greater than 0",
				})
			}
		}
	}

	// max_lock_file_lines must be a positive integer when present.
	if v, ok := params["max_lock_file_lines"]; ok {
		switch n := v.(type) {
		case int:
			if n <= 0 {
				errs = append(errs, ValidationError{
					Field:   prefix + ".params.max_lock_file_lines",
					Message: "max_lock_file_lines must be greater than 0",
				})
			}
		case float64:
			if n <= 0 {
				errs = append(errs, ValidationError{
					Field:   prefix + ".params.max_lock_file_lines",
					Message: "max_lock_file_lines must be greater than 0",
				})
			}
		}
	}

	return errs
}

// validateRetryActionParams validates params for workflow.retry actions.
func validateRetryActionParams(prefix string, params map[string]any) []ValidationError {
	var errs []ValidationError

	if params == nil {
		errs = append(errs, ValidationError{
			Field:   prefix + ".params.action",
			Message: "action is required for workflow.retry",
		})
		return errs
	}

	actionVal, ok := params["action"]
	if !ok || actionVal == nil {
		errs = append(errs, ValidationError{
			Field:   prefix + ".params.action",
			Message: "action is required for workflow.retry",
		})
	} else if s, ok := actionVal.(string); !ok || s == "" {
		errs = append(errs, ValidationError{
			Field:   prefix + ".params.action",
			Message: "action must be a non-empty string",
		})
	} else {
		if s == "workflow.retry" {
			errs = append(errs, ValidationError{
				Field:   prefix + ".params.action",
				Message: "workflow.retry cannot wrap itself",
			})
		} else if !ValidActions[s] {
			errs = append(errs, ValidationError{
				Field:   prefix + ".params.action",
				Message: fmt.Sprintf("unknown action %q", s),
			})
		}
	}

	if v, ok := params["max_attempts"]; ok {
		switch n := v.(type) {
		case int:
			if n <= 0 {
				errs = append(errs, ValidationError{
					Field:   prefix + ".params.max_attempts",
					Message: "max_attempts must be greater than 0",
				})
			}
		case float64:
			if n <= 0 {
				errs = append(errs, ValidationError{
					Field:   prefix + ".params.max_attempts",
					Message: "max_attempts must be greater than 0",
				})
			}
		}
	}

	if v, ok := params["backoff_rate"]; ok {
		switch n := v.(type) {
		case float64:
			if n <= 0 {
				errs = append(errs, ValidationError{
					Field:   prefix + ".params.backoff_rate",
					Message: "backoff_rate must be greater than 0",
				})
			}
		case int:
			if n <= 0 {
				errs = append(errs, ValidationError{
					Field:   prefix + ".params.backoff_rate",
					Message: "backoff_rate must be greater than 0",
				})
			}
		}
	}

	return errs
}

// validatePromptPath checks that a file: path doesn't escape the repo root.
func validatePromptPath(field, value string) []ValidationError {
	if value == "" || !strings.HasPrefix(value, "file:") {
		return nil
	}

	path := strings.TrimPrefix(value, "file:")
	cleaned := filepath.Clean(path)

	if filepath.IsAbs(cleaned) {
		return []ValidationError{{
			Field:   field,
			Message: "file path must be relative (not absolute)",
		}}
	}

	if strings.HasPrefix(cleaned, "..") {
		return []ValidationError{{
			Field:   field,
			Message: "file path must not escape the repository root",
		}}
	}

	return nil
}
