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

# Committing

- **NEVER add a `Co-Authored-By:` trailer (or any AI/assistant attribution) to commit
  messages or PR bodies.** Commits are authored by the user, full stop. Do not add it
  "by default" — it is explicitly unwanted here.
- **NEVER commit with `--no-verify`.** The lefthook pre-commit hooks (build, gofmt,
  goimports, `-race` unit tests, gitleaks, golangci-lint, gosec, govulncheck) are the
  required gate — same checks CI runs. Make them pass; do not bypass them.
- The hook tools (`gosec`, `govulncheck`, `golangci-lint`, `gitleaks`, `lefthook`) are
  installed locally in **`$(go env GOPATH)/bin`** (i.e. `~/go/bin`) — they are NOT in the
  repo `./bin/` (that holds server binaries). They may be missing from a fresh shell's
  PATH, which makes the hooks exit 127 ("not installed"). **Source them first:**

  ```sh
  export PATH="$(go env GOPATH)/bin:$PATH"
  ```

  then `git commit` normally. If a hook step flakes on the first run, just run the commit
  again. If `~/go/bin` is genuinely missing a tool, run `make tools` (installs the pinned
  versions via `scripts/install-tools.sh`).

# Commenting

Prior sessions over-documented this repo — algorithm walkthroughs, per-function
essays, and sprint/ticket tags welded into the logic. Most of it is narration that
restates the code and drifts into lies the moment the code changes. **Do not regrow it.**

- **Make the code carry the meaning first — a comment is the last resort.** Before
  writing a comment, try to make it unnecessary. Name the *intent*, not the mechanics:
  extract a complex condition into a named boolean (`isEligibleForTranslate := unmapped &&
  hasOtherMap && depth < maxDepth` then `if isEligibleForTranslate {`), pull a block into a
  well-named helper, replace a magic number with a named constant, use guard clauses to
  flatten flow. The name should say *why it matters* (`sourceMatchedNothing`), never just
  restate the operators (`aAndBTrue`). Self-explanatory code suffices most of the time.
- **Comment only the non-obvious *why*** that naming still can't carry — a constraint,
  trade-off, gotcha, or spec/RFC citation a reader cannot recover from the code itself.
- **NEVER write algorithm walkthroughs** (`// Algorithm: 1. … 2. …`) or any comment
  that narrates the control flow the code already expresses. Test per comment: *if
  changing the code would force changing the comment, it's narration — delete it.*
- **NEVER weld sprint/milestone/AC/story/audit tags into code** (`M5d`, `AC-4`,
  `Story 2.6`, `B7`, `SYM-GR`). That history belongs in commit messages and the tracker.
- Exported symbols get a **one-line** godoc summary (Go convention) — one line, not an essay.
- Prefer **deleting** a stale comment over updating it.
- When **bulk-deleting** comments, never strip a comment the toolchain reads (`//go:*`,
  `//nolint`, `//exhaustive:ignore`, `// +build`) or one that is the entire body of a code
  block (e.g. an empty `default:`/`case:`). Verify the pass with a non-comment **token diff**
  against the original — behavior-neutral code edits pass build and tests; only a token diff
  catches them.
