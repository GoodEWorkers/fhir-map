# Specification — Mature HL7v2 / HPRIM Source Parser & `$transform` Readers (Go)

**Type:** Normative reference — how a mature HL7v2/HPRIM parser **must** behave · **Target:** `internal/transform/hl7v2` (and the `hprim` reader) in `fhir-map`
**Audience:** implementers of the Go source-reader / `$transform` front end.
**Implementation status:** the high-value behaviours specified here are implemented and locked by tests (`golden_lab_test.go`, `hprim_test.go`). §13 tracks current state; the few remaining items live in [`../../ROADMAP.md`](../../ROADMAP.md).

This document specifies how a *production-grade* Go implementation of the HL7v2 and HPRIM source parsers — and the surrounding `$transform` reader/builder layer — **must** behave. It is derived from two sources:

1. **A reference HAPI mapping subsystem** (`ca.uhn.fhir.jpa.starter.mapping`) used as the behavioural oracle. Cited as `[ref:<file>:<line>]`.
2. **The standards and the mature converters in the wild**: HL7 v2.x Chapter 2 encoding rules, the official HL7 *Version 2-to-FHIR* Implementation Guide, HAPI HL7v2, LinuxForHealth, Microsoft FHIR-Converter, and the Interop'Santé *HPRIM Santé 2.5* specification.

The fhir-map adapter (`internal/transform/hl7v2/parse.go`) began as a minimal positional tokenizer; the high-value requirements below — header-driven delimiter discovery, escape unescaping, `~` repetition, three-state null, repeated-segment fidelity, OBX‑2→`value[x]`, ConceptMap `translate`, and the HPRIM delimiter-order divergence — have since been implemented and locked by tests. §13 tracks current state against this spec.

> **Conformance language.** MUST / MUST NOT / SHOULD / SHOULD NOT / MAY are used per RFC 2119. A **MUST** is required for correctness or data-fidelity; a **SHOULD** is a strong recommendation that may be deferred with a logged limitation.

---

## 1. Normative references

| Ref | Document |
|---|---|
| **HL7v2-Ch2** | HL7 v2.7.1, Chapter 2 — *Control / Encoding Rules* — <https://www.hl7.eu/HL7v2x/v271/std271/ch02.html> |
| **V2toFHIR** | HL7 *Version 2-to-FHIR* IG (STU1, FHIR R4) — <https://build.fhir.org/ig/HL7/v2-to-fhir/> · maps repo <https://github.com/HL7/v2-to-fhir> |
| **HPRIM-2.5** | Interop'Santé *HPRIM Santé 2.5* — <https://www.interopsante.org/f/c64740418987d91a79015768acee1f827313a593/HPsante2_5.pdf> |
| **HAPIv2** | HAPI HL7v2 (Java reference parser) — <https://hapifhir.github.io/hapi-hl7v2/> |
| **HL7AU-Parse** | HL7 Australia, *Appendix 1 — Parsing HL7v2* — <https://confluence.hl7australia.com/display/OO/Appendix+1+Parsing+HL7v2> |
| **Tbl0211** | HL7 character-set table 0211 — <https://terminology.hl7.org/CodeSystem-v2-0211.html> |

