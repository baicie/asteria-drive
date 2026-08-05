# ADR-0020: Idempotency, Maintenance Leases, and Metrics

## Status

Accepted for the Phase 1 production-readiness increment on 2026-08-05.

## Context

Upload completion is idempotent, but directory and upload-session creation are not.
The repository can list expired uploads, yet no process calls that primitive. There
is also no automatic recycle retention, Prometheus endpoint, fuzz suite, or
repeatable large-scale load harness.

## Decision

`POST /api/v1/directories` and `POST /api/v1/uploads` accept an optional
`Idempotency-Key`. Keys are bounded and hashed before persistence. A record is scoped
to tenant, principal, operation, and key digest, and stores a canonical request
digest. Matching retries replay the original resource; a different digest returns
`idempotency_conflict`. Records expire after a configured retention period.

Upload creation reserves an idempotency claim before calling S3. Claims have a short
lease and may be recovered after a process crash. The database transaction that
creates the business resource also completes the claim. Known pre-commit failures
release the claim and best-effort abort their multipart upload. This ordering avoids
creating a second session for a concurrent retry while still making abandoned S3
multipart uploads discoverable by storage lifecycle policy.

Every API instance runs the same bounded maintenance loop unless an explicit kill
switch disables it. PostgreSQL claims work with row locks, `SKIP LOCKED`, an owner
token, and an expiring lease, so multiple instances do not intentionally process the
same item. Failed work records an attempt and bounded exponential backoff. Terminal
upload state and physical cleanup state are distinct: a failed S3 abort/delete stays
claimable. The loop expires inactive multipart sessions, reconciles stale completion
states, purges recycle roots after retention, and deletes expired idempotency rows.
Shutdown cancels the loop before closing dependencies.

Prometheus metrics use bounded labels only. HTTP series use method, route template,
and status class. Maintenance, upload-state, repository, and storage metrics never
use tenant IDs, principal IDs, node IDs, object keys, idempotency keys, or request
IDs as labels. Metrics are served on a separately configurable listener that
defaults to loopback.

Fuzz targets cover name normalization, cursors, JSON request boundaries, ETags,
checksums, and multipart lists. A checked-in load command creates deterministic
namespace data and records duration, concurrency, latency quantiles, errors, commit,
and environment metadata. Million-node and sustained SLO claims are accepted only
when the corresponding report is archived; a smoke run is not equivalent evidence.

## Consequences

Client retries no longer create duplicate control-plane resources, and cleanup work
survives process and instance changes. The idempotency claim and maintenance lease
tables add write amplification and require retention. Prometheus cardinality stays
bounded, while detailed request correlation remains in structured logs and audit
events.

S3 lifecycle configuration remains a required final safety net for multipart uploads
created immediately before an unrecoverable process failure. The control plane does
not list or delete arbitrary bucket keys.

## Alternatives considered

- Cache idempotency keys in memory: rejected because restarts and multiple replicas
  would violate replay behavior.
- Write the idempotency result only after S3 creation: rejected because concurrent
  upload retries could create multiple multipart sessions.
- Use a single elected worker: rejected because leased database work is sufficient
  for the current modular monolith and avoids a new runtime dependency.
- Label metrics with tenant or resource IDs: rejected because it creates unbounded
  cardinality and leaks identifiers into the monitoring plane.

