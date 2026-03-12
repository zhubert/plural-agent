package issues

import (
	"context"
	"fmt"
	"strconv"
)

// PostClaim posts a claim comment on a GitHub issue and returns the comment ID.
// Uses the REST API so the response includes the newly created comment's ID.
// Implements ProviderClaimManager.
func (p *GitHubProvider) PostClaim(ctx context.Context, repoPath string, issueID string, claim ClaimInfo) (string, error) {
	issueNum, err := strconv.Atoi(issueID)
	if err != nil {
		return "", fmt.Errorf("invalid GitHub issue ID %q: %w", issueID, err)
	}

	body := formatClaimBodyGitHub(claim)
	commentID, err := p.gitService.CreateIssueCommentWithID(ctx, repoPath, issueNum, body)
	if err != nil {
		return "", fmt.Errorf("failed to post claim comment: %w", err)
	}

	return strconv.FormatInt(commentID, 10), nil
}

// GetClaims reads all claim comments from a GitHub issue.
// Implements ProviderClaimManager.
func (p *GitHubProvider) GetClaims(ctx context.Context, repoPath string, issueID string) ([]ClaimInfo, error) {
	comments, err := p.GetIssueComments(ctx, repoPath, issueID)
	if err != nil {
		return nil, err
	}
	return getClaimsFromComments(comments), nil
}

// DeleteClaim deletes a claim comment from a GitHub issue by its comment ID.
// Implements ProviderClaimManager.
func (p *GitHubProvider) DeleteClaim(ctx context.Context, repoPath string, issueID string, commentID string) error {
	id, err := strconv.ParseInt(commentID, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid GitHub comment ID %q: %w", commentID, err)
	}
	return p.gitService.DeleteIssueComment(ctx, repoPath, id)
}
