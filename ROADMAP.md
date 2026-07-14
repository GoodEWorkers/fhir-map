# Roadmap

fhir-map is an **MVP that ships**: the core `$translate` / `$transform` component is
released, load-tested, and runs the flagship HL7v2/HPRIM → FHIR path end-to-end. This
document is deliberately honest about where the boundary is today and what could come
next — and, importantly, **what demand signal would justify building each item**. We
build ahead of need only where it's cheap and safety-relevant; everything else is
pulled forward by a concrete requirement.

This is the public summary. The line-by-line engine coverage lives in
[`docs/specs/fhir-mapping-language-completeness.md`](docs/specs/fhir-mapping-language-completeness.md).

---

## What works today (MVP)

- **Terminology translation** — ConceptMap `$translate` (forward/reverse, `CodeableConcept`,
  `dependsOn` filtering, unmapped fallbacks, version pinning) and `$translate-batch`
  (N codes in one indexed SQL round-trip).
- **Structural transformation** — StructureMap `$transform` over a broad subset of the FHIR
  Mapping Language: all transform codes, dependent groups, nested rules, multi-source groups,
  `extends`, list modes, `where` / `check`, variable scoping.
- **HL7v2 / HPRIM ingestion path** — built-in message parsing; ADT / ORU / ORM → FHIR.
- **Dual R4 + R5** — one server, canonical R5 storage, R4 projection at the wire boundary.
- **CRUD + history** — ConceptMap / StructureMap / StructureDefinition with soft-delete,
  versioned `_history`, `vread`, and `If-Match` optimistic concurrency.
- **Production hardening** — bounded `$transform` (timeout + recursion + body caps), opt-in
  strict/fail-loud mode, opt-in structural output-validation gate, Prometheus metrics,
  TLS, and a HIPAA deployment guide.

## Known boundary (today)

- No `StructureDefinition`-driven runtime type system — so polymorphic `value[x]` dispatch,
  `<<types>>` default-group selection, and typed `create` are not implemented; arbitrary
  FHIR↔FHIR structural maps that depend on them aren't fully supported.
- Output validation is **structural** only; it does not check profiles or terminology bindings.
- The server validates output against `-tx n/a` (no value-set / terminology binding validation).

---

## Possible next steps — pulled forward by demand

Each item is parked, not promised. The **Trigger** column is the concrete signal that would
move it from "deferred" to "scheduled."

| Item | What it adds | Trigger that justifies it |
|------|--------------|---------------------------|
| **Terminology (`tx`) binding validation** | Point `$validate` / the output gate at a real FHIR terminology server (e.g. self-hosted [FHIRsmith](https://github.com/ansforge/interop-outil-fhir-terminology-server)) so coded values validate against value sets. The ANS build adds SNOMED CT FR + SMT. | A deployment that must validate codes against value sets, or a French/SMT-regulated target. |
| **Official-validator output gate (v2)** | A second `OutputValidator` implementation behind the existing seam that runs results through the official HL7 FHIR validator, not just structural checks. | A caller that needs profile-level conformance guarantees on transform output. |
| **Mapping-conformance differential gate** | A standing CI gate running the HL7 FML test maps + v2-to-FHIR IG corpus + a `hapi-compare` oracle, so map parity is *measured*, not asserted. | A requirement for proven IG parity (procurement, certification, or a parity SLA). |
| **`StructureDefinition`-driven type system** | Polymorphic `value[x]` dispatch, `<<types>>` default groups, typed `create` — unblocks arbitrary FHIR↔FHIR structural maps. | A use case beyond the HL7v2 flagship that needs general structural mapping. |
| **Transform-level audit / lineage** | Source-message-id → output-resource-id hooks for end-to-end lineage. | A compliance or traceability requirement from an embedding pipeline. |

---

## How to ask for something

Open an issue describing the use case (not just the feature). The use case is what tells us
which trigger above you're hitting — and that's what gets an item scheduled.
