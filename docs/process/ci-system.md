# Asteria Drive CI System Design

This document turns ADR-0016 and ADR-0017 into an implementation contract. Both
are accepted. Rollout steps 1 through 6 are implemented in the repository: it has
pinned quality, race, API-contract, Compose-backed integration, security, and
tagged release workflows, plus a Dependabot update path and protected main-branch
merge gates. Release publication remains gated by the configured `release`
environment.

## 1. Current state

- The repository is public and main is the default branch.
- `.github/workflows/ci.yml` runs four secret-free PR gates, all required for
  pull requests targeting protected `main`.
- `.github/workflows/security.yml` runs govulncheck, dependency review, and
  CodeQL without repository secrets; `.github/dependabot.yml` covers Go, npm,
  GitHub Actions, and Compose/Docker dependencies.
- `go.mod` declares Go 1.25; quality and API checks use patched minimum Go
  1.25.13,
  while race uses the current Go 1.26.6. Automatic toolchain downloads are
  disabled in every job.
- Contract tooling is locked by `package-lock.json` to Node.js 24.16.0,
  Action Validator 0.6.0, and Redocly CLI 2.43.3.
- `compose.yaml` pins PostgreSQL 17.5 and SeaweedFS 3.85 by image digest and
  exposes them only on host ports 15432 and 18333.
- PostgreSQL tests use ASTERIA_TEST_DATABASE_URL.
- SeaweedFS tests use ASTERIA_TEST_S3_ENDPOINT,
  ASTERIA_TEST_S3_ACCESS_KEY, ASTERIA_TEST_S3_SECRET_KEY,
  ASTERIA_TEST_S3_REGION, and ASTERIA_TEST_S3_BUCKET.
- Live HTTP tests reuse the PostgreSQL and S3 variables and create isolated
  PostgreSQL schemas and S3 buckets.
- Integration tests intentionally skip when their external dependency variables
  are absent. CI must set and validate them explicitly.

## 2. Workflow topology

The target topology contains these workflows. Rows marked planned are not yet
implemented:

| Workflow | Trigger | Merge-gate status | Responsibility |
| --- | --- | --- | --- |
| ci.yml | pull request, push to main, manual | required on protected main | quality, race, API contract, and integration implemented |
| security.yml | pull request, push to main, weekly schedule, manual | required on protected main | govulncheck, dependency review, CodeQL |
| release.yml | v* tag, manual | protected release environment | reproducible binaries, checksums, SBOM and provenance |
| dependabot.yml | GitHub configuration | n/a; implemented | Go modules, npm, Actions, and Docker update proposals |

The workflows should not duplicate business commands. Each job calls commands
documented in this repository and, where shell logic is non-trivial, a
repository-owned helper that emits structured output.

## 3. Required CI PR graph

The four implemented CI jobs run independently. Together with the three security
jobs in section 4, branch protection blocks a pull request targeting `main` unless
all seven required checks succeed and the required review policy is satisfied.

### CI / quality

Runner: Ubuntu, with the toolchain selected from the repository toolchain policy.

Steps:

1. checkout with persist-credentials: false;
2. verify go.sum with go mod verify;
3. fail if gofmt -l reports a Go file;
4. run go test ./... -count=1;
5. run go vet ./...;
6. run go build ./...;
7. run an event-aware `git diff --check` against the pull-request base, push
   predecessor, or manual run predecessor; checking the empty working tree is
   insufficient on a clean runner.

The job uploads a coverage profile and go test -json output for seven days.
No global coverage percentage is enforced until a baseline is recorded and a
ratcheting threshold is approved.

### CI / race

Runner: Ubuntu with CGO and GCC enabled.

Run go test -race ./... -count=1. This job uses the deterministic memory and
HTTP suite. It does not depend on PostgreSQL or S3; database and object-store
concurrency are covered by the integration job.

### CI / integration

Runner: Ubuntu 24.04 with Docker Compose v2 and Go 1.26.6. The job timeout is
25 minutes and it does not reference GitHub Secrets; all credentials are
disposable values scoped to the isolated Compose project.

1. Validate all required test variables and `docker compose config` before
   starting dependencies.
2. Start `compose.yaml` with `docker compose up -d --wait --wait-timeout 180`;
   PostgreSQL and SeaweedFS images are pinned to these exact digests:

   `postgres:17.5-alpine@sha256:6567bca8d7bc8c82c5922425a0baee57be8402df92bae5eacad5f01ae9544daa`

   `chrislusf/seaweedfs:3.85@sha256:49312939c00c01e5ee6afbd7d728b18027821d3764c35a797a72acd4fdf3296a`

