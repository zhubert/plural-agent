# erg

**Autonomous headless daemon for Claude Code** — label an issue, ship a PR.

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

## Build from Source

```bash
git clone https://github.com/zhubert/erg.git
cd erg
go build -o erg .
```

## License

MIT
