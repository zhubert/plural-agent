package git

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zhubert/erg/internal/config"
	"github.com/zhubert/erg/internal/logger"
)

// PRState represents the state of a GitHub pull request
type PRState string

const (
	PRStateOpen    PRState = "OPEN"
	PRStateMerged  PRState = "MERGED"
	PRStateClosed  PRState = "CLOSED"
	PRStateUnknown PRState = ""
)

// ReviewDecision represents the GitHub PR review decision.
type ReviewDecision string

const (
	ReviewApproved         ReviewDecision = "APPROVED"
	ReviewChangesRequested ReviewDecision = "CHANGES_REQUESTED"
	ReviewNone             ReviewDecision = "" // No actionable reviews yet
)

// GetPRState returns the state of a PR for the given branch using the gh CLI.
// Returns PRStateUnknown and an error if the PR cannot be found or gh fails.
func (s *GitService) GetPRState(ctx context.Context, repoPath, branch string) (PRState, error) {
	output, err := s.executor.Output(ctx, repoPath, "gh", "pr", "view", branch, "--json", "state")
	if err != nil {
		return PRStateUnknown, fmt.Errorf("gh pr view failed: %w", err)
	}

	var result struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return PRStateUnknown, fmt.Errorf("failed to parse PR state: %w", err)
	}

	switch PRState(result.State) {
	case PRStateOpen, PRStateMerged, PRStateClosed:
		return PRState(result.State), nil
	default:
		// Treat unrecognized states (e.g., DRAFT) as OPEN
		return PRStateOpen, nil
	}
}

// GetBatchPRStates returns the PR states for multiple branches.
// It uses `gh pr list --head <branch>` to filter by each branch individually,
// which correctly handles repos with any number of total PRs.
// Branches without a matching PR are omitted from the result map.
func (s *GitService) GetBatchPRStates(ctx context.Context, repoPath string, branches []string) (map[string]PRState, error) {
	result := make(map[string]PRState, len(branches))
	for _, branch := range branches {
		output, err := s.executor.Output(ctx, repoPath, "gh", "pr", "list",
			"--state", "all",
			"--json", "state,headRefName",
			"--head", branch,
		)
		if err != nil {
			return nil, fmt.Errorf("gh pr list failed: %w", err)
		}

		var prs []struct {
			State       string `json:"state"`
			HeadRefName string `json:"headRefName"`
		}
		if err := json.Unmarshal(output, &prs); err != nil {
			return nil, fmt.Errorf("failed to parse PR list: %w", err)
		}

		for _, pr := range prs {
			if pr.HeadRefName != branch {
				continue
			}
			switch PRState(pr.State) {
			case PRStateOpen, PRStateMerged, PRStateClosed:
				result[branch] = PRState(pr.State)
			default:
				// Treat unrecognized states (e.g., DRAFT) as OPEN
				result[branch] = PRStateOpen
			}
		}
	}

	return result, nil
}

// LinkedPR represents a pull request that references a GitHub issue.
type LinkedPR struct {
	Number int
	State  PRState
	URL    string
}

