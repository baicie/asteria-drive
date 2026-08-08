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
its own run directory. It cannot request an arbitrary shell, SFTP session, or port
forward.

The deployment uses trusted development authentication and non-TLS local PostgreSQL
and S3 links. Therefore it cannot satisfy the production OIDC, PITR/WAL, Object Lock,
TLS, secret-controller, independent review, high availability, or public-ingress
gates. Do not expose ports `18080`, `18333`, or `19090` publicly.

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
