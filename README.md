# erg

**Autonomous headless daemon for Claude Code** — does the work.

**Docs: [zhubert.com/erg](https://zhubert.com/erg/)** — setup, workflow configuration, CLI reference, and more.

## Install

```bash
brew install zhubert/tap/erg
```

Or [build from source](#build-from-source).

## Quick Start

```bash
erg start --repo owner/repo
```

Label a GitHub issue `queued` and erg picks it up — coding, PR, review feedback, CI, and merge are handled automatically. See the [docs](https://zhubert.com/erg/) for full configuration options including Asana and Linear as work sources.

## Run as a Service (macOS)

If you installed via Homebrew, run erg as a persistent background service that starts on login:

```bash
brew services start erg
```

See the [docs](https://zhubert.com/erg/) for daemon config and the full list of `brew services` commands.

## Build from Source

```bash
git clone https://github.com/zhubert/erg.git
cd erg
go build -o erg .
```

## License

MIT
