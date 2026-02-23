package worker

import (
	"fmt"
	"strings"

	"github.com/zhubert/erg/internal/config"
	"github.com/zhubert/erg/internal/git"
	"github.com/zhubert/erg/internal/issues"
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

// TranscriptMarker is the HTML marker used to identify session transcript comments
// posted by UploadTranscriptToPR in plural-core. These are collapsible <details> blocks
// containing the coding session transcript.
const TranscriptMarker = "<summary>Session Transcript</summary>"

// FilterTranscriptComments removes session transcript comments from a slice of PR review comments.
// The daemon automatically posts a coding transcript as a PR comment; this transcript
// should not be treated as review feedback when addressing reviewer comments.
func FilterTranscriptComments(comments []git.PRReviewComment) []git.PRReviewComment {
	filtered := make([]git.PRReviewComment, 0, len(comments))
	for _, c := range comments {
		if !strings.Contains(c.Body, TranscriptMarker) {
			filtered = append(filtered, c)
		}
	}
	return filtered
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
