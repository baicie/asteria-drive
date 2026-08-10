# Docker staging deployment

This target deploys the immutable `v0.1.1` image to one selected Docker host. It is
an operational staging target, not the production topology in
`docs/operations/deployment.md`.

The API (`18080`), S3 diagnostic (`18333`), and metrics (`19090`) ports bind only to
host loopback. PostgreSQL is reachable only on an internal Docker network.
Application credentials are generated once by `scripts/bootstrap-staging-host.sh`
and remain in server-managed Docker volumes. The same explicit root bootstrap
creates a staging-only PostgreSQL private CA and leaf certificate. The GitHub
workflow receives only a dedicated SSH key and pinned host key; it never receives a
database URL, password, PEM body, or private key.

The deploy key is forced through a root-owned dispatcher. It can upload only the
reviewed Compose file and deployment script whose SHA-256 values were pinned during
bootstrap, run that deployment, fetch the two sanitized evidence files, and remove
its own run directory. It can also execute one digest-pinned, root-owned read-only
monitor with a validated GitHub run identity. It cannot request an arbitrary shell,
SFTP session, or port forward.

The deployment uses trusted development authentication, a host-local private CA for
PostgreSQL, and a non-TLS internal S3 link. PostgreSQL TLS does not satisfy the
production OIDC, externally governed database PKI, PITR/WAL, Object Lock,
secret-controller, independent review, high availability, or public-ingress gates.
Do not expose ports `18080`, `18333`, or `19090` publicly. The boundary and PKI
decision are recorded in
[ADR-0024](../../../docs/adr/0024-staging-postgresql-private-pki.md).

The CA private key exists only as root-owned mode `0400` material at
`/etc/asteria-drive/staging-postgres-pki/issuer/ca.key`. The application volume
contains the public CA and `database-url-tls`; the PostgreSQL volume contains the
public CA, leaf certificate/key, and HBA policy. The leaf is valid only for
`DNS:postgres`. PostgreSQL requires at least TLS 1.2, rejects all plaintext TCP
connections, and uses SCRAM-SHA-256 for TLS TCP clients. Bootstrap refuses partial
PKI state and does not rotate or repair it implicitly.

The legacy `database-url` remains in the application secret volume only so the
previous active Compose definition can be restored if the first TLS candidate
fails. The `v0.1.1` API, migration, and verifier all read `database-url-tls` with
`sslmode=verify-full`. The API retains the staging `development` boundary; migration
runs with `ASTERIA_ENV=production` to exercise the released DSN guardrail.

The pinned container image's `/entrypoint.sh` prepends
`-master.volumePreallocate`, `-master.volumeSizeLimitMB=1024`, and
`-volume.max=0` when its `server` command is used. This target explicitly overrides
those image-level arguments, advertises the stable `seaweedfs` service identity,
limits each of eight slots to 256 MiB, and marks storage read-only below 5 GiB free
space. The deployment also checks the root filesystem, Docker data root, and both
application data volumes before accepting an 85% usage / 5 GiB reserve guard. These
limits protect the shared staging host; they are not a production capacity plan.

The `v0.1.1` binary can separate S3 control and public presign endpoints, but this
Compose target intentionally leaves the public endpoint unset. It therefore uses
`http://seaweedfs:8333` for both operations and presigned URLs. The deployment smoke
test reaches that exact signed host through the loopback mapping with
`curl --connect-to`; it does not turn the URL into a public client endpoint. A
platform overlay still needs a client-visible HTTPS endpoint, trusted DNS/TLS,
CORS, and real external transfer evidence before serving public clients.

The `Deploy staging` workflow verifies the `v0.1.1` OCI attestation, deploys the exact
image digest, runs forward-only migrations, checks health/readiness, performs an
authenticated multipart upload and byte-for-byte download, scrapes metrics, requires
the storage verifier to inspect at least one healthy object, and uploads secret-free
JSON evidence. Before and after activation it verifies certificate chain, host name,
validity, file metadata, HBA/TLS settings, SCRAM storage, an API session in
`pg_stat_ssl`, negotiated protocol/cipher/bits, and plaintext rejection. It retains
only certificate SHA-256 values and bounded TLS metadata. Raw plaintext-test stderr
is deleted immediately after its presence and SHA-256 are recorded.
All three workflows keep remote stdout and stderr outside their artifact directories,
validate duplicate keys, the exact field set, types, identities, and bounded values,
then reserialize accepted JSON. Quarantined bytes are deleted on every exit path;
collection artifacts expose only presence, byte count, and SHA-256 metadata for
rejected output.

If a candidate fails after changing containers, the deployment starts PostgreSQL,
SeaweedFS, and the API from the previous active Compose file with `--pull never`, then
rechecks readiness and both the fixed image reference and local config ID. If there
is no previous Compose, it removes only candidate containers and networks and never
deletes the persistent data or secret volumes. Deployment evidence uses
`asteria-drive-staging-deployment/v2`, records the failed phase and rollback booleans,
and never archives the old or new DSN.

The `Monitor staging` workflow runs every hour and on manual dispatch from protected
`main`. It shares the deployment concurrency group, uses the same strict SSH trust
boundary, and retains sanitized capacity, image, container-health, endpoint, metrics,
and loopback-binding evidence for 30 days. A threshold or probe failure makes the
Actions run fail. This is a staging failure signal, not a production monitoring,
SIEM, paging, or capacity-planning system. Monitor evidence uses
`asteria-drive-staging-monitor/v2` and independently repeats the PostgreSQL TLS
session, certificate, SCRAM, and plaintext-rejection checks.

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
remote stderr) with schema `asteria-drive-staging-recovery/v2`. Its identity includes
the GitHub run/attempt, workflow SHA, pinned Compose and image digests, and
`claim_boundary=staging-recovery-not-production`. It records backup catalog and
source-stability checks, restore/schema/table/row comparisons, archive size and
SHA-256, verifier checked/healthy/finding counts, recovered API checks, cleanup,
timing, and capacity thresholds. It also proves that the source API has at least one
TLS PostgreSQL connection and records the negotiated version, cipher, and bit
strength. A successful report requires
`status=success`, `last_phase=complete`, every positive check field to be `true`,
and both `object_versions_restored=false` and `pitr_wal_replayed=false`.

This drill proves only a staging logical metadata restore, source-side private-CA
PostgreSQL TLS, and a read-only check against the existing object store. Its isolated
temporary restore database is not production TLS evidence. The drill does not prove
PostgreSQL PITR/WAL replay, object versioning or Object Lock, encrypted production
backups, production RPO/RTO, public TLS ingress, HA, or production monitoring and
approval.
