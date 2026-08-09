# Security Control Matrix

This matrix maps Phase 1 security claims to checked-in or platform evidence. A
deployment is production-ready only when every platform-owned row is linked to the
target environment's immutable evidence.

| Control | Requirement | Repository evidence | Platform evidence | Frequency |
| --- | --- | --- | --- | --- |
| IAM-01 | Validate OIDC issuer, audience, signature and time claims | `internal/auth`, OIDC negative tests | IdP client and key-rotation record | Every release and IdP change |
| IAM-02 | Resolve tenant roles and status server-side | identity repository/service tests | IdP subject lifecycle procedure | Every release |
| IAM-03 | Protect final owner and governance authorization | governance contract and PostgreSQL concurrency tests | Independent admin review | Every release |
| ACL-01 | Tenant-local inherited allow-only ACL | ACL service/repository matrix tests; ADR-0019 | Access review export | Every release and quarterly |
| AUD-01 | Atomic append-only security audit | transaction tests and database mutation trigger | Export retention and SIEM ingestion | Every release and monthly sample |
| DAT-01 | Tenant-scoped data access | memory/PostgreSQL acceptance tests; file-only production DSN and forced `verify-full` backup/restore contracts | Database role grants, trusted CA/DNS, and measured TLS session evidence | Every release |
| DAT-02 | Private object storage with no API key disclosure | S3 adapter/API tests, split control/public endpoint contract, and recovery verifier | Public endpoint DNS/TLS/CORS test plus bucket policy, versioning, encryption, lifecycle | Every deployment and quarterly |
| SEC-01 | Sensitive values loaded from files/workload identity | configuration negative tests | Secret-controller and rotation evidence | Every deployment and rotation |
| NET-01 | Default-deny workload network | Kubernetes NetworkPolicy contract tests | Rendered overlay and CNI enforcement test | Every deployment |
| RUN-01 | Least-privilege container | Dockerfile and deployment contract tests | Admission-policy result | Every image and deployment |
| REL-01 | Immutable, attributable release | pinned workflow, checksums, SBOM, tag ancestry, binary and OCI provenance | Protected `release` approval record | Every release |
| SCA-01 | Dependency and static analysis gates | govulncheck, dependency review, CodeQL workflows | Green required checks | Every change; weekly scan |
| RES-01 | Recover metadata within accepted RPO/RTO | backup scripts, restore runbook, drill report, staging recovery workflow | Staging artifact `9031929907` proves bounded logical restore only; encrypted PITR/WAL policy and production RPO/RTO remain open | Quarterly and before schema-risk releases |
| RES-02 | Prove metadata/object integrity before traffic | `asteria-verify-storage` tests and staging recovery drill | Staging artifact `9031929907` proves isolated metadata restore and live-object verification; production restore approval remains open | Every restore |
| OBS-01 | Bounded monitoring labels and dependency signals | Prometheus metric tests | Scrape authentication/network restriction and alerts | Every release and deployment |
| DOS-01 | Bounded API and worker workload | request limits, timeouts, leased worker tests | Ingress rate/connection limits | Every release and deployment |

## Evidence rules

- A configuration example is not runtime evidence.
- A smoke run is not a sustained SLO report.
- Backup existence is not restore evidence.
- A mutable image tag is not release identity; record the manifest digest.
- Accepted findings need an owner, expiry, and explicit risk decision in the review
  record. An open high-severity finding blocks production readiness.
