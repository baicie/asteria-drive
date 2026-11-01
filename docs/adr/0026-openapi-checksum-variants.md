# ADR-0026: OpenAPI Checksum Variant Clarification

## Status

Proposed on 2026-11-01.

## Context

The OpenAPI `Checksum` schema previously used two `oneOf` variants:

- an object with optional `algorithm` and `value`;
- a SHA-256 object requiring both fields.

Because the first variant did not require any property, the empty object `{}`
matched both variants. Redocly's `no-illogical-composition-keywords` rule
therefore reported an overlapping-schema warning. This was a documentation
contract defect, not an HTTP handler bug: `drive.ValidChecksum` already accepts
three distinct forms:

- the zero-value empty object `{}`;
- an explicitly empty pair `{"algorithm":"","value":""}`;
- an explicitly declared SHA-256 pair.

## Decision

Describe the three forms as mutually exclusive `oneOf` variants:

1. empty object: `maxProperties: 0`;
2. explicit empty pair: both properties required and constrained to `""`;
3. SHA-256 pair: both properties required, `algorithm: sha256`, and the existing
   base64-encoded 32-byte digest constraints.

No HTTP, database, storage, or UI behavior changes. No synthetic `4xx` probe
responses or license metadata are added; those remaining Redocly warnings are
intentional and recorded in the MVP implementation log.

## Consequences

- The OpenAPI document now matches `ValidChecksum` without ambiguity.
- Clients can continue sending any of the three existing accepted forms.
- The schema is not made stricter for wire requests; only the machine-readable
  contract is disambiguated.
- New checksum representations still require a contract change and tests.

## Alternatives considered

- Require `algorithm` and `value` on the empty variant: rejected because it would
  document a rejection for `{}` even though the Go validator accepts it.
- Disable the Redocly rule globally: rejected because it would hide future
  `oneOf` mistakes.
- Remove the empty-object form from the API: rejected because it would be a
  wire-contract change with no product requirement.
