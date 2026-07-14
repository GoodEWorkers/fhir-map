# HL7 R5 FML corpus (vendored)

Representative slice of the official HL7 FHIR v5.0.0 R5 FML test maps used by
the FHIR validator's StructureMap parser tests. These fixtures back
`TestFML_Parse_HL7Corpus_RoundTrip` in `parser_test.go`.

## Provenance

- **Source repo:** https://github.com/FHIR/fhir-test-cases
- **Path in source:** `r5/structure-mapping/*.map`
- **Pinned commit SHA:** `1d92a9c47f2b72d9eb5c73e93b5280bcdc76d0aa`
- **Vendored on:** 2026-05-17
- **Upstream license:** Apache-2.0 (see `LICENSE`)
- **Extension change:** upstream uses `.map`; vendored as `.fml` because that
  is the FHIR R5 spec's canonical extension for FHIR Mapping Language source
  and the fhir-map parser/test discovery globs on `*.fml`.

## Selection rationale

Ten maps were vendored to exercise the surfaces M6.10 added plus baseline
forms:

| File                          | Exercises                                          |
| ----------------------------- | -------------------------------------------------- |
| `syntax.fml`                  | `///` metadata header, inline `//` comments, fpExpr |
| `syntaxshort.fml`             | Short-form rules, minimal grammar                  |
| `cast.fml`                    | `cast()` transform, `where` clause                 |
| `whereclause.fml`             | Multiple `where` clauses                           |
| `qr2pat-assignment.fml`       | QR → Patient implicit-copy assignments             |
| `qr2pat-gender.fml`           | `translate()` transform with code system           |
| `qr2pat-humannameshared.fml`  | Shared dependent-group calls (`then Name(args)`)   |
| `qr2patfordates.fml`          | Date casting, dependent calls                      |
| `qr2reference.fml`            | `reference()` and `create()` transforms            |
| `ActivityDefinition.fml`      | Large R3→R4 map with many groups and rules         |

## Known parser gaps

`corpus_test.go` pins each currently-unimplemented grammar feature via the
`knownGaps` map: filename → expected error substring. A pinned fixture must
keep failing with that exact message until the feature lands. When grammar
work closes a gap, the parse will succeed (or the error will drift), the
assertion will fail, and the developer must remove the entry — that is how
forward progress is gated. The gaps presently tracked, in roughly increasing
implementation cost:

| Feature                                                 | Files                                       |
| ------------------------------------------------------- | ------------------------------------------- |
| `<<type+>>` singular type-mode marker                   | `ActivityDefinition.fml`, `syntaxshort.fml` |
| FHIRPath `%var` variable-reference lexer                | `qr2patfordates.fml`                        |
| String-literal RHS in target assignment (`= 'female'`)  | `qr2pat-assignment.fml`                     |
| `///` metadata header in place of `map "url" = "name"`  | `syntax.fml`                                |
| `then { … }` after source-only rule (no `->` targets)   | `cast.fml`, `qr2pat-humannameshared.fml`    |
| `where <expr>` without parentheses around inner clause  | `qr2pat-gender.fml`                         |
| `log(…)` clause inserted between `where` and `->`       | `whereclause.fml`                           |

## Updating the corpus

Re-run the fetch by bumping the SHA above and re-curling each file from
`https://raw.githubusercontent.com/FHIR/fhir-test-cases/<sha>/r5/structure-mapping/<name>.map`.
Then re-run `go test ./internal/transform/fml/` — any new parser gap will
surface as a single failing fixture with the line of the offending token.

## License attribution

These files are redistributed under the upstream Apache-2.0 license. The full
license text and HL7 attribution are preserved in `LICENSE` (the upstream
`LICENSE.txt` from the pinned SHA).
