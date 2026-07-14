/**
 * fhir-map RAM Saturation Test
 * ==============================
 * Goal: ramp virtual users until the server's 1 GB RAM limit is exhausted
 * or we find the latency cliff / error cliff.
 *
 * Profile (aggressive ramp, no sustain):
 *   0s   →  30s  :  0  → 200 VUs   (gentle warm-up)
 *   30s  →  60s  :  200 → 1000 VUs (medium stress)
 *   60s  →  90s  : 1000 → 3000 VUs (heavy stress)
 *   90s  → 120s  : 3000 → 5000 VUs (saturation attempt)
 *   120s → 140s  : 5000 → 0        (ramp down / recovery check)
 *
 * Each VU hammers the hot paths only ($translate + health) to maximise
 * concurrent DB connections and in-flight goroutines, which is what
 * actually drives RAM usage in a Go HTTP server.
 *
 * Thresholds are intentionally relaxed — the goal is NOT to pass SLOs,
 * it is to FIND THE BREAKING POINT. We just need the run to complete.
 */

import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter, Trend, Rate } from 'k6/metrics';

// ─── Custom metrics ──────────────────────────────────────────────────────────
const translateDuration = new Trend('translate_duration', true);
const http5xx           = new Counter('http_5xx_count');
const http503           = new Counter('http_503_count');
const errorRate         = new Rate('error_rate');

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';

// ─── Configuration ───────────────────────────────────────────────────────────
export const options = {
  scenarios: {
    ram_saturation: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: [
        { duration: '30s',  target: 200  },  // warm-up
        { duration: '30s',  target: 1000 },  // medium stress
        { duration: '30s',  target: 3000 },  // heavy stress
        { duration: '30s',  target: 5000 },  // saturation attempt
        { duration: '20s',  target: 0    },  // recovery
      ],
      gracefulRampDown: '10s',
    },
  },

  // Relaxed thresholds — we WANT to see where it breaks
  thresholds: {
    // Just don't hard-abort; we want to collect data through the cliff
    'http_req_duration': ['p(99)<30000'],  // 30s max before test aborts
    'http_req_failed':   ['rate<0.99'],    // allow up to 99% failures
  },

  // Allow k6 to use up to 4 GB of host RAM for VU goroutines
  // (100 VUs ≈ 6 MB, 5000 VUs ≈ 300 MB — well within k6's budget)
  noConnectionReuse: false,
  discardResponseBodies: false,
};

// ─── Setup: seed one ConceptMap to translate against ─────────────────────────
export function setup() {
  const cm = {
    resourceType: 'ConceptMap',
    url: 'http://example.org/saturation-test/address-use',
    status: 'active',
    group: [{
      source: 'http://hl7.org/fhir/address-use',
      target: 'http://terminology.hl7.org/CodeSystem/v3-AddressUse',
      element: [
        { code: 'home', target: [{ code: 'H',   relationship: 'equivalent' }] },
        { code: 'work', target: [{ code: 'WP',  relationship: 'equivalent' }] },
        { code: 'temp', target: [{ code: 'TMP', relationship: 'equivalent' }] },
      ],
    }],
  };

  const r = http.post(
    `${BASE_URL}/fhir/ConceptMap`,
    JSON.stringify(cm),
    { headers: { 'Content-Type': 'application/fhir+json' } },
  );

  if (r.status !== 201) {
    console.error(`Setup failed: ${r.status} — ${r.body}`);
    return { url: cm.url };
  }
  const id = JSON.parse(r.body).id;
  console.log(`Setup: seeded ConceptMap id=${id}`);
  return { id, url: cm.url };
}

// ─── Teardown ─────────────────────────────────────────────────────────────────
export function teardown(data) {
  if (data && data.id) {
    http.del(`${BASE_URL}/fhir/ConceptMap/${data.id}`);
    console.log(`Teardown: deleted ${data.id}`);
  }
}

// ─── Hot-path loop: translate + health only ───────────────────────────────────
// These two endpoints are chosen because:
//   - $translate hits the DB (uses a connection from the pool → RAM for buffers)
//   - /health is pure Go (stresses goroutine scheduler, not DB)
// Together they maximise concurrent in-flight goroutines and DB connections.
export default function (data) {
  const url = (data && data.url) || 'http://example.org/saturation-test/address-use';
  const codes = ['home', 'work', 'temp', 'unknown'];
  const code  = codes[Math.floor(Math.random() * codes.length)];

  // ── $translate (DB-hitting hot path) ────────────────────────────────────
  const body = {
    resourceType: 'Parameters',
    parameter: [
      { name: 'url',          valueUri:  url   },
      { name: 'sourceCode',   valueCode: code  },
      { name: 'sourceSystem', valueUri:  'http://hl7.org/fhir/address-use' },
    ],
  };

  const start = Date.now();
  const r = http.post(
    `${BASE_URL}/fhir/ConceptMap/$translate`,
    JSON.stringify(body),
    {
      headers: { 'Content-Type': 'application/fhir+json' },
      timeout: '10s',
    },
  );
  translateDuration.add(Date.now() - start);

  const ok = check(r, {
    'translate 2xx': (res) => res.status >= 200 && res.status < 300,
  });
  errorRate.add(!ok);
  if (r.status >= 500) http5xx.add(1);
  if (r.status === 503) http503.add(1);

  // ── /health (pure Go, goroutine pressure) ───────────────────────────────
  const h = http.get(`${BASE_URL}/health`, { timeout: '5s' });
  check(h, { 'health 2xx': (res) => res.status === 200 });

  // Minimal sleep — we WANT to pile on pressure
  sleep(0.05);
}
