# Capacity and sustained SLO evidence

This runbook provides the repeatable fixture loader and sustained control-plane
sampler required before making a million-node or long-duration SLO claim. It is
an evidence procedure, not a claim that the targets have already been met.

## Safety boundary

Run this only against an isolated performance PostgreSQL database and a dedicated
tenant. `asteria-loadtest` creates directories but intentionally has no delete or
truncate mode. Do not point it at a production tenant, and do not place a bearer
token, database password, or presigned URL in the archived report.

The loader requires `-write`; without it, it prints a deterministic dry-run plan.
It does not run migrations. Apply migrations through `asteria-migrate` before
loading data.

## Million-node fixture

Choose a new UUID tenant for every measurement campaign. The seed controls stable
directory IDs and names, and a rerun with the same tenant and seed is safe in the
sense that existing rows are retained; it is not a cleanup operation. A different
seed creates a distinct fixture in the same tenant.

```powershell
$env:ASTERIA_LOADTEST_DATABASE_URL = 'postgres://asteria:***@127.0.0.1:15432/asteria?sslmode=disable'
go run ./cmd/asteria-loadtest `
  -tenant-id '11111111-1111-4111-8111-111111111111' `
  -nodes 1000000 -fanout 32 -batch-size 10000

go run ./cmd/asteria-loadtest `
  -database-url $env:ASTERIA_LOADTEST_DATABASE_URL `
  -tenant-id '11111111-1111-4111-8111-111111111111' `
  -nodes 1000000 -fanout 32 -batch-size 10000 -write
```

The write command uses `COPY` into a transaction-local staging table, inserts in
parent-before-child order, lets PostgreSQL enforce the namespace constraints, then
runs `ANALYZE file_node` by default. Each batch verifies that every deterministic
row is present with the expected parent and name; unrelated ID or name conflicts
fail the run rather than being silently counted as loaded. Its JSON output records
new and pre-existing row counts, elapsed time, and rows per second. Archive that
output alongside the database size and environment metadata.

## Sustained sampler

The sampler has a five-minute warmup and ten-minute measured interval by default.
Its authenticated read workload round-robins `GET /healthz`, `GET /api/v1/tenant`,
and root `GET /api/v1/directories/{id}/children?limit=50`. It reports p50/p95/p99,
max latency, unexpected statuses, dropped queued work, and `5xx`/transport failures.
It never serializes the bearer token into its JSON report.

```powershell
$env:ASTERIA_SLO_BASE_URL = 'http://127.0.0.1:18080'
$env:ASTERIA_SLO_TOKEN = 'obtain-from-the-performance-secret-store'
$env:ASTERIA_SLO_ROOT_ID = 'the-root-id-created-by-asteria-loadtest'
go run ./cmd/asteria-slo -output slo-2026-08-05.json
```

For a short wiring check, use a separate artifact path because report creation
refuses to overwrite an existing file:

```powershell
go run ./cmd/asteria-slo -duration 15s -warmup 0s -rate 10 -concurrency 2 `
  -output slo-wiring-check.json
```

For the metadata-write target, use a dedicated fixture tenant and opt in explicitly.
Every sampled write creates a uniquely named empty directory beneath the given
parent, so this has no automatic cleanup path and must not target shared data:

```powershell
go run ./cmd/asteria-slo -rate 80 -concurrency 24 `
  -include-directory-writes -write-parent-id $env:ASTERIA_SLO_ROOT_ID `
  -output slo-read-write-2026-08-05.json
```

The read-only default and optional directory writes do not prove multipart-signing,
upload-completion, download-authorization, availability, or object-storage targets
in `docs/mvp/scope.md` section 9. Those require separate production-like scenarios
and must be reported independently. A short wiring check is not sustained evidence.

## Report record

Archive the generated JSON and fill this record in the release evidence. Record
only secret-free values.

| Field | Value |
| --- | --- |
| Candidate commit / immutable image digest | `<fill>` |
| Date and operator | `<fill>` |
| API, PostgreSQL, and SeaweedFS versions | `<fill>` |
| Region, network RTT, CPU, memory, and connection-pool limits | `<fill>` |
| Tenant ID / root ID / seed | `<fill>` |
| Requested and inserted nodes / database size after load | `<fill>` |
| Loader JSON artifact | `<fill>` |
| Sampler command and JSON artifact | `<fill>` |
| Warmup and measured duration | `<fill>` |
| Per-endpoint p50 / p95 / p99 / max | `<fill>` |
| Unexpected status count, 5xx rate, and dropped count | `<fill>` |
| Comparison with scope section 9 targets | `<fill>` |
| Deviations, incidents, and follow-up owner | `<fill>` |

Until a report records a full production-like run, do not mark the million-node or
sustained SLO targets as achieved.

## 2026-08-05 evidence snapshot

The following secret-free artifacts are archived under `evidence/`:

- [`million-node-load-20260805.json`](evidence/million-node-load-20260805.json)
  and [`million-node-load-replay-20260805.json`](evidence/million-node-load-replay-20260805.json)
  record an initial load of 1,000,000 nodes and an idempotent replay. The integrity
  verifier in [`million-node-integrity-20260805.json`](evidence/million-node-integrity-20260805.json)
  found 1,000,001 reachable nodes, maximum depth 4, zero orphan nodes, and zero
  duplicate active-name groups. PostgreSQL occupied approximately 588 MB.
- [`slo-wiring-check-20260805.json`](evidence/slo-wiring-check-20260805.json)
  passed the 15-second route wiring check with no dropped, 5xx, or unexpected
  responses.
- [`slo-control-plane-20260805.json`](evidence/slo-control-plane-20260805.json)
  records a five-minute warmup followed by ten minutes at 50 RPS and concurrency
  16. It completed 29,998 requests with zero drops, one server error, and one
  unexpected status (`0.003334%` server-error rate). Endpoint p95 latency was
  `1.615 ms` for `healthz`, `4.029 ms` for directory listing, and `2.916 ms` for
  tenant discovery; all are below the corresponding initial latency and error-rate
  thresholds in scope section 9.

The single unexpected response is retained as a deviation for review rather than
silently discarded. This run exercises read-only control-plane routes only. It does
not prove metadata-write, multipart-signing/completion, download-authorization,
object-storage, availability, or ingress rate-limit targets; those remain separate
measurements. The artifacts therefore establish a reproducible scale and control-
plane evidence baseline, not a public SLA or production approval.
