package daemonstate

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/zhubert/plural-core/config"
	"github.com/zhubert/plural-core/paths"
)

// WorkItemState represents the current state of a work item in the daemon lifecycle.
// Kept for backward compatibility with serialized state; new items use CurrentStep/Phase.
type WorkItemState string

const (
	WorkItemQueued    WorkItemState = "queued"
	WorkItemActive    WorkItemState = "active"
	WorkItemCompleted WorkItemState = "completed"
	WorkItemFailed    WorkItemState = "failed"
)

// WorkItem tracks a single issue through its full lifecycle.
type WorkItem struct {
	ID                string          `json:"id"`
	IssueRef          config.IssueRef `json:"issue_ref"`
	State             WorkItemState   `json:"state"`
	CurrentStep       string          `json:"current_step"`
	Phase             string          `json:"phase"`
	StepData          map[string]any  `json:"step_data,omitempty"`
	SessionID         string          `json:"session_id"`
	Branch            string          `json:"branch"`
	PRURL             string          `json:"pr_url,omitempty"`
	CommentsAddressed int             `json:"comments_addressed"`
	FeedbackRounds    int             `json:"feedback_rounds"`
	ErrorMessage      string          `json:"error_message,omitempty"`
	ErrorCount        int             `json:"error_count"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	CompletedAt       *time.Time      `json:"completed_at,omitempty"`
	StepEnteredAt     time.Time       `json:"step_entered_at"`
}

// ConsumesSlot returns true if the work item currently consumes a concurrency slot.
// This is true when the item has an active async worker (Phase == "async_pending"
// or Phase == "addressing_feedback").
func (item *WorkItem) ConsumesSlot() bool {
	return item.Phase == "async_pending" || item.Phase == "addressing_feedback"
}

// IsTerminal returns true if the work item is in a terminal state.
func (item *WorkItem) IsTerminal() bool {
	return item.State == WorkItemCompleted || item.State == WorkItemFailed
}

// DaemonState holds the persistent state of the daemon.
type DaemonState struct {
	Version    int                  `json:"version"`
	RepoPath   string               `json:"repo_path"`
	WorkItems  map[string]*WorkItem `json:"work_items"`
	LastPollAt time.Time            `json:"last_poll_at"`
	StartedAt  time.Time            `json:"started_at"`

	mu       sync.RWMutex
	filePath string
}

const stateVersion = 2

// StateFilePath returns the path to the daemon state file for a given repo.
// Each repo gets its own state file keyed by a hash of the repo path,
// preventing multiple daemons for different repos from clobbering each other.
func StateFilePath(repoPath string) string {
	dir, err := paths.DataDir()
	if err != nil {
		// Fall back to home dir
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".plural")
	}
	if repoPath == "" {
		return filepath.Join(dir, "daemon-state.json")
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(repoPath)))
	return filepath.Join(dir, fmt.Sprintf("daemon-state-%s.json", hash[:12]))
}

// NewDaemonState creates a new empty daemon state for the given repo.
func NewDaemonState(repoPath string) *DaemonState {
	return &DaemonState{
		Version:   stateVersion,
		RepoPath:  repoPath,
		WorkItems: make(map[string]*WorkItem),
		StartedAt: time.Now(),
		filePath:  StateFilePath(repoPath),
	}
}

// LoadDaemonState loads daemon state from disk.
// Returns a new empty state if the file doesn't exist.
func LoadDaemonState(repoPath string) (*DaemonState, error) {
	fp := StateFilePath(repoPath)

	data, err := os.ReadFile(fp)
	if err != nil {
		if os.IsNotExist(err) {
			return NewDaemonState(repoPath), nil
		}
		return nil, fmt.Errorf("failed to read daemon state: %w", err)
	}

	var state DaemonState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse daemon state: %w", err)
	}

	state.filePath = fp
	if state.WorkItems == nil {
		state.WorkItems = make(map[string]*WorkItem)
	}

	// Normalize legacy state values: anything that isn't queued, completed,
	// or failed becomes "active". This handles old files with values like
	// "coding", "pr_created", "awaiting_review", etc.
	for _, item := range state.WorkItems {
		switch item.State {
		case WorkItemQueued, WorkItemCompleted, WorkItemFailed:
			// already valid
		default:
			item.State = WorkItemActive
		}
	}

	// Validate repo path matches
	if state.RepoPath != repoPath {
		return nil, fmt.Errorf("daemon state repo mismatch: expected %s, got %s", repoPath, state.RepoPath)
	}

	return &state, nil
}

// Save persists the daemon state to disk atomically (write temp file, then rename).
func (s *DaemonState) Save() error {
	s.mu.RLock()
	data, err := json.MarshalIndent(s, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("failed to marshal daemon state: %w", err)
	}

	dir := filepath.Dir(s.filePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create state directory: %w", err)
	}

	// Atomic write: temp file + rename
	tmpFile := s.filePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0o644); err != nil {
		return fmt.Errorf("failed to write temp state file: %w", err)
	}
	if err := os.Rename(tmpFile, s.filePath); err != nil {
		os.Remove(tmpFile)
		return fmt.Errorf("failed to rename state file: %w", err)
	}

	return nil
}

// AdvanceWorkItem moves a work item to a new step and phase.
func (s *DaemonState) AdvanceWorkItem(id, newStep, newPhase string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	item, ok := s.WorkItems[id]
	if !ok {
		return fmt.Errorf("work item not found: %s", id)
	}

	now := time.Now()
	if item.CurrentStep != newStep {
		item.StepEnteredAt = now
	}
	item.CurrentStep = newStep
	item.Phase = newPhase
	item.UpdatedAt = now

	return nil
}

// MarkWorkItemTerminal marks a work item as completed or failed.
func (s *DaemonState) MarkWorkItemTerminal(id string, success bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	item, ok := s.WorkItems[id]
	if !ok {
		return fmt.Errorf("work item not found: %s", id)
	}

	if success {
		item.State = WorkItemCompleted
	} else {
		item.State = WorkItemFailed
	}

	now := time.Now()
	item.CompletedAt = &now
	item.UpdatedAt = now

	return nil
}

// AddWorkItem adds a new work item in the Queued state.
func (s *DaemonState) AddWorkItem(item *WorkItem) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	item.State = WorkItemQueued
	item.Phase = "idle"
	item.CreatedAt = now
	item.UpdatedAt = now
	item.StepEnteredAt = now
	if item.StepData == nil {
		item.StepData = make(map[string]any)
	}
	s.WorkItems[item.ID] = item
}

// GetWorkItem returns a work item by ID (nil if not found).
func (s *DaemonState) GetWorkItem(id string) *WorkItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.WorkItems[id]
}

// GetWorkItemsByState returns all work items in a given state.
func (s *DaemonState) GetWorkItemsByState(state WorkItemState) []*WorkItem {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var items []*WorkItem
	for _, item := range s.WorkItems {
		if item.State == state {
			items = append(items, item)
		}
	}
	return items
}

// GetWorkItemsByStep returns all work items at a given step.
func (s *DaemonState) GetWorkItemsByStep(step string) []*WorkItem {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var items []*WorkItem
	for _, item := range s.WorkItems {
		if item.CurrentStep == step && !item.IsTerminal() {
			items = append(items, item)
		}
	}
	return items
}

// GetActiveWorkItems returns all non-terminal, non-queued work items.
func (s *DaemonState) GetActiveWorkItems() []*WorkItem {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var items []*WorkItem
	for _, item := range s.WorkItems {
		if !item.IsTerminal() && item.State != WorkItemQueued {
			items = append(items, item)
		}
	}
	return items
}

// ActiveSlotCount returns the number of work items consuming concurrency slots.
func (s *DaemonState) ActiveSlotCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	count := 0
	for _, item := range s.WorkItems {
		if item.ConsumesSlot() {
			count++
		}
	}
	return count
}

// recentFailCooldown is how long after a work item fails before the same
// issue can be re-queued. This prevents infinite re-queue loops when an issue
// persistently fails (e.g., Docker daemon is down).
const recentFailCooldown = 5 * time.Minute

// HasWorkItemForIssue checks if a work item already exists for the given issue.
// Returns true for active items and also for recently-failed items to prevent
// infinite re-queue loops.
func (s *DaemonState) HasWorkItemForIssue(issueSource, issueID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, item := range s.WorkItems {
		if item.IssueRef.Source != issueSource || item.IssueRef.ID != issueID {
			continue
		}
		if !item.IsTerminal() {
			return true
		}
		// Recently-failed items still count to prevent re-queue storms
		if item.CompletedAt != nil && time.Since(*item.CompletedAt) < recentFailCooldown {
			return true
		}
	}
	return false
}

// UpdateWorkItem applies a mutation function to a work item under the state lock.
// This is useful for recovery and other cases that need to modify multiple fields atomically.
func (s *DaemonState) UpdateWorkItem(id string, fn func(*WorkItem)) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if item, ok := s.WorkItems[id]; ok {
		fn(item)
	}
}

// SetErrorMessage sets the error message on a work item and increments the error count.
func (s *DaemonState) SetErrorMessage(id, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if item, ok := s.WorkItems[id]; ok {
		item.ErrorMessage = msg
		item.ErrorCount++
		item.UpdatedAt = time.Now()
	}
}

// StateExists returns true if any daemon state file exists on disk.
func StateExists() bool {
	dir, err := paths.DataDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".plural")
	}
	// Check for both legacy and per-repo state files
	matches, _ := filepath.Glob(filepath.Join(dir, "daemon-state*.json"))
	return len(matches) > 0
}

// ClearState removes all daemon state files from disk.
// Returns nil if no files exist.
func ClearState() error {
	dir, err := paths.DataDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".plural")
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "daemon-state*.json"))
	for _, match := range matches {
		if err := os.Remove(match); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove daemon state %s: %w", match, err)
		}
	}
	return nil
}
