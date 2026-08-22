# Phase 1 Security Review Record Template

> **Not an approval.** This file is a template with a stale historical candidate
> reference. It has not been rebound to the current release and target production
> environment, and it has no independent security/platform signatures.

## Record identity

| Field | Value |
| --- | --- |
| Template / historical review ID | `phase1-production-readiness-2026-08-05` |
| Record state | `STALE TEMPLATE - NOT APPROVED` |
| Historical candidate commit | `aab5147ae047ca336a0cdf24928db00fafdaa869` |
| Current published reference | `v0.1.1`, source `e8d26ded6e9138bbfdeac60f2487c5f835ab61a5`, OCI `sha256:2b73f8a7a271c0d7d6c7f73e15987b5e29290437146f07a57b57b9aef031d842` |
| Current reference binding | Not bound to this security review; release artifacts/provenance are not production approval |
| Target production environment | `UNASSIGNED` |
| Review status | Pending candidate rebind, all production platform evidence, findings decision, and independent security/platform approval |
| Scope | Phase 1 production readiness: identity/governance, storage, recovery, runtime, deployment, CI, release, and platform controls |

The old candidate SHA is retained only to make the staleness explicit. Do not update
one field and treat this template as approved. For the production candidate, create
or rebind a review record that freezes the exact source commit, release tag and URL,
OCI manifest digest, rendered deployment digest, target environment, evidence set,
reviewer identities, and UTC decision time. The candidate may be `v0.1.1` only if it
is still the exact promotion target and passes every current gate; otherwise bind the
newer release.

Any source, generated artifact, workflow, manifest, release setting, or relevant
platform-evidence change invalidates the affected approval and requires re-review.
The [production closure checklist](../operations/production-readiness.md) is the
authoritative gate list.

## Inputs required for a bound review

| Area | Required evidence | Result at approval |
| --- | --- | --- |
| Threats and controls | `docs/security/threat-model.md`, `docs/security/control-matrix.md`, and completed production closure checklist | Attach review comments, control owners and all final statuses |
| Code and migration | Candidate diff; `go test ./...`, `go vet ./...`, `go build ./...`, race and PostgreSQL/S3 integration evidence | Attach immutable CI run URLs for the bound commit |
| API and ownership | OpenAPI compatibility, registered-route contract, protected-main and CODEOWNERS evidence | Attach CI and repository-policy evidence |
| Release | Immutable GitHub Release, protected tag, checksums, SPDX SBOM, GHCR manifest digest, both provenance attestations and independent release Environment approval | Attach API/settings evidence, approval and attestation URLs |
| Runtime | Rendered production overlay, public TLS, network enforcement, least privilege and HA/failover result | Attach rendered-manifest digest and platform test records |
| Recovery | Encrypted PostgreSQL PITR/WAL and object-version restore in isolation, verifier JSON, measured/approved RPO and RTO | Attach immutable drill evidence and two reviewers |
| Platform controls | Production OIDC lifecycle, KMS/secrets, database trust, bucket versioning/retention/encryption, IAM, ingress/CNI, monitoring/SIEM, capacity and external presign exercise | Attach target-environment evidence for every checklist row |

The latest staging recovery run `31953850132`, artifact `9265397749`, is reusable
background evidence only. It proves a bounded staging logical restore and verifier
`2/2`, while explicitly recording `pitr_wal_replayed=false`,
`object_versions_restored=false`, and `staging-recovery-not-production`.

## Findings and decision template

No finding is accepted by omission. Replace the placeholder with each finding and
record severity, owner, expiry, mitigation, evidence, and an explicit risk decision.
An open high-severity finding blocks approval.

| ID | Severity | Owner | Expiry | Decision / evidence |
| --- | --- | --- | --- | --- |
| `TEMPLATE` | N/A | `UNASSIGNED` | N/A | Remove this row only after the bound reviewers record actual findings or explicitly record “no findings”. |

## Approval template

| Decision field | Value |
| --- | --- |
| Decision | `NOT DECIDED` |
| Security reviewer / UTC | `UNASSIGNED` |
| Platform reviewer / UTC | `UNASSIGNED` |
| Candidate commit / release / OCI digest | `UNASSIGNED` |
| Target environment / rendered manifest digest | `UNASSIGNED` |
| Access-controlled change record | `UNASSIGNED` |
| Findings disposition | `UNASSIGNED` |

Approval is permitted only when every applicable control and every production
closure row is `COMPLETE`, the recovery drill meets explicitly approved RPO/RTO,
required checks are green for the bound candidate, no high finding is open, and
authorized security and platform reviewers sign outside this repository. A checked-
in name, placeholder or pull-request approval alone is not a production decision.
