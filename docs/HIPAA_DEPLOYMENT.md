# HIPAA Deployment Guide

This runbook is the authoritative reference for deploying **fhir-map** in an
environment that processes Protected Health Information (PHI). It documents every
configuration setting an operator must set — and *why* — to satisfy HIPAA
technical safeguards, and ends with a sign-off checklist for go-live.

fhir-map is configured entirely through environment variables. Misconfiguration
fails loudly: the server refuses to start (or emits a startup warning) when a
required secret is missing or a setting is unsafe for production. Treat every
warning in the startup log as a blocker until resolved.

> **Audience:** hospital IT operators and platform engineers responsible for the
> deployment, not application developers.

---

## 1. Database Connection & TLS (`DATABASE_URL`, `DB_SSL_MODE`)

fhir-map connects to PostgreSQL using a single connection string supplied via
the **`DATABASE_URL`** environment variable. This variable is **required** — if
it is unset, the server logs `DATABASE_URL is required but not set` and exits
immediately. The connection string's value is never written to logs; the startup
log records only its presence (`"database_url_set": true`).

```
DATABASE_URL=postgres://user:password@host:5432/dbname?sslmode=verify-full
```

The `sslmode` query parameter controls transport encryption between fhir-map and
your database:

| `sslmode` | Meaning | HIPAA suitability |
|-----------|---------|-------------------|
| `disable` | No encryption — credentials and patient-adjacent data traverse the wire in **plaintext** | **Local dev only. Never in production.** |
| `require` | Connection encrypted; server certificate **not** verified | Acceptable minimum |
| `verify-full` | Connection encrypted **and** server certificate + hostname verified | **Recommended** |

HIPAA deployments **MUST** use `sslmode=require` or, preferably,
`sslmode=verify-full`. Using `disable` in production means database credentials
and any PHI-adjacent query data are exposed to anyone on the network path.

> The legacy individual `DB_*` variables (`DB_HOST`, `DB_PORT`, `DB_USER`,
> `DB_PASSWORD`, `DB_NAME`, `DB_SSL_MODE`) still exist as a fallback for local
> development. They are **only** consulted when `DATABASE_URL` is empty. In a
> HIPAA deployment, always set `DATABASE_URL` explicitly and put `sslmode` inside
> it — do not rely on the fallback.

---

## 2. TLS for the FHIR API (`TLS_CERT_*`, `TLS_KEY_*`)

fhir-map terminates TLS for inbound FHIR API traffic. Certificates are supplied
in one of two ways:

| Mode | Variables | Use when |
|------|-----------|----------|
| **Inline PEM** (recommended) | `TLS_CERT_PEM`, `TLS_KEY_PEM` | Certs delivered via Kubernetes Secrets / orchestrator-injected env |
| **File paths** | `TLS_CERT_FILE`, `TLS_KEY_FILE` | Certs mounted onto the container filesystem (volume-mounted) |

Behaviour:

- **Inline PEM takes priority** when both modes are configured.
- If a cert variable is set without its matching key (e.g. `TLS_CERT_PEM` but no
  `TLS_KEY_PEM`), the server exits with a named error.
- An invalid certificate causes the server to exit **before** binding the port —
  the failure is visible in the foreground, not buried in runtime logs.
- The minimum negotiated TLS version is enforced at **TLS 1.2**.

If no TLS variables are set, the server falls back to **plain HTTP** and emits
the startup warning `TLS not configured; serving plain HTTP — not suitable for
HIPAA production deployment`. **Plain HTTP must never be used in production.**
Either terminate TLS at fhir-map directly, or terminate it at the auth proxy /
load balancer in front of it (see Section 6) — but PHI must never traverse an
unencrypted hop.

---

## 3. CORS Configuration (`CORS_ALLOWED_ORIGINS`)

The default value (`*`) permits **any** browser origin to call the API — this is
**not acceptable** for a HIPAA deployment.

Set `CORS_ALLOWED_ORIGINS` to a comma-separated whitelist of exactly the origins
your front-end applications are served from:

```
CORS_ALLOWED_ORIGINS=https://app.hospital.org,https://admin.hospital.org
```

When a request arrives with an `Origin` header that is **not** on the whitelist,
the response contains **no** `Access-Control-Allow-Origin` header, and the
browser blocks the cross-origin read.

---

## 4. Trusted Proxy / Client IP Logging (`TRUSTED_PROXIES`)

fhir-map records the client IP address in its audit log (`client_ip` field). When
the server sits behind a load balancer or ingress, the real client IP arrives in
the `X-Forwarded-For` header. `TRUSTED_PROXIES` tells fhir-map which upstream
peers are allowed to set that header.

Set it to the CIDR range(s) of your load balancer / ingress:

```
TRUSTED_PROXIES=10.0.0.0/8
```

