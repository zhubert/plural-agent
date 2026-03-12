package issues

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// claimMarkerGitHub is the HTML-comment prefix used in GitHub claim comments.
// It is invisible when rendered by GitHub's Markdown parser.
const claimMarkerGitHub = "<!-- erg-claim "

// claimMarkerVisible is the plain-text prefix used in Asana/Linear claim comments.
const claimMarkerVisible = "[erg-claim] "

// claimJSON is the JSON payload embedded in claim comments.
type claimJSON struct {
	DaemonID string `json:"daemon"`
	Hostname string `json:"host"`
	TS       string `json:"ts"`
	Expires  string `json:"expires"`
}

// formatClaimBodyGitHub formats a claim comment for GitHub using an HTML comment
// to hide the JSON payload, followed by a human-readable line.
func formatClaimBodyGitHub(claim ClaimInfo) string {
	payload := claimJSON{
		DaemonID: claim.DaemonID,
		Hostname: claim.Hostname,
		TS:       claim.Timestamp.UTC().Format(time.RFC3339),
		Expires:  claim.Expires.UTC().Format(time.RFC3339),
	}
	jsonBytes, _ := json.Marshal(payload)
	return fmt.Sprintf("%s%s -->\nClaimed by erg daemon `%s` on `%s` at %s.",
		claimMarkerGitHub, string(jsonBytes),
		claim.DaemonID, claim.Hostname,
		claim.Timestamp.UTC().Format(time.RFC3339))
}

// formatClaimBodyVisible formats a claim comment for Asana/Linear where HTML
// comments are not hidden. Uses a visible [erg-claim] prefix.
func formatClaimBodyVisible(claim ClaimInfo) string {
	payload := claimJSON{
		DaemonID: claim.DaemonID,
		Hostname: claim.Hostname,
		TS:       claim.Timestamp.UTC().Format(time.RFC3339),
		Expires:  claim.Expires.UTC().Format(time.RFC3339),
	}
	jsonBytes, _ := json.Marshal(payload)
	return fmt.Sprintf("%s%s\nClaimed by erg daemon `%s` on `%s` at %s.",
		claimMarkerVisible, string(jsonBytes),
		claim.DaemonID, claim.Hostname,
		claim.Timestamp.UTC().Format(time.RFC3339))
}

// parseClaimFromBody attempts to extract a ClaimInfo from a comment body.
// It handles both GitHub (HTML comment) and visible (plain-text) marker formats.
// Returns nil if the comment does not contain a valid claim.
func parseClaimFromBody(body, commentID string) *ClaimInfo {
	var jsonStr string

	if idx := strings.Index(body, claimMarkerGitHub); idx >= 0 {
		// GitHub format: <!-- erg-claim {...} -->
		start := idx + len(claimMarkerGitHub)
		end := strings.Index(body[start:], " -->")
		if end < 0 {
			return nil
		}
		jsonStr = body[start : start+end]
	} else if idx := strings.Index(body, claimMarkerVisible); idx >= 0 {
		// Visible format: [erg-claim] {...}\n
		start := idx + len(claimMarkerVisible)
		end := strings.Index(body[start:], "\n")
		if end < 0 {
			// No newline — the JSON extends to end of body
			jsonStr = body[start:]
		} else {
			jsonStr = body[start : start+end]
		}
	} else {
		return nil
	}

	var payload claimJSON
	if err := json.Unmarshal([]byte(jsonStr), &payload); err != nil {
		return nil
	}

	ts, err := time.Parse(time.RFC3339, payload.TS)
	if err != nil {
		return nil
	}
	expires, err := time.Parse(time.RFC3339, payload.Expires)
	if err != nil {
		return nil
	}

	return &ClaimInfo{
		CommentID: commentID,
		DaemonID:  payload.DaemonID,
		Hostname:  payload.Hostname,
		Timestamp: ts,
		Expires:   expires,
	}
}

// getClaimsFromComments extracts claim info from a list of issue comments.
// This is the shared implementation used by all providers' GetClaims methods.
func getClaimsFromComments(comments []IssueComment) []ClaimInfo {
	var claims []ClaimInfo
	for _, c := range comments {
		if claim := parseClaimFromBody(c.Body, c.ID); claim != nil {
			claims = append(claims, *claim)
		}
	}
	return claims
}
