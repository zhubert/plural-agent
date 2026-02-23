package agentconfig

import "github.com/zhubert/erg/internal/config"

// Config defines the configuration interface required by the agent and worker packages.
// This decouples them from the concrete config.Config struct, allowing them
// to depend only on the methods they actually use.
//
// *config.Config satisfies this interface implicitly.
type Config interface {
	// Session CRUD
	GetSession(id string) *config.Session
	GetSessions() []config.Session
	AddSession(session config.Session)
	RemoveSession(id string) bool
	ClearOrphanedParentIDs(deletedIDs []string)
	MarkSessionStarted(sessionID string) bool
	MarkSessionPRCreated(sessionID string) bool
	MarkSessionPRMerged(sessionID string) bool
	MarkSessionMergedToParent(sessionID string) bool
	AddChildSession(supervisorID, childID string) bool
	GetChildSessions(supervisorID string) []config.Session
	UpdateSessionPRCommentsAddressedCount(sessionID string, count int) bool

	// Repo settings
	GetRepos() []string
	GetDefaultBranchPrefix() string
	GetContainerImage() string
	GetAllowedToolsForRepo(repoPath string) []string
	GetMCPServersForRepo(repoPath string) []config.MCPServer
	AddRepoAllowedTool(repoPath, tool string) bool

	// Automation settings
	GetAutoMaxTurns() int
	GetAutoMaxDurationMin() int
	GetAutoCleanupMerged() bool
	GetAutoAddressPRComments() bool
	GetAutoBroadcastPR() bool
	GetAutoMergeMethod() string
	GetIssueMaxConcurrent() int

	// Persistence
	Save() error
}
