package worker

import (
	"strings"
	"testing"

	"github.com/zhubert/erg/internal/config"
	"github.com/zhubert/erg/internal/git"
)

func TestTrimURL(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect string
	}{
		{"empty string", "", ""},
		{"whitespace only", "   \n\t  ", ""},
		{"valid HTTPS URL", "https://github.com/owner/repo/pull/1", "https://github.com/owner/repo/pull/1"},
		{"HTTPS URL with whitespace", "  https://github.com/owner/repo/pull/1  \n", "https://github.com/owner/repo/pull/1"},
		{"HTTP URL (no match)", "http://github.com/owner/repo/pull/1", ""},
		{"non-URL text", "Created pull request", ""},
		{"URL in middle of text", "See https://github.com/pull/1 for details", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TrimURL(tt.input)
			if got != tt.expect {
				t.Errorf("TrimURL(%q) = %q, want %q", tt.input, got, tt.expect)
			}
		})
	}
}

func TestFormatPRCommentsPrompt(t *testing.T) {
	t.Run("single comment", func(t *testing.T) {
		comments := []git.PRReviewComment{
			{Author: "alice", Body: "Fix this bug", Path: "main.go", Line: 42},
		}
		result := FormatPRCommentsPrompt(comments)

		if !strings.Contains(result, "1 comment(s)") {
			t.Error("expected comment count")
		}
		if !strings.Contains(result, "@alice") {
			t.Error("expected author mention")
		}
		if !strings.Contains(result, "main.go:42") {
			t.Error("expected file:line")
		}
		if !strings.Contains(result, "Fix this bug") {
			t.Error("expected comment body")
		}
	})

	t.Run("multiple comments", func(t *testing.T) {
		comments := []git.PRReviewComment{
			{Author: "alice", Body: "Fix this"},
			{Author: "bob", Body: "Also fix that"},
		}
		result := FormatPRCommentsPrompt(comments)

		if !strings.Contains(result, "2 comment(s)") {
			t.Error("expected 2 comments count")
		}
		if !strings.Contains(result, "Comment 1") {
			t.Error("expected Comment 1")
		}
		if !strings.Contains(result, "Comment 2") {
			t.Error("expected Comment 2")
		}
	})

	t.Run("comment without author", func(t *testing.T) {
		comments := []git.PRReviewComment{
			{Body: "Fix this"},
		}
		result := FormatPRCommentsPrompt(comments)

		if strings.Contains(result, "by @") {
			t.Error("expected no author mention for empty author")
		}
	})

	t.Run("comment with path but no line", func(t *testing.T) {
		comments := []git.PRReviewComment{
			{Body: "General file comment", Path: "README.md"},
		}
		result := FormatPRCommentsPrompt(comments)

		if !strings.Contains(result, "File: README.md") {
			t.Error("expected file path without line")
		}
		if strings.Contains(result, "README.md:0") {
			t.Error("expected no :0 suffix")
		}
	})

	t.Run("comment without path or line", func(t *testing.T) {
		comments := []git.PRReviewComment{
			{Body: "Top-level comment"},
		}
		result := FormatPRCommentsPrompt(comments)

		if strings.Contains(result, "File:") {
			t.Error("expected no File: line for pathless comment")
		}
	})

	t.Run("ends with instruction", func(t *testing.T) {
		comments := []git.PRReviewComment{{Body: "test"}}
		result := FormatPRCommentsPrompt(comments)

		if !strings.Contains(result, "Please address each of these review comments") {
			t.Error("expected closing instruction")
		}
	})
}

