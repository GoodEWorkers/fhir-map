#!/usr/bin/env bash
#
# Claude Code PreToolUse guard for `git commit`.
#
# Purpose: stop a Claude session from creating a commit that would fail CI for
# something the local pre-commit gate (lint/build/security/tests) catches.
#
#   1. Blocks `git commit --no-verify` (-n): the local gate must not be skipped
#      from inside a Claude session. Fix the issue instead.
#   2. On a normal commit, if lefthook's git hook IS installed, defer to it so
#      the checks don't run twice.
#   3. If lefthook's git hook is NOT installed, run the checks here so the gate
#      still applies (and tell the dev to run `make setup`).
#
# Exit codes: 0 = allow the tool call; 2 = block it (stderr is shown to Claude).

set -uo pipefail

input="$(cat)"
cmd="$(printf '%s' "$input" \
  | python3 -c 'import sys,json; print(json.load(sys.stdin).get("tool_input",{}).get("command",""))' 2>/dev/null || true)"

# Only act on an ACTUAL `git commit` invocation — i.e. `git commit` at a command
# boundary (start, or after ; & | && ||). A mention inside a quoted string
# (echo/grep "... git commit ...") is preceded by a quote, not a boundary, so it
# is correctly ignored.
if ! printf '%s' "$cmd" | grep -Eq '(^|[;&|])[[:space:]]*git[[:space:]]+commit([[:space:]]|$)'; then
  exit 0
fi

repo_root="$(git rev-parse --show-toplevel 2>/dev/null || echo .)"

# 1. Never allow the local gate to be bypassed from within a Claude session.
#    Scan only the FLAG REGION (between `git commit` and the message), so a
#    commit MESSAGE that merely mentions --no-verify does not trigger a block.
flags="${cmd#*git commit}"
for d in ' -m' ' --message' ' -F' ' --file' ' -C' ' -c ' '<<'; do
  flags="${flags%%$d*}"
done
if printf '%s' "$flags" | grep -Eq -- '(^|[[:space:]])(--no-verify|-n)([[:space:]]|$)'; then
  echo "BLOCKED: 'git commit --no-verify' skips the pre-commit gate (lint, build, gosec, govulncheck, tests)." >&2
  echo "Fix the underlying issue so the commit passes the gate. If a bypass is genuinely required," >&2
  echo "ask the user to run the commit themselves rather than bypassing it from this session." >&2
  exit 2
fi

# 2. If lefthook's git hook is installed, defer to it (avoids running checks twice).
if grep -q lefthook "$repo_root/.git/hooks/pre-commit" 2>/dev/null; then
  exit 0
fi

# 3. lefthook git hook missing — run the gate now so it still applies.
echo "lefthook git hook not installed — running the pre-commit gate via the Claude guard." >&2
echo "Tip: run 'make setup' once to install the git hooks and avoid this fallback." >&2
if command -v lefthook >/dev/null 2>&1; then
  if ! ( cd "$repo_root" && lefthook run pre-commit ) >&2; then
    echo "Pre-commit gate FAILED — fix the reported issues before committing." >&2
    exit 2
  fi
else
  echo "WARNING: lefthook is not installed; cannot run the local gate. Run 'make setup'. (CI will still gate.)" >&2
fi
exit 0
