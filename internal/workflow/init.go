package workflow

import (
	"fmt"
	"os"
	"path/filepath"
)

// Template is the default workflow.yaml content with the step-functions format.
// It uses the CI-gate pattern: CI must pass before any human reviews the code.
// This avoids wasting reviewer time on failing builds. Claude automatically
// attempts to fix CI failures and merge conflicts before requesting review.
const Template = `# Erg Workflow Configuration
# See: https://github.com/zhubert/erg for full documentation
#
# CI-gate workflow — CI must pass before any human reviews the code.
# Claude automatically fixes CI failures and merge conflicts before
# requesting review, so reviewers only see green builds.
#
# Flow:
#   coding → open_pr → await_ci → check_ci
#     → conflicting    → rebase (or ai.resolve_conflicts) → await_ci
#     → ci_passed      → request_reviewer → await_review → merge → done
#     → ci_failed      → ai.fix_ci → push → await_ci (bounded loop)
#
# State types:
#   task    - Execute an action (sync or async)
#   wait    - Poll for an external event, with optional timeout enforcement
#   choice  - Conditional branch based on step data
#   pass    - Inject data into step data and transition immediately
#   succeed - Terminal success
#   fail    - Terminal failure

workflow: ci-gate
start: coding

source:
  provider: github          # github, asana, or linear
  filter:
    label: queued            # GitHub: issue label to poll
    # project: ""            # Asana: project GID (required for asana provider)
    # team: ""               # Linear: team ID (required for linear provider)

states:
  coding:
    type: task
    action: ai.code
    params:
      max_turns: 50            # Max autonomous turns per session
      max_duration: 30m        # Max wall-clock time per session
      # containerized: true    # Run sessions in Docker containers
      # supervisor: true       # Use supervisor mode for coding
      # system_prompt: ""      # Custom system prompt (inline or file:path/to/prompt.md)
    # before:                  # Hooks to run before coding starts (failure blocks step)
    #   - run: "make deps"
    # after:                   # Hooks to run after coding completes (fire-and-forget)
    #   - run: "make lint"
    next: open_pr
    error: notify_failed

  open_pr:
    type: task
    action: github.create_pr
    params:
      link_issue: true         # Link PR to the source issue
      # template: ""           # PR body template (inline or file:path/to/template.md)
    next: await_ci              # CI runs before review
    error: notify_failed

  # --- CI gate: wait for CI, then route based on result ---

  await_ci:
    type: wait
    event: ci.complete
    timeout: 2h
    timeout_next: ci_timed_out
    params:
      on_failure: fix           # Report CI failure in step data (ci_failed=true)
    next: check_ci
    error: notify_failed

  check_ci:
    type: choice
    choices:
      - variable: conflicting
        equals: true
        next: rebase              # Merge conflicts → rebase onto main
      - variable: ci_passed
        equals: true
        next: request_reviewer    # CI green → assign reviewer
      - variable: ci_failed
        equals: true
        next: fix_ci              # CI red → let Claude fix it
    default: notify_failed

  # --- Merge conflict resolution ---

  rebase:
    type: task
    action: git.rebase
    params:
      max_rebase_rounds: 3
    next: await_ci
    error: resolve_conflicts      # If rebase fails (real conflicts), use Claude

  resolve_conflicts:
    type: task
    action: ai.resolve_conflicts
    params:
      max_conflict_rounds: 3
    next: push_conflict_fix
    error: notify_failed

  push_conflict_fix:
    type: task
    action: github.push
    next: await_ci
    error: notify_failed

  # --- CI fix loop (bounded by max_ci_fix_rounds) ---

  fix_ci:
    type: task
    action: ai.fix_ci
    params:
      max_ci_fix_rounds: 3       # Give up after 3 unsuccessful fix attempts
    next: push_ci_fix
    error: ci_unfixable

  push_ci_fix:
    type: task
    action: github.push
    next: await_ci                # Loop: await_ci → check_ci → fix_ci (cycle through choice)
    error: notify_failed

  ci_unfixable:
    type: task
    action: github.comment_pr
    params:
      body: "CI fix attempts exhausted after 3 rounds. Manual intervention required."
    next: notify_failed
    error: notify_failed

  ci_timed_out:
    type: task
    action: github.comment_pr
    params:
      body: "CI has been running for over 2h. Please check the CI pipeline."
    next: notify_failed
    error: notify_failed

  # --- Review (only after CI is green) ---

  request_reviewer:
    type: task
    action: github.request_review
    params:
      reviewer: your-github-username   # Replace with the reviewer's GitHub username
    next: await_review
    error: await_review                # If request fails, still wait for review

  await_review:
    type: wait
    event: pr.reviewed
    timeout: 48h
    timeout_next: review_overdue
    params:
      auto_address: true           # Automatically address PR review comments
      max_feedback_rounds: 3       # Max review/feedback cycles
      # system_prompt: ""          # Custom system prompt for review phase
    next: merge
    error: notify_failed

  review_overdue:
    type: task
    action: github.comment_issue
    params:
      body: "This PR has been awaiting review for 48h. Could a maintainer take a look?"
    next: notify_failed
    error: notify_failed

  # --- Merge and terminal states ---

  merge:
    type: task
    action: github.merge
    params:
      method: rebase             # Merge method: rebase, squash, or merge
      cleanup: true              # Delete branch after merge
    next: done

  done:
    type: succeed

  notify_failed:
    type: task
    action: github.comment_issue
    params:
      body: "Unable to complete automatically. Manual intervention required."
    next: failed
    error: failed

  failed:
    type: fail

# Agent settings (optional — override defaults for this repo)
# settings:
#   container_image: ghcr.io/zhubert/erg-claude
#   branch_prefix: erg/
#   max_concurrent: 3
#   cleanup_merged: true
`

// WriteTemplate writes the default workflow.yaml template to repoPath/.erg/workflow.yaml.
// Returns an error if the file already exists.
func WriteTemplate(repoPath string) (string, error) {
	dir := filepath.Join(repoPath, workflowDir)
	fp := filepath.Join(dir, workflowFileName)

	if _, err := os.Stat(fp); err == nil {
		return fp, fmt.Errorf("%s already exists", fp)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fp, fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	if err := os.WriteFile(fp, []byte(Template), 0o644); err != nil {
		return fp, fmt.Errorf("failed to write %s: %w", fp, err)
	}

	return fp, nil
}