3. Run the PostgreSQL, S3, and server packages serially:

   ```text
   go test -json -p=1 -count=1 -timeout=15m ./internal/postgres ./internal/s3store ./internal/server
   ```

   The committed manifest names 19 required tests: 16 PostgreSQL, one
   SeaweedFS, and two live HTTP tests.
4. Parse the JSON report structurally. Every required package and test must end
   in `pass`; any observed `skip`, missing test, package failure, or malformed
   report fails the job.
5. Under `always()`, sanitize the JSON report and Compose logs, remove the raw
   report, run `docker compose down -v --remove-orphans`, and upload only the
   sanitized evidence with seven-day retention.

The test database creates an isolated schema per test. The S3 tests use a unique
bucket and clean their objects. CI must never reuse a developer or production
endpoint.

### CI / api-contract

Steps:

- validate `.github/workflows/ci.yml` against the locked GitHub Actions schema
  (implemented);
- lint docs/openapi.yaml with the locked Redocly CLI version (implemented);
- verify that every HTTP route has a documented operation and that every
  documented operation is registered by the server (implemented);
- compare the pull-request OpenAPI document with the base document using the
  repository-owned compatibility checker (implemented);
- fail on removed operations, responses, parameters, or response schema fields;
  any OpenAPI file change also requires an ADR (implemented).

The CLI version and any Node package lockfile belong in the repository. Avoid
floating latest downloads in workflow files.

## 4. Security workflow and dependency updates

`security.yml` runs without repository secrets on pull requests, pushes to main,
weekly at 03:17 UTC on Mondays, and manual dispatches:

- `Security / govulncheck` runs the explicitly pinned
  `golang.org/x/vuln/cmd/govulncheck@v1.1.4` against the resolved module graph
  with both patched minimum Go 1.25.13 and current Go 1.26.6;
- `Security / dependency-review` runs only for pull requests and blocks newly
  introduced high or critical dependency vulnerabilities;
- `Security / codeql` analyzes Go with the pinned CodeQL action and grants only
  `security-events: write` in that job.

Every Action is pinned to a full commit SHA, checkout credentials are discarded,
and no job reads repository secrets. All three security checks are required for
pull requests targeting protected `main`. Secret scanning, push protection,
dependency graph, vulnerability alerts, and Dependabot security updates are
enabled in repository settings. The workflow must use `pull_request`, never
`pull_request_target`.

`dependabot.yml` checks the Go module graph, npm lockfile, GitHub Actions, and
Compose/Docker references weekly, with at most five open update pull requests
per ecosystem.

All workflows should declare explicit top-level permissions. The default is:

    permissions:
      contents: read

Only CodeQL receives security-events: write. Only a future release job may
receive id-token: write, attestations: write, or contents: write.

## 5. Toolchain, action, and cache policy

- Keep the Go language directive and the supported compiler policy separate.
  Quality and API checks use patched minimum Go 1.25.13; race also covers the
  current Go 1.26.6. ADR-0017 records the current compiler-floor increase; raising
  either pin again requires an explicit policy update.
- Use actions/setup-go pinned to its v5.5.0 commit with go.sum-based caching.
- Set GOTOOLCHAIN=local so CI does not silently download a different compiler.
- Use Node.js 24.16.0 and Redocly CLI 2.43.3 from the committed npm lockfile.
- Pin every third-party Action to a full commit SHA and every external container
  to a digest. Dependabot updates those pins.
- Do not cache PostgreSQL, SeaweedFS, or test buckets between jobs.
- Use workflow concurrency groups named from workflow and pull request number,
  with cancellation enabled only for pull requests.

## 6. Branch protection and repository settings

The rollout step 5 policy is applied to `main`:

- require pull requests with at least one approving review;
- dismiss stale approvals and require approval after the last push;
- require conversation resolution and the seven stable CI/security checks;
- require the branch to be up to date before merge;
- enforce the policy for administrators and require CODEOWNERS review;
- disable force pushes and branch deletion.

CODEOWNERS covers the repository by default and calls out workflow, ADR/process,
and infrastructure paths explicitly. Linear-history enforcement remains a
separate follow-up decision. Do not allow an administrator bypass for
security-sensitive workflow or migration changes without an explicitly recorded
exception.

## 7. Artifacts and failure handling

Upload only short-lived, non-secret artifacts:

- sanitized integration `go test -json`, deterministic-suite JSON, and coverage
  profiles;
