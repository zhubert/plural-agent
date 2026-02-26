package git

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/zhubert/erg/internal/logger"
)

// Configuration constants for branch operations
const (
	// MaxBranchNameLength is the maximum length for auto-generated branch names.
	// User-provided branch names can be longer (up to MaxBranchNameValidation).
	MaxBranchNameLength = 50
)

// BranchDivergence represents the divergence between local and remote branches.
type BranchDivergence struct {
	Behind int // Number of commits local is behind remote
	Ahead  int // Number of commits local is ahead of remote
}

// IsDiverged returns true if the branches have diverged (both ahead and behind).
func (d *BranchDivergence) IsDiverged() bool {
	return d.Behind > 0 && d.Ahead > 0
}

// CanFastForward returns true if local can fast-forward to remote (not ahead).
func (d *BranchDivergence) CanFastForward() bool {
	return d.Ahead == 0
}

// HasRemoteOrigin checks if the repository has a remote named "origin"
func (s *GitService) HasRemoteOrigin(ctx context.Context, repoPath string) bool {
	_, _, err := s.executor.Run(ctx, repoPath, "git", "remote", "get-url", "origin")
	return err == nil
}

// GetRemoteOriginURL returns the URL of the "origin" remote.
func (s *GitService) GetRemoteOriginURL(ctx context.Context, repoPath string) (string, error) {
	output, err := s.executor.Output(ctx, repoPath, "git", "remote", "get-url", "origin")
	if err != nil {
		return "", fmt.Errorf("failed to get remote origin URL: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// ExtractOwnerRepo extracts "owner/repo" from a git remote URL.
// Supports SSH (git@github.com:owner/repo.git) and HTTPS (https://github.com/owner/repo.git) formats.
// Returns empty string if the URL cannot be parsed.
func ExtractOwnerRepo(remoteURL string) string {
	remoteURL = strings.TrimSpace(remoteURL)
	if remoteURL == "" {
		return ""
	}

	// SSH format: git@github.com:owner/repo.git
	if strings.Contains(remoteURL, ":") && strings.HasPrefix(remoteURL, "git@") {
		// Extract part after ":"
		parts := strings.SplitN(remoteURL, ":", 2)
		if len(parts) == 2 {
			path := strings.TrimSuffix(parts[1], ".git")
			if strings.Contains(path, "/") {
				return path
			}
		}
		return ""
	}

	// HTTPS/HTTP format: https://github.com/owner/repo.git
	// Strip scheme and host
	for _, prefix := range []string{"https://", "http://"} {
		if strings.HasPrefix(remoteURL, prefix) {
			rest := remoteURL[len(prefix):]
			// rest is like "github.com/owner/repo.git"
			// Find first "/" to skip host
			_, after, ok := strings.Cut(rest, "/")
			if !ok {
				return ""
			}
			path := strings.TrimSuffix(after, ".git")
			if strings.Contains(path, "/") {
				return path
			}
			return ""
		}
	}

	return ""
}

// GetDefaultBranch returns the default branch name (main or master)
func (s *GitService) GetDefaultBranch(ctx context.Context, repoPath string) string {
	// Try to get the default branch from origin
	output, err := s.executor.Output(ctx, repoPath, "git", "symbolic-ref", "refs/remotes/origin/HEAD")
	if err == nil {
		// Output is like "refs/remotes/origin/main"
		ref := strings.TrimSpace(string(output))
		parts := strings.Split(ref, "/")
		if len(parts) > 0 {
			return parts[len(parts)-1]
		}
	}

	// Fallback: check if main exists, otherwise use master
	_, _, err = s.executor.Run(ctx, repoPath, "git", "rev-parse", "--verify", "main")
	if err == nil {
		return "main"
	}

	return "master"
}

// GetBranchDivergence returns how many commits the local branch is behind and ahead
// of the remote branch. Uses git rev-list --count --left-right which outputs "behind\tahead".
// Returns an error if either branch doesn't exist or comparison fails.
func (s *GitService) GetBranchDivergence(ctx context.Context, repoPath, localBranch, remoteBranch string) (*BranchDivergence, error) {
	// git rev-list --count --left-right remoteBranch...localBranch
	// Output format: "behind<tab>ahead"
	output, err := s.executor.Output(ctx, repoPath, "git", "rev-list", "--count", "--left-right",
		fmt.Sprintf("%s...%s", remoteBranch, localBranch))
	if err != nil {
		return nil, fmt.Errorf("failed to get branch divergence: %w", err)
	}

	// Parse "behind\tahead" format
	parts := strings.Split(strings.TrimSpace(string(output)), "\t")
	if len(parts) != 2 {
		return nil, fmt.Errorf("unexpected rev-list output format: %q", string(output))
	}

	behind, err := strconv.Atoi(parts[0])
	if err != nil {
		return nil, fmt.Errorf("failed to parse behind count: %w", err)
	}

	ahead, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil, fmt.Errorf("failed to parse ahead count: %w", err)
	}

	return &BranchDivergence{Behind: behind, Ahead: ahead}, nil
}

// HasTrackingBranch checks if the given branch has an upstream tracking branch configured.
// Uses git config to check for branch.<name>.remote which is set when tracking is configured.
func (s *GitService) HasTrackingBranch(ctx context.Context, repoPath, branch string) bool {
	_, err := s.executor.Output(ctx, repoPath, "git", "config", "--get", fmt.Sprintf("branch.%s.remote", branch))
	return err == nil
}

// RemoteBranchExists checks if a remote branch reference exists (e.g., "origin/main").
// Uses git rev-parse --verify which exits 0 if the ref exists, non-zero otherwise.
func (s *GitService) RemoteBranchExists(ctx context.Context, repoPath, remoteBranch string) bool {
	_, _, err := s.executor.Run(ctx, repoPath, "git", "rev-parse", "--verify", remoteBranch)
	return err == nil
}

// RenameBranch renames a git branch in the given worktree.
// The worktree must have the branch checked out.
func (s *GitService) RenameBranch(ctx context.Context, worktreePath, oldName, newName string) error {
	output, err := s.executor.CombinedOutput(ctx, worktreePath, "git", "branch", "-m", oldName, newName)
	if err != nil {
		return fmt.Errorf("git branch rename failed: %s: %w", string(output), err)
	}

	logger.WithComponent("git").Info("renamed branch", "oldName", oldName, "newName", newName, "worktree", worktreePath)
	return nil
}

// GetCurrentBranch returns the name of the currently checked out branch in the given repo/worktree.
// Returns an error if HEAD is detached or the command fails.
func (s *GitService) GetCurrentBranch(ctx context.Context, repoPath string) (string, error) {
	output, err := s.executor.Output(ctx, repoPath, "git", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", fmt.Errorf("failed to get current branch: %w", err)
	}

	branch := strings.TrimSpace(string(output))
	if branch == "HEAD" {
		return "", fmt.Errorf("HEAD is detached (not on a branch)")
	}

	return branch, nil
}

// CheckoutBranch checks out the specified branch in the given repo.
// Returns an error if the checkout fails (e.g., uncommitted changes would be overwritten).
func (s *GitService) CheckoutBranch(ctx context.Context, repoPath, branch string) error {
	output, err := s.executor.CombinedOutput(ctx, repoPath, "git", "checkout", branch)
	if err != nil {
		return fmt.Errorf("git checkout failed: %s: %w", strings.TrimSpace(string(output)), err)
	}

	logger.WithComponent("git").Info("checked out branch", "branch", branch, "repoPath", repoPath)
	return nil
}

// CheckoutBranchIgnoreWorktrees checks out the specified branch, even if it's
// already checked out in another worktree. This is useful for the preview feature
// where we want to temporarily view a worktree's branch in the main repo.
func (s *GitService) CheckoutBranchIgnoreWorktrees(ctx context.Context, repoPath, branch string) error {
	output, err := s.executor.CombinedOutput(ctx, repoPath, "git", "checkout", "--ignore-other-worktrees", branch)
	if err != nil {
		return fmt.Errorf("git checkout failed: %s: %w", strings.TrimSpace(string(output)), err)
	}

	logger.WithComponent("git").Info("checked out branch (ignoring worktrees)", "branch", branch, "repoPath", repoPath)
	return nil
}

// sanitizeBranchName ensures a branch name is valid for git
func sanitizeBranchName(name string) string {
	// Convert to lowercase
	name = strings.ToLower(name)

	// Replace spaces and underscores with hyphens
	name = strings.ReplaceAll(name, " ", "-")
	name = strings.ReplaceAll(name, "_", "-")

	// Remove any characters that aren't alphanumeric or hyphens
	var result strings.Builder
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			result.WriteRune(c)
		}
	}
	name = result.String()

	// Remove leading/trailing hyphens and collapse multiple hyphens
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	name = strings.Trim(name, "-")

	// Truncate if too long
	if len(name) > MaxBranchNameLength {
		name = name[:MaxBranchNameLength]
		// Don't end with a hyphen
		name = strings.TrimRight(name, "-")
	}

	return name
}
