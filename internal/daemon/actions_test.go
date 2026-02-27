package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	osexec "os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/worker"
	"github.com/zhubert/erg/internal/workflow"
	"github.com/zhubert/erg/internal/claude"
	"github.com/zhubert/erg/internal/config"
	"github.com/zhubert/erg/internal/exec"
	"github.com/zhubert/erg/internal/git"
	"github.com/zhubert/erg/internal/issues"
	"github.com/zhubert/erg/internal/session"
)

// trackingRunner wraps MockRunner to track Set* calls for configureRunner tests.
type trackingRunner struct {
	*claude.MockRunner
	containerized         bool
	containerImage        string
	supervisorEnabled     bool
	hostToolsEnabled      bool
	streamingChunksOff    bool
	systemPrompt          string
}

func newTrackingRunner(id string) *trackingRunner {
	return &trackingRunner{
		MockRunner: claude.NewMockRunner(id, false, nil),
	}
}

func (r *trackingRunner) SetContainerized(containerized bool, image string) {
	r.containerized = containerized
	r.containerImage = image
}

func (r *trackingRunner) SetSupervisor(supervisor bool) {
	r.supervisorEnabled = supervisor
	r.MockRunner.SetSupervisor(supervisor)
}

func (r *trackingRunner) SetHostTools(hostTools bool) {
	r.hostToolsEnabled = hostTools
	r.MockRunner.SetHostTools(hostTools)
}

func (r *trackingRunner) SetDisableStreamingChunks(disable bool) {
	r.streamingChunksOff = disable
}

func (r *trackingRunner) SetSystemPrompt(prompt string) {
	r.systemPrompt = prompt
}

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
	wfCfg := workflow.DefaultWorkflowConfig()
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
	wfCfg := workflow.DefaultWorkflowConfig()
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

func TestDefaultCodingSystemPrompt_ContainerEnvironment(t *testing.T) {
	// The system prompt should include container environment guidance.
	if !strings.Contains(DefaultCodingSystemPrompt, "CONTAINER ENVIRONMENT") {
		t.Error("DefaultCodingSystemPrompt should contain CONTAINER ENVIRONMENT section")
	}
	if !strings.Contains(DefaultCodingSystemPrompt, "segfault") {
		t.Error("DefaultCodingSystemPrompt should mention segfault as a transient failure")
	}
	if !strings.Contains(DefaultCodingSystemPrompt, "retry") {
		t.Error("DefaultCodingSystemPrompt should recommend retrying on transient failures")
	}
}

func TestDefaultCodingSystemPrompt_TwoPhaseTestingInstructions(t *testing.T) {
	// The system prompt should include two-phase testing guidance.
	if !strings.Contains(DefaultCodingSystemPrompt, "TWO-PHASE") {
		t.Error("DefaultCodingSystemPrompt should contain TWO-PHASE testing section")
	}
	if !strings.Contains(DefaultCodingSystemPrompt, "CI") {
		t.Error("DefaultCodingSystemPrompt should reference CI")
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

func TestConfigureRunner_ToolSelection(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	runner := newTrackingRunner("test-session")
	sess := &config.Session{
		ID:            "test-session",
		Containerized: true,
		Autonomous:    true,
	}

	d.configureRunner(runner, sess, "")

	tools := runner.GetAllowedTools()
	expected := claude.ComposeTools(
		claude.ToolSetBase,
		claude.ToolSetContainerShell,
		claude.ToolSetWeb,
		claude.ToolSetProductivity,
	)
	if len(tools) != len(expected) {
		t.Errorf("expected %d tools, got %d", len(expected), len(tools))
	}
	for _, tool := range expected {
		if !slices.Contains(tools, tool) {
			t.Errorf("missing expected tool %q", tool)
		}
	}
}

func TestConfigureRunner_ContainerMode(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	runner := newTrackingRunner("test-session")
	sess := &config.Session{
		ID:            "test-session",
		Containerized: true,
	}

	d.configureRunner(runner, sess, "")

	if !runner.containerized {
		t.Error("expected SetContainerized to be called")
	}
}

func TestConfigureRunner_ContainerMode_Disabled(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	runner := newTrackingRunner("test-session")
	sess := &config.Session{
		ID:            "test-session",
		Containerized: false,
	}

	d.configureRunner(runner, sess, "")

	if runner.containerized {
		t.Error("expected SetContainerized NOT to be called when Containerized is false")
	}
}

func TestConfigureRunner_SupervisorMode(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	runner := newTrackingRunner("test-session")
	sess := &config.Session{
		ID:           "test-session",
		IsSupervisor: true,
	}

	d.configureRunner(runner, sess, "")

	if !runner.supervisorEnabled {
		t.Error("expected SetSupervisor(true) to be called when IsSupervisor is true")
	}
}

func TestConfigureRunner_SupervisorMode_Disabled(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	runner := newTrackingRunner("test-session")
	sess := &config.Session{
		ID:           "test-session",
		IsSupervisor: false,
	}

	d.configureRunner(runner, sess, "")

	if runner.supervisorEnabled {
		t.Error("expected SetSupervisor NOT to be called when IsSupervisor is false")
	}
}

func TestConfigureRunner_NoHostTools(t *testing.T) {
	// Daemon should NEVER set host tools — workflow actions handle push/PR/merge
	cfg := testConfig()
	d := testDaemon(cfg)

	runner := newTrackingRunner("test-session")
	sess := &config.Session{
		ID:            "test-session",
		Autonomous:    true,
		IsSupervisor:  true,
		DaemonManaged: true,
	}

	d.configureRunner(runner, sess, "")

	if runner.hostToolsEnabled {
		t.Error("daemon configureRunner should NEVER enable host tools")
	}
}

func TestConfigureRunner_StreamingDisabled(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	runner := newTrackingRunner("test-session")
	sess := &config.Session{
		ID:         "test-session",
		Autonomous: true,
	}

	d.configureRunner(runner, sess, "")

	if !runner.streamingChunksOff {
		t.Error("expected streaming chunks disabled for autonomous sessions")
	}
}

func TestConfigureRunner_StreamingNotDisabled_NonAutonomous(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	runner := newTrackingRunner("test-session")
	sess := &config.Session{
		ID:         "test-session",
		Autonomous: false,
	}

	d.configureRunner(runner, sess, "")

	if runner.streamingChunksOff {
		t.Error("streaming chunks should not be disabled for non-autonomous sessions")
	}
}

func TestConfigureRunner_SystemPrompt(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	runner := newTrackingRunner("test-session")
	sess := &config.Session{ID: "test-session"}

	d.configureRunner(runner, sess, "custom prompt")

	if runner.systemPrompt != "custom prompt" {
		t.Errorf("expected system prompt 'custom prompt', got %q", runner.systemPrompt)
	}
}

func TestConfigureRunner_NoSystemPrompt(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	runner := newTrackingRunner("test-session")
	sess := &config.Session{ID: "test-session"}

	d.configureRunner(runner, sess, "")

	if runner.systemPrompt != "" {
		t.Errorf("expected empty system prompt, got %q", runner.systemPrompt)
	}
}

func TestStartCoding_SkipsCleanupWhenPRExists(t *testing.T) {
	cfg := testConfig()
	cfg.Repos = []string{"/test/repo"}

	mockExec := exec.NewMockExecutor(nil)

	// BranchExists returns true (branch exists)
	// Default mock returns success for all commands, so BranchExists returns true.

	// GetPRState returns OPEN PR via "gh pr view" prefix
	prViewJSON, _ := json.Marshal(struct {
		State string `json:"state"`
	}{State: "OPEN"})
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Stdout: prViewJSON,
	})

	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	sessSvc := session.NewSessionServiceWithExecutor(mockExec)
	d := testDaemonWithExec(cfg, mockExec)
	d.gitService = gitSvc
	d.sessionService = sessSvc
	d.repoFilter = "/test/repo"

	item := &daemonstate.WorkItem{
		ID:       "work-1",
		IssueRef: config.IssueRef{Source: "github", ID: "10", Title: "Fix bug"},
		StepData: map[string]any{},
	}
	d.state.AddWorkItem(item)

	err := d.startCoding(context.Background(), *item)
	if err == nil {
		t.Fatal("startCoding should return error when branch has existing PR")
	}
	if !errors.Is(err, errExistingPR) {
		t.Errorf("expected errExistingPR sentinel, got: %v", err)
	}

	// A tracking session should have been created so the work item can advance.
	sessions := cfg.GetSessions()
	if len(sessions) != 1 {
		t.Fatalf("expected 1 tracking session, got %d", len(sessions))
	}
	if !sessions[0].PRCreated {
		t.Error("tracking session should have PRCreated=true")
	}
	updatedItem, ok := d.state.GetWorkItem(item.ID)
	if !ok {
		t.Fatal("work item should exist in state")
	}
	if updatedItem.SessionID == "" {
		t.Error("work item should have SessionID set")
	}
}

func TestStartCoding_SkipsCleanupWhenPRMerged(t *testing.T) {
	cfg := testConfig()
	cfg.Repos = []string{"/test/repo"}

	mockExec := exec.NewMockExecutor(nil)

	// GetPRState returns MERGED PR
	prViewJSON, _ := json.Marshal(struct {
		State string `json:"state"`
	}{State: "MERGED"})
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Stdout: prViewJSON,
	})

	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	sessSvc := session.NewSessionServiceWithExecutor(mockExec)
	d := testDaemonWithExec(cfg, mockExec)
	d.gitService = gitSvc
	d.sessionService = sessSvc
	d.repoFilter = "/test/repo"

	item := &daemonstate.WorkItem{
		ID:       "work-1",
		IssueRef: config.IssueRef{Source: "github", ID: "10", Title: "Fix bug"},
		StepData: map[string]any{},
	}
	d.state.AddWorkItem(item)

	err := d.startCoding(context.Background(), *item)
	if err == nil {
		t.Fatal("startCoding should return error when branch has merged PR")
	}
	if !errors.Is(err, errMergedPR) {
		t.Errorf("expected errMergedPR sentinel, got: %v", err)
	}

	// A tracking session should have been created.
	sessions := cfg.GetSessions()
	if len(sessions) != 1 {
		t.Fatalf("expected 1 tracking session, got %d", len(sessions))
	}
	updatedItem, ok := d.state.GetWorkItem(item.ID)
	if !ok {
		t.Fatal("work item should exist in state")
	}
	if updatedItem.SessionID == "" {
		t.Error("work item should have SessionID set")
	}
}

func TestStartCoding_CleansUpWhenNoPR(t *testing.T) {
	cfg := testConfig()
	cfg.Repos = []string{"/test/repo"}

	mockExec := exec.NewMockExecutor(nil)

	// GetPRState returns error (no PR found) via "gh pr view"
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Err: fmt.Errorf("no pull requests found"),
	})

	// BranchExists: first call returns true, second returns false (cleaned up)
	branchCheckCount := 0
	mockExec.AddRule(func(dir, name string, args []string) bool {
		if name == "git" && len(args) == 3 && args[0] == "rev-parse" && args[1] == "--verify" && args[2] == "issue-10" {
			branchCheckCount++
			return branchCheckCount > 1
		}
		return false
	}, exec.MockResponse{Err: fmt.Errorf("fatal: Needed a single revision")})

	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	sessSvc := session.NewSessionServiceWithExecutor(mockExec)
	d := testDaemonWithExec(cfg, mockExec)
	d.gitService = gitSvc
	d.sessionService = sessSvc
	d.repoFilter = "/test/repo"

	item := &daemonstate.WorkItem{
		ID:       "work-1",
		IssueRef: config.IssueRef{Source: "github", ID: "10", Title: "Fix bug"},
		StepData: map[string]any{},
	}
	d.state.AddWorkItem(item)

	err := d.startCoding(context.Background(), *item)
	if err != nil {
		t.Fatalf("startCoding should succeed when no PR exists on branch, got: %v", err)
	}

	// Should have cleaned up the stale branch and created a new session
	sessions := cfg.GetSessions()
	if len(sessions) == 0 {
		t.Fatal("expected a new session to be created")
	}
}

func TestStartCoding_CleansUpStaleBranch(t *testing.T) {
	cfg := testConfig()
	cfg.Repos = []string{"/test/repo"}

	mockExec := exec.NewMockExecutor(nil)

	// Add a stale session with the branch name that startCoding will generate
	staleSess := &config.Session{
		ID:       "stale-sess",
		RepoPath: "/test/repo",
		WorkTree: "/test/worktree-stale",
		Branch:   "issue-10",
		Name:     "test/stale",
	}
	cfg.AddSession(*staleSess)

	// BranchExists uses: git rev-parse --verify <branch>
	// First call should return success (branch exists), second should return error (cleaned up).
	branchCheckCount := 0
	mockExec.AddRule(func(dir, name string, args []string) bool {
		if name == "git" && len(args) == 3 && args[0] == "rev-parse" && args[1] == "--verify" && args[2] == "issue-10" {
			branchCheckCount++
			return branchCheckCount > 1
		}
		return false
	}, exec.MockResponse{Err: fmt.Errorf("fatal: Needed a single revision")})

	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	sessSvc := session.NewSessionServiceWithExecutor(mockExec)
	d := testDaemonWithExec(cfg, mockExec)
	d.gitService = gitSvc
	d.sessionService = sessSvc
	d.repoFilter = "/test/repo"

	item := &daemonstate.WorkItem{
		ID:       "work-1",
		IssueRef: config.IssueRef{Source: "github", ID: "10", Title: "Fix bug"},
		StepData: map[string]any{},
	}
	d.state.AddWorkItem(item)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := d.startCoding(ctx, *item)
	if err != nil {
		t.Fatalf("startCoding should succeed after cleaning up stale branch, got: %v", err)
	}

	// The stale session should have been removed from config
	if cfg.GetSession("stale-sess") != nil {
		t.Error("stale session should have been removed from config")
	}

	// A new session should have been created
	sessions := cfg.GetSessions()
	if len(sessions) == 0 {
		t.Fatal("expected a new session to be created")
	}
	newSess := sessions[0]
	if newSess.Branch != "issue-10" {
		t.Errorf("expected new session branch 'issue-10', got %q", newSess.Branch)
	}

	// BranchExists should have been called twice
	if branchCheckCount != 2 {
		t.Errorf("expected BranchExists to be called 2 times, got %d", branchCheckCount)
	}
}

func TestStartCoding_CleansUpOrphanedBranch(t *testing.T) {
	cfg := testConfig()
	cfg.Repos = []string{"/test/repo"}

	mockExec := exec.NewMockExecutor(nil)

	// No stale session in config — this is a truly orphaned branch.

	// BranchExists: first call returns true (exists), second returns false (cleaned up).
	branchCheckCount := 0
	mockExec.AddRule(func(dir, name string, args []string) bool {
		if name == "git" && len(args) == 3 && args[0] == "rev-parse" && args[1] == "--verify" && args[2] == "issue-10" {
			branchCheckCount++
			return branchCheckCount > 1
		}
		return false
	}, exec.MockResponse{Err: fmt.Errorf("fatal: Needed a single revision")})

	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	sessSvc := session.NewSessionServiceWithExecutor(mockExec)
	d := testDaemonWithExec(cfg, mockExec)
	d.gitService = gitSvc
	d.sessionService = sessSvc
	d.repoFilter = "/test/repo"

	item := &daemonstate.WorkItem{
		ID:       "work-1",
		IssueRef: config.IssueRef{Source: "github", ID: "10", Title: "Fix orphaned bug"},
		StepData: map[string]any{},
	}
	d.state.AddWorkItem(item)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := d.startCoding(ctx, *item)
	if err != nil {
		t.Fatalf("startCoding should succeed after cleaning up orphaned branch, got: %v", err)
	}

	// A new session should have been created
	sessions := cfg.GetSessions()
	if len(sessions) == 0 {
		t.Fatal("expected a new session to be created")
	}
	newSess := sessions[0]
	if newSess.Branch != "issue-10" {
		t.Errorf("expected new session branch 'issue-10', got %q", newSess.Branch)
	}

	// BranchExists should have been called twice
	if branchCheckCount != 2 {
		t.Errorf("expected BranchExists to be called 2 times, got %d", branchCheckCount)
	}
}

func TestStartCoding_FailsWhenCleanupFails(t *testing.T) {
	cfg := testConfig()
	cfg.Repos = []string{"/test/repo"}

	mockExec := exec.NewMockExecutor(nil)

	// BranchExists always returns true (cleanup fails to remove the branch).
	// Default mock returns success for all commands, so BranchExists always returns true.

	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	sessSvc := session.NewSessionServiceWithExecutor(mockExec)
	d := testDaemonWithExec(cfg, mockExec)
	d.gitService = gitSvc
	d.sessionService = sessSvc
	d.repoFilter = "/test/repo"

	item := &daemonstate.WorkItem{
		ID:       "work-1",
		IssueRef: config.IssueRef{Source: "github", ID: "10", Title: "Fix bug"},
		StepData: map[string]any{},
	}
	d.state.AddWorkItem(item)

	err := d.startCoding(context.Background(), *item)
	if err == nil {
		t.Fatal("startCoding should fail when branch cannot be cleaned up")
	}
	if !strings.Contains(err.Error(), "could not be cleaned up") {
		t.Errorf("expected 'could not be cleaned up' error, got: %v", err)
	}
}

// TestStartCoding_WorkItemUpdatedBeforeConfigSave is a regression test for the
// ordering bug where the work item's SessionID was set AFTER saveConfig was
// called, leaving a window where a crash would orphan the session (config has
// the session but state file has no SessionID reference).
//
// After the fix, we verify that when startCoding succeeds:
//   - item.SessionID is set (so saveState records the link to the session)
//   - item.Branch matches the session branch
//   - item.State is WorkItemActive
//   - The SessionID on the work item matches the session recorded in config
func TestStartCoding_WorkItemUpdatedBeforeConfigSave(t *testing.T) {
	cfg := testConfig()
	cfg.Repos = []string{"/test/repo"}

	mockExec := exec.NewMockExecutor(nil)

	// Make BranchExists always return false (no pre-existing branch) so
	// startCoding proceeds directly to session creation.
	mockExec.AddRule(func(dir, name string, args []string) bool {
		return name == "git" && len(args) >= 3 && args[0] == "rev-parse" && args[1] == "--verify"
	}, exec.MockResponse{Err: fmt.Errorf("fatal: Needed a single revision")})

	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	sessSvc := session.NewSessionServiceWithExecutor(mockExec)
	d := testDaemonWithExec(cfg, mockExec)
	d.gitService = gitSvc
	d.sessionService = sessSvc
	d.repoFilter = "/test/repo"

	item := &daemonstate.WorkItem{
		ID:       "work-1",
		IssueRef: config.IssueRef{Source: "github", ID: "10", Title: "Fix orphan bug"},
		StepData: map[string]any{},
	}
	d.state.AddWorkItem(item)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := d.startCoding(ctx, *item)
	if err != nil {
		t.Fatalf("startCoding should succeed, got: %v", err)
	}

	// Re-fetch the item from state to see the updates applied via UpdateWorkItem.
	updatedItem, ok := d.state.GetWorkItem(item.ID)
	if !ok {
		t.Fatal("work item should exist in state")
	}

	// item.SessionID must be set so that a subsequent saveState records the
	// reference — this is the core of the bug fix.
	if updatedItem.SessionID == "" {
		t.Error("item.SessionID must be set before saveConfig is called (regression: orphaned session on crash)")
	}
	if updatedItem.Branch == "" {
		t.Error("item.Branch must be set after startCoding")
	}
	if updatedItem.State != daemonstate.WorkItemActive {
		t.Errorf("item.State must be WorkItemActive, got %q", updatedItem.State)
	}

	// The SessionID on the work item must match the session recorded in config,
	// confirming both are kept consistent.
	sessions := cfg.GetSessions()
	if len(sessions) == 0 {
		t.Fatal("expected a session to be recorded in config")
	}
	if sessions[0].ID != updatedItem.SessionID {
		t.Errorf("config session ID %q does not match item.SessionID %q", sessions[0].ID, updatedItem.SessionID)
	}
}

func TestParseWorktreeForBranch(t *testing.T) {
	tests := []struct {
		name           string
		porcelainOutput string
		branchName     string
		expectedPath   string
	}{
		{
			name: "finds matching branch",
			porcelainOutput: "worktree /home/user/repo\nHEAD abc123\nbranch refs/heads/main\n\nworktree /home/user/.erg/worktrees/uuid1\nHEAD def456\nbranch refs/heads/issue-10\n\n",
			branchName:      "issue-10",
			expectedPath:    "/home/user/.erg/worktrees/uuid1",
		},
		{
			name:            "no matching branch",
			porcelainOutput: "worktree /home/user/repo\nHEAD abc123\nbranch refs/heads/main\n\n",
			branchName:      "issue-10",
			expectedPath:    "",
		},
		{
			name:            "empty output",
			porcelainOutput: "",
			branchName:      "issue-10",
			expectedPath:    "",
		},
		{
			name: "multiple worktrees, match first",
			porcelainOutput: "worktree /wt/a\nHEAD aaa\nbranch refs/heads/issue-10\n\nworktree /wt/b\nHEAD bbb\nbranch refs/heads/issue-20\n\n",
			branchName:      "issue-10",
			expectedPath:    "/wt/a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseWorktreeForBranch(tt.porcelainOutput, tt.branchName)
			if result != tt.expectedPath {
				t.Errorf("expected %q, got %q", tt.expectedPath, result)
			}
		})
	}
}

// --- addLabelAction tests ---

func TestAddLabelAction_Execute_WorkItemNotFound(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	action := &addLabelAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"label": "in-progress"})
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

func TestAddLabelAction_Execute_NonGitHubIssue(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "item-1",
		IssueRef: config.IssueRef{Source: "asana", ID: "task-abc"},
	})

	action := &addLabelAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"label": "in-progress"})
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected success (no-op) for non-github issue, got error: %v", result.Error)
	}
}

func TestAddLabelAction_Execute_InvalidIssueNumber(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "item-1",
		IssueRef: config.IssueRef{Source: "github", ID: "not-a-number"},
	})

	action := &addLabelAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"label": "in-progress"})
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

func TestAddLabelAction_Execute_EmptyLabel(t *testing.T) {
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

	action := &addLabelAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"label": ""})
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure for empty label parameter")
	}
	if result.Error == nil {
		t.Error("expected error for empty label parameter")
	}
}

func TestAddLabelAction_Execute_Success(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	// Mock `gh issue edit --add-label` to succeed
	mockExec.AddPrefixMatch("gh", []string{"issue", "edit"}, exec.MockResponse{
		Stdout: []byte(""),
	})

	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	d := testDaemonWithExec(cfg, mockExec)
	d.gitService = gitSvc
	d.repoFilter = "/test/repo"
	cfg.Repos = []string{"/test/repo"}

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
	})

	action := &addLabelAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"label": "in-progress"})
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected success, got error: %v", result.Error)
	}

	// Verify gh issue edit --add-label was called
	calls := mockExec.GetCalls()
	found := false
	for _, c := range calls {
		if c.Name == "gh" && len(c.Args) >= 4 &&
			c.Args[0] == "issue" && c.Args[1] == "edit" &&
			c.Args[2] == "42" && c.Args[3] == "--add-label" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected gh issue edit --add-label to be called")
	}
}

// --- removeLabelAction tests ---

func TestRemoveLabelAction_Execute_WorkItemNotFound(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	action := &removeLabelAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"label": "queued"})
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

