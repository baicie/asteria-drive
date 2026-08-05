# Security Policy

## Supported versions

Security fixes are provided for the latest released minor version and the current
`main` branch. Older tags should be upgraded before a report is evaluated against
them.

## Reporting a vulnerability

Use GitHub's private vulnerability reporting for this repository. Do not open a
public issue or pull request containing exploit details, credentials, signed URLs,
invitation tokens, object keys, database contents, or personal data.

Include the affected commit or version, deployment assumptions, reproduction steps,
impact, and any suggested mitigation. Use synthetic data wherever possible. The
maintainer will acknowledge a complete report within three business days, provide a
triage decision within seven business days, and coordinate disclosure after a fix or
documented risk decision is available. These are response targets, not a warranty.

If private vulnerability reporting is unavailable, open a public issue containing
only a request for a private security contact. Do not include vulnerability details.

## Operational incidents

Revoke exposed OIDC, database, S3, cursor-signing, and trusted-development material
through the owning platform. Preserve append-only audit data and provider logs. Do
not paste secrets into GitHub issues, Actions logs, or application logs.

The production security boundary and residual risks are recorded in
[`docs/security/threat-model.md`](docs/security/threat-model.md). The candidate
approval gate is [`docs/security/review-record.md`](docs/security/review-record.md).
Deployment and recovery procedures are in [`docs/operations`](docs/operations).

## Research expectations

Good-faith research must avoid privacy violations, service degradation, persistence,
lateral movement, and access to data that is not owned by the researcher. Stop after
demonstrating the minimum evidence needed for the report.
