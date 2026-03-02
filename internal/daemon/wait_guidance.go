package daemon

import (
	"context"
	"strings"

	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/issues"
	"github.com/zhubert/erg/internal/workflow"
)

// postWaitGuidance posts a guidance comment on the issue tracker when the workflow
// enters a wait state that requires human action. The comment is idempotent: if a
// comment with the same step marker already exists it is updated in place.
//
// This is best-effort: failures are logged but do not affect the workflow.
func (d *Daemon) postWaitGuidance(ctx context.Context, item daemonstate.WorkItem, stepName string, state *workflow.State) {
	msg := workflow.WaitStateGuidance(state, item.PRURL)
	if msg == "" {
		return
	}

	log := d.logger.With("workItem", item.ID, "step", stepName, "event", state.Event)

	source := issues.Source(item.IssueRef.Source)
	var postErr error

	switch source {
	case issues.SourceGitHub:
		postErr = d.postGuidanceGitHub(ctx, item, stepName, msg)
	case issues.SourceAsana:
		params := workflow.NewParamHelper(map[string]any{"body": msg})
		postErr = d.commentViaProvider(ctx, item, params, issues.SourceAsana, stepName)
	case issues.SourceLinear:
		params := workflow.NewParamHelper(map[string]any{"body": msg})
		postErr = d.commentViaProvider(ctx, item, params, issues.SourceLinear, stepName)
	default:
		log.Debug("guidance posting not supported for source", "source", source)
		return
	}

	if postErr != nil {
		log.Warn("failed to post wait state guidance (non-fatal)", "error", postErr)
	} else {
		log.Info("posted wait state guidance")
	}
}

// postGuidanceGitHub posts a guidance comment on a GitHub issue.
// It tries the provider registry first (if a GitHub provider is registered),
// then falls back to the git service. The comment is idempotent via step marker.
func (d *Daemon) postGuidanceGitHub(ctx context.Context, item daemonstate.WorkItem, stepName, msg string) error {
	// Try the provider registry first.
	if d.issueRegistry != nil {
		if p := d.issueRegistry.GetProvider(issues.SourceGitHub); p != nil {
			if pa, ok := p.(issues.ProviderActions); ok {
				repoPath := d.resolveRepoPath(ctx, item)
				marker := ergGitHubMarker(stepName)
				markedBody := msg + "\n" + marker

				commentCtx, cancel := context.WithTimeout(ctx, timeoutStandardOp)
				defer cancel()

				// Idempotent upsert if provider supports it.
				if gc, ok := p.(issues.ProviderGateChecker); ok {
					if cu, ok := p.(issues.ProviderCommentUpdater); ok {
						existing, listErr := gc.GetIssueComments(commentCtx, repoPath, item.IssueRef.ID)
						if listErr == nil {
							for _, c := range existing {
								if strings.Contains(c.Body, marker) {
									return cu.UpdateComment(commentCtx, repoPath, item.IssueRef.ID, c.ID, markedBody)
								}
							}
						}
					}
				}
				return pa.Comment(commentCtx, repoPath, item.IssueRef.ID, markedBody)
			}
		}
	}

	// Fall back to git service for GitHub (handles idempotency via step marker).
	params := workflow.NewParamHelper(map[string]any{"body": msg})
	return d.commentOnIssue(ctx, item, params, stepName)
}
