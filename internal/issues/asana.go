package issues

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	asanaAPIBase     = "https://app.asana.com/api/1.0"
	asanaPATEnvVar   = "ASANA_PAT"
	asanaHTTPTimeout = 30 * time.Second
)

// AsanaProject represents an Asana project with its GID and name.
type AsanaProject struct {
	GID  string
	Name string
}

// AsanaProvider implements Provider for Asana Tasks using the Asana REST API.
type AsanaProvider struct {
	config     AsanaConfigProvider
	httpClient *http.Client
	apiBase    string // Override for testing; defaults to asanaAPIBase
}

// NewAsanaProvider creates a new Asana task provider.
func NewAsanaProvider(cfg AsanaConfigProvider) *AsanaProvider {
	return &AsanaProvider{
		config: cfg,
		httpClient: &http.Client{
			Timeout: asanaHTTPTimeout,
		},
		apiBase: asanaAPIBase,
	}
}

// NewAsanaProviderWithClient creates a new Asana task provider with a custom HTTP client and API base URL (for testing).
func NewAsanaProviderWithClient(cfg AsanaConfigProvider, client *http.Client, apiBase string) *AsanaProvider {
	if apiBase == "" {
		apiBase = asanaAPIBase
	}
	return &AsanaProvider{
		config:     cfg,
		httpClient: client,
		apiBase:    apiBase,
	}
}

// Name returns the human-readable name of this provider.
func (p *AsanaProvider) Name() string {
	return "Asana Tasks"
}

// Source returns the source type for this provider.
func (p *AsanaProvider) Source() Source {
	return SourceAsana
}

// asanaTag represents a tag on an Asana task.
type asanaTag struct {
	Name string `json:"name"`
}

// asanaTask represents a task from the Asana API response.
type asanaTask struct {
	GID       string     `json:"gid"`
	Name      string     `json:"name"`
	Notes     string     `json:"notes"`
	Permalink string     `json:"permalink_url"`
	Tags      []asanaTag `json:"tags"`
}

// asanaTasksResponse represents the Asana API response for listing tasks.
type asanaTasksResponse struct {
	Data []asanaTask `json:"data"`
}

// FetchIssues retrieves incomplete tasks from the Asana project.
// The filter.Project should be the Asana project GID.
func (p *AsanaProvider) FetchIssues(ctx context.Context, repoPath string, filter FilterConfig) ([]Issue, error) {
	pat := os.Getenv(asanaPATEnvVar)
	if pat == "" {
		return nil, fmt.Errorf("ASANA_PAT environment variable not set")
	}

	projectID := filter.Project
	if projectID == "" {
		return nil, fmt.Errorf("Asana project GID not configured for this repository")
	}

	// Fetch incomplete tasks from the project
	url := fmt.Sprintf("%s/projects/%s/tasks?opt_fields=gid,name,notes,permalink_url,tags.name&completed_since=now", p.apiBase, projectID)

	var tasksResp asanaTasksResponse
	if err := apiRequest(ctx, p.httpClient, http.MethodGet, url, nil,
		"Bearer "+pat, http.StatusOK,
		"Asana API returned 403 Forbidden - check that your ASANA_PAT has access to this project",
		"Asana", &tasksResp); err != nil {
		return nil, err
	}

	tasks := tasksResp.Data

	// Filter by tag if label is configured
	if filter.Label != "" {
		var filtered []asanaTask
		for _, task := range tasks {
			for _, tag := range task.Tags {
				if strings.EqualFold(tag.Name, filter.Label) {
					filtered = append(filtered, task)
					break
				}
			}
		}
		tasks = filtered
	}

	issues := make([]Issue, len(tasks))
	for i, task := range tasks {
		issues[i] = Issue{
			ID:     task.GID,
			Title:  task.Name,
			Body:   task.Notes,
			URL:    task.Permalink,
			Source: SourceAsana,
		}
	}

	return issues, nil
}

