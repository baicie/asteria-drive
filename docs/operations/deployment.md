# Production Deployment

## Supported boundary

The checked-in Kubernetes manifests are a hardened base, not a complete cloud
environment. A production overlay must replace the zero image digest, OIDC issuer,
initial owner, tenant IDs, S3 control/public endpoints and bucket, ingress namespace
labels, and network CIDRs. PostgreSQL, S3, OIDC, TLS termination, DNS, KMS, and
workload-identity resources are owned by the target platform.

The server refuses production startup unless authentication is OIDC, metadata is
PostgreSQL, storage is S3, the entire database DSN is file-mounted and contains
exactly one `sslmode=verify-full`, the cursor key is file-mounted, and automatic
migration and bucket creation are disabled.

## Automated single-host staging

The `Deploy staging` workflow provides a narrower operational target for the first
real server rollout. It verifies the `v0.1.1` OCI provenance and deploys only
`ghcr.io/baicie/asteria-drive@sha256:2b73f8a7a271c0d7d6c7f73e15987b5e29290437146f07a57b57b9aef031d842`
through a dedicated SSH key with strict host-key verification. Application secrets
remain in server-managed Docker volumes and are not GitHub Secrets.

The staging API, S3 diagnostic, and metrics ports bind to loopback. The workflow runs
the migration from a candidate Compose file, verifies the schema transition and
runtime image, performs an authenticated multipart upload and byte-equal download,
scrapes metrics, and requires the storage verifier to check at least one object. It
then uploads secret-free evidence. This target uses trusted development
authentication, a host-local private CA for PostgreSQL, and an internal non-TLS S3
endpoint, so it must not be exposed publicly or used as production evidence. Its
explicit boundary is `staging-not-production`; see
[ADR-0022](../adr/0022-github-actions-staging-deployment.md),
[ADR-0024](../adr/0024-staging-postgresql-private-pki.md), and
[`infra/docker/staging`](../../infra/docker/staging/README.md).

Root bootstrap creates the private CA and a `serverAuth` leaf with exactly
`SAN=DNS:postgres`. The CA private key remains only at
`/etc/asteria-drive/staging-postgres-pki/issuer/ca.key`; Actions and evidence receive
neither issuing material nor certificate bodies. The PostgreSQL process enables TLS
with a TLS 1.2 minimum, rejects plaintext TCP in HBA, and requires SCRAM-SHA-256 over
TLS. The API and migration consume only the file-mounted `database-url-tls` DSN with
`sslmode=verify-full`; the migration uses the production environment guardrail while
the API remains explicitly `development`. The legacy `database-url` is retained only
for automatic restoration of the previous active Compose definition during the
first TLS rollout.

Deployment evidence schema `asteria-drive-staging-deployment/v2` adds certificate
SHA-256, SAN and validity, `pg_stat_ssl` connection count, negotiated
version/cipher/bits, SCRAM and plaintext-rejection checks, the failed phase, and
rollback results. The plaintext-negative test deletes raw stderr immediately after
recording only its presence and SHA-256. No DSN, password, private key, PEM body, or
raw connection error is retained. If activation fails after changing containers,
the script restores PostgreSQL, SeaweedFS, and the API from the prior active Compose
file with local images only (`--pull never`) and rechecks readiness and image
identity. Both activation and rollback require the container's full fixed
`.Config.Image` reference and its `.Image` config ID to match the local image resolved
from that reference. When no prior Compose exists, a failed first candidate removes
only its containers and network; persistent and secret volumes are preserved.
Remote evidence is quarantined outside the upload directory, checked for duplicate
or extra fields and bounded values, and reserialized only after validation. Raw
stdout and stderr are deleted on every path; a local collection record retains only
presence, byte count, and SHA-256 when validation fails.

The hourly `Monitor staging` workflow serializes with deployments and invokes only a
digest-pinned, root-owned probe through the same forced-command SSH key. It checks
the active Compose and image identities, containers, loopback bindings, HTTP probes,
metrics, PostgreSQL private-CA TLS/SCRAM/plaintext rejection, and the 85%/5 GiB
capacity thresholds, then retains sanitized
`asteria-drive-staging-monitor/v2` JSON for 30 days. A failed workflow is a staging
signal only; production still requires
platform-owned monitoring, alert routing, SIEM retention, and capacity planning.
The monitor applies the same quarantine-before-reserialization rule, so its
`always()` artifact upload cannot retain unvalidated remote output.

The weekly `Drill staging recovery` workflow uses the same protected `main` ref,
`staging` environment, strict host-key boundary, and `asteria-drive-staging`
concurrency group. It can also be started with `workflow_dispatch`. The workflow
invokes only the forced
`recovery <run-id> <attempt> <workflow-sha> <recovery-script-sha256>` operation.
The host requires that requested digest to match its dispatcher pin, then performs a
file ownership/mode and SHA-256 check on the root-owned script installed during
bootstrap, performs a read-only logical PostgreSQL backup, restores it into
run-labelled temporary resources on an internal Docker network, verifies the
restored metadata and application read paths, and removes those resources before
returning. It does not publish a port or modify the active staging volumes.

