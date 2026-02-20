# CLAUDE.md

Guidance for Claude Code when working with this repository.

## Testing Requirements

**ALWAYS write tests for new code.** This is a non-negotiable requirement.

- Every new function or method must have corresponding unit tests
- Every bug fix must include a regression test
- Run `go test ./...` before considering any task complete
- Aim for high coverage (80%+) on new code
- Use table-driven tests where appropriate
- Test edge cases and error conditions, not just happy paths

## Build and Run

```bash
go build -o plural-agent .       # Build
go test ./...                    # Test

./plural-agent --version         # Show version
./plural-agent --debug           # Enable debug logging (on by default)
./plural-agent -q                # Quiet mode (info level only)

./plural-agent --repo owner/repo         # Run headless daemon
./plural-agent --repo owner/repo --once  # Process one tick and exit
./plural-agent clean                     # Clear daemon state and lock files
./plural-agent clean -y                  # Clear without confirmation prompt
```

## Debug Logs

```bash
tail -f ~/.plural/logs/plural.log      # Main app logs
tail -f ~/.plural/logs/mcp-*.log       # MCP permission logs (per-session)
tail -f ~/.plural/logs/stream-*.log    # Raw Claude stream messages (per-session)
```

---

## Architecture

### Core Flow

1. Daemon polls for issues labeled `queued` on the target repo
2. For each issue, creates a containerized Claude Code session on a new branch
3. SessionWorker manages the Claude process lifecycle (turns, duration, streaming)
4. On completion, a PR is created and the daemon monitors for review + CI
5. After approval and CI pass, the PR is auto-merged

### Package Structure

```
main.go                    Entry point, calls cmd.Execute()
cmd/                       CLI commands (Cobra)
  agent.go                 Agent daemon command and flag handling
  clean.go                 Daemon state cleanup subcommand
  mcp_server.go            MCP server for permission prompts (internal)
  root.go                  Root command, version template
  util.go                  Shared CLI helpers
internal/
  agentconfig/             Shared config interface (leaf package)
    config.go              Config interface — decouples agent/daemon from concrete config
  daemonstate/             Daemon state persistence (leaf package)
    state.go               DaemonState, WorkItem, WorkItemState types and methods
    lock.go                DaemonLock, AcquireLock, ClearLocks file-based locking
  worker/                  Session worker and helpers
    host.go                Host interface — abstraction for Agent/Daemon
    worker.go              SessionWorker — manages a single session's lifecycle
    auto_merge.go          Auto-merge logic (poll for review + CI, then merge)
    helpers.go             TrimURL, FormatPRCommentsPrompt, FormatInitialMessage
  agent/                   Headless autonomous agent
    agent.go               Agent struct, options, New, Run, polling, Host impl
    issue_poller.go        Issue polling abstraction (GitHub label-based)
  daemon/                  Persistent orchestrator
    daemon.go              Daemon struct, options, New, Run loop, Host impl
    actions.go             Workflow action handlers (coding, PR creation, merge)
    events.go              Workflow event handlers (PR reviewed, CI complete)
    polling.go             Issue polling across providers (GitHub, Asana, Linear)
    recovery.go            State recovery on daemon restart
  workflow/                Workflow engine and configuration
```

Import graph (no cycles):
```
cmd/agent.go  → daemon, agentconfig
cmd/clean.go  → daemonstate
daemon/       → worker, daemonstate, agentconfig, workflow
agent/        → worker, agentconfig
worker/       → agentconfig
daemonstate/  → (leaf)
agentconfig/  → (leaf)
```

### Key Dependencies

- `github.com/zhubert/plural-core` — shared library with config, git, session, claude, MCP, workflow, and issue provider packages
- `github.com/spf13/cobra` — CLI framework

### Key Patterns

- **Functional options**: `daemon.Option` and `agent.Option` configure Daemon and Agent via `With*()` functions
- **Interface-based config**: `agentconfig.Config` interface decouples agent/daemon from `config.Config` for testability
- **Host interface**: `worker.Host` abstracts Agent/Daemon so SessionWorker doesn't depend on either
- **Worker goroutines**: Each session gets a `SessionWorker` with its own goroutine and select loop
- **State machine**: Daemon uses `workflow.Engine` for configurable state machines per repo
- **Graceful shutdown**: Context cancellation with SIGINT/SIGTERM handling; second signal force-exits
- **Daemon lock**: File-based lock prevents multiple daemon instances for the same repo

### Container Mode

All agent sessions are containerized — the container IS the sandbox. Claude runs with `--dangerously-skip-permissions` inside Docker. Interactive prompts (like MCP tool calls) route through the MCP server over TCP.

Auth: `ANTHROPIC_API_KEY`, macOS keychain `anthropic_api_key`, `CLAUDE_CODE_OAUTH_TOKEN`, or `~/.claude/.credentials.json` (from `claude login`).

### Workflow Configuration

Per-repo workflow config via `.plural/workflow.yaml`. The daemon loads workflow configs at startup, keyed by repo path. Override chain: CLI flag > workflow yaml > config.json > default.

**State types**: `task` (execute action), `wait` (poll event with timeout enforcement), `choice` (conditional branch on step data), `pass` (inject data), `succeed`/`fail` (terminal).

**Error recovery**: Task states support `retry` (max_attempts, interval, exponential backoff) and `catch` (route specific errors to recovery states). Retry count tracked in `_retry_count` step data key.

**Timeouts**: Wait states with `timeout` are enforced at runtime. `timeout_next` provides a dedicated timeout transition edge; otherwise falls back to `error` edge. `StepEnteredAt` on WorkItem tracks when a step was entered.

**Hooks**: `before` hooks run before step execution (failure blocks the step). `after` hooks run after completion (fire-and-forget). Both use `RunBeforeHooks()`/`RunHooks()` in `workflow/hooks.go`.

Key files: `daemon/daemon.go` (main loop), `daemon/actions.go` (action execution), `daemon/events.go` (event handling), `daemon/polling.go` (issue fetching).

---

## Implementation Guide

### Adding a New Daemon Action

1. Define the action string in `workflow/` (e.g., `ai.code`, `github.create_pr`)
2. Add handler in `internal/daemon/actions.go`
3. Wire it into the workflow engine's action dispatch in `daemon.go:buildActionRegistry()`
4. Add tests in `internal/daemon/actions_test.go`

### Adding a New CLI Flag

1. Add flag variable and `Flags().XxxVar()` call in `cmd/agent.go`'s `init()`
2. Map to `daemon.Option` in `runAgent()`
3. Add corresponding `WithXxx()` option function in `internal/daemon/daemon.go`

---

## Releasing

```bash
./scripts/release.sh patch            # v0.0.3 -> v0.0.4
./scripts/release.sh minor            # v0.0.3 -> v0.1.0
./scripts/release.sh major            # v0.0.3 -> v1.0.0
./scripts/release.sh patch --dry-run  # Dry run
```

## License

MIT License
