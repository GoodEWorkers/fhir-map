# Gap: FHIRPath/FML decimal literals + arbitrary-precision numeric model

**Status:** tracked, not yet implemented. Not on the critical path for the current HL7v2 lab map
corpus (0 `valueDecimal` params and 0 decimal literals in any `condition`/`element`/`check`), but it
is a real conformance + correctness gap vs reference HAPI mapping engines.

## What the spec/FML say
- FHIRPath has two numeric types: `Integer` and **`Decimal`**. Decimal literals are written with a `.`
  (`3.3`, `0.05`); FML target-transform parameters serialize as `valueInteger` / **`valueDecimal`** / etc.
- FHIR/FHIRPath mandate **arbitrary-precision decimal semantics — not IEEE binary float** — so clinical
  values (e.g. `0.88 mmol/L`) never pick up binary-float drift (`0.1 + 0.2 = 0.30000000004`).

## How reference HAPI engines do it
HAPI's FHIRPath engine + `StructureMapUtilities` parse decimal literals into `DecimalType` backed by
`BigDecimal` (arbitrary precision) and serialize them without float artifacts. HAPI-based servers
(`hapi-fhir-jpaserver-starter`, Matchbox) inherit this — full decimal support **with** precision.

## fhir-map current state (two distinct issues)
1. **Decimal literals in expressions** — the lexer (`internal/transform/fhirpath/lexer.go`) emits only
   `tInteger`; an expression like `value > 3.3` mis-lexes as `3 . 3`. Conformance gap, but unused by the
   current corpus.
2. **Numeric precision model** — numbers are represented as Go **`float64`** throughout
   (`internal/transform/fhirpath/eval.go`: `numericFloat`, `compareFloats`; and
   `structuremap.Parameter.ValueDecimal *float64`). IEEE binary float can lose precision on decimals —
   the more important latent risk for lab values, exactly what BigDecimal avoids.

## Why deferred
- Not required by the gaps currently being closed (the lab golden corpus uses none).
- Doing it *right* is not a one-line lexer tweak: it requires preserving the literal verbatim (not eager
  `float64`), Integer-vs-Decimal type semantics across `=` / `<` / arithmetic, and float-free
  serialization — i.e. confronting the `float64` model. A half version would imply decimal support that
  doesn't actually preserve precision.

## Implementation notes (when picked up)
- Add a `tDecimal` lexer token that captures the literal **text**; a `nodeDecimal` that carries the
  verbatim string, not an eager float.
- Replace the `float64` numeric core with an arbitrary-precision decimal (e.g. `math/big.Rat`/`big.Float`
  or a decimal lib such as `cockroachdb/apd`) — **new dependency = CTO-level decision**.
- Ensure JSON output emits decimals without binary-float rounding (e.g. `5.6`, not `5.5999999`).
- Add tests: decimal literal eval, `Integer`↔`Decimal` comparison/arithmetic, and a round-trip that proves
  no precision loss (`0.1 + 0.2 == 0.3`).
