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

./plural-agent agent --repo owner/repo         # Run headless daemon
./plural-agent agent --repo owner/repo --once  # Process one tick and exit
./plural-agent agent clean                     # Clear daemon state and lock files
./plural-agent agent clean -y                  # Clear without confirmation prompt
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
  agent/                   Core agent logic
    agent.go               Agent struct — headless autonomous agent that polls issues
    config.go              AgentConfig interface — decouples agent from concrete config
    daemon.go              Daemon struct — persistent orchestrator with workflow engine
    daemon_actions.go      Workflow action handlers (coding, PR creation, merge)
    daemon_events.go       Workflow event handlers (PR reviewed, CI complete)
    daemon_polling.go      Issue polling across providers (GitHub, Asana, Linear)
    daemon_recovery.go     State recovery on daemon restart
    daemon_state.go        DaemonState persistence (daemon-state.json)
    helpers.go             Shared utility functions
    issue_poller.go        Issue polling abstraction
    worker.go              SessionWorker — manages a single session's lifecycle
    auto_merge.go          Auto-merge logic (poll for review + CI, then merge)
```

### Key Dependencies

- `github.com/zhubert/plural-core` — shared library with config, git, session, claude, MCP, workflow, and issue provider packages
- `github.com/spf13/cobra` — CLI framework

### Key Patterns

- **Functional options**: `DaemonOption` and `Option` configure Daemon and Agent via `With*()` functions
- **Interface-based config**: `AgentConfig` interface decouples agent from `config.Config` for testability
- **Worker goroutines**: Each session gets a `SessionWorker` with its own goroutine and select loop
- **State machine**: Daemon uses `workflow.Engine` for configurable state machines per repo
- **Graceful shutdown**: Context cancellation with SIGINT/SIGTERM handling; second signal force-exits
- **Daemon lock**: File-based lock prevents multiple daemon instances for the same repo

### Container Mode

All agent sessions are containerized — the container IS the sandbox. Claude runs with `--dangerously-skip-permissions` inside Docker. Interactive prompts (like MCP tool calls) route through the MCP server over TCP.

Auth: `ANTHROPIC_API_KEY`, macOS keychain `anthropic_api_key`, `CLAUDE_CODE_OAUTH_TOKEN`, or `~/.claude/.credentials.json` (from `claude login`).

### Workflow Configuration

Per-repo workflow config via `.plural/workflow.yaml`. The daemon loads workflow configs at startup, keyed by repo path. Override chain: CLI flag > workflow yaml > config.json > default.

Key files: `daemon.go` (main loop), `daemon_actions.go` (action execution), `daemon_events.go` (event handling), `daemon_polling.go` (issue fetching).

---

## Implementation Guide

### Adding a New Daemon Action

1. Define the action string in `plural-core/workflow` (e.g., `ai.code`, `github.create_pr`)
2. Add handler in `daemon_actions.go`
3. Wire it into the workflow engine's action dispatch
4. Add tests in `daemon_actions_test.go`

### Adding a New CLI Flag

1. Add flag variable and `Flags().XxxVar()` call in `cmd/agent.go`'s `init()`
2. Map to `DaemonOption` in `runAgent()`
3. Add corresponding `WithDaemonXxx()` option function in `daemon.go`

---

## License

MIT License
