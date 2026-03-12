package issues

import (
	"testing"
	"time"
)

func TestFormatAndParseClaimBodyGitHub(t *testing.T) {
	claim := ClaimInfo{
		DaemonID:  "daemon-abc123",
		Hostname:  "worker-1",
		Timestamp: time.Date(2026, 3, 12, 10, 0, 0, 0, time.UTC),
		Expires:   time.Date(2026, 3, 12, 11, 0, 0, 0, time.UTC),
	}

	body := formatClaimBodyGitHub(claim)

	// Should contain the HTML comment marker
	if got := body; got == "" {
		t.Fatal("expected non-empty body")
	}

	// Parse it back
	parsed := parseClaimFromBody(body, "comment-42")
	if parsed == nil {
		t.Fatal("expected to parse claim from GitHub body")
	}
	if parsed.DaemonID != "daemon-abc123" {
		t.Errorf("DaemonID = %q, want %q", parsed.DaemonID, "daemon-abc123")
	}
	if parsed.Hostname != "worker-1" {
		t.Errorf("Hostname = %q, want %q", parsed.Hostname, "worker-1")
	}
	if parsed.CommentID != "comment-42" {
		t.Errorf("CommentID = %q, want %q", parsed.CommentID, "comment-42")
	}
	if !parsed.Timestamp.Equal(claim.Timestamp) {
		t.Errorf("Timestamp = %v, want %v", parsed.Timestamp, claim.Timestamp)
	}
	if !parsed.Expires.Equal(claim.Expires) {
		t.Errorf("Expires = %v, want %v", parsed.Expires, claim.Expires)
	}
}

func TestFormatAndParseClaimBodyVisible(t *testing.T) {
	claim := ClaimInfo{
		DaemonID:  "daemon-xyz",
		Hostname:  "machine-2",
		Timestamp: time.Date(2026, 1, 5, 14, 30, 0, 0, time.UTC),
		Expires:   time.Date(2026, 1, 5, 15, 30, 0, 0, time.UTC),
	}

	body := formatClaimBodyVisible(claim)

	parsed := parseClaimFromBody(body, "story-99")
	if parsed == nil {
		t.Fatal("expected to parse claim from visible body")
	}
	if parsed.DaemonID != "daemon-xyz" {
		t.Errorf("DaemonID = %q, want %q", parsed.DaemonID, "daemon-xyz")
	}
	if parsed.Hostname != "machine-2" {
		t.Errorf("Hostname = %q, want %q", parsed.Hostname, "machine-2")
	}
	if parsed.CommentID != "story-99" {
		t.Errorf("CommentID = %q, want %q", parsed.CommentID, "story-99")
	}
}

func TestParseClaimFromBody_NonClaimComment(t *testing.T) {
	bodies := []string{
		"This is a regular comment",
		"<!-- some other marker -->",
		"[erg:step=coding] not a claim",
		"",
	}

	for _, body := range bodies {
		if parsed := parseClaimFromBody(body, "123"); parsed != nil {
			t.Errorf("expected nil for body %q, got %+v", body, parsed)
		}
	}
}

func TestParseClaimFromBody_InvalidJSON(t *testing.T) {
	bodies := []string{
		"<!-- erg-claim {invalid json} -->",
		"[erg-claim] {invalid json}\nsome text",
	}

	for _, body := range bodies {
		if parsed := parseClaimFromBody(body, "123"); parsed != nil {
			t.Errorf("expected nil for body with invalid JSON %q, got %+v", body, parsed)
		}
	}
}

func TestParseClaimFromBody_InvalidTimestamps(t *testing.T) {
	// Valid JSON but invalid timestamp format
	body := `<!-- erg-claim {"daemon":"d1","host":"h1","ts":"not-a-date","expires":"2026-01-01T00:00:00Z"} -->`
	if parsed := parseClaimFromBody(body, "123"); parsed != nil {
		t.Errorf("expected nil for invalid ts, got %+v", parsed)
	}

	body2 := `<!-- erg-claim {"daemon":"d1","host":"h1","ts":"2026-01-01T00:00:00Z","expires":"not-a-date"} -->`
	if parsed := parseClaimFromBody(body2, "123"); parsed != nil {
		t.Errorf("expected nil for invalid expires, got %+v", parsed)
	}
}

func TestParseClaimFromBody_VisibleNoTrailingNewline(t *testing.T) {
	// Edge case: visible format with JSON at end of body (no trailing newline)
	body := `[erg-claim] {"daemon":"d1","host":"h1","ts":"2026-03-12T10:00:00Z","expires":"2026-03-12T11:00:00Z"}`
	parsed := parseClaimFromBody(body, "abc")
	if parsed == nil {
		t.Fatal("expected to parse claim without trailing newline")
	}
	if parsed.DaemonID != "d1" {
		t.Errorf("DaemonID = %q, want %q", parsed.DaemonID, "d1")
	}
}
