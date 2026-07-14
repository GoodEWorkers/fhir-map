# Contributing to fhir-map

Thanks for contributing! This guide gets your local environment set up so that
**the checks you run locally are the same ones CI runs** — a commit that passes
locally should pass CI, so we don't burn CI minutes (or your time) on failures a
short local wait would have caught.

For architecture and deeper internals, see [`docs/DEVELOPER_GUIDE.md`](./docs/DEVELOPER_GUIDE.md). For integrating fhir-map into a data pipeline, see [`docs/INTEGRATION_GUIDE.md`](./docs/INTEGRATION_GUIDE.md).

## Prerequisites

| Tool | Version | Notes |
|------|---------|-------|
| Go | 1.25.x | Matches `go.mod` (`go 1.25.10`). |
| Docker + Compose v2 | recent | Needed for integration tests (testcontainers) and `make dev`. |
| Git | any | — |

The remaining tools (lefthook, golangci-lint, gitleaks, goimports) are installed
for you by `make setup` — you do **not** install them by hand.

## One-time setup

```bash
make setup
```

This single command:

1. **Installs pinned dev tools** into `$(go env GOPATH)/bin` at the exact
   versions CI uses (`scripts/install-tools.sh`):
   - `lefthook` — git hook runner
   - `golangci-lint` **v2.12.2** — same version as the CI Lint job
   - `gitleaks` — staged-file secret scanner
   - `goimports` — import formatting
   - `gosec` **v2.22.5** / `govulncheck` **v1.3.0** — same versions as the CI SAST job
2. **Installs the git hooks** (`lefthook install`).

Make sure `$(go env GOPATH)/bin` is on your `PATH`:

```bash
export PATH="$PATH:$(go env GOPATH)/bin"   # add to your ~/.zshrc or ~/.bashrc
```

## What runs, and when

The hooks mirror `.github/workflows/ci.yml` so local green == CI green.

### On every commit (`pre-commit`)

Runs fast → slow; the first failure stops the commit:

| Check | Command | Mirrors CI job |
|-------|---------|----------------|
| Format | `gofmt` / `goimports` | Lint (formatters) |
| Secrets | `gitleaks protect --staged` | Secret Scan (TruffleHog) |
| Lint | `golangci-lint run ./...` (**full**, not `--fast-only`) | Lint |
| Build | `go build -ldflags="-s -w" -o ./bin/server ./cmd/server` | Build |
| Security | `gosec -severity high ./...` | SAST |
| Vulnerabilities | `govulncheck ./...` | SAST |
| Unit tests | `go test -race ./...` | Tests + Coverage (unit portion) |

> **Why the full lint matters:** the previous hook used `golangci-lint run
> --fast-only`, which skips `govet`/`shadow` (those need type info). CI runs the
> full lint, so `--fast-only` could pass locally while CI failed. We now run the
> full lint locally to close that gap.

### On every push (`pre-push`)

| Check | Command | Notes |
|-------|---------|-------|
| Integration tests | `go test -tags integration -race ./...` | Needs Docker (testcontainers spin up Postgres). **Skipped with a warning if Docker isn't running** — CI remains authoritative for it. |

## Everyday commands

```bash
make verify          # run the full pre-commit gate on demand (great before a PR)
make test-unit       # fast unit tests (-short -race)
make test-integration# integration tests (-tags integration, needs Docker)
make lint            # golangci-lint run ./...
make build           # build the server binary
make dev             # postgres up + migrations + run server
```

## Claude Code users

This repo ships a shared Claude Code config in [`.claude/settings.json`](./.claude/settings.json)
(committed, team-wide). It registers a `PreToolUse` guard
([`.claude/hooks/git-commit-guard.sh`](./.claude/hooks/git-commit-guard.sh)) that:

- **blocks `git commit --no-verify`** from a Claude session (don't let the agent
  skip the gate), and
- runs the pre-commit gate itself if the lefthook git hook isn't installed.

Your personal Claude settings stay in `.claude/settings.local.json` (gitignored).

## Bypassing the gate

In a genuine emergency you can bypass the git hooks with `git commit --no-verify`.
Prefer fixing the issue — **CI re-runs all of these checks and is authoritative**,
so a bypassed commit will simply fail there instead.
