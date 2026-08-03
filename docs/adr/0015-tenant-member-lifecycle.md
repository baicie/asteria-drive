# ADR-0015: Tenant Member Lifecycle Management

## Status

Accepted for M2-2.

## Context

M2-1 establishes tenant membership, OIDC identity mapping, roles, and active/suspended
status. Membership is currently seeded from configuration, which is sufficient for
bootstrap but does not give tenant administrators a durable management surface.

The next increment needs a small, auditable control-plane API without introducing
invitations, self-registration, fine-grained ACLs, or an external audit export.

## Decision

Add tenant-scoped member lifecycle operations:

- `GET /api/v1/tenant/members` lists active and suspended members using a bounded
  keyset cursor.
- `PATCH /api/v1/tenant/members/{principal_id}` changes role and/or status.
- Only owner and admin principals may use these operations.
- Owners may update any member. Admins may update non-owner members and may not grant
  or assign the owner role.
- The last active owner cannot be suspended or changed to a non-owner role. The
  invariant is checked atomically by the Repository adapter, not by a read-then-write
  service sequence.
- PostgreSQL remains the source of truth. The in-memory repository implements the
  same port contract for deterministic tests.

The member response intentionally omits issuer and subject. Those values remain
internal identity-mapping data and are not needed for lifecycle management.

## Consequences

This creates a useful administrative control plane while preserving the existing
trust boundary: tenant and principal identity still come from authentication and
the repository, never from request JSON. Role/status changes are immediately visible
to subsequent OIDC authentication attempts. An active owner count query/lock is part
of every PostgreSQL member update, so concurrent administrators cannot remove the
last owner.

Invitations, membership deletion, identity registration, audit export, and resource
ACLs remain explicitly deferred.

## Alternatives considered

- Keep configuration-only membership: rejected because production administrators
  cannot rotate access without a deployment.
- Enforce the owner invariant only in HTTP/service code: rejected because concurrent
  requests could both observe two owners and then remove both.
- Add a separate membership microservice: rejected; the control plane is small and
  belongs in the existing modular monolith until measured scale justifies a split.
