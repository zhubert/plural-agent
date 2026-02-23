package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	osexec "os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zhubert/plural-agent/internal/daemonstate"
	"github.com/zhubert/plural-agent/internal/worker"
	"github.com/zhubert/plural-agent/internal/workflow"
	"github.com/zhubert/plural-core/claude"
	"github.com/zhubert/plural-core/config"
	"github.com/zhubert/plural-core/exec"
	"github.com/zhubert/plural-core/git"
	"github.com/zhubert/plural-core/session"
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

	err := d.startCoding(context.Background(), item)
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
	if item.SessionID == "" {
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

	err := d.startCoding(context.Background(), item)
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
	if item.SessionID == "" {
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

	err := d.startCoding(context.Background(), item)
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

	err := d.startCoding(ctx, item)
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

	err := d.startCoding(ctx, item)
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

	err := d.startCoding(context.Background(), item)
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

	err := d.startCoding(ctx, item)
	if err != nil {
		t.Fatalf("startCoding should succeed, got: %v", err)
	}

	// item.SessionID must be set so that a subsequent saveState records the
	// reference — this is the core of the bug fix.
	if item.SessionID == "" {
		t.Error("item.SessionID must be set before saveConfig is called (regression: orphaned session on crash)")
	}
	if item.Branch == "" {
		t.Error("item.Branch must be set after startCoding")
	}
	if item.State != daemonstate.WorkItemActive {
		t.Errorf("item.State must be WorkItemActive, got %q", item.State)
	}

	// The SessionID on the work item must match the session recorded in config,
	// confirming both are kept consistent.
	sessions := cfg.GetSessions()
	if len(sessions) == 0 {
		t.Fatal("expected a session to be recorded in config")
	}
	if sessions[0].ID != item.SessionID {
		t.Errorf("config session ID %q does not match item.SessionID %q", sessions[0].ID, item.SessionID)
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
			porcelainOutput: "worktree /home/user/repo\nHEAD abc123\nbranch refs/heads/main\n\nworktree /home/user/.plural/worktrees/uuid1\nHEAD def456\nbranch refs/heads/issue-10\n\n",
			branchName:      "issue-10",
			expectedPath:    "/home/user/.plural/worktrees/uuid1",
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

	err := d.runFormatter(context.Background(), d.state.GetWorkItem("item-1"), params)
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

	err := d.runFormatter(context.Background(), d.state.GetWorkItem("item-1"), params)
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

	err := d.runFormatter(context.Background(), d.state.GetWorkItem("item-1"), params)
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

	err := d.runFormatter(context.Background(), d.state.GetWorkItem("item-1"), params)
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

	err := d.runFormatter(context.Background(), d.state.GetWorkItem("item-1"), params)
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

	result := d.refreshStaleSession(context.Background(), item, sess)

	if result.ID != "sess-real" {
		t.Errorf("expected session ID unchanged, got %s", result.ID)
	}
	if d.state.GetWorkItem("item-1").SessionID != "sess-real" {
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

	result := d.refreshStaleSession(context.Background(), item, sess)

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
	updatedItem := d.state.GetWorkItem("item-stale")
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

	result := d.refreshStaleSession(context.Background(), item, sess)

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
	updatedItem := d.state.GetWorkItem("item-done")
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

	result := d.refreshStaleSession(context.Background(), item, sess)

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

	result := d.refreshStaleSession(context.Background(), item, sess)

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

	item := d.state.GetWorkItem("item-no-changes")
	_, err := d.createPR(context.Background(), item)
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
			{Author: struct{ Login string }{Login: "plural-bot"}, Body: transcriptBody, URL: "https://example.com"},
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

	item := d.state.GetWorkItem("item-1")
	d.addressFeedback(context.Background(), item)

	updated := d.state.GetWorkItem("item-1")
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

	item := d.state.GetWorkItem("item-existing")
	_, err := d.createPR(context.Background(), item)
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
	d.closeIssueGracefully(context.Background(), item)
	// No assertion needed — just verifying it doesn't panic
}