// IsConfigured returns true if Asana is configured for the given repo.
// Requires both ASANA_PAT env var and a project GID mapped to the repo.
func (p *AsanaProvider) IsConfigured(repoPath string) bool {
	// Check if PAT is set
	if os.Getenv(asanaPATEnvVar) == "" {
		return false
	}
	// Check if repo has a project mapped
	return p.config.HasAsanaProject(repoPath)
}

// slugifyRegex is used to generate URL-safe slugs from task names.
var slugifyRegex = regexp.MustCompile(`[^a-z0-9]+`)

// GenerateBranchName returns a branch name for the given Asana task.
// Format: "task-{slug}" where slug is derived from the task name.
func (p *AsanaProvider) GenerateBranchName(issue Issue) string {
	// Convert to lowercase and replace non-alphanumeric chars with hyphens
	slug := strings.ToLower(issue.Title)
	slug = slugifyRegex.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")

	// Limit length to keep branch names reasonable
	const maxSlugLen = 40
	if len(slug) > maxSlugLen {
		slug = slug[:maxSlugLen]
		// Don't end on a hyphen
		slug = strings.TrimRight(slug, "-")
	}

	// Fallback if slug is empty
	if slug == "" {
		return fmt.Sprintf("task-%s", issue.ID)
	}

	return fmt.Sprintf("task-%s", slug)
}

// asanaWorkspace represents a workspace from the Asana API.
type asanaWorkspace struct {
	GID  string `json:"gid"`
	Name string `json:"name"`
}

// asanaWorkspacesResponse represents the Asana API response for listing workspaces.
type asanaWorkspacesResponse struct {
	Data []asanaWorkspace `json:"data"`
}

// asanaProject represents a project from the Asana API.
type asanaProject struct {
	GID  string `json:"gid"`
	Name string `json:"name"`
}

// asanaNextPage represents the pagination info in Asana API responses.
type asanaNextPage struct {
	Offset string `json:"offset"`
	URI    string `json:"uri"`
	Path   string `json:"path"`
}

// asanaProjectsResponse represents the Asana API response for listing projects.
type asanaProjectsResponse struct {
	Data     []asanaProject `json:"data"`
	NextPage *asanaNextPage `json:"next_page"`
}

// FetchProjects retrieves all projects accessible to the user.
// If the user belongs to a single workspace, project names are returned directly.
// If multiple workspaces exist, names are prefixed with "WorkspaceName / ProjectName".
func (p *AsanaProvider) FetchProjects(ctx context.Context) ([]AsanaProject, error) {
	pat := os.Getenv(asanaPATEnvVar)
	if pat == "" {
		return nil, fmt.Errorf("ASANA_PAT environment variable not set")
	}

	workspaces, err := p.fetchWorkspaces(ctx, pat)
	if err != nil {
		return nil, err
	}

	if len(workspaces) == 0 {
		return nil, nil
	}

	multiWorkspace := len(workspaces) > 1

	var allProjects []AsanaProject
	for _, ws := range workspaces {
		projects, err := p.fetchWorkspaceProjects(ctx, pat, ws.GID)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch projects for workspace %q: %w", ws.Name, err)
		}
		for _, proj := range projects {
			name := proj.Name
			if multiWorkspace {
				name = ws.Name + " / " + proj.Name
			}
			allProjects = append(allProjects, AsanaProject{
				GID:  proj.GID,
				Name: name,
			})
		}
	}

	return allProjects, nil
}

// fetchWorkspaces retrieves all workspaces for the authenticated user.
func (p *AsanaProvider) fetchWorkspaces(ctx context.Context, pat string) ([]asanaWorkspace, error) {
	url := fmt.Sprintf("%s/workspaces", p.apiBase)

	var wsResp asanaWorkspacesResponse
	if err := apiRequest(ctx, p.httpClient, http.MethodGet, url, nil,
		"Bearer "+pat, http.StatusOK, "", "Asana", &wsResp); err != nil {
		return nil, err
	}

	return wsResp.Data, nil
}

