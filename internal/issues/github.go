package issues

import (
	"context"
	"fmt"
	"strconv"

	"github.com/zhubert/erg/internal/git"
)

// GitHubProvider implements Provider for GitHub Issues using the gh CLI.
type GitHubProvider struct {
	gitService *git.GitService
}

// NewGitHubProvider creates a new GitHub issue provider.
func NewGitHubProvider(gitService *git.GitService) *GitHubProvider {
	return &GitHubProvider{gitService: gitService}
}

// Name returns the human-readable name of this provider.
func (p *GitHubProvider) Name() string {
	return "GitHub Issues"
}

// Source returns the source type for this provider.
func (p *GitHubProvider) Source() Source {
	return SourceGitHub
}

// FetchIssues retrieves open GitHub issues for the given repository.
// The filter parameter is unused by GitHub (GitHub filtering happens in the daemon via gh CLI).
func (p *GitHubProvider) FetchIssues(ctx context.Context, repoPath string, filter FilterConfig) ([]Issue, error) {
	ghIssues, err := p.gitService.FetchGitHubIssues(ctx, repoPath)
	if err != nil {
		return nil, err
	}

	issues := make([]Issue, len(ghIssues))
	for i, gh := range ghIssues {
		issues[i] = Issue{
			ID:     strconv.Itoa(gh.Number),
			Title:  gh.Title,
			Body:   gh.Body,
			URL:    gh.URL,
			Source: SourceGitHub,
		}
	}
	return issues, nil
}

// IsConfigured returns true - GitHub is always available via gh CLI.
// The gh CLI is checked as a prerequisite when the app starts.
func (p *GitHubProvider) IsConfigured(repoPath string) bool {
	return true
}

// GenerateBranchName returns a branch name for the given GitHub issue.
// Format: "issue-{number}"
func (p *GitHubProvider) GenerateBranchName(issue Issue) string {
	return fmt.Sprintf("issue-%s", issue.ID)
}

// GetPRLinkText returns the text to add to PR body to link/close the issue.
// Format: "Fixes #{number}"
func (p *GitHubProvider) GetPRLinkText(issue Issue) string {
	return fmt.Sprintf("Fixes #%s", issue.ID)
}

// RemoveLabel removes a label from a GitHub issue.
// Implements ProviderActions.
func (p *GitHubProvider) RemoveLabel(ctx context.Context, repoPath string, issueID string, label string) error {
	issueNum, err := strconv.Atoi(issueID)
	if err != nil {
		return fmt.Errorf("invalid GitHub issue ID %q: %w", issueID, err)
	}
	return p.gitService.RemoveIssueLabel(ctx, repoPath, issueNum, label)
}

// Comment adds a comment to a GitHub issue.
// Implements ProviderActions.
func (p *GitHubProvider) Comment(ctx context.Context, repoPath string, issueID string, body string) error {
	issueNum, err := strconv.Atoi(issueID)
	if err != nil {
		return fmt.Errorf("invalid GitHub issue ID %q: %w", issueID, err)
	}
	return p.gitService.CommentOnIssue(ctx, repoPath, issueNum, body)
}

// CheckIssueHasLabel returns true if the GitHub issue has the given label.
// Implements ProviderGateChecker.
func (p *GitHubProvider) CheckIssueHasLabel(ctx context.Context, repoPath string, issueID string, label string) (bool, error) {
	issueNum, err := strconv.Atoi(issueID)
	if err != nil {
		return false, fmt.Errorf("invalid GitHub issue ID %q: %w", issueID, err)
	}
	return p.gitService.CheckIssueHasLabel(ctx, repoPath, issueNum, label)
}

// GetIssueComments returns all comments on a GitHub issue, ordered oldest first.
// Uses both the gh CLI and REST API to return comments with IDs (needed for
// ProviderCommentUpdater support).
// Implements ProviderGateChecker.
func (p *GitHubProvider) GetIssueComments(ctx context.Context, repoPath string, issueID string) ([]IssueComment, error) {
	issueNum, err := strconv.Atoi(issueID)
	if err != nil {
		return nil, fmt.Errorf("invalid GitHub issue ID %q: %w", issueID, err)
	}
	gitComments, err := p.gitService.GetIssueCommentsWithIDs(ctx, repoPath, issueNum)
	if err != nil {
		return nil, err
	}
	comments := make([]IssueComment, len(gitComments))
	for i, gc := range gitComments {
		comments[i] = IssueComment{
			ID:        strconv.FormatInt(gc.ID, 10),
			Author:    gc.Author,
			Body:      gc.Body,
			CreatedAt: gc.CreatedAt,
		}
	}
	return comments, nil
}

// UpdateComment updates an existing GitHub issue comment by its ID.
// Implements ProviderCommentUpdater.
func (p *GitHubProvider) UpdateComment(ctx context.Context, repoPath string, issueID string, commentID string, body string) error {
	id, err := strconv.ParseInt(commentID, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid GitHub comment ID %q: %w", commentID, err)
	}
	return p.gitService.UpdateIssueComment(ctx, repoPath, id, body)
}

// GetIssue fetches a single GitHub issue by its ID (issue number as string).
// Implements IssueGetter.
func (p *GitHubProvider) GetIssue(ctx context.Context, repoPath string, id string) (*Issue, error) {
	issueNum, err := strconv.Atoi(id)
	if err != nil {
		return nil, fmt.Errorf("invalid GitHub issue ID %q: expected an integer issue number", id)
	}
	gh, err := p.gitService.GetGitHubIssue(ctx, repoPath, issueNum)
	if err != nil {
		return nil, err
	}
	return &Issue{
		ID:     strconv.Itoa(gh.Number),
		Title:  gh.Title,
		Body:   gh.Body,
		URL:    gh.URL,
		Source: SourceGitHub,
	}, nil
}

// IsIssueClosed returns true if the GitHub issue is in CLOSED state.
// Implements IssueStateChecker.
func (p *GitHubProvider) IsIssueClosed(ctx context.Context, repoPath string, issueID string) (bool, error) {
	state, err := p.gitService.GetIssueState(ctx, repoPath, issueID)
	if err != nil {
		return false, err
	}
	return state == "CLOSED", nil
}

// GetIssueNumber returns the issue number as an int (for backwards compatibility).
// Returns 0 if the ID is not a valid number.
func GetIssueNumber(issue Issue) int {
	if issue.Source != SourceGitHub {
		return 0
	}
	num, err := strconv.Atoi(issue.ID)
	if err != nil {
		return 0
	}
	return num
}
