# Staging Recovery Evidence - 2026-08-09

## Scope

This record captures the first successful run of the repository-owned staging
recovery workflow. It proves that the selected staging deployment can create a
bounded logical PostgreSQL backup, restore it into isolated temporary resources,
verify metadata and live object references, exercise the recovered application,
and remove every drill resource.

The claim boundary is `staging-recovery-not-production`. This is not PostgreSQL
PITR/WAL replay, object-version recovery, production RPO/RTO evidence, or a public
production-readiness approval.

## Immutable identity

| Item | Value |
| --- | --- |
| Pull request | [#34](https://github.com/baicie/asteria-drive/pull/34) |
| Merge commit | `0a7a5360dbb0fd0ad6f46e262929a7a2e318cb18` |
| Workflow run | [31292745979](https://github.com/baicie/asteria-drive/actions/runs/31292745979), attempt `1` |
| Workflow SHA | `0a7a5360dbb0fd0ad6f46e262929a7a2e318cb18` |
| Recovery script SHA-256 | `0e03a8e03ee2a98c277818e172822a401614f778f58f8c3e85130a83467402a6` |
| Compose SHA-256 | `d7d39a2e965849f364ceb25ab4106efd575f9a6d924e8ebfd9d508a594adc5dc` |
| Application OCI digest | `sha256:f5da244cba2055764a8caae7b9e9a752cc8f07356c0d7ae6397a6a7992e0cccc` |
| PostgreSQL image digest | `sha256:6567bca8d7bc8c82c5922425a0baee57be8402df92bae5eacad5f01ae9544daa` |
| Artifact | `9031929907`, `staging-recovery-evidence`, 90-day retention |
| Artifact ZIP SHA-256 | `e1edc196bf2b33d08f2567509136bdd94b23a73ddde01a6a28bdc4e87d9accad` |
| Evidence JSON SHA-256 | `4d1f55d5ac166cae12b87c35046a456e78710217e24762b9b78de29a5b5034c7` |

PR #34 passed all seven protected checks and the additional CodeQL check before
merge. No review approval is recorded on the PR, so this evidence does not satisfy
the independent security/platform approval gate.

## Independent verification

The artifact was downloaded through the GitHub API and checked in memory. Its
998-byte ZIP digest matched the API digest, and its only member was the 2,030-byte
`recovery-evidence.json`. Duplicate keys were rejected and the exact 49-field
allowlist was enforced. No raw remote stderr was present or retained.

| Check | Observed result |
| --- | --- |
| Evidence lifecycle | `success`, last phase `complete` |
| Runtime | `2026-08-09T03:35:52Z` to `2026-08-09T03:36:09Z` |
| Logical archive | 51,454 bytes; catalog verified; backup elapsed 1 second |
| Schema | `3 -> 3` |
| Metadata counts | 15 tables; 12 source rows -> 12 restored rows |
| Restore | Succeeded in 8 seconds |
| Storage verifier | 1 checked, 1 healthy, 0 findings, not truncated |
| Recovered application | health, readiness, authenticated read, and metrics all succeeded |
| Capacity | root/Docker filesystem 62% before and after; above the 5 GiB reserve |
| Cleanup | Verified; no drill container, network, volume, or temporary directory remained |
| Object versions | `object_versions_restored=false` |
| PostgreSQL PITR/WAL | `pitr_wal_replayed=false` |

The host dispatcher accepted only the run-bound forced command carrying the
reviewed recovery-script digest. Arbitrary shell execution, port forwarding, the
legacy four-argument recovery command, and an incorrect script digest were all
rejected. The host scripts and their parent directories were verified as
root-owned with the expected modes; the rotated ED25519 private key remained in
process memory and was never written to disk.

## Remaining production gates

This run closes the executable staging logical-recovery and artifact-evidence gap.
Production still requires encrypted PostgreSQL PITR/WAL with a measured replay
point, object versioning or Object Lock, production restore approval and accepted
RPO/RTO, HTTPS OIDC/IdP lifecycle, `verify-full`, KMS-backed secret rotation,
public TLS ingress, production monitoring/SIEM and external alerts, host-level HA,
an externally valid presign endpoint, capacity planning, and independent
security/platform approval.