// fetchWorkspaceProjects retrieves all projects in a workspace, handling pagination.
func (p *AsanaProvider) fetchWorkspaceProjects(ctx context.Context, pat, workspaceGID string) ([]asanaProject, error) {
	var allProjects []asanaProject
	baseURL := fmt.Sprintf("%s/workspaces/%s/projects?opt_fields=gid,name&limit=100", p.apiBase, workspaceGID)
	requestURL := baseURL

	for {
		projects, nextOffset, err := p.fetchProjectsPage(ctx, pat, requestURL)
		if err != nil {
			return nil, err
		}

		allProjects = append(allProjects, projects...)

		if nextOffset == "" {
			break
		}

		requestURL = baseURL + "&offset=" + nextOffset
	}

	return allProjects, nil
}

// fetchProjectsPage fetches a single page of projects and returns the projects and the next page offset.
func (p *AsanaProvider) fetchProjectsPage(ctx context.Context, pat, requestURL string) ([]asanaProject, string, error) {
	var projResp asanaProjectsResponse
	if err := apiRequest(ctx, p.httpClient, http.MethodGet, requestURL, nil,
		"Bearer "+pat, http.StatusOK, "", "Asana", &projResp); err != nil {
		return nil, "", err
	}

	var nextOffset string
	if projResp.NextPage != nil {
		nextOffset = projResp.NextPage.Offset
	}

	return projResp.Data, nextOffset, nil
}

// GetPRLinkText returns empty string for Asana tasks.
// Asana doesn't support auto-closing tasks via PR merge.
func (p *AsanaProvider) GetPRLinkText(issue Issue) string {
	// Asana doesn't have auto-close support via commit messages.
	// Users can manually link PRs in Asana or use the Asana GitHub integration.
	return ""
}

// asanaTagsResponse represents the Asana API response for task tags.
type asanaTagsResponse struct {
	Data []asanaTag `json:"data"`
}

// asanaTagWithGID is a tag with both name and GID.
type asanaTagWithGID struct {
	GID  string `json:"gid"`
	Name string `json:"name"`
}

// asanaTagsWithGIDResponse is the response for fetching tags with GIDs.
type asanaTagsWithGIDResponse struct {
	Data []asanaTagWithGID `json:"data"`
}

// RemoveLabel removes a tag from an Asana task by name.
// It first fetches the task's tags to find the GID of the matching tag,
// then removes it via the Asana API.
// Implements ProviderActions.
func (p *AsanaProvider) RemoveLabel(ctx context.Context, repoPath string, issueID string, label string) error {
	pat := os.Getenv(asanaPATEnvVar)
	if pat == "" {
		return fmt.Errorf("ASANA_PAT environment variable not set")
	}

	// Fetch current tags on the task to find the GID for the target label.
	tagsURL := fmt.Sprintf("%s/tasks/%s?opt_fields=tags.gid,tags.name", p.apiBase, issueID)

	type taskTagsResponse struct {
		Data struct {
			Tags []asanaTagWithGID `json:"tags"`
		} `json:"data"`
	}

	var tagsResp taskTagsResponse
	if err := apiRequest(ctx, p.httpClient, http.MethodGet, tagsURL, nil,
		"Bearer "+pat, http.StatusOK, "", "Asana", &tagsResp); err != nil {
		return err
	}

	// Find the tag GID matching the label name.
	tagGID := ""
	for _, tag := range tagsResp.Data.Tags {
		if strings.EqualFold(tag.Name, label) {
			tagGID = tag.GID
			break
		}
	}
	if tagGID == "" {
		// Tag not found on task — nothing to remove.
		return nil
	}

	// Remove the tag from the task.
	removeURL := fmt.Sprintf("%s/tasks/%s/removeTag", p.apiBase, issueID)
	tagJSON, _ := json.Marshal(tagGID)
	removeBody := fmt.Sprintf(`{"data":{"tag":%s}}`, tagJSON)

	return apiRequest(ctx, p.httpClient, http.MethodPost, removeURL, strings.NewReader(removeBody),
		"Bearer "+pat, http.StatusOK, "", "Asana", nil)
}

