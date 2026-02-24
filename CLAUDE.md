# CLAUDE.md

Guidance for Claude Code when working with this repository.

## Testing Requirements

**ALWAYS write tests for new code.** This is a non-negotiable requirement.

- Every new function or method must have corresponding unit tests
- Every bug fix must include a regression test
- Run `go test -p=1 -count=1 ./...` before considering any task complete
- Use table-driven tests where appropriate
- Test edge cases and error conditions, not just happy paths
- If `go test` fails with a segfault or signal, retry — it may be a transient container issue

## Build and Test

```bash
go build -o erg .
go test -p=1 -count=1 ./...
```

## Debug Logs

```bash
tail -f ~/.erg/logs/erg.log          # Main app logs
tail -f ~/.erg/logs/mcp-*.log       # MCP permission logs (per-session)
tail -f ~/.erg/logs/stream-*.log    # Raw Claude stream messages (per-session)
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
main.go              Entry point, calls cmd.Execute()
cmd/                  CLI commands (Cobra): agent, clean, mcp_server, workflow
internal/
  paths/              Path resolution, XDG support (leaf)
  exec/               Command execution + MockExecutor (leaf)
  cli/                CLI prerequisite validation (leaf)
  logger/             Structured slog logging
  config/             Session, IssueRef, MCPServer, Config
  git/                GitService, PR/branch ops
  issues/             Provider interface: GitHub, Asana, Linear
  mcp/                MCP protocol, socket server/client
  claude/             RunnerInterface, process mgmt, tool sets
  session/            SessionService
  manager/            SessionManager
  agentconfig/        Config interface (leaf, no internal deps)
  container/          Container lifecycle and Docker management
  daemonstate/        Daemon state persistence and file-based locking (leaf)
  worker/             SessionWorker — manages a single session's lifecycle
  workflow/           Workflow engine, config, validation, and visualization
  daemon/             Persistent orchestrator: polling, actions, events, recovery
```

Import hierarchy (no cycles):
```
cmd       → daemon, agentconfig, workflow
daemon    → worker, daemonstate, agentconfig, workflow, manager, session, config, ...
worker    → agentconfig, claude, manager, session, config, git, ...
workflow  → (leaf)
```

### Key Patterns

- **Functional options**: `daemon.Option` configures Daemon via `With*()` functions
- **Interface-based config**: `agentconfig.Config` interface decouples daemon from concrete config
- **Host interface**: `worker.Host` abstracts the daemon so SessionWorker doesn't depend on it directly
- **Worker goroutines**: Each session gets a `SessionWorker` with its own goroutine and select loop
- **State machine**: Daemon uses `workflow.Engine` for configurable state machines per repo

---

## Implementation Guide

### Adding a New Daemon Action

1. Define the action string in `internal/workflow/actions.go`
2. Add handler in `internal/daemon/actions.go`
3. Wire it into the action dispatch in `daemon.go:buildActionRegistry()`
4. Add tests in `internal/daemon/actions_test.go`

### Adding a New CLI Flag

1. Add flag variable and `Flags().XxxVar()` call in `cmd/agent.go`'s `init()`
2. Map to `daemon.Option` in `runAgent()`
3. Add corresponding `WithXxx()` option function in `internal/daemon/daemon.go`
