package issues

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	linearAPIBase      = "https://api.linear.app"
	linearAPIKeyEnvVar = "LINEAR_API_KEY"
	linearHTTPTimeout  = 30 * time.Second
)

// LinearTeam represents a Linear team with its ID and name.
type LinearTeam struct {
	ID   string
	Name string
}

// LinearProvider implements Provider for Linear Issues using the Linear GraphQL API.
type LinearProvider struct {
	config     LinearConfigProvider
	httpClient *http.Client
	apiBase    string // Override for testing; defaults to linearAPIBase
}

// NewLinearProvider creates a new Linear issue provider.
func NewLinearProvider(cfg LinearConfigProvider) *LinearProvider {
	return &LinearProvider{
		config: cfg,
		httpClient: &http.Client{
			Timeout: linearHTTPTimeout,
		},
		apiBase: linearAPIBase,
	}
}

// NewLinearProviderWithClient creates a new Linear issue provider with a custom HTTP client and API base URL (for testing).
func NewLinearProviderWithClient(cfg LinearConfigProvider, client *http.Client, apiBase string) *LinearProvider {
	if apiBase == "" {
		apiBase = linearAPIBase
	}
	return &LinearProvider{
		config:     cfg,
		httpClient: client,
		apiBase:    apiBase,
	}
}

// Name returns the human-readable name of this provider.
func (p *LinearProvider) Name() string {
	return "Linear Issues"
}

// Source returns the source type for this provider.
func (p *LinearProvider) Source() Source {
	return SourceLinear
}

// linearGraphQLRequest represents a GraphQL request body.
type linearGraphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

// linearIssue represents an issue from the Linear GraphQL API response.
type linearIssue struct {
	ID          string `json:"id"`
	Identifier  string `json:"identifier"`
	Title       string `json:"title"`
	Description string `json:"description"`
	URL         string `json:"url"`
}

// linearTeamIssuesResponse represents the Linear GraphQL response for team issues.
type linearTeamIssuesResponse struct {
	Data struct {
		Team struct {
			Issues struct {
				Nodes []linearIssue `json:"nodes"`
			} `json:"issues"`
		} `json:"team"`
	} `json:"data"`
}

// linearTeam represents a team from the Linear GraphQL API response.
type linearTeam struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// linearTeamsResponse represents the Linear GraphQL response for listing teams.
type linearTeamsResponse struct {
	Data struct {
		Teams struct {
			Nodes []linearTeam `json:"nodes"`
		} `json:"teams"`
	} `json:"data"`
}

// FetchIssues retrieves active issues from the Linear team.
// The filter.Team should be the Linear team ID.
func (p *LinearProvider) FetchIssues(ctx context.Context, repoPath string, filter FilterConfig) ([]Issue, error) {
	apiKey := os.Getenv(linearAPIKeyEnvVar)
	if apiKey == "" {
		return nil, fmt.Errorf("LINEAR_API_KEY environment variable not set")
	}

	projectID := filter.Team
	if projectID == "" {
		return nil, fmt.Errorf("Linear team ID not configured for this repository")
	}

	var query string
	variables := map[string]any{
		"teamId": projectID,
	}

	if filter.Label != "" {
		query = `query($teamId: String!, $label: String!) {
  team(id: $teamId) {
    issues(filter: {
      state: { type: { nin: ["completed", "canceled"] } }
      labels: { name: { eqIgnoreCase: $label } }
    }) {
      nodes {
        id
        identifier
        title
        description
        url
      }
    }
  }
}`
		variables["label"] = filter.Label
	} else {
		query = `query($teamId: String!) {
  team(id: $teamId) {
    issues(filter: { state: { type: { nin: ["completed", "canceled"] } } }) {
      nodes {
        id
        identifier
        title
        description
        url
      }
    }
  }
}`
	}

	var gqlResp linearTeamIssuesResponse
	if err := p.linearGraphQL(ctx, query, variables,
		"Linear API returned 403 Forbidden - check that your LINEAR_API_KEY has access to this team",
		&gqlResp); err != nil {
		return nil, err
	}

	nodes := gqlResp.Data.Team.Issues.Nodes
	issues := make([]Issue, len(nodes))
	for i, issue := range nodes {
		issues[i] = Issue{
			ID:     issue.Identifier,
			Title:  issue.Title,
			Body:   issue.Description,
			URL:    issue.URL,
			Source: SourceLinear,
		}
	}

	return issues, nil
}

// IsConfigured returns true if Linear is configured for the given repo.
// Requires both LINEAR_API_KEY env var and a team ID mapped to the repo.
func (p *LinearProvider) IsConfigured(repoPath string) bool {
	if os.Getenv(linearAPIKeyEnvVar) == "" {
		return false
	}
	return p.config.HasLinearTeam(repoPath)
}

// GenerateBranchName returns a branch name for the given Linear issue.
// Format: "linear-{identifier}" where identifier is lowercased (e.g., "linear-eng-123").
func (p *LinearProvider) GenerateBranchName(issue Issue) string {
	return fmt.Sprintf("linear-%s", strings.ToLower(issue.ID))
}

// GetPRLinkText returns the text to add to PR body to link/close the Linear issue.
// Linear supports auto-close via identifier mentions (e.g., "Fixes ENG-123").
func (p *LinearProvider) GetPRLinkText(issue Issue) string {
	return fmt.Sprintf("Fixes %s", issue.ID)
}