// asanaTaskTagsResponse is the response when fetching a task's tags.
type asanaTaskTagsResponse struct {
	Data struct {
		Tags []asanaTag `json:"tags"`
	} `json:"data"`
}

// CheckIssueHasLabel returns true if the Asana task has a tag matching label.
// Implements ProviderGateChecker.
func (p *AsanaProvider) CheckIssueHasLabel(ctx context.Context, repoPath string, issueID string, label string) (bool, error) {
	pat := os.Getenv(asanaPATEnvVar)
	if pat == "" {
		return false, fmt.Errorf("ASANA_PAT environment variable not set")
	}

	url := fmt.Sprintf("%s/tasks/%s?opt_fields=tags.name", p.apiBase, issueID)

	var tagsResp asanaTaskTagsResponse
	if err := apiRequest(ctx, p.httpClient, http.MethodGet, url, nil,
		"Bearer "+pat, http.StatusOK, "", "Asana", &tagsResp); err != nil {
		return false, err
	}

	for _, tag := range tagsResp.Data.Tags {
		if strings.EqualFold(tag.Name, label) {
			return true, nil
		}
	}
	return false, nil
}

// asanaStory represents a single story (comment) on an Asana task.
type asanaStory struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	CreatedAt string `json:"created_at"`
	CreatedBy struct {
		Name string `json:"name"`
	} `json:"created_by"`
}

// asanaStoriesResponse is the response when fetching a task's stories.
type asanaStoriesResponse struct {
	Data []asanaStory `json:"data"`
}

// GetIssueComments returns all comments (stories of type "comment") on an Asana task.
// Implements ProviderGateChecker.
func (p *AsanaProvider) GetIssueComments(ctx context.Context, repoPath string, issueID string) ([]IssueComment, error) {
	pat := os.Getenv(asanaPATEnvVar)
	if pat == "" {
		return nil, fmt.Errorf("ASANA_PAT environment variable not set")
	}

	url := fmt.Sprintf("%s/tasks/%s/stories?opt_fields=type,text,created_at,created_by.name", p.apiBase, issueID)

	var storiesResp asanaStoriesResponse
	if err := apiRequest(ctx, p.httpClient, http.MethodGet, url, nil,
		"Bearer "+pat, http.StatusOK, "", "Asana", &storiesResp); err != nil {
		return nil, err
	}

	var comments []IssueComment
	for _, story := range storiesResp.Data {
		if story.Type != "comment" {
			continue
		}
		if story.Text == "" {
			continue
		}
		createdAt, _ := time.Parse(time.RFC3339, story.CreatedAt)
		comments = append(comments, IssueComment{
			Author:    story.CreatedBy.Name,
			Body:      story.Text,
			CreatedAt: createdAt,
		})
	}
	return comments, nil
}

// Comment adds a comment (story) to an Asana task.
// Implements ProviderActions.
func (p *AsanaProvider) Comment(ctx context.Context, repoPath string, issueID string, body string) error {
	pat := os.Getenv(asanaPATEnvVar)
	if pat == "" {
		return fmt.Errorf("ASANA_PAT environment variable not set")
	}

	storiesURL := fmt.Sprintf("%s/tasks/%s/stories", p.apiBase, issueID)
	textJSON, _ := json.Marshal(body)
	reqBody := fmt.Sprintf(`{"data":{"text":%s}}`, textJSON)

	return apiRequest(ctx, p.httpClient, http.MethodPost, storiesURL, strings.NewReader(reqBody),
		"Bearer "+pat, http.StatusCreated, "", "Asana", nil)
}
