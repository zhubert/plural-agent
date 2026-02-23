package agentconfig

import (
	"testing"
	"time"

	"github.com/zhubert/erg/internal/config"
	"github.com/zhubert/erg/internal/issues"
)

// Compile-time interface checks.
var (
	_ Config                    = (*AgentConfig)(nil)
	_ issues.AsanaConfigProvider  = (*AgentConfig)(nil)
	_ issues.LinearConfigProvider = (*AgentConfig)(nil)
)

func TestNewAgentConfig_Defaults(t *testing.T) {
	c := NewAgentConfig()

	if c.GetAutoMaxTurns() != DefaultMaxTurns {
		t.Errorf("maxTurns: got %d, want %d", c.GetAutoMaxTurns(), DefaultMaxTurns)
	}
	if c.GetAutoMaxDurationMin() != DefaultMaxDurationMin {
		t.Errorf("maxDurationMin: got %d, want %d", c.GetAutoMaxDurationMin(), DefaultMaxDurationMin)
	}
	if c.GetIssueMaxConcurrent() != DefaultMaxConcurrent {
		t.Errorf("maxConcurrent: got %d, want %d", c.GetIssueMaxConcurrent(), DefaultMaxConcurrent)
	}
	if c.GetAutoMergeMethod() != DefaultMergeMethod {
		t.Errorf("mergeMethod: got %q, want %q", c.GetAutoMergeMethod(), DefaultMergeMethod)
	}
	if c.GetContainerImage() != DefaultContainerImage {
		t.Errorf("containerImage: got %q, want %q", c.GetContainerImage(), DefaultContainerImage)
	}
	if c.GetDefaultBranchPrefix() != "" {
		t.Errorf("branchPrefix: got %q, want empty", c.GetDefaultBranchPrefix())
	}
	if c.GetAutoCleanupMerged() != DefaultCleanupMerged {
		t.Errorf("cleanupMerged: got %v, want %v", c.GetAutoCleanupMerged(), DefaultCleanupMerged)
	}
	if len(c.GetRepos()) != 0 {
		t.Errorf("repos: got %v, want empty", c.GetRepos())
	}
}

func TestNewAgentConfig_Options(t *testing.T) {
	c := NewAgentConfig(
		WithRepos([]string{"/path/to/repo"}),
		WithBranchPrefix("agent/"),
		WithContainerImage("custom-image:latest"),
		WithCleanupMerged(true),
		WithMaxConcurrent(5),
	)

	if repos := c.GetRepos(); len(repos) != 1 || repos[0] != "/path/to/repo" {
		t.Errorf("repos: got %v", repos)
	}
	if c.GetDefaultBranchPrefix() != "agent/" {
		t.Errorf("branchPrefix: got %q", c.GetDefaultBranchPrefix())
	}
	if c.GetContainerImage() != "custom-image:latest" {
		t.Errorf("containerImage: got %q", c.GetContainerImage())
	}
	if !c.GetAutoCleanupMerged() {
		t.Error("cleanupMerged: got false")
	}
	if c.GetIssueMaxConcurrent() != 5 {
		t.Errorf("maxConcurrent: got %d", c.GetIssueMaxConcurrent())
	}
}

func TestNewAgentConfig_WorkflowSettingsOptions(t *testing.T) {
	c := NewAgentConfig(
		WithMaxTurns(80),
		WithMaxDuration(45),
		WithMergeMethod("squash"),
	)

	if c.GetAutoMaxTurns() != 80 {
		t.Errorf("maxTurns: got %d, want 80", c.GetAutoMaxTurns())
	}
	if c.GetAutoMaxDurationMin() != 45 {
		t.Errorf("maxDurationMin: got %d, want 45", c.GetAutoMaxDurationMin())
	}
	if c.GetAutoMergeMethod() != "squash" {
		t.Errorf("mergeMethod: got %q, want squash", c.GetAutoMergeMethod())
	}
}

