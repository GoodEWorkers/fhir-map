/**
 * fhir-map Load & Stress Test
 * ============================
 * Target: 1 vCPU / 1 GB RAM server (see docker-compose.loadtest.yml)
 *
 * Endpoint coverage
 * -----------------
 *  /health                           — liveness baseline
 *  GET  /fhir/ConceptMap             — search (read-heavy)
 *  GET  /fhir/ConceptMap/{id}        — read by ID
 *  POST /fhir/ConceptMap/$translate  — single-code translate (hot path)
 *  POST /fhir/ConceptMap/$translate-batch — bulk translate
 *  POST /fhir/ConceptMap             — create (write path)
 *  GET  /fhir/StructureMap/{id}      — StructureMap read
 *
 * Scenario profile
 * ----------------
 *  Stage 1 — Ramp-up    0 → 20 VUs over 30 s   (warm-up, find baseline)
 *  Stage 2 — Sustain   20 VUs for 90 s          (steady-state SLO check)
 *  Stage 3 — Spike     20 → 80 VUs over 20 s    (find saturation point)
 *  Stage 4 — Hold      80 VUs for 30 s          (sustained stress)
 *  Stage 5 — Ramp-down 80 → 0 VUs over 10 s     (graceful recovery check)
 *
 * Thresholds (SLOs for 1 vCPU / 1 GB)
 * -------------------------------------
 *  http_req_duration p95 < 500 ms   (overall; translate is the hot path)
 *  http_req_duration p95 < 200 ms   (health + read endpoints)
 *  http_req_failed   < 1 %          (error budget)
 *  translate_p95     < 300 ms       (single-code translate SLA)
 *
 * Usage
 * -----
 *  make load-test              — via Docker Compose
 *  k6 run -e BASE_URL=http://localhost:8080 loadtest/load_test.js  — local
 */

import http from 'k6/http';
import { check, sleep, group } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';
import { SharedArray } from 'k6/data';

// ─── Custom metrics ──────────────────────────────────────────────────────────
const translateDuration = new Trend('translate_duration', true);
const batchDuration     = new Trend('batch_duration',     true);
const errorRate         = new Rate('error_rate');
const http413Count      = new Counter('http_413_count');
const http5xxCount      = new Counter('http_5xx_count');

// ─── Test configuration ──────────────────────────────────────────────────────
const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';

export const options = {
  scenarios: {
    load_test: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: [
        { duration: '30s', target: 20 },  // Stage 1: ramp-up
        { duration: '90s', target: 20 },  // Stage 2: sustain
        { duration: '20s', target: 80 },  // Stage 3: spike
        { duration: '30s', target: 80 },  // Stage 4: hold at peak
        { duration: '10s', target: 0  },  // Stage 5: ramp-down
      ],
      gracefulRampDown: '10s',
    },
  },

  thresholds: {
    // Overall latency
    'http_req_duration':                        ['p(95)<500'],
    // Hot-path translate SLA
    'translate_duration':                       ['p(95)<300'],
    // Batch translate — 50-probe batch in under 1 s at p95
    'batch_duration':                           ['p(95)<1000'],
    // Low-latency endpoints
    'http_req_duration{endpoint:health}':       ['p(95)<50'],
    'http_req_duration{endpoint:read}':         ['p(95)<200'],
    'http_req_duration{endpoint:search}':       ['p(95)<200'],
    // Error budget: less than 1 % of requests fail
    'http_req_failed':                          ['rate<0.01'],
    'error_rate':                               ['rate<0.01'],
  },
};

// ─── Shared test data (loaded once, shared across VUs) ───────────────────────

// IDs seeded in setup() — shared across all VUs via SharedArray.
const seededIDs = new SharedArray('seededIDs', function () {
  // Populated by setup(); the array is written to a JSON file that k6 reads.
  // If the file doesn't exist (local run without setup), fall back to empty.
  try {
    return JSON.parse(open('/scripts/seeded_ids.json'));
  } catch (_) {
    return [];
  }
});

// Static batch probes — representative 50-code batch.
const BATCH_PROBES = Array.from({ length: 50 }, (_, i) => ({
  name: 'code',
  part: [
    { name: 'code',   valueCode: i % 3 === 0 ? 'home' : i % 3 === 1 ? 'work' : 'temp' },
    { name: 'system', valueUri:  'http://hl7.org/fhir/address-use' },
  ],
}));

// ─── Setup: seed ConceptMap fixtures ─────────────────────────────────────────

