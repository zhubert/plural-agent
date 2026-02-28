package model

import "testing"

func TestSession_GetIssueRef_PreferIssueRef(t *testing.T) {
	ref := &IssueRef{Source: "asana", ID: "12345", Title: "Fix bug", URL: "https://app.asana.com/0/12345"}
	s := &Session{IssueRef: ref, IssueNumber: 42}

	got := s.GetIssueRef()
	if got != ref {
		t.Errorf("expected IssueRef to be preferred, got %+v", got)
	}
}

func TestSession_GetIssueRef_FallbackToLegacy(t *testing.T) {
	s := &Session{IssueNumber: 42}

	got := s.GetIssueRef()
	if got == nil {
		t.Fatal("expected non-nil IssueRef from legacy fallback")
	}
	if got.Source != "github" {
		t.Errorf("source: got %q, want github", got.Source)
	}
	if got.ID != "42" {
		t.Errorf("id: got %q, want 42", got.ID)
	}
}

func TestSession_GetIssueRef_NoIssue(t *testing.T) {
	s := &Session{}
	if got := s.GetIssueRef(); got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestSession_GetIssueRef_ZeroIssueNumber(t *testing.T) {
	s := &Session{IssueNumber: 0}
	if got := s.GetIssueRef(); got != nil {
		t.Errorf("expected nil for zero issue number, got %+v", got)
	}
}

func TestSession_GetWorkDir(t *testing.T) {
	tests := []struct {
		name string
		sess Session
		want string
	}{
		{"worktree set", Session{RepoPath: "/repo", WorkTree: "/worktree"}, "/worktree"},
		{"worktree empty", Session{RepoPath: "/repo"}, "/repo"},
		{"both empty", Session{}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.sess.GetWorkDir(); got != tt.want {
				t.Errorf("GetWorkDir() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSession_HasIssue(t *testing.T) {
	tests := []struct {
		name string
		sess Session
		want bool
	}{
		{"with IssueRef", Session{IssueRef: &IssueRef{Source: "github", ID: "1"}}, true},
		{"with legacy IssueNumber", Session{IssueNumber: 5}, true},
		{"no issue", Session{}, false},
		{"zero issue number", Session{IssueNumber: 0}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.sess.HasIssue(); got != tt.want {
				t.Errorf("HasIssue() = %v, want %v", got, tt.want)
			}
		})
	}
}