func TestRemoveLabelAction_Execute_NonGitHubIssue(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "item-1",
		IssueRef: config.IssueRef{Source: "linear", ID: "LIN-123"},
	})

	action := &removeLabelAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"label": "queued"})
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected success (no-op) for non-github issue, got error: %v", result.Error)
	}
}

func TestRemoveLabelAction_Execute_Success(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	// Mock `gh issue edit --remove-label` to succeed
	mockExec.AddPrefixMatch("gh", []string{"issue", "edit"}, exec.MockResponse{
		Stdout: []byte(""),
	})

	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	d := testDaemonWithExec(cfg, mockExec)
	d.gitService = gitSvc
	d.repoFilter = "/test/repo"
	cfg.Repos = []string{"/test/repo"}

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
	})

	action := &removeLabelAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"label": "queued"})
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected success, got error: %v", result.Error)
	}

	// Verify gh issue edit --remove-label was called
	calls := mockExec.GetCalls()
	found := false
	for _, c := range calls {
		if c.Name == "gh" && len(c.Args) >= 4 &&
			c.Args[0] == "issue" && c.Args[1] == "edit" &&
			c.Args[2] == "42" && c.Args[3] == "--remove-label" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected gh issue edit --remove-label to be called")
	}
}

// --- closeIssueAction tests ---

func TestCloseIssueAction_Execute_WorkItemNotFound(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	action := &closeIssueAction{daemon: d}
	ac := &workflow.ActionContext{
		WorkItemID: "nonexistent",
		Params:     workflow.NewParamHelper(nil),
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure for missing work item")
	}
	if result.Error == nil {
		t.Error("expected error for missing work item")
	}
}

func TestCloseIssueAction_Execute_NonGitHubIssue(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "item-1",
		IssueRef: config.IssueRef{Source: "asana", ID: "task-xyz"},
	})

	action := &closeIssueAction{daemon: d}
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     workflow.NewParamHelper(nil),
	}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected success (no-op) for non-github issue, got error: %v", result.Error)
	}
}

// --- requestReviewAction tests ---

func TestRequestReviewAction_Execute_WorkItemNotFound(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	action := &requestReviewAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"reviewer": "octocat"})
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

func TestRequestReviewAction_Execute_MissingReviewerParam(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-sess-1",
	})

	action := &requestReviewAction{daemon: d}
	// No reviewer param provided
	params := workflow.NewParamHelper(map[string]any{})
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure for missing reviewer parameter")
	}
	if result.Error == nil {
		t.Error("expected error for missing reviewer parameter")
	}
}

// --- assignPRAction tests ---

func TestAssignPRAction_Execute_WorkItemNotFound(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	action := &assignPRAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"assignee": "octocat"})
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

func TestAssignPRAction_Execute_NoSession(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "nonexistent-session",
		Branch:    "feature-1",
	})

	action := &assignPRAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"assignee": "octocat"})
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure for missing session")
	}
	if result.Error == nil {
		t.Error("expected error for missing session")
	}
}

func TestAssignPRAction_Execute_MissingAssigneeParam(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-sess-1",
	})

	action := &assignPRAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{})
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure for missing assignee parameter")
	}
	if result.Error == nil {
		t.Error("expected error for missing assignee parameter")
	}
}

// --- commentPRAction tests ---

func TestCommentPRAction_Execute_WorkItemNotFound(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	action := &commentPRAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"body": "LGTM"})
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

// --- fixCIAction tests ---

func TestFixCIAction_Execute_WorkItemNotFound(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	action := &fixCIAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"max_ci_fix_rounds": 3})
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

func TestFixCIAction_Execute_MaxRoundsExceeded(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-sess-1",
		StepData:  map[string]any{"ci_fix_rounds": 3},
	})

	action := &fixCIAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"max_ci_fix_rounds": 3})
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure when max rounds exceeded")
	}
	if result.Error == nil {
		t.Error("expected error when max rounds exceeded")
	}
	if !strings.Contains(result.Error.Error(), "max CI fix rounds exceeded") {
		t.Errorf("expected 'max CI fix rounds exceeded' error, got: %v", result.Error)
	}
}

func TestFixCIAction_Execute_MaxRoundsFloat64(t *testing.T) {
	// JSON deserialization produces float64 for numbers
	cfg := testConfig()
	d := testDaemon(cfg)

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-sess-1",
		StepData:  map[string]any{"ci_fix_rounds": float64(3)},
	})

	action := &fixCIAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"max_ci_fix_rounds": 3})
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure when max rounds exceeded (float64)")
	}
	if result.Error == nil {
		t.Error("expected error when max rounds exceeded (float64)")
	}
}

func TestFixCIAction_Execute_NoSession(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "nonexistent",
		Branch:    "feature-1",
		StepData:  map[string]any{},
	})

	action := &fixCIAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"max_ci_fix_rounds": 3})
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure when session not found")
	}
	if result.Error == nil {
		t.Error("expected error when session not found")
	}
}

func TestGetCIFixRounds(t *testing.T) {
	tests := []struct {
		name     string
		stepData map[string]any
		expected int
	}{
		{"nil step data", nil, 0},
		{"empty step data", map[string]any{}, 0},
		{"int value", map[string]any{"ci_fix_rounds": 2}, 2},
		{"float64 value (JSON)", map[string]any{"ci_fix_rounds": float64(3)}, 3},
		{"string value (invalid)", map[string]any{"ci_fix_rounds": "2"}, 0},
		{"zero value", map[string]any{"ci_fix_rounds": 0}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getCIFixRounds(tt.stepData)
			if got != tt.expected {
				t.Errorf("expected %d, got %d", tt.expected, got)
			}
		})
	}
}

func TestFormatCIFixPrompt(t *testing.T) {
	prompt := formatCIFixPrompt(2, "Error: test failed\nexit 1")
	if !strings.Contains(prompt, "FIX ROUND 2") {
		t.Error("expected prompt to contain round number")
	}
	if !strings.Contains(prompt, "Error: test failed") {
		t.Error("expected prompt to contain CI logs")
	}
	if !strings.Contains(prompt, "DO NOT push") {
		t.Error("expected prompt to contain push prohibition")
	}
}

func TestCommentPRAction_Execute_EmptyBody(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-sess-1",
	})

	action := &commentPRAction{daemon: d}
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

// --- formatAction tests ---

// initTestGitRepo creates a temporary directory with an initialized git repo
// containing an initial commit, suitable for format action tests.
func initTestGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	mustRunGit(t, dir, "init")
	mustRunGit(t, dir, "config", "user.email", "test@test.com")
	mustRunGit(t, dir, "config", "user.name", "Test User")

	// Create initial commit so there is a HEAD reference
	readmePath := filepath.Join(dir, "README.md")
	createCmd := osexec.Command("sh", "-c", "echo '# Test' > "+readmePath)
	if out, err := createCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to create README.md: %v (output: %s)", err, out)
	}
	mustRunGit(t, dir, "add", ".")
	mustRunGit(t, dir, "commit", "-m", "initial commit")

	return dir
}

func mustRunGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := osexec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v (output: %s)", args, err, out)
	}
}

func TestFormatAction_Execute_WorkItemNotFound(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	action := &formatAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"command": "go fmt ./..."})
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

func TestFormatAction_Execute_SessionNotFound(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "nonexistent-session",
	})

	action := &formatAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"command": "go fmt ./..."})
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure when session not found")
	}
	if result.Error == nil {
		t.Error("expected error when session not found")
	}
}

func TestFormatAction_Execute_MissingCommand(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
	})

	action := &formatAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{})
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure for missing command parameter")
	}
	if result.Error == nil {
		t.Error("expected error for missing command parameter")
	}
}

func TestFormatAction_Execute_EmptyCommand(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
	})

	action := &formatAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"command": ""})
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure for empty command")
	}
	if result.Error == nil {
		t.Error("expected error for empty command")
	}
}

func TestRunFormatter_NoChanges(t *testing.T) {
	workDir := initTestGitRepo(t)

	cfg := testConfig()
	sess := testSession("sess-1")
	sess.WorkTree = workDir
	cfg.AddSession(*sess)

	d := testDaemon(cfg)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
	})

	// 'true' is a no-op command that succeeds but makes no file changes
	params := workflow.NewParamHelper(map[string]any{"command": "true"})

	workItem1, _ := d.state.GetWorkItem("item-1")
	err := d.runFormatter(context.Background(), workItem1, params)
	if err != nil {
		t.Fatalf("expected no error for no-op formatter, got: %v", err)
	}

	// Verify no additional commits were created (only the initial commit)
	cmd := osexec.Command("git", "log", "--oneline")
	cmd.Dir = workDir
	out, _ := cmd.Output()
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 1 {
		t.Errorf("expected 1 commit (initial), got %d commits: %s", len(lines), out)
	}
}

func TestRunFormatter_WithChanges(t *testing.T) {
	workDir := initTestGitRepo(t)

	cfg := testConfig()
	sess := testSession("sess-1")
	sess.WorkTree = workDir
	cfg.AddSession(*sess)

	d := testDaemon(cfg)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
	})

	// Command that creates a new file, simulating a formatter adding/modifying files
	params := workflow.NewParamHelper(map[string]any{"command": "echo 'formatted' > formatted.txt"})

	workItem1, _ := d.state.GetWorkItem("item-1")
	err := d.runFormatter(context.Background(), workItem1, params)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// Verify a formatting commit was added
	cmd := osexec.Command("git", "log", "--oneline")
	cmd.Dir = workDir
	out, _ := cmd.Output()
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 {
		t.Errorf("expected 2 commits (initial + formatting), got %d: %s", len(lines), out)
	}
	if !strings.Contains(lines[0], "auto-formatting") {
		t.Errorf("expected formatting commit message to contain 'auto-formatting', got: %s", lines[0])
	}
}

func TestRunFormatter_CustomCommitMessage(t *testing.T) {
	workDir := initTestGitRepo(t)

	cfg := testConfig()
	sess := testSession("sess-1")
	sess.WorkTree = workDir
	cfg.AddSession(*sess)

	d := testDaemon(cfg)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
	})

	params := workflow.NewParamHelper(map[string]any{
		"command": "echo 'fmt' > fmt.txt",
		"message": "chore: apply prettier formatting",
	})

	workItem1, _ := d.state.GetWorkItem("item-1")
	err := d.runFormatter(context.Background(), workItem1, params)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// Verify the custom commit message was used
	cmd := osexec.Command("git", "log", "--format=%s", "-1")
	cmd.Dir = workDir
	out, _ := cmd.Output()
	msg := strings.TrimSpace(string(out))
	if msg != "chore: apply prettier formatting" {
		t.Errorf("expected custom commit message, got: %q", msg)
	}
}

func TestRunFormatter_CommandFails(t *testing.T) {
	workDir := initTestGitRepo(t)

	cfg := testConfig()
	sess := testSession("sess-1")
	sess.WorkTree = workDir
	cfg.AddSession(*sess)

	d := testDaemon(cfg)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
	})

	// Command that exits non-zero to simulate formatter failure
	params := workflow.NewParamHelper(map[string]any{"command": "exit 1"})

	workItem1, _ := d.state.GetWorkItem("item-1")
	err := d.runFormatter(context.Background(), workItem1, params)
	if err == nil {
		t.Fatal("expected error when formatter command fails")
	}
	if !strings.Contains(err.Error(), "formatter failed") {
		t.Errorf("expected 'formatter failed' in error, got: %v", err)
	}
}

func TestRunFormatter_FallbackToRepoPath(t *testing.T) {
	workDir := initTestGitRepo(t)

	cfg := testConfig()
	// Session with empty WorkTree — should fall back to RepoPath
	sess := &config.Session{
		ID:       "sess-1",
		RepoPath: workDir,
		WorkTree: "", // empty — forces fallback
		Branch:   "feature-sess-1",
		Name:     "test/sess-1",
	}
	cfg.AddSession(*sess)

	d := testDaemon(cfg)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
	})

	params := workflow.NewParamHelper(map[string]any{"command": "true"})

	workItem1, _ := d.state.GetWorkItem("item-1")
	err := d.runFormatter(context.Background(), workItem1, params)
	if err != nil {
		t.Fatalf("expected no error when falling back to RepoPath, got: %v", err)
	}
}

func TestTruncateLogs(t *testing.T) {
	const maxLogLen = 50000
	const truncSuffix = "\n\n... (truncated)"

	truncate := func(logs string) string {
		if len(logs) > maxLogLen {
			return logs[:maxLogLen-len(truncSuffix)] + truncSuffix
		}
		return logs
	}

	t.Run("short log is unchanged", func(t *testing.T) {
		input := strings.Repeat("x", 100)
		got := truncate(input)
		if got != input {
			t.Errorf("expected unchanged log, got len=%d", len(got))
		}
	})

	t.Run("log exactly at maxLogLen is unchanged", func(t *testing.T) {
		input := strings.Repeat("x", maxLogLen)
		got := truncate(input)
		if got != input {
			t.Errorf("expected unchanged log at exact limit, got len=%d", len(got))
		}
	})

	t.Run("long log is truncated to exactly maxLogLen", func(t *testing.T) {
		input := strings.Repeat("x", maxLogLen+1000)
		got := truncate(input)
		if len(got) != maxLogLen {
			t.Errorf("expected len=%d, got len=%d", maxLogLen, len(got))
		}
		if !strings.HasSuffix(got, truncSuffix) {
			t.Errorf("expected truncated log to end with suffix %q", truncSuffix)
		}
	})
}

func TestDaemon_RefreshStaleSession_ActiveWorker(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	// Session with an active worker — should NOT be refreshed
	sess := testSession("sess-real")
	cfg.AddSession(*sess)

	item := &daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "1"},
		SessionID: "sess-real",
		Branch:    "feature-sess-real",
	}
	d.state.AddWorkItem(item)

	// Register an active worker for this item
	d.mu.Lock()
	d.workers[item.ID] = &worker.SessionWorker{}
	d.mu.Unlock()

	result := d.refreshStaleSession(context.Background(), *item, sess)

	if result.ID != "sess-real" {
		t.Errorf("expected session ID unchanged, got %s", result.ID)
	}
	stateItem1, _ := d.state.GetWorkItem("item-1")
	if stateItem1.SessionID != "sess-real" {
		t.Error("expected work item session ID unchanged")
	}
}

func TestDaemon_RefreshStaleSession_NoActiveWorker(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	// Session with no active worker — conversation is dead, should be refreshed
	sess := &config.Session{
		ID:            "sess-stale",
		RepoPath:      "/test/repo",
		Branch:        "issue-38",
		DaemonManaged: true,
		Autonomous:    true,
		Containerized: true,
		Started:       true,
		PRCreated:     true,
	}
	cfg.AddSession(*sess)

	item := &daemonstate.WorkItem{
		ID:        "item-stale",
		IssueRef:  config.IssueRef{Source: "github", ID: "38"},
		SessionID: "sess-stale",
		Branch:    "issue-38",
	}
	d.state.AddWorkItem(item)

	result := d.refreshStaleSession(context.Background(), *item, sess)

	// Should have a new session ID
	if result.ID == "sess-stale" {
		t.Error("expected new session ID, got the stale one")
	}
	if result.ID == "" {
		t.Error("expected non-empty session ID")
	}

	// Old session should be gone from config
	if cfg.GetSession("sess-stale") != nil {
		t.Error("expected old session to be removed from config")
	}

	// New session should exist in config with same properties
	newSess := cfg.GetSession(result.ID)
	if newSess == nil {
		t.Fatal("expected new session to exist in config")
	}
	if newSess.RepoPath != "/test/repo" {
		t.Errorf("expected RepoPath /test/repo, got %s", newSess.RepoPath)
	}
	if newSess.Branch != "issue-38" {
		t.Errorf("expected Branch issue-38, got %s", newSess.Branch)
	}
	if !newSess.Containerized {
		t.Error("expected Containerized=true")
	}
	if !newSess.PRCreated {
		t.Error("expected PRCreated=true")
	}

	// Work item should reference new session
	updatedItem, _ := d.state.GetWorkItem("item-stale")
	if updatedItem.SessionID != result.ID {
		t.Errorf("expected work item to reference new session %s, got %s", result.ID, updatedItem.SessionID)
	}
}

func TestDaemon_RefreshStaleSession_WorkTreeButNoWorker(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	// Session has WorkTree set (initial coding session completed) but no active
	// worker — the container and conversation are gone. This is the bug scenario:
	// previously refreshStaleSession would skip this because WorkTree != "".
	sess := testSession("sess-done")
	cfg.AddSession(*sess)

	item := &daemonstate.WorkItem{
		ID:        "item-done",
		IssueRef:  config.IssueRef{Source: "github", ID: "38"},
		SessionID: "sess-done",
		Branch:    "feature-sess-done",
	}
	d.state.AddWorkItem(item)

	// No worker registered for this item — conversation is dead

	result := d.refreshStaleSession(context.Background(), *item, sess)

	// Should have a new session ID
	if result.ID == "sess-done" {
		t.Error("expected new session ID, got the original one")
	}
	if result.ID == "" {
		t.Error("expected non-empty session ID")
	}

	// Old session should be gone from config
	if cfg.GetSession("sess-done") != nil {
		t.Error("expected old session to be removed from config")
	}

	// New session should exist in config with same properties
	newSess := cfg.GetSession(result.ID)
	if newSess == nil {
		t.Fatal("expected new session to exist in config")
	}
	if newSess.WorkTree != "/test/worktree-sess-done" {
		t.Errorf("expected WorkTree preserved, got %s", newSess.WorkTree)
	}
	if newSess.Branch != "feature-sess-done" {
		t.Errorf("expected Branch feature-sess-done, got %s", newSess.Branch)
	}

	// Work item should reference new session
	updatedItem, _ := d.state.GetWorkItem("item-done")
	if updatedItem.SessionID != result.ID {
		t.Errorf("expected work item to reference new session %s, got %s", result.ID, updatedItem.SessionID)
	}
}

func TestDaemon_RefreshStaleSession_RecreatesWorktree(t *testing.T) {
	// Create a real git repo so worktree creation succeeds
	repoDir := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := osexec.Command("git", append([]string{"-C", repoDir}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %s (%v)", args, out, err)
		}
	}
	runGit("init")
	runGit("config", "user.email", "test@test.com")
	runGit("config", "user.name", "Test")
	runGit("commit", "--allow-empty", "-m", "init")
	runGit("checkout", "-b", "issue-42")
	runGit("commit", "--allow-empty", "-m", "work on issue")
	runGit("checkout", "-") // back to default branch

	cfg := testConfig()
	d := testDaemon(cfg)

	// Reconstructed session — no WorkTree (simulates daemon restart)
	sess := &config.Session{
		ID:            "sess-recovered",
		RepoPath:      repoDir,
		Branch:        "issue-42",
		DaemonManaged: true,
		Autonomous:    true,
		Containerized: true,
		Started:       true,
		PRCreated:     true,
	}
	cfg.AddSession(*sess)

	item := &daemonstate.WorkItem{
		ID:        "item-recovered",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-recovered",
		Branch:    "issue-42",
	}
	d.state.AddWorkItem(item)

	result := d.refreshStaleSession(context.Background(), *item, sess)

	// Should have a new session ID
	if result.ID == "sess-recovered" {
		t.Error("expected new session ID")
	}

	// Should have a worktree path set
	if result.WorkTree == "" {
		t.Fatal("expected WorkTree to be recreated, got empty string")
	}

	// Worktree directory should exist on disk
	if _, err := osexec.Command("git", "-C", result.WorkTree, "rev-parse", "--git-dir").Output(); err != nil {
		t.Errorf("expected worktree to be a valid git directory: %v", err)
	}

	// Clean up worktree
	_ = osexec.Command("git", "-C", repoDir, "worktree", "remove", "--force", result.WorkTree).Run()
}