The workflow accepts a run only when the sanitized JSON uses schema
`asteria-drive-staging-recovery/v2`, has `status=success` and
`last_phase=complete`, matches the pinned Compose/image and GitHub run identities,
and carries `claim_boundary=staging-recovery-not-production`. The artifact is named
`staging-recovery-evidence` and is retained for 90 days; it contains the archive
digest, schema/table/row comparisons, storage-verifier counts,
health/readiness/authenticated-read/metrics results, source API PostgreSQL TLS
connection/version/cipher/bits, cleanup proof, timing, and capacity measurements.
The report must retain
`object_versions_restored=false` and `pitr_wal_replayed=false`. This proves a
staging logical-restore path and source private-CA TLS only; the temporary restore
database remains an isolated drill resource. It is not evidence of production TLS,
encrypted PITR/WAL, object-version recovery, HA, production RPO/RTO, or
public-service readiness.

## Image

Build from an immutable source commit and record the resulting multi-architecture
digest:

```text
docker buildx build --platform linux/amd64,linux/arm64 \
  --build-arg VERSION=vX.Y.Z \
  --build-arg COMMIT=<40-character-commit> \
  --build-arg BUILD_TIME=<UTC-RFC3339> \
  --tag <registry>/asteria-drive:vX.Y.Z --push .
```

The Dockerfile uses a digest-pinned builder. The final image is `scratch`, contains
only CA roots and the two static binaries, and runs as UID/GID 65532. Replace the
image in both the base Deployment and migration Job with the published digest, not a
mutable tag.

## Secrets and workload identity

Create `asteria-runtime-secrets` through the platform secret controller. Do not
commit a Secret manifest. It must expose these file keys:

- `database-url`: the complete PostgreSQL URL with exactly one
  `sslmode=verify-full`; production never accepts an inline DSN, including one with
  a query-string password or no current password;
- `cursor-hmac-key`: at least 32 cryptographically random bytes.

Prefer workload identity for S3. If the S3 implementation requires static keys,
mount `s3-access-key-id` and `s3-secret-access-key` and set the corresponding
`ASTERIA_*_FILE` variables in an overlay. The workload may read and write only its
private bucket prefix and may not administer buckets or IAM.

Rotate secrets by introducing the new credential, rolling instances until readiness
passes, then revoking the old credential. Cursor-key rotation invalidates existing
pagination cursors and must be announced as a control-plane compatibility event.

## S3 control and client endpoints

`ASTERIA_S3_ENDPOINT` is the private control endpoint used by the API, maintenance
worker, and recovery verifier. `ASTERIA_S3_PUBLIC_ENDPOINT` is used only when
signing client upload and download requests; it defaults to the control endpoint for
single-endpoint installations. Both must be HTTPS in production and must not contain
credentials, a query, or a fragment.

The two names must route to the same bucket and object namespace with identical
signing credentials, region, and path-style behavior. Do not rewrite a URL after it
has been signed: the scheme, host, path, signed headers, and query are part of the
AWS canonical request. A production overlay must prove the public endpoint's DNS and
certificate chain, preserve the signed `Host`, enforce CORS, and complete a real
external multipart upload and download before claiming that this platform gate is
closed. Readiness continues to check the private control endpoint and does not prove
client reachability.

## Rollout

1. Render and policy-check both Kustomize targets.
2. Create or update the runtime secret through the platform controller.
3. Run the migration Job with the exact candidate image digest and wait for success.
4. Apply the application base/overlay with `maxUnavailable: 0`.
5. Verify readiness, OIDC authentication, namespace read/write, one multipart upload,
   download authorization, metrics scraping, and audit export.
6. Preserve the previous image digest until the observation window closes.

The base can be rendered with:

```text
kubectl kustomize infra/kubernetes/migration
kubectl kustomize infra/kubernetes/base
```

Database migrations are forward-only. Application rollback is permitted only when
the previous binary is compatible with the migrated schema. Otherwise roll forward.

## Network and TLS

The base denies all traffic, then allows ingress from namespaces labeled
`asteria.network/ingress=true`, DNS, external HTTPS, and PostgreSQL on private
address ranges. Every production overlay must narrow HTTPS and database egress to
the platform's actual destinations. TLS terminates at a trusted ingress or service
mesh; traffic from that boundary to the pod must remain inside the controlled
cluster network or use mesh mTLS.

The application Service is `ClusterIP`. Do not expose the pod or local Compose
dependency ports directly to the Internet.
