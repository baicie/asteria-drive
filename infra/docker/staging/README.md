# Docker staging deployment

This target deploys the immutable `v0.1.0` image to one selected Docker host. It is
an operational staging target, not the production topology in
`docs/operations/deployment.md`.

The API (`18080`), S3 diagnostic (`18333`), and metrics (`19090`) ports bind only to
host loopback. PostgreSQL is reachable only on an internal Docker network.
Application credentials are generated once by `scripts/bootstrap-staging-host.sh`
and remain in server-managed Docker volumes; the GitHub workflow receives only a
dedicated SSH key and pinned host key.

The deploy key is forced through a root-owned dispatcher. It can upload only the
reviewed Compose file and deployment script whose SHA-256 values were pinned during
bootstrap, run that deployment, fetch the two sanitized evidence files, and remove
its own run directory. It can also execute one digest-pinned, root-owned read-only
monitor with a validated GitHub run identity. It cannot request an arbitrary shell,
SFTP session, or port forward.

The deployment uses trusted development authentication and non-TLS local PostgreSQL
and S3 links. Therefore it cannot satisfy the production OIDC, PITR/WAL, Object Lock,
TLS, secret-controller, independent review, high availability, or public-ingress
gates. Do not expose ports `18080`, `18333`, or `19090` publicly.

The pinned container image's `/entrypoint.sh` prepends
`-master.volumePreallocate`, `-master.volumeSizeLimitMB=1024`, and
`-volume.max=0` when its `server` command is used. This target explicitly overrides
those image-level arguments, advertises the stable `seaweedfs` service identity,
limits each of eight slots to 256 MiB, and marks storage read-only below 5 GiB free
space. The deployment also checks the root filesystem, Docker data root, and both
application data volumes before accepting an 85% usage / 5 GiB reserve guard. These
limits protect the shared staging host; they are not a production capacity plan.

The immutable `v0.1.0` binary uses the internal endpoint `http://seaweedfs:8333` for
both S3 operations and presigned URLs. The deployment smoke test reaches that exact
signed host through the loopback mapping with `curl --connect-to`; it does not turn
the URL into a public client endpoint. A later release must separate internal and
client-facing S3 endpoints before this topology can serve external clients.

The `Deploy staging` workflow verifies the OCI attestation, deploys the exact image
digest, runs forward-only migrations, checks health/readiness, performs an
authenticated multipart upload and byte-for-byte download, scrapes metrics, requires
the storage verifier to inspect at least one healthy object, and uploads secret-free
JSON evidence.

The `Monitor staging` workflow runs every hour and on manual dispatch from protected
`main`. It shares the deployment concurrency group, uses the same strict SSH trust
boundary, and retains sanitized capacity, image, container-health, endpoint, metrics,
and loopback-binding evidence for 30 days. A threshold or probe failure makes the
Actions run fail. This is a staging failure signal, not a production monitoring,
SIEM, paging, or capacity-planning system.

## Staging recovery drill

The `Drill staging recovery` workflow runs weekly and can be started manually from
protected `main`. It uses the `staging` environment and the same
`asteria-drive-staging` concurrency group, so it cannot overlap deployment or
monitoring. The forced-command dispatcher accepts only
`recovery <run-id> <attempt> <workflow-sha> <recovery-script-sha256>` for this
workflow. The recovery script is installed during host bootstrap, and the dispatcher
requires the requested digest to equal its host-side pin before checking root
ownership, mode, and file SHA-256; no application secret is sent to GitHub Actions.

The drill takes a read-only logical `pg_dump` of the active metadata database and
restores it into a run-labelled PostgreSQL volume on a temporary `--internal`
Docker network. It starts a digest-pinned recovered API without publishing any
ports, checks schema and table counts, runs the storage verifier, probes health,
readiness, metrics, and one authenticated read, then removes only the containers,
network, volume, and temporary files carrying the current run labels. Existing
`postgres-data`, `seaweedfs-data`, and secret volumes are not targets for cleanup.

The uploaded `staging-recovery-evidence` artifact is retained for 90 days and
contains one sanitized JSON report (plus, when needed, a SHA-256 file for discarded
remote stderr) with schema `asteria-drive-staging-recovery/v1`. Its identity includes
the GitHub run/attempt, workflow SHA, pinned Compose and image digests, and
`claim_boundary=staging-recovery-not-production`. It records backup catalog and
source-stability checks, restore/schema/table/row comparisons, archive size and
SHA-256, verifier checked/healthy/finding counts, recovered API checks, cleanup,
timing, and capacity thresholds. A successful report requires
`status=success`, `last_phase=complete`, every positive check field to be `true`,
and both `object_versions_restored=false` and `pitr_wal_replayed=false`.

This drill proves only a staging logical metadata restore and a read-only check
against the existing object store. It does not prove PostgreSQL PITR/WAL replay,
object versioning or Object Lock, encrypted production backups, production RPO/RTO,
public TLS ingress, or production monitoring and approval.
