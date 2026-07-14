# Load & Stress Tests — fhir-map

## Results from real runs

Both tests below were run against a **1 vCPU / 1 GB RAM** Docker container with
hard memory limits and no swap — the equivalent of the smallest cloud VM tier.

### Steady-state test (80 concurrent users)

Profile: 0 → 20 VUs (30 s) → hold 20 VUs (90 s) → spike to 80 VUs (20 s) →
hold 80 VUs (30 s) → ramp-down (10 s).

| Metric | Result |
|---|---|
| Total requests | 58,876 |
| Peak throughput | **326 req/s** |
| Error rate | **0.00%** |
| Checks passed | **100% (102,400 / 102,400)** |
| Peak RAM used | **12 MB / 1,024 MB** |

Latency by endpoint:

| Endpoint | p50 | p95 |
|---|---|---|
| `/health` | 239 µs | 1.5 ms |
| `GET /ConceptMap/{id}` | 567 µs | 2.5 ms |
| `GET /ConceptMap` search | 1.8 ms | 5.5 ms |
| `POST $translate` | 1 ms | 4 ms |
| `POST $translate-batch` (50 probes) | 2 ms | 6 ms |

### RAM saturation test (5,000 concurrent users)

Profile: 0 → 200 → 1,000 → 3,000 → 5,000 VUs over 2 minutes, then ramp-down.

| Metric | Result |
|---|---|
| Total requests | **1,913,252** |
| Peak throughput | **13,654 req/s** |
| Error rate | **0.00%** |
| Peak RAM at 5,000 users | **326 MB / 1,024 MB (32%)** |
| OOM kill | **No** |
| p95 latency at peak | 508 ms |
| p99 latency at peak | 746 ms |

RAM timeline as users increased:

| VUs | RAM Used |
|---|---|
| 0 (idle) | 13 MB |
| 200 | 17 MB |
| 1,000 | 92 MB |
| 3,000 | 255 MB |
| 5,000 | **326 MB** |
| Recovery | 235 MB |

---

## What this test suite does

Runs load profiles against the fhir-map server constrained to **1 vCPU / 1 GB
RAM** using [k6](https://k6.io). Two scripts are included:

| Script | Purpose |
|---|---|
| `load_test.js` | Steady-state SLO test — enforces latency thresholds |
| `saturation_test.js` | RAM saturation test — ramps to 5,000 VUs to find the breaking point |

---

## Running

### Option A — Docker Compose (recommended)

```bash
make load-test
```

This builds the server image, starts Postgres and the server with 1 vCPU / 1 GB
limits applied, waits for the health check, runs k6, and prints results.

### Option B — Local k6 against a running server

```bash
# Start the stack
docker compose up -d

# Steady-state SLO test
k6 run -e BASE_URL=http://localhost:8080 loadtest/load_test.js

# RAM saturation test
k6 run -e BASE_URL=http://localhost:8080 loadtest/saturation_test.js
```

### Stress test (max throughput, no SLO gates)

```bash
make stress-test
```

---

## Test profiles

### load_test.js — Ramp + Sustain + Spike

```
VUs
80  │                    ████████████████████
    │                   ██                  ██
20  │   ████████████████                      
    │  ██                                     ████
 0  │██                                            ██
    └──────────────────────────────────────────────────▶
      0s   30s         2min      2m20s  2m50s 3min
```

SLO thresholds enforced (test exits non-zero if any fail):

| Metric | Threshold |
|---|---|
| Overall p95 latency | < 500 ms |
| `/health` p95 | < 50 ms |
| Read / Search p95 | < 200 ms |
| `$translate` p95 | < 300 ms |
| `$translate-batch` p95 | < 1,000 ms |
| Error rate | < 1% |

### saturation_test.js — RAM Exhaustion Ramp

```
VUs
5000│                         █████████
    │                   ██████         ██████
3000│             ██████                     ██
    │       ██████                             ██
1000│  █████                                    ██
    │██                                           ████
   0│                                                 ██
    └──────────────────────────────────────────────────▶
      0s    30s    60s    90s   2min                2m20s
```

Thresholds are intentionally relaxed (p99 < 30 s, error rate < 99%) — the goal
is to find where the server breaks, not to enforce SLOs.

---

## Reading results

k6 prints a summary table at the end. Key sections to watch:

- **`http_req_duration`** — latency percentiles (p50/p90/p95/p99)
- **`translate_duration`** — `$translate`-specific latency
- **`error_rate`** — fraction of failed checks
- **`http_413_count`** — body-size limit hits (should be 0)
- **`http_5xx_count`** — server errors (must be 0 for a healthy run)

---

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| p95 > 500 ms at 20 VUs | DB pool exhausted — try increasing `DB_MAX_CONNS` |
| OOM kill (server exits 137) | `GOMEMLIMIT` too tight or batch payloads too large |
| p95 spikes at high VUs then recovers | Normal GC pressure — expected at saturation |
| Error rate > 1% | Check `http_5xx_count` — likely a timeout cascade or panic |
| `http_413_count` > 0 | Request body exceeded 10 MiB limit (not expected with provided scripts) |