func TestFilterTranscriptComments(t *testing.T) {
	transcriptBody := "<details>\n<summary>Session Transcript</summary>\n\n```text\nUser:\nHello\n\nAssistant:\nHi\n```\n</details>"

	t.Run("filters out transcript comments", func(t *testing.T) {
		comments := []git.PRReviewComment{
			{Author: "alice", Body: "Please fix the typo"},
			{Author: "bot", Body: transcriptBody},
			{Author: "bob", Body: "Add error handling"},
		}
		filtered := FilterTranscriptComments(comments)
		if len(filtered) != 2 {
			t.Fatalf("expected 2 comments, got %d", len(filtered))
		}
		if filtered[0].Body != "Please fix the typo" {
			t.Errorf("expected first comment body 'Please fix the typo', got %q", filtered[0].Body)
		}
		if filtered[1].Body != "Add error handling" {
			t.Errorf("expected second comment body 'Add error handling', got %q", filtered[1].Body)
		}
	})

	t.Run("keeps all comments when no transcripts", func(t *testing.T) {
		comments := []git.PRReviewComment{
			{Author: "alice", Body: "Fix this"},
			{Author: "bob", Body: "Also fix that"},
		}
		filtered := FilterTranscriptComments(comments)
		if len(filtered) != 2 {
			t.Fatalf("expected 2 comments, got %d", len(filtered))
		}
	})

	t.Run("returns empty slice when all are transcripts", func(t *testing.T) {
		comments := []git.PRReviewComment{
			{Author: "bot", Body: transcriptBody},
		}
		filtered := FilterTranscriptComments(comments)
		if len(filtered) != 0 {
			t.Fatalf("expected 0 comments, got %d", len(filtered))
		}
	})

	t.Run("handles nil input", func(t *testing.T) {
		filtered := FilterTranscriptComments(nil)
		if len(filtered) != 0 {
			t.Fatalf("expected 0 comments, got %d", len(filtered))
		}
	})

	t.Run("handles empty input", func(t *testing.T) {
		filtered := FilterTranscriptComments([]git.PRReviewComment{})
		if len(filtered) != 0 {
			t.Fatalf("expected 0 comments, got %d", len(filtered))
		}
	})

	t.Run("does not filter comments that mention transcript casually", func(t *testing.T) {
		comments := []git.PRReviewComment{
			{Author: "alice", Body: "Can you check the session transcript for context?"},
		}
		filtered := FilterTranscriptComments(comments)
		if len(filtered) != 1 {
			t.Fatalf("expected 1 comment, got %d", len(filtered))
		}
	})
}

func TestFormatInitialMessage(t *testing.T) {
	tests := []struct {
		name     string
		ref      config.IssueRef
		body     string
		contains []string
	}{
		{
			name:     "GitHub issue without body",
			ref:      config.IssueRef{Source: "github", ID: "42", Title: "Fix the bug", URL: "https://github.com/owner/repo/issues/42"},
			contains: []string{"GitHub Issue #42", "Fix the bug", "https://github.com/owner/repo/issues/42"},
		},
		{
			name:     "GitHub issue with body",
			ref:      config.IssueRef{Source: "github", ID: "42", Title: "Fix the bug", URL: "https://github.com/owner/repo/issues/42"},
			body:     "The login page crashes when submitting empty form",
			contains: []string{"GitHub Issue #42", "Fix the bug", "https://github.com/owner/repo/issues/42", "The login page crashes when submitting empty form"},
		},
		{
			name:     "Asana task",
			ref:      config.IssueRef{Source: "asana", ID: "task-abc", Title: "Implement feature", URL: "https://app.asana.com/task/abc"},
			contains: []string{"Asana Task", "Implement feature", "https://app.asana.com/task/abc"},
		},
		{
			name:     "Linear issue",
			ref:      config.IssueRef{Source: "linear", ID: "ENG-123", Title: "Add tests", URL: "https://linear.app/team/issue/ENG-123"},
			contains: []string{"Linear Issue ENG-123", "Add tests", "https://linear.app/team/issue/ENG-123"},
		},
		{
			name:     "Linear issue with body",
			ref:      config.IssueRef{Source: "linear", ID: "ENG-123", Title: "Add tests", URL: "https://linear.app/team/issue/ENG-123"},
			body:     "We need unit tests for the auth module",
			contains: []string{"Linear Issue ENG-123", "Add tests", "https://linear.app/team/issue/ENG-123", "We need unit tests for the auth module"},
		},
		{
			name:     "unknown provider",
			ref:      config.IssueRef{Source: "jira", ID: "PROJ-1", Title: "Migrate DB", URL: "https://jira.example.com/1"},
			contains: []string{"Issue PROJ-1", "Migrate DB", "https://jira.example.com/1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FormatInitialMessage(tt.ref, tt.body)
			for _, s := range tt.contains {
				if !strings.Contains(result, s) {
					t.Errorf("expected %q in result, got: %s", s, result)
				}
			}
		})
	}
}

func TestFormatInitialMessage_BodyPlacement(t *testing.T) {
	ref := config.IssueRef{Source: "github", ID: "10", Title: "Test", URL: "https://github.com/owner/repo/issues/10"}

	t.Run("empty body produces no trailing content", func(t *testing.T) {
		result := FormatInitialMessage(ref, "")
		expected := "GitHub Issue #10: Test\n\nhttps://github.com/owner/repo/issues/10"
		if result != expected {
			t.Errorf("expected %q, got %q", expected, result)
		}
	})

	t.Run("body appears after URL", func(t *testing.T) {
		result := FormatInitialMessage(ref, "Detailed description here")
		expected := "GitHub Issue #10: Test\n\nhttps://github.com/owner/repo/issues/10\n\nDetailed description here"
		if result != expected {
			t.Errorf("expected %q, got %q", expected, result)
		}
	})
}
