// Package workflow provides configurable workflow definitions for the erg daemon.
// Workflows are defined in .erg/workflow.yaml per repository.
package workflow

import (
	"fmt"
	"time"
)

// StateType represents the kind of state in the workflow graph.
type StateType string

const (
	StateTypeTask    StateType = "task"
	StateTypeWait    StateType = "wait"
	StateTypeChoice  StateType = "choice"
	StateTypePass    StateType = "pass"
	StateTypeSucceed StateType = "succeed"
	StateTypeFail    StateType = "fail"
)

// Config is the top-level workflow configuration.
type Config struct {
	Workflow string            `yaml:"workflow"`
	Start    string            `yaml:"start"`
	Source   SourceConfig      `yaml:"source"`
	States   map[string]*State `yaml:"states"`
	Settings *SettingsConfig   `yaml:"settings,omitempty"`
}

// SettingsConfig holds agent-level settings that can be specified in the workflow YAML.
type SettingsConfig struct {
	ContainerImage string `yaml:"container_image,omitempty"`
	BranchPrefix   string `yaml:"branch_prefix,omitempty"`
	MaxConcurrent  int    `yaml:"max_concurrent,omitempty"`
	CleanupMerged  *bool  `yaml:"cleanup_merged,omitempty"`
	MaxTurns       int    `yaml:"max_turns,omitempty"`
	MaxDuration    int    `yaml:"max_duration,omitempty"` // minutes
	AutoMerge      *bool  `yaml:"auto_merge,omitempty"`
	MergeMethod    string `yaml:"merge_method,omitempty"`
}

// State represents a single node in the workflow graph.
type State struct {
	Type        StateType      `yaml:"type"`
	Action      string         `yaml:"action,omitempty"`
	Event       string         `yaml:"event,omitempty"`
	Params      map[string]any `yaml:"params,omitempty"`
	Next        string         `yaml:"next,omitempty"`
	Error       string         `yaml:"error,omitempty"`
	Timeout     *Duration      `yaml:"timeout,omitempty"`
	TimeoutNext string         `yaml:"timeout_next,omitempty"`
	Retry       []RetryConfig  `yaml:"retry,omitempty"`
	Catch       []CatchConfig  `yaml:"catch,omitempty"`
	Choices     []ChoiceRule   `yaml:"choices,omitempty"`
	Default     string         `yaml:"default,omitempty"`
	Data        map[string]any `yaml:"data,omitempty"`
	Before      []HookConfig   `yaml:"before,omitempty"`
	After       []HookConfig   `yaml:"after,omitempty"`
}

// ChoiceRule defines a conditional branch in a choice state.
// The rule evaluates a variable from step data against a condition.
type ChoiceRule struct {
	Variable    string `yaml:"variable"`              // Step data key to evaluate
	Equals      any    `yaml:"equals,omitempty"`      // Exact equality comparison
	NotEquals   any    `yaml:"not_equals,omitempty"`   // Inequality comparison
	IsPresent   *bool  `yaml:"is_present,omitempty"`   // Check if variable exists in data
	Next        string `yaml:"next"`                  // State to transition to if matched
}

// RetryConfig defines retry behavior for a state on failure.
type RetryConfig struct {
	MaxAttempts int       `yaml:"max_attempts"`
	Interval    *Duration `yaml:"interval,omitempty"`
	BackoffRate float64   `yaml:"backoff_rate,omitempty"`
	Errors      []string  `yaml:"errors,omitempty"` // Error patterns to match ("*" matches all)
}

// CatchConfig defines error catching with a transition to another state.
type CatchConfig struct {
	Errors []string `yaml:"errors,omitempty"` // Error patterns to match ("*" matches all)
	Next   string   `yaml:"next"`
}

// SourceConfig defines where issues come from.
type SourceConfig struct {
	Provider string       `yaml:"provider"`
	Filter   FilterConfig `yaml:"filter"`
}

// FilterConfig holds provider-specific filter parameters.
type FilterConfig struct {
	Label   string `yaml:"label"`   // GitHub: issue label to poll
	Project string `yaml:"project"` // Asana: project GID
	Team    string `yaml:"team"`    // Linear: team ID
}

// HookConfig defines a hook to run after a workflow step.
type HookConfig struct {
	Run string `yaml:"run"`
}

// Duration is a wrapper around time.Duration that implements YAML unmarshaling
// from human-readable strings like "30m", "2h".
type Duration struct {
	time.Duration
}

// UnmarshalYAML implements yaml.Unmarshaler for Duration.
func (d *Duration) UnmarshalYAML(unmarshal func(any) error) error {
	var s string
	if err := unmarshal(&s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = parsed
	return nil
}

// MarshalYAML implements yaml.Marshaler for Duration.
func (d Duration) MarshalYAML() (any, error) {
	return d.Duration.String(), nil
}

// ValidActions is the set of recognized action names for task states.
var ValidActions = map[string]bool{
	"ai.code":               true,
	"github.create_pr":      true,
	"github.push":           true,
	"github.merge":          true,
	"github.comment_issue":  true,
	"github.comment_pr":     true,
	"github.add_label":      true,
	"github.remove_label":   true,
	"github.close_issue":    true,
	"github.request_review": true,
	"ai.fix_ci":             true,
	"ai.resolve_conflicts":  true,
	"ai.address_review":     true,
	"git.format":            true,
	"git.rebase":            true,
	"asana.comment":         true,
	"linear.comment":        true,
	"workflow.wait":         true,
}

// RetryableActions is the set of network-bound actions that should be retried
// by default when no explicit retry config is provided. These are actions that
// make network calls and are susceptible to transient failures.
var RetryableActions = map[string]bool{
	"github.create_pr":      true,
	"github.push":           true,
	"github.merge":          true,
	"github.comment_issue":  true,
	"github.comment_pr":     true,
	"github.add_label":      true,
	"github.remove_label":   true,
	"github.close_issue":    true,
	"github.request_review": true,
	"git.rebase":            true,
	"asana.comment":         true,
	"linear.comment":        true,
}

// DefaultRetryConfig returns a standard retry configuration for network-bound actions:
// 3 attempts, 15s interval, 2.0x exponential backoff (delays: 15s, 30s, 60s).
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts: 3,
		Interval:    &Duration{15 * time.Second},
		BackoffRate: 2.0,
	}
}

// DefaultRetryForAction returns a default retry config slice for the given action
// if it is a known retryable (network-bound) action. Returns nil for non-retryable
// actions like ai.code and ai.fix_ci (expensive) or git.format (local-only).
func DefaultRetryForAction(action string) []RetryConfig {
	if RetryableActions[action] {
		return []RetryConfig{DefaultRetryConfig()}
	}
	return nil
}

// ValidEvents is the set of recognized event names for wait states.
var ValidEvents = map[string]bool{
	"pr.reviewed":       true,
	"ci.complete":       true,
	"ci.wait_for_checks": true,
	"pr.mergeable":      true,
	"gate.approved":     true,
}

// ValidStateTypes is the set of recognized state types.
var ValidStateTypes = map[StateType]bool{
	StateTypeTask:    true,
	StateTypeWait:    true,
	StateTypeChoice:  true,
	StateTypePass:    true,
	StateTypeSucceed: true,
	StateTypeFail:    true,
}
