# Production Threat Model

Status: reviewed design baseline for the Phase 1 production-readiness increment.
The review record names the immutable candidate commit and test evidence.

## Scope and assets

The assessed system is the Asteria control plane, its PostgreSQL metadata, its
private S3 bucket, OIDC verification, maintenance loop, release workflow, and
Kubernetes base. TLS termination, the identity provider, managed database, object
storage, KMS, ingress, and workload-identity implementation are platform controls.

Assets include tenant membership and ACL state, file metadata, object references,
audit events, invitation and idempotency digests, release artifacts, configuration,
and short-lived signed URLs. File bytes remain on the S3 data path.

Trust boundaries are:

1. Internet client to trusted TLS ingress and HTTP API.
2. Internet client to the client-visible S3 endpoint through a short-lived signed URL.
3. API process to OIDC discovery and JWKS endpoints.
4. API and maintenance processes to PostgreSQL.
5. API, recovery verifier, and maintenance processes to the private S3 control endpoint.
6. GitHub tag and protected release environment to GitHub Releases and GHCR.
7. Backup operators and isolated recovery infrastructure to backup media.

## Threats and controls

| ID | Threat | Primary controls | Residual risk |
| --- | --- | --- | --- |
| TM-01 | Forged or confused-deputy identity | OIDC issuer, signature, audience and time validation; tenant membership resolved server-side; production rejects trusted-dev | Compromised IdP or signing key remains an upstream incident |
| TM-02 | Cross-tenant metadata access | Tenant-scoped repository predicates and composite foreign keys; foreign IDs map to not-found; contract tests | A missing tenant predicate in a new query remains a review-sensitive defect |
| TM-03 | Privilege escalation through membership, group, or ACL mutation | Owner/admin mutation rules, last-owner invariant, tenant-local subjects, inherited allow-only ACL evaluation, atomic audit event | Allow-only ACLs cannot subtract an inherited grant |
| TM-04 | Invitation theft or replay | 256-bit token, SHA-256 digest at rest, exact issuer/subject binding, expiry, one-way terminal state, single response of raw token | A stolen unexpired token and matching IdP account can be used until revocation |
| TM-05 | Signed URL, object key, credential disclosure, or endpoint confusion | Bounded errors and logs, no storage identifiers in API models, secret-file inputs, separate control/public endpoint signing, production HTTPS validation, no post-sign rewrite | Client endpoints and provider access logs necessarily observe signed URLs; DNS, TLS, CORS, and routing remain platform controls |
| TM-06 | Namespace corruption or duplicate creation | Tenant mutation lock ordering, database constraints, revisions, idempotency claims with leases | Provider success immediately before process loss can leave a multipart orphan until lifecycle cleanup |
| TM-07 | Maintenance double execution or destructive cleanup | PostgreSQL row claims with `SKIP LOCKED`, owner tokens, expiring leases, bounded retries, kill switch | Storage operations are not transactional with PostgreSQL and must be idempotent |
| TM-08 | Audit deletion or silent governance mutation | Governance mutation and event share a transaction; append-only trigger rejects update/delete; bounded NDJSON export | Database superusers and backup administrators remain privileged actors |
| TM-09 | Resource exhaustion | Request/body/part/page bounds, HTTP timeouts, bounded worker batches and labels, dependency backoff | Per-principal rate limiting is delegated to ingress in this increment |
| TM-10 | SQL, JSON, header, or cursor injection | Parameterized SQL, strict JSON decoding, UUID/name/header validation, HMAC cursor, fuzz/property tests | New protocol surfaces require equivalent negative tests |
| TM-11 | Malicious dependency or release substitution | Pinned Actions and images, protected tag ancestry, checksums, SBOM, CodeQL, govulncheck, OIDC provenance, immutable OCI digest | GitHub and upstream build images remain supply-chain dependencies |
| TM-12 | Secret theft from configuration or image | Production rejects inline sensitive configuration, scratch non-root image, read-only root, workload identity, no Secret manifest | Host, node, or platform secret-controller compromise is outside the process boundary |
| TM-13 | Backup loss, rollback, or object mismatch | Encrypted PITR/WAL, versioned object storage, isolated restore, forward migrations, metadata-to-object verifier | Cloud-specific immutability and retention must be proven by each overlay |
| TM-14 | Container breakout and lateral movement | Numeric non-root UID, dropped capabilities, seccomp, read-only root, resource limits, default-deny network policy | Kernel, runtime, CNI, ingress, and mesh defects remain platform risks |

## Abuse cases

- An admin attempts to delete the final active owner or manage an owner. The
  transaction is rejected without an audit success event.
- A user reuses an idempotency key with different JSON. The request returns a stable
  conflict and cannot claim the previous resource.
- Two replicas claim the same expired upload. Row locking and the lease owner allow
  only one active worker; a crashed lease becomes recoverable.
- A caller supplies a node ID from another tenant. Both authorization lookup and
  repository mutation remain tenant-scoped and return not-found.
- An operator restores only PostgreSQL. Traffic remains disabled until the verifier
  proves every available Blob has a matching object of the expected size/checksum.
- A pull request modifies release code. It cannot publish; only a protected-main tag
  and protected release environment can write a Release or GHCR package.

## Explicit non-goals

This increment does not provide malware scanning, DLP, legal hold, end-to-end file
encryption, per-user quotas, deny ACLs, public sharing, SCIM, cross-region replication,
or application-level rate limiting. Those capabilities require separate product and
threat-model updates. Their absence must not be described as an implemented control.