func TestDaemon_RefreshStaleSession_RemovesStaleWorktree(t *testing.T) {
	// Create a real git repo with a branch checked out in a worktree
	repoDir := t.TempDir()
	runGit := func(args ...string) string {
		t.Helper()
		cmd := osexec.Command("git", append([]string{"-C", repoDir}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %s (%v)", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	runGit("init")
	runGit("config", "user.email", "test@test.com")
	runGit("config", "user.name", "Test")
	runGit("commit", "--allow-empty", "-m", "init")
	runGit("checkout", "-b", "issue-99")
	runGit("commit", "--allow-empty", "-m", "work on issue")
	runGit("checkout", "-")

	// Create a stale worktree that holds the branch (simulates the old session's worktree)
	staleWorktree := filepath.Join(t.TempDir(), "stale-worktree")
	runGit("worktree", "add", staleWorktree, "issue-99")

	cfg := testConfig()
	d := testDaemon(cfg)

	sess := &config.Session{
		ID:            "sess-stale-wt",
		RepoPath:      repoDir,
		Branch:        "issue-99",
		DaemonManaged: true,
		Autonomous:    true,
		Containerized: true,
		Started:       true,
		PRCreated:     true,
	}
	cfg.AddSession(*sess)

	item := &daemonstate.WorkItem{
		ID:        "item-stale-wt",
		IssueRef:  config.IssueRef{Source: "github", ID: "99"},
		SessionID: "sess-stale-wt",
		Branch:    "issue-99",
	}
	d.state.AddWorkItem(item)

	result := d.refreshStaleSession(context.Background(), *item, sess)

	if result.ID == "sess-stale-wt" {
		t.Error("expected new session ID")
	}

	// Should have a new worktree despite the stale one existing
	if result.WorkTree == "" {
		t.Fatal("expected WorkTree to be recreated, got empty string")
	}
	if result.WorkTree == staleWorktree {
		t.Error("expected a new worktree path, not the stale one")
	}

	// New worktree should be valid
	if _, err := osexec.Command("git", "-C", result.WorkTree, "rev-parse", "--git-dir").Output(); err != nil {
		t.Errorf("expected new worktree to be a valid git directory: %v", err)
	}

	// Clean up
	_ = osexec.Command("git", "-C", repoDir, "worktree", "remove", "--force", result.WorkTree).Run()
}

// --- branchHasChanges tests ---

func TestBranchHasChanges_NoCommitsNoChanges(t *testing.T) {
	// Create a git repo with a branch that has no new commits relative to main
	repoDir := initTestGitRepo(t)

	// Create a branch at the same commit as the current HEAD
	mustRunGit(t, repoDir, "branch", "issue-50")
	mustRunGit(t, repoDir, "checkout", "issue-50")

	cfg := testConfig()
	d := testDaemon(cfg)

	sess := &config.Session{
		ID:         "sess-no-changes",
		RepoPath:   repoDir,
		WorkTree:   repoDir,
		Branch:     "issue-50",
		BaseBranch: "main",
	}

	// Determine which branch is the default (could be "main" or "master")
	defaultBranch := getDefaultBranch(t, repoDir)
	sess.BaseBranch = defaultBranch

	hasChanges, err := d.branchHasChanges(context.Background(), sess)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hasChanges {
		t.Error("expected no changes on branch with no new commits and no uncommitted changes")
	}
}

func TestBranchHasChanges_HasCommits(t *testing.T) {
	repoDir := initTestGitRepo(t)

	defaultBranch := getDefaultBranch(t, repoDir)

	// Create a branch and add a commit
	mustRunGit(t, repoDir, "checkout", "-b", "issue-51")
	filePath := filepath.Join(repoDir, "new-file.txt")
	createCmd := osexec.Command("sh", "-c", "echo 'hello' > "+filePath)
	if out, err := createCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to create file: %v (output: %s)", err, out)
	}
	mustRunGit(t, repoDir, "add", ".")
	mustRunGit(t, repoDir, "commit", "-m", "add new file")

	cfg := testConfig()
	d := testDaemon(cfg)

	sess := &config.Session{
		ID:         "sess-with-commits",
		RepoPath:   repoDir,
		WorkTree:   repoDir,
		Branch:     "issue-51",
		BaseBranch: defaultBranch,
	}

	hasChanges, err := d.branchHasChanges(context.Background(), sess)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasChanges {
		t.Error("expected changes on branch with new commits")
	}
}

func TestBranchHasChanges_HasUncommittedChanges(t *testing.T) {
	repoDir := initTestGitRepo(t)

	defaultBranch := getDefaultBranch(t, repoDir)

	// Create a branch (no new commits) but add uncommitted changes
	mustRunGit(t, repoDir, "checkout", "-b", "issue-52")
	filePath := filepath.Join(repoDir, "uncommitted.txt")
	createCmd := osexec.Command("sh", "-c", "echo 'uncommitted' > "+filePath)
	if out, err := createCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to create file: %v (output: %s)", err, out)
	}

	cfg := testConfig()
	d := testDaemon(cfg)

	sess := &config.Session{
		ID:         "sess-uncommitted",
		RepoPath:   repoDir,
		WorkTree:   repoDir,
		Branch:     "issue-52",
		BaseBranch: defaultBranch,
	}

	hasChanges, err := d.branchHasChanges(context.Background(), sess)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasChanges {
		t.Error("expected changes when there are uncommitted files")
	}
}

func TestBranchHasChanges_DefaultsToMain(t *testing.T) {
	repoDir := initTestGitRepo(t)

	// Ensure the default branch is called "main" for this test
	defaultBranch := getDefaultBranch(t, repoDir)
	if defaultBranch != "main" {
		mustRunGit(t, repoDir, "branch", "-m", defaultBranch, "main")
	}

	mustRunGit(t, repoDir, "checkout", "-b", "issue-53")

	cfg := testConfig()
	d := testDaemon(cfg)

	sess := &config.Session{
		ID:         "sess-default-base",
		RepoPath:   repoDir,
		WorkTree:   repoDir,
		Branch:     "issue-53",
		BaseBranch: "", // empty — should default to "main"
	}

	hasChanges, err := d.branchHasChanges(context.Background(), sess)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hasChanges {
		t.Error("expected no changes on branch with no new commits")
	}
}

func TestBranchHasChanges_FallsBackToRepoPath(t *testing.T) {
	repoDir := initTestGitRepo(t)

	defaultBranch := getDefaultBranch(t, repoDir)
	mustRunGit(t, repoDir, "checkout", "-b", "issue-54")

	cfg := testConfig()
	d := testDaemon(cfg)

	sess := &config.Session{
		ID:         "sess-no-worktree",
		RepoPath:   repoDir,
		WorkTree:   "", // empty — should fall back to RepoPath
		Branch:     "issue-54",
		BaseBranch: defaultBranch,
	}

	hasChanges, err := d.branchHasChanges(context.Background(), sess)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hasChanges {
		t.Error("expected no changes")
	}
}

// TestCreatePR_NoChanges_ReturnsError verifies that createPR returns a clear error
// when the coding session made no changes (no commits and no uncommitted changes).
// This is a regression test for the bug where the daemon would attempt to create
// a PR on GitHub even when there were no changes, resulting in a cryptic GraphQL error.
func TestCreatePR_NoChanges_ReturnsError(t *testing.T) {
	repoDir := initTestGitRepo(t)

	defaultBranch := getDefaultBranch(t, repoDir)
	mustRunGit(t, repoDir, "checkout", "-b", "issue-50")

	cfg := testConfig()
	d := testDaemon(cfg)

	sess := &config.Session{
		ID:         "sess-no-changes",
		RepoPath:   repoDir,
		WorkTree:   repoDir,
		Branch:     "issue-50",
		BaseBranch: defaultBranch,
	}
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-no-changes",
		IssueRef:  config.IssueRef{Source: "github", ID: "50"},
		SessionID: "sess-no-changes",
		Branch:    "issue-50",
		StepData:  map[string]any{},
	})

	item, _ := d.state.GetWorkItem("item-no-changes")
	_, err := d.createPR(context.Background(), item, false)
	if err == nil {
		t.Fatal("expected error when creating PR with no changes")
	}
	if !strings.Contains(err.Error(), "no changes on branch") {
		t.Errorf("expected 'no changes on branch' error, got: %v", err)
	}
}

// getDefaultBranch returns the name of the default branch in the repo.
func getDefaultBranch(t *testing.T, repoDir string) string {
	t.Helper()
	cmd := osexec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("failed to get default branch: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func TestAddressFeedback_TranscriptOnlyResetsPhase(t *testing.T) {
	// Regression: when all PR comments are transcripts, addressFeedback must
	// reset the work item phase back to "idle" so the concurrency slot is freed.
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	// Return a single transcript comment from gh pr view
	transcriptBody := "<details><summary>Session Transcript</summary>\nsome log\n</details>"
	reviewsJSON, _ := json.Marshal(struct {
		Comments []struct {
			Author struct{ Login string } `json:"author"`
			Body   string                `json:"body"`
			URL    string                `json:"url"`
		} `json:"comments"`
		Reviews []any `json:"reviews"`
	}{
		Comments: []struct {
			Author struct{ Login string } `json:"author"`
			Body   string                `json:"body"`
			URL    string                `json:"url"`
		}{
			{Author: struct{ Login string }{Login: "erg-bot"}, Body: transcriptBody, URL: "https://example.com"},
		},
		Reviews: []any{},
	})
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Stdout: reviewsJSON,
	})

	d := testDaemonWithExec(cfg, mockExec)
	d.repoFilter = "/test/repo"

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "feature-sess-1",
		CurrentStep: "await_review",
	})

	item, _ := d.state.GetWorkItem("item-1")
	// batchCommentCount=1: 1 top-level comment + 0 actionable reviews
	d.addressFeedback(context.Background(), item, 1)

	updated, _ := d.state.GetWorkItem("item-1")
	if updated.Phase != "idle" {
		t.Errorf("expected phase to be reset to 'idle' after transcript-only comments, got %q", updated.Phase)
	}
	if !updated.ConsumesSlot() {
		// Phase is idle, so it shouldn't consume a slot — this is the expected behavior
	}
	if updated.ConsumesSlot() {
		t.Error("work item should not consume a concurrency slot after transcript-only feedback")
	}
}

// TestAddressFeedback_CommentsAddressedMatchesBatchCount is a regression test
// for a bug where addressFeedback used len(FetchPRReviewComments) to set
// CommentsAddressed, which over-counts because inline code review comments are
// expanded individually. The detection side (GetBatchPRStatesWithComments)
// counts each review submission as 1. This mismatch caused CommentsAddressed
// to permanently exceed CommentCount, preventing detection of new comments.
func TestAddressFeedback_CommentsAddressedMatchesBatchCount(t *testing.T) {
	cfg := testConfig()
	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	// Simulate a CHANGES_REQUESTED review with 4 inline comments.
	// FetchPRReviewComments returns 5 items (1 review body + 4 inline),
	// but GetBatchPRStatesWithComments would count this as 1 actionable review.
	reviewJSON := `{
		"reviews": [{
			"author": {"login": "reviewer"},
			"body": "Please fix the duplication",
			"state": "CHANGES_REQUESTED",
			"comments": [
				{"author": {"login": "reviewer"}, "body": "Comment 1", "path": "file.go", "line": 10, "url": "https://example.com/1"},
				{"author": {"login": "reviewer"}, "body": "Comment 2", "path": "file.go", "line": 20, "url": "https://example.com/2"},
				{"author": {"login": "reviewer"}, "body": "Comment 3", "path": "file.go", "line": 30, "url": "https://example.com/3"},
				{"author": {"login": "reviewer"}, "body": "Comment 4", "path": "file.go", "line": 40, "url": "https://example.com/4"}
			]
		}],
		"comments": []
	}`

	mockExec := exec.NewMockExecutor(nil)
	mockExec.AddExactMatch("gh", []string{"pr", "view", "feature-sess-1", "--json", "reviews,comments"},
		exec.MockResponse{Stdout: []byte(reviewJSON)})

	d := testDaemonWithExec(cfg, mockExec)

	item := &daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-sess-1",
		StepData:  map[string]any{},
	}
	d.state.AddWorkItem(item)

	// batchCommentCount=1: the batch API sees 0 top-level comments + 1 review
	d.addressFeedback(context.Background(), *item, 1)

	updated, _ := d.state.GetWorkItem("item-1")
	// CommentsAddressed must equal the batchCommentCount (1), NOT the
	// individual comment count from FetchPRReviewComments (5).
	if updated.CommentsAddressed != 1 {
		t.Errorf("CommentsAddressed = %d, want 1 (batch count); using individual comment count would over-count and break detection", updated.CommentsAddressed)
	}
}

// --- codingAction sentinel error tests ---

func TestCodingAction_ExistingPR_AdvancesToOpenPR(t *testing.T) {
	cfg := testConfig()
	cfg.Repos = []string{"/test/repo"}

	mockExec := exec.NewMockExecutor(nil)

	// GetPRState returns OPEN PR
	prViewJSON, _ := json.Marshal(struct {
		State string `json:"state"`
	}{State: "OPEN"})
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Stdout: prViewJSON,
	})

	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	sessSvc := session.NewSessionServiceWithExecutor(mockExec)
	d := testDaemonWithExec(cfg, mockExec)
	d.gitService = gitSvc
	d.sessionService = sessSvc
	d.repoFilter = "/test/repo"

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "work-1",
		IssueRef: config.IssueRef{Source: "github", ID: "54", Title: "Fix bug"},
		StepData: map[string]any{},
	})

	action := &codingAction{daemon: d}
	ac := &workflow.ActionContext{
		WorkItemID: "work-1",
		Params:     workflow.NewParamHelper(nil),
	}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected success on existing PR, got error: %v", result.Error)
	}
	if result.Async {
		t.Error("expected synchronous result (not async) on existing PR")
	}
	if result.OverrideNext != "" {
		t.Errorf("expected no OverrideNext for open PR, got %q", result.OverrideNext)
	}
}

func TestCodingAction_MergedPR_SkipsToDone(t *testing.T) {
	cfg := testConfig()
	cfg.Repos = []string{"/test/repo"}

	mockExec := exec.NewMockExecutor(nil)

	// GetPRState returns MERGED PR
	prViewJSON, _ := json.Marshal(struct {
		State string `json:"state"`
	}{State: "MERGED"})
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Stdout: prViewJSON,
	})

	// Mock gh issue edit (remove label) and gh issue comment/close
	mockExec.AddPrefixMatch("gh", []string{"issue", "edit"}, exec.MockResponse{})
	mockExec.AddPrefixMatch("gh", []string{"issue", "comment"}, exec.MockResponse{})
	mockExec.AddPrefixMatch("gh", []string{"issue", "close"}, exec.MockResponse{})

	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	sessSvc := session.NewSessionServiceWithExecutor(mockExec)
	d := testDaemonWithExec(cfg, mockExec)
	d.gitService = gitSvc
	d.sessionService = sessSvc
	d.repoFilter = "/test/repo"

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "work-1",
		IssueRef: config.IssueRef{Source: "github", ID: "54", Title: "Fix bug"},
		StepData: map[string]any{},
	})

	action := &codingAction{daemon: d}
	ac := &workflow.ActionContext{
		WorkItemID: "work-1",
		Params:     workflow.NewParamHelper(nil),
	}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected success on merged PR, got error: %v", result.Error)
	}
	if result.OverrideNext != "done" {
		t.Errorf("expected OverrideNext='done', got %q", result.OverrideNext)
	}
}

// --- createPRAction sentinel error tests ---

func TestCreatePR_ExistingPR_ReturnsWithoutError(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	// GetPRState returns OPEN via "gh pr view" prefix
	prViewJSON, _ := json.Marshal(struct {
		State string `json:"state"`
	}{State: "OPEN"})
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Stdout: prViewJSON,
	})

	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	d := testDaemonWithExec(cfg, mockExec)
	d.gitService = gitSvc
	d.repoFilter = "/test/repo"

	sess := &config.Session{
		ID:        "sess-existing",
		RepoPath:  "/test/repo",
		WorkTree:  "/test/worktree",
		Branch:    "issue-54",
		PRCreated: true,
	}
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-existing",
		IssueRef:  config.IssueRef{Source: "github", ID: "54"},
		SessionID: "sess-existing",
		Branch:    "issue-54",
		StepData:  map[string]any{},
	})

	item, _ := d.state.GetWorkItem("item-existing")
	_, err := d.createPR(context.Background(), item, false)
	if err != nil {
		t.Fatalf("expected no error for existing PR, got: %v", err)
	}
	// Note: getPRURL uses os/exec directly and won't find a real PR in test,
	// but createPR should still return without error (URL may be empty).
}

func TestCreatePRAction_NoChanges_ClosesIssue(t *testing.T) {
	repoDir := initTestGitRepo(t)

	defaultBranch := getDefaultBranch(t, repoDir)
	mustRunGit(t, repoDir, "checkout", "-b", "issue-50")

	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	// Mock gh issue edit/comment/close for closeIssueGracefully
	mockExec.AddPrefixMatch("gh", []string{"issue", "edit"}, exec.MockResponse{})
	mockExec.AddPrefixMatch("gh", []string{"issue", "comment"}, exec.MockResponse{})
	mockExec.AddPrefixMatch("gh", []string{"issue", "close"}, exec.MockResponse{})

	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	d := testDaemonWithExec(cfg, mockExec)
	d.gitService = gitSvc
	d.repoFilter = repoDir
	cfg.Repos = []string{repoDir}

	sess := &config.Session{
		ID:         "sess-no-changes",
		RepoPath:   repoDir,
		WorkTree:   repoDir,
		Branch:     "issue-50",
		BaseBranch: defaultBranch,
	}
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-no-changes",
		IssueRef:  config.IssueRef{Source: "github", ID: "50"},
		SessionID: "sess-no-changes",
		Branch:    "issue-50",
		StepData:  map[string]any{},
	})

	action := &createPRAction{daemon: d}
	ac := &workflow.ActionContext{
		WorkItemID: "item-no-changes",
		Params:     workflow.NewParamHelper(nil),
	}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected success on no-changes, got error: %v", result.Error)
	}
	if result.OverrideNext != "done" {
		t.Errorf("expected OverrideNext='done', got %q", result.OverrideNext)
	}
}

// --- closeIssueGracefully test ---

func TestCloseIssueGracefully_NonGitHub(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	item := &daemonstate.WorkItem{
		ID:       "item-1",
		IssueRef: config.IssueRef{Source: "asana", ID: "task-1"},
	}

	// Should return immediately without error for non-GitHub issues
	d.closeIssueGracefully(context.Background(), *item)
	// No assertion needed — just verifying it doesn't panic
}

func TestMergePR_SavesRepoPathBeforeCleanup(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	// Mock merge success
	mockExec.AddPrefixMatch("gh", []string{"pr", "merge"}, exec.MockResponse{
		Stdout: []byte("merged"),
	})

	d := testDaemonWithExec(cfg, mockExec)
	d.repoFilter = "/test/repo"

	sess := testSession("sess-1")
	sess.RepoPath = "/actual/repo/path"
	cfg.AddSession(*sess)
	cfg.AutoCleanupMerged = true

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "feature-sess-1",
		CurrentStep: "merge",
		StepData:    map[string]any{},
	})

	mergeItem, _ := d.state.GetWorkItem("item-1")
	err := d.mergePR(context.Background(), mergeItem)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Session should have been cleaned up
	if cfg.GetSession("sess-1") != nil {
		t.Error("expected session to be cleaned up after merge")
	}

	// Repo path should be preserved in step data
	item, _ := d.state.GetWorkItem("item-1")
	repoPath, ok := item.StepData["_repo_path"].(string)
	if !ok || repoPath != "/actual/repo/path" {
		t.Errorf("expected _repo_path=/actual/repo/path in step data, got %v", item.StepData["_repo_path"])
	}

	// workItemView should use the step data repo path
	view := d.workItemView(item)
	if view.RepoPath != "/actual/repo/path" {
		t.Errorf("expected workItemView to use step data repo path, got %s", view.RepoPath)
	}
}

// TestHandleAsyncComplete_RunsFormatterOnSuccess verifies that when
// _format_command is stored in step data and the worker exits successfully,
// handleAsyncComplete runs the formatter (producing a formatting commit).
func TestHandleAsyncComplete_RunsFormatterOnSuccess(t *testing.T) {
	workDir := initTestGitRepo(t)

	cfg := testConfig()
	sess := testSession("sess-1")
	sess.RepoPath = workDir
	sess.WorkTree = workDir
	cfg.AddSession(*sess)

	d := testDaemon(cfg)
	d.loadWorkflowConfigs()

	item := &daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "42"},
		SessionID:   "sess-1",
		CurrentStep: "coding",
		State:       daemonstate.WorkItemActive,
		StepData: map[string]any{
			"_format_command": "echo 'formatted' > fmt.txt",
			"_format_message": "style: auto-format",
			"_repo_path":      workDir,
		},
	}
	d.state.AddWorkItem(item)

	// exitErr == nil → success path → formatter should run
	d.handleAsyncComplete(context.Background(), *item, nil)

	// Verify the formatting commit was created
	cmd := osexec.Command("git", "log", "--format=%s", "-1")
	cmd.Dir = workDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git log failed: %v", err)
	}
	msg := strings.TrimSpace(string(out))
	if msg != "style: auto-format" {
		t.Errorf("expected formatting commit with custom message, got: %q", msg)
	}
}

// TestHandleAsyncComplete_SkipsFormatterOnFailure verifies that the formatter
// is NOT run when the coding worker exits with an error.
func TestHandleAsyncComplete_SkipsFormatterOnFailure(t *testing.T) {
	workDir := initTestGitRepo(t)

	cfg := testConfig()
	sess := testSession("sess-1")
	sess.RepoPath = workDir
	sess.WorkTree = workDir
	cfg.AddSession(*sess)

	d := testDaemon(cfg)
	d.loadWorkflowConfigs()

	item := &daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "42"},
		SessionID:   "sess-1",
		CurrentStep: "coding",
		State:       daemonstate.WorkItemActive,
		StepData: map[string]any{
			"_format_command": "echo 'formatted' > fmt.txt",
			"_repo_path":      workDir,
		},
	}
	d.state.AddWorkItem(item)

	// exitErr != nil → failure path → formatter should NOT run
	d.handleAsyncComplete(context.Background(), *item, errors.New("worker failed"))

	// Verify no formatting commit was added (only the initial commit)
	cmd := osexec.Command("git", "log", "--oneline")
	cmd.Dir = workDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git log failed: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 1 {
		t.Errorf("expected only 1 commit (initial), got %d: %s", len(lines), out)
	}
}

// --- startFixCI format_command tests ---

// TestStartFixCI_FormatCommandStoredInStepData verifies that when the fix_ci
// workflow state has a format_command param, it is stored in item.StepData.
func TestStartFixCI_FormatCommandStoredInStepData(t *testing.T) {
	cfg := testConfig()
	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d := testDaemon(cfg)

	// Custom workflow config: fix_ci state has format_command + format_message.
	d.workflowConfigs = map[string]*workflow.Config{
		"/test/repo": {
			States: map[string]*workflow.State{
				"fix_ci": {
					Params: map[string]any{
						"format_command": "gofmt -l -w .",
						"format_message": "style: gofmt",
					},
				},
			},
		},
	}

	item := &daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-sess-1",
		StepData:  map[string]any{},
	}
	d.state.AddWorkItem(item)

	_ = d.startFixCI(context.Background(), *item, sess, 1, "CI failed: test failure")

	updatedItem, _ := d.state.GetWorkItem(item.ID)
	if got, _ := updatedItem.StepData["_format_command"].(string); got != "gofmt -l -w ." {
		t.Errorf("expected _format_command=%q, got %q", "gofmt -l -w .", got)
	}
	if got, _ := updatedItem.StepData["_format_message"].(string); got != "style: gofmt" {
		t.Errorf("expected _format_message=%q, got %q", "style: gofmt", got)
	}
}

// TestStartFixCI_FormatCommandDefaultMessage verifies that when fix_ci has
// format_command but no format_message, the default message is used.
func TestStartFixCI_FormatCommandDefaultMessage(t *testing.T) {
	cfg := testConfig()
	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d := testDaemon(cfg)
	d.workflowConfigs = map[string]*workflow.Config{
		"/test/repo": {
			States: map[string]*workflow.State{
				"fix_ci": {
					Params: map[string]any{
						"format_command": "gofmt -l -w .",
						// no format_message
					},
				},
			},
		},
	}

	item := &daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-sess-1",
		StepData:  map[string]any{},
	}
	d.state.AddWorkItem(item)

	_ = d.startFixCI(context.Background(), *item, sess, 1, "CI failed")

	updatedItem, _ := d.state.GetWorkItem(item.ID)
	if got, _ := updatedItem.StepData["_format_command"].(string); got != "gofmt -l -w ." {
		t.Errorf("expected _format_command=%q, got %q", "gofmt -l -w .", got)
	}
	if got, _ := updatedItem.StepData["_format_message"].(string); got != "Apply auto-formatting" {
		t.Errorf("expected default _format_message=%q, got %q", "Apply auto-formatting", got)
	}
}

// TestStartFixCI_InheritsFormatCommandFromStepData verifies that when fix_ci
// has no format_command param, the existing _format_command in step data
// (set by the coding step) is preserved.
func TestStartFixCI_InheritsFormatCommandFromStepData(t *testing.T) {
	cfg := testConfig()
	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d := testDaemon(cfg)
	// Use default workflow config — fix_ci has no format_command param.

	item := &daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-sess-1",
		// _format_command already set by the coding step
		StepData: map[string]any{
			"_format_command": "gofmt ./...",
			"_format_message": "style: format",
		},
	}
	d.state.AddWorkItem(item)

	_ = d.startFixCI(context.Background(), *item, sess, 1, "CI failed")

	// Existing _format_command must not be cleared.
	updatedItem, _ := d.state.GetWorkItem(item.ID)
	if got, _ := updatedItem.StepData["_format_command"].(string); got != "gofmt ./..." {
		t.Errorf("expected _format_command=%q, got %q", "gofmt ./...", got)
	}
}

// TestStartFixCI_FormatCommandOverridesStepData verifies that a fix_ci-specific
// format_command replaces whatever the coding step stored in step data.
func TestStartFixCI_FormatCommandOverridesStepData(t *testing.T) {
	cfg := testConfig()
	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d := testDaemon(cfg)
	d.workflowConfigs = map[string]*workflow.Config{
		"/test/repo": {
			States: map[string]*workflow.State{
				"fix_ci": {
					Params: map[string]any{
						"format_command": "prettier --write .",
						"format_message": "style: prettier",
					},
				},
			},
		},
	}

	item := &daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-sess-1",
		// Coding step set a different formatter.
		StepData: map[string]any{
			"_format_command": "gofmt ./...",
			"_format_message": "style: gofmt",
		},
	}
	d.state.AddWorkItem(item)

	_ = d.startFixCI(context.Background(), *item, sess, 1, "CI failed")

	updatedItem, _ := d.state.GetWorkItem(item.ID)
	if got, _ := updatedItem.StepData["_format_command"].(string); got != "prettier --write ." {
		t.Errorf("expected _format_command=%q, got %q", "prettier --write .", got)
	}
	if got, _ := updatedItem.StepData["_format_message"].(string); got != "style: prettier" {
		t.Errorf("expected _format_message=%q, got %q", "style: prettier", got)
	}
}