Comparator implementations: LinuxForHealth `hl7v2-fhir-converter` (<https://github.com/LinuxForHealth/hl7v2-fhir-converter>), Microsoft FHIR-Converter (<https://github.com/microsoft/FHIR-Converter>), NHapi, HL7apy, python-hl7.

---

## 2. Terminology

- **ER7** — the pipe-delimited encoding of an HL7v2 message (`MSH|^~\&|...`).
- **Segment** — a line; a 3-char ID (`MSH`, `OBX`, `Zxx`) followed by fields.
- **Field / Repetition / Component / Subcomponent** — the four nesting levels below a segment.
- **Segment group** — a named, possibly-repeating bundle of segments defined by a message's abstract syntax (e.g. ORU_R01 `ORDER_OBSERVATION` = one `OBR` + its child `OBX`s).
- **Reader** — a component that turns an encoded source payload (HL7v2 / HPRIM / CSV / XML / JSON) into a navigable in-memory model for the `$transform` engine.
- **Builder** — the inverse: assembles an output payload (e.g. HL7v2 ER7) from mapping targets.
- **Three-state field** — *populated* / *absent* / *explicit-null (`""`)* — see §5.5.

---

## 3. Where this fits — the `$transform` pipeline

```
POST [base]/StructureMap/$transform   (Parameters)
  ├─ structureMap | source(+structureMapEndpoint)   → the map(s)
  ├─ input: name=<group-input-name>, Binary(base64)  → source payload(s)
  └─ terminologyEndpoint?                            → ConceptMap $translate

   Parameters.input[name] ──► READER (by group-input type) ──► navigable model
                                                                    │
   StructureMap rules ── navigate via Path/HPRIMPath ──► transforms ─┤
                                                                    ▼
                                          BUILDER (FHIR resource | HL7v2 | JSON | CSV)
                                                                    │
                                       serialize ► Parameters.output[name] Binary(base64)
```

Reference contract `[ref:provider/TransformProvider.java:41]` and `[ref:service/Mapper.java:107]`:

- Source/target payloads travel as **base64 `Binary` resources** inside a `Parameters` envelope, **keyed by the StructureMap group input `name`** `[ref:Mapper.java:121]`.
- The group input **`type` string** (`"HL7v2"`, `"HPRIM"`, `"CSV"`, `"JSON"`, `"XML"`, or a FHIR type) drives **both** which reader parses the input and which builder/serializer is used for the output `[ref:Mapper.java:159,173,194]`.
- A missing required **source** input is fatal; a **target** input is initialized to an empty builder `[ref:Mapper.java:130,138]`.

A Go implementation **MUST** preserve this envelope contract so it is wire-compatible with the existing prod request set (the Bruno collections and the `hapi-compare` oracle).

---

## 4. The `DataReader` abstraction

Define a common Go interface so HL7v2 and HPRIM are siblings of CSV/XML/JSON, and the transform engine navigates them uniformly:

```go
// Reader parses a decoded source payload into a navigable model.
type Reader interface {
    // Parse consumes the already-base64-decoded bytes.
    Parse(raw []byte) (Model, error)
}

// Model is navigated by the executor via typed paths.
type Model interface {
    // Select returns 0..n items addressed by a parsed path
    // (a segment set, a field, a component, …). Never errors on
    // "not found" — returns an empty slice (see §5.10 / §6/§ tolerance).
    Select(p Path) []Item
}
```

**MUST**: readers accept the **base64-decoded** content (the `$transform` layer base64-decodes the `Binary.data` first, as the reference does `[ref:HL7v2DataReader.java:31]`, `[ref:HPRIMDataReader.java:28]`).
**MUST**: a malformed payload aborts the transform with a clear error; the reference wraps all reader failures as a single fatal error `[ref:HL7v2DataReader.java:35]`. (But *navigation* of a successfully-parsed model is best-effort — see §5.10.)
**MUST**: the **type→reader/builder/content-type** table is reproduced exactly:

| `type` | Reader produces | Output builder | Output `Content-Type` |
|---|---|---|---|
| `HL7v2` | structured HL7v2 message | HL7v2 ER7 builder | `text/x-hl7-ft` `[ref:Mapper.java:194]` |
| `HPRIM` | HPRIM message | (HL7v2 builder / JSON) | `text/x-hl7-ft` |
| `CSV` | record list (header-keyed) | CSV builder | `text/csv` |
| `JSON` | object tree | JSON builder | `application/json` |
| `XML` | object tree (mapped through JSON path `[ref:Mapper.java:338]`) | — | `application/xml` |
| *FHIR type* | FHIR resource | FHIR resource | `application/fhir+json` |

---

## 5. HL7v2 parser specification

### 5.1 Input acceptance

- **MUST** accept HL7v2 wrapped in a FHIR `Binary` (`resourceType:"Binary"`, base64 `data`) — current detection sniffs the decoded first segment header (`MSH`/`H`/`BHS`/`FHS`) `[fhir-map: parse.go:30]`.
- **SHOULD** strip MLLP framing if present (`0x0B` start block; `0x1C 0x0D` end block) and a leading UTF-8/UTF-16 **BOM** before parsing `[HL7AU-Parse]`.
- **Charset (MSH-18):** **SHOULD** decode bytes using the character set declared in **MSH-18** (HL7 table 0211: `ASCII`, `8859/1…15`, `UTF-8`, `ISO IR14`, `GB 18030`, …). If MSH-18 is empty, default to **ASCII/UTF-8**. MSH-18 is repeating; the first element is the single-byte default `[Tbl0211]`. (Many real feeds force UTF-8; make it configurable.)

### 5.2 Delimiter discovery — **MUST NOT hardcode** `^~\&`

`MSH` is special and **MUST** be parsed before anything else, because it declares the delimiters for the *entire* message `[HL7v2-Ch2]`:

1. Verify the message begins with `MSH` (or `FHS`/`BHS` for file/batch headers).
2. **`MSH-1` = the field separator** — it is the literal 4th byte (`msg[3]`), immediately after `MSH`. Usually `|`.
3. **`MSH-2` = the encoding characters** — the run of bytes from `msg[4]` up to the next field separator. In order: **component (`^`), repetition (`~`), escape (`\`), subcomponent (`&`), and optional truncation (`#`)**.
4. Tokenize the rest of the message using *those discovered* characters.

**Field-numbering offset (MUST):** because `MSH-1` *is* the separator and `MSH-2` *is* the encoding string, MSH (and FHS/BHS) carry a **+1 field offset** relative to naive "split on `|`": in MSH, the token after the *second* `|` is `MSH-3`. Every other segment's first post-ID token is field 1. Get this wrong and every MSH field is off by one.

The reference sidesteps all of this by delegating to HAPI's `GenericParser` `[ref:HL7v2DataReader.java:30]`. A from-scratch Go parser **MUST** implement delimiter discovery itself (HAPI is unavailable in Go).

### 5.3 Segmentation & line-ending tolerance

- The spec segment terminator is **carriage return `\r` (0x0D) only** — not LF, not CRLF `[HL7v2-Ch2]`.
- **MUST** normalize real-world `\n` and `\r\n` to a single internal break before segmentation (senders are inconsistent). The HPRIM reader already splits on `\r?\n` `[ref:HPRIMDataReader.java:45]`; the current fhir-map HL7v2 parser normalizes CRLF/CR→`\n` `[fhir-map: parse.go:133]`.
- **MUST** skip blank lines and trailing non-printable bytes.

### 5.4 Structured field model

Model every segment as the full four-level hierarchy, **always** (even single occurrences), so paths and round-tripping are lossless:

```
message: ordered []Segment            // preserves received order
Segment: { ID string; Fields []Field }
Field:   []Repetition                 // length 1 when not repeated
Repetition: []Component
Component:  []Subcomponent (string)
```

This mirrors the builder's 4-deep nesting `[ref:HL7v2Builder.java:26]` and the repetition rule in §5.7. The current fhir-map parser flattens to `SEG-N` / `SEG-N-C` / `SEG-N-C-S` string keys and exposes repeating segments as a list-of-rows `[fhir-map: parse.go:161]` — acceptable as a *navigation surface* but it **MUST** retain repetition arrays at the field level (it currently does **not** model `~` repetition — see §13).

### 5.5 Three-state field model — **MUST** preserve

A field/component between delimiters is one of three distinct states `[HL7v2-Ch2]`:

| State | Wire form | Meaning | Internal repr (suggested) |
|---|---|---|---|
| **Populated** | `value` | a value | `*string` non-empty |
| **Absent / not present** | (nothing between delimiters) | "leave unchanged" | absent / `nil` |
| **Explicit null** | `""` (two double-quotes) | "set to null / delete" | a distinct `Null` marker |

**MUST NOT** collapse *absent* and *explicit-null* into the same value. In FHIR mapping, explicit-null commonly becomes a `data-absent-reason` extension, not an omitted element.
**MUST** treat an *absent* field as having no value for `.exists()` semantics — the current fhir-map parser already emits no key for empty fields `[fhir-map: parse.go:108]`. Extend this to also recognize and flag `""`.

### 5.6 Escape sequences — **MUST** unescape correctly

Data fields may contain escape sequences delimited by the escape char (default `\`). Rules `[HL7v2-Ch2]`:

| Sequence | Meaning |
|---|---|
| `\F\` `\S\` `\T\` `\R\` `\E\` | literal field / component / subcomponent / repetition / escape character |
| `\Xdddd…\` | hex: each pair of hex digits = one 8-bit byte |
| `\Cxxyy\` / `\Mxxyyzz\` | single-/multi-byte charset switch (zz optional) |
| `\.br\` `\.sp\` `\.fi\` `\.nf\` `\.in±n\` `\.ti±n\` `\.ce\` `\.sk n\` `\H\` `\N\` | formatting (valid in FT/TX/CF text fields) |
| `\Zdddd…\` | locally-defined — **SHOULD** preserve, not discard |

**MUST**:
- Unescape **with a left-to-right scanner**, never search-and-replace — escape sequences cannot nest, and a naive replace of `\E\`→`\` corrupts the rest `[HL7v2-Ch2]`.
- Unescape delimiter and hex escapes into raw text for data fields.
- Map `\.br\` → newline for FHIR `string` targets; **MAY** strip other formatting escapes; **SHOULD** preserve unknown `\Z..\`.
- Unescaping happens at **leaf-value access** (after splitting on delimiters), not before segmentation.

The current fhir-map parser does **no** unescaping — this is a correctness gap (`\F\`, `\S\`, `\T\`, `\X0D\` etc. pass through verbatim).

### 5.7 Repetition fidelity — **MUST**

- The repetition separator `~` applies to **fields only** (not components/subcomponents) `[HL7v2-Ch2]`.
- Every field **MUST** be modeled as a list of repetitions (length 1 when non-repeating) so `~`-separated values round-trip with no loss.
- Repeated *segments* (multiple `OBX`, `NK1`, `AL1`, `OBR`) **MUST** each be retained as distinct occurrences and be independently iterable. *(This is the exact site of the confirmed fhir-map "segment collapse" defect — see §13.)*

### 5.8 Segment groups & message structure

Provide **two navigation modes**, like HAPI:

1. **Generic / flat (MUST):** a list of segments, each navigable by `SEG[-field[-comp[-sub]]]`, never failing on unknown segments. This is the baseline and is sufficient for hand-written StructureMaps.
2. **Grouped / structured (SHOULD):** apply a message's abstract syntax (by `MSH-9` type + `MSH-12` version) to nest segment groups, so a path like `PATIENT_RESULT.ORDER_OBSERVATION[0].OBX` resolves real groups. ORU_R01 is the canonical lab case:

   ```
   MSH
   { PATIENT_RESULT:
       [ PID [PD1] {NTE} [ {NK1} ] [ PV1 [PV2] ] ]
       { ORDER_OBSERVATION: [ORC] OBR {NTE} { [OBX] {NTE} } … } }
   ```

The reference relies on HAPI for grouping `[ref:Mapper.java:950 findSegments]`, plus a **fallback** that, if group-navigation finds nothing, scans the whole message tree for any segment whose **normalized name** matches `[ref:Mapper.java:1005 scanGroupsRecursively]`. A Go port that offers only the flat model **MUST** document that group-qualified paths degrade to a flat segment-name match (which is what the reference's fallback does anyway). Segment-name **normalization** (case-insensitive; trailing digits trimmed off numbered/Z segments, e.g. `ZBR1`→`ZBR`) **SHOULD** be replicated `[ref:Mapper.java:917 normalizeSegmentName]`.

### 5.9 Versions, backward compatibility, Z-segments

- **SHOULD** read `MSH-12` (version) and `MSH-9` (message type) to drive the structured model; the flat model is version-independent.
- **MUST** accept newer/older minor versions gracefully — extra trailing fields/repetitions are ignored, not errors (v2 is back-compatible by design).
- **MUST** accept arbitrary `Z`-segments (local/custom) in the flat model without error `[HL7v2-Ch2]`.

### 5.10 Error model — strict vs lenient

- **Parse failures** (no MSH, undiscoverable delimiters, truncated message) are **fatal** — abort the transform with a precise error naming the segment/offset.
- **Navigation/extraction** is **best-effort**: a path that addresses a missing segment/field/component **MUST** yield zero items, **not** an error — matching the reference, which logs and continues on out-of-bounds access `[ref:Mapper.java:907,910]`.
- **SHOULD** offer a configurable strict mode (per trading partner) that escalates cardinality/structure violations from warning→error, modeled on HAPI's `ParserConfiguration`.
- **MUST** classify issues as warning / error / fatal and never silently truncate — log dropped content.

---

## 6. HPRIM parser specification

> HPRIM is **not** HL7v2. It is built on **ASTM E1238**, maintained by Interop'Santé, and used in France for **laboratory/biology results** `[HPRIM-2.5]`. It superficially resembles ER7 (pipe-delimited) but has its own segment vocabulary and — critically — a **different delimiter order**.

### 6.1 Segment vocabulary

| Seg | Role |
|---|---|
| **H** | header (sender/receiver/message id); **exactly one, first** — analogous to MSH |
| **P** | patient |
| **OBR** | analysis / radiology request |
| **OBX** | result of a test |
| **C** | comment on the preceding segment (insertable at any level) |
| **A** | addendum / continuation of the preceding segment (used past the 220-char limit) |
| **L** | trailer; **exactly one, last** |
| `FAC` `ACT` `REG` `AP` `AC` `ERR` | HPRIM-specific (invoicing, insurance coverage, errors) |

Structure is strictly hierarchical (`H → {P → {OBR → {OBX}}} → L`); `C`/`A` insertable anywhere `[HPRIM-2.5 §A]`.

### 6.2 Delimiter discovery from `H` — **the dangerous divergence (MUST)**

Per `[HPRIM-2.5 §7.2]`, *the 5 ASCII characters immediately after the `H`* define the separators, in **this order**:

| Position after `H` | Role (HPRIM) | Conventional char |
|---|---|---|
| 1st | **field** separator | `\|` |
| 2nd | **subfield** (sous-champ) separator | `~` |
| 3rd | **repeater** (répétiteur) | `^` |
| 4th | **escape** | `\` |
| 5th | **subsubfield** separator | `&` |

⚠️ **This is the trap.** A typical HPRIM header is `H|~^\&…`. The byte string `|~^\&` *looks* like HL7v2's `|^~\&` but the **2nd and 3rd characters are swapped in meaning**: in HPRIM `~`=subfield and `^`=repeater; in HL7v2 `^`=component and `~`=repetition. A parser that assumes HL7v2 semantics will mis-split every HPRIM field.

**MUST**: discover HPRIM delimiters from the `H` segment using **HPRIM position semantics**, never hardcode, and never reuse the HL7v2 delimiter assignment.

> Both existing implementations got this wrong: the reference `HPRIMDataReader` hardcoded a split on `|` then `^` `[ref:HPRIMDataReader.java:52,58]` (treating `^` as the component/subfield split, ignoring the H-declared order and `~`); fhir-map's adapter likewise hardcoded `^`/`&`. A *mature* Go parser **MUST** fix this — read the actual `H` separators. (fhir-map now does — see §13.)

### 6.3 Length limit & continuation

- Segments are capped at **220 characters** including the terminator; overflow continues in an **`A`** segment `[HPRIM-2.5 §B.1]`. **SHOULD** transparently re-join `A` continuations onto the preceding segment.

### 6.4 Field model

- Terminator = **CR**; non-printable chars after CR (below SP) ignored — tolerate CRLF/LFCR `[HPRIM-2.5 §B.1]`.
- Repeaters apply to **fields only** (same principle as HL7v2).
- Trailing empty fields after the last populated field are omitted; preserve interior empties positionally (use a keep-empties split, as the reference does with limit `-1` `[ref:HPRIMDataReader.java:52]`).
- Map HPRIM `H/P/OBR/OBX` → the same FHIR targets as HL7v2 (P→Patient, OBR→DiagnosticReport/ServiceRequest, OBX→Observation), **but ASTM datatypes differ from HL7v2 datatypes**, so field-level mapping tables are **not** interchangeable — keep a separate HPRIM mapping dictionary.

### 6.5 Model & navigation parity

Expose the HPRIM model through the same `Reader`/`Model` interface (§4): `segmentName → []occurrence → []field → []component`. Insertion order and per-name grouping **MUST** be preserved (`LinkedHashMap` equivalent) `[ref:HPRIMMessage.java:10]`.

---

## 7. Path navigation grammar (read side)

Two path dialects exist in the reference. A Go port **MUST** support both grammars and replicate their (differing) index bases exactly, because StructureMaps in the wild are authored against them.

### 7.1 HL7v2 `Path` `[ref:model/Path.java]`

Grammar:
```
[ Group([rep])? . ]…  SEG ([rep])?  ( - field ([rep])? ( - comp ( - sub )? )? )?
e.g.  OBR-4-1            PATIENT_RESULT.ORDER_OBSERVATION[0].OBX-5-1[2]
```
- Dotted **group prefix**, each optionally `[rep]`.
- `field` and `fieldRepetition` are kept **1-based** (passed straight to the segment field accessor).
- `component` and `subComponent` are **decremented to 0-based** `[ref:Path.java:66]`.
- No `field` ⇒ the matched segments themselves are the items `[ref:Mapper.java:860]`.

### 7.2 `HPRIMPath` `[ref:model/HPRIMPath.java]`

Grammar `SEG[idx]-FIELD[rep]-COMP-SUB`:
- `segmentIndex` and `fieldRepetition` default to **0** (0-based).
- `field`, `component`, `subComponent` are all **decremented to 0-based** `[ref:HPRIMPath.java:30-33]`.
- `hasExplicitComponent` = the path literally contains `-d+-d+`. This distinguishes `OBX-5` (whole field, components re-joined with the component sep) from `OBX-5-1` (just component 1) `[ref:HPRIMPath.java:34]`.
- **Quirk to replicate:** when there is no explicit component, `fieldRepetition` is reused as an index into the field's component array `[ref:Mapper.java:1100-1104]`.

**MUST** document and unit-test the three different index conventions (HL7v2 `Path`: field 1-based, comp/sub 0-based; `HPRIMPath`: all 0-based; builder: field 0-based, comp/sub 1-based with `+` append — §8). They are not unifiable without breaking existing maps.

---

## 8. HL7v2 as a **target** (builder)

For the HPRIM→HL7v2 direction, a builder assembles ER7 output `[ref:model/HL7v2Builder.java]`.

### 8.1 Put-by-path grammar
```
SEG([idx|+])? - FIELD ([rep|+])? ( - COMP[+]? ( - SUB[+]? )? )? (.value)?
```
- `+` token = **append a new** occurrence at that level (`-1` → current size) `[ref:HL7v2Builder.java:140-149]`.
- `field` index 0-based; component/sub default to first / appended; subcomponents stored by split/rejoin on the subcomponent sep `[ref:HL7v2Builder.java:65-78]`.
- A **whole-segment copy** path (e.g. just `"OBX"`) bound to an `HPRIMSegment` copies its field/component arrays verbatim `[ref:Mapper.java:1311]`.

### 8.2 Builder fidelity requirements

The reference builder has two **quirks** a mature implementation **SHOULD fix** (and **MUST** at least document):

1. **Segment ordering:** it emits segments grouped by segment-name in first-occurrence order, **not** original message order `[ref:HL7v2Builder.java:106]`. A faithful builder **SHOULD** preserve original/clinically-correct segment order.
2. **No MSH / encoding header:** it emits raw `SEG|…\r` with `^`/`~`/`&` hardcoded and generates no `MSH-1`/`MSH-2` `[ref:HL7v2Builder.java:10-14]`. A mature builder **SHOULD** emit a proper MSH with declared encoding characters and **MUST** escape any data value containing a delimiter (inverse of §5.6).

---

## 9. `$transform` integration contract

- **Resolve the StructureMap** from `structureMap` (inline) or `source` canonical URL (via `structureMapEndpoint` remote client, else local store); >1 match is fatal `[ref:TransformProvider.java:69-108]`.
- **Imports:** recursively resolve `StructureMap.import[]` with cycle detection; merge contained resources (local wins) and groups by name + input-type signature; non-matching imported groups are **prepended** `[ref:Mapper.java:2327,2415]`.
- **`translate(coding, conceptMapUrl, fieldToReturn)`** (ConceptMap): if `services` (a `terminologyEndpoint`) is configured, delegate to a remote `ConceptMap/$translate` and read the `match → part(concept).valueCoding` `[ref:TransformerService.java:52]`. Otherwise resolve **contained / local** ConceptMaps only `[ref:Mapper.java:2032]`. The Go port **MUST** model this **two-mode** behavior — it is precisely why the `hapi-compare` Step-2 run 500'd on the unreachable contained map in single-instance mode.
- **Equivalence filter** for local translate: accept `EQUAL`/`RELATEDTO`/`EQUIVALENT`/`WIDER`; `UNMATCHED` short-circuits; multiple matches is an error `[ref:Mapper.java:2062]`.
- **Output:** every output variable is serialized (FHIR→JSON; builders→`toString()`) and base64-wrapped into a `Parameters.output` part with the type-appropriate content-type `[ref:Mapper.java:144,182,194]`.

---

## 10. FHIR mapping guidance (V2-to-FHIR IG)

The parser's job is to expose data faithfully; the *mapping* is StructureMap-driven, but the parser **MUST** expose everything the IG mappings need. Key IG facts to design against `[V2toFHIR]`:

- **Datatypes:** XPN→HumanName, CX→Identifier, XAD→Address, TS/DTM→dateTime, XTN→ContactPoint, CWE/CE/CNE→CodeableConcept, HD/EI→Identifier/Organization/… (context-dependent — same source, different FHIR target by usage).
- **Segments:** MSH→MessageHeader, PID→Patient (or RelatedPerson), PV1→Encounter, **OBR→ServiceRequest *and/or* DiagnosticReport** (order vs result context), OBX→Observation, NK1→RelatedPerson, AL1→AllergyIntolerance.
- **The load-bearing lab rule — OBX-2 selects `Observation.value[x]`:** `NM`→valueQuantity, `ST/FT/TX`→valueString, `CWE/CE/CNE/CF/IS`→valueCodeableConcept, `DR`→valuePeriod, `DTM/DT`→valueDateTime, `TM`→valueTime, `NR`→valueRange, `SN`→ratio/range/quantity/string, `NA`→valueSampledData. OBX-3→code, OBX-6→units, OBX-7→referenceRange, OBX-8→interpretation (vocab), OBX-11 `N`→data-absent-reason.
- **Vocabulary:** coded values translate through ConceptMaps (the IG "Vocabulary Mapping" column); an empty mapping ⇒ user-defined table ⇒ pass through code+system or expose a config hook.

> The `hapi-compare` Step-2 finding that fhir-map produced "0 Observations with a value or LOINC code" traces directly to **not applying the OBX-2→value[x] rule and not invoking the ConceptMap translate** — both are mapping-layer obligations the parser must *enable* by exposing OBX-2, OBX-3, OBX-5, OBX-6 cleanly with repetition fidelity.

---

## 11. Robustness requirements (real-world pitfalls)

A mature parser **MUST** handle, and have tests for, all of:

1. **Line endings** — CR / LF / CRLF / mixed; strip BOM and MLLP framing (§5.3).
2. **Never hardcode delimiters** — discover from MSH (HL7v2) / H (HPRIM) every time (§5.2, §6.2).
3. **Cascading boundary errors** — one stray delimiter shifts all following boundaries; parse defensively and report the offending offset.
4. **Charset / MSH-18** — decode with declared charset; default ASCII/UTF-8 (§5.1).
5. **Segment reordering / orphans** — preserve received order in the flat model; in the structured pass attach orphans to the nearest parent and warn, don't fail.
6. **Variable-precision dates (TS/DTM)** — `YYYY[MM[DD[HH[MM[SS[.S]]]]]][±ZZZZ]`; choose `date` vs `dateTime`, preserve timezone, route unparseable values to `data-absent-reason` instead of dropping the resource.
7. **Non-conformant phone / numeric values** — free-text XTN, numeric OBX-5 with `<`/`>`/`+`/commas/units; coerce with `valueString` fallback. *(Note: HAPI 8.8.0's strict typed validation rejects exactly these — see the `hapi-compare` hl7eu-head 500. A lenient default is preferable for ingestion; strictness should be opt-in.)*
8. **Repetition & repeated-segment fidelity** (§5.7) — the #1 silent data-loss bug.
9. **Three-state null** (§5.5).
10. **Performance** — ORU can carry thousands of OBX and large `ED`/base64 payloads. Single-pass, allocation-light tokenization; lazy unescape; stream segments; cap message/field/recursion sizes (reuse the existing bounded-`$transform` limits).

---

## 12. Conformance & test requirements

A Go implementation **MUST** ship:

1. **Round-trip tests** — parse → navigate → build → re-parse, asserting no segment/field/repetition loss (directly targets the segment-collapse defect).
2. **Delimiter-discovery tests** — non-default MSH-2 (e.g. `MSH|#@/\&|…`) and HPRIM `H|~^\&|…`, asserting the *swapped* HPRIM semantics.
3. **Escape unescape tests** — `\F\ \S\ \T\ \R\ \E\`, `\X0D\`, `\.br\`, unknown `\Z..\`.
4. **Three-state tests** — populated vs absent vs `""`.
5. **Multi-occurrence tests** — 10 OBR / 18 OBX preserved (the exact `hapi-compare` `exemple1` fixture — reuse it as a golden corpus).
6. **OBX-2→value[x] mapping tests** — NM/ST/CWE/DTM at minimum, plus a LOINC ConceptMap translate.
7. **Differential corpus** — run the `hapi-compare` oracle (a recorded HAPI output bundle) and assert parity with the reference HAPI output for the segments fhir-map currently drops.

The existing `internal/transform/hl7v2/{parse_test.go,component_test.go}` cover components/subcomponents/empty-as-absent/multi-segment-list — extend them per the above.

---

## 13. Gap analysis — current fhir-map adapter vs this spec

This spec started life with an aspirational gap table against a minimal positional tokenizer.
The high-value requirements are now implemented; what follows is the **current** state (verify
against HEAD).

**Done — implemented and locked by tests** (`golden_lab_test.go` produces 5 Observations with
`valueQuantity` + LOINC codes, 1 Patient, 1 DiagnosticReport, all carrying `resourceType`;
`hprim_test.go` locks the HPRIM path):

- **Header-driven delimiter discovery** (MSH and HPRIM `H`) — the conventional `^~\&` set stays
  byte-identical (§5.2).
- **Escape unescaping** `\F\ \S\ \T\ \R\ \E\`, `\Xdddd\`, `\.br\`; `\Z..\` preserved; encoding
  field exempt (§5.6).
- **Field `~` repetition** modeled as a list (scalar when single-rep) (§5.7).
- **Repeated-segment fidelity** — multiple OBR/OBX preserved (previously collapsed to the last) (§5.7).
- **Three-state null** (`""` vs absent) (§5.5).
- **HPRIM delimiter-order divergence** — roles assigned by encoding-char *position*, so `H|~^\&`
  parses with subfield `~` / repeater `^` (§6.2).
- **OBX-2 → `value[x]`** selection and **ConceptMap `translate`** applied; output carries
  `resourceType` (§10 + builder).
- Best-effort navigation — no error on a missing path (§4).

**Open — lower-acuity, tracked in [`../../ROADMAP.md`](../../ROADMAP.md):**

- **MSH +1 field offset** — deferred *by design*: `MSH-1` = encoding chars is what every existing
  map is authored against; the standard +1 offset is a breaking change, gated behind a future
  `WithMSHFieldOffset` opt-in (§5.2).
- **Charset / MSH-18** — UTF-8 assumed (§5.1).
- **Segment groups (ORU_R01 tree)** — flat segment access today (§5.8).
- **HPRIM `A`-continuation / 220-char** (§6.3).

---

## 14. Implementation notes for Go

- **Package layout:** `internal/transform/hl7v2` (ER7 parser + builder), new `internal/transform/hprim` (HPRIM parser sharing a generic delimited-record core), and a shared `Reader`/`Model` interface in `internal/transform` (§4).
- **Delimiter set** is a small value type discovered per message; thread it through tokenization — do **not** use package-level constants for `^~\&`.
- **Model:** `[]Segment` with `Fields [][]Repetition…`; keep a name→occurrences index for flat navigation and (optionally) a group tree for structured navigation.
- **Unescape** is a method on the leaf accessor, lazy, scanner-based (§5.6).
- **Strictness** via functional options (`WithStrict()`, `WithCharset()`, `WithHPRIMDelimitersFromHeader()`), mirroring HAPI's `ParserConfiguration`/`HapiContext` DI.
- **Bounded execution:** apply the existing `$transform` recursion/size/timeout limits to the parser front end (§11.10).
- **Golden corpus:** vendor the `hapi-compare` fixtures (`exemple1` 10 OBR/18 OBX, `sim2`) and the `oracle/` HAPI output as test data.

---

## Appendix A — source map

- Reference HAPI impl (`feature/mapping`): `service/{HL7v2DataReader,HPRIMDataReader,Mapper,TransformerService,CSVDataReader,XMLDataReader,JSONDataReader,FFHIRPathHostServices}.java`, `provider/TransformProvider.java`, `model/{HL7v2Builder,HPRIMMessage,HPRIMSegment,HPRIMPath,Path,…}.java`.
- Current Go adapter: `internal/transform/hl7v2/parse.go` (+ tests).
- Differential harness & oracle: `~/code/hapi-compare/` (`compare.py`, `RESULTS.md`, `oracle/`, `out/`).
- Standards & comparators: see §1.
