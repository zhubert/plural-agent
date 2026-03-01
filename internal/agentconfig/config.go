package agentconfig

import "github.com/zhubert/erg/internal/model"

// Config defines the configuration interface required by the agent and worker packages.
// This decouples them from the concrete config.Config struct, allowing them
// to depend only on the methods they actually use.
//
// *config.Config satisfies this interface implicitly.
type Config interface {
	// Session CRUD
	GetSession(id string) *model.Session
	GetSessions() []model.Session
	AddSession(session model.Session)
	RemoveSession(id string) bool
	ClearOrphanedParentIDs(deletedIDs []string)
	MarkSessionStarted(sessionID string) bool
	MarkSessionPRMerged(sessionID string) bool
	MarkSessionMergedToParent(sessionID string) bool
	UpdateSessionPRCommentsAddressedCount(sessionID string, count int) bool

	// Repo settings
	GetRepos() []string
	GetDefaultBranchPrefix() string
	GetContainerImage() string
	GetAllowedToolsForRepo(repoPath string) []string
	GetMCPServersForRepo(repoPath string) []model.MCPServer
	AddRepoAllowedTool(repoPath, tool string) bool

	// Automation settings
	GetAutoMaxTurns() int
	GetAutoMaxDurationMin() int
	GetAutoCleanupMerged() bool
	GetAutoAddressPRComments() bool
	GetAutoBroadcastPR() bool
	GetAutoMergeMethod() string
	GetIssueMaxConcurrent() int

	// Issue providers
	SetAsanaProject(repoPath, projectGID string)
	SetLinearTeam(repoPath, teamID string)

	// Persistence
	Save() error
}
