package workflow

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
		{Run: "exit 1"},               // This fails
		{Run: "echo ok > " + outFile}, // This should still run
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
		{Run: "exit 1"},                 // This fails
		{Run: "echo nope > " + outFile}, // This should NOT run
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

func TestFilteredEnv_SecretVarsAreRemoved(t *testing.T) {
	secrets := []struct{ key, value string }{
		{"ANTHROPIC_API_KEY", "sk-ant-secret"},
		{"GITHUB_TOKEN", "ghp_secret"},
		{"GH_TOKEN", "ghp_alt_secret"},
		{"ASANA_PAT", "asana_secret"},
		{"LINEAR_API_KEY", "linear_secret"},
		{"CLAUDE_CODE_OAUTH_TOKEN", "oauth_secret"},
	}
	for _, s := range secrets {
		t.Setenv(s.key, s.value)
	}

	env := filteredEnv()

	envMap := make(map[string]string, len(env))
	for _, kv := range env {
		key, val, _ := strings.Cut(kv, "=")
		envMap[key] = val
	}

	for _, s := range secrets {
		if _, found := envMap[s.key]; found {
			t.Errorf("sensitive env var %s should have been filtered out", s.key)
		}
	}
}

func TestFilteredEnv_NonSecretVarsAreKept(t *testing.T) {
	t.Setenv("MY_CUSTOM_VAR", "keep_me")
	t.Setenv("ANTHROPIC_API_KEY", "strip_me")

	env := filteredEnv()

	envMap := make(map[string]string, len(env))
	for _, kv := range env {
		key, val, _ := strings.Cut(kv, "=")
		envMap[key] = val
	}

	if got, ok := envMap["MY_CUSTOM_VAR"]; !ok || got != "keep_me" {
		t.Errorf("non-secret env var MY_CUSTOM_VAR should be present, got %q", got)
	}
	if _, found := envMap["ANTHROPIC_API_KEY"]; found {
		t.Error("ANTHROPIC_API_KEY should have been filtered out")
	}
}

func TestRunHooks_SecretsNotPassedToHook(t *testing.T) {
	dir := t.TempDir()
	outFile := filepath.Join(dir, "secret_check.txt")

	t.Setenv("ANTHROPIC_API_KEY", "super_secret_key")
	t.Setenv("GITHUB_TOKEN", "ghp_super_secret")

	hooks := []HookConfig{
		{Run: "echo \"$ANTHROPIC_API_KEY $GITHUB_TOKEN\" > " + outFile},
	}

	hookCtx := HookContext{RepoPath: dir, Branch: "test"}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	RunHooks(context.Background(), hooks, hookCtx, logger)

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("hook output file not created: %v", err)
	}
	content := string(data)
	if strings.Contains(content, "super_secret_key") {
		t.Error("ANTHROPIC_API_KEY value leaked into hook environment")
	}
	if strings.Contains(content, "ghp_super_secret") {
		t.Error("GITHUB_TOKEN value leaked into hook environment")
	}
}

func TestRunBeforeHooks_SecretsNotPassedToHook(t *testing.T) {
	dir := t.TempDir()
	outFile := filepath.Join(dir, "secret_check.txt")

	t.Setenv("ANTHROPIC_API_KEY", "super_secret_key")
	t.Setenv("GITHUB_TOKEN", "ghp_super_secret")

	hooks := []HookConfig{
		{Run: "echo \"$ANTHROPIC_API_KEY $GITHUB_TOKEN\" > " + outFile},
	}

	hookCtx := HookContext{RepoPath: dir, Branch: "test"}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := RunBeforeHooks(context.Background(), hooks, hookCtx, logger); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("hook output file not created: %v", err)
	}
	content := string(data)
	if strings.Contains(content, "super_secret_key") {
		t.Error("ANTHROPIC_API_KEY value leaked into before hook environment")
	}
	if strings.Contains(content, "ghp_super_secret") {
		t.Error("GITHUB_TOKEN value leaked into before hook environment")
	}
}

func TestRunHooks_ErgVarsStillPassedAfterFiltering(t *testing.T) {
	dir := t.TempDir()
	outFile := filepath.Join(dir, "erg_check.txt")

	t.Setenv("ANTHROPIC_API_KEY", "should_be_stripped")

	hooks := []HookConfig{
		{Run: "echo $ERG_BRANCH > " + outFile},
	}

	hookCtx := HookContext{RepoPath: dir, Branch: "feature/my-branch"}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	RunHooks(context.Background(), hooks, hookCtx, logger)

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("hook output file not created: %v", err)
	}
	if got := string(data); got != "feature/my-branch\n" {
		t.Errorf("ERG_BRANCH should be available in hook env, got %q", got)
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
