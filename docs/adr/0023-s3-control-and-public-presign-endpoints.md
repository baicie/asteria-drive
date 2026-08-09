# ADR-0023: Separate S3 Control and Public Presign Endpoints

## Status

Accepted on 2026-08-09.

## Context

Asteria sends file bytes directly between clients and S3. The API also needs a
private, stable S3 endpoint for multipart control calls, readiness, maintenance,
and recovery verification. A single endpoint works in local development, but an
internal service name such as `seaweedfs:8333` is not resolvable by an Internet
client. Rewriting the host after signing is invalid because the host and path are
part of the AWS Signature V4 canonical request.

The application needs to support split-network deployments without making the
public data path a dependency of API readiness or sending server-side control calls
through an Internet-facing gateway.

## Decision

- `ASTERIA_S3_ENDPOINT` remains the private control endpoint.
- `ASTERIA_S3_PUBLIC_ENDPOINT` selects the base endpoint used by the presigner for
  upload-part and download URLs. It defaults to the control endpoint for backward
  compatibility.
- Both clients share the same credentials, region, bucket, path-style choice, and
  checksum capability. The endpoints must route to the same object namespace.
- The API, maintenance worker, and recovery verifier use only the control client.
  Presigning is local computation and does not contact the public endpoint.
- Production configuration requires both endpoint URLs to use HTTPS and rejects
  credentials, query strings, and fragments in either value.
- Signed URLs are returned exactly as produced. Proxies must preserve the signed
  host and path; application code must not rewrite them after signing.

## Consequences

Private cluster DNS can now be used for control traffic while clients receive URLs
on a separately routed hostname. A failure of the public gateway will not make the
S3 readiness probe fail, so the platform must monitor and test that path separately.

This repository change is necessary but insufficient for the public-production
gate. Each production overlay must prove that the public hostname has trusted DNS
and TLS, routes to the same bucket namespace, preserves Signature V4 inputs,
enforces the intended CORS policy, and succeeds for real external multipart upload,
download, Range, and expiry tests. Until that evidence exists, the project must not
claim an externally valid presign endpoint.

## Alternatives considered

- Rewrite internal URLs at the API or ingress: rejected because it breaks Signature
  V4 or requires unsafe re-signing outside the storage adapter.
- Route all control traffic through the public gateway: rejected because it expands
  exposure and couples readiness to the Internet data path.
- Proxy file bytes through the API: rejected because it violates the control/data
  plane boundary and makes API memory and bandwidth scale with file size.
