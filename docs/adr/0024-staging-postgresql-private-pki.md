# ADR-0024: Staging PostgreSQL Private PKI and TLS Evidence

## Status

Accepted on 2026-08-10.

## Context

ADR-0022 introduced a single-host staging deployment whose PostgreSQL connection
was intentionally local and unencrypted. The production configuration boundary now
requires a file-sourced DSN with exactly one `sslmode=verify-full`, and release
`v0.1.1` contains that guardrail. Leaving staging on plaintext would avoid exercising
the released validation path and would leave transport, host-name verification, and
credential authentication unmeasured.

The staging host has no platform PKI or managed certificate controller. GitHub
Actions must not receive a database URL, password, PEM body, or issuing key, and a
failed candidate rollout must not strand the existing staging service after its
dependencies have changed.

This ADR supersedes only ADR-0022's decision to use a local non-TLS PostgreSQL link.
Its trusted-development identity, loopback exposure, single-host topology, and
`staging-not-production` claim boundary remain unchanged. ADR-0021 continues to
define the production database and recovery requirements.

## Decision

An explicit root bootstrap creates a staging-only private PKI on the selected host:

- a 3072-bit RSA private CA with a ten-year certificate, explicit critical
  `CA:TRUE` basic constraints, and `keyCertSign,cRLSign` key usage;
- a 3072-bit RSA PostgreSQL leaf certificate valid for 397 days, with
  `serverAuth` extended key usage and exactly `SAN=DNS:postgres`;
- a root-owned issuer directory at
  `/etc/asteria-drive/staging-postgres-pki/issuer`; and
- a PostgreSQL HBA policy that rejects every TCP plaintext connection and permits
  TCP TLS connections only with `scram-sha-256` authentication.

The CA private key remains only at
`/etc/asteria-drive/staging-postgres-pki/issuer/ca.key`, owned by root with mode
`0400`. It is not copied into a Docker volume, workflow input, Actions secret, or
evidence artifact. The application secret volume receives only the public CA and a
`database-url-tls` file. The PostgreSQL secret volume receives the public CA, leaf
certificate, leaf private key, and HBA file with access limited to root and the
fixed PostgreSQL image group. Bootstrap creates this material only when the complete
set is absent and refuses partial state; repair and rotation must be explicit,
reviewed operator actions.

The existing `database-url` file is retained only so the previous active Compose
definition can be restored automatically if a candidate rollout fails. Every
`v0.1.1` candidate component uses `database-url-tls`, containing
`sslmode=verify-full`, the mounted CA path, and
`application_name=asteria-drive-staging-api`. The API remains in `development` with
trusted-development authentication, while the one-shot migration runs with
`ASTERIA_ENV=production` so the release's production DSN guardrail is exercised.

PostgreSQL starts with TLS enabled, a minimum protocol of TLS 1.2, the generated
certificate and key, and the reviewed HBA file. Bootstrap and deployment preflight
together verify file ownership and modes, CA/leaf key correspondence, the
certificate chain, the `postgres` host name, server-auth usage, at least 30 days of
remaining leaf validity, the exact DSN parameters, and the stored SCRAM password
form before changing the runtime.

After activation, deployment and monitoring require all of the following:

- at least one API connection visible in `pg_stat_ssl`;
- TLS 1.2 or TLS 1.3, a syntactically bounded cipher name, and at least 128 cipher
  bits;
- the expected PostgreSQL TLS settings and HBA path;
- the exact server-side DSN, including `verify-full`, CA path, service host, and
  application name, without emitting its password;
- the complete parsed IPv4/IPv6 HBA rule set with no ignored parse errors;
- a certificate chain valid for `DNS:postgres`; and
- rejection of an explicit `sslmode=disable` TCP connection.

The plaintext-negative probe writes stderr only to a bounded temporary file. The
probe records whether stderr existed and its SHA-256, then immediately deletes the
raw file. Evidence may contain CA and leaf certificate DER SHA-256 values, SAN,
validity timestamps, protocol, cipher, bit strength, and connection counts. It must
not contain a DSN, password, private key, PEM body, or raw connection error.

Deployment, monitor, and recovery artifacts move to their respective v2 schemas
with strict duplicate-key and exact-field checks. Recovery v2 proves that its
source API is using PostgreSQL TLS before taking a logical backup. Its temporary,
internal restore database remains a staging drill resource; the new source-TLS
fields do not turn that drill into PITR, production TLS, or failover evidence.
Each evidence-producing remote command first places stdout and stderr in
runner-temporary quarantine outside the artifact directory. Its workflow records
only local presence, size, and SHA-256
collection metadata, validates every allowed evidence field and bounded value, and
then reserializes the accepted JSON into the artifact directory. The quarantined
bytes are deleted on both success and failure, so an unknown field or malformed
value cannot be retained by the unconditional artifact-upload step.

If a candidate deployment fails after changing runtime containers, the deployment
script starts PostgreSQL, SeaweedFS, and the API from the previous active Compose
file with `--pull never`, then rechecks readiness and both the previous fixed image
reference and local config ID. The old Compose file is not replaced until every
postflight check and the success evidence write have completed. A failed first
deployment removes candidate containers and networks without deleting secret or
data volumes. The success artifact
must say that rollback was not attempted; failure evidence records the failed phase
and any rollback attempt without exposing diagnostics that may contain secrets.

## Consequences

The staging API candidate encrypts its PostgreSQL TCP traffic, verifies the private
CA and `postgres` service identity, exercises the released `verify-full` gate, and
retains sanitized proof of the negotiated connection. A failed TLS rollout has an
explicit path back to the previously active runtime and legacy DSN.

This is not public-production TLS. The CA and database run on one host, the issuer
is not platform managed, rotation and revocation are not automated, the API still
uses trusted-development authentication, and S3 remains an internal HTTP endpoint.
The evidence does not establish externally trusted DNS or certificates, encrypted
PITR/WAL, object versioning or Object Lock, KMS-backed secret rotation, host-level
HA, public ingress, production monitoring/SIEM, or independent approval.

## Alternatives considered

- Continue using plaintext because the Docker network is internal: rejected because
  network placement does not test transport encryption, host-name verification, or
  the released production DSN guardrail.
- Put the CA private key in a Docker or Actions secret: rejected because neither the
  database runtime nor CI needs issuing authority.
- Replace the legacy DSN immediately: rejected because the active `v0.1.0` Compose
  file needs it for automatic recovery during the first TLS rollout.
- Treat the staging CA as production trust: rejected because a host-local private CA
  has none of the target platform's ownership, rotation, revocation, HA, or external
  trust evidence.
