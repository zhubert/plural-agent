package worker

import (
	"fmt"
	"strings"

	"github.com/zhubert/plural-core/config"
	"github.com/zhubert/plural-core/git"
	"github.com/zhubert/plural-core/issues"
)

// TrimURL extracts a URL from output text.
func TrimURL(output string) string {
	trimmed := strings.TrimSpace(output)
	if strings.HasPrefix(trimmed, "https://") {
		return trimmed
	}
	return ""
}

// FormatPRCommentsPrompt formats PR review comments as a prompt string.
func FormatPRCommentsPrompt(comments []git.PRReviewComment) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("New PR review comments need to be addressed (%d comment(s)):\n\n", len(comments)))

	for i, c := range comments {
		sb.WriteString(fmt.Sprintf("--- Comment %d", i+1))
		if c.Author != "" {
			sb.WriteString(fmt.Sprintf(" by @%s", c.Author))
		}
		sb.WriteString(" ---\n")
		if c.Path != "" {
			if c.Line > 0 {
				sb.WriteString(fmt.Sprintf("File: %s:%d\n", c.Path, c.Line))
			} else {
				sb.WriteString(fmt.Sprintf("File: %s\n", c.Path))
			}
		}
		sb.WriteString(c.Body)
		sb.WriteString("\n\n")
	}

	sb.WriteString("Please address each of these review comments. For code changes, make the necessary edits. For questions, provide a response and make any relevant code changes.")
	return sb.String()
}

// FormatInitialMessage formats the initial message for a coding session based on the issue provider.
// The optional body parameter contains the issue description/body text.
func FormatInitialMessage(ref config.IssueRef, body string) string {
	provider := issues.Source(ref.Source)

	var header string
	switch provider {
	case issues.SourceGitHub:
		header = fmt.Sprintf("GitHub Issue #%s: %s\n\n%s", ref.ID, ref.Title, ref.URL)
	case issues.SourceAsana:
		header = fmt.Sprintf("Asana Task: %s\n\n%s", ref.Title, ref.URL)
	case issues.SourceLinear:
		header = fmt.Sprintf("Linear Issue %s: %s\n\n%s", ref.ID, ref.Title, ref.URL)
	default:
		header = fmt.Sprintf("Issue %s: %s\n\n%s", ref.ID, ref.Title, ref.URL)
	}

	if body != "" {
		return header + "\n\n" + body
	}
	return header
}
