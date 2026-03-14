package issues

import "testing"

func TestFormatUnqueuedComment_GitHub(t *testing.T) {
	body := FormatUnqueuedComment(SourceGitHub, "PR already merged.")
	if body != "<!-- erg:unqueued -->\nPR already merged." {
		t.Errorf("unexpected GitHub comment body: %s", body)
	}
}

func TestFormatUnqueuedComment_Asana(t *testing.T) {
	body := FormatUnqueuedComment(SourceAsana, "No changes made.")
	if body != "[erg:unqueued] No changes made." {
		t.Errorf("unexpected Asana comment body: %s", body)
	}
}

func TestFormatUnqueuedComment_Linear(t *testing.T) {
	body := FormatUnqueuedComment(SourceLinear, "Closed externally.")
	if body != "[erg:unqueued] Closed externally." {
		t.Errorf("unexpected Linear comment body: %s", body)
	}
}

func TestFormatUnqueuedCommentWithSuffix_GitHub(t *testing.T) {
	tests := []struct {
		suffix   string
		reason   string
		expected string
	}{
		{"success", "Work completed.", "<!-- erg:unqueued:success -->\nWork completed."},
		{"failed", "CI unfixable.", "<!-- erg:unqueued:failed -->\nCI unfixable."},
		{"no_changes", "No changes needed.", "<!-- erg:unqueued:no_changes -->\nNo changes needed."},
		{"", "Legacy format.", "<!-- erg:unqueued -->\nLegacy format."},
	}
	for _, tt := range tests {
		t.Run(tt.suffix, func(t *testing.T) {
			body := FormatUnqueuedCommentWithSuffix(SourceGitHub, tt.reason, tt.suffix)
			if body != tt.expected {
				t.Errorf("got %q, want %q", body, tt.expected)
			}
		})
	}
}

func TestFormatUnqueuedCommentWithSuffix_Asana(t *testing.T) {
	tests := []struct {
		suffix   string
		reason   string
		expected string
	}{
		{"success", "Done.", "[erg:unqueued:success] Done."},
		{"failed", "Failed.", "[erg:unqueued:failed] Failed."},
		{"", "Legacy.", "[erg:unqueued] Legacy."},
	}
	for _, tt := range tests {
		t.Run(tt.suffix, func(t *testing.T) {
			body := FormatUnqueuedCommentWithSuffix(SourceAsana, tt.reason, tt.suffix)
			if body != tt.expected {
				t.Errorf("got %q, want %q", body, tt.expected)
			}
		})
	}
}

func TestHasUnqueuedMarker(t *testing.T) {
	tests := []struct {
		name     string
		comments []IssueComment
		want     bool
	}{
		{
			name:     "no comments",
			comments: nil,
			want:     false,
		},
		{
			name: "no marker",
			comments: []IssueComment{
				{Body: "Just a regular comment"},
			},
			want: false,
		},
		{
			name: "legacy github marker",
			comments: []IssueComment{
				{Body: "<!-- erg:unqueued -->\nPR already merged."},
			},
			want: true,
		},
		{
			name: "legacy visible marker",
			comments: []IssueComment{
				{Body: "[erg:unqueued] No changes made."},
			},
			want: true,
		},
		{
			name: "suffixed github marker success",
			comments: []IssueComment{
				{Body: "<!-- erg:unqueued:success -->\nWork completed."},
			},
			want: true,
		},
		{
			name: "suffixed github marker failed",
			comments: []IssueComment{
				{Body: "<!-- erg:unqueued:failed -->\nCI unfixable."},
			},
			want: true,
		},
		{
			name: "suffixed visible marker success",
			comments: []IssueComment{
				{Body: "[erg:unqueued:success] Done."},
			},
			want: true,
		},
		{
			name: "suffixed visible marker failed",
			comments: []IssueComment{
				{Body: "[erg:unqueued:failed] Failed."},
			},
			want: true,
		},
		{
			name: "marker among other comments",
			comments: []IssueComment{
				{Body: "Some earlier comment"},
				{Body: "<!-- erg:unqueued:success -->\nDone."},
				{Body: "A later comment"},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HasUnqueuedMarker(tt.comments)
			if got != tt.want {
				t.Errorf("HasUnqueuedMarker() = %v, want %v", got, tt.want)
			}
		})
	}
}
