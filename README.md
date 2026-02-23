# erg

**Autonomous headless daemon for Claude Code** — label an issue, ship a PR.

Erg polls for work from GitHub, Asana, or Linear, spins up containerized Claude Code sessions, and manages the full lifecycle: coding, PR creation, review feedback, CI, and merge. The workflow is a configurable state machine — customize the pipeline per-repo or use sensible defaults.

## Install

```bash
brew tap zhubert/tap
brew install erg
```

Or [build from source](#build-from-source).

## Requirements

- [Claude Code CLI](https://claude.ai/code) installed and authenticated
- Git
- GitHub CLI (`gh`)
- A container runtime: [Docker Desktop](https://docs.docker.com/get-docker/) or [Colima](https://github.com/abiosoft/colima)

## Quick Start

```bash
erg --repo owner/repo
```

Label a GitHub issue `queued` and erg picks it up automatically. For Asana or Linear, configure the [workflow source](https://zhubert.com/erg/).

## How It Works

1. Agent polls for issues from your configured provider
2. Creates a containerized Claude Code session on a new branch
3. Claude works the issue autonomously
4. A PR is created when coding is complete
5. Agent addresses review feedback, waits for CI, and merges

For complex issues, Claude can delegate subtasks to child sessions via MCP tools. The supervisor waits for all children before creating a PR.

## Documentation

Full documentation — workflow configuration, state types, error handling, hooks, CLI reference — is at **[zhubert.com/erg](https://zhubert.com/erg/)**.

## Build from Source

```bash
git clone https://github.com/zhubert/erg.git
cd erg
go build -o erg .
```

## License

MIT
