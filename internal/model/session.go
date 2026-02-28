package model

import (
	"strconv"
	"time"
)

// IssueRef represents a reference to an issue/task from any supported source.
// This is the generic replacement for the deprecated IssueNumber field.
type IssueRef struct {
	Source string `json:"source"` // "github" or "asana"
	ID     string `json:"id"`     // Issue/task ID (number for GitHub, GID for Asana)
	Title  string `json:"title"`  // Issue/task title for display
	URL    string `json:"url"`    // Link to the issue/task
}

// Session represents a Claude Code conversation session with its own worktree
type Session struct {
	ID         string    `json:"id"`
	RepoPath   string    `json:"repo_path"`
	WorkTree   string    `json:"worktree"`
	Branch     string    `json:"branch"`
	BaseBranch string    `json:"base_branch,omitempty"` // Branch this session was created from (e.g., "main", parent branch)
	Name       string    `json:"name"`
	CreatedAt  time.Time `json:"created_at"`
	Started    bool      `json:"started,omitempty"` // Whether session has been started with Claude CLI

	Merged           bool      `json:"merged,omitempty"`             // Whether session has been merged to main
	PRCreated        bool      `json:"pr_created,omitempty"`         // Whether a PR has been created for this session
	PRMerged         bool      `json:"pr_merged,omitempty"`          // Whether the PR was merged on GitHub
	PRClosed         bool      `json:"pr_closed,omitempty"`          // Whether the PR was closed without merging on GitHub
	ParentID         string    `json:"parent_id,omitempty"`          // ID of parent session if this is a fork
	MergedToParent   bool      `json:"merged_to_parent,omitempty"`   // Whether session has been merged back to its parent (locks the session)
	IssueNumber      int       `json:"issue_number,omitempty"`       // Deprecated: use IssueRef instead. Kept for backwards compatibility.
	IssueRef         *IssueRef `json:"issue_ref,omitempty"`          // Generic issue/task reference (GitHub, Asana, etc.)
	BroadcastGroupID string    `json:"broadcast_group_id,omitempty"` // Links sessions created from the same broadcast
	Containerized    bool      `json:"containerized,omitempty"`      // Whether this session runs inside a container
	PRCommentCount            int       `json:"pr_comment_count,omitempty"`             // Last-seen PR comment count (comments + reviews)
	PRCommentsAddressedCount  int       `json:"pr_comments_addressed_count,omitempty"`  // Comment count last addressed by Claude for merge
	Autonomous       bool      `json:"autonomous,omitempty"`         // Whether this session runs in autonomous mode (no user prompts)
	DaemonManaged    bool      `json:"daemon_managed,omitempty"`     // Whether this session is managed by the daemon (suppresses host tools)
}

// GetIssueRef returns the IssueRef for this session, converting from legacy IssueNumber if needed.
// Returns nil if no issue is associated with this session.
// Migration: older sessions only have IssueNumber (GitHub-specific int). New sessions use IssueRef
// which supports any provider. Once all persisted sessions have been re-saved with IssueRef,
// the IssueNumber field and this fallback can be removed.
func (s *Session) GetIssueRef() *IssueRef {
	// Prefer new IssueRef if set
	if s.IssueRef != nil {
		return s.IssueRef
	}
	// Fall back to legacy IssueNumber for backwards compatibility
	if s.IssueNumber > 0 {
		return &IssueRef{
			Source: "github",
			ID:     strconv.Itoa(s.IssueNumber),
			Title:  "", // Title not stored in legacy format
			URL:    "", // URL not stored in legacy format
		}
	}
	return nil
}

// HasIssue returns true if this session was created from an issue/task.
func (s *Session) HasIssue() bool {
	return s.GetIssueRef() != nil
}

// GetWorkDir returns the effective working directory for this session.
// It prefers the worktree path if set, falling back to the repo path.
func (s *Session) GetWorkDir() string {
	if s.WorkTree != "" {
		return s.WorkTree
	}
	return s.RepoPath
}