// GetLinkedPRsForIssue returns open or merged pull requests that cross-reference the given issue.
// It queries the GitHub GraphQL API for cross-referenced events in the issue timeline.
// Only PRs in OPEN or MERGED state are included — CLOSED PRs are excluded.
func (s *GitService) GetLinkedPRsForIssue(ctx context.Context, repoPath string, issueNumber int) ([]LinkedPR, error) {
	// Resolve owner/repo from the remote URL so we can query the correct repo.
	remoteURL, err := s.GetRemoteOriginURL(ctx, repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to get remote origin URL: %w", err)
	}
	ownerRepo := ExtractOwnerRepo(remoteURL)
	if ownerRepo == "" {
		return nil, fmt.Errorf("could not extract owner/repo from remote URL %q", remoteURL)
	}
	parts := strings.SplitN(ownerRepo, "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid owner/repo format %q", ownerRepo)
	}
	owner, repo := parts[0], parts[1]

	// Use gh api graphql to find cross-referenced PRs in the issue's timeline.
	const query = `query($owner: String!, $repo: String!, $number: Int!) {
  repository(owner: $owner, name: $repo) {
    issue(number: $number) {
      timelineItems(itemTypes: [CROSS_REFERENCED_EVENT], first: 25) {
        nodes {
          ... on CrossReferencedEvent {
            source {
              ... on PullRequest {
                number
                state
                url
              }
            }
          }
        }
      }
    }
  }
}`

	output, err := s.executor.Output(ctx, repoPath, "gh", "api", "graphql",
		"-f", "query="+query,
		"-f", "owner="+owner,
		"-f", "repo="+repo,
		"-F", fmt.Sprintf("number=%d", issueNumber),
	)
	if err != nil {
		return nil, fmt.Errorf("gh api graphql failed: %w", err)
	}

	var gqlResp struct {
		Data struct {
			Repository struct {
				Issue struct {
					TimelineItems struct {
						Nodes []struct {
							Source struct {
								Number int    `json:"number"`
								State  string `json:"state"`
								URL    string `json:"url"`
							} `json:"source"`
						} `json:"nodes"`
					} `json:"timelineItems"`
				} `json:"issue"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(output, &gqlResp); err != nil {
		return nil, fmt.Errorf("failed to parse GraphQL response: %w", err)
	}

	var linked []LinkedPR
	seen := make(map[int]bool)
	for _, node := range gqlResp.Data.Repository.Issue.TimelineItems.Nodes {
		src := node.Source
		if src.Number == 0 {
			// Node is not a PullRequest (e.g., a regular issue cross-reference).
			continue
		}
		if seen[src.Number] {
			continue
		}
		seen[src.Number] = true

		state := PRState(src.State)
		if state != PRStateOpen && state != PRStateMerged {
			continue
		}
		linked = append(linked, LinkedPR{
			Number: src.Number,
			State:  state,
			URL:    src.URL,
		})
	}

	return linked, nil
}

// PRBatchResult holds the state and comment count for a PR from a batch query.
type PRBatchResult struct {
	State        PRState
	CommentCount int // len(comments) + len(actionable reviews) — excludes APPROVED/DISMISSED reviews
}

// GetBatchPRStatesWithComments returns PR states and comment counts for multiple branches.
// Uses `gh pr list --head <branch>` per branch to filter exactly, correctly handling
// repos with any number of total PRs. The comment count is len(comments) + len(reviews),
// which captures top-level PR comments and review submissions.
func (s *GitService) GetBatchPRStatesWithComments(ctx context.Context, repoPath string, branches []string) (map[string]PRBatchResult, error) {
	result := make(map[string]PRBatchResult, len(branches))
	for _, branch := range branches {
		output, err := s.executor.Output(ctx, repoPath, "gh", "pr", "list",
			"--state", "all",
			"--json", "state,headRefName,comments,reviews",
			"--head", branch,
		)
		if err != nil {
			return nil, fmt.Errorf("gh pr list failed: %w", err)
		}

		var prs []struct {
			State       string            `json:"state"`
			HeadRefName string            `json:"headRefName"`
			Comments    []json.RawMessage `json:"comments"`
			Reviews     []json.RawMessage `json:"reviews"`
		}
		if err := json.Unmarshal(output, &prs); err != nil {
			return nil, fmt.Errorf("failed to parse PR list: %w", err)
		}

		for _, pr := range prs {
			if pr.HeadRefName != branch {
				continue
			}
			var state PRState
			switch PRState(pr.State) {
			case PRStateOpen, PRStateMerged, PRStateClosed:
				state = PRState(pr.State)
			default:
				state = PRStateOpen
			}

			// Count reviews, excluding APPROVED and DISMISSED reviews
			// (approval reviews don't contain actionable feedback)
			actionableReviewCount := 0
			for _, rawReview := range pr.Reviews {
				var review struct {
					State string `json:"state"`
				}
				if err := json.Unmarshal(rawReview, &review); err == nil {
					if review.State != "APPROVED" && review.State != "DISMISSED" {
						actionableReviewCount++
					}
				}
			}

			result[branch] = PRBatchResult{
				State:        state,
				CommentCount: len(pr.Comments) + actionableReviewCount,
			}
		}
	}

	return result, nil
}

// PRReviewComment represents a single review comment from a GitHub pull request.
// This can be a top-level PR comment, a review body, or an inline code review comment.
type PRReviewComment struct {
	Author string // GitHub username
	Body   string // Comment text
	Path   string // File path (empty for top-level/review body comments)
	Line   int    // Line number (0 for top-level/review body comments)
	URL    string // Permalink
}

// JSON types for gh pr view --json reviews,comments response
type ghPRReviewsResponse struct {
	Reviews  []ghReview  `json:"reviews"`
	Comments []ghComment `json:"comments"`
}

type ghReview struct {
	Author   ghAuthor    `json:"author"`
	Body     string      `json:"body"`
	State    string      `json:"state"`
	Comments []ghComment `json:"comments"`
}

type ghComment struct {
	Author ghAuthor `json:"author"`
	Body   string   `json:"body"`
	Path   string   `json:"path"`
	Line   int      `json:"line"`
	URL    string   `json:"url"`
}

type ghAuthor struct {
	Login string `json:"login"`
}

// FetchPRReviewComments fetches review comments from a pull request using the gh CLI.
// Returns top-level PR comments, review body comments, and inline code review comments
// as a flattened slice. The repoPath is used as the working directory.
func (s *GitService) FetchPRReviewComments(ctx context.Context, repoPath, branch string) ([]PRReviewComment, error) {
	output, err := s.executor.Output(ctx, repoPath, "gh", "pr", "view", branch, "--json", "reviews,comments")
	if err != nil {
		return nil, fmt.Errorf("gh pr view failed: %w", err)
	}

	var response ghPRReviewsResponse
	if err := json.Unmarshal(output, &response); err != nil {
		return nil, fmt.Errorf("failed to parse PR review data: %w", err)
	}

	var comments []PRReviewComment

	// Top-level PR comments
	for _, c := range response.Comments {
		if c.Body == "" {
			continue
		}
		comments = append(comments, PRReviewComment{
			Author: c.Author.Login,
			Body:   c.Body,
			URL:    c.URL,
		})
	}

	// Review-level body comments and inline code review comments
	for _, review := range response.Reviews {
		isApproval := review.State == "APPROVED" || review.State == "DISMISSED"

		// Include review body if non-empty, but skip for approval/dismissed reviews
		// (approval body text like "LGTM" isn't actionable feedback)
		if review.Body != "" && !isApproval {
			comments = append(comments, PRReviewComment{
				Author: review.Author.Login,
				Body:   review.Body,
			})
		}
		// Always include inline code review comments, even from approval reviews
		// (reviewer may have approved with nits)
		for _, c := range review.Comments {
			if c.Body == "" {
				continue
			}
			comments = append(comments, PRReviewComment{
				Author: c.Author.Login,
				Body:   c.Body,
				Path:   c.Path,
				Line:   c.Line,
				URL:    c.URL,
			})
		}
	}

	return comments, nil
}

// GitHubIssue represents a GitHub issue fetched via the gh CLI
type GitHubIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	URL    string `json:"url"`
}

// FetchGitHubIssues fetches open issues from a GitHub repository using the gh CLI.
// The repoPath is used as the working directory to determine which repo to query.
func (s *GitService) FetchGitHubIssues(ctx context.Context, repoPath string) ([]GitHubIssue, error) {
	output, err := s.executor.Output(ctx, repoPath, "gh", "issue", "list",
		"--json", "number,title,body,url",
		"--state", "open",
	)
	if err != nil {
		return nil, fmt.Errorf("gh issue list failed: %w", err)
	}

	var issues []GitHubIssue
	if err := json.Unmarshal(output, &issues); err != nil {
		return nil, fmt.Errorf("failed to parse issues: %w", err)
	}

	return issues, nil
}

// FetchGitHubIssuesWithLabel fetches open issues with a specific label from a GitHub repository.
func (s *GitService) FetchGitHubIssuesWithLabel(ctx context.Context, repoPath, label string) ([]GitHubIssue, error) {
	args := []string{"issue", "list",
		"--json", "number,title,body,url",
		"--state", "open",
	}
	if label != "" {
		args = append(args, "--label", label)
	}
	output, err := s.executor.Output(ctx, repoPath, "gh", args...)
	if err != nil {
		return nil, fmt.Errorf("gh issue list failed: %w", err)
	}

	var issues []GitHubIssue
	if err := json.Unmarshal(output, &issues); err != nil {
		return nil, fmt.Errorf("failed to parse issues: %w", err)
	}

	return issues, nil
}

// AddIssueLabel adds a label to a GitHub issue using the gh CLI.
func (s *GitService) AddIssueLabel(ctx context.Context, repoPath string, issueNumber int, label string) error {
	_, _, err := s.executor.Run(ctx, repoPath, "gh", "issue", "edit",
		fmt.Sprintf("%d", issueNumber),
		"--add-label", label,
	)
	if err != nil {
		return fmt.Errorf("gh issue edit --add-label failed: %w", err)
	}
	return nil
}

// RemoveIssueLabel removes a label from a GitHub issue using the gh CLI.
func (s *GitService) RemoveIssueLabel(ctx context.Context, repoPath string, issueNumber int, label string) error {
	_, _, err := s.executor.Run(ctx, repoPath, "gh", "issue", "edit",
		fmt.Sprintf("%d", issueNumber),
		"--remove-label", label,
	)
	if err != nil {
		return fmt.Errorf("gh issue edit --remove-label failed: %w", err)
	}
	return nil
}

// CommentOnIssue leaves a comment on a GitHub issue using the gh CLI.
func (s *GitService) CommentOnIssue(ctx context.Context, repoPath string, issueNumber int, body string) error {
	_, _, err := s.executor.Run(ctx, repoPath, "gh", "issue", "comment",
		fmt.Sprintf("%d", issueNumber),
		"--body", body,
	)
	if err != nil {
		return fmt.Errorf("gh issue comment failed: %w", err)
	}
	return nil
}

// UploadTranscriptToPR posts a session transcript as a comment on the PR for the given branch.
// The transcript is formatted as a collapsed <details> block so it does not clutter the PR.
func (s *GitService) UploadTranscriptToPR(ctx context.Context, repoPath, branch, transcript string) error {
	if transcript == "" {
		return nil
	}
	body := "<details>\n<summary>Session Transcript</summary>\n\n```text\n" + transcript + "\n```\n</details>"
	_, _, err := s.executor.Run(ctx, repoPath, "gh", "pr", "comment", branch, "--body", body)
	if err != nil {
		return fmt.Errorf("gh pr comment failed: %w", err)
	}
	return nil
}

// MergeableStatus represents whether a PR can be cleanly merged.
type MergeableStatus string

const (
	MergeableMergeable   MergeableStatus = "MERGEABLE"
	MergeableConflicting MergeableStatus = "CONFLICTING"
	MergeableUnknown     MergeableStatus = "UNKNOWN"
)

// CheckPRMergeableStatus checks the mergeable status of a PR for the given branch.
// Uses `gh pr view --json mergeable` which returns MERGEABLE, CONFLICTING, or UNKNOWN.
func (s *GitService) CheckPRMergeableStatus(ctx context.Context, repoPath, branch string) (MergeableStatus, error) {
	output, err := s.executor.Output(ctx, repoPath, "gh", "pr", "view", branch, "--json", "mergeable")
	if err != nil {
		return MergeableUnknown, fmt.Errorf("gh pr view --json mergeable failed: %w", err)
	}

	var result struct {
		Mergeable string `json:"mergeable"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return MergeableUnknown, fmt.Errorf("failed to parse mergeable status: %w", err)
	}

	switch MergeableStatus(result.Mergeable) {
	case MergeableMergeable:
		return MergeableMergeable, nil
	case MergeableConflicting:
		return MergeableConflicting, nil
	default:
		return MergeableUnknown, nil
	}
}

// RebaseBranch rebases a branch onto the latest base branch and force-pushes.
// This is a mechanical rebase (no Claude needed). If real file-level conflicts
// exist, the rebase is aborted and an error is returned.
func (s *GitService) RebaseBranch(ctx context.Context, worktreePath, branch, baseBranch string) error {
	// Fetch latest base branch
	_, err := s.executor.CombinedOutput(ctx, worktreePath, "git", "fetch", "origin", baseBranch)
	if err != nil {
		return fmt.Errorf("git fetch origin %s failed: %w", baseBranch, err)
	}

	// Attempt rebase
	_, rebaseErr := s.executor.CombinedOutput(ctx, worktreePath, "git", "rebase", "origin/"+baseBranch)
	if rebaseErr != nil {
		// Abort the rebase to leave the worktree in a clean state
		s.executor.CombinedOutput(ctx, worktreePath, "git", "rebase", "--abort")
		return fmt.Errorf("git rebase origin/%s failed (conflicts): %w", baseBranch, rebaseErr)
	}

	// Force-push with lease for safety
	_, pushErr := s.executor.CombinedOutput(ctx, worktreePath, "git", "push", "--force-with-lease", "origin", branch)
	if pushErr != nil {
		return fmt.Errorf("git push --force-with-lease failed: %w", pushErr)
	}

	return nil
}

// CIStatus represents the overall CI check status for a PR.
type CIStatus string

const (
	CIStatusPassing CIStatus = "passing"
	CIStatusFailing CIStatus = "failing"
	CIStatusPending CIStatus = "pending"
	CIStatusNone    CIStatus = "none" // No checks configured
)

// CheckPRChecks checks the CI status of a PR for the given branch.
// Uses `gh pr checks` which returns exit code 0 if all checks pass.
func (s *GitService) CheckPRChecks(ctx context.Context, repoPath, branch string) (CIStatus, error) {
	output, err := s.executor.Output(ctx, repoPath, "gh", "pr", "checks", branch, "--json", "state")
	if err != nil {
		// gh pr checks returns non-zero if checks fail or are pending
		outputStr := string(output)
		if outputStr != "" {
			// Parse the JSON output to determine status
			var checks []struct {
				State string `json:"state"`
			}
			if jsonErr := json.Unmarshal(output, &checks); jsonErr == nil {
				if len(checks) == 0 {
					return CIStatusNone, nil
				}
				hasFailing := false
				hasPending := false
				for _, c := range checks {
					switch c.State {
					case "FAILURE", "ERROR", "CANCELLED":
						hasFailing = true
					case "PENDING", "QUEUED", "IN_PROGRESS", "WAITING", "REQUESTED":
						hasPending = true
					}
				}
				if hasFailing {
					return CIStatusFailing, nil
				}
				if hasPending {
					return CIStatusPending, nil
				}
			}
		}
		// If output is empty (e.g., network error, no PR found), return the error
		// rather than silently treating it as pending (which could cause infinite polling).
		if outputStr == "" {
			return CIStatusPending, fmt.Errorf("gh pr checks failed with no output: %w", err)
		}
		return CIStatusPending, nil
	}

	// Exit code 0 means all checks pass
	var checks []struct {
		State string `json:"state"`
	}
	if jsonErr := json.Unmarshal(output, &checks); jsonErr == nil && len(checks) == 0 {
		return CIStatusNone, nil
	}
	return CIStatusPassing, nil
}

// CheckPRReviewDecision returns the review decision for a PR by inspecting
// individual reviews. GitHub's reviewDecision field only works with branch
// protection rules, so we derive the decision ourselves by looking at each
// reviewer's most recent actionable review state.
func (s *GitService) CheckPRReviewDecision(ctx context.Context, repoPath, branch string) (ReviewDecision, error) {
	output, err := s.executor.Output(ctx, repoPath, "gh", "pr", "view", branch, "--json", "reviews")
	if err != nil {
		return ReviewNone, fmt.Errorf("gh pr view failed: %w", err)
	}

	var result struct {
		Reviews []struct {
			Author struct {
				Login string `json:"login"`
			} `json:"author"`
			State string `json:"state"`
		} `json:"reviews"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return ReviewNone, fmt.Errorf("failed to parse reviews: %w", err)
	}

	// For each unique author, find their latest actionable review.
	// Reviews are ordered by submission time (earliest first).
	latestByAuthor := make(map[string]string)
	for _, review := range result.Reviews {
		switch review.State {
		case "APPROVED", "CHANGES_REQUESTED":
			latestByAuthor[review.Author.Login] = review.State
		}
		// Ignore COMMENTED, DISMISSED, PENDING — not actionable decisions
	}

	// Derive decision: any CHANGES_REQUESTED wins, else any APPROVED wins
	hasApproved := false
	for _, state := range latestByAuthor {
		if state == "CHANGES_REQUESTED" {
			return ReviewChangesRequested, nil
		}
		if state == "APPROVED" {
			hasApproved = true
		}
	}
	if hasApproved {
		return ReviewApproved, nil
	}

	return ReviewNone, nil
}

// MergePR merges a PR for the given branch using the specified merge method.
// Valid methods: "rebase" (default), "squash", "merge". If method is empty, defaults to "rebase".
// The deleteBranch parameter controls whether to delete the branch after merging.
// For autonomous sessions, pass false since the branch is deleted during session cleanup.
func (s *GitService) MergePR(ctx context.Context, repoPath, branch string, deleteBranch bool, method string) error {
	var flag string
	switch method {
	case "squash":
		flag = "--squash"
	case "merge":
		flag = "--merge"
	default:
		flag = "--rebase"
	}
	args := []string{"pr", "merge", branch, flag}
	if deleteBranch {
		args = append(args, "--delete-branch")
	}
	_, stderr, err := s.executor.Run(ctx, repoPath, "gh", args...)
	if err != nil {
		stderrStr := strings.TrimSpace(string(stderr))
		if stderrStr != "" {
			return fmt.Errorf("gh pr merge failed: %s", stderrStr)
		}
		return fmt.Errorf("gh pr merge failed: %w", err)
	}
	return nil
}

// GeneratePRTitleAndBodyWithIssueRef uses Claude to generate a PR title and body from the branch changes.
// If issueRef is provided, it will add appropriate link text based on the source:
//   - GitHub: adds "Fixes #{number}" to auto-close the issue
//   - Asana: no auto-close support (Asana doesn't use commit message keywords)
//
// baseBranch is the branch this PR will be compared against (typically the session's BaseBranch or main).
func (s *GitService) GeneratePRTitleAndBodyWithIssueRef(ctx context.Context, repoPath, branch, baseBranch string, issueRef *config.IssueRef) (title, body string, err error) {
	log := logger.WithComponent("git")
	log.Info("generating PR title and body with Claude", "branch", branch, "baseBranch", baseBranch, "issueRef", issueRef)

	// If baseBranch is empty, fall back to default branch
	if baseBranch == "" {
		baseBranch = s.GetDefaultBranch(ctx, repoPath)
		log.Debug("baseBranch empty, using default", "defaultBranch", baseBranch)
	}

	// Use origin/<baseBranch> for git comparisons so we compare against the
	// remote state, not a potentially stale local branch. The daemon creates
	// session branches from origin/<default>, so comparing against the local
	// branch would include unrelated commits if local main has fallen behind.
	// We fetch first to ensure origin/<baseBranch> is current.
	comparisonRef := baseBranch
	_, fetchErr := s.executor.CombinedOutput(ctx, repoPath, "git", "fetch", "origin", baseBranch)
	if fetchErr == nil {
		candidateRef := fmt.Sprintf("origin/%s", baseBranch)
		_, _, verifyErr := s.executor.Run(ctx, repoPath, "git", "rev-parse", "--verify", candidateRef)
		if verifyErr == nil {
			comparisonRef = candidateRef
			log.Debug("using remote ref for PR comparison", "ref", comparisonRef)
		}
	}

	// Get the commit log for this branch
	commitLog, err := s.executor.Output(ctx, repoPath, "git", "log", fmt.Sprintf("%s..%s", comparisonRef, branch), "--oneline")
	if err != nil {
		log.Error("failed to get commit log", "error", err, "branch", branch)
		return "", "", fmt.Errorf("failed to get commit log: %w", err)
	}

	// Get the diff from base branch (use --no-ext-diff to ensure output goes to stdout)
	diffOutput, err := s.executor.Output(ctx, repoPath, "git", "diff", "--no-ext-diff", fmt.Sprintf("%s...%s", comparisonRef, branch))
	if err != nil {
		log.Error("failed to get diff", "error", err, "branch", branch)
		return "", "", fmt.Errorf("failed to get diff: %w", err)
	}

	fullDiff := string(diffOutput)

	// Truncate diff if too large
	maxDiffSize := MaxDiffSize
	if len(fullDiff) > maxDiffSize {
		fullDiff = fullDiff[:maxDiffSize] + "\n... (diff truncated)"
	}

	// Build the prompt for Claude
	prompt := fmt.Sprintf(`Generate a GitHub pull request title and body for the following changes.

Output format (use exactly this format with the markers):
---TITLE---
Your PR title here in conventional commit format
---BODY---
## Summary
Brief description of what this PR does

## Changes
- Bullet points of key changes

## Test plan
- How to test these changes

Rules:
1. Title MUST follow conventional commit format: <type>[optional scope]: <description>
   - type: feat, fix, docs, style, refactor, perf, test, build, ci, chore, revert
   - scope: optional component/module name in parentheses
   - description: concise summary in imperative mood, lowercase, no period at end
   - Example: "feat(auth): add OAuth2 login support"
   - Example: "fix: prevent race condition in request handling"
   - Keep total title length under 72 characters
2. Body should explain the purpose and changes clearly
3. Include a test plan section
4. Do NOT include any preamble - start directly with ---TITLE---

Commits in this branch:
%s

Diff:
%s`, string(commitLog), fullDiff)

	// Call Claude CLI
	output, err := s.executor.Output(ctx, repoPath, "claude", "--print", "-p", prompt)
	if err != nil {
		log.Error("Claude PR generation failed", "error", err)
		return "", "", fmt.Errorf("failed to generate PR with Claude: %w", err)
	}

	result := strings.TrimSpace(string(output))

	// Parse the output
	titleMarker := "---TITLE---"
	bodyMarker := "---BODY---"

	titleStart := strings.Index(result, titleMarker)
	bodyStart := strings.Index(result, bodyMarker)

	if titleStart == -1 || bodyStart == -1 {
		// Fallback: use first line as title, rest as body
		lines := strings.SplitN(result, "\n", 2)
		title = strings.TrimSpace(lines[0])
		if len(lines) > 1 {
			body = strings.TrimSpace(lines[1])
		}
	} else {
		title = strings.TrimSpace(result[titleStart+len(titleMarker) : bodyStart])
		body = strings.TrimSpace(result[bodyStart+len(bodyMarker):])
	}

	if title == "" {
		return "", "", fmt.Errorf("Claude returned empty PR title")
	}

	// Add issue reference to the body based on source
	if issueRef != nil {
		linkText := GetPRLinkText(issueRef)
		if linkText != "" {
			body = body + linkText
			log.Info("added issue reference", "source", issueRef.Source, "id", issueRef.ID)
		}
	}

	log.Info("generated PR title", "title", title)
	return title, body, nil
}

// GetPRLinkText returns the appropriate text to add to a PR body based on the issue source.
// For GitHub issues: returns "\n\nFixes #123"
// For Asana tasks: returns "" (no auto-close support)
// For unknown sources: returns ""
func GetPRLinkText(issueRef *config.IssueRef) string {
	if issueRef == nil {
		return ""
	}

	switch issueRef.Source {
	case "github":
		return fmt.Sprintf("\n\nFixes #%s", issueRef.ID)
	case "asana":
		// Asana doesn't support auto-closing tasks via commit message keywords.
		// Users can manually link PRs in Asana or use the Asana GitHub integration.
		return ""
	default:
		return ""
	}
}