export function setup() {
  const ids = [];

  // Wait for the server to be ready before seeding. The compose stack no
  // longer gates k6 on a container healthcheck (the distroless runtime image
  // has no shell for an in-container probe), so readiness is asserted here.
  const maxWaitSeconds = 60;
  let ready = false;
  for (let i = 0; i < maxWaitSeconds; i++) {
    const probe = http.get(`${BASE_URL}/health`, { tags: { endpoint: 'setup-health' } });
    if (probe.status === 200) {
      ready = true;
      break;
    }
    sleep(1);
  }
  if (!ready) {
    throw new Error(`server at ${BASE_URL} not ready after ${maxWaitSeconds}s`);
  }

  // Seed the address-use ConceptMap used by translate tests.
  const addressUseMap = {
    resourceType: 'ConceptMap',
    url: 'http://example.org/loadtest/address-use',
    status: 'active',
    group: [{
      source: 'http://hl7.org/fhir/address-use',
      target: 'http://terminology.hl7.org/CodeSystem/v3-AddressUse',
      element: [
        { code: 'home', target: [{ code: 'H',   relationship: 'equivalent' }] },
        { code: 'work', target: [{ code: 'WP',  relationship: 'equivalent' }] },
        { code: 'temp', target: [{ code: 'TMP', relationship: 'equivalent' }] },
        { code: 'old',  target: [{ code: 'BAD', relationship: 'not-related-to' }] },
      ],
    }],
  };

  const createResp = http.post(
    `${BASE_URL}/fhir/ConceptMap`,
    JSON.stringify(addressUseMap),
    { headers: { 'Content-Type': 'application/fhir+json' } },
  );

  if (createResp.status === 201) {
    const body = JSON.parse(createResp.body);
    ids.push({ id: body.id, url: addressUseMap.url });
  } else {
    console.warn(`Seed create failed: ${createResp.status} ${createResp.body}`);
  }

  // Seed 10 additional ConceptMaps for read/search load variety.
  for (let i = 0; i < 10; i++) {
    const cm = {
      resourceType: 'ConceptMap',
      url: `http://example.org/loadtest/variety-${i}`,
      status: 'active',
      group: [{
        source: `http://src-${i}`,
        target: `http://tgt-${i}`,
        element: Array.from({ length: 20 }, (_, j) => ({
          code: `CODE-${j}`,
          target: [{ code: `TGT-${j}`, relationship: 'equivalent' }],
        })),
      }],
    };
    const r = http.post(
      `${BASE_URL}/fhir/ConceptMap`,
      JSON.stringify(cm),
      { headers: { 'Content-Type': 'application/fhir+json' } },
    );
    if (r.status === 201) {
      ids.push({ id: JSON.parse(r.body).id, url: cm.url });
    }
  }

  console.log(`Setup complete: seeded ${ids.length} ConceptMaps`);
  return { ids };
}

// ─── Teardown ─────────────────────────────────────────────────────────────────

export function teardown(data) {
  // Best-effort cleanup — delete seeded resources.
  for (const { id } of (data.ids || [])) {
    http.del(`${BASE_URL}/fhir/ConceptMap/${id}`);
  }
  console.log(`Teardown complete: attempted deletion of ${(data.ids || []).length} fixtures`);
}

// ─── Default function (per-VU iteration) ─────────────────────────────────────