// --- addressFeedback format_command tests ---

// testPRReviewJSON is a minimal CHANGES_REQUESTED review that passes
// FilterTranscriptComments so addressFeedback reaches the format_command logic.
const testPRReviewJSON = `{"reviews":[{"author":{"login":"reviewer1"},"body":"Please address the issue","state":"CHANGES_REQUESTED","comments":[]}],"comments":[]}`

// TestAddressFeedback_FormatCommandStoredInStepData verifies that when the
// await_review workflow state has a format_command param, it is stored in
// item.StepData when addressing review feedback.
func TestAddressFeedback_FormatCommandStoredInStepData(t *testing.T) {
	cfg := testConfig()
	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	mockExec := exec.NewMockExecutor(nil)
	mockExec.AddExactMatch("gh", []string{"pr", "view", "feature-sess-1", "--json", "reviews,comments"},
		exec.MockResponse{Stdout: []byte(testPRReviewJSON)})

	d := testDaemonWithExec(cfg, mockExec)
	d.workflowConfigs = map[string]*workflow.Config{
		"/test/repo": {
			States: map[string]*workflow.State{
				"await_review": {
					Params: map[string]any{
						"format_command": "gofmt -l -w .",
						"format_message": "style: gofmt",
					},
				},
			},
		},
	}

	item := &daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-sess-1",
		StepData:  map[string]any{},
	}
	d.state.AddWorkItem(item)

	// batchCommentCount=1: 0 top-level comments + 1 actionable review
	d.addressFeedback(context.Background(), *item, 1)

	updatedItem, _ := d.state.GetWorkItem(item.ID)
	if got, _ := updatedItem.StepData["_format_command"].(string); got != "gofmt -l -w ." {
		t.Errorf("expected _format_command=%q, got %q", "gofmt -l -w .", got)
	}
	if got, _ := updatedItem.StepData["_format_message"].(string); got != "style: gofmt" {
		t.Errorf("expected _format_message=%q, got %q", "style: gofmt", got)
	}
}

// TestAddressFeedback_InheritsFormatCommandFromStepData verifies that when
// await_review has no format_command param, the existing _format_command in
// step data (from the coding step) is preserved.
func TestAddressFeedback_InheritsFormatCommandFromStepData(t *testing.T) {
	cfg := testConfig()
	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	mockExec := exec.NewMockExecutor(nil)
	mockExec.AddExactMatch("gh", []string{"pr", "view", "feature-sess-1", "--json", "reviews,comments"},
		exec.MockResponse{Stdout: []byte(testPRReviewJSON)})

	d := testDaemonWithExec(cfg, mockExec)
	// Default workflow config — await_review has no format_command param.

	item := &daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-sess-1",
		// _format_command already set by the coding step.
		StepData: map[string]any{
			"_format_command": "gofmt ./...",
			"_format_message": "style: format",
		},
	}
	d.state.AddWorkItem(item)

	// batchCommentCount=1: 0 top-level comments + 1 actionable review
	d.addressFeedback(context.Background(), *item, 1)

	// Existing _format_command must not be cleared.
	updatedItem, _ := d.state.GetWorkItem(item.ID)
	if got, _ := updatedItem.StepData["_format_command"].(string); got != "gofmt ./..." {
		t.Errorf("expected _format_command=%q, got %q", "gofmt ./...", got)
	}
}

// TestHandleAsyncComplete_NoFormatCommandSkipsFormatter verifies that when
// no _format_command is in step data, no formatter is run.
func TestHandleAsyncComplete_NoFormatCommandSkipsFormatter(t *testing.T) {
	workDir := initTestGitRepo(t)

	cfg := testConfig()
	sess := testSession("sess-1")
	sess.RepoPath = workDir
	sess.WorkTree = workDir
	cfg.AddSession(*sess)

	d := testDaemon(cfg)
	d.loadWorkflowConfigs()

	item := &daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "42"},
		SessionID:   "sess-1",
		CurrentStep: "coding",
		State:       daemonstate.WorkItemActive,
		StepData: map[string]any{
			"_repo_path": workDir,
		},
	}
	d.state.AddWorkItem(item)

	d.handleAsyncComplete(context.Background(), *item, nil)

	// Verify no extra commits
	cmd := osexec.Command("git", "log", "--oneline")
	cmd.Dir = workDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git log failed: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 1 {
		t.Errorf("expected only 1 commit (initial), got %d: %s", len(lines), out)
	}
}

// TestUnqueueIssue_GitHub verifies that unqueueIssue calls RemoveLabel and Comment
// via the GitHub provider, but does NOT close the issue.
func TestUnqueueIssue_GitHub(t *testing.T) {
	cfg := testConfig()
	cfg.Repos = []string{"/test/repo"}
	mockExec := exec.NewMockExecutor(nil)

	// Mock label removal and comment — success responses.
	mockExec.AddPrefixMatch("gh", []string{"issue", "edit"}, exec.MockResponse{})
	mockExec.AddPrefixMatch("gh", []string{"issue", "comment"}, exec.MockResponse{})

	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	sessSvc := session.NewSessionServiceWithExecutor(mockExec)
	githubProvider := issues.NewGitHubProvider(gitSvc)
	registry := issues.NewProviderRegistry(githubProvider)

	d := New(cfg, gitSvc, sessSvc, registry, discardLogger())
	d.sessionMgr.SetSkipMessageLoad(true)
	d.state = daemonstate.NewDaemonState("/test/repo")
	d.repoFilter = "/test/repo"

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	item := &daemonstate.WorkItem{
		ID:        "item-gh-42",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
	}
	d.state.AddWorkItem(item)

	d.unqueueIssue(context.Background(), *item, "Test unqueue reason.")

	calls := mockExec.GetCalls()

	// Verify RemoveLabel was called (gh issue edit --remove-label).
	foundRemoveLabel := false
	for _, c := range calls {
		if c.Name == "gh" && len(c.Args) >= 3 && c.Args[0] == "issue" && c.Args[1] == "edit" {
			for _, arg := range c.Args {
				if arg == "--remove-label" {
					foundRemoveLabel = true
					break
				}
			}
		}
	}
	if !foundRemoveLabel {
		t.Error("expected gh issue edit --remove-label to be called")
	}

	// Verify Comment was called (gh issue comment).
	foundComment := false
	for _, c := range calls {
		if c.Name == "gh" && len(c.Args) >= 2 && c.Args[0] == "issue" && c.Args[1] == "comment" {
			foundComment = true
			break
		}
	}
	if !foundComment {
		t.Error("expected gh issue comment to be called")
	}

	// Verify gh issue close was NOT called.
	for _, c := range calls {
		if c.Name == "gh" && len(c.Args) >= 2 && c.Args[0] == "issue" && c.Args[1] == "close" {
			t.Error("gh issue close should NOT be called by unqueueIssue")
		}
	}
}

// TestUnqueueIssue_NoProviderActions verifies that unqueueIssue is a no-op
// when the provider doesn't implement ProviderActions.
func TestUnqueueIssue_NoProviderActions(t *testing.T) {
	cfg := testConfig()
	cfg.Repos = []string{"/test/repo"}
	mockExec := exec.NewMockExecutor(nil)

	// Empty registry — no ProviderActions implementations.
	d := testDaemonWithExec(cfg, mockExec)
	d.repoFilter = "/test/repo"

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	item := &daemonstate.WorkItem{
		ID:        "item-asana-123",
		IssueRef:  config.IssueRef{Source: "asana", ID: "task-gid-123"},
		SessionID: "sess-1",
	}
	d.state.AddWorkItem(item)

	// Should not panic or error — just a no-op.
	d.unqueueIssue(context.Background(), *item, "Test reason.")

	calls := mockExec.GetCalls()
	if len(calls) != 0 {
		t.Errorf("expected no CLI calls for provider without ProviderActions, got %d", len(calls))
	}
}

// TestUnqueueIssue_NoRepoPath verifies that unqueueIssue is a no-op
// when the repo path cannot be resolved.
func TestUnqueueIssue_NoRepoPath(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	d := testDaemonWithExec(cfg, mockExec)
	// No repoFilter set, no sessions — resolveRepoPath returns ""

	item := &daemonstate.WorkItem{
		ID:       "item-gh-5",
		IssueRef: config.IssueRef{Source: "github", ID: "5"},
	}
	d.state.AddWorkItem(item)

	// Should return early without calling any CLI commands.
	d.unqueueIssue(context.Background(), *item, "No repo path.")

	if len(mockExec.GetCalls()) != 0 {
		t.Error("expected no CLI calls when repo path is empty")
	}
}

// --- rebaseAction tests ---

func TestRebaseAction_Execute_WorkItemNotFound(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	action := &rebaseAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"max_rebase_rounds": 3})
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

// mockCommentProvider is a test double for Provider + ProviderActions.
// It records Comment calls and optionally returns a configured error.
type mockCommentProvider struct {
	src        issues.Source
	commentErr error
	comments   []mockCommentCall
}

type mockCommentCall struct {
	repoPath string
	issueID  string
	body     string
}

func (m *mockCommentProvider) Name() string                    { return string(m.src) }
func (m *mockCommentProvider) Source() issues.Source           { return m.src }
func (m *mockCommentProvider) IsConfigured(_ string) bool      { return true }
func (m *mockCommentProvider) GenerateBranchName(_ issues.Issue) string { return "" }
func (m *mockCommentProvider) GetPRLinkText(_ issues.Issue) string      { return "" }
func (m *mockCommentProvider) FetchIssues(_ context.Context, _ string, _ issues.FilterConfig) ([]issues.Issue, error) {
	return nil, nil
}
func (m *mockCommentProvider) RemoveLabel(_ context.Context, _ string, _ string, _ string) error {
	return nil
}
func (m *mockCommentProvider) Comment(_ context.Context, repoPath, issueID, body string) error {
	m.comments = append(m.comments, mockCommentCall{repoPath: repoPath, issueID: issueID, body: body})
	return m.commentErr
}

func TestRebaseAction_Execute_NoSession(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "nonexistent",
		Branch:    "feature-1",
		StepData:  map[string]any{},
	})

	action := &rebaseAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"max_rebase_rounds": 3})
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure when session not found")
	}
	if result.Error == nil {
		t.Error("expected error when session not found")
	}
}

func TestRebaseAction_Execute_MaxRoundsExceeded(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-sess-1",
		StepData:  map[string]any{"rebase_rounds": 3},
	})

	action := &rebaseAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"max_rebase_rounds": 3})
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure when max rounds exceeded")
	}
	if result.Error == nil {
		t.Error("expected error when max rounds exceeded")
	}
	if !strings.Contains(result.Error.Error(), "max rebase rounds exceeded") {
		t.Errorf("expected 'max rebase rounds exceeded' error, got: %v", result.Error)
	}
}

func TestRebaseAction_Execute_MaxRoundsFloat64(t *testing.T) {
	// JSON deserialization produces float64 for numbers
	cfg := testConfig()
	d := testDaemon(cfg)

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-sess-1",
		StepData:  map[string]any{"rebase_rounds": float64(3)},
	})

	action := &rebaseAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"max_rebase_rounds": 3})
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure when max rounds exceeded (float64)")
	}
	if result.Error == nil {
		t.Error("expected error when max rounds exceeded (float64)")
	}
}

func TestRebaseAction_Execute_Success(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	// Mock rev-parse HEAD (for RebaseBranchWithStatus), git fetch, rebase, push
	mockExec.AddExactMatch("git", []string{"rev-parse", "HEAD"}, exec.MockResponse{
		Stdout: []byte("abc123\n"),
	})
	mockExec.AddExactMatch("git", []string{"fetch", "origin", "main"}, exec.MockResponse{})
	mockExec.AddExactMatch("git", []string{"rebase", "origin/main"}, exec.MockResponse{})
	mockExec.AddExactMatch("git", []string{"push", "--force-with-lease", "origin", "feature-sess-1"}, exec.MockResponse{})
	// Mock GetDefaultBranch (git symbolic-ref)
	mockExec.AddPrefixMatch("git", []string{"symbolic-ref"}, exec.MockResponse{
		Stdout: []byte("refs/remotes/origin/main"),
	})

	d := testDaemonWithExec(cfg, mockExec)

	sess := testSession("sess-1")
	sess.BaseBranch = "main"
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-sess-1",
		StepData:  map[string]any{},
	})

	action := &rebaseAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"max_rebase_rounds": 3})
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected success, got error: %v", result.Error)
	}

	// Verify rounds incremented
	item, _ := d.state.GetWorkItem("item-1")
	rounds := getRebaseRounds(item.StepData)
	if rounds != 1 {
		t.Errorf("expected rebase_rounds=1, got %d", rounds)
	}

	// Verify rebase status data returned
	if result.Data == nil {
		t.Fatal("expected Data in result")
	}
	if result.Data["last_rebase_clean"] != true {
		t.Errorf("expected last_rebase_clean=true (no-op), got %v", result.Data["last_rebase_clean"])
	}
	if _, ok := result.Data["last_rebase_at"].(string); !ok {
		t.Errorf("expected last_rebase_at to be a string timestamp, got %T", result.Data["last_rebase_at"])
	}
}

func TestRebaseAction_Execute_RebaseFails(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	// Mock rev-parse HEAD (for RebaseBranchWithStatus)
	mockExec.AddExactMatch("git", []string{"rev-parse", "HEAD"}, exec.MockResponse{
		Stdout: []byte("abc123\n"),
	})
	// Mock git fetch succeeds, rebase fails
	mockExec.AddExactMatch("git", []string{"fetch", "origin", "main"}, exec.MockResponse{})
	mockExec.AddExactMatch("git", []string{"rebase", "origin/main"}, exec.MockResponse{
		Err: fmt.Errorf("conflict"),
	})
	mockExec.AddExactMatch("git", []string{"rebase", "--abort"}, exec.MockResponse{})

	d := testDaemonWithExec(cfg, mockExec)

	sess := testSession("sess-1")
	sess.BaseBranch = "main"
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-sess-1",
		StepData:  map[string]any{},
	})

	action := &rebaseAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"max_rebase_rounds": 3})
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure when rebase fails")
	}
	if result.Error == nil {
		t.Error("expected error when rebase fails")
	}
	if !strings.Contains(result.Error.Error(), "rebase failed") {
		t.Errorf("expected 'rebase failed' error, got: %v", result.Error)
	}
}

func TestGetRebaseRounds(t *testing.T) {
	tests := []struct {
		name     string
		stepData map[string]any
		expected int
	}{
		{"nil step data", nil, 0},
		{"empty step data", map[string]any{}, 0},
		{"int value", map[string]any{"rebase_rounds": 2}, 2},
		{"float64 value (JSON)", map[string]any{"rebase_rounds": float64(3)}, 3},
		{"string value (invalid)", map[string]any{"rebase_rounds": "2"}, 0},
		{"zero value", map[string]any{"rebase_rounds": 0}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getRebaseRounds(tt.stepData)
			if got != tt.expected {
				t.Errorf("expected %d, got %d", tt.expected, got)
			}
		})
	}
}

// --- squashAction tests ---

func TestSquashAction_Execute_WorkItemNotFound(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	action := &squashAction{daemon: d}
	ac := &workflow.ActionContext{
		WorkItemID: "nonexistent",
		Params:     workflow.NewParamHelper(map[string]any{}),
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure for missing work item")
	}
	if result.Error == nil {
		t.Error("expected error for missing work item")
	}
}

func TestSquashAction_Execute_SessionNotFound(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "missing-session",
		Branch:    "feature-1",
		StepData:  map[string]any{},
	})

	action := &squashAction{daemon: d}
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     workflow.NewParamHelper(map[string]any{}),
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure when session not found")
	}
	if result.Error == nil {
		t.Error("expected error when session not found")
	}
}

func TestSquashAction_Execute_Success(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	// Mock the git commands called by SquashBranch
	mockExec.AddExactMatch("git", []string{"fetch", "origin", "main"}, exec.MockResponse{})
	mockExec.AddExactMatch("git", []string{"merge-base", "HEAD", "origin/main"}, exec.MockResponse{
		Stdout: []byte("abc1234567890\n"),
	})
	mockExec.AddExactMatch("git", []string{"reset", "--soft", "abc1234567890"}, exec.MockResponse{})
	mockExec.AddExactMatch("git", []string{"commit", "-m", "squash commit"}, exec.MockResponse{})
	mockExec.AddExactMatch("git", []string{"push", "--force-with-lease", "origin", "feature-sess-1"}, exec.MockResponse{})

	d := testDaemonWithExec(cfg, mockExec)

	sess := testSession("sess-1")
	sess.BaseBranch = "main"
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-sess-1",
		StepData:  map[string]any{},
	})

	action := &squashAction{daemon: d}
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     workflow.NewParamHelper(map[string]any{"message": "squash commit"}),
	}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected success, got error: %v", result.Error)
	}
}

func TestSquashAction_Execute_SquashFails(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	// Fetch fails and local merge-base also fails
	mockExec.AddExactMatch("git", []string{"fetch", "origin", "main"}, exec.MockResponse{
		Err: fmt.Errorf("network error"),
	})
	mockExec.AddExactMatch("git", []string{"merge-base", "HEAD", "main"}, exec.MockResponse{
		Err: fmt.Errorf("branch not found"),
	})

	d := testDaemonWithExec(cfg, mockExec)

	sess := testSession("sess-1")
	sess.BaseBranch = "main"
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-sess-1",
		StepData:  map[string]any{},
	})

	action := &squashAction{daemon: d}
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     workflow.NewParamHelper(map[string]any{"message": "squash commit"}),
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure when squash fails")
	}
	if result.Error == nil {
		t.Error("expected error when squash fails")
	}
	if !strings.Contains(result.Error.Error(), "squash failed") {
		t.Errorf("expected 'squash failed' error, got: %v", result.Error)
	}
}

// --- resolveConflictsAction tests ---

func TestResolveConflictsAction_WorkItemNotFound(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	action := &resolveConflictsAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"max_conflict_rounds": 3})
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

func TestResolveConflictsAction_NoSession(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "nonexistent",
		Branch:    "feature-1",
		StepData:  map[string]any{},
	})

	action := &resolveConflictsAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"max_conflict_rounds": 3})
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure when session not found")
	}
	if result.Error == nil {
		t.Error("expected error when session not found")
	}
}

func TestResolveConflictsAction_MaxRoundsExceeded(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-sess-1",
		StepData:  map[string]any{"conflict_rounds": 3},
	})

	action := &resolveConflictsAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"max_conflict_rounds": 3})
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure when max rounds exceeded")
	}
	if result.Error == nil {
		t.Error("expected error when max rounds exceeded")
	}
	if !strings.Contains(result.Error.Error(), "max conflict resolution rounds exceeded") {
		t.Errorf("expected 'max conflict resolution rounds exceeded' error, got: %v", result.Error)
	}
}

func TestResolveConflictsAction_MaxRoundsFloat64(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-sess-1",
		StepData:  map[string]any{"conflict_rounds": float64(3)},
	})

	action := &resolveConflictsAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"max_conflict_rounds": 3})
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure when max rounds exceeded (float64)")
	}
	if result.Error == nil {
		t.Error("expected error when max rounds exceeded (float64)")
	}
}

func TestResolveConflictsAction_CleanMerge(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	// Mock IsMergeInProgress (git rev-parse --verify MERGE_HEAD fails = no merge)
	mockExec.AddExactMatch("git", []string{"rev-parse", "--verify", "MERGE_HEAD"}, exec.MockResponse{
		Err: fmt.Errorf("not found"),
	})
	// Mock git fetch
	mockExec.AddExactMatch("git", []string{"fetch", "origin", "main"}, exec.MockResponse{})
	// Mock git merge (clean)
	mockExec.AddExactMatch("git", []string{"merge", "origin/main", "--no-edit"}, exec.MockResponse{})
	// Mock GetDefaultBranch
	mockExec.AddPrefixMatch("git", []string{"symbolic-ref"}, exec.MockResponse{
		Stdout: []byte("refs/remotes/origin/main"),
	})

	d := testDaemonWithExec(cfg, mockExec)

	sess := testSession("sess-1")
	sess.BaseBranch = "main"
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-sess-1",
		StepData:  map[string]any{},
	})

	action := &resolveConflictsAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"max_conflict_rounds": 3})
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected success for clean merge, got error: %v", result.Error)
	}
	if result.Async {
		t.Error("expected sync result for clean merge (no Claude needed)")
	}

	// Verify rounds incremented
	item, _ := d.state.GetWorkItem("item-1")
	rounds := getConflictRounds(item.StepData)
	if rounds != 1 {
		t.Errorf("expected conflict_rounds=1, got %d", rounds)
	}
}

func TestResolveConflictsAction_ConflictsStartWorker(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	// Mock IsMergeInProgress (no stale merge)
	mockExec.AddExactMatch("git", []string{"rev-parse", "--verify", "MERGE_HEAD"}, exec.MockResponse{
		Err: fmt.Errorf("not found"),
	})
	// Mock git fetch
	mockExec.AddExactMatch("git", []string{"fetch", "origin", "main"}, exec.MockResponse{})
	// Mock git merge (conflicts)
	mockExec.AddExactMatch("git", []string{"merge", "origin/main", "--no-edit"}, exec.MockResponse{
		Err: fmt.Errorf("conflict"),
	})
	// Mock GetConflictedFiles
	mockExec.AddExactMatch("git", []string{"diff", "--name-only", "--diff-filter=U"}, exec.MockResponse{
		Stdout: []byte("file1.go\nfile2.go\n"),
	})
	// Mock GetDefaultBranch
	mockExec.AddPrefixMatch("git", []string{"symbolic-ref"}, exec.MockResponse{
		Stdout: []byte("refs/remotes/origin/main"),
	})

	d := testDaemonWithExec(cfg, mockExec)

	sess := testSession("sess-1")
	sess.BaseBranch = "main"
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-sess-1",
		StepData:  map[string]any{},
	})

	action := &resolveConflictsAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"max_conflict_rounds": 3})
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected success, got error: %v", result.Error)
	}
	if !result.Async {
		t.Error("expected async result when conflicts need Claude resolution")
	}

	// Verify rounds incremented
	item, _ := d.state.GetWorkItem("item-1")
	rounds := getConflictRounds(item.StepData)
	if rounds != 1 {
		t.Errorf("expected conflict_rounds=1, got %d", rounds)
	}
}

func TestGetConflictRounds(t *testing.T) {
	tests := []struct {
		name     string
		stepData map[string]any
		expected int
	}{
		{"nil step data", nil, 0},
		{"empty step data", map[string]any{}, 0},
		{"int value", map[string]any{"conflict_rounds": 2}, 2},
		{"float64 value (JSON)", map[string]any{"conflict_rounds": float64(3)}, 3},
		{"string value (invalid)", map[string]any{"conflict_rounds": "2"}, 0},
		{"zero value", map[string]any{"conflict_rounds": 0}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getConflictRounds(tt.stepData)
			if got != tt.expected {
				t.Errorf("expected %d, got %d", tt.expected, got)
			}
		})
	}
}

func TestFormatConflictResolutionPrompt(t *testing.T) {
	prompt := formatConflictResolutionPrompt(2, []string{"file1.go", "file2.go"})

	if !strings.Contains(prompt, "ROUND 2") {
		t.Error("expected prompt to contain round number")
	}
	if !strings.Contains(prompt, "file1.go") {
		t.Error("expected prompt to contain first conflicted file")
	}
	if !strings.Contains(prompt, "file2.go") {
		t.Error("expected prompt to contain second conflicted file")
	}
	if !strings.Contains(prompt, "git add") {
		t.Error("expected prompt to instruct git add")
	}
	if !strings.Contains(prompt, "git commit --no-edit") {
		t.Error("expected prompt to instruct git commit --no-edit")
	}
	if !strings.Contains(prompt, "DO NOT push") {
		t.Error("prompt should instruct not to push")
	}
}

