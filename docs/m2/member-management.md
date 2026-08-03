# M2-2 Tenant Member Management

This document defines the implementation contract for tenant member lifecycle
management. It follows [ADR-0015](../adr/0015-tenant-member-lifecycle.md).

## Scope

Included:

- tenant-scoped member listing;
- owner/admin role and active/suspended status changes;
- in-memory and PostgreSQL Repository adapters;
- last-active-owner protection;
- HTTP/OpenAPI contract and regression tests.

Deferred:

- invitations and email delivery;
- self-registration or external identity provisioning;
- member deletion;
- fine-grained ACLs, groups, and audit export.

## API contract

`GET /api/v1/tenant/members`

- Requires an authenticated active member with owner or admin role.
- Supports `limit` from 1 through 200 (default 50) and an opaque cursor bound to
  tenant and the member-list scope.
- Returns `{data: [...], page: {next_cursor: "..."}}`, matching the existing
  namespace and recycle-bin list response shape.
- Each item contains `principal_id`, `display_name`, `role`, and `status`.
- Ordering is by `principal_id` to keep pagination stable while names or roles change.

`PATCH /api/v1/tenant/members/{principal_id}`

- Requires owner/admin role and a tenant-scoped principal ID in the path.
- The body must contain at least one of `role` or `status`; unknown fields are
  rejected. No request field can change tenant or principal identity.
- Owner may change any member. Admin may not target an owner and may not set role to
  owner. The actor cannot bypass these rules by targeting itself.
- A final active owner cannot be suspended or demoted. Such a request returns
  `409 invalid_state` and leaves the row unchanged.
- Success returns the updated member envelope with `200`.

## Application and Repository boundaries

The service validates IDs, roles, statuses, and patch shape, and supplies the
authenticated actor role to the Repository command. The Repository performs the
tenant filter, target lookup, role authorization defense-in-depth, and atomic owner
invariant check.

The PostgreSQL adapter updates `tenant_member` in one transaction. It locks the
tenant row first, locks the actor and target membership rows, counts active owners,
and rejects an update that would leave zero active owners. The in-memory adapter
holds its repository mutex over the equivalent operations.

OIDC resolution continues to reject suspended members, so a status update affects
new requests without changing the JWT verifier or trusting token role claims.

## Verification matrix

- owner lists and updates members;
- admin lists and updates non-owner members;
- editor/viewer receive `403`;
- admin cannot modify or create an owner relationship;
- final-owner suspend/demotion is rejected;
- suspended members cannot authenticate through OIDC;
- foreign-tenant principal IDs are not mutable;
- concurrent updates preserve at least one active owner;
- memory and PostgreSQL adapters satisfy the same contract.
