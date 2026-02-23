package worker

import (
	"context"
	"log/slog"

	"github.com/zhubert/erg/internal/agentconfig"
	"github.com/zhubert/erg/internal/claude"
	"github.com/zhubert/erg/internal/git"
	"github.com/zhubert/erg/internal/manager"
)

// Host is the interface that SessionWorker uses to access its owning daemon.
// It decouples SessionWorker from the concrete Daemon type.
type Host interface {
	// Config returns the agent configuration.
	Config() agentconfig.Config

	// GitService returns the git service.
	GitService() *git.GitService

	// SessionManager returns the session manager.
	SessionManager() *manager.SessionManager

	// Logger returns the structured logger.
	Logger() *slog.Logger

	// Settings
	MaxTurns() int
	MaxDuration() int
	AutoMerge() bool
	MergeMethod() string
	AutoAddressPRComments() bool

	// Operations
	CreateChildSession(ctx context.Context, supervisorID, taskDescription string) (SessionInfo, error)
	CleanupSession(ctx context.Context, sessionID string) error
	SaveRunnerMessages(sessionID string, runner claude.RunnerInterface)
	IsWorkerRunning(sessionID string) bool
}

// SessionInfo holds the minimal info needed after creating a child session.
type SessionInfo struct {
	ID     string
	Branch string
}
