# Backup and Restore Runbook

## Recovery contract

PostgreSQL encrypted PITR/WAL is the authoritative production recovery mechanism.
The checked-in logical backup is a portable migration and drill artifact. S3 must
provide versioning or equivalent immutability, encryption, inventory, and lifecycle
retention. Cloud-specific policies belong in the deployment overlay and its evidence.

No RPO or RTO is promised by this document. Only a timed, seeded restore drill can
establish those values for an environment.

## Prerequisites

- Use an isolated recovery account, network, PostgreSQL instance, and S3 bucket.
- Mount a libpq service file with mode `0600`; set `PGSERVICEFILE` and `PGSERVICE`.
- Never pass a password-bearing database URL on a process command line.
- Pin the application image by digest and retain the image that created the backup.
- Disable maintenance and destructive cleanup before any recovered server starts.
- Record database backup ID/time, WAL recovery point, object-storage version or
  inventory ID, image digest, schema version, operator, and UTC timestamps.

## Create a portable metadata backup

Use a read-only backup role with access to all Asteria tables:

```bash
export PGSERVICEFILE=/run/asteria/backup/pg_service.conf
export PGSERVICE=asteria_backup
./scripts/backup-postgres.sh /encrypted-backups/asteria
```

The script requires a `0600` service file, creates a PostgreSQL custom-format
archive, verifies its catalog, writes a SHA-256 sidecar, and publishes it only after
success. It refuses to overwrite a same-second archive so operators must select a
new destination after a collision. Upload both files to encrypted, immutable backup
media. Capture the matching S3 inventory/version checkpoint.

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

Run a seeded restore drill at least quarterly and before releases with migration or
storage-state risk. Preserve sanitized commands, versions, row/object counts,
timestamps, verifier JSON, measured RPO/RTO, findings, and reviewer identity. Never
archive database URLs, credentials, signed URLs, raw object keys, or customer data in
the repository.

The latest repository-procedure evidence is the
[2026-08-05 isolated recovery drill](evidence/recovery-drill-20260805.md). It passed
the local seeded restore path but explicitly leaves production PITR, object
immutability, platform controls, candidate binding, and independent approval open.
