package issues

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/zhubert/erg/internal/secrets"
)

// BoardSection represents a section or workflow state on a project board.
type BoardSection struct {
	ID   string
	Name string
}

// boardAsanaBase and boardLinearBase are the API base URLs used by the board
// functions. They default to the production URLs and can be overridden in tests.
var (
	boardAsanaBase  = asanaAPIBase
	boardLinearBase = linearAPIBase
)

// newBoardHTTPClient returns a short-lived HTTP client for one-off board API calls
// during configure.
func newBoardHTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

// ListAsanaSections returns all sections in an Asana project.
func ListAsanaSections(ctx context.Context, projectGID string) ([]BoardSection, error) {
	pat, ok := resolveToken(asanaPATEnvVar, secrets.AsanaPATService)
	if !ok {
		return nil, secrets.TokenNotFoundError(asanaPATEnvVar)
	}

	url := fmt.Sprintf("%s/projects/%s/sections?opt_fields=gid,name", boardAsanaBase, projectGID)
	var resp asanaSectionsResponse
	if err := apiRequest(ctx, newBoardHTTPClient(), http.MethodGet, url, nil,
		"Bearer "+pat, http.StatusOK, "", "Asana", &resp); err != nil {
		return nil, err
	}

	sections := make([]BoardSection, len(resp.Data))
	for i, s := range resp.Data {
		sections[i] = BoardSection{ID: s.GID, Name: s.Name}
	}
	return sections, nil
}

// CreateAsanaSection creates a new section in an Asana project.
// If insertBeforeID is non-empty, the section is placed before that section.
func CreateAsanaSection(ctx context.Context, projectGID, name, insertBeforeID string) error {
	pat, ok := resolveToken(asanaPATEnvVar, secrets.AsanaPATService)
	if !ok {
		return secrets.TokenNotFoundError(asanaPATEnvVar)
	}

	url := fmt.Sprintf("%s/projects/%s/sections", boardAsanaBase, projectGID)
	body := fmt.Sprintf(`{"data":{"name":%q}}`, name)
	if insertBeforeID != "" {
		body = fmt.Sprintf(`{"data":{"name":%q,"insert_before":%q}}`, name, insertBeforeID)
	}

	return apiRequest(ctx, newBoardHTTPClient(), http.MethodPost, url, strings.NewReader(body),
		"Bearer "+pat, http.StatusCreated, "", "Asana", nil)
}

// ListLinearWorkflowStates returns all workflow states for a Linear team.
func ListLinearWorkflowStates(ctx context.Context, teamID string) ([]BoardSection, error) {
	p := &LinearProvider{
		httpClient: newBoardHTTPClient(),
		apiBase:    boardLinearBase,
	}

	var statesResp linearTeamStatesResponse
	if err := p.linearGraphQL(ctx, linearTeamStatesQuery, map[string]any{"teamId": teamID}, "", &statesResp); err != nil {
		return nil, err
	}

	nodes := statesResp.Data.Team.States.Nodes
	states := make([]BoardSection, len(nodes))
	for i, s := range nodes {
		states[i] = BoardSection{ID: s.ID, Name: s.Name}
	}
	return states, nil
}

// CreateLinearWorkflowState creates a new workflow state for a Linear team.
// The stateType should be one of: "started", "unstarted", "completed", "canceled", "backlog", "triage".
func CreateLinearWorkflowState(ctx context.Context, teamID, name, stateType, color string) error {
	p := &LinearProvider{
		httpClient: newBoardHTTPClient(),
		apiBase:    boardLinearBase,
	}

	mutation := `mutation($teamId: String!, $name: String!, $type: String!, $color: String!) {
  workflowStateCreate(input: { teamId: $teamId, name: $name, type: $type, color: $color }) {
    success
  }
}`

	var resp struct {
		Data struct {
			WorkflowStateCreate struct {
				Success bool `json:"success"`
			} `json:"workflowStateCreate"`
		} `json:"data"`
	}

	if err := p.linearGraphQL(ctx, mutation, map[string]any{
		"teamId": teamID,
		"name":   name,
		"type":   stateType,
		"color":  color,
	}, "", &resp); err != nil {
		return err
	}

	if !resp.Data.WorkflowStateCreate.Success {
		return fmt.Errorf("Linear API returned success=false creating state %q", name)
	}

	return nil
}

// FindSectionByName searches for a section by name (case-insensitive) in the given list.
// Returns the section and true if found, zero value and false otherwise.
func FindSectionByName(sections []BoardSection, name string) (BoardSection, bool) {
	for _, s := range sections {
		if strings.EqualFold(s.Name, name) {
			return s, true
		}
	}
	return BoardSection{}, false
}
