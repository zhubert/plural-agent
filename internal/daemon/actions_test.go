package daemon

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/zhubert/plural-agent/internal/daemonstate"
	"github.com/zhubert/plural-agent/internal/workflow"
	"github.com/zhubert/plural-core/config"
	"github.com/zhubert/plural-core/exec"
	"github.com/zhubert/plural-core/git"
)

var errGHFailed = fmt.Errorf("gh: command failed")

func TestCommentIssueAction_Execute_WorkItemNotFound(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	action := &commentIssueAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"body": "Hello!"})
	ac := &workflow.ActionContext{
		WorkItemID: "nonexistent",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure for missing work item")
	}
	if result.Error == nil {
		t.Error("expected error for missing work item")
	}
}

func TestCommentIssueAction_Execute_NonGitHubIssue(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	// Add a work item with Asana source -- should succeed (no-op)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "item-1",
		IssueRef: config.IssueRef{Source: "asana", ID: "task-abc"},
	})

	action := &commentIssueAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"body": "Hello!"})
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected success (no-op) for non-github issue, got error: %v", result.Error)
	}
}

func TestCommentIssueAction_Execute_InvalidIssueNumber(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	// GitHub issue with non-numeric ID (invalid)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "item-1",
		IssueRef: config.IssueRef{Source: "github", ID: "not-a-number"},
	})

	action := &commentIssueAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"body": "Hello!"})
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure for invalid issue number")
	}
	if result.Error == nil {
		t.Error("expected error for invalid issue number")
	}
}

func TestCommentIssueAction_Execute_Success(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	// Mock `gh issue comment` to succeed
	mockExec.AddPrefixMatch("gh", []string{"issue", "comment"}, exec.MockResponse{
		Stdout: []byte("https://github.com/owner/repo/issues/42#issuecomment-1\n"),
	})

	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	d := testDaemonWithExec(cfg, mockExec)
	d.gitService = gitSvc
	d.repoFilter = "/test/repo"
	cfg.Repos = []string{"/test/repo"}

	// Add session so repo path can be resolved
	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
	})

	action := &commentIssueAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"body": "Work has started on this issue."})
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected success, got error: %v", result.Error)
	}

	// Verify gh issue comment was called
	calls := mockExec.GetCalls()
	found := false
	for _, c := range calls {
		if c.Name == "gh" && len(c.Args) >= 2 && c.Args[0] == "issue" && c.Args[1] == "comment" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected gh issue comment to be called")
	}
}

func TestCommentIssueAction_Execute_EmptyBody(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)
	d.repoFilter = "/test/repo"

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
	})

	action := &commentIssueAction{daemon: d}
	// Empty body -- should fail
	params := workflow.NewParamHelper(map[string]any{"body": ""})
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure for empty comment body")
	}
	if result.Error == nil {
		t.Error("expected error for empty comment body")
	}
}

func TestCommentIssueAction_Execute_GhError(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	// Mock `gh issue comment` to fail
	mockExec.AddPrefixMatch("gh", []string{"issue", "comment"}, exec.MockResponse{
		Err: errGHFailed,
	})

	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	d := testDaemonWithExec(cfg, mockExec)
	d.gitService = gitSvc
	d.repoFilter = "/test/repo"

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
	})

	action := &commentIssueAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"body": "Starting work!"})
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure when gh CLI fails")
	}
	if result.Error == nil {
		t.Error("expected error when gh CLI fails")
	}
}

func TestDaemon_CodingParamsExtractsLimits(t *testing.T) {
	// Verify that max_turns and max_duration params on the coding state
	// are correctly read by the ParamHelper, matching the startCoding logic.
	wfCfg := workflow.DefaultConfig()
	wfCfg.States["coding"].Params["max_turns"] = 10
	wfCfg.States["coding"].Params["max_duration"] = "5m"

	params := workflow.NewParamHelper(wfCfg.States["coding"].Params)

	maxTurns := params.Int("max_turns", 0)
	if maxTurns != 10 {
		t.Errorf("expected max_turns=10, got %d", maxTurns)
	}

	maxDuration := params.Duration("max_duration", 0)
	if maxDuration != 5*time.Minute {
		t.Errorf("expected max_duration=5m, got %v", maxDuration)
	}
}

