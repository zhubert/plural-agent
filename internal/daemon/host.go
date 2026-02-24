package daemon

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/zhubert/erg/internal/agentconfig"
	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/worker"
	"github.com/zhubert/erg/internal/workflow"
	"github.com/zhubert/erg/internal/claude"
	"github.com/zhubert/erg/internal/git"
	"github.com/zhubert/erg/internal/manager"
)

// Compile-time assertion: Daemon must implement worker.Host.
var _ worker.Host = (*Daemon)(nil)

// --- Host interface implementation ---

func (d *Daemon) Config() agentconfig.Config             { return d.config }
func (d *Daemon) GitService() *git.GitService            { return d.gitService }
func (d *Daemon) SessionManager() *manager.SessionManager { return d.sessionMgr }
func (d *Daemon) Logger() *slog.Logger                   { return d.logger }
func (d *Daemon) MaxTurns() int                          { return d.getMaxTurns() }
func (d *Daemon) MaxDuration() int                       { return d.getMaxDuration() }
func (d *Daemon) AutoMerge() bool                        { return d.autoMerge }
func (d *Daemon) MergeMethod() string                    { return d.getMergeMethod() }
func (d *Daemon) AutoAddressPRComments() bool            { return d.getAutoAddressPRComments() }

func (d *Daemon) CreateChildSession(ctx context.Context, supervisorID, taskDescription string) (worker.SessionInfo, error) {
	// Daemon doesn't directly support child sessions through Host interface;
	// worker child creation goes through the Agent path. This is a no-op for daemon.
	return worker.SessionInfo{}, fmt.Errorf("child sessions not supported in daemon mode")
}

func (d *Daemon) CleanupSession(ctx context.Context, sessionID string) error {
	d.cleanupSession(ctx, sessionID)
	return nil
}

func (d *Daemon) SaveRunnerMessages(sessionID string, runner claude.RunnerInterface) {
	d.saveRunnerMessages(sessionID, runner)
}

func (d *Daemon) IsWorkerRunning(sessionID string) bool {
	d.mu.Lock()
	w, exists := d.workers[sessionID]
	d.mu.Unlock()
	return exists && !w.Done()
}

// RecordSpend adds token and cost data to the daemon's running totals and
// persists the updated state to disk so that `erg status` reflects current spend.
func (d *Daemon) RecordSpend(costUSD float64, outputTokens, inputTokens int) {
	d.state.AddSpend(costUSD, outputTokens, inputTokens)
	if err := d.state.Save(); err != nil {
		d.logger.Warn("failed to save state after recording spend", "error", err)
	}
}

// workItemView creates a read-only view of a work item for the engine.
func (d *Daemon) workItemView(item *daemonstate.WorkItem) *workflow.WorkItemView {
	// Use the session's actual repo path rather than d.repoFilter,
	// which may be empty or a pattern (e.g., "owner/repo") in multi-repo daemons.
	repoPath := d.repoFilter
	if sess := d.config.GetSession(item.SessionID); sess != nil {
		repoPath = sess.RepoPath
	} else if rp, ok := item.StepData["_repo_path"].(string); ok && rp != "" {
		repoPath = rp
	} else if item.SessionID != "" {
		d.logger.Debug("session not found for work item, using repoFilter",
			"workItem", item.ID, "sessionID", item.SessionID, "repoFilter", d.repoFilter)
	}

	return &workflow.WorkItemView{
		ID:                item.ID,
		SessionID:         item.SessionID,
		RepoPath:          repoPath,
		Branch:            item.Branch,
		PRURL:             item.PRURL,
		CurrentStep:       item.CurrentStep,
		Phase:             item.Phase,
		StepData:          item.StepData,
		FeedbackRounds:    item.FeedbackRounds,
		CommentsAddressed: item.CommentsAddressed,
		StepEnteredAt:     item.StepEnteredAt,
	}
}

// activeSlotCount returns the number of work items consuming concurrency slots.
func (d *Daemon) activeSlotCount() int {
	return d.state.ActiveSlotCount()
}
