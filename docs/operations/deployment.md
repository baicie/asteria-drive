# Production Deployment

## Supported boundary

The checked-in Kubernetes manifests are a hardened base, not a complete cloud
environment. A production overlay must replace the zero image digest, OIDC issuer,
initial owner, tenant IDs, S3 endpoint/bucket, ingress namespace labels, and network
CIDRs. PostgreSQL, S3, OIDC, TLS termination, DNS, KMS, and workload-identity
resources are owned by the target platform.

The server refuses production startup unless authentication is OIDC, metadata is
PostgreSQL, storage is S3, database TLS uses `sslmode=verify-full`, the cursor key is
file-mounted, and automatic migration and bucket creation are disabled.

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

- `database-url`: a PostgreSQL URL using `sslmode=verify-full`; credentials, when
  needed, are present only in this mounted file;
- `cursor-hmac-key`: at least 32 cryptographically random bytes.

Prefer workload identity for S3. If the S3 implementation requires static keys,
mount `s3-access-key-id` and `s3-secret-access-key` and set the corresponding
`ASTERIA_*_FILE` variables in an overlay. The workload may read and write only its
private bucket prefix and may not administer buckets or IAM.

Rotate secrets by introducing the new credential, rolling instances until readiness
passes, then revoking the old credential. Cursor-key rotation invalidates existing
pagination cursors and must be announced as a control-plane compatibility event.

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
