# Backup and Restore Runbook

## Recovery contract

PostgreSQL encrypted PITR/WAL is the authoritative production recovery mechanism.
The checked-in logical backup is a portable migration and drill artifact. S3 must
provide versioning or equivalent immutability, encryption, inventory, and lifecycle
retention. Cloud-specific policies belong in the deployment overlay and its evidence.

No RPO or RTO is promised by this document. Only a timed, seeded restore drill can
establish those values for an environment.

## Automated staging recovery drill

The `Drill staging recovery` workflow in
`.github/workflows/drill-staging-recovery.yml` is the repeatable staging check for
the repository-owned recovery procedure. It is available through
`workflow_dispatch` and runs on a weekly schedule from protected `main`. It uses
the `staging` environment and shares the `asteria-drive-staging` concurrency group
with deployment and monitoring, so a drill cannot race a rollout or a capacity
probe.

The explicit host bootstrap installs `scripts/recover-staging.sh` root-owned and
pins its digest in the forced-command dispatcher. Before every execution, the
dispatcher checks the installed file's ownership, mode, and SHA-256. The workflow
can invoke only
`recovery <run-id> <attempt> <workflow-sha> <recovery-script-sha256>`; the
dispatcher requires the workflow's reviewed digest to equal its host-side pin and does
not provide an interactive shell, SFTP, forwarding, or application credentials.
The pinned script reads the active staging PostgreSQL database, creates a
run-labelled temporary internal Docker network, PostgreSQL volume, and recovered
API, and removes every resource it created before returning. Existing staging
database and object-store volumes are never deleted or replaced, and no recovery
port is published.

This drill restores PostgreSQL metadata with a custom-format `pg_dump` archive. It
does not copy object data or object versions: the verifier reads the existing
staging object store over the temporary internal network to check metadata/object
consistency. Consequently it is a logical metadata restore check, not a point-in-
time recovery or a production failover exercise.

The workflow validates the returned JSON before uploading the secret-free
`staging-recovery-evidence` artifact. The artifact contains the recovery JSON and,
when the remote command writes diagnostics, only a SHA-256 of that stderr; raw
remote stderr is discarded. GitHub retains this artifact for 90 days. A successful
report has schema
`asteria-drive-staging-recovery/v1`, `status=success`, `last_phase=complete`, the
current GitHub run identity, the pinned Compose and image digests, and the claim
boundary `staging-recovery-not-production`. The check fields that must be true are
`backup_catalog_verified`, `source_stable_during_backup`, `restore_succeeded`,
`schema_verified`, `table_counts_verified`, `storage_verifier_succeeded`,
`recovered_health_succeeded`, `recovered_readiness_succeeded`,
`recovered_authenticated_read_succeeded`, `recovered_metrics_scrape_succeeded`,
`cleanup_verified`, and `capacity_guard_verified`.

The same report records the archive byte count and SHA-256, source/restored schema
versions, checked table and row totals, row-count digest, verifier checked/healthy
counts and finding status, backup/restore elapsed seconds, and before/after disk
threshold measurements. `object_versions_restored` and `pitr_wal_replayed` must be
`false`; those values document what the drill did not prove. The workflow rejects
unknown or extra JSON fields, malformed identities, failed cleanup, or a disk use
above 85% / free space below 5 GiB. A failed run is an actionable staging signal,
but its artifact is not approval to promote traffic.

The exact v1 field allowlist is:

```text
schema status last_phase started_at completed_at github_run_id github_run_attempt
workflow_sha compose_sha256 expected_image expected_postgres_image claim_boundary
backup_catalog_verified source_stable_during_backup restore_succeeded schema_verified
table_counts_verified storage_verifier_succeeded recovered_health_succeeded
recovered_readiness_succeeded recovered_authenticated_read_succeeded
recovered_metrics_scrape_succeeded cleanup_verified capacity_guard_verified
object_versions_restored pitr_wal_replayed backup_archive_size_bytes
backup_archive_sha256 source_schema_version restored_schema_version tables_checked
source_total_rows restored_total_rows row_counts_sha256 storage_verifier_checked
storage_verifier_healthy storage_verifier_finding_count
storage_verifier_findings_truncated backup_elapsed_seconds restore_elapsed_seconds
disk_used_percent_before disk_available_bytes_before disk_used_percent_after
disk_available_bytes_after capacity_max_disk_used_percent
capacity_min_disk_available_bytes backup_archive_max_bytes
```

