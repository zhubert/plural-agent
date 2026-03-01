package config

import "github.com/zhubert/erg/internal/model"

// Session is an alias for model.Session, keeping all existing config.Session references working.
type Session = model.Session

// IssueRef is an alias for model.IssueRef, keeping all existing config.IssueRef references working.
type IssueRef = model.IssueRef

// AddSession adds a new session
func (c *Config) AddSession(session Session) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.Sessions = append(c.Sessions, session)
}

// RemoveSession removes a session by ID
func (c *Config) RemoveSession(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	for i, s := range c.Sessions {
		if s.ID == id {
			c.Sessions = append(c.Sessions[:i], c.Sessions[i+1:]...)
			return true
		}
	}
	return false
}

// ClearOrphanedParentIDs clears ParentID references that point to any of the deleted session IDs.
// This prevents child sessions from referencing non-existent parents.
func (c *Config) ClearOrphanedParentIDs(deletedIDs []string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	idSet := make(map[string]bool, len(deletedIDs))
	for _, id := range deletedIDs {
		idSet[id] = true
	}

	for i := range c.Sessions {
		if c.Sessions[i].ParentID != "" && idSet[c.Sessions[i].ParentID] {
			c.Sessions[i].ParentID = ""
			c.Sessions[i].MergedToParent = false
		}
	}
}

// ClearSessions removes all sessions
func (c *Config) ClearSessions() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Sessions = []Session{}
}

// GetSession returns a copy of a session by ID.
// Returns nil if no session with the given ID exists.
func (c *Config) GetSession(id string) *Session {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for i := range c.Sessions {
		if c.Sessions[i].ID == id {
			sess := c.Sessions[i] // copy
			return &sess
		}
	}
	return nil
}

// GetSessions returns a copy of the sessions slice
func (c *Config) GetSessions() []Session {
	c.mu.RLock()
	defer c.mu.RUnlock()

	sessions := make([]Session, len(c.Sessions))
	copy(sessions, c.Sessions)
	return sessions
}

// MarkSessionStarted marks a session as started with Claude CLI
func (c *Config) MarkSessionStarted(sessionID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	for i := range c.Sessions {
		if c.Sessions[i].ID == sessionID {
			c.Sessions[i].Started = true
			return true
		}
	}
	return false
}

// MarkSessionMerged marks a session as merged to main
func (c *Config) MarkSessionMerged(sessionID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	for i := range c.Sessions {
		if c.Sessions[i].ID == sessionID {
			c.Sessions[i].Merged = true
			return true
		}
	}
	return false
}

// MarkSessionPRMerged marks a session's PR as merged on GitHub
func (c *Config) MarkSessionPRMerged(sessionID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	for i := range c.Sessions {
		if c.Sessions[i].ID == sessionID {
			c.Sessions[i].PRMerged = true
			return true
		}
	}
	return false
}

// MarkSessionPRClosed marks a session's PR as closed without merging on GitHub
func (c *Config) MarkSessionPRClosed(sessionID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	for i := range c.Sessions {
		if c.Sessions[i].ID == sessionID {
			c.Sessions[i].PRClosed = true
			return true
		}
	}
	return false
}

// MarkSessionMergedToParent marks a session as merged to its parent (locks the session)
func (c *Config) MarkSessionMergedToParent(sessionID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	for i := range c.Sessions {
		if c.Sessions[i].ID == sessionID {
			c.Sessions[i].MergedToParent = true
			return true
		}
	}
	return false
}

// RenameSession updates the name and branch of a session
func (c *Config) RenameSession(sessionID, newName, newBranch string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	for i := range c.Sessions {
		if c.Sessions[i].ID == sessionID {
			c.Sessions[i].Name = newName
			c.Sessions[i].Branch = newBranch
			return true
		}
	}
	return false
}

// GetSessionsByBroadcastGroup returns all sessions that belong to the given broadcast group
func (c *Config) GetSessionsByBroadcastGroup(groupID string) []Session {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if groupID == "" {
		return nil
	}

	var sessions []Session
	for _, s := range c.Sessions {
		if s.BroadcastGroupID == groupID {
			sessions = append(sessions, s)
		}
	}
	return sessions
}

// SetSessionBroadcastGroup sets the broadcast group ID for a session
func (c *Config) SetSessionBroadcastGroup(sessionID, groupID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	for i := range c.Sessions {
		if c.Sessions[i].ID == sessionID {
			c.Sessions[i].BroadcastGroupID = groupID
			return true
		}
	}
	return false
}

// SetSessionAutonomous sets the autonomous mode for a session.
func (c *Config) SetSessionAutonomous(sessionID string, autonomous bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	for i := range c.Sessions {
		if c.Sessions[i].ID == sessionID {
			c.Sessions[i].Autonomous = autonomous
			return true
		}
	}
	return false
}

// UpdateSessionWorkTree updates the worktree path for a session.
// Used during migration from pre-rename legacy .plural-worktrees to centralized directory.
func (c *Config) UpdateSessionWorkTree(sessionID string, workTree string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	for i := range c.Sessions {
		if c.Sessions[i].ID == sessionID {
			c.Sessions[i].WorkTree = workTree
			return true
		}
	}
	return false
}

// UpdateSessionPRCommentCount updates the last-seen PR comment count for a session.
func (c *Config) UpdateSessionPRCommentCount(sessionID string, count int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	for i := range c.Sessions {
		if c.Sessions[i].ID == sessionID {
			c.Sessions[i].PRCommentCount = count
			return true
		}
	}
	return false
}

// UpdateSessionPRCommentsAddressedCount updates the addressed PR comment count for a session.
// This tracks the comment count at the time comments were last sent to Claude for addressing.
func (c *Config) UpdateSessionPRCommentsAddressedCount(sessionID string, count int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	for i := range c.Sessions {
		if c.Sessions[i].ID == sessionID {
			c.Sessions[i].PRCommentsAddressedCount = count
			return true
		}
	}
	return false
}
