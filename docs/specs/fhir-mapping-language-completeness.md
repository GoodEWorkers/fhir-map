# FHIR Mapping Language / `$transform` Engine — Completeness Reference

**Scope:** `internal/transform/**` (fhirpath, fml, executor)
**Companion docs:** [`hl7v2-hprim-parser-spec.md`](./hl7v2-hprim-parser-spec.md) (the flagship source-reader spec) · [`../../ROADMAP.md`](../../ROADMAP.md) (public roadmap & demand-driven next steps)

This is the engineering reference for how complete the mapping engine is against the FHIR
Mapping Language (FML) spec. It records what has landed, the gaps that remain, **why** they
exist, and — for each — a concrete FML snippet plus the FHIR payload that illustrates the
gap. The prioritized, demand-driven view of these gaps lives in [`ROADMAP.md`](../../ROADMAP.md);
this document is the detailed backing it links to.

> **State labels** used throughout:
> - **✅ done** — parsed *and* executed correctly.
> - **🟡 parsed-only** — the FML parser lowers it into the AST, but the executor does not act on it yet.
> - **❌ missing** — not parsed (or not implemented) at all.

---

## 0. What already works (baseline)

Confirmed by unit + integration tests and a live Bruno end-to-end run
(`bruno/` 67/67 requests · 283/283 assertions; `bruno-mapping-coverage/` 82/82 · 174/174):

- **All spec transforms** are implemented: `copy`, `create`, `truncate`, `escape`, `cast`,
  `append`, `translate`, `reference`, `uuid`, `pointer`, `evaluate`, `cc`, `c`, `qty`, `id`,
  `cp` — plus `toDate(Time/Time)` and `unix*`, which are R6-draft transforms (FHIR-40645),
  not in published R4/R5.
- **Executor honors**: source `where` / `check` / `listMode` / cardinality / `defaultValue`,
  group `extends` (parent rules run inline), `let`, inline conceptmaps, type modes, the
  `///` metadata header form, and exact-URL `imports`.
