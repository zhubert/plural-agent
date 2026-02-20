package workflow

import (
	"fmt"
	"os"
	"path/filepath"
)

// Template is the default workflow.yaml content with the step-functions format.
const Template = `# Plural Agent Workflow Configuration
# See: https://github.com/zhubert/plural for full documentation
#
# This file defines a state machine that controls how the plural agent
# daemon processes issues. States are nodes connected by next (success)
# and error (failure) edges.
#
# State types:
#   task    - Execute an action (sync or async)
#   wait    - Poll for an external event, with optional timeout enforcement
#   choice  - Conditional branch based on step data
#   pass    - Inject data into step data and transition immediately
#   succeed - Terminal success
#   fail    - Terminal failure

workflow: issue-to-merge
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
    # retry:                   # Retry on failure with exponential backoff
    #   - max_attempts: 3
    #     interval: 30s
    #     backoff_rate: 2.0
    #     errors: ["*"]        # Error patterns to match ("*" = all)
    # catch:                   # Route specific errors to recovery states
    #   - errors: ["*"]
    #     next: failed
    next: open_pr
    error: failed

  open_pr:
    type: task
    action: github.create_pr
    params:
      link_issue: true         # Link PR to the source issue
      # template: ""           # PR body template (inline or file:path/to/template.md)
    next: await_review
    error: failed

  await_review:
    type: wait
    event: pr.reviewed
    params:
      auto_address: true       # Automatically address PR review comments
      max_feedback_rounds: 3   # Max review/feedback cycles
      # system_prompt: ""      # Custom system prompt for review phase
    next: await_ci
    error: failed

  await_ci:
    type: wait
    event: ci.complete
    timeout: 2h                # Enforced: transitions to error after 2h
    # timeout_next: ci_timed_out  # Optional: dedicated timeout transition (instead of error)
    params:
      on_failure: retry        # What to do on CI failure: retry, abandon, or notify
    next: merge
    error: failed

  merge:
    type: task
    action: github.merge
    params:
      method: rebase           # Merge method: rebase, squash, or merge
      cleanup: true            # Delete branch after merge
    # after:
    #   - run: "echo merged"
    next: done

  done:
    type: succeed

  failed:
    type: fail

  # --- Example: choice state (conditional branching) ---
  # check_result:
  #   type: choice
  #   choices:
  #     - variable: ci_status    # Step data key to evaluate
  #       equals: passing        # Condition: equals, not_equals, or is_present
  #       next: merge
  #     - variable: ci_status
  #       equals: failing
  #       next: fix_ci
  #   default: await_ci          # Fallback if no rule matches

  # --- Example: pass state (data injection) ---
  # set_defaults:
  #   type: pass
  #   data:
  #     merge_method: squash
  #     cleanup_branch: true
  #   next: coding

# Agent settings (optional — override defaults for this repo)
# settings:
#   container_image: ghcr.io/zhubert/plural-claude
#   branch_prefix: plural/
#   max_concurrent: 3
#   cleanup_merged: true
`

// WriteTemplate writes the default workflow.yaml template to repoPath/.plural/workflow.yaml.
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