// linearIssueLabelsResponse represents the Linear GraphQL response for issue labels.
type linearIssueLabelsResponse struct {
	Data struct {
		Issue struct {
			ID     string `json:"id"`
			Labels struct {
				Nodes []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"nodes"`
			} `json:"labels"`
		} `json:"issue"`
	} `json:"data"`
}

// linearIssueLabelsQuery fetches a Linear issue's UUID and current labels by identifier.
const linearIssueLabelsQuery = `query($id: String!) {
  issue(id: $id) {
    id
    labels {
      nodes {
        id
        name
      }
    }
  }
}`

// linearCommentCreateMutation creates a comment on a Linear issue.
const linearCommentCreateMutation = `mutation($issueId: String!, $body: String!) {
  commentCreate(input: { issueId: $issueId, body: $body }) {
    success
  }
}`

// linearIssueUpdateMutation updates a Linear issue's labels.
const linearIssueUpdateMutation = `mutation($id: String!, $labelIds: [String!]!) {
  issueUpdate(id: $id, input: { labelIds: $labelIds }) {
    success
  }
}`

// linearGraphQL executes a GraphQL request against the Linear API.
// If forbiddenMsg is non-empty, a 403 response produces that specific error.
func (p *LinearProvider) linearGraphQL(ctx context.Context, query string, variables map[string]any, forbiddenMsg string, result any) error {
	apiKey := os.Getenv(linearAPIKeyEnvVar)
	if apiKey == "" {
		return fmt.Errorf("LINEAR_API_KEY environment variable not set")
	}

	gqlReq := linearGraphQLRequest{
		Query:     query,
		Variables: variables,
	}
	body, err := json.Marshal(gqlReq)
	if err != nil {
		return fmt.Errorf("failed to marshal GraphQL request: %w", err)
	}

	url := fmt.Sprintf("%s/graphql", p.apiBase)
	return apiRequest(ctx, p.httpClient, http.MethodPost, url, bytes.NewReader(body),
		apiKey, http.StatusOK, forbiddenMsg, "Linear", result)
}

// RemoveLabel removes a label from a Linear issue by name.
// It fetches the current labels to find the label ID, then updates the issue
// with the label removed.
// Implements ProviderActions.
func (p *LinearProvider) RemoveLabel(ctx context.Context, repoPath string, issueID string, label string) error {
	// Fetch the issue UUID and current labels.
	var labelsResp linearIssueLabelsResponse
	if err := p.linearGraphQL(ctx, linearIssueLabelsQuery, map[string]any{"id": issueID}, "", &labelsResp); err != nil {
		return fmt.Errorf("failed to fetch issue labels: %w", err)
	}

	issueUUID := labelsResp.Data.Issue.ID
	if issueUUID == "" {
		return fmt.Errorf("issue %q not found in Linear", issueID)
	}

	// Build updated label list excluding the target label.
	var updatedLabelIDs []string
	found := false
	for _, l := range labelsResp.Data.Issue.Labels.Nodes {
		if strings.EqualFold(l.Name, label) {
			found = true
			continue
		}
		updatedLabelIDs = append(updatedLabelIDs, l.ID)
	}
	if !found {
		// Label not on issue — nothing to do.
		return nil
	}

	// Update the issue with the remaining labels.
	if updatedLabelIDs == nil {
		updatedLabelIDs = []string{}
	}
	var updateResp struct {
		Data struct {
			IssueUpdate struct {
				Success bool `json:"success"`
			} `json:"issueUpdate"`
		} `json:"data"`
	}
	if err := p.linearGraphQL(ctx, linearIssueUpdateMutation, map[string]any{
		"id":       issueUUID,
		"labelIds": updatedLabelIDs,
	}, "", &updateResp); err != nil {
		return fmt.Errorf("failed to update issue labels: %w", err)
	}

	return nil
}

// Comment creates a comment on a Linear issue.
// Implements ProviderActions.
func (p *LinearProvider) Comment(ctx context.Context, repoPath string, issueID string, body string) error {
	// First, look up the issue UUID by identifier since commentCreate requires it.
	var issueResp struct {
		Data struct {
			Issue struct {
				ID string `json:"id"`
			} `json:"issue"`
		} `json:"data"`
	}
	lookupQuery := `query($id: String!) { issue(id: $id) { id } }`
	if err := p.linearGraphQL(ctx, lookupQuery, map[string]any{"id": issueID}, "", &issueResp); err != nil {
		return fmt.Errorf("failed to look up issue UUID: %w", err)
	}

	issueUUID := issueResp.Data.Issue.ID
	if issueUUID == "" {
		return fmt.Errorf("issue %q not found in Linear", issueID)
	}

	var commentResp struct {
		Data struct {
			CommentCreate struct {
				Success bool `json:"success"`
			} `json:"commentCreate"`
		} `json:"data"`
	}
	if err := p.linearGraphQL(ctx, linearCommentCreateMutation, map[string]any{
		"issueId": issueUUID,
		"body":    body,
	}, "", &commentResp); err != nil {
		return fmt.Errorf("failed to create comment: %w", err)
	}

	return nil
}

// FetchTeams retrieves all teams accessible to the user.
func (p *LinearProvider) FetchTeams(ctx context.Context) ([]LinearTeam, error) {
	var gqlResp linearTeamsResponse
	if err := p.linearGraphQL(ctx, `{ teams { nodes { id name } } }`, nil, "", &gqlResp); err != nil {
		return nil, err
	}

	nodes := gqlResp.Data.Teams.Nodes
	teams := make([]LinearTeam, len(nodes))
	for i, team := range nodes {
		teams[i] = LinearTeam{
			ID:   team.ID,
			Name: team.Name,
		}
	}

	return teams, nil
}
