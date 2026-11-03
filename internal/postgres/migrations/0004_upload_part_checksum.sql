-- Cross-reload resume (ADR-0027) must prove that a part already in object storage holds
-- the bytes of the file the user just re-selected, not merely that some file with the same
-- name, size and lastModified was uploaded. The SHA-256 of each part is that identity.
--
-- The row is written when the client asks for a signature and declares the digest of the
-- part it is about to send. Signing is the right moment: it is the only control-plane call
-- that happens before the bytes leave the browser, so a row means "the client committed to
-- these bytes for this part number". A part found in storage without a matching row has no
-- integrity evidence and must be re-uploaded, never skipped.
--
-- This is deliberately separate from `upload_part`, which is the frozen completion list
-- written once at completion time and whose ON CONFLICT identity must not change.
CREATE TABLE upload_part_signature (
    tenant_id uuid NOT NULL,
    upload_session_id uuid NOT NULL,
    part_number integer NOT NULL CHECK (part_number BETWEEN 1 AND 10000),
    size_bytes bigint NOT NULL CHECK (size_bytes > 0),
    content_sha256 text NOT NULL CHECK (content_sha256 ~ '^[A-Za-z0-9+/]{43}=$'),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, upload_session_id, part_number),
    CONSTRAINT upload_part_signature_session_fk
        FOREIGN KEY (tenant_id, upload_session_id)
        REFERENCES upload_session(tenant_id, id) ON DELETE CASCADE
);
