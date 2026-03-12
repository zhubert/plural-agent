package issues

import (
	"context"
	"fmt"
)

// linearCommentCreateWithIDMutation creates a comment and returns its ID.
const linearCommentCreateWithIDMutation = `mutation($issueId: String!, $body: String!) {
  commentCreate(input: { issueId: $issueId, body: $body }) {
    success
    comment {
      id
    }
  }
}`

// linearCommentDeleteMutation deletes a comment by its ID.
const linearCommentDeleteMutation = `mutation($id: String!) {
  commentDelete(id: $id) {
    success
  }
}`

// PostClaim posts a claim comment on a Linear issue and returns the comment ID.
// Implements ProviderClaimManager.
func (p *LinearProvider) PostClaim(ctx context.Context, repoPath string, issueID string, claim ClaimInfo) (string, error) {
	// Look up the issue UUID since commentCreate requires it.
	var issueResp struct {
		Data struct {
			Issue struct {
				ID string `json:"id"`
			} `json:"issue"`
		} `json:"data"`
	}
	lookupQuery := `query($id: String!) { issue(id: $id) { id } }`
	if err := p.linearGraphQL(ctx, lookupQuery, map[string]any{"id": issueID}, "", &issueResp); err != nil {
		return "", fmt.Errorf("failed to look up issue UUID: %w", err)
	}
	issueUUID := issueResp.Data.Issue.ID
	if issueUUID == "" {
		return "", fmt.Errorf("issue %q not found in Linear", issueID)
	}

	body := formatClaimBodyVisible(claim)

	var createResp struct {
		Data struct {
			CommentCreate struct {
				Success bool `json:"success"`
				Comment struct {
					ID string `json:"id"`
				} `json:"comment"`
			} `json:"commentCreate"`
		} `json:"data"`
	}
	if err := p.linearGraphQL(ctx, linearCommentCreateWithIDMutation, map[string]any{
		"issueId": issueUUID,
		"body":    body,
	}, "", &createResp); err != nil {
		return "", fmt.Errorf("failed to create claim comment: %w", err)
	}
	if !createResp.Data.CommentCreate.Success {
		return "", fmt.Errorf("Linear API returned success=false for claim comment on issue %q", issueID)
	}

	return createResp.Data.CommentCreate.Comment.ID, nil
}

// GetClaims reads all claim comments from a Linear issue.
// Implements ProviderClaimManager.
func (p *LinearProvider) GetClaims(ctx context.Context, repoPath string, issueID string) ([]ClaimInfo, error) {
	comments, err := p.GetIssueComments(ctx, repoPath, issueID)
	if err != nil {
		return nil, err
	}

	return getClaimsFromComments(comments), nil
}

// DeleteClaim deletes a claim comment from a Linear issue by its comment ID.
// Implements ProviderClaimManager.
func (p *LinearProvider) DeleteClaim(ctx context.Context, repoPath string, issueID string, commentID string) error {
	var deleteResp struct {
		Data struct {
			CommentDelete struct {
				Success bool `json:"success"`
			} `json:"commentDelete"`
		} `json:"data"`
	}
	if err := p.linearGraphQL(ctx, linearCommentDeleteMutation, map[string]any{
		"id": commentID,
	}, "", &deleteResp); err != nil {
		return fmt.Errorf("failed to delete claim comment: %w", err)
	}
	if !deleteResp.Data.CommentDelete.Success {
		return fmt.Errorf("Linear API returned success=false for comment delete %q", commentID)
	}
	return nil
}