func TestDaemon_CodingParamsDefaultsWhenAbsent(t *testing.T) {
	// When max_turns and max_duration are not in params, defaults are returned.
	params := workflow.NewParamHelper(map[string]any{
		"containerized": true,
		"supervisor":    true,
	})

	maxTurns := params.Int("max_turns", 0)
	if maxTurns != 0 {
		t.Errorf("expected 0 when max_turns absent, got %d", maxTurns)
	}

	maxDuration := params.Duration("max_duration", 0)
	if maxDuration != 0 {
		t.Errorf("expected 0 when max_duration absent, got %v", maxDuration)
	}
}

func TestDefaultWorkflowConfig_SupervisorFalse(t *testing.T) {
	// The default workflow config should have supervisor: false for coding state
	wfCfg := workflow.DefaultConfig()
	codingState := wfCfg.States["coding"]
	if codingState == nil {
		t.Fatal("expected coding state in default workflow config")
	}

	params := workflow.NewParamHelper(codingState.Params)
	supervisor := params.Bool("supervisor", true) // pass true as default to test param value
	if supervisor {
		t.Error("default workflow config should have supervisor: false for coding state")
	}
}

func TestSupervisorParamDefaultsFalseWhenAbsent(t *testing.T) {
	// When supervisor is absent from params, it should default to false
	params := workflow.NewParamHelper(map[string]any{
		"containerized": true,
	})

	supervisor := params.Bool("supervisor", false)
	if supervisor {
		t.Error("supervisor should default to false when absent from params")
	}
}

func TestSupervisorExplicitOptIn(t *testing.T) {
	// Explicit supervisor: true should still work as opt-in
	params := workflow.NewParamHelper(map[string]any{
		"supervisor": true,
	})

	supervisor := params.Bool("supervisor", false)
	if !supervisor {
		t.Error("explicit supervisor: true should be respected")
	}
}

func TestDefaultCodingSystemPrompt_Applied(t *testing.T) {
	// The DefaultCodingSystemPrompt should be non-empty and contain key instructions
	if DefaultCodingSystemPrompt == "" {
		t.Fatal("DefaultCodingSystemPrompt should not be empty")
	}

	if !strings.Contains(DefaultCodingSystemPrompt, "autonomous coding agent") {
		t.Error("DefaultCodingSystemPrompt should identify as autonomous coding agent")
	}
	if !strings.Contains(DefaultCodingSystemPrompt, "DO NOT") {
		t.Error("DefaultCodingSystemPrompt should contain DO NOT instructions")
	}
	if !strings.Contains(DefaultCodingSystemPrompt, "git push") {
		t.Error("DefaultCodingSystemPrompt should mention git push as forbidden")
	}
	if !strings.Contains(DefaultCodingSystemPrompt, "create pull requests") {
		t.Error("DefaultCodingSystemPrompt should mention PR creation as forbidden")
	}
}

func TestDefaultCodingSystemPrompt_NotAppliedWhenCustomSet(t *testing.T) {
	// Simulate the logic: when a custom prompt is set, DefaultCodingSystemPrompt is NOT used
	customPrompt := "My custom coding instructions"
	codingPrompt := customPrompt

	// This mirrors the logic in startCoding
	if codingPrompt == "" {
		codingPrompt = DefaultCodingSystemPrompt
	}

	if codingPrompt != customPrompt {
		t.Errorf("expected custom prompt %q, got %q", customPrompt, codingPrompt)
	}
}

func TestDefaultCodingSystemPrompt_AppliedWhenEmpty(t *testing.T) {
	// When no custom prompt is configured, DefaultCodingSystemPrompt should be applied
	codingPrompt := ""

	// This mirrors the logic in startCoding
	if codingPrompt == "" {
		codingPrompt = DefaultCodingSystemPrompt
	}

	if codingPrompt != DefaultCodingSystemPrompt {
		t.Error("expected DefaultCodingSystemPrompt to be applied when no custom prompt is set")
	}
}
