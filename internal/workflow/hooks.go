package workflow

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
)

// HookContext provides environment variables for hook execution.
type HookContext struct {
	RepoPath  string
	Branch    string
	SessionID string
	IssueID   string
	IssueTitle string
	IssueURL  string
	PRURL     string
	WorkTree  string
	Provider  string
}

// envVars returns the hook context as environment variable pairs.
func (hc HookContext) envVars() []string {
	return []string{
		fmt.Sprintf("ERG_REPO_PATH=%s", hc.RepoPath),
		fmt.Sprintf("ERG_BRANCH=%s", hc.Branch),
		fmt.Sprintf("ERG_SESSION_ID=%s", hc.SessionID),
		fmt.Sprintf("ERG_ISSUE_ID=%s", hc.IssueID),
		fmt.Sprintf("ERG_ISSUE_TITLE=%s", hc.IssueTitle),
		fmt.Sprintf("ERG_ISSUE_URL=%s", hc.IssueURL),
		fmt.Sprintf("ERG_PR_URL=%s", hc.PRURL),
		fmt.Sprintf("ERG_WORKTREE=%s", hc.WorkTree),
		fmt.Sprintf("ERG_PROVIDER=%s", hc.Provider),
	}
}

// RunHooks executes hooks sequentially. Errors are logged but do not block the workflow.
func RunHooks(ctx context.Context, hooks []HookConfig, hookCtx HookContext, logger *slog.Logger) {
	for _, hook := range hooks {
		if hook.Run == "" {
			continue
		}

		cmd := exec.CommandContext(ctx, "sh", "-c", hook.Run)
		cmd.Dir = hookCtx.RepoPath
		cmd.Env = append(os.Environ(), hookCtx.envVars()...)

		output, err := cmd.CombinedOutput()
		if err != nil {
			logger.Warn("hook failed",
				"command", hook.Run,
				"error", err,
				"output", string(output),
			)
			continue
		}

		logger.Debug("hook completed",
			"command", hook.Run,
			"output", string(output),
		)
	}
}

// RunBeforeHooks executes before-hooks sequentially. Unlike RunHooks (after-hooks),
// a failure stops execution and returns the error, blocking the workflow step.
func RunBeforeHooks(ctx context.Context, hooks []HookConfig, hookCtx HookContext, logger *slog.Logger) error {
	for _, hook := range hooks {
		if hook.Run == "" {
			continue
		}

		cmd := exec.CommandContext(ctx, "sh", "-c", hook.Run)
		cmd.Dir = hookCtx.RepoPath
		cmd.Env = append(os.Environ(), hookCtx.envVars()...)

		output, err := cmd.CombinedOutput()
		if err != nil {
			logger.Error("before hook failed, blocking step",
				"command", hook.Run,
				"error", err,
				"output", string(output),
			)
			return fmt.Errorf("before hook %q failed: %w", hook.Run, err)
		}

		logger.Debug("before hook completed",
			"command", hook.Run,
			"output", string(output),
		)
	}
	return nil
}
