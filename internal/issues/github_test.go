package issues

import (
	"context"
	"fmt"
	"testing"

	"github.com/zhubert/erg/internal/exec"
	"github.com/zhubert/erg/internal/git"
)

func TestGitHubProvider_Name(t *testing.T) {
	p := NewGitHubProvider(nil)
	if p.Name() != "GitHub Issues" {
		t.Errorf("expected 'GitHub Issues', got '%s'", p.Name())
	}
}

func TestGitHubProvider_Source(t *testing.T) {
	p := NewGitHubProvider(nil)
	if p.Source() != SourceGitHub {
		t.Errorf("expected SourceGitHub, got '%s'", p.Source())
	}
}

func TestGitHubProvider_IsConfigured(t *testing.T) {
	p := NewGitHubProvider(nil)
	// GitHub is always configured (gh CLI is a prerequisite)
	if !p.IsConfigured("/any/repo") {
		t.Error("expected GitHub to always be configured")
	}
}

func TestGitHubProvider_GenerateBranchName(t *testing.T) {
	p := NewGitHubProvider(nil)

	tests := []struct {
		issue    Issue
		expected string
	}{
		{Issue{ID: "123", Source: SourceGitHub}, "issue-123"},
		{Issue{ID: "1", Source: SourceGitHub}, "issue-1"},
		{Issue{ID: "99999", Source: SourceGitHub}, "issue-99999"},
	}

	for _, tc := range tests {
		result := p.GenerateBranchName(tc.issue)
		if result != tc.expected {
			t.Errorf("GenerateBranchName(%v) = %s, expected %s", tc.issue.ID, result, tc.expected)
		}
	}
}

func TestGitHubProvider_GetPRLinkText(t *testing.T) {
	p := NewGitHubProvider(nil)

	tests := []struct {
		issue    Issue
		expected string
	}{
		{Issue{ID: "123", Source: SourceGitHub}, "Fixes #123"},
		{Issue{ID: "1", Source: SourceGitHub}, "Fixes #1"},
		{Issue{ID: "99999", Source: SourceGitHub}, "Fixes #99999"},
	}

	for _, tc := range tests {
		result := p.GetPRLinkText(tc.issue)
		if result != tc.expected {
			t.Errorf("GetPRLinkText(%v) = %s, expected %s", tc.issue.ID, result, tc.expected)
		}
	}
}

func TestGitHubProvider_RemoveLabel(t *testing.T) {
	mock := exec.NewMockExecutor(nil)
	mock.AddExactMatch("gh", []string{"issue", "edit", "42", "--remove-label", "queued"}, exec.MockResponse{})

	gitSvc := git.NewGitServiceWithExecutor(mock)
	p := NewGitHubProvider(gitSvc)

	err := p.RemoveLabel(context.Background(), "/repo", "42", "queued")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	calls := mock.GetCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
}

func TestGitHubProvider_RemoveLabel_InvalidID(t *testing.T) {
	p := NewGitHubProvider(nil)

	err := p.RemoveLabel(context.Background(), "/repo", "not-a-number", "queued")
	if err == nil {
		t.Error("expected error for invalid issue ID")
	}
}

func TestGitHubProvider_RemoveLabel_CLIError(t *testing.T) {
	mock := exec.NewMockExecutor(nil)
	mock.AddExactMatch("gh", []string{"issue", "edit", "42", "--remove-label", "queued"},
		exec.MockResponse{Err: fmt.Errorf("gh: failed")})

	gitSvc := git.NewGitServiceWithExecutor(mock)
	p := NewGitHubProvider(gitSvc)

	err := p.RemoveLabel(context.Background(), "/repo", "42", "queued")
	if err == nil {
		t.Error("expected error from CLI failure")
	}
}

