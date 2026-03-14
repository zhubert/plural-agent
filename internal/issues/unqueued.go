package issues

import "strings"

// unqueuedMarkerGitHub is the HTML-comment marker embedded in unqueue comments
// on GitHub. It is invisible when rendered by GitHub's Markdown parser.
const unqueuedMarkerGitHub = "<!-- erg:unqueued -->"

// unqueuedMarkerVisible is the plain-text marker used in unqueue comments on
// Asana and Linear, where HTML comments are not hidden.
const unqueuedMarkerVisible = "[erg:unqueued]"

// FormatUnqueuedComment formats an unqueue comment with the appropriate marker
// for the given provider source. The marker allows the poller to detect that
// an issue was previously unqueued without relying on in-memory state.
func FormatUnqueuedComment(source Source, reason string) string {
	switch source {
	case SourceGitHub:
		return unqueuedMarkerGitHub + "\n" + reason
	default:
		return unqueuedMarkerVisible + " " + reason
	}
}

// HasUnqueuedMarker returns true if any of the given comments contain the
// unqueued marker, indicating this issue was previously dequeued by erg.
func HasUnqueuedMarker(comments []IssueComment) bool {
	for _, c := range comments {
		if strings.Contains(c.Body, unqueuedMarkerGitHub) ||
			strings.Contains(c.Body, unqueuedMarkerVisible) {
			return true
		}
	}
	return false
}
