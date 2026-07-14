# fhir-map Mapping Language Coverage Report

**Date:** 2026-05-17 (post M6.14)
**Spec references:** `build.fhir.org/mapping-language.html`, `build.fhir.org/structuremap.html`
**Server:** `fhir-map` @ commit `3e0316d` (M6.14)
**Result:** 82 Bruno requests · 173 assertions · 82 passing · 7 spec-gap failures remaining (down from 9).

Every failing test asserts the FHIR-spec-intended behaviour, not the server's
current behaviour. A red test is the bug report — the docs block of each `.bru`
explains the fix surface.

## Test-integrity note

A previous revision of this collection adjusted two tests to mask still-present
spec gaps (changed `defaultValueString` choice-type to a flat `defaultValue`
key; changed `check` on an element-less source to an element-bound source).
Those edits made the tests pass against a non-conformant server — bad practice
for a spec-conformance suite. They have been reverted. Both probes are back to
the spec-conformant wire form and now correctly stay red.

## How to run

```sh
cd bruno-mapping-coverage
npx --yes @usebruno/cli@latest run --env local
```

| Folder | Coverage |
|---|---|
| `01_Setup` | Seeds ConceptMap + sentinel StructureMap. |
| `02_Transforms` | Every transform code recognised by the executor (28 cases). |
| `03_FHIRPath` | Source-condition and target-evaluate FHIRPath. |
| `04_ListMode` | source.listMode + target.listMode gap. |
| `05_FML` | Inline FML via `fml` / `srcMap`. |
| `06_Structure` | Dependents, nested rules, multi-source product, group extends. |
| `07_Endpoints` | Type / instance / system endpoints; R4 vs R5; param rejection. |
| `08_Errors` | OperationOutcome shape for invalid input. |
| `09_SpecGaps` | Tests written to fail until a specific gap is fixed. |
| `99_Cleanup` | DELETEs seed resources. |

---

## What M6.Fix.1–5 closed (5 gaps → green)

| Fix | Test that turned green | What works now |
|---|---|---|
| M6.Fix.1 | `09_SpecGaps/04_min_max_cardinality_GAP` | `source.min`/`max` enforced, 422 on violation. |
| M6.Fix.2 | `06_Structure/03_multiple_sources_GAP` + `06_Structure/05_multi_source_product` | Multi-source cartesian product — `(given, family) -> name.text` works. |
| M6.Fix.3 | `09_SpecGaps/07_fml_implicit_copy_form_GAP` | `tgt.x = v;` shorthand parses as `copy(v)`. |
| M6.Fix.4 | `02_Transforms/21_dateOp`, `23_id_identifier`, `24_cp_contactpoint`, `28_dateOp_arithmetic` | `dateOp` does ISO-8601 duration arithmetic; `id(system,value)` builds Identifier; `cp(system,value[,use])` builds ContactPoint; single-arg forms kept as back-compat passthrough. |
| M6.Fix.5 | `09_SpecGaps/01_toDateTime_transform_GAP`, `02_Transforms/25–27` | `toDateTime`/`toDate`/`toTime` + `unixToDateTime`/`unixToDate`/`unixToTime` family with Java SimpleDateFormat-style patterns. |

---

## Residual gaps (9 failing tests, each a real bug or missing feature)

### Executor — semantic gaps

1. **`source.check` is skipped when the source has no element**
   (`03_FHIRPath/15_check_GAP`). `evalSource()` early-returns at
   `if src.Element == ""` before reaching the check block. Move
   check/cardinality/defaultValue/listMode above the early-return.

2. **`source.defaultValue[x]` choice-type wire form not parsed**
   (`03_FHIRPath/16_defaultValue_GAP`). The struct flattens it to
   `defaultValue json.RawMessage` (model.go:105); spec uses
   `defaultValueString`/`defaultValueInteger`/etc. M6.Fix.1 implemented the
   semantics; the wire form blocks any spec-conformant payload from reaching
   them. Custom UnmarshalJSON needed, same pattern as Parameter.value[x].

3. ~~**`group.extends`** (`06_Structure/04_group_extends_GAP`).~~ **CLOSED M6.14** — `entryGroup()` picks the leaf (non-extended) group as entry point; executor already ran parent rules before child rules once the right group was invoked.

### FML parser — syntax gaps

