package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/zhubert/erg/internal/agentconfig"
	"github.com/zhubert/erg/internal/claude"
	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/git"
	"github.com/zhubert/erg/internal/issues"
	"github.com/zhubert/erg/internal/worker"
	"github.com/zhubert/erg/internal/workflow"
)

// Compile-time assertion: Daemon must implement worker.Host.
var _ worker.Host = (*Daemon)(nil)

// --- Host interface implementation ---

func (d *Daemon) Config() agentconfig.Config  { return d.config }
func (d *Daemon) GitService() *git.GitService { return d.gitService }
func (d *Daemon) Logger() *slog.Logger        { return d.logger }

func (d *Daemon) GetPendingMessage(sessionID string) string {
	return d.sessionMgr.StateManager().GetPendingMessage(sessionID)
}

func (d *Daemon) SetPendingMessage(sessionID, msg string) {
	d.sessionMgr.StateManager().GetOrCreate(sessionID).SetPendingMsg(msg)
}
func (d *Daemon) MaxTurns() int               { return d.getMaxTurns() }
func (d *Daemon) MaxDuration() int            { return d.getMaxDuration() }
func (d *Daemon) AutoMerge() bool             { return d.autoMerge }
func (d *Daemon) MergeMethod() string         { return d.getMergeMethod() }
func (d *Daemon) AutoAddressPRComments() bool { return d.getAutoAddressPRComments() }

func (d *Daemon) CleanupSession(ctx context.Context, sessionID string) error {
	d.cleanupSession(ctx, sessionID)
	return nil
}

func (d *Daemon) SaveRunnerMessages(sessionID string, runner claude.RunnerSession) {
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

// RecordItemSpend accumulates spend data on the work item associated with the
// given session ID. Linear scan over all items — count is always small.
func (d *Daemon) RecordItemSpend(sessionID string, costUSD float64, outputTokens, inputTokens int) {
	for _, item := range d.state.GetAllWorkItems() {
		if item.SessionID == sessionID {
			d.state.RecordItemSpend(item.ID, costUSD, outputTokens, inputTokens)
			return
		}
	}
	d.logger.Warn("RecordItemSpend: no work item found for session", "sessionID", sessionID)
}

// SetWorkItemData stores a key-value pair in the work item's StepData
// for the work item associated with the given session ID.
func (d *Daemon) SetWorkItemData(sessionID, key string, value any) error {
	for _, item := range d.state.GetActiveWorkItems() {
		if item.SessionID == sessionID {
			d.state.UpdateWorkItem(item.ID, func(it *daemonstate.WorkItem) {
				if it.StepData == nil {
					it.StepData = make(map[string]any)
				}
				it.StepData[key] = value
			})
			return nil
		}
	}
	return fmt.Errorf("no work item found for session %s", sessionID)
}

// CommentOnIssue posts a comment on the issue/task associated with the given session.
// It routes through the appropriate provider (GitHub, Asana, Linear) based on the
// issue source. For GitHub, falls back to GitService if no provider is registered.
func (d *Daemon) CommentOnIssue(ctx context.Context, sessionID, body string) error {
	return d.UpsertIssueComment(ctx, sessionID, body, "")
}

// UpsertIssueComment posts or updates a comment on the issue/task associated
// with the given session. If marker is non-empty and an existing comment
// contains it, the comment is updated in place; otherwise a new comment is
// created. When marker is empty, a new comment is always created.
func (d *Daemon) UpsertIssueComment(ctx context.Context, sessionID, body, marker string) error {
	sess, err := d.getSessionOrError(sessionID)
	if err != nil {
		return err
	}

	issueRef := sess.GetIssueRef()
	if issueRef == nil {
		return fmt.Errorf("no issue associated with session %s", sessionID)
	}

	commentCtx, cancel := context.WithTimeout(ctx, timeoutStandardOp)
	defer cancel()

	source := issues.Source(issueRef.Source)

	if d.issueRegistry != nil {
		if p := d.issueRegistry.GetProvider(source); p != nil {
			// Try to find and update an existing comment with the marker.
			if marker != "" {
				if gc, ok := p.(issues.ProviderGateChecker); ok {
					if cu, ok := p.(issues.ProviderCommentUpdater); ok {
						existing, listErr := gc.GetIssueComments(commentCtx, sess.RepoPath, issueRef.ID)
						if listErr == nil {
							for i := len(existing) - 1; i >= 0; i-- {
								if containsMarker(existing[i], marker) {
									return cu.UpdateComment(commentCtx, sess.RepoPath, issueRef.ID, existing[i].ID, body)
								}
							}
						} else {
							d.logger.Warn("failed to list comments for upsert, creating new", "error", listErr)
						}
					}
				}
			}
			// No existing comment found (or no marker) — create a new one.
			if pa, ok := p.(issues.ProviderActions); ok {
				return pa.Comment(commentCtx, sess.RepoPath, issueRef.ID, body)
			}
		}
	}

	// Fallback for GitHub when no provider is registered.
	if source == issues.SourceGitHub {
		issueNum, err := strconv.Atoi(issueRef.ID)
		if err != nil {
			return fmt.Errorf("invalid GitHub issue ID %q: %w", issueRef.ID, err)
		}
		if marker != "" {
			existing, listErr := d.gitService.ListIssueComments(commentCtx, sess.RepoPath, issueNum)
			if listErr == nil {
				for i := len(existing) - 1; i >= 0; i-- {
					if strings.Contains(existing[i].Body, marker) {
						return d.gitService.UpdateIssueComment(commentCtx, sess.RepoPath, existing[i].ID, body)
					}
				}
			} else {
				d.logger.Warn("failed to list GitHub comments for upsert, creating new", "error", listErr)
			}
		}
		return d.gitService.CommentOnIssue(commentCtx, sess.RepoPath, issueNum, body)
	}

	return fmt.Errorf("no provider registered for %s issues", source)
}

// workItemView creates a read-only view of a work item snapshot for the engine.
func (d *Daemon) workItemView(item daemonstate.WorkItem) *workflow.WorkItemView {
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
