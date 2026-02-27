package workflow

import (
	"testing"
)

func TestValidate(t *testing.T) {
	validDefault := DefaultWorkflowConfig()

	tests := []struct {
		name       string
		cfg        *Config
		wantFields []string // expected error fields (empty = no errors)
	}{
		{
			name:       "valid default config",
			cfg:        validDefault,
			wantFields: nil,
		},
		{
			name: "valid github config",
			cfg: &Config{
				Start: "coding",
				Source: SourceConfig{
					Provider: "github",
					Filter:   FilterConfig{Label: "queued"},
				},
				States: map[string]*State{
					"coding": {Type: StateTypeTask, Action: "ai.code", Next: "done", Error: "failed"},
					"done":   {Type: StateTypeSucceed},
					"failed": {Type: StateTypeFail},
				},
			},
			wantFields: nil,
		},
		{
			name: "valid asana config",
			cfg: &Config{
				Start: "coding",
				Source: SourceConfig{
					Provider: "asana",
					Filter:   FilterConfig{Project: "12345"},
				},
				States: map[string]*State{
					"coding": {Type: StateTypeTask, Action: "ai.code", Next: "done"},
					"done":   {Type: StateTypeSucceed},
				},
			},
			wantFields: nil,
		},
		{
			name: "valid linear config",
			cfg: &Config{
				Start: "coding",
				Source: SourceConfig{
					Provider: "linear",
					Filter:   FilterConfig{Team: "my-team"},
				},
				States: map[string]*State{
					"coding": {Type: StateTypeTask, Action: "ai.code", Next: "done"},
					"done":   {Type: StateTypeSucceed},
				},
			},
			wantFields: nil,
		},
		{
			name:       "empty provider",
			cfg:        &Config{Start: "s", States: map[string]*State{"s": {Type: StateTypeSucceed}}},
			wantFields: []string{"source.provider"},
		},
		{
			name: "unknown provider",
			cfg: &Config{
				Start:  "s",
				Source: SourceConfig{Provider: "jira"},
				States: map[string]*State{"s": {Type: StateTypeSucceed}},
			},
			wantFields: []string{"source.provider"},
		},
		{
			name: "github missing label",
			cfg: &Config{
				Start:  "s",
				Source: SourceConfig{Provider: "github"},
				States: map[string]*State{"s": {Type: StateTypeSucceed}},
			},
			wantFields: []string{"source.filter.label"},
		},
		{
			name: "asana missing project",
			cfg: &Config{
				Start:  "s",
				Source: SourceConfig{Provider: "asana"},
				States: map[string]*State{"s": {Type: StateTypeSucceed}},
			},
			wantFields: []string{"source.filter.project"},
		},
		{
			name: "linear missing team",
			cfg: &Config{
				Start:  "s",
				Source: SourceConfig{Provider: "linear"},
				States: map[string]*State{"s": {Type: StateTypeSucceed}},
			},
			wantFields: []string{"source.filter.team"},
		},
		{
			name:       "missing start",
			cfg:        &Config{States: map[string]*State{"s": {Type: StateTypeSucceed}}, Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}}},
			wantFields: []string{"start"},
		},
		{
			name: "start references non-existent state",
			cfg: &Config{
				Start:  "nonexistent",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{"coding": {Type: StateTypeTask, Action: "ai.code", Next: "done"}, "done": {Type: StateTypeSucceed}},
			},
			wantFields: []string{"start"},
		},
		{
			name: "no states",
			cfg: &Config{
				Start:  "coding",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
			},
			wantFields: []string{"states"},
		},
		{
			name: "task missing action",
			cfg: &Config{
				Start:  "t",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"t":    {Type: StateTypeTask, Next: "done"},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: []string{"states.t.action"},
		},
		{
			name: "task unknown action",
			cfg: &Config{
				Start:  "t",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"t":    {Type: StateTypeTask, Action: "unknown.action", Next: "done"},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: []string{"states.t.action"},
		},
		{
			name: "task missing next",
			cfg: &Config{
				Start:  "t",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"t": {Type: StateTypeTask, Action: "ai.code"},
				},
			},
			wantFields: []string{"states.t.next"},
		},
		{
			name: "wait missing event",
			cfg: &Config{
				Start:  "w",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"w":    {Type: StateTypeWait, Next: "done"},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: []string{"states.w.event"},
		},
		{
			name: "wait unknown event",
			cfg: &Config{
				Start:  "w",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"w":    {Type: StateTypeWait, Event: "unknown.event", Next: "done"},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: []string{"states.w.event"},
		},
		{
			name: "terminal state with next",
			cfg: &Config{
				Start:  "done",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"done": {Type: StateTypeSucceed, Next: "other"},
				},
			},
			wantFields: []string{"states.done.next"},
		},
		{
			name: "next references non-existent state",
			cfg: &Config{
				Start:  "t",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"t": {Type: StateTypeTask, Action: "ai.code", Next: "nonexistent"},
				},
			},
			wantFields: []string{"states.t.next"},
		},
		{
			name: "error references non-existent state",
			cfg: &Config{
				Start:  "t",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"t":    {Type: StateTypeTask, Action: "ai.code", Next: "done", Error: "nonexistent"},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: []string{"states.t.error"},
		},
		{
			name: "invalid merge method in params",
			cfg: &Config{
				Start:  "m",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"m":    {Type: StateTypeTask, Action: "github.merge", Params: map[string]any{"method": "yolo"}, Next: "done"},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: []string{"states.m.params.method"},
		},
		{
			name: "invalid on_failure in ci params",
			cfg: &Config{
				Start:  "ci",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"ci":   {Type: StateTypeWait, Event: "ci.complete", Params: map[string]any{"on_failure": "explode"}, Next: "done"},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: []string{"states.ci.params.on_failure"},
		},
		{
			name: "system prompt absolute path in coding params",
			cfg: &Config{
				Start:  "c",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"c":    {Type: StateTypeTask, Action: "ai.code", Params: map[string]any{"system_prompt": "file:/etc/passwd"}, Next: "done"},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: []string{"states.c.params.system_prompt"},
		},
		{
			name: "system prompt path traversal",
			cfg: &Config{
				Start:  "c",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"c":    {Type: StateTypeTask, Action: "ai.code", Params: map[string]any{"system_prompt": "file:../../etc/passwd"}, Next: "done"},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: []string{"states.c.params.system_prompt"},
		},
		{
			name: "valid system prompt file path",
			cfg: &Config{
				Start:  "c",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"c":    {Type: StateTypeTask, Action: "ai.code", Params: map[string]any{"system_prompt": "file:./prompts/coding.md"}, Next: "done"},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: nil,
		},
		{
			name: "ai.code empty format_command rejected",
			cfg: &Config{
				Start:  "c",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"c":    {Type: StateTypeTask, Action: "ai.code", Params: map[string]any{"format_command": ""}, Next: "done"},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: []string{"states.c.params.format_command"},
		},
		{
			name: "ai.code non-string format_command rejected",
			cfg: &Config{
				Start:  "c",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"c":    {Type: StateTypeTask, Action: "ai.code", Params: map[string]any{"format_command": 42}, Next: "done"},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: []string{"states.c.params.format_command"},
		},
		{
			name: "ai.code valid format_command accepted",
			cfg: &Config{
				Start:  "c",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"c":    {Type: StateTypeTask, Action: "ai.code", Params: map[string]any{"format_command": "go fmt ./..."}, Next: "done"},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: nil,
		},
		{
			name: "ai.code valid format_command with format_message accepted",
			cfg: &Config{
				Start:  "c",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"c":    {Type: StateTypeTask, Action: "ai.code", Params: map[string]any{"format_command": "go fmt ./...", "format_message": "chore: go fmt"}, Next: "done"},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: nil,
		},
		{
			name: "unknown state type",
			cfg: &Config{
				Start:  "x",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"x": {Type: "bogus"},
				},
			},
			wantFields: []string{"states.x.type"},
		},
		{
			name: "github.comment_issue missing params",
			cfg: &Config{
				Start:  "c",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"c":    {Type: StateTypeTask, Action: "github.comment_issue", Next: "done"},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: []string{"states.c.params.body"},
		},
		{
			name: "github.comment_issue missing body param",
			cfg: &Config{
				Start:  "c",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"c":    {Type: StateTypeTask, Action: "github.comment_issue", Params: map[string]any{"other": "value"}, Next: "done"},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: []string{"states.c.params.body"},
		},
		{
			name: "github.comment_issue empty body",
			cfg: &Config{
				Start:  "c",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"c":    {Type: StateTypeTask, Action: "github.comment_issue", Params: map[string]any{"body": ""}, Next: "done"},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: []string{"states.c.params.body"},
		},
		{
			name: "github.comment_issue valid body",
			cfg: &Config{
				Start:  "c",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"c":    {Type: StateTypeTask, Action: "github.comment_issue", Params: map[string]any{"body": "Starting work on this issue!"}, Next: "done"},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: nil,
		},
		{
			name: "github.comment_issue valid file body path",
			cfg: &Config{
				Start:  "c",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"c":    {Type: StateTypeTask, Action: "github.comment_issue", Params: map[string]any{"body": "file:templates/comment.md"}, Next: "done"},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: nil,
		},
		{
			name: "github.comment_issue body absolute path",
			cfg: &Config{
				Start:  "c",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"c":    {Type: StateTypeTask, Action: "github.comment_issue", Params: map[string]any{"body": "file:/etc/passwd"}, Next: "done"},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: []string{"states.c.params.body"},
		},
		{
			name: "github.comment_issue body path traversal",
			cfg: &Config{
				Start:  "c",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"c":    {Type: StateTypeTask, Action: "github.comment_issue", Params: map[string]any{"body": "file:../../etc/passwd"}, Next: "done"},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: []string{"states.c.params.body"},
		},
		{
			name: "valid pass state",
			cfg: &Config{
				Start:  "setup",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"setup": {Type: StateTypePass, Data: map[string]any{"key": "val"}, Next: "done"},
					"done":  {Type: StateTypeSucceed},
				},
			},
			wantFields: nil,
		},
		{
			name: "pass state missing next",
			cfg: &Config{
				Start:  "setup",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"setup": {Type: StateTypePass, Data: map[string]any{"key": "val"}},
				},
			},
			wantFields: []string{"states.setup.next"},
		},
		{
			name: "valid choice state",
			cfg: &Config{
				Start:  "check",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"check": {Type: StateTypeChoice, Choices: []ChoiceRule{
						{Variable: "status", Equals: "done", Next: "done"},
					}, Default: "wait"},
					"done": {Type: StateTypeSucceed},
					"wait": {Type: StateTypeSucceed},
				},
			},
			wantFields: nil,
		},
		{
			name: "choice state no choices",
			cfg: &Config{
				Start:  "check",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"check": {Type: StateTypeChoice, Default: "done"},
					"done":  {Type: StateTypeSucceed},
				},
			},
			wantFields: []string{"states.check.choices"},
		},
		{
			name: "choice rule missing variable",
			cfg: &Config{
				Start:  "check",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"check": {Type: StateTypeChoice, Choices: []ChoiceRule{
						{Equals: "x", Next: "done"},
					}},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: []string{"states.check.choices[0].variable"},
		},
		{
			name: "choice rule missing next",
			cfg: &Config{
				Start:  "check",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"check": {Type: StateTypeChoice, Choices: []ChoiceRule{
						{Variable: "x", Equals: "y"},
					}},
				},
			},
			wantFields: []string{"states.check.choices[0].next"},
		},
		{
			name: "choice rule no condition",
			cfg: &Config{
				Start:  "check",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"check": {Type: StateTypeChoice, Choices: []ChoiceRule{
						{Variable: "x", Next: "done"},
					}},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: []string{"states.check.choices[0]"},
		},
		{
			name: "choice rule references non-existent state",
			cfg: &Config{
				Start:  "check",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"check": {Type: StateTypeChoice, Choices: []ChoiceRule{
						{Variable: "x", Equals: "y", Next: "nonexistent"},
					}},
				},
			},
			wantFields: []string{"states.check.choices[0].next"},
		},
		{
			name: "choice default references non-existent state",
			cfg: &Config{
				Start:  "check",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"check": {Type: StateTypeChoice, Choices: []ChoiceRule{
						{Variable: "x", Equals: "y", Next: "done"},
					}, Default: "nonexistent"},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: []string{"states.check.default"},
		},
		{
			name: "retry with zero max_attempts",
			cfg: &Config{
				Start:  "t",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"t":    {Type: StateTypeTask, Action: "ai.code", Next: "done", Retry: []RetryConfig{{MaxAttempts: 0}}},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: []string{"states.t.retry[0].max_attempts"},
		},
		{
			name: "retry with negative backoff_rate",
			cfg: &Config{
				Start:  "t",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"t":    {Type: StateTypeTask, Action: "ai.code", Next: "done", Retry: []RetryConfig{{MaxAttempts: 3, BackoffRate: -1.0}}},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: []string{"states.t.retry[0].backoff_rate"},
		},
		{
			name: "catch missing next",
			cfg: &Config{
				Start:  "t",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"t":    {Type: StateTypeTask, Action: "ai.code", Next: "done", Catch: []CatchConfig{{Errors: []string{"*"}}}},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: []string{"states.t.catch[0].next"},
		},
		{
			name: "catch references non-existent state",
			cfg: &Config{
				Start:  "t",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"t":    {Type: StateTypeTask, Action: "ai.code", Next: "done", Catch: []CatchConfig{{Errors: []string{"*"}, Next: "nonexistent"}}},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: []string{"states.t.catch[0].next"},
		},
		{
			name: "valid retry and catch config",
			cfg: &Config{
				Start:  "t",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"t": {Type: StateTypeTask, Action: "ai.code", Next: "done",
						Retry: []RetryConfig{{MaxAttempts: 3, BackoffRate: 2.0}},
						Catch: []CatchConfig{{Errors: []string{"*"}, Next: "failed"}},
					},
					"done":   {Type: StateTypeSucceed},
					"failed": {Type: StateTypeFail},
				},
			},
			wantFields: nil,
		},
		{
			name: "timeout_next without timeout",
			cfg: &Config{
				Start:  "w",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"w":     {Type: StateTypeWait, Event: "ci.complete", TimeoutNext: "nudge", Next: "done"},
					"nudge": {Type: StateTypeSucceed},
					"done":  {Type: StateTypeSucceed},
				},
			},
			wantFields: []string{"states.w.timeout_next"},
		},
		{
			name: "timeout_next references non-existent state",
			cfg: &Config{
				Start:  "w",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"w":    {Type: StateTypeWait, Event: "ci.complete", Timeout: &Duration{Duration: 3600000000000}, TimeoutNext: "nonexistent", Next: "done"},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: []string{"states.w.timeout_next"},
		},
		{
			name: "valid timeout_next with timeout",
			cfg: &Config{
				Start:  "w",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"w":     {Type: StateTypeWait, Event: "ci.complete", Timeout: &Duration{Duration: 3600000000000}, TimeoutNext: "nudge", Next: "done"},
					"nudge": {Type: StateTypeSucceed},
					"done":  {Type: StateTypeSucceed},
				},
			},
			wantFields: nil,
		},
		{
			name: "valid settings",
			cfg: &Config{
				Start:  "s",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{"s": {Type: StateTypeSucceed}},
				Settings: &SettingsConfig{
					ContainerImage: "img:v1",
					MaxConcurrent:  3,
				},
			},
			wantFields: nil,
		},
		{
			name: "negative max_concurrent in settings",
			cfg: &Config{
				Start:  "s",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{"s": {Type: StateTypeSucceed}},
				Settings: &SettingsConfig{
					MaxConcurrent: -1,
				},
			},
			wantFields: []string{"settings.max_concurrent"},
		},
		{
			name: "nil settings is valid",
			cfg: &Config{
				Start:  "s",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{"s": {Type: StateTypeSucceed}},
			},
			wantFields: nil,
		},
		{
			name: "request_review missing reviewer param",
			cfg: &Config{
				Start:  "req",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"req":  {Type: StateTypeTask, Action: "github.request_review", Next: "done"},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: []string{"states.req.params.reviewer"},
		},
		{
			name: "request_review with empty reviewer param",
			cfg: &Config{
				Start:  "req",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"req":  {Type: StateTypeTask, Action: "github.request_review", Next: "done", Params: map[string]any{"reviewer": ""}},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: []string{"states.req.params.reviewer"},
		},
		{
			name: "request_review with valid reviewer",
			cfg: &Config{
				Start:  "req",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"req":  {Type: StateTypeTask, Action: "github.request_review", Next: "done", Params: map[string]any{"reviewer": "octocat"}},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: nil,
		},
		{
			name: "assign_pr missing assignee param",
			cfg: &Config{
				Start:  "assign",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"assign": {Type: StateTypeTask, Action: "github.assign_pr", Next: "done"},
					"done":   {Type: StateTypeSucceed},
				},
			},
			wantFields: []string{"states.assign.params.assignee"},
		},
		{
			name: "assign_pr with empty assignee param",
			cfg: &Config{
				Start:  "assign",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"assign": {Type: StateTypeTask, Action: "github.assign_pr", Next: "done", Params: map[string]any{"assignee": ""}},
					"done":   {Type: StateTypeSucceed},
				},
			},
			wantFields: []string{"states.assign.params.assignee"},
		},
		{
			name: "assign_pr with valid assignee",
			cfg: &Config{
				Start:  "assign",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"assign": {Type: StateTypeTask, Action: "github.assign_pr", Next: "done", Params: map[string]any{"assignee": "octocat"}},
					"done":   {Type: StateTypeSucceed},
				},
			},
			wantFields: nil,
		},
		{
			name: "git.format missing command param",
			cfg: &Config{
				Start:  "fmt",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"fmt":  {Type: StateTypeTask, Action: "git.format", Next: "done"},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: []string{"states.fmt.params.command"},
		},
		{
			name: "git.format with empty command param",
			cfg: &Config{
				Start:  "fmt",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"fmt":  {Type: StateTypeTask, Action: "git.format", Next: "done", Params: map[string]any{"command": ""}},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: []string{"states.fmt.params.command"},
		},
		{
			name: "git.format with valid command",
			cfg: &Config{
				Start:  "fmt",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"fmt":  {Type: StateTypeTask, Action: "git.format", Next: "done", Params: map[string]any{"command": "go fmt ./..."}},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: nil,
		},
		{
			name: "git.format with command and custom message",
			cfg: &Config{
				Start:  "fmt",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"fmt":  {Type: StateTypeTask, Action: "git.format", Next: "done", Params: map[string]any{"command": "prettier --write .", "message": "chore: format"}},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: nil,
		},
		{
			name: "cycle detection: simple A→B→A",
			cfg: &Config{
				Start:  "a",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"a": {Type: StateTypeTask, Action: "ai.code", Next: "b"},
					"b": {Type: StateTypeTask, Action: "ai.code", Next: "a"},
				},
			},
			wantFields: []string{"states"},
		},
		{
			name: "cycle detection: A→B→C→A",
			cfg: &Config{
				Start:  "a",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"a": {Type: StateTypeTask, Action: "ai.code", Next: "b"},
					"b": {Type: StateTypeTask, Action: "ai.code", Next: "c"},
					"c": {Type: StateTypeTask, Action: "ai.code", Next: "a"},
				},
			},
			wantFields: []string{"states"},
		},
		{
			name: "no cycle: valid linear flow",
			cfg: &Config{
				Start:  "a",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"a":    {Type: StateTypeTask, Action: "ai.code", Next: "b"},
					"b":    {Type: StateTypeTask, Action: "ai.code", Next: "c"},
					"c":    {Type: StateTypeTask, Action: "ai.code", Next: "done"},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: nil,
		},
		{
			name: "no cycle: error edge to fail state",
			cfg: &Config{
				Start:  "a",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"a":      {Type: StateTypeTask, Action: "ai.code", Next: "done", Error: "failed"},
					"done":   {Type: StateTypeSucceed},
					"failed": {Type: StateTypeFail},
				},
			},
			wantFields: nil,
		},
		{
			name: "valid on_failure fix policy",
			cfg: &Config{
				Start:  "ci",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"ci":   {Type: StateTypeWait, Event: "ci.complete", Params: map[string]any{"on_failure": "fix"}, Next: "done"},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: nil,
		},
		{
			name: "choice-gated cycle allowed",
			cfg: &Config{
				Start:  "a",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"a": {Type: StateTypeWait, Event: "ci.complete", Next: "choice"},
					"choice": {Type: StateTypeChoice, Choices: []ChoiceRule{
						{Variable: "pass", Equals: true, Next: "done"},
						{Variable: "fail", Equals: true, Next: "fix"},
					}, Default: "done"},
					"fix":  {Type: StateTypeTask, Action: "ai.fix_ci", Next: "push"},
					"push": {Type: StateTypeTask, Action: "github.push", Next: "a"},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: nil, // Cycle through choice state should be allowed
		},
		{
			name: "cycle without choice state still fails",
			cfg: &Config{
				Start:  "a",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"a": {Type: StateTypeTask, Action: "ai.code", Next: "b"},
					"b": {Type: StateTypeTask, Action: "github.push", Next: "a"},
				},
			},
			wantFields: []string{"states"},
		},
		{
			name: "valid ai.fix_ci action",
			cfg: &Config{
				Start:  "fix",
				Source: SourceConfig{Provider: "github", Filter: FilterConfig{Label: "q"}},
				States: map[string]*State{
					"fix":  {Type: StateTypeTask, Action: "ai.fix_ci", Params: map[string]any{"max_ci_fix_rounds": 3}, Next: "done"},
					"done": {Type: StateTypeSucceed},
				},
			},
			wantFields: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := Validate(tt.cfg)

			if len(tt.wantFields) == 0 {
				if len(errs) > 0 {
					t.Errorf("expected no errors, got %d: %v", len(errs), errs)
				}
				return
			}

			errFields := make(map[string]bool)
			for _, e := range errs {
				errFields[e.Field] = true
			}

			for _, field := range tt.wantFields {
				if !errFields[field] {
					t.Errorf("expected error for field %q, got errors: %v", field, errs)
				}
			}
		})
	}
}

func TestValidateRebaseParams(t *testing.T) {
	tests := []struct {
		name      string
		params    map[string]any
		wantError bool
	}{
		{"nil params", nil, false},
		{"empty params", map[string]any{}, false},
		{"valid max_rebase_rounds", map[string]any{"max_rebase_rounds": 3}, false},
		{"zero max_rebase_rounds", map[string]any{"max_rebase_rounds": 0}, true},
		{"negative max_rebase_rounds", map[string]any{"max_rebase_rounds": -1}, true},
		{"float64 valid", map[string]any{"max_rebase_rounds": float64(2)}, false},
		{"float64 zero", map[string]any{"max_rebase_rounds": float64(0)}, true},
		{"float64 negative", map[string]any{"max_rebase_rounds": float64(-1)}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateRebaseParams("states.rebase", tt.params)
			if tt.wantError && len(errs) == 0 {
				t.Error("expected validation error")
			}
			if !tt.wantError && len(errs) > 0 {
				t.Errorf("unexpected validation errors: %v", errs)
			}
		})
	}
}

func TestValidate_GitRebaseAction(t *testing.T) {
	// A workflow with git.rebase and invalid max_rebase_rounds should fail validation
	cfg := &Config{
		Workflow: "test",
		Start:   "rebase",
		Source:   SourceConfig{Provider: "github", Filter: FilterConfig{Label: "queued"}},
		States: map[string]*State{
			"rebase": {
				Type:   StateTypeTask,
				Action: "git.rebase",
				Params: map[string]any{
					"max_rebase_rounds": 0,
				},
				Next:  "done",
				Error: "failed",
			},
			"done": {
				Type: StateTypeSucceed,
			},
			"failed": {
				Type: StateTypeFail,
			},
		},
	}

	errs := Validate(cfg)
	found := false
	for _, e := range errs {
		if e.Field == "states.rebase.params.max_rebase_rounds" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected validation error for max_rebase_rounds=0, got errors: %v", errs)
	}
}

func TestValidateResolveConflictsParams(t *testing.T) {
	tests := []struct {
		name      string
		params    map[string]any
		wantError bool
	}{
		{"nil params", nil, false},
		{"empty params", map[string]any{}, false},
		{"valid max_conflict_rounds", map[string]any{"max_conflict_rounds": 3}, false},
		{"zero max_conflict_rounds", map[string]any{"max_conflict_rounds": 0}, true},
		{"negative max_conflict_rounds", map[string]any{"max_conflict_rounds": -1}, true},
		{"float64 valid", map[string]any{"max_conflict_rounds": float64(2)}, false},
		{"float64 zero", map[string]any{"max_conflict_rounds": float64(0)}, true},
		{"float64 negative", map[string]any{"max_conflict_rounds": float64(-1)}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateResolveConflictsParams("states.resolve", tt.params)
			if tt.wantError && len(errs) == 0 {
				t.Error("expected validation error")
			}
			if !tt.wantError && len(errs) > 0 {
				t.Errorf("unexpected validation errors: %v", errs)
			}
		})
	}
}

func TestValidate_ResolveConflictsAction(t *testing.T) {
	cfg := &Config{
		Workflow: "test",
		Start:   "resolve",
		Source:   SourceConfig{Provider: "github", Filter: FilterConfig{Label: "queued"}},
		States: map[string]*State{
			"resolve": {
				Type:   StateTypeTask,
				Action: "ai.resolve_conflicts",
				Params: map[string]any{
					"max_conflict_rounds": 0,
				},
				Next:  "done",
				Error: "failed",
			},
			"done": {
				Type: StateTypeSucceed,
			},
			"failed": {
				Type: StateTypeFail,
			},
		},
	}

	errs := Validate(cfg)
	found := false
	for _, e := range errs {
		if e.Field == "states.resolve.params.max_conflict_rounds" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected validation error for max_conflict_rounds=0, got errors: %v", errs)
	}
}

func TestValidateDiffParams(t *testing.T) {
	tests := []struct {
		name      string
		params    map[string]any
		wantError bool
	}{
		{"nil params", nil, false},
		{"empty params", map[string]any{}, false},
		{"valid max_diff_lines", map[string]any{"max_diff_lines": 500}, false},
		{"zero max_diff_lines", map[string]any{"max_diff_lines": 0}, true},
		{"negative max_diff_lines", map[string]any{"max_diff_lines": -1}, true},
		{"float64 valid max_diff_lines", map[string]any{"max_diff_lines": float64(100)}, false},
		{"float64 zero max_diff_lines", map[string]any{"max_diff_lines": float64(0)}, true},
		{"valid max_lock_file_lines", map[string]any{"max_lock_file_lines": 200}, false},
		{"zero max_lock_file_lines", map[string]any{"max_lock_file_lines": 0}, true},
		{"negative max_lock_file_lines", map[string]any{"max_lock_file_lines": -5}, true},
		{"float64 zero max_lock_file_lines", map[string]any{"max_lock_file_lines": float64(0)}, true},
		{"require_tests only", map[string]any{"require_tests": true}, false},
		{"forbidden_patterns only", map[string]any{"forbidden_patterns": []interface{}{".env"}}, false},
		{"all valid params", map[string]any{
			"max_diff_lines":      1000,
			"max_lock_file_lines": 100,
			"require_tests":       true,
			"forbidden_patterns":  []interface{}{".env", "*.pem"},
		}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateDiffParams("states.validate", tt.params)
			if tt.wantError && len(errs) == 0 {
				t.Error("expected validation error")
			}
			if !tt.wantError && len(errs) > 0 {
				t.Errorf("unexpected validation errors: %v", errs)
			}
		})
	}
}

func TestValidate_ValidateDiffAction(t *testing.T) {
	cfg := &Config{
		Workflow: "test",
		Start:   "validate",
		Source:   SourceConfig{Provider: "github", Filter: FilterConfig{Label: "queued"}},
		States: map[string]*State{
			"validate": {
				Type:   StateTypeTask,
				Action: "git.validate_diff",
				Params: map[string]any{
					"max_diff_lines": 0,
				},
				Next:  "done",
				Error: "failed",
			},
			"done": {
				Type: StateTypeSucceed,
			},
			"failed": {
				Type: StateTypeFail,
			},
		},
	}

	errs := Validate(cfg)
	found := false
	for _, e := range errs {
		if e.Field == "states.validate.params.max_diff_lines" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected validation error for max_diff_lines=0, got errors: %v", errs)
	}
}