## Prerequisites

- Use an isolated recovery account, network, PostgreSQL instance, and S3 bucket.
- Mount a libpq service file with mode `0600`; set `PGSERVICEFILE` and `PGSERVICE`.
  The service must provide a trusted root certificate (or approved system trust)
  and a host name present in the PostgreSQL leaf certificate SAN.
- Never pass a password-bearing database URL on a process command line.
- Pin the application image by digest and retain the image that created the backup.
- Disable maintenance and destructive cleanup before any recovered server starts.
- Record database backup ID/time, WAL recovery point, object-storage version or
  inventory ID, image digest, schema version, operator, and UTC timestamps.
- Retain only sanitized TLS evidence such as CA/leaf SHA-256, leaf SAN and expiry,
  negotiated protocol and cipher. Never retain a DSN, password, private key, or
  certificate body in the drill artifact.

## Create a portable metadata backup

Use a read-only backup role with access to all Asteria tables:

```bash
export PGSERVICEFILE=/run/asteria/backup/pg_service.conf
export PGSERVICE=asteria_backup
./scripts/backup-postgres.sh /encrypted-backups/asteria
```

The script requires a `0600` service file and appends `sslmode=verify-full` to its
password-free libpq connection string, so a weaker service-file or environment
setting cannot disable certificate and host-name verification. It creates a
PostgreSQL custom-format archive, verifies its catalog, writes a SHA-256 sidecar,
and publishes it only after success. It refuses to overwrite a same-second archive
so operators must select a new destination after a collision. Upload both files to
encrypted, immutable backup media. Capture the matching S3 inventory/version
checkpoint.

## Restore into isolation

1. Provision an empty PostgreSQL database and private S3 recovery bucket. The libpq
   service name must begin with `asteria_restore_`.
2. Restore the required S3 object versions into the recovery bucket without deleting
   newer production versions.
3. Restore the logical archive:

```bash
export PGSERVICEFILE=/run/asteria/restore/pg_service.conf
export PGSERVICE=asteria_restore_drill_20260805
export ASTERIA_RESTORE_TARGET_KIND=isolated
export ASTERIA_RESTORE_CONFIRM=isolated-target
./scripts/restore-postgres.sh /encrypted-backups/asteria/asteria_UTC.dump
```

The restore script also appends `sslmode=verify-full`; the isolated database must
therefore present a certificate whose SAN matches the service-file host and chains
to its configured root.

4. Run `asteria-migrate` from the candidate image against the isolated database.
5. Configure the verifier with the isolated PostgreSQL and S3 endpoints, then run:

```bash
asteria-verify-storage -batch-size 500 -concurrency 16 -max-findings 100
```

6. A verified report requires the expected schema version, zero missing objects,
   zero size mismatches, zero checksum mismatches, and no truncated findings.
7. Start one server with `ASTERIA_MAINTENANCE_ENABLED=false`. Exercise OIDC, tenant
   reads, namespace reads, one new upload/download, metrics, and audit export.
8. Compare row counts and sampled audit sequence ranges with the source evidence.
9. Record measured recovery point and elapsed time. Keep traffic disabled if any
   check is ambiguous or failed.

## Production recovery decision

Promoting a recovered environment requires two-person approval, an immutable image
digest, a clean verifier report, and a written decision for data after the recovery
point. DNS/ingress changes and any source-environment fencing are platform operations.
Do not run the generic logical restore script against a service name that is not
explicitly dedicated to an isolated target.

## Drill cadence and failure handling

Run the automated staging drill weekly and run a production-scoped, seeded restore
drill at least quarterly and before releases with migration or storage-state risk.
Preserve sanitized commands, versions, row/object counts, timestamps, verifier JSON,
measured RPO/RTO, findings, and reviewer identity. Never archive database URLs,
credentials, signed URLs, raw object keys, or customer data in the repository.

The latest repository-procedure evidence is the
[2026-08-05 isolated recovery drill](evidence/recovery-drill-20260805.md). It passed
the local seeded restore path but explicitly leaves production PITR, object
immutability, platform controls, candidate binding, and independent approval open.
An automated staging artifact may supplement that report, but it carries the same
`staging-recovery-not-production` boundary and cannot establish production RPO/RTO.
