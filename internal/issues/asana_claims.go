package issues

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/zhubert/erg/internal/secrets"
)

// PostClaim posts a claim comment on an Asana task and returns the story GID.
// Implements ProviderClaimManager.
func (p *AsanaProvider) PostClaim(ctx context.Context, repoPath string, issueID string, claim ClaimInfo) (string, error) {
	pat, ok := resolveToken(asanaPATEnvVar, secrets.AsanaPATService)
	if !ok {
		return "", secrets.TokenNotFoundError(asanaPATEnvVar)
	}

	body := formatClaimBodyVisible(claim)
	storiesURL := fmt.Sprintf("%s/tasks/%s/stories", p.apiBase, issueID)
	textJSON, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("failed to marshal claim body: %w", err)
	}
	reqBody := fmt.Sprintf(`{"data":{"text":%s}}`, textJSON)

	var resp struct {
		Data struct {
			GID string `json:"gid"`
		} `json:"data"`
	}
	if err := apiRequest(ctx, p.httpClient, http.MethodPost, storiesURL, strings.NewReader(reqBody),
		"Bearer "+pat, http.StatusCreated, "", "Asana", &resp); err != nil {
		return "", fmt.Errorf("failed to post claim comment: %w", err)
	}

	return resp.Data.GID, nil
}

// GetClaims reads all claim comments from an Asana task.
// Implements ProviderClaimManager.
func (p *AsanaProvider) GetClaims(ctx context.Context, repoPath string, issueID string) ([]ClaimInfo, error) {
	comments, err := p.GetIssueComments(ctx, repoPath, issueID)
	if err != nil {
		return nil, err
	}

	return getClaimsFromComments(comments), nil
}

// DeleteClaim deletes a claim comment (story) from an Asana task by its GID.
// Implements ProviderClaimManager.
func (p *AsanaProvider) DeleteClaim(ctx context.Context, repoPath string, issueID string, commentID string) error {
	pat, ok := resolveToken(asanaPATEnvVar, secrets.AsanaPATService)
	if !ok {
		return secrets.TokenNotFoundError(asanaPATEnvVar)
	}

	storyURL := fmt.Sprintf("%s/stories/%s", p.apiBase, commentID)
	return apiRequest(ctx, p.httpClient, http.MethodDelete, storyURL, nil,
		"Bearer "+pat, http.StatusOK, "", "Asana", nil)
}
