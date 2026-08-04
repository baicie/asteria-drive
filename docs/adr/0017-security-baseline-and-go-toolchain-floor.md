# ADR-0017: Security Baseline and Go Toolchain Floor

## Status

Accepted on 2026-08-04.

## Context

Rollout step 4 introduces govulncheck, dependency review, CodeQL, and
Dependabot. The first symbol-level govulncheck baseline found three reachable
advisories in the existing dependency graph:

- GO-2026-5970 in `golang.org/x/text`;
- GO-2026-5764 in the AWS SDK for Go v2 event-stream and S3 modules;
- GO-2026-5004 in `github.com/jackc/pgx/v5`.

The first fixed releases require Go 1.24 or Go 1.25. Keeping the previous Go
1.23 compiler floor would therefore require ignoring reachable vulnerabilities
or keeping the security job permanently red.

The initial scan used the current Go 1.26.5 lane. A follow-up scan of the
previous nominal minimum, Go 1.25.0, also exposed reachable standard-library
advisory GO-2026-4341 in `net/url`; its fix is included in the Go 1.25.6
patch line.

## Decision

Keep the module directive at Go 1.25.0, raise the supported and minimum CI
compiler to patched Go 1.25.12, and retain Go 1.26.5 as the current-toolchain
lane. Upgrade the vulnerable dependencies to at least these fixed versions:

- `golang.org/x/text` v0.39.0;
- `github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream` v1.7.8;
- `github.com/aws/aws-sdk-go-v2/service/s3` v1.97.3;
- `github.com/jackc/pgx/v5` v5.9.2.

Run the pinned `golang.org/x/vuln/cmd/govulncheck@v1.1.4` command as a failing
security check against both supported Go lanes. Do not add advisory exclusions
merely to preserve the earlier toolchain floor. Keep dependency review and
CodeQL as named candidate checks until rollout step 5 defines repository rules.

All security workflow Actions remain pinned to commit SHAs. Workflow-level
permissions remain `contents: read`; `security-events: write` is scoped only to
the CodeQL job. Pull-request workflows continue to receive no repository
secrets.

## Consequences

Developers and clean CI runners now require patched Go 1.25.12 or newer. The
dependency and toolchain upgrades remove the reachable vulnerability baseline
and allow govulncheck to be a meaningful pass/fail signal instead of an
informational exception list.

The higher compiler floor is a compatibility change for contributors, but it
does not change the HTTP or storage contracts. Existing minimum-compiler,
current-compiler, integration, and race tests remain the migration evidence.
Future dependency fixes that require another compiler-floor increase need a
new policy decision or an amendment ADR.

## Alternatives considered

- Keep Go 1.23 and mark govulncheck non-blocking: rejected because it hides
  reachable vulnerabilities behind a permanently tolerated baseline.
- Add advisory exclusions: rejected because all reachable findings have
  available fixes and no repository-specific non-reachability evidence.
- Run security checks only on a schedule: rejected because vulnerable
  dependency changes would not receive pull-request feedback.
