#!/usr/bin/env bash
#
# Install the pinned local dev tools so local checks match CI exactly.
# Versions here MUST track .github/workflows/ci.yml and lefthook.yml.
#
# Invoked by `make tools` / `make setup`.

set -euo pipefail

# Keep in sync with CI. golangci-lint MUST match the CI Lint job.
LEFTHOOK_VERSION="v2.1.8"
GOLANGCI_LINT_VERSION="v2.12.2"   # == .github/workflows/ci.yml Lint job
GITLEAKS_VERSION="v8.30.1"
GOSEC_VERSION="v2.22.5"           # == CI SAST job
GOVULNCHECK_VERSION="v1.3.0"      # == CI SAST job
# gosec/govulncheck are installed as pinned binaries (not `go run`).
# govulncheck must be >= v1.3.0: older builds (e.g. v1.1.4) link without an
# LC_UUID load command and abort at launch on recent macOS ("missing LC_UUID").

if ! command -v go >/dev/null 2>&1; then
  echo "✗ Go is not installed. Install Go 1.25.x first: https://go.dev/dl/" >&2
  exit 1
fi

GOBIN="$(go env GOPATH)/bin"
echo "==> Installing pinned dev tools into ${GOBIN}"

go install "github.com/evilmartians/lefthook/v2@${LEFTHOOK_VERSION}"
go install "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_LINT_VERSION}"
go install "github.com/zricethezav/gitleaks/v8@${GITLEAKS_VERSION}"
go install "golang.org/x/tools/cmd/goimports@latest"
go install "github.com/securego/gosec/v2/cmd/gosec@${GOSEC_VERSION}"
go install "golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}"

echo
echo "Installed:"
missing=0
for t in lefthook golangci-lint gitleaks goimports gosec govulncheck; do
  if command -v "$t" >/dev/null 2>&1; then
    printf '  ✓ %-14s %s\n' "$t" "$(command -v "$t")"
  else
    printf '  ✗ %-14s NOT on PATH\n' "$t"
    missing=1
  fi
done

if [ "$missing" -eq 1 ]; then
  echo
  echo "Some tools are not on your PATH. Add this to your shell profile:" >&2
  echo "  export PATH=\"\$PATH:${GOBIN}\"" >&2
  exit 1
fi
