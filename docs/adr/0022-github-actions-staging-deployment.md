# ADR-0022: GitHub Actions single-host staging deployment

Status: Accepted

## Context

The release workflow publishes an immutable multi-architecture image, but the
repository previously had no remote deployment path. The available target is a
single Docker host without the external OIDC, TLS PostgreSQL, immutable S3, KMS,
secret controller, ingress, or monitoring controls required for production.

Reusing the local development Compose file would expose fixed credentials and blur
the production boundary. Giving a workflow an SSH password or application secrets
would also create an unnecessary long-lived credential path.

## Decision

Provide a manual, protected `staging` GitHub Actions deployment with these rules:

- deploy only `v0.1.0` by its OCI digest after strict provenance verification;
- allow dispatch only from protected `main`, serialize deployments, and use a
  dedicated environment;
- authenticate with a dedicated ED25519 key and a pinned SSH host key, never the
  inventory CSV password or `ssh-keyscan`; force that key through a root-owned
  dispatcher that accepts only hash-pinned deployment files and fixed operations;
- keep database, cursor, authentication, and S3 credentials in server-managed
  Docker volumes created during an explicit host bootstrap;
- bind API and metrics ports to loopback and isolate dependencies on an internal
  Docker network;
- run the forward-only migration from a candidate Compose file before activating it,
  then verify binary identity, loopback bindings, health, readiness, authenticated
  upload/download bytes, metrics, and non-empty storage integrity;
- install digest-pinned, root-owned monitor and recovery scripts that the dispatcher
  can invoke only with validated GitHub run identities; serialize their scheduled
  and manual workflows with deployment;
- keep recovery drills on a run-labelled internal network, restore only logical
  PostgreSQL metadata into temporary resources, verify read paths, and prove cleanup;
- archive only secret-free deployment evidence.

The staging topology intentionally uses trusted development authentication and
local non-TLS dependencies. Its evidence must carry the claim boundary
`staging-not-production`.

## Consequences

This closes the repeatable server-deployment gap and provides a real runtime target
for further testing. It does not satisfy production identity, encrypted PITR/WAL,
object immutability, platform secret rotation, public TLS ingress, independent SIEM,
node high availability, or two-person approval.

The deployment account belongs to the Docker group, so local execution as that
account remains root-equivalent. The SSH key cannot invoke arbitrary commands: a
root-owned forced-command dispatcher permits only hash-pinned upload, deployment,
sanitized evidence fetch and cleanup, plus digest-pinned monitor and isolated
logical-recovery operations. The key disables forwarding and PTY features, lives
only in the GitHub `staging` environment, and must be rotated if the environment,
dispatcher allowlist, or host ownership changes.

Recovery evidence carries `staging-recovery-not-production` and explicitly records
that object versions were not restored and PITR/WAL was not replayed. It validates
the repository procedure against the existing staging object store; it does not
change the production recovery, immutability, RPO/RTO, or approval requirements.

The released binary signs URLs with its internal `seaweedfs:8333` endpoint. The
deployment verifier connects that signed host to loopback port `18333` without
recording the URL. This proves the staging object path but is not a client-facing URL
design; separating internal and public presign endpoints remains future work.
