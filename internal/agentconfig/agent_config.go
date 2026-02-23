package agentconfig

import (
	"sync"

	"github.com/zhubert/erg/internal/config"
)

// Default values for AgentConfig.
const (
	DefaultMaxTurns       = 50
	DefaultMaxDurationMin = 30
	DefaultMaxConcurrent  = 3
	DefaultMergeMethod    = "rebase"
	DefaultContainerImage = ""
	DefaultCleanupMerged  = true
)

// AgentConfig is an in-memory implementation of the Config interface.
// It replaces the file-backed config.Config for agent mode, where all
// settings come from workflow YAML and CLI flags rather than config.json.
type AgentConfig struct {
	mu       sync.RWMutex
	sessions []config.Session

	repos          []string
	branchPrefix   string
	containerImage string
	cleanupMerged  bool
	maxTurns       int
	maxDurationMin int
	maxConcurrent  int
	mergeMethod    string
}

// Compile-time interface satisfaction check.
var _ Config = (*AgentConfig)(nil)

// AgentConfigOption configures an AgentConfig.
type AgentConfigOption func(*AgentConfig)

// WithRepos sets the list of repo paths.
func WithRepos(repos []string) AgentConfigOption {
	return func(c *AgentConfig) { c.repos = repos }
}

// WithBranchPrefix sets the default branch prefix.
func WithBranchPrefix(prefix string) AgentConfigOption {
	return func(c *AgentConfig) { c.branchPrefix = prefix }
}

// WithContainerImage sets the container image.
func WithContainerImage(image string) AgentConfigOption {
	return func(c *AgentConfig) { c.containerImage = image }
}

// WithCleanupMerged sets whether to clean up merged branches.
func WithCleanupMerged(cleanup bool) AgentConfigOption {
	return func(c *AgentConfig) { c.cleanupMerged = cleanup }
}

// WithMaxConcurrent sets the max concurrent sessions.
func WithMaxConcurrent(max int) AgentConfigOption {
	return func(c *AgentConfig) { c.maxConcurrent = max }
}

// WithMaxTurns sets the max autonomous turns per session.
func WithMaxTurns(max int) AgentConfigOption {
	return func(c *AgentConfig) { c.maxTurns = max }
}

// WithMaxDuration sets the max autonomous duration in minutes per session.
func WithMaxDuration(max int) AgentConfigOption {
	return func(c *AgentConfig) { c.maxDurationMin = max }
}

// WithMergeMethod sets the merge method (rebase, squash, or merge).
func WithMergeMethod(method string) AgentConfigOption {
	return func(c *AgentConfig) { c.mergeMethod = method }
}

// NewAgentConfig creates a new AgentConfig with defaults, then applies options.
func NewAgentConfig(opts ...AgentConfigOption) *AgentConfig {
	c := &AgentConfig{
		containerImage: DefaultContainerImage,
		cleanupMerged:  DefaultCleanupMerged,
		maxTurns:       DefaultMaxTurns,
		maxDurationMin: DefaultMaxDurationMin,
		maxConcurrent:  DefaultMaxConcurrent,
		mergeMethod:    DefaultMergeMethod,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// --- Session CRUD ---

func (c *AgentConfig) GetSession(id string) *config.Session {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for i := range c.sessions {
		if c.sessions[i].ID == id {
			s := c.sessions[i]
			return &s
		}
	}
	return nil
}

func (c *AgentConfig) GetSessions() []config.Session {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]config.Session, len(c.sessions))
	copy(out, c.sessions)
	return out
}

func (c *AgentConfig) AddSession(session config.Session) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessions = append(c.sessions, session)
}

func (c *AgentConfig) RemoveSession(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.sessions {
		if c.sessions[i].ID == id {
			c.sessions = append(c.sessions[:i], c.sessions[i+1:]...)
			return true
		}
	}
	return false
}

func (c *AgentConfig) ClearOrphanedParentIDs(deletedIDs []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	idSet := make(map[string]bool, len(deletedIDs))
	for _, id := range deletedIDs {
		idSet[id] = true
	}
	for i := range c.sessions {
		if idSet[c.sessions[i].ParentID] {
			c.sessions[i].ParentID = ""
		}
	}
}