func TestGitHubProvider_Comment(t *testing.T) {
	mock := exec.NewMockExecutor(nil)
	mock.AddExactMatch("gh", []string{"issue", "comment", "42", "--body", "hello"}, exec.MockResponse{})

	gitSvc := git.NewGitServiceWithExecutor(mock)
	p := NewGitHubProvider(gitSvc)

	err := p.Comment(context.Background(), "/repo", "42", "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	calls := mock.GetCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
}

func TestGitHubProvider_Comment_InvalidID(t *testing.T) {
	p := NewGitHubProvider(nil)

	err := p.Comment(context.Background(), "/repo", "not-a-number", "hello")
	if err == nil {
		t.Error("expected error for invalid issue ID")
	}
}

func TestGitHubProvider_ImplementsProviderActions(t *testing.T) {
	var _ ProviderActions = (*GitHubProvider)(nil)
}

func TestGitHubProvider_ImplementsProviderGateChecker(t *testing.T) {
	var _ ProviderGateChecker = (*GitHubProvider)(nil)
}

func TestGitHubProvider_ImplementsProviderCommentUpdater(t *testing.T) {
	var _ ProviderCommentUpdater = (*GitHubProvider)(nil)
}

func TestGitHubProvider_CheckIssueHasLabel(t *testing.T) {
	mock := exec.NewMockExecutor(nil)
	mock.AddExactMatch("gh", []string{"issue", "view", "42", "--json", "labels"},
		exec.MockResponse{Stdout: []byte(`{"labels":[{"name":"queued"},{"name":"bug"}]}`)})

	gitSvc := git.NewGitServiceWithExecutor(mock)
	p := NewGitHubProvider(gitSvc)

	has, err := p.CheckIssueHasLabel(context.Background(), "/repo", "42", "queued")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !has {
		t.Error("expected issue to have label 'queued'")
	}
}

func TestGitHubProvider_CheckIssueHasLabel_NotFound(t *testing.T) {
	mock := exec.NewMockExecutor(nil)
	mock.AddExactMatch("gh", []string{"issue", "view", "42", "--json", "labels"},
		exec.MockResponse{Stdout: []byte(`{"labels":[{"name":"bug"}]}`)})

	gitSvc := git.NewGitServiceWithExecutor(mock)
	p := NewGitHubProvider(gitSvc)

	has, err := p.CheckIssueHasLabel(context.Background(), "/repo", "42", "queued")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if has {
		t.Error("expected issue to NOT have label 'queued'")
	}
}

func TestGitHubProvider_CheckIssueHasLabel_InvalidID(t *testing.T) {
	p := NewGitHubProvider(nil)

	_, err := p.CheckIssueHasLabel(context.Background(), "/repo", "not-a-number", "queued")
	if err == nil {
		t.Error("expected error for invalid issue ID")
	}
}

func TestGitHubProvider_GetIssueComments(t *testing.T) {
	mock := exec.NewMockExecutor(nil)
	mock.AddExactMatch("gh", []string{"api", "repos/:owner/:repo/issues/42/comments"},
		exec.MockResponse{Stdout: []byte(`[{"id":100,"body":"hello","user":{"login":"alice"},"created_at":"2024-01-01T00:00:00Z"},{"id":101,"body":"world","user":{"login":"bob"},"created_at":"2024-01-02T00:00:00Z"}]`)})

	gitSvc := git.NewGitServiceWithExecutor(mock)
	p := NewGitHubProvider(gitSvc)

	comments, err := p.GetIssueComments(context.Background(), "/repo", "42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(comments) != 2 {
		t.Fatalf("expected 2 comments, got %d", len(comments))
	}
	if comments[0].ID != "100" {
		t.Errorf("expected comment ID '100', got '%s'", comments[0].ID)
	}
	if comments[0].Author != "alice" {
		t.Errorf("expected author 'alice', got '%s'", comments[0].Author)
	}
	if comments[0].Body != "hello" {
		t.Errorf("expected body 'hello', got '%s'", comments[0].Body)
	}
}

func TestGitHubProvider_GetIssueComments_InvalidID(t *testing.T) {
	p := NewGitHubProvider(nil)

	_, err := p.GetIssueComments(context.Background(), "/repo", "not-a-number")
	if err == nil {
		t.Error("expected error for invalid issue ID")
	}
}

func TestGitHubProvider_UpdateComment(t *testing.T) {
	mock := exec.NewMockExecutor(nil)
	mock.AddExactMatch("gh", []string{"api", "--method", "PATCH", "repos/:owner/:repo/issues/comments/100", "-f", "body=updated"},
		exec.MockResponse{})

	gitSvc := git.NewGitServiceWithExecutor(mock)
	p := NewGitHubProvider(gitSvc)

	err := p.UpdateComment(context.Background(), "/repo", "42", "100", "updated")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGitHubProvider_UpdateComment_InvalidCommentID(t *testing.T) {
	p := NewGitHubProvider(nil)

	err := p.UpdateComment(context.Background(), "/repo", "42", "not-a-number", "body")
	if err == nil {
		t.Error("expected error for invalid comment ID")
	}
}

func TestGetIssueNumber(t *testing.T) {
	tests := []struct {
		name     string
		issue    Issue
		expected int
	}{
		{"GitHub issue with valid number", Issue{ID: "123", Source: SourceGitHub}, 123},
		{"GitHub issue with 1", Issue{ID: "1", Source: SourceGitHub}, 1},
		{"Asana task returns 0", Issue{ID: "1234567890123", Source: SourceAsana}, 0},
		{"Invalid number returns 0", Issue{ID: "abc", Source: SourceGitHub}, 0},
		{"Empty ID returns 0", Issue{ID: "", Source: SourceGitHub}, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := GetIssueNumber(tc.issue)
			if result != tc.expected {
				t.Errorf("GetIssueNumber(%v) = %d, expected %d", tc.issue, result, tc.expected)
			}
		})
	}
}