- OpenAPI lint output;
- sanitized Compose logs;
- benchmark output when a benchmark job is explicitly requested.

Never upload environment files, Authorization headers, signed URLs, database
URLs, or full request bodies. Required jobs must not retry failed tests
automatically; re-running the workflow is the diagnostic action. A flaky test
may be quarantined only with an issue, owner, expiry date, and a visible
non-passing status.

Target operational budgets:

- quality feedback p95 below 8 minutes;
- all required PR checks p95 below 15 minutes;
- integration failure logs available within 2 minutes after cleanup;
- no unowned quarantine item older than 14 days.

## 8. Release boundary

ADR-0018 defines the release format. `release.yml` builds the server and
migration binaries for Linux `amd64` and `arm64` from a clean tag checkout,
produces deterministic archives, a sorted SHA-256 manifest, an SPDX JSON SBOM,
and a release manifest. The tag must be reachable from protected `main`.

The publish job is separated from the build job, requires the `release`
environment, attaches GitHub OIDC provenance to checksum subjects and the
published OCI digest, and publishes an immutable GitHub Release plus a
multi-architecture GHCR image. Pull requests never publish or attest release
artifacts. Deployment automation remains outside this rollout.

## 9. Rollout sequence

1. Completed: review and accept ADR-0016; choose supported Go toolchain versions.
2. Completed: add pinned tool/action manifests and the quality, race, and
   API-contract jobs.
3. Completed: add Compose-backed integration, structured pass/skip detection,
   sanitized evidence retention, and unconditional resource cleanup.
4. Completed: enable security scanning and Dependabot; review the first baseline.
5. Completed: apply main branch protection using the stable check names.
6. Completed in repository: define the artifact format and add deterministic
   binaries, checksums, SBOM, OIDC provenance, and protected publication.

## 10. Rollout steps 2 through 6 acceptance

- Pull requests, pushes to main, and manual runs create stable `CI / quality`,
  `CI / race`, `CI / api-contract`, and `CI / integration` checks without
  repository secrets; all four are required for pull requests targeting `main`.
- Quality verifies modules and formatting, runs tests, vet and build, checks the
  diff, and retains test/coverage evidence for seven days.
- Race runs the deterministic suite with the Linux race detector.
- API contract installs only locked npm dependencies, validates the workflow
  schema, lints OpenAPI, compares the pull-request document with its base, and
  checks exact equality between documented and registered HTTP operations.
- Workflow permissions are read-only, checkout credentials are not persisted,
  pull-request concurrency is cancellable, and third-party Actions use full SHAs.
- Integration uses disposable repository-owned values rather than GitHub Secrets
  and starts the digest-pinned PostgreSQL and SeaweedFS services with health
  checks.
- Its manifest requires 19 tests: 16 PostgreSQL, one SeaweedFS, and two live
  HTTP tests. Structured report verification requires package and test `pass`
  actions and rejects every `skip`.
- Sanitized test and Compose evidence is retained for seven days. Sanitization,
  raw-report removal, `docker compose down -v --remove-orphans`, and artifact
  upload all run through `always()` paths.
- The seven stable CI/security checks are required for pull requests targeting
  protected `main`; the protection policy uses strict up-to-date checks.
- Security checks run without repository secrets, use pinned Action/tool versions,
  and keep CodeQL's write permission scoped to its job.
- Dependabot proposes weekly updates for Go, npm, Actions, and Compose/Docker
  dependencies; it does not itself grant merge or branch permissions.
- Release tags are validated against protected `main`; pull requests cannot
  publish artifacts.
- Release archives contain both control-plane binaries and README metadata for
  Linux `amd64` and `arm64`, with deterministic timestamps and embedded build
  metadata.
- The release bundle contains a manifest, sorted SHA-256 checksums covering the
  bundle, and an SPDX JSON SBOM.
- Only the protected `release` environment may publish, with write and OIDC
  permissions limited to the publish job.
- The published GHCR image is built for Linux `amd64` and `arm64`, carries the
  source-commit tag, and receives a provenance attestation for its manifest
  digest. Deployments pin that digest rather than a mutable tag.

## 11. Definition of done for the full CI system

- A fork pull request can run all required checks without secrets.
- Every required integration test passes instead of being skipped.
- A clean runner can reproduce quality, race, migration, S3, and HTTP checks.
- Main cannot merge while any required check is red or missing.
- Workflow and dependency changes receive required CODEOWNERS review.
- Logs and artifacts contain no credentials or signed URLs.
- A weekly security run and dependency update path are observable.
