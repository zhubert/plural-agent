package daemonstate

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/zhubert/erg/internal/config"
	"github.com/zhubert/erg/internal/paths"
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

	// StepDisplayName is a human-readable label for CurrentStep, set when the
	// workflow engine advances to a new step. Empty for custom workflows that
	// don't define display names; callers should fall back to StepLabel.
	StepDisplayName string `json:"step_display_name,omitempty"`

	// Per-session spend (accumulated across all turns in this session)
	CostUSD      float64 `json:"cost_usd,omitempty"`
	InputTokens  int     `json:"input_tokens,omitempty"`
	OutputTokens int     `json:"output_tokens,omitempty"`
}

// ConsumesSlot returns true if the work item currently consumes a concurrency slot.
// This is true when the item has an active async worker (Phase == "async_pending"
// or Phase == "addressing_feedback"), UNLESS the item is in the await_review step.
// Items awaiting review are "set aside" — they should never block new coding work
// from starting, even while actively addressing PR feedback comments.
func (item *WorkItem) ConsumesSlot() bool {
	if item.CurrentStep == "await_review" {
		return false
	}
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

	// Spend tracking — accumulated since daemon last started (reset on restart)
	TotalCostUSD      float64 `json:"total_cost_usd"`
	TotalOutputTokens int     `json:"total_output_tokens"`
	TotalInputTokens  int     `json:"total_input_tokens"`

	// Repo display labels resolved at daemon startup from git remote URLs.
	// RepoLabels is an ordered list of owner/repo labels for all repos this daemon manages.
	// RepoPathLabels maps local filesystem path → owner/repo label.
	// Both fields are omitempty for backward compatibility with existing state files.
	RepoLabels     []string          `json:"repo_labels,omitempty"`
	RepoPathLabels map[string]string `json:"repo_path_labels,omitempty"`

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
		dir = filepath.Join(home, ".erg")
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
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create state directory: %w", err)
	}
	// Tighten permissions on existing directories (MkdirAll ignores mode if dir exists).
	if runtime.GOOS != "windows" {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("failed to set state directory permissions: %w", err)
		}
	}

	// Atomic write: temp file + rename.
	// Remove any stale tmp file first — WriteFile does not update permissions on existing files.
	tmpFile := s.filePath + ".tmp"
	if err := os.Remove(tmpFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove existing temp state file: %w", err)
	}
	if err := os.WriteFile(tmpFile, data, 0o600); err != nil {
		return fmt.Errorf("failed to write temp state file: %w", err)
	}
	if err := os.Rename(tmpFile, s.filePath); err != nil {
		os.Remove(tmpFile)
		return fmt.Errorf("failed to rename state file: %w", err)
	}

	return nil
}

