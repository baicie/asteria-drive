# ADR-0019: Production Identity Governance and Resource ACLs

## Status

Accepted for the Phase 1 production-readiness increment on 2026-08-05.

## Context

OIDC identity resolution and tenant roles are implemented, but tenant membership is
still bootstrapped from deployment configuration. There is no invitation lifecycle,
membership removal, group model, resource ACL, or durable audit export. Reapplying
bootstrap configuration currently also overwrites managed member role and status,
which can restore access that an administrator deliberately removed.

## Decision

PostgreSQL remains authoritative for principals, memberships, invitations, groups,
ACL entries, and audit events. Deployment bootstrap is create-only: it may establish
the first membership, but it never changes an existing role or status.

Invitations are tenant-scoped and bound to the configured OIDC issuer and an exact
subject. An owner or admin chooses the initial tenant role. The API returns a
256-bit invitation token once; only its SHA-256 digest is stored. Invitations move
from `pending` to exactly one of `accepted`, `revoked`, or `expired`. Acceptance is
idempotent, creates or reuses the bound principal, and creates the membership in the
same transaction. Email delivery and self-registration remain separate product
work.

Membership deletion removes only the tenant relationship. It preserves the global
principal and historical attribution. An admin cannot delete an owner, and no
operation may remove the last active owner.

Groups are tenant-scoped. Group membership may reference only a member of the same
tenant. Removing a tenant membership cascades its group memberships but does not
delete groups.

Node ACL entries use a principal or group subject and one of three allow-only roles:
`reader`, `contributor`, or `manager`. Grants inherit from the closest node through
its ancestors. `reader` permits reads, `contributor` adds namespace writes, and
`manager` adds delete and ACL management. Tenant owner/admin roles always have full
namespace access. Tenant editor/viewer roles remain implicit root-level contributor
and reader grants, respectively, preserving existing clients while ACLs allow scoped
elevation. ACLs never grant tenant administration permissions and have no deny
entries in this increment.

Every governance mutation and security-relevant namespace mutation emits an audit
event. Events contain bounded identifiers and structured metadata, never bearer
tokens, invitation tokens, signed URLs, object keys, file contents, or credentials.
Governance mutations and their success events are committed atomically. Audit rows
are append-only at both the repository API and database trigger boundary. Owners and
admins can page events and export a bounded time range as NDJSON.

All tenant-owned foreign keys and uniqueness constraints include `tenant_id` where
that is needed to prevent cross-tenant references. Cross-tenant resource lookups
continue to return `not_found`.

## Consequences

Production membership is managed without deployment restarts, revocation survives a
restart, ACL grants can address groups, and security changes have a durable export.
OIDC remains the authentication authority; Asteria stores authorization state and
does not become a password or session provider.

Allow-only ACLs cannot express an exception that subtracts an inherited grant. A
future deny model would require conflict precedence, cache invalidation, and a new
ADR. External email delivery, SCIM/group synchronization, legal-hold retention, and
cross-tenant sharing remain outside this increment.

## Alternatives considered

- Treat bootstrap configuration as continuously authoritative: rejected because a
  restart could silently reverse an emergency suspension or role reduction.
- Put roles and ACLs in JWT claims: rejected because tokens become stale and tenant
  administrators lose immediate revocation.
- Store raw invitation tokens: rejected because a database read would become an
  account-provisioning credential leak.
- Add deny entries now: rejected until precedence and inheritance behavior can be
  specified without ambiguous authorization results.