func (c *AgentConfig) MarkSessionStarted(sessionID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.sessions {
		if c.sessions[i].ID == sessionID {
			c.sessions[i].Started = true
			return true
		}
	}
	return false
}

func (c *AgentConfig) MarkSessionPRCreated(sessionID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.sessions {
		if c.sessions[i].ID == sessionID {
			c.sessions[i].PRCreated = true
			return true
		}
	}
	return false
}

func (c *AgentConfig) MarkSessionPRMerged(sessionID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.sessions {
		if c.sessions[i].ID == sessionID {
			c.sessions[i].PRMerged = true
			return true
		}
	}
	return false
}

func (c *AgentConfig) MarkSessionMergedToParent(sessionID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.sessions {
		if c.sessions[i].ID == sessionID {
			c.sessions[i].MergedToParent = true
			return true
		}
	}
	return false
}

func (c *AgentConfig) AddChildSession(supervisorID, childID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.sessions {
		if c.sessions[i].ID == supervisorID {
			c.sessions[i].ChildSessionIDs = append(c.sessions[i].ChildSessionIDs, childID)
			return true
		}
	}
	return false
}

func (c *AgentConfig) GetChildSessions(supervisorID string) []config.Session {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var childIDs []string
	for i := range c.sessions {
		if c.sessions[i].ID == supervisorID {
			childIDs = c.sessions[i].ChildSessionIDs
			break
		}
	}
	if len(childIDs) == 0 {
		return nil
	}
	idSet := make(map[string]bool, len(childIDs))
	for _, id := range childIDs {
		idSet[id] = true
	}
	var children []config.Session
	for _, s := range c.sessions {
		if idSet[s.ID] {
			children = append(children, s)
		}
	}
	return children
}

func (c *AgentConfig) UpdateSessionPRCommentsAddressedCount(sessionID string, count int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.sessions {
		if c.sessions[i].ID == sessionID {
			c.sessions[i].PRCommentsAddressedCount = count
			return true
		}
	}
	return false
}

// --- Repo settings ---

func (c *AgentConfig) GetRepos() []string {
	out := make([]string, len(c.repos))
	copy(out, c.repos)
	return out
}

func (c *AgentConfig) GetDefaultBranchPrefix() string {
	return c.branchPrefix
}

func (c *AgentConfig) GetContainerImage() string {
	return c.containerImage
}

func (c *AgentConfig) GetAllowedToolsForRepo(_ string) []string {
	return nil // Container mode uses --dangerously-skip-permissions
}

func (c *AgentConfig) GetMCPServersForRepo(_ string) []config.MCPServer {
	return nil // Container mode handles MCP internally
}

func (c *AgentConfig) AddRepoAllowedTool(_, _ string) bool {
	return false // No-op in agent mode
}

// --- Automation settings ---

func (c *AgentConfig) GetAutoMaxTurns() int       { return c.maxTurns }
func (c *AgentConfig) GetAutoMaxDurationMin() int  { return c.maxDurationMin }
func (c *AgentConfig) GetAutoCleanupMerged() bool  { return c.cleanupMerged }
func (c *AgentConfig) GetAutoAddressPRComments() bool { return false }
func (c *AgentConfig) GetAutoBroadcastPR() bool    { return false }
func (c *AgentConfig) GetAutoMergeMethod() string  { return c.mergeMethod }
func (c *AgentConfig) GetIssueMaxConcurrent() int  { return c.maxConcurrent }

// --- Persistence ---

// Save is a no-op for the in-memory config.
func (c *AgentConfig) Save() error { return nil }

// --- Issue provider compatibility ---

// HasAsanaProject returns false; Asana projects are not configured in agent mode.
func (c *AgentConfig) HasAsanaProject(_ string) bool { return false }

// HasLinearTeam returns false; Linear teams are not configured in agent mode.
func (c *AgentConfig) HasLinearTeam(_ string) bool { return false }
