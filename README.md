# Plural Agent

**Autonomous headless daemon for Claude Code.** Label an issue, and plural-agent codes it, opens a PR, addresses review comments, and merges — no human in the loop.

Plural Agent is the headless agent extracted from [Plural](https://github.com/zhubert/plural). It runs as a persistent daemon that polls for issues, spins up containerized Claude Code sessions, and manages the full lifecycle from coding through merge.

## Install

```bash
brew tap zhubert/tap
brew install plural-agent
```

Or [build from source](#build-from-source).

## Requirements

- [Claude Code CLI](https://claude.ai/code) installed and authenticated
- Git
- GitHub CLI (`gh`)
- Docker

## Quick Start

```bash
plural-agent agent --repo owner/repo
```

Label a GitHub issue `queued` and plural-agent picks it up automatically.

---

## How It Works

1. Agent finds issues labeled `queued` on the target repo
2. Creates a containerized Claude Code session on a new branch
3. Claude works the issue autonomously
4. A PR is created when coding is complete
5. Agent polls for review approval and CI, then merges

For complex issues, Claude can delegate subtasks to child sessions via MCP tools (`create_child_session`, `list_child_sessions`, `merge_child_to_parent`). The supervisor waits for all children before creating a PR.

## Agent Flags

```bash
plural-agent agent --repo owner/repo              # Required: repo to poll
plural-agent agent --repo owner/repo --once       # Single tick, then exit
plural-agent agent --repo owner/repo --auto-merge # Auto-merge after review + CI (default)
plural-agent agent --repo owner/repo --no-auto-merge
plural-agent agent --repo owner/repo --max-concurrent 5
plural-agent agent --repo owner/repo --max-turns 80
plural-agent agent --repo owner/repo --max-duration 45
plural-agent agent --repo owner/repo --merge-method squash
plural-agent agent --repo owner/repo --auto-address-pr-comments
```

If `--repo` is not specified and the current directory is inside a git repository, that repository is used as the default.

## Workflow Configuration

Create `.plural/workflow.yaml` in your repo to customize the agent's behavior per-repository. The workflow is defined as a **state machine** — a directed graph of states connected by `next` (success) and `error` (failure) edges. If no file exists, the agent uses sensible defaults.

```yaml
workflow: issue-to-merge
start: coding

source:
  provider: github          # github | asana | linear
  filter:
    label: queued            # github: issue label to poll

states:
  coding:
    type: task
    action: ai.code
    params:
      max_turns: 50
      max_duration: 30m
      containerized: true
      supervisor: true
    next: open_pr
    error: failed

  open_pr:
    type: task
    action: github.create_pr
    params:
      link_issue: true
    next: await_review
    error: failed

  await_review:
    type: wait
    event: pr.reviewed
    params:
      auto_address: true
      max_feedback_rounds: 3
    next: await_ci
    error: failed

  await_ci:
    type: wait
    event: ci.complete
    timeout: 2h
    params:
      on_failure: retry      # retry | abandon | notify
    next: merge
    error: failed

  merge:
    type: task
    action: github.merge
    params:
      method: rebase         # rebase | squash | merge
      cleanup: true
    next: done

  done:
    type: succeed

  failed:
    type: fail
```

### State types

| Type | Purpose | Required fields |
|------|---------|-----------------|
| `task` | Executes an action | `action`, `next` |
| `wait` | Polls for an external event | `event`, `next` |
| `succeed` | Terminal success state | -- |
| `fail` | Terminal failure state | -- |

### Actions and events

| Actions | Events |
|---------|--------|
| `ai.code` -- run a Claude coding session | `pr.reviewed` -- PR has been reviewed |
| `github.create_pr` -- open a pull request | `ci.complete` -- CI checks have finished |
| `github.push` -- push to remote | |
| `github.merge` -- merge the PR | |
| `github.comment_issue` -- post a comment on the GitHub issue | |

### Customizing the graph

You can add, remove, or reorder states to match your workflow. States from the default config that aren't overridden in your file are preserved -- you only need to define the states you want to change or add.

**Override precedence**: CLI flag > `.plural/workflow.yaml` > `~/.plural/config.json` > defaults.

**System prompts** can be inline strings or `file:./path` references (relative to repo root).

**Hooks** run on the host after each workflow step via the `after` field. Hook failures are logged but don't block the workflow.

**Provider support**: The agent can poll GitHub issues (by label), Asana tasks (by project), or Linear issues (by team).

## Configuration

These can also be set via `~/.plural/config.json`:

| Setting | JSON Key | Default |
|---------|----------|---------|
| Max concurrent sessions | `issue_max_concurrent` | `3` |
| Max turns per session | `auto_max_turns` | `50` |
| Max duration (minutes) | `auto_max_duration_min` | `30` |
| Merge method | `auto_merge_method` | `rebase` |
| Auto-address PR comments | `auto_address_pr_comments` | `false` |

Graceful shutdown: `SIGINT`/`SIGTERM` once to finish current work, twice to force exit.

## CLI Reference

```bash
plural-agent agent --repo ...          # Run headless agent daemon
plural-agent agent --repo ... --once   # Process one tick and exit
plural-agent agent clean               # Remove daemon state and lock files
plural-agent agent clean -y            # Clean without confirmation
plural-agent --version                 # Show version
plural-agent --debug                   # Debug logging (default: on)
plural-agent -q / --quiet              # Info-level logging only
```

## Data Storage

All data lives in `~/.plural/` by default. Supports XDG Base Directory Specification.

| Purpose | Default | XDG |
|---------|---------|-----|
| Config | `~/.plural/config.json` | `$XDG_CONFIG_HOME/plural/` |
| Daemon state | `~/.plural/daemon-state.json` | `$XDG_DATA_HOME/plural/` |
| Logs | `~/.plural/logs/` | `$XDG_STATE_HOME/plural/` |

## Build from Source

```bash
git clone https://github.com/zhubert/plural-agent.git
cd plural-agent
make build
```

## License

MIT