// AdvanceWorkItem moves a work item to a new step and phase.
// An optional displayName parameter sets StepDisplayName on the item.
// When the step changes and no displayName is provided, StepDisplayName is cleared.
// When only the phase changes (step unchanged), StepDisplayName is preserved.
func (s *DaemonState) AdvanceWorkItem(id, newStep, newPhase string, displayName ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	item, ok := s.WorkItems[id]
	if !ok {
		return fmt.Errorf("work item not found: %s", id)
	}

	now := time.Now()
	stepChanged := item.CurrentStep != newStep
	if stepChanged {
		item.StepEnteredAt = now
	}
	item.CurrentStep = newStep
	item.Phase = newPhase
	if len(displayName) > 0 {
		item.StepDisplayName = displayName[0]
	} else if stepChanged {
		// Clear display name when advancing to a new step without an explicit name.
		item.StepDisplayName = ""
	}
	// When the step is unchanged (phase-only reset), preserve existing StepDisplayName.
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

// GetWorkItem returns a copy of the work item by ID.
// Returns the zero value and false if not found.
func (s *DaemonState) GetWorkItem(id string) (WorkItem, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.WorkItems[id]
	if !ok {
		return WorkItem{}, false
	}
	return *item, true
}

// GetWorkItemBySessionID returns a copy of the work item associated with the
// given session ID. Returns the zero value and false if no match is found.
// This is an O(n) scan, which is acceptable because work item count is bounded
// by max_concurrent (typically 1–10). An index is not maintained because
// SessionID is mutated through UpdateWorkItem's callback, making index
// consistency harder than the linear scan it would save.
func (s *DaemonState) GetWorkItemBySessionID(sessionID string) (WorkItem, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.WorkItems {
		if item.SessionID == sessionID {
			return *item, true
		}
	}
	return WorkItem{}, false
}

// GetWorkItemsByState returns copies of all work items in a given state.
func (s *DaemonState) GetWorkItemsByState(state WorkItemState) []WorkItem {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var items []WorkItem
	for _, item := range s.WorkItems {
		if item.State == state {
			items = append(items, *item)
		}
	}
	return items
}

// GetActiveWorkItems returns copies of all non-terminal, non-queued work items.
func (s *DaemonState) GetActiveWorkItems() []WorkItem {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var items []WorkItem
	for _, item := range s.WorkItems {
		if !item.IsTerminal() && item.State != WorkItemQueued {
			items = append(items, *item)
		}
	}
	return items
}

// GetAllWorkItems returns copies of all work items regardless of state.
// Safe to call concurrently; the returned slice is independent of internal state.
func (s *DaemonState) GetAllWorkItems() []WorkItem {
	s.mu.RLock()
	defer s.mu.RUnlock()

	items := make([]WorkItem, 0, len(s.WorkItems))
	for _, item := range s.WorkItems {
		items = append(items, *item)
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

// HasWorkItemForIssue checks if a work item already exists for the given issue.
// Returns true for any work item that matches, regardless of state — active,
// completed, or failed. Because the ai-assisted label is now permanent and
// never removed from issues, every terminal work item must block re-polling
// to prevent the poller from re-adding the same issue on every cycle.
// Terminal items are eventually cleaned up by PruneTerminalItems, at which
// point the issue becomes eligible for re-polling if the label is still present.
func (s *DaemonState) HasWorkItemForIssue(issueSource, issueID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, item := range s.WorkItems {
		if item.IssueRef.Source == issueSource && item.IssueRef.ID == issueID {
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
// ErrorCount is only incremented when msg is non-empty; passing "" clears the message without affecting the count.
func (s *DaemonState) SetErrorMessage(id, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if item, ok := s.WorkItems[id]; ok {
		item.ErrorMessage = msg
		if msg != "" {
			item.ErrorCount++
		}
		item.UpdatedAt = time.Now()
	}
}

// SetLastPollAt updates LastPollAt under the write lock.
// This must be used instead of direct field assignment to avoid a data race
// with concurrent readers that call Save() under RLock.
func (s *DaemonState) SetLastPollAt(t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LastPollAt = t
}

// GetLastPollAt returns LastPollAt under the read lock.
func (s *DaemonState) GetLastPollAt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.LastPollAt
}

// AddSpend accumulates token and cost data from a completed Claude response.
// Thread-safe; may be called concurrently from multiple worker goroutines.
func (s *DaemonState) AddSpend(costUSD float64, outputTokens, inputTokens int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.TotalCostUSD += costUSD
	s.TotalOutputTokens += outputTokens
	s.TotalInputTokens += inputTokens
}

// RecordItemSpend accumulates spend data on the named work item.
// Thread-safe; may be called concurrently from multiple worker goroutines.
func (s *DaemonState) RecordItemSpend(id string, costUSD float64, outputTokens, inputTokens int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if item, ok := s.WorkItems[id]; ok {
		item.CostUSD += costUSD
		item.OutputTokens += outputTokens
		item.InputTokens += inputTokens
	}
}

// ResetSpend zeroes the accumulated spend counters.
// Called when the daemon starts to ensure cost tracking reflects only the
// current daemon run, not previous runs.
func (s *DaemonState) ResetSpend() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.TotalCostUSD = 0
	s.TotalOutputTokens = 0
	s.TotalInputTokens = 0
}

// GetSpend returns the accumulated spend totals in a thread-safe manner.
func (s *DaemonState) GetSpend() (costUSD float64, outputTokens, inputTokens int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.TotalCostUSD, s.TotalOutputTokens, s.TotalInputTokens
}

// SetRepoLabels stores resolved owner/repo display labels under the write lock.
// labels is an ordered list matching config.GetRepos(); pathLabels maps local path → label.
func (s *DaemonState) SetRepoLabels(labels []string, pathLabels map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Defensive copy of labels slice to prevent callers from mutating internal state.
	if labels != nil {
		copiedLabels := make([]string, len(labels))
		copy(copiedLabels, labels)
		s.RepoLabels = copiedLabels
	} else {
		s.RepoLabels = nil
	}

	// Defensive copy of pathLabels map to prevent callers from mutating internal state.
	if pathLabels != nil {
		copiedPathLabels := make(map[string]string, len(pathLabels))
		for k, v := range pathLabels {
			copiedPathLabels[k] = v
		}
		s.RepoPathLabels = copiedPathLabels
	} else {
		s.RepoPathLabels = nil
	}
}

// GetRepoLabels returns the resolved display labels in a thread-safe manner.
func (s *DaemonState) GetRepoLabels() (labels []string, pathLabels map[string]string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Return defensive copies to avoid exposing internal mutable state.
	if s.RepoLabels != nil {
		labels = make([]string, len(s.RepoLabels))
		copy(labels, s.RepoLabels)
	}

	if s.RepoPathLabels != nil {
		pathLabels = make(map[string]string, len(s.RepoPathLabels))
		for k, v := range s.RepoPathLabels {
			pathLabels[k] = v
		}
	}

	return labels, pathLabels
}

// StateExists returns true if any daemon state file exists on disk.
func StateExists() bool {
	dir, err := paths.DataDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".erg")
	}
	// Check for both legacy and per-repo state files
	matches, _ := filepath.Glob(filepath.Join(dir, "daemon-state*.json"))
	return len(matches) > 0
}

// PruneTerminalItems removes completed and failed work items that finished
// more than maxAge ago. This prevents unbounded growth of the WorkItems map
// in long-running daemons. Returns the number of items pruned.
func (s *DaemonState) PruneTerminalItems(maxAge time.Duration) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	pruned := 0
	for id, item := range s.WorkItems {
		if !item.IsTerminal() {
			continue
		}
		completedAt := item.CompletedAt
		if completedAt == nil {
			// Terminal item without CompletedAt — use UpdatedAt as fallback.
			completedAt = &item.UpdatedAt
		}
		if now.Sub(*completedAt) > maxAge {
			delete(s.WorkItems, id)
			pruned++
		}
	}
	return pruned
}

// ClearNonTerminalItems removes all non-terminal (queued and active) work items.
// Terminal items (completed, failed) are preserved for dashboard display and history.
// This is used during state reconstruction to wipe stale in-progress items before
// rebuilding them from the issue tracker.
func (s *DaemonState) ClearNonTerminalItems() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for id, item := range s.WorkItems {
		if !item.IsTerminal() {
			delete(s.WorkItems, id)
		}
	}
}

// AddRebuiltWorkItem adds a work item that was rebuilt from the issue tracker.
// Unlike AddWorkItem, this allows setting State and CurrentStep directly
// (not forced to Queued), and does not overwrite fields already set by the caller.
func (s *DaemonState) AddRebuiltWorkItem(item *WorkItem) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	if item.CreatedAt.IsZero() {
		item.CreatedAt = now
	}
	item.UpdatedAt = now
	if item.StepEnteredAt.IsZero() {
		item.StepEnteredAt = now
	}
	if item.StepData == nil {
		item.StepData = make(map[string]any)
	}
	s.WorkItems[item.ID] = item
}

// ClearState removes all daemon state files from disk.
// Returns nil if no files exist.
func ClearState() error {
	dir, err := paths.DataDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".erg")
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "daemon-state*.json"))
	for _, match := range matches {
		if err := os.Remove(match); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove daemon state %s: %w", match, err)
		}
	}
	return nil
}
