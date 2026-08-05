# Isolated Recovery Drill - 2026-08-05

## Decision

The repository-owned logical backup, isolated restore procedure, storage verifier,
and post-restore application checks completed successfully in the local Compose
reference environment. This proves the checked-in recovery path for the seeded
fixture. It does not establish a production RPO/RTO, approve a deployment, or
replace encrypted PITR/WAL, immutable object versions, platform controls, and
independent review.

The drill ran from the production-readiness worktree before an immutable candidate
commit and OCI manifest digest existed. A release reviewer must bind a later drill
or an accepted equivalence decision to the final candidate. No approval is implied
by this report.

## Environment and safety boundary

| Field | Recorded value |
| --- | --- |
| Drill date | 2026-08-05 |
| Source database | Isolated local PostgreSQL 17.5 Compose service |
| Object store | Isolated local SeaweedFS 3.85 S3 gateway |
| Restore target | Separate PostgreSQL container/database with no application traffic |
| Application maintenance | Disabled before restored application startup |
| Seed tenant | `a571e91a-0000-4000-8000-000000000001` |
| Seed object | 32-byte non-customer fixture; raw key and credentials omitted |
| Candidate commit/image | Pending immutable candidate and OCI manifest digest |
| Reviewers | Pending independent security/platform review |

The report contains no database URL, password, bearer token, static S3 credential,
presigned URL, or raw object key. The restore target and its volumes were removed
after the recorded drill. The logical archive remains only in the operator's local
temporary directory and is not a durable or encrypted production backup.

## Timeline and measured recovery

| Measurement | Result |
| --- | --- |
| Logical backup checkpoint | `2026-08-05T05:56:23Z` |
| Archive catalog and SHA-256 verification | Passed |
| Metadata restore elapsed time | 10.38 seconds |
| Checkpoint to completed storage verification | Approximately 170.995 seconds |
| Observed metadata RPO at selected checkpoint | Zero row-count difference |
| Traffic promotion/failover time | Not measured |

The observed values apply only to this small local logical-backup fixture. They are
not a production RPO/RTO commitment and do not measure WAL replay, a regional
failure, DNS/ingress changes, source fencing, or operator approval latency.

## Backup identity

| Field | Value |
| --- | --- |
| Archive | `asteria_20260805T055623Z.dump` |
| Archive size | 52,079 bytes |
| SHA-256 | `d2b624c227ca93b294c7ace37b2202dbef0a2dcdf53605ab162b029a4e679a61` |
| Sidecar comparison | Recomputed on 2026-08-05 and matched |
| Restore mode | PostgreSQL custom archive, single-transaction restore |

The first script run exposed a portability defect: Alpine accepts
`sha256sum -c`, not the GNU long spelling used by the initial script. The restore
script and its repository contract test were corrected, and the successful run
used `sha256sum -c`.

## Metadata comparison

Source and restored target values at the backup checkpoint matched exactly:

| Measure | Source | Restored target |
| --- | ---: | ---: |
| Schema migration version | 3 | 3 |
| Tenants | 5 | 5 |
| File nodes | 44 | 44 |
| Blobs | 1 | 1 |
| File versions | 1 | 1 |
| Upload sessions | 1 | 1 |
| Audit sequence range | `1..1` | `1..1` |

## Object and application verification

`asteria-verify-storage` returned `verified=true` with one checked record and one
healthy record. There were no missing objects, size mismatches, checksum
mismatches, or truncated findings.

Before backup, the fixture was uploaded through the real HTTP API, sent directly to
SeaweedFS, downloaded through a signed authorization, and compared byte-for-byte.
After restore, the recovered server passed readiness and tenant reads with
maintenance disabled. A fresh upload/download round trip also passed, the metrics
endpoint responded, and bounded NDJSON audit export returned two events. The
restored application remained isolated throughout verification.

## Open production gates

This drill deliberately leaves the following platform-owned evidence open:

- encrypted PostgreSQL PITR/WAL retention and a point-in-time replay;
- encrypted, versioned, immutable object backup/inventory and retention policy;
- production OIDC, KMS, IAM, network, monitoring, and SIEM evidence;
- an immutable candidate commit, OCI manifest digest, and hosted CI run URLs;
- two authorized reviewers and the access-controlled change decision;
- production-scale recovery duration and an explicitly accepted RPO/RTO.

Until those items are attached, this result closes the repository procedure check
only. It must not be described as a production recovery approval.
