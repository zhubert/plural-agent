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
			name: "github marker",
			comments: []IssueComment{
				{Body: "<!-- erg:unqueued -->\nPR already merged."},
			},
			want: true,
		},
		{
			name: "visible marker",
			comments: []IssueComment{
				{Body: "[erg:unqueued] No changes made."},
			},
			want: true,
		},
		{
			name: "marker among other comments",
			comments: []IssueComment{
				{Body: "Some earlier comment"},
				{Body: "<!-- erg:unqueued -->\nDone."},
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
