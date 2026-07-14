# fhir-map

[![CI](https://img.shields.io/github/actions/workflow/status/GoodEWorkers/fhir-map/ci.yml?branch=main&label=CI&logo=githubactions&logoColor=white)](https://github.com/GoodEWorkers/fhir-map/actions/workflows/ci.yml)
[![License: GPL-3.0](https://img.shields.io/github/license/GoodEWorkers/fhir-map?color=blue)](LICENSE)
[![Latest release](https://img.shields.io/github/v/release/GoodEWorkers/fhir-map?include_prereleases&sort=semver&logo=github)](https://github.com/GoodEWorkers/fhir-map/releases)
[![Coverage](https://img.shields.io/endpoint?url=https%3A%2F%2Fraw.githubusercontent.com%2FGoodEWorkers%2Ffhir-map%2Fmain%2Fdocs%2Fbadges%2Fcoverage.json)](https://github.com/GoodEWorkers/fhir-map/actions/workflows/ci.yml)
[![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/GoodEWorkers/fhir-map/badge)](https://securityscorecards.dev/viewer/?uri=github.com/GoodEWorkers/fhir-map)
[![FHIR R5 + R4](https://img.shields.io/badge/FHIR-R5%20%2B%20R4-orange?logo=hl7&logoColor=white)](#conformance--benchmarks)
[![HL7 Validator](https://img.shields.io/endpoint?url=https%3A%2F%2Fraw.githubusercontent.com%2FGoodEWorkers%2Ffhir-map%2Fmain%2Fdocs%2Fbadges%2Fconformance-e2e.json)](https://github.com/GoodEWorkers/fhir-map/actions/workflows/ci.yml)

**Production-grade FHIR terminology and transformation server for healthcare data pipelines.**

Bridge terminology systems, transform clinical messages, and translate codes across any FHIR-compliant workflow — with full R4/R5 support, sub-millisecond lookups, and the reliability to handle clinical traffic at scale.

---

## Why fhir-map

Healthcare data integration is hard. EHRs speak different dialects. Lab systems use LOINC, billing uses ICD-10, pharmacies use RxNorm. Every interface engine, every integration, every migration requires mapping — and mapping at scale requires infrastructure that most teams build from scratch, badly, and maintain forever.

fhir-map is that infrastructure. It handles **code translation**, **concept mapping**, and **structural transformation** as first-class HTTP operations — FHIR-native, auditable, and fast enough to sit inline in clinical workflows.

---

## What it does

**Terminology translation** — translate a code from one system to another in a single HTTP call. ICD-10 → SNOMED CT, HL7v2 → FHIR, local codes → standard vocabularies. Batch translate thousands of codes in one round-trip.

**Structural transformation** — convert clinical messages between formats using FHIR Mapping Language (FML). HL7v2 → FHIR R4, FHIR R4 → R5, QuestionnaireResponse → Patient — any mapping expressible in the FHIR spec.

**Dual-version API** — serve R4 and R5 clients from the same server, same data, same mappings. No forking, no version sprawl.

---

## Scope

fhir-map is a **transformation and terminology component** — a deterministic `$transform` / `$translate` service plus a versioned map store. It is built to sit *inside* a data pipeline, not to be the pipeline.

**In scope:** ConceptMap `$translate` (+ batch), StructureMap `$transform` (FML), CRUD + versioned history for ConceptMap / StructureMap / StructureDefinition, parallel R4 + R5 wire formats, and built-in HL7v2 / HPRIM message parsing.

**Out of scope — the embedding pipeline's responsibility:** message ingestion (MLLP / file / queue), orchestration, scheduling, retry / dead-letter, backpressure, and data lineage. Terminology *binding* validation (checking coded values against value sets via a `tx` server) is opt-in and off by default. See [`ROADMAP.md`](ROADMAP.md) for what is deliberately deferred and the demand signals that would pull it forward.

---

## Designed for Healthcare Enterprises

| Requirement | How fhir-map addresses it |
|---|---|
| **FHIR R4 + R5 compliance** | Parallel `/fhir/R4/` and `/fhir/R5/` URL trees; canonical R5 storage with R4 projection at the wire boundary |
| **Clinical pipeline latency** | Sub-millisecond `$translate` lookups via indexed flat table — safe to call inline in order processing, ADT, or lab result workflows |
| **Bulk ETL throughput** | `$translate-batch` collapses N code translations into a single SQL round-trip; ingest 100k mappings in ~1.5 s |
| **HL7v2 interoperability** | Native HL7v2 message parser built into the `$transform` pipeline — no external adapter needed |
| **Audit trail** | Soft-delete, versioned history, `_history` and `vread` endpoints on every resource |
| **Optimistic concurrency** | `If-Match: W/"N"` ETag locking prevents silent overwrites in multi-system environments |

---

## Performance

Benchmarked against a **1 vCPU / 1 GB RAM** container — the smallest available tier on AWS, GCP, or Azure. No tuning, no caching layer, no CDN.

### Steady-state clinical load

> 80 simultaneous users · 326 req/s · 3-minute run

| Endpoint | Median | p95 |
|---|---|---|
| Code translation (`$translate`) | 1 ms | **4 ms** |
| Batch translation (`$translate-batch`, 50 codes) | 2 ms | **6 ms** |
| Record read | 567 µs | 2.5 ms |
| Search | 1.8 ms | 5.5 ms |
| Health check | 239 µs | 1.5 ms |

**0 errors. 0 crashes. 12 MB RAM used.**

### Saturation test

> 5,000 simultaneous users · 1.9 million requests · 2-minute ramp

| | |
|---|---|
| Peak throughput | **13,654 req/s** |
| p95 latency | 508 ms |
| Error rate | **0.00%** |
| Peak RAM (1 GB hard limit, no swap) | **326 MB — 32% of limit** |
| OOM-killed | **No** |
| Server crashed | **No** |

RAM grew linearly with load and recovered cleanly on ramp-down. No memory leak. The server remained alive and correct at 60× typical clinical load on minimal hardware.

> **Bottleneck at scale is the DB connection pool, not the server.** With `DB_MAX_CONNS=25` and a second core, throughput scales to ~40,000 req/s before memory becomes a factor.

[Run the load tests yourself →](#load-testing)

---

## Conformance & Benchmarks

fhir-map's spec compliance and performance are continuously published from CI.

- **FHIR conformance results** — every GitHub Release runs the Bruno conformance suite (`bruno/` + `bruno-mapping-coverage/`) and a black-box FHIR E2E harness whose captured response bodies are validated by the official HL7 FHIR Validator CLI at R4 (4.0.1) and R5 (5.0.0). Results are attached as 90-day artifacts on each release run — open the [Actions tab](../../actions) and the matching release run. Covers FR-15 (FHIR conformance suite).
- **Performance benchmarks** — every release commits an in-memory benchmark report under [`docs/benchmarks/`](docs/benchmarks/) (`docs/benchmarks/<tag>.md`) with p50/p95/p99 ns/op for `BenchmarkTranslateForward` and `BenchmarkBatch_50kProbes_100kConceptMap` (`go test -bench`, `-benchtime=10s -count=3`). The release job fails before publishing the image if the new `$translate-batch` p99 regresses >20% against the last published file. Covers FR-17 (benchmark publication + regression gate).
- **FHIR R5 TestScript spec files** — [`testdata/testscripts/`](testdata/testscripts/) ships seven R5 `TestScript` resources documenting the canonical translate / metadata flows in machine-readable form for matchbox / Touchstone consumers.

> HTTP-level latency under sustained user load is covered separately by the k6 load test → [Load Testing](#load-testing). The release benchmarks above are engine hot-path measurements (no DB, no HTTP).

---

## Quick Start

```sh
# Requires: Go ≥ 1.25, Docker

# Start PostgreSQL
docker compose up -d postgres

# Build, migrate, and start the server
make dev
```

Server listens on `:8080`.

```sh
# Verify it's running
curl localhost:8080/health
# → {"status":"healthy","timestamp":"..."}

# Translate a code
curl -X POST localhost:8080/fhir/ConceptMap/\$translate \
  -H 'Content-Type: application/fhir+json' \
  -d '{
    "resourceType": "Parameters",
    "parameter": [
      {"name": "url",          "valueUri":  "http://example.org/my-map"},
      {"name": "sourceCode",   "valueCode": "J18.9"},
      {"name": "sourceSystem", "valueUri":  "http://hl7.org/fhir/sid/icd-10"}
    ]
  }'
```

---

## API

```
# Terminology
POST   /fhir/{version}/ConceptMap/$translate         Translate a single code
POST   /fhir/{version}/ConceptMap/$translate-batch   Bulk translate (N codes, 1 round-trip)
GET    /fhir/{version}/ConceptMap/{id}/$translate    Instance-level translate

# Structural transformation
POST   /fhir/{version}/StructureMap/$transform       Transform a resource or message
POST   /fhir/{version}/StructureMap/{id}/$transform  Instance-level transform

# Resource management
POST   /fhir/{version}/ConceptMap                    Create / ingest
GET    /fhir/{version}/ConceptMap/{id}               Read
PUT    /fhir/{version}/ConceptMap/{id}               Update (If-Match)
DELETE /fhir/{version}/ConceptMap/{id}               Soft-delete
GET    /fhir/{version}/ConceptMap                    Search
GET    /fhir/{version}/ConceptMap/{id}/_history      Audit history
GET    /fhir/{version}/metadata                      CapabilityStatement
GET    /health                                       Liveness probe
```

`{version}` is `R4`, `R5`, or omitted (defaults to R5). All routes exist under all three prefixes.

---

## Key Capabilities

### Code Translation

`$translate` maps a source code to one or more target codes using a stored ConceptMap.

- **Forward and reverse** — translate in either direction
- **CodeableConcept input** — pass multiple codings, get a result per coding
- **dependsOn filtering** — context-sensitive translation (e.g. filter by gender, age bracket, or clinical setting)
- **Unmapped fallback strategies** — `fixed` code, `use-source-code`, or chain to another ConceptMap via `other-map`
- **Version pinning** — request a specific ConceptMap version explicitly

### Batch Translation for ETL

`$translate-batch` accepts a Parameters body with N code probes and resolves them all in a single indexed SQL query — no N+1 problem, no connection overhead per code.

```json
{
  "resourceType": "Parameters",
  "parameter": [
    {"name": "url", "valueUri": "http://example.org/icd10-to-snomed"},
    {"name": "code", "part": [{"name": "code", "valueCode": "J18.9"}, {"name": "system", "valueUri": "..."}]},
    {"name": "code", "part": [{"name": "code", "valueCode": "E11.9"}, {"name": "system", "valueUri": "..."}]}
  ]
}
```

### Structural Transformation

`$transform` executes FHIR Mapping Language (FML) scripts against any FHIR resource or HL7v2 message. It implements a broad subset of FML — enough to drive the flagship HL7v2/HPRIM → FHIR path end-to-end:

- All transform codes (`copy`, `cast`, `create`, `truncate`, `evaluate`, `cc`, `id`, `qty`, `translate`, ...)
- Dependent group calls, nested rules, multi-source groups
- `extends`, list modes (`first`, `last`, `share`, `collate`), `where` clauses
- Variable scoping, `check` expressions
- HL7v2 message parsing built in — transform ADT, ORU, ORM messages directly

A `StructureDefinition`-driven type system (polymorphic `value[x]` dispatch, `<<types>>` default groups, typed `create`) is **not** yet implemented, so arbitrary FHIR↔FHIR structural maps that rely on it aren't fully supported. See the [engine completeness reference](docs/specs/fhir-mapping-language-completeness.md) for the exact implemented-vs-pending matrix.

### Dual FHIR Version Support

One server, two clients. R4 and R5 differ in vocabulary (`equivalence` vs `relationship`), parameter names, and a handful of field names. fhir-map handles the projection at the HTTP boundary:

- R5 is the canonical internal representation
- R4 wire format is applied on the way out for `/fhir/R4/` requests
- Both versions share the same ConceptMaps, StructureMaps, and storage

---

## Configuration

```sh
SERVER_PORT=8080          # HTTP listen port
SERVER_TRANSFORM_TIMEOUT=15s  # Per-request $transform execution budget (Go duration; default 15s, keep < SERVER_WRITE_TIMEOUT)
SERVER_TRANSFORM_STRICT=false   # Fail-loud strict transform mode: coercion/unmapped-translate failures return 422 (default lenient/best-effort)
SERVER_TRANSFORM_VALIDATE_OUTPUT=off  # Output-validation gate: off (default) | lenient (validate + flag via Warning header) | strict (reject invalid output as 422)
SERVER_MAX_BODY_BYTES=10485760  # Global request body cap in bytes (default 10 MiB; floored at 4 KiB)
DB_HOST=localhost         # PostgreSQL host
DB_PORT=5432
DB_USER=fhir
DB_PASSWORD=...           # Never logged — compliant with SYM-GR-0013
DB_NAME=fhir
DB_SSL_MODE=disable       # Set to 'require' or 'verify-full' for production
DB_MAX_CONNS=25           # Connection pool size — tune for your core count
DB_MIN_CONNS=5
GOMEMLIMIT=939524096      # Go runtime memory limit (bytes) — set to ~88% of container RAM
GOMAXPROCS=1              # Match to container CPU limit
MIGRATIONS_PATH=migrations # Path to SQL migrations (Docker) or internal/... (local)
```

---

## Load Testing

The repo ships with a k6 test suite that replicates both steady-state clinical traffic and RAM-exhaustion stress patterns.

```sh
# SLO-gated load test (enforces p95 latency thresholds, exits non-zero on failure)
make load-test

# RAM saturation test (ramps to 5,000 users — find the breaking point)
k6 run -e BASE_URL=http://localhost:8080 loadtest/saturation_test.js

# Tear down
make load-test-down
```

See [`loadtest/README.md`](loadtest/README.md) for the full methodology, result interpretation, and troubleshooting guide.

---

## Architecture

The hot path for `$translate` is a single indexed SQL lookup against a pre-normalised flat table (`concept_map_mappings`). There is no JSONB scanning, no in-memory ConceptMap hydration, and no cache to warm.

```
Ingest (PUT /ConceptMap)
  └─▶ dual-write: FHIR JSONB blob + flat mapping rows (pgx.CopyFrom)

$translate request
  └─▶ resolve ConceptMap → indexed query (source_system, source_code, cm_pk)
        └─▶ return match rows → emit Parameters response   ≈ 1–4 ms
```

| Design decision | Rationale |
|---|---|
| Flat normalised table | Compound B-tree index makes lookups O(log n) regardless of ConceptMap size |
| Dual-write | FHIR JSONB preserved for spec-compliance reads; flat rows power the hot path |
| Optimistic concurrency | `If-Match` ETags prevent lost updates in multi-system environments without global locking |
| R4/R5 projection at wire boundary | Single canonical storage; version complexity isolated to HTTP handlers |
| Depth-capped `other-map` recursion | Prevents infinite loops in chained ConceptMap configurations |
| Depth-capped `$transform` group recursion | Dependent group call chains are capped (`MaxGroupRecursionDepth`); exceeding it returns HTTP 422 `too-costly` rather than overflowing the stack. |
| `$transform` execution timeout | Each `$transform` is bounded by a wall-clock budget (`SERVER_TRANSFORM_TIMEOUT`, default 15s). The recursion cap limits call *depth*, but a crafted source resource can fan the cartesian source-product out by data *breadth* within that cap; the engine checks the deadline at each group/source-product node, so a runaway map is interrupted and returns HTTP 422 `too-costly` instead of pinning a worker for the full write timeout. Set to `0` to disable (then bounded only by `SERVER_WRITE_TIMEOUT`). |
| 10 MiB request body limit (global) | `MaxBodyBytesMiddleware` applies to **every** endpoint (ConceptMap, StructureMap, `$transform`, `$translate-batch`). Responses exceeding the limit receive HTTP 413 with a FHIR OperationOutcome `too-costly`. Raise the limit without recompiling via `SERVER_MAX_BODY_BYTES` (bytes). |

---

## Development

```sh
make dev            # Start postgres, migrate, run server
make test           # All tests (unit + integration, requires Docker)
make test-unit      # Unit tests only (no Docker)
make bench          # Benchmarks
make test-coverage  # Coverage report
make lint           # golangci-lint
```

Integration tests use `testcontainers-go` — no manual database setup required.

---

## Status & Roadmap

fhir-map is released (see [Releases](../../releases)) and load-tested, with the flagship HL7v2/HPRIM → FHIR path covered end-to-end. It is an MVP in the precise sense: the core component is production-shaped and shipping, while a number of larger capabilities are deliberately deferred until real demand justifies them.

- **What works today and what's next** — [`ROADMAP.md`](ROADMAP.md), with the demand signal that would pull each item forward.
- **Exact FML / `$transform` engine coverage** (implemented vs. pending) — [`docs/specs/fhir-mapping-language-completeness.md`](docs/specs/fhir-mapping-language-completeness.md).

---

## License

[GNU General Public License v3.0](LICENSE)
