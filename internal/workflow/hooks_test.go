package workflow

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestRunHooks_Success(t *testing.T) {
	dir := t.TempDir()
	outFile := filepath.Join(dir, "output.txt")

	hooks := []HookConfig{
		{Run: "echo hello > " + outFile},
	}

	hookCtx := HookContext{
		RepoPath: dir,
		Branch:   "test-branch",
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	RunHooks(context.Background(), hooks, hookCtx, logger)

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("hook output file not created: %v", err)
	}
	if got := string(data); got != "hello\n" {
		t.Errorf("hook output: got %q, want %q", got, "hello\n")
	}
}

func TestRunHooks_FailureDoesNotBlock(t *testing.T) {
	dir := t.TempDir()
	outFile := filepath.Join(dir, "second.txt")

	hooks := []HookConfig{
		{Run: "exit 1"},                      // This fails
		{Run: "echo ok > " + outFile},         // This should still run
	}

	hookCtx := HookContext{RepoPath: dir}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	RunHooks(context.Background(), hooks, hookCtx, logger)

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("second hook should have run: %v", err)
	}
	if string(data) != "ok\n" {
		t.Errorf("second hook output: got %q", string(data))
	}
}

func TestRunHooks_EmptyRun(t *testing.T) {
	hooks := []HookConfig{{Run: ""}}
	hookCtx := HookContext{RepoPath: t.TempDir()}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// Should not panic
	RunHooks(context.Background(), hooks, hookCtx, logger)
}

func TestRunHooks_EnvironmentVariables(t *testing.T) {
	dir := t.TempDir()
	outFile := filepath.Join(dir, "env.txt")

	hooks := []HookConfig{
		{Run: "echo $ERG_BRANCH > " + outFile},
	}

	hookCtx := HookContext{
		RepoPath: dir,
		Branch:   "feature/test",
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	RunHooks(context.Background(), hooks, hookCtx, logger)

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("hook output file not created: %v", err)
	}
	if got := string(data); got != "feature/test\n" {
		t.Errorf("env var: got %q, want %q", got, "feature/test\n")
	}
}

func TestRunHooks_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	hooks := []HookConfig{
		{Run: "sleep 10"},
	}

	hookCtx := HookContext{RepoPath: t.TempDir()}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// Should return quickly due to cancelled context
	RunHooks(ctx, hooks, hookCtx, logger)
}

func TestHookContext_EnvVars(t *testing.T) {
	hc := HookContext{
		RepoPath:   "/repo",
		Branch:     "main",
		SessionID:  "abc123",
		IssueID:    "42",
		IssueTitle: "Fix bug",
		IssueURL:   "https://github.com/test/repo/issues/42",
		PRURL:      "https://github.com/test/repo/pull/1",
		WorkTree:   "/worktree",
		Provider:   "github",
	}

	vars := hc.envVars()
	expected := map[string]string{
		"ERG_REPO_PATH":   "/repo",
		"ERG_BRANCH":      "main",
		"ERG_SESSION_ID":  "abc123",
		"ERG_ISSUE_ID":    "42",
		"ERG_ISSUE_TITLE": "Fix bug",
		"ERG_ISSUE_URL":   "https://github.com/test/repo/issues/42",
		"ERG_PR_URL":      "https://github.com/test/repo/pull/1",
		"ERG_WORKTREE":    "/worktree",
		"ERG_PROVIDER":    "github",
	}

	varMap := make(map[string]string)
	for _, v := range vars {
		parts := splitEnvVar(v)
		if len(parts) == 2 {
			varMap[parts[0]] = parts[1]
		}
	}

	for k, want := range expected {
		got, ok := varMap[k]
		if !ok {
			t.Errorf("missing env var %s", k)
			continue
		}
		if got != want {
			t.Errorf("%s: got %q, want %q", k, got, want)
		}
	}
}

func TestRunBeforeHooks_Success(t *testing.T) {
	dir := t.TempDir()
	outFile := filepath.Join(dir, "before_output.txt")

	hooks := []HookConfig{
		{Run: "echo before > " + outFile},
	}

	hookCtx := HookContext{RepoPath: dir, Branch: "test"}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	err := RunBeforeHooks(context.Background(), hooks, hookCtx, logger)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("hook output file not created: %v", err)
	}
	if got := string(data); got != "before\n" {
		t.Errorf("hook output: got %q, want %q", got, "before\n")
	}
}

func TestRunBeforeHooks_FailureBlocks(t *testing.T) {
	dir := t.TempDir()
	outFile := filepath.Join(dir, "should_not_exist.txt")

	hooks := []HookConfig{
		{Run: "exit 1"},                             // This fails
		{Run: "echo nope > " + outFile},              // This should NOT run
	}

	hookCtx := HookContext{RepoPath: dir}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	err := RunBeforeHooks(context.Background(), hooks, hookCtx, logger)
	if err == nil {
		t.Fatal("expected error from failing before hook")
	}

	// Second hook should NOT have run
	if _, err := os.Stat(outFile); err == nil {
		t.Error("second hook should not have run after first hook failure")
	}
}

func TestRunBeforeHooks_EmptyRun(t *testing.T) {
	hooks := []HookConfig{{Run: ""}}
	hookCtx := HookContext{RepoPath: t.TempDir()}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	err := RunBeforeHooks(context.Background(), hooks, hookCtx, logger)
	if err != nil {
		t.Fatalf("expected no error for empty run, got: %v", err)
	}
}

func TestRunBeforeHooks_MultipleSuccess(t *testing.T) {
	dir := t.TempDir()
	outFile1 := filepath.Join(dir, "first.txt")
	outFile2 := filepath.Join(dir, "second.txt")

	hooks := []HookConfig{
		{Run: "echo first > " + outFile1},
		{Run: "echo second > " + outFile2},
	}

	hookCtx := HookContext{RepoPath: dir}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	err := RunBeforeHooks(context.Background(), hooks, hookCtx, logger)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	for _, f := range []string{outFile1, outFile2} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("expected file %s to exist", f)
		}
	}
}

func splitEnvVar(s string) []string {
	idx := 0
	for i, c := range s {
		if c == '=' {
			idx = i
			break
		}
	}
	if idx == 0 {
		return []string{s}
	}
	return []string{s[:idx], s[idx+1:]}
}