func TestNewAgentConfig_WorkflowSettingsOptions_DoNotOverrideDefaults(t *testing.T) {
	// When workflow settings are not specified, defaults should still apply.
	c := NewAgentConfig()

	if c.GetAutoMaxTurns() != DefaultMaxTurns {
		t.Errorf("maxTurns: got %d, want default %d", c.GetAutoMaxTurns(), DefaultMaxTurns)
	}
	if c.GetAutoMaxDurationMin() != DefaultMaxDurationMin {
		t.Errorf("maxDurationMin: got %d, want default %d", c.GetAutoMaxDurationMin(), DefaultMaxDurationMin)
	}
	if c.GetAutoMergeMethod() != DefaultMergeMethod {
		t.Errorf("mergeMethod: got %q, want default %q", c.GetAutoMergeMethod(), DefaultMergeMethod)
	}
}

func TestAgentConfig_SessionCRUD(t *testing.T) {
	c := NewAgentConfig()

	// Initially empty
	if sessions := c.GetSessions(); len(sessions) != 0 {
		t.Fatalf("expected no sessions, got %d", len(sessions))
	}
	if s := c.GetSession("nonexistent"); s != nil {
		t.Fatal("expected nil for nonexistent session")
	}

	// Add a session
	s1 := config.Session{
		ID:       "s1",
		RepoPath: "/repo",
		Branch:   "feature/test",
		Name:     "test session",
		CreatedAt: time.Now(),
	}
	c.AddSession(s1)

	if got := c.GetSession("s1"); got == nil {
		t.Fatal("expected to find session s1")
	} else if got.Branch != "feature/test" {
		t.Errorf("branch: got %q", got.Branch)
	}

	if sessions := c.GetSessions(); len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}

	// Add a second session
	s2 := config.Session{ID: "s2", RepoPath: "/repo", Branch: "feature/other"}
	c.AddSession(s2)

	if sessions := c.GetSessions(); len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}

	// Remove first session
	if !c.RemoveSession("s1") {
		t.Error("expected RemoveSession to return true")
	}
	if c.GetSession("s1") != nil {
		t.Error("expected s1 to be removed")
	}

	// Remove nonexistent session
	if c.RemoveSession("s1") {
		t.Error("expected RemoveSession to return false for already removed session")
	}

}

func TestAgentConfig_MarkSessionMethods(t *testing.T) {
	c := NewAgentConfig()
	c.AddSession(config.Session{ID: "s1"})

	tests := []struct {
		name   string
		markFn func(string) bool
		checkFn func(*config.Session) bool
	}{
		{"Started", c.MarkSessionStarted, func(s *config.Session) bool { return s.Started }},
		{"PRCreated", c.MarkSessionPRCreated, func(s *config.Session) bool { return s.PRCreated }},
		{"PRMerged", c.MarkSessionPRMerged, func(s *config.Session) bool { return s.PRMerged }},
		{"MergedToParent", c.MarkSessionMergedToParent, func(s *config.Session) bool { return s.MergedToParent }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Mark nonexistent
			if tt.markFn("nonexistent") {
				t.Error("expected false for nonexistent session")
			}
			// Mark existing
			if !tt.markFn("s1") {
				t.Error("expected true for existing session")
			}
			s := c.GetSession("s1")
			if !tt.checkFn(s) {
				t.Error("expected field to be true after marking")
			}
		})
	}
}

func TestAgentConfig_ChildSessions(t *testing.T) {
	c := NewAgentConfig()
	c.AddSession(config.Session{ID: "supervisor"})
	c.AddSession(config.Session{ID: "child1"})
	c.AddSession(config.Session{ID: "child2"})

	// Add children
	if !c.AddChildSession("supervisor", "child1") {
		t.Error("expected true")
	}
	if !c.AddChildSession("supervisor", "child2") {
		t.Error("expected true")
	}
	if c.AddChildSession("nonexistent", "child1") {
		t.Error("expected false for nonexistent supervisor")
	}

	// Get children
	children := c.GetChildSessions("supervisor")
	if len(children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(children))
	}

	// No children for nonexistent
	if children := c.GetChildSessions("nonexistent"); len(children) != 0 {
		t.Errorf("expected no children, got %d", len(children))
	}
}

