// Package issues provides a generic interface for fetching issues from multiple sources
// (GitHub, Asana, etc.) to create Plural sessions.
package issues

import (
	"context"
	"time"
)

// Source identifies the origin of an issue (GitHub, Asana, Linear, etc.)
type Source string

const (
	SourceGitHub Source = "github"
	SourceAsana  Source = "asana"
	SourceLinear Source = "linear"
)

// Issue represents a generic issue/task from any supported source.
type Issue struct {
	ID     string // Unique identifier ("123" for GitHub, "1234567890123" for Asana)
	Title  string
	Body   string
	URL    string
	Source Source
}

// FilterConfig holds provider-specific filter parameters for fetching issues.
type FilterConfig struct {
	Label   string // Tag/label name to filter by (empty = no filtering)
	Project string // Asana: project GID
	Team    string // Linear: team ID
}

// Provider defines the interface for fetching issues from different sources.
type Provider interface {
	// Name returns the human-readable name of this provider (e.g., "GitHub Issues", "Asana Tasks")
	Name() string

	// Source returns the source type for this provider
	Source() Source

	// FetchIssues retrieves open issues/tasks for the given repository.
	// The filter parameter holds provider-specific filtering options:
	//   - GitHub: filter is unused (GitHub filtering happens in the daemon via gh CLI)
	//   - Asana: filter.Project is the Asana project GID
	//   - Linear: filter.Team is the Linear team ID
	FetchIssues(ctx context.Context, repoPath string, filter FilterConfig) ([]Issue, error)

	// IsConfigured returns true if this provider is configured and usable for the given repo.
	// For GitHub: always true (gh CLI is a prerequisite)
	// For Asana: true if ASANA_PAT env var is set AND repo has a mapped project
	// For Linear: true if LINEAR_API_KEY env var is set AND repo has a mapped team
	IsConfigured(repoPath string) bool

	// GenerateBranchName returns a branch name for the given issue.
	// For GitHub: "issue-{number}"
	// For Asana: "task-{slug}" where slug is derived from task name
	// For Linear: "linear-{identifier}" where identifier is lowercased (e.g., "linear-eng-123")
	GenerateBranchName(issue Issue) string

	// GetPRLinkText returns the text to add to PR body to link/close the issue.
	// For GitHub: "Fixes #123"
	// For Asana: "" (Asana doesn't support auto-close via commit message)
	// For Linear: "Fixes ENG-123" (Linear supports auto-close via identifier mentions)
	GetPRLinkText(issue Issue) string
}

// ProviderActions extends Provider with write operations for issue management.
// Providers that support label removal and commenting should implement this interface.
// Operations are expected to be best-effort; callers should log but not fail on errors.
type ProviderActions interface {
	// RemoveLabel removes a label/tag from an issue/task.
	RemoveLabel(ctx context.Context, repoPath string, issueID string, label string) error

	// Comment adds a comment/story to an issue/task.
	Comment(ctx context.Context, repoPath string, issueID string, body string) error
}

// ProviderRegistry holds all available issue providers.
type ProviderRegistry struct {
	providers []Provider
}

// NewProviderRegistry creates a new registry with the given providers.
func NewProviderRegistry(providers ...Provider) *ProviderRegistry {
	return &ProviderRegistry{providers: providers}
}

// GetConfiguredProviders returns all providers that are configured for the given repo.
func (r *ProviderRegistry) GetConfiguredProviders(repoPath string) []Provider {
	var configured []Provider
	for _, p := range r.providers {
		if p.IsConfigured(repoPath) {
			configured = append(configured, p)
		}
	}
	return configured
}

// GetProvider returns the provider for the given source, or nil if not found.
func (r *ProviderRegistry) GetProvider(source Source) Provider {
	for _, p := range r.providers {
		if p.Source() == source {
			return p
		}
	}
	return nil
}

// AllProviders returns all registered providers.
func (r *ProviderRegistry) AllProviders() []Provider {
	return r.providers
}

// IssueComment represents a comment on an issue from any supported source.
type IssueComment struct {
	Author    string    // Username or display name
	Body      string    // Comment text
	CreatedAt time.Time // When the comment was posted
}

// ProviderGateChecker extends Provider with operations needed for gate/approval events.
// Providers that support label checking and comment fetching implement this interface,
// enabling gate.approved and plan.user_replied events to work across all sources.
type ProviderGateChecker interface {
	// CheckIssueHasLabel returns true if the issue/task currently has the given label or tag.
	CheckIssueHasLabel(ctx context.Context, repoPath string, issueID string, label string) (bool, error)

	// GetIssueComments returns all comments on the issue/task, ordered oldest first.
	GetIssueComments(ctx context.Context, repoPath string, issueID string) ([]IssueComment, error)
}
