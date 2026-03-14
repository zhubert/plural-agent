package issues

import (
	"fmt"
	"strings"
)

// unqueuedMarkerPrefixGitHub is the prefix of the HTML-comment marker embedded
// in unqueue comments on GitHub. Detection uses prefix matching so that both
// the legacy marker (<!-- erg:unqueued -->) and suffixed markers
// (<!-- erg:unqueued:success -->) are recognised.
const unqueuedMarkerPrefixGitHub = "<!-- erg:unqueued"

// unqueuedMarkerPrefixVisible is the prefix of the plain-text marker used in
// unqueue comments on Asana and Linear. Same prefix-matching approach.
const unqueuedMarkerPrefixVisible = "[erg:unqueued"

// FormatUnqueuedComment formats an unqueue comment with the appropriate marker
// for the given provider source. The marker allows the poller to detect that
// an issue was previously unqueued without relying on in-memory state.
// Uses the legacy marker format (no suffix).
func FormatUnqueuedComment(source Source, reason string) string {
	return FormatUnqueuedCommentWithSuffix(source, reason, "")
}

// FormatUnqueuedCommentWithSuffix formats an unqueue comment with a
// machine-readable suffix embedded in the marker. The suffix indicates why the
// issue was unqueued (e.g. "success", "failed", "no_changes",
// "closed_externally"). An empty suffix produces the legacy marker format.
func FormatUnqueuedCommentWithSuffix(source Source, reason, suffix string) string {
	switch source {
	case SourceGitHub:
		marker := formatGitHubMarker(suffix)
		return marker + "\n" + reason
	default:
		marker := formatVisibleMarker(suffix)
		return marker + " " + reason
	}
}

// formatGitHubMarker returns the HTML-comment marker with an optional suffix.
func formatGitHubMarker(suffix string) string {
	if suffix == "" {
		return "<!-- erg:unqueued -->"
	}
	return fmt.Sprintf("<!-- erg:unqueued:%s -->", suffix)
}

// formatVisibleMarker returns the plain-text marker with an optional suffix.
func formatVisibleMarker(suffix string) string {
	if suffix == "" {
		return "[erg:unqueued]"
	}
	return fmt.Sprintf("[erg:unqueued:%s]", suffix)
}

// HasUnqueuedMarker returns true if any of the given comments contain an
// unqueued marker, indicating this issue was previously dequeued by erg.
// Matches both legacy markers and suffixed markers via prefix matching.
func HasUnqueuedMarker(comments []IssueComment) bool {
	for _, c := range comments {
		if strings.Contains(c.Body, unqueuedMarkerPrefixGitHub) ||
			strings.Contains(c.Body, unqueuedMarkerPrefixVisible) {
			return true
		}
	}
	return false
}