- **Recently completed** (merged to `main`, PRs #7/#8):
  decimal + unary `±` literals; `<` `<=` `>` `>=` and `$`-variables in FML lexing;
  `$index` engine support; inline `(expr)` execution; FML lowering of source `check`,
  source list modes, source element `type`, target list modes, and source `default`.

---

## 1. Two axes of "complete"

| Axis | Meaning | Primary bar |
|---|---|---|
| **A — FML conformance** | Behave per the [FHIR Mapping Language spec](https://build.fhir.org/mapping-language.html) and FHIRPath | HL7 FML test maps |
| **B — Flagship use case** | HL7v2 / HPRIM → FHIR lab ingestion (this product's headline) | `hapi-compare` oracle + v2-to-FHIR IG |

Both matter. Axis B carries the highest *real-world* value for this product because its
gaps are silent **clinical data-loss** bugs (see §6).

---

## 2. Root cause behind most Axis-A gaps: a structure-unaware engine

The executor navigates **generic JSON maps** (`map[string]any`), not FHIR structures driven
by `StructureDefinition`s. There is no runtime type system. That single fact is the root of
the largest remaining gaps:

- It cannot choose `value[x]` polymorphic targets by type.
- It cannot auto-select a **default mapping group** by (sourceType, targetType) — the
  `<<types>>` mechanism.
- `create(type)` cannot instantiate a properly-shaped FHIR datatype.

A `StructureDefinition`-backed type layer is the **single highest-leverage** investment: it
unblocks several Tier-B gaps at once. It is also the largest.

---

## 3. Tier A — runtime for already-parsed constructs (cheap; AST ready)

### 3.1 Target list-mode execution — ✅ done

Target list modes (`first` / `last` / `single` / `share` / `collate`) parse into
`Target.ListMode` / `Target.ListRuleId` **and are now executed**. They govern how repeating
target elements are written and, critically, whether successive writes **collate into one
instance** or create a new one each time. The textbook case is `share`: several source items
write into a *single* shared target element (one `HumanName` with many `given`s), not one
element per item.

Implemented semantics (grounded in HAPI's `StructureMapUtilities`): `share <ruleId>` reuses a
single instance across the rule's firings (scope-level store keyed by the rule id); `collate`
always builds a list; `first`/`last` keep the first/last firing; `single` errors on a second
write (`ErrTargetListSingle`). No mode keeps the prior promote-on-repeat heuristic.

**FML** (`share <ruleId>` ties the writes to the same instance)
```
group names(source pid, target patient : Patient) {
  pid.given as g -> patient.name = create('HumanName') as n share nameRule,
                    n.given = g;
}
```
Source `pid.given` = `["Ada", "Augusta"]`.

**Desired FHIR — one `HumanName`, two `given`s (share honored):**
```json
{ "resourceType": "Patient", "name": [ { "given": ["Ada", "Augusta"] } ] }
```

**Before this change — `share` ignored, last firing won, earlier givens lost:**
```json
{ "resourceType": "Patient", "name": { "given": "Augusta" } }
```

---

## 4. Tier B — the type system (largest, highest leverage)

### 4.1 Polymorphic `value[x]` dispatch — 🟡 partial: shape-inferred, not SD-driven (source `type` is 🟡 parsed-only)

The load-bearing lab rule: **OBX-2 selects `Observation.value[x]`** (`NM`→`valueQuantity`,
`ST/FT/TX`→`valueString`, `CWE`→`valueCodeableConcept`, …). The engine cannot pick the typed
element because it does not know element types.

**FML (intended)**
```
group obx2obs(source obx, target obs : Observation) {
  obx where (obx.valueType = 'NM') -> obs.value : Quantity = qty(obx.value) "asQty";
  obx where (obx.valueType = 'ST') -> obs.value : string  = copy(obx.value)  "asStr";
}
```

**Source (HL7v2 OBX, numeric):** `OBX|1|NM|CRP^C-Reactive Protein^L||4.2|mg/L|...`

**Desired FHIR**
```json
{
  "resourceType": "Observation",
  "code": { "coding": [{ "code": "CRP", "display": "C-Reactive Protein" }] },
  "valueQuantity": { "value": 4.2, "unit": "mg/L" }
}
```

**Today:** a **shape-inference heuristic** (`normalizeChoiceTypes`, landed with the HAPI-parity
work) picks the suffix from the *runtime value's shape* — the lab golden test now produces
`valueQuantity` + LOINC. Remaining gap: dispatch is not **declared-type/StructureDefinition-
driven** (the parsed source/target `type` and rule `obs.value : Quantity` annotations are
ignored; hardcoded to 8 base names; numeric ambiguity unresolved; no choice elements on
non-resource objects). **Done =** type-directed selection of the `value[x]` element name from
the declared/source type, driven by `StructureDefinition`.

### 4.2 Type-directed default groups (`<<types>>` / `<<type+>>`) — ❌ missing

Per spec, an unqualified sub-mapping should auto-select the **default group** whose
(sourceType, targetType) signature matches — without an explicit `then group(...)`.

**FML**
```
group main(source src : TLeft, target tgt : TRight) <<types>> {
  src.child as c -> tgt.child as d;   // <-- engine must find the TChild->TDChild default group
}
group childMap(source c : TChild, target d : TDChild) <<types>> { ... }
```

**Today:** only explicit `then group(...)` and `extends` resolve; automatic type-based
default-group selection does not. **Done =** match default groups by input type signature
(requires the §2 type layer).

### 4.3 Typed `create(type)` — 🟡 partial

`create('Patient')` → `{ "resourceType": "Patient" }` **is done** (resourceType stamping via
the embedded HL7 R5 base StructureDefinitions). Remaining: no-arg `create()` inferred from the
target element definition, canonical-URL/alias type parameters, and datatype instantiation
beyond a bare `{}` (`create('Quantity')` → an object the downstream `value`/`unit`/`system`/
`code` writes attach to with correct types).

---

## 5. Tier C — remaining FML grammar

### 5.1 Explicit `evaluate(context, <expression>)` — ✅ done

The explicit two-arg form now parses: `parseTarget` special-cases `evaluate(context, expr)` —
the 1st arg is the context (a variable/path), the 2nd is a FHIRPath **expression** captured raw
via `captureParenBalanced` (mirroring the inline `(expr)` form), so operators and nested function
calls (`iif($this > 0, 'pos', 'neg')`) round-trip to the engine.

```
s.value as v -> t.out = evaluate(v, $this + 2);   // ✅ now parses → 7
s.value as v -> t.out = (v + 1);                  // ✅ inline shorthand
```

### 5.2 `imports "...*"` wildcard — ⏸ deferred

`resolveImports` resolves only exact canonical URLs; the spec allows a trailing `*`. Implementing
it requires extending the `MapResolver` interface (currently exact `FindByURL` only) with a
prefix/list method + impls (Service + the in-memory test resolver) — a cross-cutting change for a
niche feature. Deferred until a real multi-map project needs it.

### 5.3 Minor — markdown `"""` metadata values, `uses … as queried/produced` semantics.

---

## 6. Axis B — HL7v2 / HPRIM parser (flagship)

> **Status (2026-05-31): largely resolved.** The "confirmed live defects" below were a stale
> snapshot. Verified against current HEAD:
> - §6.1 repeated-segment collapse, §6.2 OBX-2→`value[x]`, §6.3 missing `resourceType` — ✅ **fixed**
>   (commit `6573493`; `golden_lab_test.go` produces 5 Observations with `valueQuantity` + LOINC, 1
>   Patient, 1 DiagnosticReport, none missing `resourceType`).
> - §6.4 parser maturity — ✅ **done**: header-driven delimiter discovery, HPRIM delimiter order (the
>   §6.2 swap, handled by discovery — no opt-in needed), escape unescaping, `~` field repetition, and
>   three-state null (`""`), each with the encoding field guarded and the default corpus byte-identical
>   (`internal/transform/hl7v2/{parse,escape}.go` + `*_test.go`).
> - ⏸ Deferred (documented): the **MSH +1 field offset** (breaking vs existing maps → future opt-in),
>   charset/MSH-18, structured segment groups (ORU_R01), HPRIM `A`-continuation.
>
> The original defect descriptions are kept below for history.

These were **correctness bugs on the primary workflow** — see
[`hl7v2-hprim-parser-spec.md`](./hl7v2-hprim-parser-spec.md) §13.

### 6.1 Repeated-segment collapse — ❌ (silent clinical data loss)

**Source (ER7, two results):**
```
OBX|1|NM|Na^Sodium^L||140|mmol/L|...
OBX|2|NM|K^Potassium^L||4.1|mmol/L|...
```
**Desired:** two `Observation`s (Sodium 140, Potassium 4.1).
**Today:** only the **last** OBX survives — the Sodium result is dropped.

### 6.2 OBX-2 → `value[x]` not applied — ❌ (see §4.1)

Observations emit without a value or LOINC code.

### 6.3 Output entries missing `resourceType` — ❌ (invalid FHIR)

**Today (invalid):**
```json
{ "entry": [ { "resource": { "code": { } } } ] }
```
**Desired:**
```json
{ "entry": [ { "resource": { "resourceType": "Observation", "code": { } } } ] }
```

### 6.4 Also: MSH/`H` delimiter discovery, escape unescaping (`\F\`, `\X0D\`, `\.br\`), the
HPRIM delimiter-order divergence (`~`=subfield, `^`=repeater) — all per the companion spec.

---

## 7. Tier D — transform / datatype depth

- **Date format codes** for `toDate` / `toDateTime` / `toTime` / `dateOp` — 🟡 partial.

  *Provenance (fact-checked 2026-06-10):* the published spec defines **no** format codes —
  R4/R5 `dateOp` is, verbatim, "Perform a date operation. Parameters to be documented", and
  HAPI's reference engine throws "Transform dateOp not supported yet"
  ([org.hl7.fhir.core#948](https://github.com/hapifhir/org.hl7.fhir.core/issues/948)). The
  mask dialect we accept is **Java-SimpleDateFormat-style by empirical necessity**: the real
  (anonymized) client lab maps were authored for a HAPI-ecosystem engine and use Java-isms
  (`yyyy-MM-dd'T'HH:mm:ssXXX`). The forward-looking alignment target is the **R6 draft**
  ([build.fhir.org/mapping-language.html](https://build.fhir.org/mapping-language.html),
  FHIR-40645), which adds `toDateTime`/`toDate`/`toTime`/`unixTo*` (transforms this engine
  already implements) and defines FHIR's own vendor-neutral code table:
  `yy yyyy M MM MMM MMMM d dd h hh H HH m mm s ss S[+] a z Z`.

  Supported today (run-tokenized, loud error on anything else): `yyyy yy MM M dd d HH hh h
  mm m ss s a Z` + literal `T`, and `.SSS`/`,SSS` (Go's fractional-second token includes the
  separator; bare `SSS` is rejected loud). (`a`/`Z` previously slipped the guard
  untranslated — fixed.) Still missing vs the R6 draft table: single `S`/`S[+]`, `MMM`,
  `MMMM`, `z`, unpadded 24-hour `H` (no Go equivalent), and the Java-side colon-form
  offsets (`XXX`/`+02:00`) seen in client maps. Known divergence: `a` matches uppercase
  `AM`/`PM` only.
- **`cast` / `translate` equivalence filtering** and the constructor transforms
  (`cc` / `c` / `qty` / `id` / `cp`) — present; verified gaps: `pointer` is a passthrough stub,
  `escape` ignores its `(string1, string2)` args, `cast` requires an explicit type, `translate`
  lacks `system`/`display` output types, `qty` 3-arg drops its third argument.

---

## 8. Conformance & validation — what's gated today vs. what isn't

**Resource validation (in place).** CI's `fhir-validate` job runs the **official HL7 FHIR
Validator CLI** (`validator_cli.jar` 6.4.0) at R5 (and R4 for the Story-4.3 fixtures) over the
conformance fixtures + examples (`testdata/conformance/**`, the StructureDefinition / StructureMap
/ ConceptMap fixtures, the ADT→Bundle example). A runtime **`$validate`** with strict/lenient
modes (`ValidateMode`) enforces structural invariants in both tiers and vocabulary bindings in
strict mode. So *authored/stored resources* are validated against the official validator.

**Mapping conformance (NOT gated).** Distinct from resource validation: do our **maps produce the
IG-expected output**? That bar isn't run yet:

1. **HL7 FHIR Mapping Language test maps** (the spec's own `.map` + expected-output fixtures).
2. **HL7 v2-to-FHIR IG** maps repo (the segment/datatype mapping corpus).
3. **`hapi-compare` oracle** — a recorded HAPI output bundle for differential parity with HAPI.

**Partially gated, related:** an opt-in **structural** output gate now exists
(`SERVER_TRANSFORM_VALIDATE_OUTPUT`, see §10 P0.2) — it rejects/flags malformed `$transform` output
in-process. What is still **not** wired is running the output through the *official* validator
inline (the `validator_cli`/remote-`$validate` impl behind the `OutputValidator` seam), and the
validator still runs `-tx n/a` (no terminology/value-set binding validation). Wiring the mapping
corpus + the official-validator output impl is what would turn "compatible" into measured pass/fail.

---

## 9. Prioritized roadmap

| # | Item | Tier | Status |
|---|---|---|---|
| 1 | Land branch `feat/fhirpath-numeric-literals` (PR) | — | ✅ done — merged to `main` (PRs #7/#8) |
| 2 | Target list-mode **execution** | A (§3.1) | ✅ done |
| 3 | HL7v2: repeated-segment + `resourceType` | B (§6.1/6.3) | ✅ done (pre-existing; verified) |
| 4 | HL7v2: OBX-2 → `value[x]` + ConceptMap translate | B (§6.2) | ✅ done (pre-existing; verified) |
| 4b | HL7v2 parser maturity (discovery, escaping, `~`, HPRIM order, null) | B (§6.4) | ✅ done |
| 5 | Explicit `evaluate(ctx, expr)` | C (§5.1) | ✅ done (PR #13 + quoted-form fix) |
| 5b | `imports "...*"` wildcard | C (§5.2) | ⏸ deferred |
| 6 | Date-format coverage; transform edge cases | D (§7) | ☐ todo |
| 7 | **Type-system layer** (StructureDefinition-driven) | B (§4) | ☐ todo (largest leverage) |
| 8 | Mapping-conformance corpus as CI gate | §8 | ☐ todo (the "done" bar) |

**Guiding principle:** every change is TDD (red → green), one topic per commit, with the full
lefthook gate (build, gofmt, goimports, `-race` tests, gitleaks, golangci-lint, gosec,
govulncheck) and an `$transform` Bruno e2e before merge.

---

## 10. Production-readiness plan — as a **FHIR transform component**

Scope correction: this engine is a **FHIR `$transform` component** (a deterministic transform
service + map store), **not** an ETL pipeline. Ingestion (MLLP/file/queue), orchestration,
scheduling, dead-letter/retry, backpressure, and data-lineage are the **embedding pipeline's**
responsibility and are explicitly out of scope here. This plan is what makes the *component*
safe to embed in a production pipeline.

### P0 — block go-live (the component must not silently lie)
1. **Strict / fail-loud transform mode.** ✅ **DONE (v1).** `WithStrictTransform(true)` (opt-in;
   server flag `SERVER_TRANSFORM_STRICT=true`; default lenient/byte-identical) fails loud with a
   precise 422 OperationOutcome on the two **unambiguous silent-data-loss** cases: a typed-transform
   **coercion failure** (`ErrNonConformantCoercion` → code `value`; overrides conformance logging)
   and a **resolved-ConceptMap-with-no-mapping** translate drop (`ErrTranslateNoMatch` → code
   `not-found`). Error details are PHI-conservative (transform name / map URL only). Calibrated
   scope: spec-compliant best-effort no-ops (empty/optional source, `where`-filtering,
   unbound-context, default-filled) **stay lenient even under strict** — hardening them would turn
   normal optionality into false failures (per the design's adversarial review). *v2 candidates
   (deferred, need the review's guards):* unbound-target-context (guard on `source.min>0`),
   all-params-nil, source cardinality/listMode, `const` cardinality.
2. **Opt-in output validation gate.** ✅ **DONE (v1, structural).** A pluggable `OutputValidator`
   seam validates the `$transform` *result* before return; opt-in via
   `SERVER_TRANSFORM_VALIDATE_OUTPUT` (`off` default / `lenient` / `strict`). In strict mode invalid
   output is rejected as a 422 OperationOutcome (`code: structure`, PHI-conservative diagnostics);
   in lenient mode it is returned but flagged via a `Warning` response header + server log — never
   emitted blindly. The v1 implementation is an **in-process structural** validator (well-formed
   FHIR object, non-empty `resourceType`, and every Bundle entry resource likewise — closes the
   §6.3 invalid-FHIR gap). It does **not** check profiles/cardinalities/terminology — that is the
   official validator's job and plugs in behind the same `OutputValidator` interface (a
   `validator_cli`/remote-`$validate` impl is the deferred v2; the heavy JVM path is intentionally
   kept out of the per-request hot loop). HL7v2 (ER7) targets are exempt — not FHIR.
3. **Observability.** ✅ **DONE (metrics).** Prometheus metrics at `GET /metrics`, two layers:
   (a) generic HTTP RED across **every** route — `fhirmap_http_requests_total{method,route,status}` +
   `fhirmap_http_request_duration_seconds{method,route}`, labelled by the matched route *template*
   (`r.Pattern`, e.g. `GET /fhir/R5/StructureMap/{id}`) so it's PHI-safe and low-cardinality
   (unmatched → `unmatched`); and (b) the richer `$transform` domain layer —
   `fhirmap_transform_total{result,code}` (success/error by FHIR issue code) +
   `fhirmap_transform_duration_seconds`. Transform metrics meter *execution* outcomes
   (pre-execution request/map-resolution errors are captured by the generic HTTP layer instead).
   *Deferred:* distributed **tracing** (OTel spans) — lower value for an in-process transform (the
   embedding pipeline owns cross-service tracing); revisit if needed. *Deferred:* per-rule failure
   counts (needs rule-path context — pairs with the strict-mode v2 work).

### P1 — strongly recommended before scale
4. **Mapping-conformance CI gate** (§8): run the HL7 FML test maps + v2-to-FHIR IG corpus +
   `hapi-compare` oracle as a standing differential gate. *Acceptance:* green = measured FML/IG
   parity, not inferred.
5. **Terminology validation option:** allow `$validate`/the gate to run against a real `tx` server
   (currently `-tx n/a`) so coded values validate against value sets.
6. **Transform-level audit / lineage** hooks (source message id → output resource ids) and a **TLS
   + PHI sign-off** (TLS is supported; confirm enabled, log-PHI review, encryption at rest).

### P2 — completeness & scale
7. **Type-system layer** (§4): polymorphic `value[x]`, `<<types>>` default groups, typed `create`
   — unblocks arbitrary FHIR↔FHIR structural maps (largest leverage).
8. **Load / soak at production volume** (large ORU: thousands of OBX, big `ED`/base64) using the
   existing `load-test`/`stress-test` targets, with SLO thresholds.
9. **Merge + release** the branch; cut a versioned artifact.

**Highest-impact first step:** P0.1 (strict/fail-loud mode) — it converts the single biggest
ETL-safety risk (silent partial output) into an explicit, caller-handled signal, and it reuses the
strict/lenient policy the engine already has for `$validate`.

---

## Appendix — current capability matrix

| Construct | Parsed | Executed |
|---|---|---|
| Source `where` / `check` / list modes / `default` / cardinality | ✅ | ✅ |
| Source element `type` | ✅ | ❌ (no type dispatch) |
| Target list modes (`first`/`last`/`single`/`share`/`collate`) | ✅ | ✅ |
| Inline `(expr)` | ✅ | ✅ |
| Explicit `evaluate(ctx, <expr>)` | ✅ | ✅ |
| `let`, `extends`, dependent `then group(...)`, inline conceptmap | ✅ | ✅ |
| Default groups via `<<types>>` signature | ✅ (mode parsed) | ❌ (no type match) |
| Polymorphic `value[x]` | n/a | 🟡 shape-inferred (`normalizeChoiceTypes`); SD/declared-type dispatch missing |
| `imports` exact URL / wildcard `*` | ✅ / ❌ | ✅ / ❌ |
| All transform functions | ✅ | ✅ (depth varies — §7) |