export default function (data) {
  const ids    = data.ids || [];
  const cm     = ids.length > 0 ? ids[Math.floor(Math.random() * ids.length)] : null;
  const cmID   = cm ? cm.id  : null;
  const cmURL  = cm ? cm.url : 'http://example.org/loadtest/address-use';

  const commonHeaders = { 'Content-Type': 'application/fhir+json' };

  // ── 1. Health check ──────────────────────────────────────────────────────
  group('health', () => {
    const r = http.get(`${BASE_URL}/health`, { tags: { endpoint: 'health' } });
    const ok = check(r, {
      'health 200': (res) => res.status === 200,
      'health body has status': (res) => res.json('status') === 'healthy',
    });
    errorRate.add(!ok);
    recordHTTPErrors(r);
  });

  sleep(randomBetween(0.05, 0.15));

  // ── 2. Search ConceptMap ─────────────────────────────────────────────────
  group('search', () => {
    const r = http.get(`${BASE_URL}/fhir/ConceptMap?status=active&_count=10`, {
      tags: { endpoint: 'search' },
    });
    const ok = check(r, {
      'search 200': (res) => res.status === 200,
      'search returns Bundle': (res) => {
        try { return JSON.parse(res.body).resourceType === 'Bundle'; } catch { return false; }
      },
    });
    errorRate.add(!ok);
    recordHTTPErrors(r);
  });

  sleep(randomBetween(0.05, 0.1));

  // ── 3. Read by ID ────────────────────────────────────────────────────────
  if (cmID) {
    group('read', () => {
      const r = http.get(`${BASE_URL}/fhir/ConceptMap/${cmID}`, {
        tags: { endpoint: 'read' },
      });
      const ok = check(r, {
        'read 200': (res) => res.status === 200,
        'read returns ConceptMap': (res) => {
          try { return JSON.parse(res.body).resourceType === 'ConceptMap'; } catch { return false; }
        },
      });
      errorRate.add(!ok);
      recordHTTPErrors(r);
    });
    sleep(randomBetween(0.02, 0.08));
  }

  // ── 4. Single-code $translate (hot path) ────────────────────────────────
  group('translate', () => {
    const codes = ['home', 'work', 'temp', 'old', 'no-such-code'];
    const code  = codes[Math.floor(Math.random() * codes.length)];

    const body = {
      resourceType: 'Parameters',
      parameter: [
        { name: 'url',          valueUri:  cmURL },
        { name: 'sourceCode',   valueCode: code  },
        { name: 'sourceSystem', valueUri:  'http://hl7.org/fhir/address-use' },
      ],
    };

    const start = Date.now();
    const r = http.post(
      `${BASE_URL}/fhir/ConceptMap/$translate`,
      JSON.stringify(body),
      { headers: commonHeaders, tags: { endpoint: 'translate' } },
    );
    translateDuration.add(Date.now() - start);

    const ok = check(r, {
      'translate 200': (res) => res.status === 200,
      'translate has result': (res) => {
        try {
          const p = JSON.parse(res.body);
          return p.parameter && p.parameter.some((x) => x.name === 'result');
        } catch { return false; }
      },
    });
    errorRate.add(!ok);
    recordHTTPErrors(r);
  });

  sleep(randomBetween(0.05, 0.15));

  // ── 5. $translate-batch (bulk path, 50 probes) ───────────────────────────
  // Only run ~30 % of the time to avoid overwhelming DB on 1 core.
  if (Math.random() < 0.30) {
    group('translate-batch', () => {
      const body = {
        resourceType: 'Parameters',
        parameter: [
          { name: 'url', valueUri: cmURL },
          ...BATCH_PROBES,
        ],
      };

      const start = Date.now();
      const r = http.post(
        `${BASE_URL}/fhir/ConceptMap/$translate-batch`,
        JSON.stringify(body),
        { headers: commonHeaders, tags: { endpoint: 'batch' } },
      );
      batchDuration.add(Date.now() - start);

      const ok = check(r, {
        'batch 200': (res) => res.status === 200,
        'batch has translate parts': (res) => {
          try {
            const p = JSON.parse(res.body);
            return p.parameter && p.parameter.some((x) => x.name === 'translate');
          } catch { return false; }
        },
      });
      errorRate.add(!ok);
      recordHTTPErrors(r);
    });
    sleep(randomBetween(0.1, 0.3));
  }

  // ── 6. Create ConceptMap (write path, ~15 % of iterations) ──────────────
  if (Math.random() < 0.15) {
    group('create', () => {
      const cm = {
        resourceType: 'ConceptMap',
        status: 'draft',
        group: [{
          source: 'http://src-loadtest',
          target: 'http://tgt-loadtest',
          element: [{ code: 'X', target: [{ code: 'Y', relationship: 'equivalent' }] }],
        }],
      };
      const r = http.post(
        `${BASE_URL}/fhir/ConceptMap`,
        JSON.stringify(cm),
        { headers: commonHeaders, tags: { endpoint: 'create' } },
      );
      const ok = check(r, {
        'create 201': (res) => res.status === 201,
      });
      errorRate.add(!ok);
      recordHTTPErrors(r);

      // Clean up the created resource immediately to avoid DB bloat.
      if (r.status === 201) {
        try {
          const id = JSON.parse(r.body).id;
          if (id) http.del(`${BASE_URL}/fhir/ConceptMap/${id}`);
        } catch (_) { /* best effort */ }
      }
    });
    sleep(randomBetween(0.05, 0.1));
  }

  // ── 7. StructureMap read (404 expected if none seeded — that's fine) ─────
  group('structuremap-read', () => {
    const r = http.get(`${BASE_URL}/fhir/StructureMap?status=active&_count=5`, {
      tags: { endpoint: 'sm-search' },
    });
    // 200 with empty bundle is fine; 500 is not.
    const ok = check(r, {
      'sm-search not 5xx': (res) => res.status < 500,
    });
    errorRate.add(!ok);
    recordHTTPErrors(r);
  });

  sleep(randomBetween(0.1, 0.2));
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

function recordHTTPErrors(r) {
  if (r.status === 413) http413Count.add(1);
  if (r.status >= 500)  http5xxCount.add(1);
}

/** Returns a random float between lo and hi (seconds). */
function randomBetween(lo, hi) {
  return lo + Math.random() * (hi - lo);
}