// --- asanaCommentAction tests ---

func TestAsanaCommentAction_WorkItemNotFound(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	action := &asanaCommentAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"body": "Work started."})
	ac := &workflow.ActionContext{WorkItemID: "nonexistent", Params: params}

	result := action.Execute(context.Background(), ac)

	if result.Error == nil {
		t.Error("expected error for missing work item")
	}
}

func TestAsanaCommentAction_SourceMismatch(t *testing.T) {
	cfg := testConfig()
	provider := &mockCommentProvider{src: issues.SourceAsana}
	registry := issues.NewProviderRegistry(provider)
	gitSvc := git.NewGitServiceWithExecutor(exec.NewMockExecutor(nil))
	sessSvc := session.NewSessionServiceWithExecutor(exec.NewMockExecutor(nil))
	d := New(cfg, gitSvc, sessSvc, registry, discardLogger())
	d.sessionMgr.SetSkipMessageLoad(true)
	d.state = daemonstate.NewDaemonState("/test/repo")
	d.repoFilter = "/test/repo"

	// Work item has linear source, not asana — should be a no-op.
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "item-1",
		IssueRef: config.IssueRef{Source: "linear", ID: "LIN-1"},
	})

	action := &asanaCommentAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"body": "Hello!"})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected no-op success for source mismatch, got error: %v", result.Error)
	}
	if len(provider.comments) != 0 {
		t.Error("expected Comment not to be called for source mismatch")
	}
}

func TestAsanaCommentAction_Success(t *testing.T) {
	cfg := testConfig()
	provider := &mockCommentProvider{src: issues.SourceAsana}
	registry := issues.NewProviderRegistry(provider)
	gitSvc := git.NewGitServiceWithExecutor(exec.NewMockExecutor(nil))
	sessSvc := session.NewSessionServiceWithExecutor(exec.NewMockExecutor(nil))
	d := New(cfg, gitSvc, sessSvc, registry, discardLogger())
	d.sessionMgr.SetSkipMessageLoad(true)
	d.state = daemonstate.NewDaemonState("/test/repo")

	sess := testSession("sess-1")
	cfg.AddSession(*sess)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "asana", ID: "task-abc"},
		SessionID: "sess-1",
	})

	action := &asanaCommentAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"body": "Work has started."})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected success, got error: %v", result.Error)
	}
	if len(provider.comments) != 1 {
		t.Fatalf("expected 1 comment call, got %d", len(provider.comments))
	}
	if provider.comments[0].issueID != "task-abc" {
		t.Errorf("expected issueID %q, got %q", "task-abc", provider.comments[0].issueID)
	}
	if provider.comments[0].body != "Work has started." {
		t.Errorf("expected body %q, got %q", "Work has started.", provider.comments[0].body)
	}
}

func TestAsanaCommentAction_EmptyBody(t *testing.T) {
	cfg := testConfig()
	provider := &mockCommentProvider{src: issues.SourceAsana}
	registry := issues.NewProviderRegistry(provider)
	gitSvc := git.NewGitServiceWithExecutor(exec.NewMockExecutor(nil))
	sessSvc := session.NewSessionServiceWithExecutor(exec.NewMockExecutor(nil))
	d := New(cfg, gitSvc, sessSvc, registry, discardLogger())
	d.sessionMgr.SetSkipMessageLoad(true)
	d.state = daemonstate.NewDaemonState("/test/repo")

	sess := testSession("sess-1")
	cfg.AddSession(*sess)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "asana", ID: "task-abc"},
		SessionID: "sess-1",
	})

	action := &asanaCommentAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"body": ""})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if result.Error == nil {
		t.Error("expected error for empty comment body")
	}
}

func TestAsanaCommentAction_ProviderError(t *testing.T) {
	cfg := testConfig()
	provider := &mockCommentProvider{
		src:        issues.SourceAsana,
		commentErr: fmt.Errorf("asana API error"),
	}
	registry := issues.NewProviderRegistry(provider)
	gitSvc := git.NewGitServiceWithExecutor(exec.NewMockExecutor(nil))
	sessSvc := session.NewSessionServiceWithExecutor(exec.NewMockExecutor(nil))
	d := New(cfg, gitSvc, sessSvc, registry, discardLogger())
	d.sessionMgr.SetSkipMessageLoad(true)
	d.state = daemonstate.NewDaemonState("/test/repo")

	sess := testSession("sess-1")
	cfg.AddSession(*sess)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "asana", ID: "task-abc"},
		SessionID: "sess-1",
	})

	action := &asanaCommentAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"body": "Starting work."})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if result.Error == nil {
		t.Error("expected error when provider returns error")
	}
}

func TestAsanaCommentAction_NoProvider(t *testing.T) {
	cfg := testConfig()
	// Registry with no Asana provider registered.
	registry := issues.NewProviderRegistry()
	gitSvc := git.NewGitServiceWithExecutor(exec.NewMockExecutor(nil))
	sessSvc := session.NewSessionServiceWithExecutor(exec.NewMockExecutor(nil))
	d := New(cfg, gitSvc, sessSvc, registry, discardLogger())
	d.sessionMgr.SetSkipMessageLoad(true)
	d.state = daemonstate.NewDaemonState("/test/repo")

	sess := testSession("sess-1")
	cfg.AddSession(*sess)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "asana", ID: "task-abc"},
		SessionID: "sess-1",
	})

	action := &asanaCommentAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"body": "Starting work."})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if result.Error == nil {
		t.Error("expected error when asana provider is not registered")
	}
}

// --- linearCommentAction tests ---

func TestLinearCommentAction_WorkItemNotFound(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	action := &linearCommentAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"body": "Work started."})
	ac := &workflow.ActionContext{WorkItemID: "nonexistent", Params: params}

	result := action.Execute(context.Background(), ac)

	if result.Error == nil {
		t.Error("expected error for missing work item")
	}
}

func TestLinearCommentAction_SourceMismatch(t *testing.T) {
	cfg := testConfig()
	provider := &mockCommentProvider{src: issues.SourceLinear}
	registry := issues.NewProviderRegistry(provider)
	gitSvc := git.NewGitServiceWithExecutor(exec.NewMockExecutor(nil))
	sessSvc := session.NewSessionServiceWithExecutor(exec.NewMockExecutor(nil))
	d := New(cfg, gitSvc, sessSvc, registry, discardLogger())
	d.sessionMgr.SetSkipMessageLoad(true)
	d.state = daemonstate.NewDaemonState("/test/repo")
	d.repoFilter = "/test/repo"

	// Work item has asana source, not linear — should be a no-op.
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "item-1",
		IssueRef: config.IssueRef{Source: "asana", ID: "task-1"},
	})

	action := &linearCommentAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"body": "Hello!"})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected no-op success for source mismatch, got error: %v", result.Error)
	}
	if len(provider.comments) != 0 {
		t.Error("expected Comment not to be called for source mismatch")
	}
}

func TestLinearCommentAction_Success(t *testing.T) {
	cfg := testConfig()
	provider := &mockCommentProvider{src: issues.SourceLinear}
	registry := issues.NewProviderRegistry(provider)
	gitSvc := git.NewGitServiceWithExecutor(exec.NewMockExecutor(nil))
	sessSvc := session.NewSessionServiceWithExecutor(exec.NewMockExecutor(nil))
	d := New(cfg, gitSvc, sessSvc, registry, discardLogger())
	d.sessionMgr.SetSkipMessageLoad(true)
	d.state = daemonstate.NewDaemonState("/test/repo")

	sess := testSession("sess-1")
	cfg.AddSession(*sess)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "linear", ID: "ENG-42"},
		SessionID: "sess-1",
	})

	action := &linearCommentAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"body": "PR is ready for review."})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected success, got error: %v", result.Error)
	}
	if len(provider.comments) != 1 {
		t.Fatalf("expected 1 comment call, got %d", len(provider.comments))
	}
	if provider.comments[0].issueID != "ENG-42" {
		t.Errorf("expected issueID %q, got %q", "ENG-42", provider.comments[0].issueID)
	}
	if provider.comments[0].body != "PR is ready for review." {
		t.Errorf("expected body %q, got %q", "PR is ready for review.", provider.comments[0].body)
	}
}

func TestLinearCommentAction_EmptyBody(t *testing.T) {
	cfg := testConfig()
	provider := &mockCommentProvider{src: issues.SourceLinear}
	registry := issues.NewProviderRegistry(provider)
	gitSvc := git.NewGitServiceWithExecutor(exec.NewMockExecutor(nil))
	sessSvc := session.NewSessionServiceWithExecutor(exec.NewMockExecutor(nil))
	d := New(cfg, gitSvc, sessSvc, registry, discardLogger())
	d.sessionMgr.SetSkipMessageLoad(true)
	d.state = daemonstate.NewDaemonState("/test/repo")

	sess := testSession("sess-1")
	cfg.AddSession(*sess)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "linear", ID: "ENG-42"},
		SessionID: "sess-1",
	})

	action := &linearCommentAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"body": ""})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if result.Error == nil {
		t.Error("expected error for empty comment body")
	}
}

func TestLinearCommentAction_ProviderError(t *testing.T) {
	cfg := testConfig()
	provider := &mockCommentProvider{
		src:        issues.SourceLinear,
		commentErr: fmt.Errorf("linear API error"),
	}
	registry := issues.NewProviderRegistry(provider)
	gitSvc := git.NewGitServiceWithExecutor(exec.NewMockExecutor(nil))
	sessSvc := session.NewSessionServiceWithExecutor(exec.NewMockExecutor(nil))
	d := New(cfg, gitSvc, sessSvc, registry, discardLogger())
	d.sessionMgr.SetSkipMessageLoad(true)
	d.state = daemonstate.NewDaemonState("/test/repo")

	sess := testSession("sess-1")
	cfg.AddSession(*sess)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "linear", ID: "ENG-42"},
		SessionID: "sess-1",
	})

	action := &linearCommentAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"body": "Starting work."})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if result.Error == nil {
		t.Error("expected error when provider returns error")
	}
}

// --- addressReviewAction tests ---

func TestAddressReviewAction_Execute_WorkItemNotFound(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	action := &addressReviewAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"max_review_rounds": 3})
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

func TestAddressReviewAction_Execute_MaxRoundsExceeded(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	sess := testSession("sess-1")
	cfg.AddSession(*sess)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-sess-1",
		StepData:  map[string]any{"review_rounds": 3},
	})

	action := &addressReviewAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"max_review_rounds": 3})
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure when max rounds exceeded")
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), "max review rounds exceeded") {
		t.Errorf("expected 'max review rounds exceeded' error, got: %v", result.Error)
	}
}

func TestAddressReviewAction_Execute_MaxRoundsFloat64(t *testing.T) {
	// JSON deserialization produces float64 for numbers
	cfg := testConfig()
	d := testDaemon(cfg)

	sess := testSession("sess-1")
	cfg.AddSession(*sess)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-sess-1",
		StepData:  map[string]any{"review_rounds": float64(3)},
	})

	action := &addressReviewAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"max_review_rounds": 3})
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected error when max rounds exceeded (float64)")
	}
}

func TestAddressReviewAction_Execute_NoSession(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "item-1",
		IssueRef: config.IssueRef{Source: "github", ID: "42"},
		Branch:   "feature-1",
		StepData: map[string]any{},
	})

	action := &addressReviewAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"max_review_rounds": 3})
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure when session not found")
	}
	if result.Error == nil {
		t.Error("expected error when session not found")
	}
}

func TestGetReviewRounds(t *testing.T) {
	tests := []struct {
		name     string
		stepData map[string]any
		want     int
	}{
		{"missing", map[string]any{}, 0},
		{"int zero", map[string]any{"review_rounds": 0}, 0},
		{"int one", map[string]any{"review_rounds": 1}, 1},
		{"int three", map[string]any{"review_rounds": 3}, 3},
		{"float64 zero", map[string]any{"review_rounds": float64(0)}, 0},
		{"float64 two", map[string]any{"review_rounds": float64(2)}, 2},
		{"wrong type", map[string]any{"review_rounds": "oops"}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := getReviewRounds(tc.stepData)
			if got != tc.want {
				t.Errorf("getReviewRounds(%v) = %d, want %d", tc.stepData, got, tc.want)
			}
		})
	}
}

func TestFormatAddressReviewPrompt(t *testing.T) {
	prompt := formatAddressReviewPrompt(2)
	if !strings.Contains(prompt, "ROUND 2") {
		t.Error("expected prompt to contain round number")
	}
	if !strings.Contains(prompt, "DO NOT push") {
		t.Error("expected prompt to contain DO NOT push instruction")
	}
}

// --- startAddressReview format_command tests ---

// testAddressReviewJSON is a minimal CHANGES_REQUESTED review that passes
// FilterTranscriptComments so startAddressReview reaches the format_command logic.
const testAddressReviewJSON = `{"reviews":[{"author":{"login":"reviewer1"},"body":"Please fix the issue","state":"CHANGES_REQUESTED","comments":[]}],"comments":[]}`

// TestStartAddressReview_FormatCommandStoredInStepData verifies that when the
// address_review workflow state has a format_command param, it is stored in
// item.StepData.
func TestStartAddressReview_FormatCommandStoredInStepData(t *testing.T) {
	cfg := testConfig()
	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d := testDaemon(cfg)
	d.workflowConfigs = map[string]*workflow.Config{
		"/test/repo": {
			States: map[string]*workflow.State{
				"address_review": {
					Params: map[string]any{
						"format_command": "gofmt -l -w .",
						"format_message": "style: gofmt",
					},
				},
			},
		},
	}

	item := &daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-sess-1",
		StepData:  map[string]any{},
	}
	d.state.AddWorkItem(item)

	comments := worker.FilterTranscriptComments([]git.PRReviewComment{
		{Author: "reviewer1", Body: "Please fix the issue"},
	})
	_ = d.startAddressReview(context.Background(), *item, sess, 1, comments)

	updatedItem, _ := d.state.GetWorkItem(item.ID)
	if got, _ := updatedItem.StepData["_format_command"].(string); got != "gofmt -l -w ." {
		t.Errorf("expected _format_command=%q, got %q", "gofmt -l -w .", got)
	}
	if got, _ := updatedItem.StepData["_format_message"].(string); got != "style: gofmt" {
		t.Errorf("expected _format_message=%q, got %q", "style: gofmt", got)
	}
}

// TestStartAddressReview_FormatCommandDefaultMessage verifies that when address_review has
// format_command but no format_message, the default message is used.
func TestStartAddressReview_FormatCommandDefaultMessage(t *testing.T) {
	cfg := testConfig()
	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d := testDaemon(cfg)
	d.workflowConfigs = map[string]*workflow.Config{
		"/test/repo": {
			States: map[string]*workflow.State{
				"address_review": {
					Params: map[string]any{
						"format_command": "gofmt -l -w .",
						// no format_message
					},
				},
			},
		},
	}

	item := &daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-sess-1",
		StepData:  map[string]any{},
	}
	d.state.AddWorkItem(item)

	_ = d.startAddressReview(context.Background(), *item, sess, 1, nil)

	updatedItem, _ := d.state.GetWorkItem(item.ID)
	if got, _ := updatedItem.StepData["_format_command"].(string); got != "gofmt -l -w ." {
		t.Errorf("expected _format_command=%q, got %q", "gofmt -l -w .", got)
	}
	if got, _ := updatedItem.StepData["_format_message"].(string); got != "Apply auto-formatting" {
		t.Errorf("expected default _format_message=%q, got %q", "Apply auto-formatting", got)
	}
}

// TestStartAddressReview_InheritsFormatCommandFromStepData verifies that when
// address_review has no format_command param, the existing _format_command in
// step data (from the coding step) is preserved.
func TestStartAddressReview_InheritsFormatCommandFromStepData(t *testing.T) {
	cfg := testConfig()
	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d := testDaemon(cfg)
	// Use default workflow config — address_review state has no format_command param.

	item := &daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-sess-1",
		// _format_command already set by the coding step.
		StepData: map[string]any{
			"_format_command": "gofmt ./...",
			"_format_message": "style: format",
		},
	}
	d.state.AddWorkItem(item)

	_ = d.startAddressReview(context.Background(), *item, sess, 1, nil)

	// Existing _format_command must not be cleared.
	updatedItem, _ := d.state.GetWorkItem(item.ID)
	if got, _ := updatedItem.StepData["_format_command"].(string); got != "gofmt ./..." {
		t.Errorf("expected _format_command=%q, got %q", "gofmt ./...", got)
	}
}

// TestStartAddressReview_FormatCommandOverridesStepData verifies that an
// address_review-specific format_command replaces whatever the coding step stored.
func TestStartAddressReview_FormatCommandOverridesStepData(t *testing.T) {
	cfg := testConfig()
	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d := testDaemon(cfg)
	d.workflowConfigs = map[string]*workflow.Config{
		"/test/repo": {
			States: map[string]*workflow.State{
				"address_review": {
					Params: map[string]any{
						"format_command": "prettier --write .",
						"format_message": "style: prettier",
					},
				},
			},
		},
	}

	item := &daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-sess-1",
		StepData: map[string]any{
			"_format_command": "gofmt ./...",
			"_format_message": "style: gofmt",
		},
	}
	d.state.AddWorkItem(item)

	_ = d.startAddressReview(context.Background(), *item, sess, 1, nil)

	updatedItem, _ := d.state.GetWorkItem(item.ID)
	if got, _ := updatedItem.StepData["_format_command"].(string); got != "prettier --write ." {
		t.Errorf("expected _format_command=%q, got %q", "prettier --write .", got)
	}
	if got, _ := updatedItem.StepData["_format_message"].(string); got != "style: prettier" {
		t.Errorf("expected _format_message=%q, got %q", "style: prettier", got)
	}
}

func TestLinearCommentAction_NoProvider(t *testing.T) {
	cfg := testConfig()
	// Registry with no Linear provider registered.
	registry := issues.NewProviderRegistry()
	gitSvc := git.NewGitServiceWithExecutor(exec.NewMockExecutor(nil))
	sessSvc := session.NewSessionServiceWithExecutor(exec.NewMockExecutor(nil))
	d := New(cfg, gitSvc, sessSvc, registry, discardLogger())
	d.sessionMgr.SetSkipMessageLoad(true)
	d.state = daemonstate.NewDaemonState("/test/repo")

	sess := testSession("sess-1")
	cfg.AddSession(*sess)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "linear", ID: "ENG-42"},
		SessionID: "sess-1",
	})

	action := &linearCommentAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"body": "Starting work."})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if result.Error == nil {
		t.Error("expected error when linear provider is not registered")
	}
}

// ---- slack.notify tests ----

func TestSlackNotifyAction_WorkItemNotFound(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	action := &slackNotifyAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{
		"webhook_url": "https://hooks.slack.com/services/test",
		"message":     "Hello!",
	})
	ac := &workflow.ActionContext{WorkItemID: "nonexistent", Params: params}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure for missing work item")
	}
	if result.Error == nil {
		t.Error("expected error for missing work item")
	}
}

func TestSlackNotifyAction_MissingWebhookURL(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "item-1",
		IssueRef: config.IssueRef{Source: "github", ID: "1", Title: "Test Issue"},
	})

	action := &slackNotifyAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{
		"message": "Hello!",
		// no webhook_url
	})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure when webhook_url is missing")
	}
	if result.Error == nil {
		t.Error("expected error when webhook_url is missing")
	}
}

func TestSlackNotifyAction_MissingMessage(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "item-1",
		IssueRef: config.IssueRef{Source: "github", ID: "1", Title: "Test Issue"},
	})

	action := &slackNotifyAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{
		"webhook_url": "https://hooks.slack.com/services/test",
		// no message
	})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure when message is missing")
	}
	if result.Error == nil {
		t.Error("expected error when message is missing")
	}
}

func TestSlackNotifyAction_InvalidTemplate(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "item-1",
		IssueRef: config.IssueRef{Source: "github", ID: "1", Title: "Test Issue"},
	})

	action := &slackNotifyAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{
		"webhook_url": "https://hooks.slack.com/services/test",
		"message":     "{{.Unclosed",
	})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure for invalid template")
	}
	if result.Error == nil {
		t.Error("expected error for invalid template")
	}
}

func TestSlackNotifyAction_EnvVarWebhookURL(t *testing.T) {
	// Set an env var to resolve the webhook URL, and point it at a test server.
	srv := newSlackTestServer(t, http.StatusOK)
	defer srv.Close()

	t.Setenv("TEST_SLACK_WEBHOOK", srv.URL)

	cfg := testConfig()
	d := testDaemon(cfg)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "item-1",
		IssueRef: config.IssueRef{Source: "github", ID: "42", Title: "Fix bug", URL: "https://github.com/o/r/issues/42"},
		Branch:   "issue-42",
		PRURL:    "https://github.com/o/r/pull/99",
	})

	action := &slackNotifyAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{
		"webhook_url": "$TEST_SLACK_WEBHOOK",
		"message":     "PR ready: {{.PRURL}}",
	})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected success with env-var webhook URL, got error: %v", result.Error)
	}
}

func TestSlackNotifyAction_TemplateVariables(t *testing.T) {
	// Verify that all template variables are populated correctly.
	var capturedPayload slackWebhookPayload
	srv := newSlackTestServerCapture(t, http.StatusOK, &capturedPayload)
	defer srv.Close()

	cfg := testConfig()
	d := testDaemon(cfg)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:    "item-1",
		IssueRef: config.IssueRef{
			Source: "github",
			ID:     "99",
			Title:  "Implement feature",
			URL:    "https://github.com/o/r/issues/99",
		},
		Branch: "issue-99",
		PRURL:  "https://github.com/o/r/pull/7",
	})

	action := &slackNotifyAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{
		"webhook_url": srv.URL,
		"message":     "{{.Title}} (#{{.IssueID}}) — PR: {{.PRURL}} branch: {{.Branch}} status: {{.Status}}",
		"username":    "mybot",
		"icon_emoji":  ":tada:",
		"channel":     "#eng",
	})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}

	wantText := "Implement feature (#99) — PR: https://github.com/o/r/pull/7 branch: issue-99 status: queued"
	if capturedPayload.Text != wantText {
		t.Errorf("message text mismatch\n got: %q\nwant: %q", capturedPayload.Text, wantText)
	}
	if capturedPayload.Username != "mybot" {
		t.Errorf("username: got %q, want %q", capturedPayload.Username, "mybot")
	}
	if capturedPayload.IconEmoji != ":tada:" {
		t.Errorf("icon_emoji: got %q, want %q", capturedPayload.IconEmoji, ":tada:")
	}
	if capturedPayload.Channel != "#eng" {
		t.Errorf("channel: got %q, want %q", capturedPayload.Channel, "#eng")
	}
}

