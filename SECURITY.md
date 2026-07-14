# Security Policy

## Supported Versions

fhir-map is pre-1.0. Security fixes are applied to the latest released
version on the `main` branch. Older tags are not patched.

## Reporting a Vulnerability

Please report security vulnerabilities privately — **do not open a public
issue** for a suspected vulnerability.

- Use GitHub's [private vulnerability reporting](https://github.com/GoodEWorkers/fhir-map/security/advisories/new)
  ("Report a vulnerability" on the Security tab).

Please include:

- a description of the issue and its impact,
- steps to reproduce (a minimal request/payload is ideal),
- affected version or commit, and
- any suggested remediation.

## Response

- We aim to acknowledge a report within **5 business days**.
- We will keep you informed of progress and coordinate a disclosure timeline
  once a fix is available.
- We will credit reporters who wish to be named once the fix is released.

## Scope

This project is a transformation and terminology component intended to run
*inside* a data pipeline. Deployment hardening (network exposure, authn/authz,
TLS termination, PHI handling) is the operator's responsibility — see
[`docs/HIPAA_DEPLOYMENT.md`](docs/HIPAA_DEPLOYMENT.md).