func TestAgentConfig_ClearOrphanedParentIDs(t *testing.T) {
	c := NewAgentConfig()
	c.AddSession(config.Session{ID: "s1", ParentID: "deleted-parent"})
	c.AddSession(config.Session{ID: "s2", ParentID: "existing-parent"})
	c.AddSession(config.Session{ID: "s3", ParentID: "deleted-parent"})

	c.ClearOrphanedParentIDs([]string{"deleted-parent"})

	if s := c.GetSession("s1"); s.ParentID != "" {
		t.Errorf("s1 parent should be cleared, got %q", s.ParentID)
	}
	if s := c.GetSession("s2"); s.ParentID != "existing-parent" {
		t.Errorf("s2 parent should be unchanged, got %q", s.ParentID)
	}
	if s := c.GetSession("s3"); s.ParentID != "" {
		t.Errorf("s3 parent should be cleared, got %q", s.ParentID)
	}
}

func TestAgentConfig_PRCommentsAddressedCount(t *testing.T) {
	c := NewAgentConfig()
	c.AddSession(config.Session{ID: "s1"})

	if c.UpdateSessionPRCommentsAddressedCount("nonexistent", 3) {
		t.Error("expected false for nonexistent")
	}
	if !c.UpdateSessionPRCommentsAddressedCount("s1", 3) {
		t.Error("expected true")
	}
	if c.GetSession("s1").PRCommentsAddressedCount != 3 {
		t.Errorf("PRCommentsAddressedCount: got %d", c.GetSession("s1").PRCommentsAddressedCount)
	}
}

func TestAgentConfig_NoOpMethods(t *testing.T) {
	c := NewAgentConfig()

	if err := c.Save(); err != nil {
		t.Errorf("Save should return nil, got %v", err)
	}
	if c.GetAllowedToolsForRepo("/repo") != nil {
		t.Error("GetAllowedToolsForRepo should return nil")
	}
	if c.GetMCPServersForRepo("/repo") != nil {
		t.Error("GetMCPServersForRepo should return nil")
	}
	if c.AddRepoAllowedTool("/repo", "tool") {
		t.Error("AddRepoAllowedTool should return false")
	}
	if c.GetAutoAddressPRComments() {
		t.Error("GetAutoAddressPRComments should return false")
	}
	if c.GetAutoBroadcastPR() {
		t.Error("GetAutoBroadcastPR should return false")
	}
}

func TestAgentConfig_IssueProviderCompat(t *testing.T) {
	c := NewAgentConfig()

	if c.HasAsanaProject("/repo") {
		t.Error("HasAsanaProject should return false")
	}
	if c.HasLinearTeam("/repo") {
		t.Error("HasLinearTeam should return false")
	}
}

func TestAgentConfig_GetRepos_ReturnsCopy(t *testing.T) {
	c := NewAgentConfig(WithRepos([]string{"/repo1", "/repo2"}))
	repos := c.GetRepos()
	repos[0] = "modified"
	if c.GetRepos()[0] == "modified" {
		t.Error("GetRepos should return a copy, not the original slice")
	}
}

func TestAgentConfig_GetSessions_ReturnsCopy(t *testing.T) {
	c := NewAgentConfig()
	c.AddSession(config.Session{ID: "s1", Name: "original"})

	sessions := c.GetSessions()
	sessions[0].Name = "modified"

	if c.GetSession("s1").Name == "modified" {
		t.Error("GetSessions should return a copy")
	}
}

func TestAgentConfig_GetSession_ReturnsCopy(t *testing.T) {
	c := NewAgentConfig()
	c.AddSession(config.Session{ID: "s1", Name: "original"})

	s := c.GetSession("s1")
	s.Name = "modified"

	if c.GetSession("s1").Name == "modified" {
		t.Error("GetSession should return a copy")
	}
}

func TestNewAgentConfig_BYOC_EmptyContainerImage(t *testing.T) {
	// BYOC: default container image should be empty, requiring user configuration
	c := NewAgentConfig()
	if c.GetContainerImage() != "" {
		t.Errorf("expected empty default container image (BYOC), got %q", c.GetContainerImage())
	}
}

func TestNewAgentConfig_BYOC_WithContainerImage(t *testing.T) {
	// User can still configure a container image via options
	c := NewAgentConfig(WithContainerImage("my-custom:latest"))
	if c.GetContainerImage() != "my-custom:latest" {
		t.Errorf("expected my-custom:latest, got %q", c.GetContainerImage())
	}
}