> **NEVER set `TRUSTED_PROXIES=0.0.0.0/0`.** A wildcard means *every* client is
> trusted to set `X-Forwarded-For`, allowing any caller to forge an arbitrary
> `client_ip` and poison the audit trail — a direct violation of HIPAA audit-log
> integrity requirements. fhir-map **rejects** `0.0.0.0/0` (and `::/0`) at startup
> with a descriptive error and refuses to start.

If `TRUSTED_PROXIES` is left unset, fhir-map logs the immediate TCP peer as
`client_ip`. That is correct only for direct connections with no proxy in front;
if you deploy behind a proxy without setting this, every log line will show the
proxy's IP instead of the real client. The server emits a startup warning when
the variable is unset.

---

## 5. Log Level for Production (`LOG_LEVEL`)

`LOG_LEVEL` controls how much detail is written to the structured (JSON) audit
log. The HIPAA-relevant trade-off is whether per-request access logs — which
contain `client_ip`, `method`, and `path` — are emitted.

| `LOG_LEVEL` | Access logs | Notes |
|-------------|-------------|-------|
| `warn` (recommended) | Suppressed | Only startup, TLS, migration events, and errors are written |
| `info` | Written | HIPAA-safe: access logs contain **no** query strings and **no** request bodies |
| `debug` | Written **with** `request_uri` including query strings | **Prohibited in production — see below** |

- **Recommended production value: `warn`.**
- At `info`, each access log line carries: `time`, `level`, `msg=request`,
  `service`, `env`, `method`, `path` (path only, **no** query string), `status`,
  `duration_ms`, `request_id`, `client_ip`. This field set is confirmed PHI-safe.
- **`LOG_LEVEL=debug` combined with `APP_ENV=production` is a hard stop** — the
  server refuses to start, because `debug` logs `request_uri` including query
  strings, which is a PHI exposure risk.
- Keep `LOG_FORMAT=json` (the default) in production. `LOG_FORMAT=text` triggers
  a startup warning because JSON-consuming audit pipelines (Splunk / Datadog /
  Loki) cannot parse text-format structured fields.

---

## 6. Auth Proxy Deployment Pattern

**fhir-map has no built-in authentication.** It trusts that every request
reaching it has already been authenticated. HIPAA deployments **MUST** place an
authenticating reverse proxy in front of fhir-map.

Recommended pattern: **nginx + OAuth2 Proxy** (or equivalent) that validates a
JWT / session before forwarding the request:

```
                 ┌─────────────────────────────────────────────┐
                 │                  Hospital network            │
   Browser /     │                                              │
   FHIR client ──┼──► Auth Proxy ───► fhir-map ───► PostgreSQL  │
       (TLS)     │   (nginx +         (FHIR API)    (sslmode=    │
                 │    OAuth2 Proxy)                  verify-full) │
                 │    - validates JWT                            │
                 │    - terminates TLS                           │
                 │    - forwards only authenticated requests     │
                 └─────────────────────────────────────────────┘
```

Guidance:

- The auth proxy validates the caller's token and forwards only authenticated
  requests to fhir-map.
- Set `TRUSTED_PROXIES` (Section 4) to the auth proxy's CIDR so `client_ip` is
  attributed correctly.
- Set `CORS_ALLOWED_ORIGINS` (Section 3) to the origins served by the auth
  proxy's front-end.
- Ensure no network path lets clients reach fhir-map directly, bypassing the
  proxy.

---

## 7. Operator HIPAA Compliance Checklist

Complete and sign off on every item before go-live:

- [ ] `DATABASE_URL` is set and uses `sslmode=require` or `sslmode=verify-full`
- [ ] TLS configured via `TLS_CERT_PEM`/`TLS_KEY_PEM` or `TLS_CERT_FILE`/`TLS_KEY_FILE` (or TLS terminated at the auth proxy with no plaintext hop to fhir-map)
- [ ] `CORS_ALLOWED_ORIGINS` set to a specific origin whitelist (not `*`)
- [ ] `TRUSTED_PROXIES` set to the exact LB/ingress CIDR (not `0.0.0.0/0`)
- [ ] `LOG_LEVEL` is `warn` or `info` (never `debug` in production)
- [ ] `LOG_FORMAT` is `json`
- [ ] `APP_ENV=production` is set
- [ ] Auth proxy deployed in front of fhir-map; no direct client path bypasses it
- [ ] Docker image pulled from `ghcr.io` and signature verified with cosign:
  ```
  cosign verify ghcr.io/goodeworkers/fhir-map:vX.Y.Z \
    --certificate-identity-regexp 'https://github.com/GoodEWorkers/fhir-map/.github/workflows/ci.yml@refs/tags/.*' \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com
  ```
- [ ] Container does not run as root (image uses distroless `nonroot` base, UID 65532, enforced in the Dockerfile)
