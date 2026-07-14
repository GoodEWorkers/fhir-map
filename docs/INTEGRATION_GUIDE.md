# fhir-map Integration Guide

> Integrating the **fhir-map** ConceptMap `$translate` / `$transform` component into data-engineering workflows.

*Last synced:* fhir-map main (`go1.25`)

---

## Table of Contents

1.  [What is this service?](#what-is-this-service)
2.  [Run it locally (5 minutes)](#run-it-locally)
    *   Quick start with Docker Compose
    *   Verify the service is up
3.  [Core API Concepts](#core-api-concepts)
    *   FHIR R4 vs R5 wire format
    *   Key terminology: `system`, `code`, `relationship`, `$translate-batch`
4.  [Loading your ConceptMaps](#loading-conceptmaps)
    *   Loading a single ConceptMap
    *   Loading large/bulk ConceptMaps (ETL use case)
    *   Understanding `unmapped` strategies
5.  [Translating Concepts (for ETL)](#translating-concepts)
    *   Single-code lookup (`$translate`)
    *   **Batch translation (`$translate-batch`) — recommended for ETL**
6.  [ETL Integration Examples](#etl-integration-examples)
    *   Python (Airflow / pandas)
    *   Shell / cURL
    *   Go
    *   Handling errors gracefully
7.  [Reverse Translation](#reverse-translation)
8.  [Vocabulary Projection (R4 ↔ R5)](#vocabulary-projection)
9.  [Performance, Limits & Observability](#performance)
10. [Configuration Reference](#configuration-reference)
11. [Advanced: `dependsOn`, `product`, `unmapped` modes](#advanced-features)
12. [Troubleshooting](#troubleshooting)

---

<a id="what-is-this-service"></a>

## 1. What is this service?

This is a **fast, lightweight microservice** that implements the FHIR `ConceptMap` resource and the `$translate` operation. It serves as a **"terminology-brick"** in ETL pipelines.

**Problem it solves:**
In healthcare ETL pipelines, data engineers constantly need to translate codes from one coding system to another (e.g., ICD-10 to SNOMED CT, HL7 v2 to FHIR). Writing this logic manually in every pipeline is slow, error-prone, and hard to maintain.

**How it solves it:**
You store `ConceptMap` resources (which define mappings between code systems) in this service. Then, via a simple HTTP API, you translate codes in real-time or in batches. The service handles the heavy lifting: R4/R5 compatibility, reverse lookups, dependency filtering, and unmapped-code fallback strategies.

**Key Design Principles for ETL:**
*   **Speed:** Sub-millisecond single lookups; batch lookups in a single SQL roundtrip.
*   **Stateless (API):** Pure HTTP service—easy to scale horizontally.
*   **Bulk-friendly:** `$translate-batch` lets you look up hundreds of codes in one request.
*   **Audit-friendly:** Stores the *original* FHIR JSONB resource for perfect round-tripping.

---

<a id="run-it-locally"></a>

## 2. Run it locally (5 minutes)

### Prerequisites

*   **Go** 1.25+ (if running natively)
*   **Docker** & **Docker Compose**
*   `npx` (for the optional Bruno integration test)

### 2.1 Quick start with Docker Compose

The `docker-compose.yml` in the project root starts a PostgreSQL database and the server.

```bash
# Clone / navigate to the project
cd private/code/fhir-map

# Start everything (Postgres + server)
docker compose up -d

# Wait for the health check to pass
docker compose ps
```

*This starts:*
*   `postgres:16-alpine` on port `5432`
*   `fhir-map-server` on port `8080`

### 2.2 Verify the service is up

```bash
# 1. Health check
curl http://localhost:8080/health
# Expected: {"status":"healthy","timestamp":"..."}

# 2. View the FHIR CapabilityStatement (lists supported resources & operations)
curl http://localhost:8080/fhir/R5/metadata | jq .
```

---

<a id="core-api-concepts"></a>

## 3. Core API Concepts

### 3.1 FHIR R4 vs R5 Wire Format

The server exposes **three URL trees** with the same underlying storage:

| Prefix | FHIR Version | Notes |
| :--- | :--- | :--- |
| `/fhir` | **R5** | Backwards-compatible alias |
| `/fhir/R5` | **R5** | Explicit R5 wire format |
| `/fhir/R4` | **R4** | Uses `equivalence` instead of `relationship` |

**For ETL pipelines**, you usually want a stable URL. We recommend using the explicit version trees (`/fhir/R5` or `/fhir/R4`) so your client code is decoupled from any default changes.

### 3.2 Key Terminology

| Term | Description | Example |
| :--- | :--- | :--- |
| **`system`** | A URI identifying the code system (vocabulary) | `http://hl7.org/fhir/sid/icd-10` |
| **`code`** | The actual code value | `A00` |
| **`url`** | The canonical URL of the `ConceptMap` resource | `http://example.org/icd10-to-snomed` |
| **`$translate`** | The FHIR operation to translate one code | `GET /fhir/R5/ConceptMap/$translate?...` |
| **`$translate-batch`** | A non-standard bulk operation for N codes | `POST /fhir/R5/ConceptMap/$translate-batch` |
| **`relationship`** | The R5 term indicating how source/target relate | `equivalent`, `source-is-broader-than-target` |
| **`equivalence`** | The R4 equivalent of `relationship` | `equivalent`, `wider`, `narrower` |

---

<a id="loading-conceptmaps"></a>

## 4. Loading your ConceptMaps

A `ConceptMap` must exist in the server *before* you can translate codes with it.

### 4.1 Loading a single ConceptMap

Send a `POST` request with a valid FHIR `ConceptMap` JSON body.

```bash
curl -X POST http://localhost:8080/fhir/R5/ConceptMap \
  -H "Content-Type: application/fhir+json" \
  -d '{
    "resourceType": "ConceptMap",
    "url": "http://example.org/icd10-to-snomed",
    "name": "ICD10ToSnomed",
    "status": "active",
    "group": [{
      "source": "http://hl7.org/fhir/sid/icd-10",
      "target": "http://snomed.info/sct",
      "element": [{
        "code": "A00",
        "target": [{
          "code": "18639002",
          "relationship": "equivalent"
        }]
      }]
    }]
  }'
```

**Key notes for ETL:**
*   **`status`**: Must be `active` for the map to be usable for translation.
*   **`url`**: This is the *primary key* you will use to reference the map in `$translate` calls.
*   **`relationship`**: On the `/fhir/R5` tree, use R5 codes (`equivalent`, `source-is-broader-than-target`, etc.). On `/fhir/R4`, use R4 codes (`equivalent`, `wider`, etc.).
*   **`group`**: A ConceptMap can have multiple groups. Each group has one `source` and one `target` system.

### 4.2 Loading large / bulk ConceptMaps (ETL use case)

The server is optimized for large ingestions (e.g., mapping the entire ICD-10 to SNOMED).

*   **How it works internally:** When you `POST` or `PUT` a ConceptMap, the server extracts every `group.element.target` into a flat table (`concept_map_mappings`) using `pgx.CopyFrom`. This means a 100,000-row mapping can be ingested in **~1.5 seconds**.
*   **Optimistic concurrency:** On `PUT`, you can use `If-Match: W/"N"` to prevent overwriting a map that was just updated by another process.

### 4.3 Understanding `unmapped` strategies

If a code in your pipeline does *not* have a mapping in the ConceptMap, the server applies the `group.unmapped` strategy defined in the ConceptMap itself.

```json
{
  "group": [{
    "source": "...",
    "target": "...",
    "element": [...],
    "unmapped": {
      "mode": "fixed",
      "code": "UNKNOWN",
      "display": "Unknown code",
      "relationship": "not-related-to"
    }
  }]
}
```

| `mode` | Behavior | ETL Impact |
| :--- | :--- | :--- |
| `fixed` | Returns a static fallback code/display | Good for a default "unknown" value |
| `use-source-code` | Passes the input code through to the target system | Good for identity mappings (pass-through) |
| `other-map` | Recursively looks up the code in *another* ConceptMap | Good for cascading fallbacks (e.g., national -> local map) |

> **Note:** `other-map` has a recursion depth cap of **5** to prevent infinite loops.

---

<a id="translating-concepts"></a>

## 5. Translating Concepts (for ETL)

### 5.1 Single-code lookup (`$translate`)

Use this for ad-hoc lookups or when your pipeline processes records one by one.

**GET Request (Type-level):**
```bash
curl "http://localhost:8080/fhir/R5/ConceptMap/\$translate?\
url=http://example.org/icd10-to-snomed\
&sourceSystem=http://hl7.org/fhir/sid/icd-10\
&sourceCode=A00\
&targetSystem=http://snomed.info/sct"
```

**POST Request (Type-level):**
```bash
curl -X POST http://localhost:8080/fhir/R5/ConceptMap/\$translate \
  -H "Content-Type: application/fhir+json" \
  -d '{
    "resourceType": "Parameters",
    "parameter": [
      {"name": "url", "valueUri": "http://example.org/icd10-to-snomed"},
      {"name": "sourceSystem", "valueUri": "http://hl7.org/fhir/sid/icd-10"},
      {"name": "sourceCode", "valueCode": "A00"},
      {"name": "targetSystem", "valueUri": "http://snomed.info/sct"}
    ]
  }'
```

**Response (R5):**
```json
{
  "resourceType": "Parameters",
  "parameter": [
    {"name": "result", "valueBoolean": true},
    {"name": "match", "part": [
      {"name": "relationship", "valueCode": "equivalent"},
      {"name": "concept", "valueCoding": {"system": "http://snomed.info/sct", "code": "18639002"}},
      {"name": "originMap", "valueUri": "http://example.org/icd10-to-snomed"}
    ]}
  ]
}
```

### 5.2 Batch translation (`$translate-batch`) — Recommended for ETL

This is the **primary integration point for ETL pipelines**. Instead of making N HTTP requests, you send a **single request** with N `(code, system)` probes. The server resolves them in a **single SQL roundtrip**.

**POST Request:**
```bash
curl -X POST http://localhost:8080/fhir/R5/ConceptMap/\$translate-batch \
  -H "Content-Type: application/fhir+json" \
  -d '{
    "resourceType": "Parameters",
    "parameter": [
      {"name": "url", "valueUri": "http://example.org/icd10-to-snomed"},
      {"name": "code", "part": [
        {"name": "code", "valueCode": "A00"},
        {"name": "system", "valueUri": "http://hl7.org/fhir/sid/icd-10"}
      ]},
      {"name": "code", "part": [
        {"name": "code", "valueCode": "A01"},
        {"name": "system", "valueUri": "http://hl7.org/fhir/sid/icd-10"}
      ]},
      {"name": "code", "part": [
        {"name": "code", "valueCode": "B99"},
        {"name": "system", "valueUri": "http://hl7.org/fhir/sid/icd-10"}
      ]}
    ]
  }'
```

**Response:**
```json
{
  "resourceType": "Parameters",
  "parameter": [
    {"name": "result", "valueBoolean": true},
    {"name": "translate", "part": [
      {"name": "input", "part": [
        {"name": "code", "valueCode": "A00"},
        {"name": "system", "valueUri": "http://hl7.org/fhir/sid/icd-10"}
      ]},
      {"name": "result", "valueBoolean": true},
      {"name": "match", "part": [...]}
    ]},
    {"name": "translate", "part": [
      {"name": "input", "part": [...]},
      {"name": "result", "valueBoolean": true},
      {"name": "match", "part": [...]}
    ]},
    {"name": "translate", "part": [
      {"name": "input", "part": [...]},
      {"name": "result", "valueBoolean": false},
      {"name": "message", "valueString": "Only negative matches found"}
    ]}
  ]
}
```

**Key features for ETL:**
*   **Per-probe independence:** If one code is not found, it returns `result: false` and a message for *that specific probe*, while others succeed.
*   **Input echo:** Each result contains the original `code` and `system`, allowing you to easily map results back to your input records.
*   **Overall result:** The top-level `result` is `true` if **any** probe matched at least one target.

---

<a id="etl-integration-examples"></a>

## 6. ETL Integration Examples

### 6.1 Python (Airflow / pandas)

This is the most common ETL integration pattern.

```python
import requests
import pandas as pd
import json

FHIR_BASE_URL = "http://localhost:8080/fhir/R5"
CONCEPT_MAP_URL = "http://example.org/icd10-to-snomed"
SOURCE_SYSTEM = "http://hl7.org/fhir/sid/icd-10"
TARGET_SYSTEM = "http://snomed.info/sct"

def translate_batch(codes: list[str]) -> list[dict]:
    """
    Translates a list of codes using $translate-batch.
    Returns a list of result dicts, one per input code.
    """
    if not codes:
        return []

    params = {
        "resourceType": "Parameters",
        "parameter": [
            {"name": "url", "valueUri": CONCEPT_MAP_URL},
        ]
    }

    for code in codes:
        params["parameter"].append({
            "name": "code",
            "part": [
                {"name": "code", "valueCode": code},
                {"name": "system", "valueUri": SOURCE_SYSTEM}
            ]
        })

    resp = requests.post(
        f"{FHIR_BASE_URL}/ConceptMap/$translate-batch",
        json=params,
        headers={"Content-Type": "application/fhir+json"}
    )
    resp.raise_for_status()
    data = resp.json()

    results = []
    # The first parameter is 'result', subsequent ones are 'translate'
    for param in data.get("parameter", [])[1:]:
        if param.get("name") != "translate":
            continue

        # Extract input echo
        input_part = next((p for p in param.get("part", []) if p.get("name") == "input"), {})
        input_code = next((p["valueCode"] for p in input_part.get("part", []) if p.get("name") == "code"), "")

        # Extract result
        result_part = next((p for p in param.get("part", []) if p.get("name") == "result"), {})
        found = result_part.get("valueBoolean", False)

        # Extract first match
        matches = [p for p in param.get("part", []) if p.get("name") == "match"]
        target_code = None
        relationship = None

        if matches:
            concept_part = next((p for p in matches[0].get("part", []) if p.get("name") == "concept"), {})
            coding = concept_part.get("valueCoding", {})
            target_code = coding.get("code")

            rel_part = next((p for p in matches[0].get("part", []) if p.get("name") == "relationship"), {})
            relationship = rel_part.get("valueCode")

        results.append({
            "source_code": input_code,
            "found": found,
            "target_code": target_code,
            "relationship": relationship
        })

    return results

# --- Example Usage with Pandas ---
df = pd.DataFrame({"icd10_code": ["A00", "A01", "B99", "Z99"]})

# Process in batches of 100 to keep request size reasonable
BATCH_SIZE = 100
all_results = []

for i in range(0, len(df), BATCH_SIZE):
    batch = df["icd10_code"][i : i + BATCH_SIZE].tolist()
    results = translate_batch(batch)
    all_results.extend(results)

results_df = pd.DataFrame(all_results)
final_df = df.join(results_df)
print(final_df)
```

### 6.2 Shell / cURL

Useful for one-off lookups or CI/CD pipeline smoke tests.

```bash
#!/bin/bash
set -e

FHIR_URL="http://localhost:8080/fhir/R5"
MAP_URL="http://example.org/icd10-to-snomed"

echo "Translating A00..."
curl -s "${FHIR_URL}/ConceptMap/\$translate?url=${MAP_URL}&sourceSystem=http://hl7.org/fhir/sid/icd-10&sourceCode=A00" | jq .

echo "Batch translating A00, A01..."
curl -s -X POST "${FHIR_URL}/ConceptMap/\$translate-batch" \
  -H "Content-Type: application/fhir+json" \
  -d @- <<EOF | jq '.parameter | length'
{
  "resourceType": "Parameters",
  "parameter": [
    {"name": "url", "valueUri": "${MAP_URL}"},
    {"name": "code", "part": [
      {"name": "code", "valueCode": "A00"},
      {"name": "system", "valueUri": "http://hl7.org/fhir/sid/icd-10"}
    ]},
    {"name": "code", "part": [
      {"name": "code", "valueCode": "A01"},
      {"name": "system", "valueUri": "http://hl7.org/fhir/sid/icd-10"}
    ]}
  ]
}
EOF
```

### 6.3 Go

Useful if you are writing a Go-based data processor or sidecar.

```go
package main

import (
    "bytes"
    "encoding/json"
    "fmt"
    "net/http"
    "time"
)

type Parameters struct {
    ResourceType string      `json:"resourceType"`
    Parameter    []Parameter `json:"parameter"`
}

type Parameter struct {
    Name      string      `json:"name"`
    ValueURI  string      `json:"valueUri,omitempty"`
    ValueCode string      `json:"valueCode,omitempty"`
    Part      []Parameter `json:"part,omitempty"`
}

func main() {
    url := "http://localhost:8080/fhir/R5/ConceptMap/$translate-batch"

    payload := Parameters{
        ResourceType: "Parameters",
        Parameter: []Parameter{
            {Name: "url", ValueURI: "http://example.org/icd10-to-snomed"},
            {
                Name: "code",
                Part: []Parameter{
                    {Name: "code", ValueCode: "A00"},
                    {Name: "system", ValueURI: "http://hl7.org/fhir/sid/icd-10"},
                },
            },
        },
    }

    body, _ := json.Marshal(payload)
    resp, err := http.Post(url, "application/fhir+json", bytes.NewBuffer(body))
    if err != nil {
        panic(err)
    }
    defer resp.Body.Close()

    var result Parameters
    json.NewDecoder(resp.Body).Decode(&result)
    fmt.Printf("Response: %+v\n", result)
}
```

### 6.4 Handling errors gracefully

All errors are returned as FHIR `OperationOutcome` resources with an appropriate HTTP status code.

```json
{
  "resourceType": "OperationOutcome",
  "issue": [{
    "severity": "error",
    "code": "not-found",
    "diagnostics": "Resource not found"
  }]
}
```

| HTTP Status | FHIR `code` | Meaning | ETL Action |
| :--- | :--- | :--- | :--- |
| `200 OK` | — | Success | Process result |
| `400 Bad Request` | `invalid` | Invalid JSON, missing params, R4/R5 param conflict | Log and fail the record |
| `404 Not Found` | `not-found` | ConceptMap URL not found, or code not found in map | Map missing? Retry? Log? |
| `409 Conflict` | `conflict` | Stale `If-Match` on update | Retry with fresh ETag |
| `500 Internal Server Error` | `exception` | Internal error | Retry with backoff |

**In Python:**
```python
try:
    resp = requests.post(...)
    resp.raise_for_status()
except requests.exceptions.HTTPError as e:
    error_data = e.response.json()
    issue = error_data.get("issue", [{}])[0]
    print(f"FHIR Error: {issue.get('code')} - {issue.get('diagnostics')}")
```

---

<a id="reverse-translation"></a>

## 7. Reverse Translation

Sometimes you have a target code and need to find the source. E.g., you have a SNOMED code and want the original ICD-10.

**Method 1: `reverse=true` (R5)**
```bash
curl "http://localhost:8080/fhir/R5/ConceptMap/\$translate?\
url=http://example.org/icd10-to-snomed\
&sourceSystem=http://hl7.org/fhir/sid/icd-10\
&sourceCode=A00\
&targetSystem=http://snomed.info/sct\
&reverse=true"
```

**Method 2: `targetCode` (R4/R5)**
```bash
curl "http://localhost:8080/fhir/R5/ConceptMap/\$translate?\
url=http://example.org/icd10-to-snomed\
&targetSystem=http://snomed.info/sct\
&targetCode=18639002"
```

**Important for ETL:** If you use `$translate-batch`, reverse translation is **not** supported in the batch endpoint. You must fall back to single `$translate` calls for reverse lookups.

---

<a id="vocabulary-projection"></a>

## 8. Vocabulary Projection (R4 ↔ R5)

The server stores everything internally in **R5** format. This table shows how R4 `equivalence` codes map to R5 `relationship` codes.

| R5 `relationship` | R4 `equivalence` | Notes |
| :--- | :--- | :--- |
| `equivalent` | `equivalent` | Perfect 1:1 match |
| `source-is-broader-than-target` | `wider` | Source is less specific |
| `source-is-narrower-than-target` | `narrower` | Source is more specific |
| `related-to` | `relatedto` | Vague relationship |
| `not-related-to` | `unmatched` | No mapping found |

### Impact on ETL

*   **If you write to `/fhir/R5`:** Requests and responses use `relationship`. The server validates that incoming `ConceptMap` resources use R5 vocabulary.
*   **If you write to `/fhir/R4`:** Requests and responses use `equivalence`. The server automatically projects R4 vocabulary to R5 for storage, and R5 back to R4 for responses.
*   **You cannot mix R4 and R5 parameter names** in the same `$translate` request (e.g., `code` + `sourceCode`). The server returns `400 InvalidRequest`.

---

<a id="performance"></a>

## 9. Performance, Limits & Observability

### 9.1 Performance Characteristics

*(Measured on a single MacBook M-series, warm cache)*

| Operation | Latency | Notes |
| :--- | :--- | :--- |
| Single `$translate` | **p50: 2 ms, p99: 5 ms** | Indexed query on flat table |
| `$translate-batch` | **1 SQL roundtrip** | Scales linearly with number of probes |
| Ingest 100k mappings | **~1.5 s** | `pgx.CopyFrom` path |

### 9.2 Limits

| Limit | Value | Description |
| :--- | :--- | :--- |
| `other-map` depth | `5` | Prevents infinite loops in unmapped fallback |
| Search `_count` | `20` (default), `1000` (max) | Pagination on ConceptMap search |
| Batch size | Unenforced (but recommended ≤ 500) | Dependent on your HTTP client timeout and memory |

### 9.3 Observability

*   **Health check:** `GET /health`
*   **Structured logs:** The server emits JSON logs via Go's `slog` (standard output). Look for `duration_ms` for query timing.
*   **Request IDs:** Every request gets a `X-Request-ID` header (injected by middleware) for tracing.

---

<a id="configuration-reference"></a>

## 10. Configuration Reference

All configuration is via environment variables.

| Variable | Default | Description |
| :--- | :--- | :--- |
| `SERVER_PORT` | `8080` | HTTP listen port |
| `SERVER_READ_TIMEOUT` | `30s` | HTTP read timeout |
| `SERVER_WRITE_TIMEOUT` | `30s` | HTTP write timeout |
| `SERVER_IDLE_TIMEOUT` | `120s` | HTTP idle timeout |
| `SERVER_SHUTDOWN_TIMEOUT` | `15s` | Graceful shutdown deadline |
| `DB_HOST` | `localhost` | PostgreSQL host |
| `DB_PORT` | `5432` | PostgreSQL port |
| `DB_USER` | `fhir` | Database user |
| `DB_PASSWORD` | `fhir` | Database password |
| `DB_NAME` | `fhir` | Database name |
| `DB_SSL_MODE` | `disable` | SSL mode (`disable`, `require`, `verify-ca`, `verify-full`) |
| `DB_MAX_CONNS` | `25` | Max connection pool size |
| `DB_MIN_CONNS` | `5` | Min connection pool size |
| `DB_MAX_CONN_LIFETIME` | `1h` | Max connection lifetime |
| `DB_MAX_CONN_IDLE_TIME` | `30m` | Max idle connection time |

---

<a id="advanced-features"></a>

## 11. Advanced: `dependsOn`, `product`, `unmapped` modes

### 11.1 `dependsOn` (Dependency Filtering)

In a `ConceptMap`, a target can have `dependsOn` attributes. When translating, you can provide a `dependency` parameter to only return targets where the dependency matches.

**Example ConceptMap snippet:**
```json
{
  "element": [{
    "code": "A00",
    "target": [{
      "code": "18639002",
      "relationship": "equivalent",
      "dependsOn": [{
        "property": "http://example.org/severity",
        "valueCode": "severe"
      }]
    }]
  }]
}
```

**Translate request with dependency:**
```bash
curl -X POST http://localhost:8080/fhir/R5/ConceptMap/\$translate \
  -H "Content-Type: application/fhir+json" \
  -d '{
    "resourceType": "Parameters",
    "parameter": [
      {"name": "url", "valueUri": "http://example.org/icd10-to-snomed"},
      {"name": "sourceSystem", "valueUri": "http://hl7.org/fhir/sid/icd-10"},
      {"name": "sourceCode", "valueCode": "A00"},
      {"name": "dependency", "part": [
        {"name": "attribute", "valueUri": "http://example.org/severity"},
        {"name": "value", "valueCode": "severe"}
      ]}
    ]
  }'
```

### 11.2 `product` (Metadata Forwarding)

`product` is similar to `dependsOn`, but it is returned in the response as metadata about the match, rather than being used as a filter.

### 11.3 Multi-coding `CodeableConcept` Input

If your source data contains a `CodeableConcept` (a list of codings), you can pass the whole object. The server translates **every** coding and aggregates the results.

**Request:**
```json
{
  "parameter": [
    {"name": "sourceCodeableConcept", "valueCodeableConcept": {
      "coding": [
        {"system": "http://hl7.org/fhir/sid/icd-10", "code": "A00"},
        {"system": "http://hl7.org/fhir/sid/icd-10", "code": "A01"}
      ]
    }}
  ]
}
```

---

<a id="troubleshooting"></a>

## 12. Troubleshooting

### 12.1 "Resource not found" (404)

*   **Cause:** The `url` in your `$translate` request does not match any `ConceptMap` in the database.
*   **Fix:** Check the `url` field in your `ConceptMap` resource. Also verify the ConceptMap is loaded via `GET /fhir/R5/ConceptMap`.

### 12.2 "Only negative matches found" (200)

*   **Cause:** The code exists in the ConceptMap, but all mappings have a `relationship` of `not-related-to` (R5) or `unmatched` (R4).
*   **Fix:** Check your `group.unmapped` strategy if you expect a fallback. Or verify your `ConceptMap` logic.

### 12.3 "No mapping found for the provided concept" (200)

*   **Cause:** The code/system combination is not present in the ConceptMap.
*   **Fix:** If this is expected for some codes, ensure your `unmapped.mode` is set to `fixed` or `use-source-code`. If it's unexpected, check the `source` and `target` systems in your `ConceptMap` groups.

### 12.4 "parameters X and Y are mutually exclusive" (400)

*   **Cause:** You mixed R4 and R5 parameter names (e.g., `code` and `sourceCode`).
*   **Fix:** Choose one vocabulary. For new ETL pipelines, we recommend **R5** (`sourceCode`, `sourceSystem`, etc.).

### 12.5 Slow batch performance

*   **Cause:** Sending very large batches (e.g., > 1000 codes) in a single request.
*   **Fix:** Chunk your data into smaller batches (100–500 codes) and parallelize requests if needed. The server is stateless and scales well horizontally.

---

## Quick Reference Card

```
FHIR Base URL:      http://localhost:8080/fhir/R5
Health:             GET  /health
Load Map:           POST /fhir/R5/ConceptMap
Search Maps:        GET  /fhir/R5/ConceptMap
Translate (single): GET  /fhir/R5/ConceptMap/$translate?url=...&sourceSystem=...&sourceCode=...
Translate (batch):  POST /fhir/R5/ConceptMap/$translate-batch
Reverse:            GET  /fhir/R5/ConceptMap/$translate?...&reverse=true
```