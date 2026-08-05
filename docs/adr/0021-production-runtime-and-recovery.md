# ADR-0021: Production Runtime, Secrets, and Recovery

## Status

Accepted for the Phase 1 production-readiness increment on 2026-08-05.

## Context

The release workflow produces verifiable binaries, but the repository has no
production container contract, hardened deployment baseline, secret-file input,
restore automation, or completed security-review record. The local Compose file and
development credentials must not be treated as a production topology.

## Decision

Production uses OIDC, PostgreSQL, and S3. Database TLS must verify the server
certificate and hostname. Automatic migration and bucket creation are forbidden in
the server process. A dedicated migration job runs the exact release image before a
rollout.

Sensitive configuration supports mutually exclusive `NAME` and `NAME_FILE` inputs.
Production requires file-mounted secrets or workload identity; inline database
credentials, static S3 credentials, trusted tokens, and cursor keys are rejected.
Secret files are read once at startup, have a bounded size, and are never logged.
S3 uses the AWS default credential chain when static keys are absent, allowing
short-lived workload credentials. Rotation uses overlapping credentials or a
rolling restart and is verified by readiness before old material is revoked.

The OCI image is built from the released source in a pinned multi-stage build and
runs as a numeric non-root user with no shell requirement. The Kubernetes baseline
uses a read-only root filesystem, dropped capabilities, seccomp, resource requests
and limits, readiness/liveness probes, a disruption budget, topology spreading, and
default-deny network policy with explicit database, object-storage, OIDC, DNS, and
metrics paths. TLS terminates at a documented trusted ingress or service mesh.

PostgreSQL recovery uses encrypted PITR/WAL as the production mechanism; a portable
logical dump is retained for migration drills. Object storage requires versioning or
equivalent immutability and lifecycle rules. Restore always targets an isolated
environment first, starts with maintenance and destructive cleanup disabled, applies
migrations, and runs a metadata-to-object integrity verifier before traffic is
enabled. RPO/RTO are evidence-based and are not claimed from configuration alone.

A release is not production-ready until the repository contains a threat model,
security control matrix, dependency/static-analysis evidence, restore drill report,
and a review record tied to an immutable commit. Findings are fixed or have an owner,
expiry, and explicit risk acceptance. Independent approval remains required for the
final protected-branch merge.

## Consequences

The same binary can consume platform-managed secrets without embedding them, and the
deployment baseline constrains common container escape and accidental-exposure
paths. Backup existence is separated from proven restoration. Cloud-specific KMS,
PITR, ingress, and object-lock resources remain deployment overlays, but the required
interfaces and verification steps are stable.

## Alternatives considered

- Keep all secrets in environment variables: rejected because process and platform
  inspection surfaces are broader than read-only mounted files or workload identity.
- Let every replica auto-migrate: rejected because concurrent rollout and rollback
  behavior becomes difficult to control.
- Claim RPO/RTO from a runbook alone: rejected because only a timed restore drill can
  establish recovery performance.
- Publish a container without a deployment contract: rejected because provenance
  alone does not establish runtime hardening or recoverability.

