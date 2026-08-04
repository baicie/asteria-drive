# ADR-0016: GitHub Actions CI Trust Boundary and Merge Gates

## Status

Accepted on 2026-08-04.

## Context

Asteria Drive is hosted in a public GitHub repository. The repository currently
has no committed GitHub Actions workflow, the main branch has no branch
protection, and Actions accepts all third-party actions without SHA pinning.

The codebase already has several test boundaries that must remain distinct:

- deterministic memory and HTTP tests;
- PostgreSQL Repository and migration tests;
- SeaweedFS S3 contract tests;
- live HTTP plus PostgreSQL plus SeaweedFS tests;
- Go static checks and OpenAPI validation.

The CI system must make these boundaries observable and enforceable without
exposing credentials, depending on production services, or silently accepting
skipped integration tests.

## Decision

Use GitHub Actions as the CI control plane with four stable required PR checks:

- CI / quality: formatting, module verification, unit/component tests, vet, and
  build;
- CI / race: Linux race detector over the deterministic test suite;
- CI / integration: isolated PostgreSQL and SeaweedFS services followed by the
  Repository, StorageProvider, and live HTTP tests;
- CI / api-contract: OpenAPI lint and API compatibility/route checks.

Run the same required checks on pushes to main. Run dependency/security checks
on pull requests, pushes, and a weekly schedule. Release workflows are separate,
tag-triggered, and are not part of this initial CI implementation.

All pull-request workflows use the pull_request event and receive no
repository secrets. Integration tests use only disposable credentials from the
checked-in Compose topology. Production endpoints and deployment credentials are
never valid CI inputs.

Third-party Actions and CI container images are pinned to immutable commit SHAs
or digests. Workflow permissions default to contents: read; elevated
permissions are scoped to the individual CodeQL or release job. Concurrency
cancels obsolete pull-request runs, while main-branch and release runs are not
cancelled.

The integration job must fail fast when required environment variables are
missing and must verify the named integration tests completed with pass, not
skip, in go test -json output. A test may be skipped only in local runs where
the external dependency gate is intentionally absent.

Protect main with required pull-request review, conversation resolution,
up-to-date required checks, no force pushes, and no direct pushes. Require the
four stable checks above before merge. Use squash merge and delete merged
branches.

## Consequences

The fast deterministic checks provide feedback without containers, while the
integration check proves the real PostgreSQL/S3 protocol boundary. CI becomes
reproducible on fresh runners and does not depend on a developer's local Docker
state.

The required integration job costs more runner time and needs explicit cleanup.
The system therefore stores logs and test reports only as short-lived artifacts,
and records duration/flakiness metrics before adding stricter coverage
thresholds. A release artifact and deployment pipeline remain separate concerns.

## Alternatives considered

- Use only go test ./...: rejected because external tests intentionally skip
  without service variables and would not prove PostgreSQL/S3 behavior.
- Run integration tests against shared hosted services: rejected because shared
  state, credentials, and network availability make PRs nondeterministic.
- Use pull_request_target to access secrets: rejected because it executes
  untrusted fork code with the target repository's privilege boundary.
- Add a single all-in-one workflow: rejected because a slow or flaky integration
  job would hide the status of fast quality checks and make branch protection
  difficult to reason about.