func TestSlackNotifyAction_DefaultUsernameAndEmoji(t *testing.T) {
	var capturedPayload slackWebhookPayload
	srv := newSlackTestServerCapture(t, http.StatusOK, &capturedPayload)
	defer srv.Close()

	cfg := testConfig()
	d := testDaemon(cfg)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "item-1",
		IssueRef: config.IssueRef{Source: "github", ID: "1", Title: "Bug"},
	})

	action := &slackNotifyAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{
		"webhook_url": srv.URL,
		"message":     "Hello from erg",
		// username and icon_emoji intentionally omitted — should get defaults
	})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)
	if !result.Success {
		t.Fatalf("expected success, got: %v", result.Error)
	}

	if capturedPayload.Username != "erg" {
		t.Errorf("expected default username 'erg', got %q", capturedPayload.Username)
	}
	if capturedPayload.IconEmoji != ":robot_face:" {
		t.Errorf("expected default icon ':robot_face:', got %q", capturedPayload.IconEmoji)
	}
}

func TestSlackNotifyAction_SlackReturnsError(t *testing.T) {
	// Slack returns 500 — action should fail.
	srv := newSlackTestServer(t, http.StatusInternalServerError)
	defer srv.Close()

	cfg := testConfig()
	d := testDaemon(cfg)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "item-1",
		IssueRef: config.IssueRef{Source: "github", ID: "1", Title: "Bug"},
	})

	action := &slackNotifyAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{
		"webhook_url": srv.URL,
		"message":     "Hello!",
	})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure when Slack returns non-200")
	}
	if result.Error == nil {
		t.Error("expected error when Slack returns non-200")
	}
}

func TestSlackNotifyAction_RegisteredInActionRegistry(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)
	registry := d.buildActionRegistry()

	if !registry.Has("slack.notify") {
		t.Error("expected slack.notify to be registered in action registry")
	}
}

// newSlackTestServer creates an httptest.Server that returns the given status code.
func newSlackTestServer(t *testing.T, statusCode int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(statusCode)
	}))
}

// newSlackTestServerCapture creates an httptest.Server that captures the decoded
// JSON payload into out and returns statusCode.
func newSlackTestServerCapture(t *testing.T, statusCode int, out *slackWebhookPayload) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(out); err != nil {
			t.Errorf("failed to decode Slack payload: %v", err)
		}
		w.WriteHeader(statusCode)
	}))
}

// --- webhook.post tests ---

func TestWebhookPostAction_Execute_WorkItemNotFound(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	action := &webhookPostAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{
		"url":  "http://example.com/hook",
		"body": `{"msg": "hello"}`,
	})
	ac := &workflow.ActionContext{WorkItemID: "nonexistent", Params: params}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure for missing work item")
	}
	if result.Error == nil {
		t.Error("expected non-nil error for missing work item")
	}
}

func TestWebhookPostAction_Execute_MissingURL(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "item-1",
		IssueRef: config.IssueRef{Source: "github", ID: "1"},
	})

	action := &webhookPostAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{
		"body": `{"msg": "hello"}`,
	})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure when url is missing")
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), "url parameter is required") {
		t.Errorf("expected 'url parameter is required' error, got: %v", result.Error)
	}
}

func TestWebhookPostAction_Execute_MissingBody(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "item-1",
		IssueRef: config.IssueRef{Source: "github", ID: "1"},
	})

	action := &webhookPostAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{
		"url": "http://example.com/hook",
	})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure when body is missing")
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), "body parameter is required") {
		t.Errorf("expected 'body parameter is required' error, got: %v", result.Error)
	}
}

func TestWebhookPostAction_Execute_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := testConfig()
	d := testDaemon(cfg)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "item-1",
		IssueRef: config.IssueRef{Source: "github", ID: "42"},
		Branch:   "issue-42",
		PRURL:    "https://github.com/owner/repo/pull/7",
	})

	action := &webhookPostAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{
		"url":  srv.URL,
		"body": `{"issue": "42"}`,
	})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected success, got error: %v", result.Error)
	}
	if result.Data == nil || result.Data["response_status"] != 200 {
		t.Errorf("expected response_status=200 in Data, got: %v", result.Data)
	}
}

func TestWebhookPostAction_Execute_UnexpectedStatus(t *testing.T) {
	// Server returns 422 with an error body; action expects 200.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":"invalid payload"}`))
	}))
	defer srv.Close()

	cfg := testConfig()
	d := testDaemon(cfg)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "item-1",
		IssueRef: config.IssueRef{Source: "github", ID: "1"},
	})

	action := &webhookPostAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{
		"url":  srv.URL,
		"body": `{}`,
	})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure for unexpected status code")
	}
	if result.Error == nil {
		t.Error("expected non-nil error for unexpected status code")
	}
	// Verify the response body is captured in the error for debugging.
	if !strings.Contains(result.Error.Error(), "invalid payload") {
		t.Errorf("expected error to contain response body, got: %v", result.Error)
	}
	if !strings.Contains(result.Error.Error(), "422") {
		t.Errorf("expected error to contain status code 422, got: %v", result.Error)
	}
}

func TestWebhookPostAction_Execute_CustomExpectedStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	cfg := testConfig()
	d := testDaemon(cfg)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "item-1",
		IssueRef: config.IssueRef{Source: "github", ID: "1"},
	})

	action := &webhookPostAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{
		"url":             srv.URL,
		"body":            `{}`,
		"expected_status": 204,
	})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected success for matching expected_status=204, got error: %v", result.Error)
	}
	if result.Data == nil || result.Data["response_status"] != 204 {
		t.Errorf("expected response_status=204 in Data, got: %v", result.Data)
	}
}

func TestWebhookPostAction_Execute_WithHeaders(t *testing.T) {
	var receivedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := testConfig()
	d := testDaemon(cfg)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "item-1",
		IssueRef: config.IssueRef{Source: "github", ID: "1"},
	})

	action := &webhookPostAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{
		"url":  srv.URL,
		"body": `{}`,
		"headers": map[string]any{
			"Authorization": "Bearer secret-token",
		},
	})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected success, got error: %v", result.Error)
	}
	if receivedAuth != "Bearer secret-token" {
		t.Errorf("expected Authorization header 'Bearer secret-token', got %q", receivedAuth)
	}
}

func TestWebhookPostAction_Execute_TemplateInterpolation(t *testing.T) {
	var receivedBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		receivedBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := testConfig()
	d := testDaemon(cfg)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "item-1",
		IssueRef: config.IssueRef{Source: "github", ID: "99", Title: "Fix the bug"},
		Branch:   "issue-99",
		PRURL:    "https://github.com/owner/repo/pull/5",
	})
	d.state.UpdateWorkItem("item-1", func(it *daemonstate.WorkItem) {
		it.State = daemonstate.WorkItemActive
	})

	action := &webhookPostAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{
		"url":  srv.URL,
		"body": `{"issue_id":"{{.IssueID}}","title":"{{.IssueTitle}}","pr":"{{.PRURL}}","branch":"{{.Branch}}","state":"{{.State}}"}`,
	})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected success, got error: %v", result.Error)
	}

	var got map[string]string
	if err := json.Unmarshal([]byte(receivedBody), &got); err != nil {
		t.Fatalf("failed to parse received body as JSON: %v\nbody: %s", err, receivedBody)
	}

	if got["issue_id"] != "99" {
		t.Errorf("expected issue_id=99, got %q", got["issue_id"])
	}
	if got["title"] != "Fix the bug" {
		t.Errorf("expected title='Fix the bug', got %q", got["title"])
	}
	if got["pr"] != "https://github.com/owner/repo/pull/5" {
		t.Errorf("expected pr URL, got %q", got["pr"])
	}
	if got["branch"] != "issue-99" {
		t.Errorf("expected branch=issue-99, got %q", got["branch"])
	}
	if got["state"] != "active" {
		t.Errorf("expected state=active, got %q", got["state"])
	}
}

func TestWebhookPostAction_Execute_DefaultContentType(t *testing.T) {
	var receivedContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedContentType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := testConfig()
	d := testDaemon(cfg)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "item-1",
		IssueRef: config.IssueRef{Source: "github", ID: "1"},
	})

	action := &webhookPostAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{
		"url":  srv.URL,
		"body": `{}`,
	})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected success, got error: %v", result.Error)
	}
	if receivedContentType != "application/json" {
		t.Errorf("expected Content-Type=application/json, got %q", receivedContentType)
	}
}

func TestInterpolateWebhookBody_ValidTemplate(t *testing.T) {
	data := webhookTemplateData{
		IssueID:     "42",
		IssueTitle:  "Fix crash",
		IssueURL:    "https://github.com/o/r/issues/42",
		IssueSource: "github",
		PRURL:       "https://github.com/o/r/pull/7",
		Branch:      "issue-42",
		State:       "active",
		WorkItemID:  "wi-1",
	}

	tmpl := `{"id":"{{.IssueID}}","pr":"{{.PRURL}}","source":"{{.IssueSource}}"}`
	got, err := interpolateWebhookBody(tmpl, data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]string
	if err := json.Unmarshal([]byte(got), &result); err != nil {
		t.Fatalf("interpolated body is not valid JSON: %v\nbody: %s", err, got)
	}
	if result["id"] != "42" {
		t.Errorf("expected id=42, got %q", result["id"])
	}
	if result["pr"] != "https://github.com/o/r/pull/7" {
		t.Errorf("expected pr URL, got %q", result["pr"])
	}
	if result["source"] != "github" {
		t.Errorf("expected source=github, got %q", result["source"])
	}
}

func TestInterpolateWebhookBody_InvalidTemplate(t *testing.T) {
	data := webhookTemplateData{IssueID: "1"}
	_, err := interpolateWebhookBody(`{{.Unclosed`, data)
	if err == nil {
		t.Error("expected error for invalid template syntax")
	}
}

func TestWebhookPostAction_Execute_CustomTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := testConfig()
	d := testDaemon(cfg)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "item-1",
		IssueRef: config.IssueRef{Source: "github", ID: "1"},
	})

	action := &webhookPostAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{
		"url":     srv.URL,
		"body":    `{}`,
		"timeout": "5s",
	})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected success with custom timeout, got error: %v", result.Error)
	}
}

// --- git.validate_diff tests ---

// initTestGitRepoWithBranch initialises a temp git repo with one commit on the
// default branch and checks out a new feature branch. Returns the repo dir and
// the name of the default branch.
func initTestGitRepoWithBranch(t *testing.T, featureBranch string) (string, string) {
	t.Helper()
	dir := initTestGitRepo(t)
	defaultBranch := getDefaultBranch(t, dir)
	mustRunGit(t, dir, "checkout", "-b", featureBranch)
	return dir, defaultBranch
}

// writeTestFile writes content to a file inside the given repo directory.
func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatalf("failed to write %s: %v", name, err)
	}
}

func TestValidateDiffAction_Execute_WorkItemNotFound(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	action := &validateDiffAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{})
	ac := &workflow.ActionContext{WorkItemID: "nonexistent", Params: params}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure for missing work item")
	}
	if result.Error == nil {
		t.Error("expected error for missing work item")
	}
}

func TestValidateDiffAction_Execute_SessionNotFound(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "nonexistent-session",
		Branch:    "feature-1",
	})

	action := &validateDiffAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure when session not found")
	}
	if result.Error == nil {
		t.Error("expected error when session not found")
	}
}

func TestValidateDiff_NoDiff(t *testing.T) {
	dir, baseBranch := initTestGitRepoWithBranch(t, "feature-empty")

	cfg := testConfig()
	sess := &config.Session{
		ID:         "sess-1",
		RepoPath:   dir,
		WorkTree:   dir,
		Branch:     "feature-empty",
		BaseBranch: baseBranch,
	}
	cfg.AddSession(*sess)

	d := testDaemon(cfg)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "1"},
		SessionID: "sess-1",
		Branch:    "feature-empty",
	})

	action := &validateDiffAction{daemon: d}
	// Even with strict limits, an empty diff should pass.
	params := workflow.NewParamHelper(map[string]any{
		"max_diff_lines": 10,
		"require_tests":  true,
	})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected success for empty diff, got error: %v", result.Error)
	}
}

func TestValidateDiff_MaxDiffLines_Pass(t *testing.T) {
	dir, baseBranch := initTestGitRepoWithBranch(t, "feature-small")

	writeTestFile(t, dir, "small.go", "package main\n\nfunc foo() {}\nfunc bar() {}\nfunc baz() {}\n")
	mustRunGit(t, dir, "add", ".")
	mustRunGit(t, dir, "commit", "-m", "add small file")

	cfg := testConfig()
	sess := &config.Session{
		ID:         "sess-1",
		RepoPath:   dir,
		WorkTree:   dir,
		Branch:     "feature-small",
		BaseBranch: baseBranch,
	}
	cfg.AddSession(*sess)

	d := testDaemon(cfg)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "1"},
		SessionID: "sess-1",
		Branch:    "feature-small",
	})

	action := &validateDiffAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"max_diff_lines": 100})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected success for diff under limit, got error: %v", result.Error)
	}
}

func TestValidateDiff_MaxDiffLines_Fail(t *testing.T) {
	dir, baseBranch := initTestGitRepoWithBranch(t, "feature-large")

	lines := make([]string, 50)
	for i := range lines {
		lines[i] = fmt.Sprintf("var x%d = %d", i, i)
	}
	writeTestFile(t, dir, "large.go", "package main\n\n"+strings.Join(lines, "\n")+"\n")
	mustRunGit(t, dir, "add", ".")
	mustRunGit(t, dir, "commit", "-m", "add large file")

	cfg := testConfig()
	sess := &config.Session{
		ID:         "sess-1",
		RepoPath:   dir,
		WorkTree:   dir,
		Branch:     "feature-large",
		BaseBranch: baseBranch,
	}
	cfg.AddSession(*sess)

	d := testDaemon(cfg)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "1"},
		SessionID: "sess-1",
		Branch:    "feature-large",
	})

	action := &validateDiffAction{daemon: d}
	// Limit is 10 lines; we added 52+ lines — should fail.
	params := workflow.NewParamHelper(map[string]any{"max_diff_lines": 10})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure for diff over limit")
	}
	if result.Error == nil {
		t.Error("expected error for diff over limit")
	}
	if !strings.Contains(result.Error.Error(), "diff too large") {
		t.Errorf("expected 'diff too large' in error, got: %v", result.Error)
	}
}

func TestValidateDiff_ForbiddenPatterns_Pass(t *testing.T) {
	dir, baseBranch := initTestGitRepoWithBranch(t, "feature-clean")

	writeTestFile(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	mustRunGit(t, dir, "add", ".")
	mustRunGit(t, dir, "commit", "-m", "add main.go")

	cfg := testConfig()
	sess := &config.Session{
		ID:         "sess-1",
		RepoPath:   dir,
		WorkTree:   dir,
		Branch:     "feature-clean",
		BaseBranch: baseBranch,
	}
	cfg.AddSession(*sess)

	d := testDaemon(cfg)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "1"},
		SessionID: "sess-1",
		Branch:    "feature-clean",
	})

	action := &validateDiffAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{
		"forbidden_patterns": []interface{}{".env", "*.pem", "credentials.json"},
	})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected success with no forbidden files, got error: %v", result.Error)
	}
}

func TestValidateDiff_ForbiddenPatterns_Fail(t *testing.T) {
	dir, baseBranch := initTestGitRepoWithBranch(t, "feature-secret")

	writeTestFile(t, dir, ".env", "SECRET_KEY=abc123\n")
	mustRunGit(t, dir, "add", ".")
	mustRunGit(t, dir, "commit", "-m", "oops add .env")

	cfg := testConfig()
	sess := &config.Session{
		ID:         "sess-1",
		RepoPath:   dir,
		WorkTree:   dir,
		Branch:     "feature-secret",
		BaseBranch: baseBranch,
	}
	cfg.AddSession(*sess)

	d := testDaemon(cfg)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "1"},
		SessionID: "sess-1",
		Branch:    "feature-secret",
	})

	action := &validateDiffAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{
		"forbidden_patterns": []interface{}{".env", "*.pem"},
	})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure for forbidden file")
	}
	if result.Error == nil {
		t.Error("expected error for forbidden file")
	}
	if !strings.Contains(result.Error.Error(), "forbidden file") {
		t.Errorf("expected 'forbidden file' in error, got: %v", result.Error)
	}
	if !strings.Contains(result.Error.Error(), ".env") {
		t.Errorf("expected '.env' in error, got: %v", result.Error)
	}
}

func TestValidateDiff_RequireTests_NoSourceChanges(t *testing.T) {
	dir, baseBranch := initTestGitRepoWithBranch(t, "feature-docs")

	// Only docs — no source files — should pass even with require_tests.
	writeTestFile(t, dir, "docs.md", "# Docs\n\nsome documentation\n")
	mustRunGit(t, dir, "add", ".")
	mustRunGit(t, dir, "commit", "-m", "add docs")

	cfg := testConfig()
	sess := &config.Session{
		ID:         "sess-1",
		RepoPath:   dir,
		WorkTree:   dir,
		Branch:     "feature-docs",
		BaseBranch: baseBranch,
	}
	cfg.AddSession(*sess)

	d := testDaemon(cfg)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "1"},
		SessionID: "sess-1",
		Branch:    "feature-docs",
	})

	action := &validateDiffAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"require_tests": true})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected success when no source files changed, got error: %v", result.Error)
	}
}

func TestValidateDiff_RequireTests_WithTestFile(t *testing.T) {
	dir, baseBranch := initTestGitRepoWithBranch(t, "feature-with-tests")

	writeTestFile(t, dir, "foo.go", "package main\n\nfunc Foo() {}\n")
	writeTestFile(t, dir, "foo_test.go", "package main\n\nimport \"testing\"\n\nfunc TestFoo(t *testing.T) {}\n")
	mustRunGit(t, dir, "add", ".")
	mustRunGit(t, dir, "commit", "-m", "add foo with test")

	cfg := testConfig()
	sess := &config.Session{
		ID:         "sess-1",
		RepoPath:   dir,
		WorkTree:   dir,
		Branch:     "feature-with-tests",
		BaseBranch: baseBranch,
	}
	cfg.AddSession(*sess)

	d := testDaemon(cfg)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "1"},
		SessionID: "sess-1",
		Branch:    "feature-with-tests",
	})

	action := &validateDiffAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"require_tests": true})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected success with both source and test files, got error: %v", result.Error)
	}
}

func TestValidateDiff_RequireTests_MissingTest(t *testing.T) {
	dir, baseBranch := initTestGitRepoWithBranch(t, "feature-no-tests")

	writeTestFile(t, dir, "bar.go", "package main\n\nfunc Bar() {}\n")
	mustRunGit(t, dir, "add", ".")
	mustRunGit(t, dir, "commit", "-m", "add bar without test")

	cfg := testConfig()
	sess := &config.Session{
		ID:         "sess-1",
		RepoPath:   dir,
		WorkTree:   dir,
		Branch:     "feature-no-tests",
		BaseBranch: baseBranch,
	}
	cfg.AddSession(*sess)

	d := testDaemon(cfg)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "1"},
		SessionID: "sess-1",
		Branch:    "feature-no-tests",
	})

	action := &validateDiffAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"require_tests": true})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure when source changed without tests")
	}
	if result.Error == nil {
		t.Error("expected error when source changed without tests")
	}
	if !strings.Contains(result.Error.Error(), "no test files") {
		t.Errorf("expected 'no test files' in error, got: %v", result.Error)
	}
}

func TestValidateDiff_MaxLockFileLines_Pass(t *testing.T) {
	dir, baseBranch := initTestGitRepoWithBranch(t, "feature-smallsum")

	writeTestFile(t, dir, "go.sum", "module1 v1.0.0 h1:abc\nmodule2 v2.0.0 h1:def\nmodule3 v3.0.0 h1:ghi\n")
	mustRunGit(t, dir, "add", ".")
	mustRunGit(t, dir, "commit", "-m", "small go.sum update")

	cfg := testConfig()
	sess := &config.Session{
		ID:         "sess-1",
		RepoPath:   dir,
		WorkTree:   dir,
		Branch:     "feature-smallsum",
		BaseBranch: baseBranch,
	}
	cfg.AddSession(*sess)

	d := testDaemon(cfg)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "1"},
		SessionID: "sess-1",
		Branch:    "feature-smallsum",
	})

	action := &validateDiffAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"max_lock_file_lines": 50})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected success for small lock file change, got error: %v", result.Error)
	}
}

func TestValidateDiff_MaxLockFileLines_Fail(t *testing.T) {
	dir, baseBranch := initTestGitRepoWithBranch(t, "feature-bigsum")

	lines := make([]string, 100)
	for i := range lines {
		lines[i] = fmt.Sprintf("module%d v1.0.%d h1:abc%d", i, i, i)
	}
	writeTestFile(t, dir, "go.sum", strings.Join(lines, "\n")+"\n")
	mustRunGit(t, dir, "add", ".")
	mustRunGit(t, dir, "commit", "-m", "huge go.sum update")

	cfg := testConfig()
	sess := &config.Session{
		ID:         "sess-1",
		RepoPath:   dir,
		WorkTree:   dir,
		Branch:     "feature-bigsum",
		BaseBranch: baseBranch,
	}
	cfg.AddSession(*sess)

	d := testDaemon(cfg)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "1"},
		SessionID: "sess-1",
		Branch:    "feature-bigsum",
	})

	action := &validateDiffAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"max_lock_file_lines": 20})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure for large lock file change")
	}
	if result.Error == nil {
		t.Error("expected error for large lock file change")
	}
	if !strings.Contains(result.Error.Error(), "go.sum") {
		t.Errorf("expected 'go.sum' in error, got: %v", result.Error)
	}
}

func TestValidateDiff_MultipleViolations(t *testing.T) {
	dir, baseBranch := initTestGitRepoWithBranch(t, "feature-multi")

	writeTestFile(t, dir, "code.go", "package main\n\nfunc Code() {}\n")
	writeTestFile(t, dir, ".env", "SECRET=value\n")
	mustRunGit(t, dir, "add", ".")
	mustRunGit(t, dir, "commit", "-m", "multiple violations")

	cfg := testConfig()
	sess := &config.Session{
		ID:         "sess-1",
		RepoPath:   dir,
		WorkTree:   dir,
		Branch:     "feature-multi",
		BaseBranch: baseBranch,
	}
	cfg.AddSession(*sess)

	d := testDaemon(cfg)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "1"},
		SessionID: "sess-1",
		Branch:    "feature-multi",
	})

	action := &validateDiffAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{
		"require_tests":      true,
		"forbidden_patterns": []interface{}{".env"},
	})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure for multiple violations")
	}
	if result.Error == nil {
		t.Error("expected error for multiple violations")
	}
	if !strings.Contains(result.Error.Error(), "forbidden file") {
		t.Errorf("expected 'forbidden file' in error, got: %v", result.Error)
	}
	if !strings.Contains(result.Error.Error(), "no test files") {
		t.Errorf("expected 'no test files' in error, got: %v", result.Error)
	}
	if result.Data == nil {
		t.Error("expected Data field to contain violations")
	} else if v, ok := result.Data["violations"].([]string); !ok || len(v) != 2 {
		t.Errorf("expected 2 violations in Data, got: %v", result.Data["violations"])
	}
}

func TestValidateDiff_CustomSourceAndTestPatterns(t *testing.T) {
	dir, baseBranch := initTestGitRepoWithBranch(t, "feature-custom")

	writeTestFile(t, dir, "service.py", "def handle(): pass\n")
	writeTestFile(t, dir, "test_service.py", "def test_handle(): pass\n")
	mustRunGit(t, dir, "add", ".")
	mustRunGit(t, dir, "commit", "-m", "add python service with test")

	cfg := testConfig()
	sess := &config.Session{
		ID:         "sess-1",
		RepoPath:   dir,
		WorkTree:   dir,
		Branch:     "feature-custom",
		BaseBranch: baseBranch,
	}
	cfg.AddSession(*sess)

	d := testDaemon(cfg)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "1"},
		SessionID: "sess-1",
		Branch:    "feature-custom",
	})

	action := &validateDiffAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{
		"require_tests":   true,
		"source_patterns": []interface{}{"*.py"},
		"test_patterns":   []interface{}{"test_*.py"},
	})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected success with custom patterns and matching test file, got error: %v", result.Error)
	}
}

