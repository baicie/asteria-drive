# ADR-0018: Release Artifact Format and Provenance Boundary

## Status

Proposed on 2026-08-05.

## Context

The repository has a tested service and migration binary, but it does not yet
define a reproducible release format. A release must be inspectable without a
checkout, must not publish from a pull request, and must let consumers verify
both the artifact digest and the source commit. The project does not publish a
container image in this phase.

## Decision

Release tags use the form `vMAJOR.MINOR.PATCH` with an optional prerelease or
build suffix. A tag is eligible only when its commit is an ancestor of the
protected `main` branch. Each release produces two deterministic Linux archives
for `amd64` and `arm64`:

```text
asteria-drive_<version>_linux_<arch>.tar.gz
```

Each archive contains `asteria-server`, `asteria-migrate`, and `README.md` at
the archive root. Binaries are built with `CGO_ENABLED=0`, `-trimpath`,
`-buildvcs=false`, and embedded version, source commit, and UTC build metadata.
Archive entry order, modes, ownership, and timestamps use stable values derived
from `SOURCE_DATE_EPOCH`.

The release directory also contains:

- `release-manifest.json`, identifying the version, source commit, source epoch,
  and archive names;
- `checksums.txt`, a sorted SHA-256 manifest covering every release file except
  itself;
- an SPDX JSON SBOM generated from the release directory.

The workflow builds and scans artifacts in a read-only job. A separate publish
job is gated by the `release` environment and is the only job with
`contents: write`, `id-token: write`, and `attestations: write`. GitHub OIDC
build provenance is attached to every release file before the immutable GitHub
Release is created. Pull requests never publish or attest release artifacts.

## Consequences

Consumers can select an architecture, verify the archive and SBOM against the
checksum manifest, and inspect the embedded source commit. Release publication
requires an explicitly protected environment and a tag reachable from `main`.
The first release workflow does not deploy infrastructure or create a Docker
image; those require separate deployment and container decisions.

## Alternatives considered

- Publish directly from every push: rejected because it would make unreviewed
  pull-request code a release input.
- Use a mutable archive name such as `latest`: rejected because it prevents
  reliable digest and provenance verification.
- Add a Docker image in this rollout: rejected because no container runtime
  contract or deployment environment is defined yet.