4. **`then { ... }` inline rules and `then groupName(args)` dependent-call
   syntax** (`05_FML/06_fml_then_inline_GAP`). Neither the lexer nor the parser
   recognises the `then` keyword. Forces mapping authors to drop to the JSON
   form for nested rules.

5. ~~**`let name = expr;` constants** (`05_FML/07_fml_let_const_GAP`).~~ **CLOSED M6.14** — top-level `let` now parses into `StructureMap.Const`; engine injects constants into root scope via `fhirpath.EvalIn` before first group runs.

6. **`uses ... as source` / `as target`** (`09_SpecGaps/08_fml_uses_as_mode_GAP`).
   Lexer reserves `source`/`target` as `tKwSource`/`tKwTarget`; `parseUses`
   expects `tIdent` after `as`. Either drop those from the keyword table or
   make `parseUses` accept those token kinds.

7. **`as v where (...)` order** (`09_SpecGaps/09_fml_as_before_where_GAP`).
   `parseSource()` reads `where` before `as`. The spec allows either order.
   Authors must currently put `where` first.

### Transform vocabulary — missing forms

8. **`cc(text)` one-arg form** (`09_SpecGaps/02_cc_single_arg_GAP`). Spec
   permits CodeableConcept with only a `text` slot. Server requires (system,
   code[,display]).

9. **`qty(text)` one-arg form** (`09_SpecGaps/03_qty_text_form_GAP`). Spec
   parses `"5 mg"` into `{value: 5, unit: "mg"}`. Server requires (value,
   unit).

### Other (already documented previously but not in the failing-9 because the
gap test is permissive)

- `source.type` polymorphic dispatch (`09_SpecGaps/05`) — `source.type` hint
  ignored; rules fire regardless of actual JSON type.
- `source.logMessage` (`09_SpecGaps/06`) — FHIRPath logging not evaluated.
- `source.listMode = share` and **target.listMode** (`03_FHIRPath/14`,
  `04_ListMode/04`) — share / single collation not implemented.
- `pointer` transform (`02_Transforms/20`) — passthrough, not Reference shape.

---

## Priority for an ETL pipeline shipping into hospital systems

If the next milestone is "feed real hospital data through this", the order that
minimises map rewrites:

1. **`defaultValue[x]` choice-type wire form (#2)** — every spec-conformant map
   uses this shape; the M6.Fix.1 semantics are stranded behind a wire-form
   mismatch.
2. **Element-less `check` (#1)** — two-line fix in evalSource; restores spec
   coverage of the now-existing check feature.
3. **FML `then` and `let` (#4, #5)** — unblocks nested FML; without `then`,
   maps must be authored as JSON.
4. **`uses ... as source/target` (#6)** — required to declare structure
   ingress/egress modes in FML at all.
5. **`group.extends` (#3)** — enables map composition.
6. **`cc(text)` / `qty(text)` (#8, #9)** — covers free-text clinical fields
   common in hospital exports.

The remaining items (target listMode, polymorphic type, logMessage, pointer,
share collation) are useful for the long tail of published maps but rarely
appear in straight HL7v2/CSV → FHIR pipelines.

---

## What the suite confirms now works

### Transforms (28 green tests)
copy, create, truncate, escape, append, uuid, cast (×3 types), reference,
c (Coding), cc (system+code+display), qty (4-arg Quantity), evaluate,
translate (×3 outputs), id (single-arg passthrough + (system,value) Identifier),
cp (single-arg + (system,value[,use]) ContactPoint), dateOp (single-arg
passthrough + real duration arithmetic), toDateTime, toDate, unixToDateTime,
pointer (passthrough).

### FHIRPath
Equality, `and`, `iif`, `substring`, `matches` regex, `length`, arithmetic with
correct precedence, `&` concat, `.exists(predicate)`, `in (... | ...)`,
`$this`.

### Source semantics
`condition`, `check` (with element), `defaultValue` (server's flat key —
choice-type variant still broken), `min`/`max` cardinality, `listMode` (first,
last, not_first, not_last, only_one).

### Structure
Dependent group invocation; nested rule[]; multi-source cartesian product.

### Endpoints
R5 type-level, R5 instance, system-level alias, legacy `/fhir` R5 alias, R4
tree, R4 rejection of R5-only params with `not-supported`.

### Error surface
Missing content → 400; bad FML → 400 with line number; unknown transform →
422; unbound context → 422; unknown map canonical → 404.