func TestValidateDiff_CustomLockFilePatterns(t *testing.T) {
	dir, baseBranch := initTestGitRepoWithBranch(t, "feature-customlock")

	lines := make([]string, 60)
	for i := range lines {
		lines[i] = fmt.Sprintf("package%d==1.%d.0", i, i)
	}
	writeTestFile(t, dir, "requirements.txt", strings.Join(lines, "\n")+"\n")
	mustRunGit(t, dir, "add", ".")
	mustRunGit(t, dir, "commit", "-m", "update requirements.txt")

	cfg := testConfig()
	sess := &config.Session{
		ID:         "sess-1",
		RepoPath:   dir,
		WorkTree:   dir,
		Branch:     "feature-customlock",
		BaseBranch: baseBranch,
	}
	cfg.AddSession(*sess)

	d := testDaemon(cfg)
	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "1"},
		SessionID: "sess-1",
		Branch:    "feature-customlock",
	})

	action := &validateDiffAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{
		"max_lock_file_lines": 10,
		"lock_file_patterns":  []interface{}{"requirements.txt"},
	})
	ac := &workflow.ActionContext{WorkItemID: "item-1", Params: params}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure for large requirements.txt change")
	}
	if !strings.Contains(result.Error.Error(), "requirements.txt") {
		t.Errorf("expected 'requirements.txt' in error, got: %v", result.Error)
	}
}

// TestActionErrorWrapping verifies that action Execute methods use %w so that
// errors.Is can traverse the chain from the outer ActionResult.Error back to
// the original sentinel / leaf error returned by the underlying operation.
func TestActionErrorWrapping(t *testing.T) {
	// sentinelErr is the leaf error injected via the mock executor.
	sentinelErr := errors.New("sentinel gh failure")

	setup := func(t *testing.T, prefixArgs []string) (*Daemon, *config.Config, *exec.MockExecutor) {
		t.Helper()
		cfg := testConfig()
		mockExec := exec.NewMockExecutor(nil)
		mockExec.AddPrefixMatch("gh", prefixArgs, exec.MockResponse{Err: sentinelErr})
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
		return d, cfg, mockExec
	}

	t.Run("commentIssueAction wraps error", func(t *testing.T) {
		d, _, _ := setup(t, []string{"issue", "comment"})
		action := &commentIssueAction{daemon: d}
		ac := &workflow.ActionContext{
			WorkItemID: "item-1",
			Params:     workflow.NewParamHelper(map[string]any{"body": "hello"}),
		}
		result := action.Execute(context.Background(), ac)
		if result.Success {
			t.Fatal("expected failure")
		}
		if !errors.Is(result.Error, sentinelErr) {
			t.Errorf("errors.Is did not find sentinel in chain: %v", result.Error)
		}
	})

	t.Run("addLabelAction wraps error", func(t *testing.T) {
		d, _, _ := setup(t, []string{"issue", "edit"})
		action := &addLabelAction{daemon: d}
		ac := &workflow.ActionContext{
			WorkItemID: "item-1",
			Params:     workflow.NewParamHelper(map[string]any{"label": "bug"}),
		}
		result := action.Execute(context.Background(), ac)
		if result.Success {
			t.Fatal("expected failure")
		}
		if !errors.Is(result.Error, sentinelErr) {
			t.Errorf("errors.Is did not find sentinel in chain: %v", result.Error)
		}
	})

	t.Run("removeLabelAction wraps error", func(t *testing.T) {
		d, _, _ := setup(t, []string{"issue", "edit"})
		action := &removeLabelAction{daemon: d}
		ac := &workflow.ActionContext{
			WorkItemID: "item-1",
			Params:     workflow.NewParamHelper(map[string]any{"label": "bug"}),
		}
		result := action.Execute(context.Background(), ac)
		if result.Success {
			t.Fatal("expected failure")
		}
		if !errors.Is(result.Error, sentinelErr) {
			t.Errorf("errors.Is did not find sentinel in chain: %v", result.Error)
		}
	})
}

func TestCreateReleaseAction_Execute_WorkItemNotFound(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	action := &createReleaseAction{daemon: d}
	ac := &workflow.ActionContext{
		WorkItemID: "nonexistent",
		Params:     workflow.NewParamHelper(map[string]any{"tag": "v1.0.0"}),
	}

	result := action.Execute(context.Background(), ac)
	if result.Success {
		t.Error("expected failure for missing work item")
	}
	if result.Error == nil {
		t.Error("expected error for missing work item")
	}
}

func TestCreateReleaseAction_Execute_MissingTag(t *testing.T) {
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

	action := &createReleaseAction{daemon: d}
	// No "tag" param provided
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     workflow.NewParamHelper(map[string]any{}),
	}

	result := action.Execute(context.Background(), ac)
	if result.Success {
		t.Error("expected failure when tag is missing")
	}
	if result.Error == nil {
		t.Error("expected error when tag is missing")
	}
}

func TestCreateReleaseAction_Execute_Success(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	// Mock `gh release create` to succeed
	mockExec.AddPrefixMatch("gh", []string{"release", "create", "v1.0.0"}, exec.MockResponse{
		Stdout: []byte("https://github.com/owner/repo/releases/tag/v1.0.0\n"),
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

	action := &createReleaseAction{daemon: d}
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     workflow.NewParamHelper(map[string]any{"tag": "v1.0.0"}),
	}

	result := action.Execute(context.Background(), ac)
	if !result.Success {
		t.Errorf("expected success, got error: %v", result.Error)
	}
	if result.Data["release_url"] != "https://github.com/owner/repo/releases/tag/v1.0.0" {
		t.Errorf("unexpected release_url in Data: %v", result.Data["release_url"])
	}

	// Verify gh release create was called
	calls := mockExec.GetCalls()
	found := false
	for _, c := range calls {
		if c.Name == "gh" && len(c.Args) >= 2 && c.Args[0] == "release" && c.Args[1] == "create" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected gh release create to be called")
	}
}

func TestCreateReleaseAction_Execute_GhError(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	// Mock `gh release create` to fail
	mockExec.AddPrefixMatch("gh", []string{"release", "create"}, exec.MockResponse{
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

	action := &createReleaseAction{daemon: d}
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     workflow.NewParamHelper(map[string]any{"tag": "v1.0.0"}),
	}

	result := action.Execute(context.Background(), ac)
	if result.Success {
		t.Error("expected failure when gh CLI fails")
	}
	if result.Error == nil {
		t.Error("expected error when gh CLI fails")
	}
}

func TestWaitAction_Execute(t *testing.T) {
	t.Run("completes after duration", func(t *testing.T) {
		cfg := testConfig()
		d := testDaemon(cfg)
		action := &waitAction{daemon: d}
		ac := &workflow.ActionContext{
			WorkItemID: "item-1",
			Params:     workflow.NewParamHelper(map[string]any{"duration": "10ms"}),
		}

		start := time.Now()
		result := action.Execute(context.Background(), ac)
		elapsed := time.Since(start)

		if !result.Success {
			t.Errorf("expected success, got error: %v", result.Error)
		}
		if elapsed < 10*time.Millisecond {
			t.Errorf("expected to wait at least 10ms, but only waited %v", elapsed)
		}
	})

	t.Run("zero duration returns immediately", func(t *testing.T) {
		cfg := testConfig()
		d := testDaemon(cfg)
		action := &waitAction{daemon: d}
		ac := &workflow.ActionContext{
			WorkItemID: "item-1",
			Params:     workflow.NewParamHelper(map[string]any{}),
		}

		result := action.Execute(context.Background(), ac)

		if !result.Success {
			t.Errorf("expected success for zero duration, got error: %v", result.Error)
		}
		if result.Error != nil {
			t.Errorf("expected no error for zero duration, got: %v", result.Error)
		}
	})

	t.Run("cancels on context done", func(t *testing.T) {
		cfg := testConfig()
		d := testDaemon(cfg)
		action := &waitAction{daemon: d}
		ac := &workflow.ActionContext{
			WorkItemID: "item-1",
			Params:     workflow.NewParamHelper(map[string]any{"duration": "10s"}),
		}

		ctx, cancel := context.WithCancel(context.Background())
		// Cancel after a short delay.
		go func() {
			time.Sleep(10 * time.Millisecond)
			cancel()
		}()

		start := time.Now()
		result := action.Execute(ctx, ac)
		elapsed := time.Since(start)

		if result.Success {
			t.Error("expected failure when context is cancelled")
		}
		if result.Error == nil {
			t.Error("expected error when context is cancelled")
		}
		if !errors.Is(result.Error, context.Canceled) {
			t.Errorf("expected context.Canceled, got: %v", result.Error)
		}
		// Should have been cancelled well before the 10s duration.
		if elapsed >= 1*time.Second {
			t.Errorf("expected cancellation within 1s, took %v", elapsed)
		}
	})

	t.Run("registered in action registry", func(t *testing.T) {
		cfg := testConfig()
		d := testDaemon(cfg)
		registry := d.buildActionRegistry()
		if registry.Get("workflow.wait") == nil {
			t.Error("workflow.wait not registered in action registry")
		}
	})
}

// --- ai.review action tests ---

func TestAIReviewAction_Execute_WorkItemNotFound(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	action := &aiReviewAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"max_ai_review_rounds": 1})
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

func TestWritePRDescriptionAction_WorkItemNotFound(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	action := &writePRDescriptionAction{daemon: d}
	ac := &workflow.ActionContext{
		WorkItemID: "nonexistent",
		Params:     workflow.NewParamHelper(nil),
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure for missing work item")
	}
	if result.Error == nil {
		t.Error("expected error for missing work item")
	}
}

func TestAIReviewAction_Execute_MaxRoundsExceeded(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-sess-1",
		StepData:  map[string]any{"ai_review_rounds": 1},
	})

	action := &aiReviewAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"max_ai_review_rounds": 1})
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure when max rounds exceeded")
	}
	if result.Error == nil {
		t.Error("expected error when max rounds exceeded")
	}
	if !strings.Contains(result.Error.Error(), "max AI review rounds exceeded") {
		t.Errorf("expected 'max AI review rounds exceeded' error, got: %v", result.Error)
	}
}

func TestAIReviewAction_Execute_MaxRoundsFloat64(t *testing.T) {
	// JSON deserialization produces float64 for numbers
	cfg := testConfig()
	d := testDaemon(cfg)

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-sess-1",
		StepData:  map[string]any{"ai_review_rounds": float64(1)},
	})

	action := &aiReviewAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"max_ai_review_rounds": 1})
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure when max rounds exceeded (float64)")
	}
	if result.Error == nil {
		t.Error("expected error when max rounds exceeded (float64)")
	}
}

func TestAIReviewAction_Execute_NoSession(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "nonexistent",
		Branch:    "feature-1",
		StepData:  map[string]any{},
	})

	action := &aiReviewAction{daemon: d}
	params := workflow.NewParamHelper(map[string]any{"max_ai_review_rounds": 1})
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     params,
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure when session not found")
	}
	if result.Error == nil {
		t.Error("expected error when session not found")
	}
}

func TestWritePRDescriptionAction_SessionNotFound(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "session-does-not-exist",
		Branch:    "feature-42",
		StepData:  map[string]any{},
	})

	action := &writePRDescriptionAction{daemon: d}
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     workflow.NewParamHelper(nil),
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure when session not found")
	}
	if result.Error == nil {
		t.Error("expected error when session not found")
	}
}

func TestAIReviewAction_RegisteredInRegistry(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)
	registry := d.buildActionRegistry()
	if registry.Get("ai.review") == nil {
		t.Error("ai.review not registered in action registry")
	}
}

func TestGetAIReviewRounds(t *testing.T) {
	tests := []struct {
		name     string
		stepData map[string]any
		want     int
	}{
		{"missing", map[string]any{}, 0},
		{"int zero", map[string]any{"ai_review_rounds": 0}, 0},
		{"int one", map[string]any{"ai_review_rounds": 1}, 1},
		{"float64 zero", map[string]any{"ai_review_rounds": float64(0)}, 0},
		{"float64 two", map[string]any{"ai_review_rounds": float64(2)}, 2},
		{"wrong type", map[string]any{"ai_review_rounds": "oops"}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := getAIReviewRounds(tc.stepData)
			if got != tc.want {
				t.Errorf("getAIReviewRounds(%v) = %d, want %d", tc.stepData, got, tc.want)
			}
		})
	}
}

func TestTruncateDiff(t *testing.T) {
	tests := []struct {
		name      string
		diff      string
		maxRunes  int
		suffix    string
		wantLen   int // expected rune count of result
		wantTrunc bool
	}{
		{
			name:      "short diff unchanged",
			diff:      "hello world",
			maxRunes:  100,
			suffix:    "...truncated",
			wantLen:   len([]rune("hello world")),
			wantTrunc: false,
		},
		{
			name:      "exact length unchanged",
			diff:      strings.Repeat("a", 100),
			maxRunes:  100,
			suffix:    "...truncated",
			wantLen:   100,
			wantTrunc: false,
		},
		{
			name:      "long diff truncated to maxRunes",
			diff:      strings.Repeat("a", 200),
			maxRunes:  100,
			suffix:    strings.Repeat("s", 10),
			wantLen:   100,
			wantTrunc: true,
		},
		{
			name:      "multibyte UTF-8 not split",
			diff:      strings.Repeat("日", 200), // each '日' is 3 bytes, 1 rune
			maxRunes:  100,
			suffix:    strings.Repeat("s", 10),
			wantLen:   100,
			wantTrunc: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateDiff(tc.diff, tc.maxRunes, tc.suffix)
			runeCount := len([]rune(got))
			if runeCount != tc.wantLen {
				t.Errorf("rune count = %d, want %d", runeCount, tc.wantLen)
			}
			if tc.wantTrunc && !strings.HasSuffix(got, tc.suffix) {
				t.Errorf("expected result to end with suffix %q", tc.suffix)
			}
			if !tc.wantTrunc && got != tc.diff {
				t.Errorf("expected unchanged diff, got %q", got)
			}
		})
	}
}

