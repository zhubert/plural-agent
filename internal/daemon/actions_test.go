package daemon

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zhubert/plural-agent/internal/daemonstate"
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
