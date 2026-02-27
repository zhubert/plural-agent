package worker

import (
	"context"
	"log/slog"

	"github.com/zhubert/erg/internal/agentconfig"
	"github.com/zhubert/erg/internal/claude"
	"github.com/zhubert/erg/internal/git"
)

// Host is the interface that SessionWorker uses to access its owning daemon.
// It decouples SessionWorker from the concrete Daemon type.
type Host interface {
	// Config returns the agent configuration.
	Config() agentconfig.Config

	// GitService returns the git service.
	GitService() *git.GitService

	// GetPendingMessage returns and clears the pending message for a session.
	// This is a consuming get — the message is cleared after retrieval.
	GetPendingMessage(sessionID string) string

	// SetPendingMessage queues a message to be sent to a session on its next turn.
	SetPendingMessage(sessionID, msg string)

	// Logger returns the structured logger.
	Logger() *slog.Logger

	// Settings
	MaxTurns() int
	MaxDuration() int
	AutoMerge() bool
	MergeMethod() string
	AutoAddressPRComments() bool

	// Operations
	CleanupSession(ctx context.Context, sessionID string) error
	SaveRunnerMessages(sessionID string, runner claude.RunnerSession)
	IsWorkerRunning(sessionID string) bool

	// RecordSpend adds token and cost data from a completed Claude response
	// to the daemon's running totals.
	RecordSpend(costUSD float64, outputTokens, inputTokens int)

	// SetWorkItemData stores a key-value pair in the work item's StepData
	// for the session identified by sessionID.
	SetWorkItemData(sessionID, key string, value any) error

	// CommentOnIssue posts a comment on the issue/task associated with the
	// given session. Routes through the appropriate provider (GitHub, Asana,
	// Linear) based on the issue source.
	CommentOnIssue(ctx context.Context, sessionID, body string) error
}