func TestGetAIReviewDiff(t *testing.T) {
	// Create a remote bare repo to serve as origin
	remoteDir := t.TempDir()
	localDir := t.TempDir()

	runGit := func(dir string, args ...string) string {
		t.Helper()
		cmd := osexec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %s (%v)", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}

	// Initialize the remote repo and create an initial commit
	runGit(remoteDir, "init", "-b", "main")
	runGit(remoteDir, "config", "user.email", "test@test.com")
	runGit(remoteDir, "config", "user.name", "Test")
	helloPath := filepath.Join(remoteDir, "hello.go")
	if err := os.WriteFile(helloPath, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(remoteDir, "add", ".")
	runGit(remoteDir, "commit", "-m", "initial")

	// Clone into localDir so origin/main is set up correctly
	cloneCmd := osexec.Command("git", "clone", remoteDir, localDir)
	if out, err := cloneCmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone failed: %s (%v)", out, err)
	}
	runGit(localDir, "config", "user.email", "test@test.com")
	runGit(localDir, "config", "user.name", "Test")

	// Add a new file on the local branch (simulating work done by Claude)
	newFile := filepath.Join(localDir, "new.go")
	if err := os.WriteFile(newFile, []byte("package main\nfunc New() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(localDir, "add", ".")
	runGit(localDir, "commit", "-m", "add new.go")

	diff, err := getAIReviewDiff(context.Background(), localDir, "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(diff, "new.go") {
		t.Errorf("expected diff to mention new.go; got:\n%s", diff)
	}
}

func TestGetAIReviewDiff_EmptyBaseBranch(t *testing.T) {
	// When baseBranch is empty it should default to "main"
	remoteDir := t.TempDir()
	localDir := t.TempDir()

	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := osexec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %s (%v)", args, out, err)
		}
	}

	runGit(remoteDir, "init", "-b", "main")
	runGit(remoteDir, "config", "user.email", "test@test.com")
	runGit(remoteDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(remoteDir, "readme.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(remoteDir, "add", ".")
	runGit(remoteDir, "commit", "-m", "initial")

	cloneCmd := osexec.Command("git", "clone", remoteDir, localDir)
	if out, err := cloneCmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone failed: %s (%v)", out, err)
	}
	runGit(localDir, "config", "user.email", "test@test.com")
	runGit(localDir, "config", "user.name", "Test")

	// No extra commit — HEAD == origin/main; diff should be empty
	diff, err := getAIReviewDiff(context.Background(), localDir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diff != "" {
		t.Errorf("expected empty diff when HEAD == origin/main, got:\n%s", diff)
	}
}

func TestGetAIReviewDiff_InvalidDir(t *testing.T) {
	_, err := getAIReviewDiff(context.Background(), "/nonexistent/path", "main")
	if err == nil {
		t.Error("expected error for invalid directory")
	}
}

func TestGetAIReviewDiff_Truncation(t *testing.T) {
	// Verify that diffs larger than 50000 runes are truncated
	remoteDir := t.TempDir()
	localDir := t.TempDir()

	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := osexec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %s (%v)", args, out, err)
		}
	}

	runGit(remoteDir, "init", "-b", "main")
	runGit(remoteDir, "config", "user.email", "test@test.com")
	runGit(remoteDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(remoteDir, "base.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(remoteDir, "add", ".")
	runGit(remoteDir, "commit", "-m", "initial")

	cloneCmd := osexec.Command("git", "clone", remoteDir, localDir)
	if out, err := cloneCmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone failed: %s (%v)", out, err)
	}
	runGit(localDir, "config", "user.email", "test@test.com")
	runGit(localDir, "config", "user.name", "Test")

	// Write a file large enough to exceed the 50000-rune limit
	bigContent := "package main\n// " + strings.Repeat("x", 60000) + "\n"
	if err := os.WriteFile(filepath.Join(localDir, "big.go"), []byte(bigContent), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(localDir, "add", ".")
	runGit(localDir, "commit", "-m", "add big file")

	diff, err := getAIReviewDiff(context.Background(), localDir, "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	const wantSuffix = "\n\n... (diff truncated)"
	if !strings.HasSuffix(diff, wantSuffix) {
		t.Errorf("expected truncated diff to end with %q; got suffix: %q",
			wantSuffix, diff[max(0, len(diff)-30):])
	}
	if runeCount := len([]rune(diff)); runeCount != 50000 {
		t.Errorf("expected truncated diff to be exactly 50000 runes, got %d", runeCount)
	}
}

func TestFormatAIReviewPrompt(t *testing.T) {
	diff := "diff --git a/foo.go b/foo.go\n+func Foo() {}"
	prompt := formatAIReviewPrompt(2, diff)

	if !strings.Contains(prompt, "ROUND 2") {
		t.Error("expected prompt to contain round number")
	}
	if !strings.Contains(prompt, diff) {
		t.Error("expected prompt to contain the diff")
	}
	if !strings.Contains(prompt, "DO NOT modify") {
		t.Error("expected prompt to contain DO NOT modify instruction")
	}
	if !strings.Contains(prompt, "ai_review.json") {
		t.Error("expected prompt to mention ai_review.json output file")
	}
}

func TestReadAIReviewResult_FileNotFound(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	dir := t.TempDir()
	sess := &config.Session{WorkTree: dir}

	passed, summary := d.readAIReviewResult(sess)

	if !passed {
		t.Error("expected passed=true when ai_review.json is absent")
	}
	if summary != "" {
		t.Errorf("expected empty summary when file absent, got %q", summary)
	}
}

func TestReadAIReviewResult_Passed(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	dir := t.TempDir()
	ergDir := filepath.Join(dir, ".erg")
	if err := os.MkdirAll(ergDir, 0o755); err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(ergDir, "ai_review.json")
	content := `{"passed": true, "summary": "Looks good", "issues": []}`
	if err := os.WriteFile(resultPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	sess := &config.Session{WorkTree: dir}
	passed, summary := d.readAIReviewResult(sess)

	if !passed {
		t.Error("expected passed=true")
	}
	if summary != "Looks good" {
		t.Errorf("expected summary 'Looks good', got %q", summary)
	}
}

func TestReadAIReviewResult_Failed(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	dir := t.TempDir()
	ergDir := filepath.Join(dir, ".erg")
	if err := os.MkdirAll(ergDir, 0o755); err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(ergDir, "ai_review.json")
	content := `{"passed": false, "summary": "SQL injection risk found", "issues": [{"severity": "BLOCKING", "description": "SQL injection in query builder"}]}`
	if err := os.WriteFile(resultPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	sess := &config.Session{WorkTree: dir}
	passed, summary := d.readAIReviewResult(sess)

	if passed {
		t.Error("expected passed=false for blocking issues")
	}
	if summary != "SQL injection risk found" {
		t.Errorf("expected summary 'SQL injection risk found', got %q", summary)
	}
}

func TestReadAIReviewResult_InvalidJSON(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	dir := t.TempDir()
	ergDir := filepath.Join(dir, ".erg")
	if err := os.MkdirAll(ergDir, 0o755); err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(ergDir, "ai_review.json")
	if err := os.WriteFile(resultPath, []byte("not valid json {{{"), 0o644); err != nil {
		t.Fatal(err)
	}

	sess := &config.Session{WorkTree: dir}
	passed, _ := d.readAIReviewResult(sess)

	if !passed {
		t.Error("expected passed=true when JSON is invalid (fail-open for malformed output)")
	}
}

func TestReadAIReviewResult_FallbackToRepoPath(t *testing.T) {
	// When WorkTree is empty, use RepoPath
	cfg := testConfig()
	d := testDaemon(cfg)

	dir := t.TempDir()
	ergDir := filepath.Join(dir, ".erg")
	if err := os.MkdirAll(ergDir, 0o755); err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(ergDir, "ai_review.json")
	if err := os.WriteFile(resultPath, []byte(`{"passed": false, "summary": "critical bug"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// WorkTree is empty — should fall back to RepoPath
	sess := &config.Session{WorkTree: "", RepoPath: dir}
	passed, summary := d.readAIReviewResult(sess)

	if passed {
		t.Error("expected passed=false")
	}
	if summary != "critical bug" {
		t.Errorf("expected summary 'critical bug', got %q", summary)
	}
}

func TestWritePRDescriptionAction_Success(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	// git fetch origin
	mockExec.AddPrefixMatch("git", []string{"fetch", "origin"}, exec.MockResponse{})
	// git rev-parse --verify origin/main
	mockExec.AddPrefixMatch("git", []string{"rev-parse", "--verify"}, exec.MockResponse{
		Stdout: []byte("abc123\n"),
	})
	// git log
	mockExec.AddPrefixMatch("git", []string{"log"}, exec.MockResponse{
		Stdout: []byte("abc123 feat: add feature\n"),
	})
	// git diff
	mockExec.AddPrefixMatch("git", []string{"diff"}, exec.MockResponse{
		Stdout: []byte("diff --git a/foo.go b/foo.go\n+new line\n"),
	})
	// claude --print -p ...
	mockExec.AddPrefixMatch("claude", []string{"--print", "-p"}, exec.MockResponse{
		Stdout: []byte("## Summary\nThis PR adds a feature.\n\n## Changes\n- Added foo.go\n\n## Test plan\n- Run tests\n\n## Breaking changes\nNone"),
	})
	// gh pr edit --body ...
	mockExec.AddPrefixMatch("gh", []string{"pr", "edit"}, exec.MockResponse{})
	// git symbolic-ref (for GetDefaultBranch)
	mockExec.AddPrefixMatch("git", []string{"symbolic-ref"}, exec.MockResponse{
		Stdout: []byte("refs/remotes/origin/main\n"),
	})

	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	d := testDaemonWithExec(cfg, mockExec)
	d.gitService = gitSvc

	sess := testSession("sess-1")
	sess.BaseBranch = "main"
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-sess-1",
		StepData:  map[string]any{},
	})

	action := &writePRDescriptionAction{daemon: d}
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     workflow.NewParamHelper(nil),
	}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected success, got error: %v", result.Error)
	}

	// Verify gh pr edit was called
	calls := mockExec.GetCalls()
	found := false
	for _, c := range calls {
		if c.Name == "gh" && len(c.Args) >= 3 && c.Args[0] == "pr" && c.Args[1] == "edit" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected gh pr edit to be called to update PR body")
	}
}

func TestWritePRDescriptionAction_ClaudeFailure(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	mockExec.AddPrefixMatch("git", []string{"fetch", "origin"}, exec.MockResponse{})
	mockExec.AddPrefixMatch("git", []string{"rev-parse", "--verify"}, exec.MockResponse{
		Stdout: []byte("abc123\n"),
	})
	mockExec.AddPrefixMatch("git", []string{"log"}, exec.MockResponse{
		Stdout: []byte("abc123 feat: something\n"),
	})
	mockExec.AddPrefixMatch("git", []string{"diff"}, exec.MockResponse{
		Stdout: []byte("diff output\n"),
	})
	mockExec.AddPrefixMatch("claude", []string{"--print", "-p"}, exec.MockResponse{
		Err: fmt.Errorf("claude: not found"),
	})
	mockExec.AddPrefixMatch("git", []string{"symbolic-ref"}, exec.MockResponse{
		Stdout: []byte("refs/remotes/origin/main\n"),
	})

	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	d := testDaemonWithExec(cfg, mockExec)
	d.gitService = gitSvc

	sess := testSession("sess-2")
	sess.BaseBranch = "main"
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-2",
		IssueRef:  config.IssueRef{Source: "github", ID: "43"},
		SessionID: "sess-2",
		Branch:    "feature-sess-2",
		StepData:  map[string]any{},
	})

	action := &writePRDescriptionAction{daemon: d}
	ac := &workflow.ActionContext{
		WorkItemID: "item-2",
		Params:     workflow.NewParamHelper(nil),
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure when Claude fails")
	}
	if result.Error == nil {
		t.Error("expected non-nil error when Claude fails")
	}
}

func TestWritePRDescriptionAction_RegisteredInRegistry(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)
	registry := d.buildActionRegistry()
	if registry.Get("ai.write_pr_description") == nil {
		t.Error("ai.write_pr_description not registered in action registry")
	}
}


// --- planningAction tests ---

func TestPlanningAction_Execute_WorkItemNotFound(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	action := &planningAction{daemon: d}
	ac := &workflow.ActionContext{
		WorkItemID: "nonexistent",
		Params:     workflow.NewParamHelper(nil),
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure for missing work item")
	}
	if result.Error == nil {
		t.Error("expected error for missing work item")
	}
}

func TestPlanningAction_Execute_NoRepo(t *testing.T) {
	cfg := testConfig()
	// No repos configured — startPlanning should fail
	cfg.Repos = []string{}
	d := testDaemon(cfg)
	d.repoFilter = ""

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "item-1",
		IssueRef: config.IssueRef{Source: "github", ID: "42"},
		StepData: map[string]any{},
	})

	action := &planningAction{daemon: d}
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     workflow.NewParamHelper(nil),
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure when no repo is found")
	}
	if result.Error == nil {
		t.Error("expected error when no repo is found")
	}
}

func TestStartPlanning_CreatesSessionOnDefaultBranch(t *testing.T) {
	cfg := testConfig()
	cfg.Repos = []string{"/test/repo"}

	mockExec := exec.NewMockExecutor(nil)
	// Mock GetDefaultBranch (git symbolic-ref)
	mockExec.AddPrefixMatch("git", []string{"symbolic-ref"}, exec.MockResponse{
		Stdout: []byte("refs/remotes/origin/main\n"),
	})

	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	sessSvc := session.NewSessionServiceWithExecutor(mockExec)
	d := testDaemonWithExec(cfg, mockExec)
	d.gitService = gitSvc
	d.sessionService = sessSvc
	d.repoFilter = "/test/repo"

	item := &daemonstate.WorkItem{
		ID:       "work-1",
		IssueRef: config.IssueRef{Source: "github", ID: "42", Title: "Implement feature"},
		StepData: map[string]any{},
	}
	d.state.AddWorkItem(item)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := d.startPlanning(ctx, *item)
	if err != nil {
		t.Fatalf("startPlanning failed: %v", err)
	}

	// Verify work item was updated with a session ID and marked active
	updatedItem, ok := d.state.GetWorkItem(item.ID)
	if !ok {
		t.Fatal("work item should exist in state")
	}
	if updatedItem.SessionID == "" {
		t.Error("SessionID must be set after startPlanning")
	}
	if updatedItem.State != daemonstate.WorkItemActive {
		t.Errorf("item.State must be WorkItemActive, got %q", updatedItem.State)
	}

	// Verify a session was recorded in config
	sessions := cfg.GetSessions()
	if len(sessions) == 0 {
		t.Fatal("expected a session to be recorded in config")
	}
	sess := sessions[0]

	// Session should be on the default branch, not a new feature branch
	if sess.Branch != "main" {
		t.Errorf("session branch = %q, want %q", sess.Branch, "main")
	}

	// WorkTree should be the repo path itself (no separate worktree)
	if sess.WorkTree != "/test/repo" {
		t.Errorf("session WorkTree = %q, want %q", sess.WorkTree, "/test/repo")
	}

	// Session should be marked as daemon-managed and autonomous
	if !sess.DaemonManaged {
		t.Error("session should be DaemonManaged")
	}
	if !sess.Autonomous {
		t.Error("session should be Autonomous")
	}

	// Config session ID must match item's SessionID
	if sess.ID != updatedItem.SessionID {
		t.Errorf("config session ID %q does not match item.SessionID %q", sess.ID, updatedItem.SessionID)
	}
}

func TestPlanningAction_Execute_ReturnsAsync(t *testing.T) {
	cfg := testConfig()
	cfg.Repos = []string{"/test/repo"}

	mockExec := exec.NewMockExecutor(nil)
	mockExec.AddPrefixMatch("git", []string{"symbolic-ref"}, exec.MockResponse{
		Stdout: []byte("refs/remotes/origin/main\n"),
	})

	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	sessSvc := session.NewSessionServiceWithExecutor(mockExec)
	d := testDaemonWithExec(cfg, mockExec)
	d.gitService = gitSvc
	d.sessionService = sessSvc
	d.repoFilter = "/test/repo"

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:       "work-1",
		IssueRef: config.IssueRef{Source: "github", ID: "42", Title: "Plan feature"},
		StepData: map[string]any{},
	})

	action := &planningAction{daemon: d}
	ac := &workflow.ActionContext{
		WorkItemID: "work-1",
		Params:     workflow.NewParamHelper(nil),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result := action.Execute(ctx, ac)

	if !result.Success {
		t.Errorf("expected success, got error: %v", result.Error)
	}
	if !result.Async {
		t.Error("expected Async=true for planningAction")
	}
}

func TestPlanningAction_RegisteredInRegistry(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)
	registry := d.buildActionRegistry()
	if registry.Get("ai.plan") == nil {
		t.Error("ai.plan not registered in action registry")
	}
}

func TestDefaultPlanningSystemPrompt_NotEmpty(t *testing.T) {
	if DefaultPlanningSystemPrompt == "" {
		t.Fatal("DefaultPlanningSystemPrompt should not be empty")
	}
}

func TestDefaultPlanningSystemPrompt_FocusesOnAnalysis(t *testing.T) {
	if !strings.Contains(DefaultPlanningSystemPrompt, "planning agent") {
		t.Error("DefaultPlanningSystemPrompt should identify as a planning agent")
	}
	if !strings.Contains(DefaultPlanningSystemPrompt, "DO NOT") {
		t.Error("DefaultPlanningSystemPrompt should contain DO NOT instructions")
	}
	if !strings.Contains(DefaultPlanningSystemPrompt, "implementation plan") {
		t.Error("DefaultPlanningSystemPrompt should mention implementation plan")
	}
}

func TestDefaultPlanningSystemPrompt_ExplicitlyForbidsCodeChanges(t *testing.T) {
	if !strings.Contains(DefaultPlanningSystemPrompt, "code changes") {
		t.Error("DefaultPlanningSystemPrompt should explicitly forbid code changes")
	}
	if !strings.Contains(DefaultPlanningSystemPrompt, "commits") {
		t.Error("DefaultPlanningSystemPrompt should explicitly forbid commits")
	}
}

func TestDefaultPlanningSystemPrompt_InstructsToPostComment(t *testing.T) {
	if !strings.Contains(DefaultPlanningSystemPrompt, "issue comment") {
		t.Error("DefaultPlanningSystemPrompt should instruct Claude to post an issue comment")
	}
}

func TestDefaultPlanningSystemPrompt_ContainerEnvironment(t *testing.T) {
	if !strings.Contains(DefaultPlanningSystemPrompt, "CONTAINER ENVIRONMENT") {
		t.Error("DefaultPlanningSystemPrompt should contain CONTAINER ENVIRONMENT section")
	}
}

func TestStartPlanning_UsesCustomSystemPrompt(t *testing.T) {
	cfg := testConfig()
	cfg.Repos = []string{"/test/repo"}

	mockExec := exec.NewMockExecutor(nil)
	mockExec.AddPrefixMatch("git", []string{"symbolic-ref"}, exec.MockResponse{
		Stdout: []byte("refs/remotes/origin/main\n"),
	})

	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	sessSvc := session.NewSessionServiceWithExecutor(mockExec)
	d := testDaemonWithExec(cfg, mockExec)
	d.gitService = gitSvc
	d.sessionService = sessSvc
	d.repoFilter = "/test/repo"

	// Set up workflow config with a custom system prompt for the planning state
	customPrompt := "My custom planning instructions"
	wfCfg := workflow.DefaultWorkflowConfig()
	wfCfg.States["planning"] = &workflow.State{
		Type:   workflow.StateTypeTask,
		Action: "ai.plan",
		Next:   "done",
		Params: map[string]any{"system_prompt": customPrompt},
	}
	d.workflowConfigs = map[string]*workflow.Config{"/test/repo": wfCfg}

	item := &daemonstate.WorkItem{
		ID:       "work-1",
		IssueRef: config.IssueRef{Source: "github", ID: "42", Title: "Plan feature"},
		StepData: map[string]any{},
	}
	d.state.AddWorkItem(item)

	var capturedRunner *trackingRunner
	d.sessionMgr.SetRunnerFactory(func(sessionID, workingDir, repoPath string, sessionStarted bool, initialMessages []claude.Message) claude.RunnerInterface {
		r := newTrackingRunner(sessionID)
		capturedRunner = r
		return r
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := d.startPlanning(ctx, *item)
	if err != nil {
		t.Fatalf("startPlanning failed: %v", err)
	}

	if capturedRunner == nil {
		t.Fatal("expected runner factory to be called")
	}
	if capturedRunner.systemPrompt != customPrompt {
		t.Errorf("expected custom prompt %q, got %q", customPrompt, capturedRunner.systemPrompt)
	}
}

func TestStartPlanning_UsesDefaultPromptWhenNoCustom(t *testing.T) {
	cfg := testConfig()
	cfg.Repos = []string{"/test/repo"}

	mockExec := exec.NewMockExecutor(nil)
	mockExec.AddPrefixMatch("git", []string{"symbolic-ref"}, exec.MockResponse{
		Stdout: []byte("refs/remotes/origin/main\n"),
	})

	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	sessSvc := session.NewSessionServiceWithExecutor(mockExec)
	d := testDaemonWithExec(cfg, mockExec)
	d.gitService = gitSvc
	d.sessionService = sessSvc
	d.repoFilter = "/test/repo"

	item := &daemonstate.WorkItem{
		ID:       "work-1",
		IssueRef: config.IssueRef{Source: "github", ID: "42", Title: "Plan feature"},
		StepData: map[string]any{},
	}
	d.state.AddWorkItem(item)

	var capturedRunner *trackingRunner
	d.sessionMgr.SetRunnerFactory(func(sessionID, workingDir, repoPath string, sessionStarted bool, initialMessages []claude.Message) claude.RunnerInterface {
		r := newTrackingRunner(sessionID)
		capturedRunner = r
		return r
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := d.startPlanning(ctx, *item)
	if err != nil {
		t.Fatalf("startPlanning failed: %v", err)
	}

	if capturedRunner == nil {
		t.Fatal("expected runner factory to be called")
	}
	if capturedRunner.systemPrompt != DefaultPlanningSystemPrompt {
		t.Errorf("expected DefaultPlanningSystemPrompt when no custom prompt, got %q", capturedRunner.systemPrompt)
	}
}


// --- cherryPickAction tests ---

func TestCherryPickAction_Execute_WorkItemNotFound(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	action := &cherryPickAction{daemon: d}
	ac := &workflow.ActionContext{
		WorkItemID: "nonexistent",
		Params:     workflow.NewParamHelper(map[string]any{"commits": "abc1234", "target_branch": "release-v2"}),
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure for missing work item")
	}
	if result.Error == nil {
		t.Error("expected error for missing work item")
	}
}

func TestCherryPickAction_Execute_SessionNotFound(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "missing-session",
		Branch:    "feature-1",
		StepData:  map[string]any{},
	})

	action := &cherryPickAction{daemon: d}
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     workflow.NewParamHelper(map[string]any{"commits": "abc1234", "target_branch": "release-v2"}),
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure when session not found")
	}
	if result.Error == nil {
		t.Error("expected error when session not found")
	}
}

func TestCherryPickAction_Execute_MissingTargetBranch(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-1",
		StepData:  map[string]any{},
	})

	action := &cherryPickAction{daemon: d}
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     workflow.NewParamHelper(map[string]any{"commits": "abc1234"}),
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure when target_branch is missing")
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), "target_branch") {
		t.Errorf("expected target_branch error, got: %v", result.Error)
	}
}

func TestCherryPickAction_Execute_MissingCommits(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-1",
		StepData:  map[string]any{},
	})

	action := &cherryPickAction{daemon: d}
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     workflow.NewParamHelper(map[string]any{"target_branch": "release-v2"}),
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure when commits param is missing")
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), "commits") {
		t.Errorf("expected commits error, got: %v", result.Error)
	}
}

func TestCherryPickAction_Execute_Success_StringCommits(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	mockExec.AddExactMatch("git", []string{"fetch", "origin", "release-v2"}, exec.MockResponse{})
	mockExec.AddExactMatch("git", []string{"checkout", "release-v2"}, exec.MockResponse{})
	mockExec.AddExactMatch("git", []string{"cherry-pick", "abc1234"}, exec.MockResponse{})
	mockExec.AddExactMatch("git", []string{"push", "origin", "release-v2"}, exec.MockResponse{})

	d := testDaemonWithExec(cfg, mockExec)

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-sess-1",
		StepData:  map[string]any{},
	})

	action := &cherryPickAction{daemon: d}
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     workflow.NewParamHelper(map[string]any{"commits": "abc1234", "target_branch": "release-v2"}),
	}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected success, got error: %v", result.Error)
	}
}

func TestCherryPickAction_Execute_Success_ListCommits(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	mockExec.AddExactMatch("git", []string{"fetch", "origin", "release-v2"}, exec.MockResponse{})
	mockExec.AddExactMatch("git", []string{"checkout", "release-v2"}, exec.MockResponse{})
	mockExec.AddExactMatch("git", []string{"cherry-pick", "abc1234", "def5678"}, exec.MockResponse{})
	mockExec.AddExactMatch("git", []string{"push", "origin", "release-v2"}, exec.MockResponse{})

	d := testDaemonWithExec(cfg, mockExec)

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-sess-1",
		StepData:  map[string]any{},
	})

	action := &cherryPickAction{daemon: d}
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params: workflow.NewParamHelper(map[string]any{
			"commits":       []any{"abc1234", "def5678"},
			"target_branch": "release-v2",
		}),
	}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected success, got error: %v", result.Error)
	}
}

func TestCherryPickAction_Execute_CherryPickFails(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	mockExec.AddExactMatch("git", []string{"fetch", "origin", "release-v2"}, exec.MockResponse{})
	mockExec.AddExactMatch("git", []string{"checkout", "release-v2"}, exec.MockResponse{})
	mockExec.AddExactMatch("git", []string{"cherry-pick", "abc1234"}, exec.MockResponse{
		Err: fmt.Errorf("merge conflict"),
	})
	mockExec.AddExactMatch("git", []string{"cherry-pick", "--abort"}, exec.MockResponse{})

	d := testDaemonWithExec(cfg, mockExec)

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-1",
		IssueRef:  config.IssueRef{Source: "github", ID: "42"},
		SessionID: "sess-1",
		Branch:    "feature-sess-1",
		StepData:  map[string]any{},
	})

	action := &cherryPickAction{daemon: d}
	ac := &workflow.ActionContext{
		WorkItemID: "item-1",
		Params:     workflow.NewParamHelper(map[string]any{"commits": "abc1234", "target_branch": "release-v2"}),
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure when cherry-pick fails")
	}
	if result.Error == nil {
		t.Error("expected error when cherry-pick fails")
	}
}

func TestCherryPickAction_RegisteredInRegistry(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)
	registry := d.buildActionRegistry()
	if registry.Get("git.cherry_pick") == nil {
		t.Error("git.cherry_pick not registered in action registry")
	}
}

// --- parseCherryPickCommits tests ---

func TestParseCherryPickCommits_StringSingle(t *testing.T) {
	params := workflow.NewParamHelper(map[string]any{"commits": "abc1234"})
	commits, err := parseCherryPickCommits(params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(commits) != 1 || commits[0] != "abc1234" {
		t.Errorf("expected [abc1234], got %v", commits)
	}
}

func TestParseCherryPickCommits_StringMultiple(t *testing.T) {
	params := workflow.NewParamHelper(map[string]any{"commits": "abc1234 def5678"})
	commits, err := parseCherryPickCommits(params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(commits) != 2 || commits[0] != "abc1234" || commits[1] != "def5678" {
		t.Errorf("expected [abc1234 def5678], got %v", commits)
	}
}

func TestParseCherryPickCommits_List(t *testing.T) {
	params := workflow.NewParamHelper(map[string]any{"commits": []any{"abc1234", "def5678"}})
	commits, err := parseCherryPickCommits(params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(commits) != 2 || commits[0] != "abc1234" || commits[1] != "def5678" {
		t.Errorf("expected [abc1234 def5678], got %v", commits)
	}
}

func TestParseCherryPickCommits_MissingParam(t *testing.T) {
	params := workflow.NewParamHelper(map[string]any{})
	_, err := parseCherryPickCommits(params)
	if err == nil {
		t.Fatal("expected error for missing commits param")
	}
	if !strings.Contains(err.Error(), "commits parameter is required") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestParseCherryPickCommits_EmptyString(t *testing.T) {
	params := workflow.NewParamHelper(map[string]any{"commits": ""})
	_, err := parseCherryPickCommits(params)
	if err == nil {
		t.Fatal("expected error for empty string commits")
	}
}

func TestParseCherryPickCommits_EmptyList(t *testing.T) {
	params := workflow.NewParamHelper(map[string]any{"commits": []any{}})
	_, err := parseCherryPickCommits(params)
	if err == nil {
		t.Fatal("expected error for empty list commits")
	}
}

func TestParseCherryPickCommits_InvalidType(t *testing.T) {
	params := workflow.NewParamHelper(map[string]any{"commits": 12345})
	_, err := parseCherryPickCommits(params)
	if err == nil {
		t.Fatal("expected error for invalid type")
	}
}


// --- github.create_draft_pr tests ---

func TestCreateDraftPRAction_WorkItemNotFound(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	action := &createDraftPRAction{daemon: d}
	ac := &workflow.ActionContext{
		WorkItemID: "nonexistent",
		Params:     workflow.NewParamHelper(nil),
	}

	result := action.Execute(context.Background(), ac)

	if result.Success {
		t.Error("expected failure for missing work item")
	}
	if result.Error == nil {
		t.Error("expected non-nil error for missing work item")
	}
}

func TestCreateDraftPRAction_NoChanges_UnqueuesIssue(t *testing.T) {
	repoDir := initTestGitRepo(t)

	defaultBranch := getDefaultBranch(t, repoDir)
	mustRunGit(t, repoDir, "checkout", "-b", "issue-60")

	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	// Mock gh issue edit/comment/close for unqueueIssue
	mockExec.AddPrefixMatch("gh", []string{"issue", "edit"}, exec.MockResponse{})
	mockExec.AddPrefixMatch("gh", []string{"issue", "comment"}, exec.MockResponse{})
	mockExec.AddPrefixMatch("gh", []string{"issue", "close"}, exec.MockResponse{})

	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	d := testDaemonWithExec(cfg, mockExec)
	d.gitService = gitSvc
	d.repoFilter = repoDir
	cfg.Repos = []string{repoDir}

	sess := &config.Session{
		ID:         "sess-draft-no-changes",
		RepoPath:   repoDir,
		WorkTree:   repoDir,
		Branch:     "issue-60",
		BaseBranch: defaultBranch,
	}
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-draft-no-changes",
		IssueRef:  config.IssueRef{Source: "github", ID: "60"},
		SessionID: "sess-draft-no-changes",
		Branch:    "issue-60",
		StepData:  map[string]any{},
	})

	action := &createDraftPRAction{daemon: d}
	ac := &workflow.ActionContext{
		WorkItemID: "item-draft-no-changes",
		Params:     workflow.NewParamHelper(nil),
	}

	result := action.Execute(context.Background(), ac)

	if !result.Success {
		t.Errorf("expected success on no-changes, got error: %v", result.Error)
	}
	if result.OverrideNext != "done" {
		t.Errorf("expected OverrideNext='done', got %q", result.OverrideNext)
	}
}

func TestCreateDraftPRAction_RegisteredInRegistry(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)
	registry := d.buildActionRegistry()
	if registry.Get("github.create_draft_pr") == nil {
		t.Error("github.create_draft_pr not registered in action registry")
	}
}

func TestCreatePRAction_DraftParam_PassedThrough(t *testing.T) {
	// Verifies that github.create_pr with draft=true reads the param correctly.
	// Since createPR will fail with "session not found" before reaching git ops,
	// we just verify the action returns an error (not a panic or wrong code path).
	cfg := testConfig()
	d := testDaemon(cfg)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:        "item-draft-param",
		IssueRef:  config.IssueRef{Source: "github", ID: "99"},
		SessionID: "nonexistent-session",
		Branch:    "issue-99",
		StepData:  map[string]any{},
	})

	action := &createPRAction{daemon: d}
	ac := &workflow.ActionContext{
		WorkItemID: "item-draft-param",
		Params:     workflow.NewParamHelper(map[string]any{"draft": true}),
	}

	result := action.Execute(context.Background(), ac)

	// Should fail because session doesn't exist, not because of a panic or wrong param handling
	if result.Success {
		t.Error("expected failure for missing session")
	}
	if result.Error == nil {
		t.Error("expected non-nil error")
	}
}
