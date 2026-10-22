# Security Policy

AboueiChecker is pre-release software. There are no supported production
releases yet; `main` and `develop` are development branches.

## Reporting a Vulnerability

Please use GitHub's private vulnerability reporting:

<https://github.com/Erfan-Abouei/Abouei_Checker/security/advisories/new>

Do not open a public issue for a suspected vulnerability and do not include real
credentials, production endpoints, or customer data in any report.

Include, where possible:

- affected commit/version and platform;
- configuration shape with secrets removed;
- reproduction steps;
- expected and observed behavior;
- impact assessment;
- suggested fix or mitigation.

The maintainer will acknowledge the report through GitHub, investigate, and
coordinate disclosure after a fix is available. This is a personal open-source
project; no response-time SLA is promised.

## Scope

Security-relevant areas include:

- secret resolution and redaction;
- header injection and URL validation;
- TLS verification and redirect handling;
- response-size limits and malformed responses;
- SSRF considerations for operator-controlled monitoring targets;
- webhook payload and retry behavior;
- SQLite file handling;
- Docker image defaults;
- CI and supply-chain configuration.

The intended trust model is documented in
[docs/security.md](docs/security.md). Configuration and the operator environment
are trusted; monitored endpoints and webhook receivers are untrusted.

## Handling Secrets

- Use `${NAME}` environment references rather than literal secrets.
- Never commit a real `abouei.yaml`, `.env`, database, or webhook URL.
- Do not include secrets in bug reports or logs.
- If a secret is exposed, rotate it first, then report the exposure path without
  including the value.

## Supported Versions

No version is currently supported for production use. Once releases exist, this
policy will list supported release lines and security-fix expectations. Until
then, only the latest development code is considered.

## Release Security Gates

`v1.0.0` requires a security review, race and dependency checks, `govulncheck`,
CodeQL, secret scanning, image scanning, and validation of the controls in
[docs/release-checklist.md](docs/release-checklist.md).
