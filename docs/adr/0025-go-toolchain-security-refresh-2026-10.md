# ADR-0025: 2026-10 Go Toolchain and Dependency Security Refresh

## Status

Accepted on 2026-10-22.

## Context

The 2026-10 Go vulnerability data introduced reachable standard-library
advisories that affect both existing CI lanes. The symbol-level govulncheck run
on Go 1.25.13 reported eleven reachable findings, including:

- GO-2026-6809 in `crypto/x509`, fixed in Go 1.26.10 and Go 1.27.3;
- GO-2026-6617, GO-2026-6613, GO-2026-6612, GO-2026-6611, GO-2026-6610,
  GO-2026-6605, and GO-2026-6603 in `net/http`, fixed in Go 1.26.9 and Go 1.27.2;
- GO-2026-6608 in `net/textproto`, fixed in Go 1.26.9 and Go 1.27.2;
- GO-2026-6607 in `crypto/tls`, fixed in Go 1.26.9 and Go 1.27.2;
- GO-2026-6629 in `golang.org/x/text` v0.40.0, fixed in v0.41.0.

The Go 1.25 line is no longer covered by those fixes. Keeping the old floor
would require either accepting reachable vulnerabilities or permanently
exception-listing them. The same review also found the high-severity npm advisory
GHSA-68fv-2mgg-jv7q in the transitive `source-map-js` 1.2.1 used by the web
build; 1.2.2 contains the fix.

## Decision

- Keep the module directive at Go 1.25.0; it is a language-version declaration,
  not the supported compiler floor.
- Raise the supported and minimum CI compiler to patched Go 1.26.10.
- Use Go 1.27.3 for the current-toolchain, race, integration, release, and
  container-builder lanes.
- Pin the builder image to
  `golang:1.27.3@sha256:f20d83a83e18dbd2a4714b8ffd4b30b287b6243962dd75608e86abcc63ba4efc`.
- Upgrade `golang.org/x/text` to v0.41.0.
- Override the transitive web dependency `source-map-js` to 1.2.2 so the
  lockfile resolves only the patched version.
- Continue running the pinned `golang.org/x/vuln/cmd/govulncheck@v1.1.4` command
  against both Go lanes. Do not add advisory exclusions.

## Consequences

Clean development and CI runners now require Go 1.26.10 or newer. The Docker
builder, release workflow, and current-toolchain lane move to Go 1.27.3. This is a
compatibility change for contributors, but it does not change HTTP, storage, or
migration contracts.

The dependency-review gate remains fail-closed for high-severity findings. Any
future compiler-floor increase needs another explicit policy update rather than
an implicit change in a feature pull request.

## Alternatives considered

- Keep Go 1.25.13 and add exclusions: rejected because the vulnerabilities are
  reachable and fixes are available.
- Move only the minimum lane: rejected because the current lane and release
  builder would remain vulnerable.
- Wait for a scheduled dependency update: rejected because required pull-request
  security checks would stay red for every change.
