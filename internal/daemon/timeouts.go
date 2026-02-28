package daemon

import "time"

// Timeout constants for context deadlines across daemon operations.
// These replace magic duration literals to make timeout budgets visible
// and easy to tune in one place. Test files intentionally use their own
// literals so that test timeouts are self-contained and obvious.
const (
	// timeoutQuickAPI is for fast API calls and status checks
	// (PR state, PR URL, branch-has-changes, gate labels, Slack notify).
	timeoutQuickAPI = 15 * time.Second

	// timeoutStandardOp is for standard-weight operations
	// (comments, labels, polling, review fetches, worktree add, PR body update).
	timeoutStandardOp = 30 * time.Second

	// timeoutGitHubMerge is for heavier single-shot GitHub operations
	// (merge PR, create release, fetch CI failure logs).
	timeoutGitHubMerge = 60 * time.Second

	// timeoutGitPush is for operations that push data to a remote
	// (push updates, create PR, rebase-linearize, diff validation).
	timeoutGitPush = 2 * time.Minute

	// timeoutPRDescription is for AI-generated PR description generation.
	timeoutPRDescription = 3 * time.Minute

	// timeoutGitRewrite is for history-rewriting operations
	// (rebase, squash, cherry-pick, format, merge-base-into-branch).
	timeoutGitRewrite = 5 * time.Minute

	// timeoutDockerHealth is for the Docker daemon health check.
	timeoutDockerHealth = 5 * time.Second
)
