# CLAUDE.md

Guidance for Claude Code when working with this repository.

## Testing Requirements

**ALWAYS write tests for new code.** This is a non-negotiable requirement.

- Every new function or method must have corresponding unit tests
- Every bug fix must include a regression test
- Run `go test -p=1 -count=1 ./...` before considering any task complete
- Aim for high coverage (80%+) on new code
- Use table-driven tests where appropriate
- Test edge cases and error conditions, not just happy paths
- If `go test` fails with a segfault or signal, retry — it may be a transient container issue

## Build and Run

```bash
go build -o erg .       # Build
go test -p=1 -count=1 ./...     # Test (use -p=1 in containers to avoid Go toolchain segfaults)

./erg --version         # Show version
./erg --debug           # Enable debug logging (on by default)
./erg -q                # Quiet mode (info level only)

./erg --repo owner/repo         # Fork/detach daemon (prints PID, exits)
./erg -f --repo owner/repo     # Foreground with live status display
./erg --repo owner/repo --once  # Process one tick and exit (implies -f)
./erg status                    # One-shot daemon status summary
./erg clean                     # Clear daemon state and lock files
./erg clean -y                  # Clear without confirmation prompt
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
  logger/             Structured slog logging (depends on: paths)
  config/             Session, IssueRef, MCPServer, Config (depends on: paths)
  git/                GitService, PR/branch ops (depends on: exec, logger)
  issues/             Provider interface: GitHub, Asana, Linear (depends on: git)
  mcp/                MCP protocol, socket server/client (depends on: logger)
  claude/             RunnerInterface, process mgmt, tool sets (depends on: mcp)
  session/            SessionService (depends on: exec, config, logger, paths)
  manager/            SessionManager (depends on: claude, config, git, logger, mcp)
  agentconfig/        Config interface (leaf package, no internal deps)
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

### Key Dependencies

- `github.com/spf13/cobra` — CLI framework
- `gopkg.in/yaml.v3` — YAML parsing for workflow configs

### Key Patterns

- **Functional options**: `daemon.Option` configures Daemon via `With*()` functions
- **Interface-based config**: `agentconfig.Config` interface decouples daemon from concrete config
- **Host interface**: `worker.Host` abstracts the daemon so SessionWorker doesn't depend on it directly
- **Worker goroutines**: Each session gets a `SessionWorker` with its own goroutine and select loop
- **State machine**: Daemon uses `workflow.Engine` for configurable state machines per repo
- **Graceful shutdown**: Context cancellation with SIGINT/SIGTERM handling; second signal force-exits
- **Daemon lock**: File-based lock with stale PID detection prevents multiple instances per repo

### Container Mode

All sessions are containerized — the container IS the sandbox. Claude runs with `--dangerously-skip-permissions` inside Docker. Interactive prompts route through the MCP server over TCP.

### Workflow Configuration

Per-repo workflow config via `.erg/workflow.yaml`. Override chain: CLI flag > workflow yaml > config.json > default.

**State types**: `task` (execute action), `wait` (poll event with timeout), `choice` (conditional branch), `pass` (inject data), `succeed`/`fail` (terminal).

**Error recovery**: Task states support `retry` (max_attempts, interval, backoff) and `catch` (route errors to recovery states).

**Hooks**: `before` hooks run before step execution (failure blocks the step). `after` hooks run after completion (fire-and-forget).

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

---

## Releasing

```bash
./scripts/release.sh patch            # v0.0.3 -> v0.0.4
./scripts/release.sh minor            # v0.0.3 -> v0.1.0
./scripts/release.sh major            # v0.0.3 -> v1.0.0
./scripts/release.sh patch --dry-run  # Dry run
```
