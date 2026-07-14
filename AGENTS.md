<!-- gitnexus:start -->
# GitNexus — Code Intelligence

This project is indexed by GitNexus as **fhir-map** (6286 symbols, 16620 relationships, 255 execution flows). Use the GitNexus MCP tools to understand code, assess impact, and navigate safely.

> If any GitNexus tool warns the index is stale, run `npx gitnexus analyze` in terminal first.

## Always Do

- **MUST run impact analysis before editing any symbol.** Before modifying a function, class, or method, run `gitnexus_impact({target: "symbolName", direction: "upstream"})` and report the blast radius (direct callers, affected processes, risk level) to the user.
- **MUST run `gitnexus_detect_changes()` before committing** to verify your changes only affect expected symbols and execution flows.
- **MUST warn the user** if impact analysis returns HIGH or CRITICAL risk before proceeding with edits.
- When exploring unfamiliar code, use `gitnexus_query({query: "concept"})` to find execution flows instead of grepping. It returns process-grouped results ranked by relevance.
- When you need full context on a specific symbol — callers, callees, which execution flows it participates in — use `gitnexus_context({name: "symbolName"})`.

## Never Do

- NEVER edit a function, class, or method without first running `gitnexus_impact` on it.
- NEVER ignore HIGH or CRITICAL risk warnings from impact analysis.
- NEVER rename symbols with find-and-replace — use `gitnexus_rename` which understands the call graph.
- NEVER commit changes without running `gitnexus_detect_changes()` to check affected scope.

## Resources

| Resource | Use for |
|----------|---------|
| `gitnexus://repo/fhir-map/context` | Codebase overview, check index freshness |
| `gitnexus://repo/fhir-map/clusters` | All functional areas |
| `gitnexus://repo/fhir-map/processes` | All execution flows |
| `gitnexus://repo/fhir-map/process/{name}` | Step-by-step execution trace |

## CLI

| Task | Read this skill file |
|------|---------------------|
| Understand architecture / "How does X work?" | `.claude/skills/gitnexus/gitnexus-exploring/SKILL.md` |
| Blast radius / "What breaks if I change X?" | `.claude/skills/gitnexus/gitnexus-impact-analysis/SKILL.md` |
| Trace bugs / "Why is X failing?" | `.claude/skills/gitnexus/gitnexus-debugging/SKILL.md` |
| Rename / extract / split / refactor | `.claude/skills/gitnexus/gitnexus-refactoring/SKILL.md` |
| Tools, resources, schema reference | `.claude/skills/gitnexus/gitnexus-guide/SKILL.md` |
| Index, status, clean, wiki CLI commands | `.claude/skills/gitnexus/gitnexus-cli/SKILL.md` |

<!-- gitnexus:end -->

# Core Principles — code changes

Non-negotiable for every code change in this repo — even docs and one-liners.

- **Branch + PR, never straight to `main`.** Every change lands on a feature branch and
  merges through a pull request. Never commit or push to `main` directly, and never bypass
  branch protection.
- **The gate is the contract.** Every PR must pass the full gate — the same checks locally
  (lefthook) and in CI: build, gofmt, goimports, `-race` tests, gitleaks, golangci-lint,
  gosec, govulncheck. Never `--no-verify`, never merge past required checks, and never
  suppress a finding just to go green — make it actually pass.
- **Fix the root cause and prove it.** Don't paper over a finding. Exercise the change
  end-to-end (tests, fuzz, govulncheck), and add regression coverage for every bug you fix
  (e.g. commit the fuzz crasher).
- **Be honest about what you can't fix.** If a finding has no code fix — no upstream patch,
  time-based, or an external human step — say so plainly. Never fake a resolution or
  silently drop it.
- **Keep dependencies pinned and patched.** Pin actions and images by SHA/digest, keep
  modules current, and treat security-scan findings as work, not noise.
