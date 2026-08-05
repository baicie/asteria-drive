CREATE TABLE idempotency_record (
    tenant_id uuid NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    principal_id uuid NOT NULL,
    scope text NOT NULL CHECK (scope IN ('create_directory', 'create_upload')),
    key_hash char(64) NOT NULL,
    request_digest char(64) NOT NULL,
    state text NOT NULL CHECK (state IN ('pending', 'completed')),
    claim_token uuid,
    resource_id uuid,
    locked_until timestamptz,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, principal_id, scope, key_hash),
    CONSTRAINT idempotency_state_shape CHECK (
        (state = 'pending' AND claim_token IS NOT NULL AND resource_id IS NULL AND locked_until IS NOT NULL) OR
        (state = 'completed' AND claim_token IS NULL AND resource_id IS NOT NULL AND locked_until IS NULL)
    )
);

CREATE INDEX idempotency_record_expiry
    ON idempotency_record(expires_at, tenant_id, principal_id, scope, key_hash);

ALTER TABLE upload_session
    ADD COLUMN cleanup_status text NOT NULL DEFAULT 'none'
        CHECK (cleanup_status IN ('none', 'pending', 'complete')),
    ADD COLUMN maintenance_owner uuid,
    ADD COLUMN maintenance_lease_until timestamptz,
    ADD COLUMN maintenance_not_before timestamptz,
    ADD COLUMN maintenance_attempts integer NOT NULL DEFAULT 0 CHECK (maintenance_attempts >= 0),
    ADD COLUMN maintenance_error_code text NOT NULL DEFAULT '';

CREATE INDEX upload_session_maintenance_claim
    ON upload_session(maintenance_not_before, expires_at, id)
    WHERE status IN ('created', 'uploading', 'completing', 'object_completed')
       OR cleanup_status = 'pending';

ALTER TABLE file_node
    ADD COLUMN maintenance_owner uuid,
    ADD COLUMN maintenance_lease_until timestamptz,
    ADD COLUMN maintenance_not_before timestamptz,
    ADD COLUMN maintenance_attempts integer NOT NULL DEFAULT 0 CHECK (maintenance_attempts >= 0),
    ADD COLUMN maintenance_error_code text NOT NULL DEFAULT '';

CREATE INDEX file_node_recycle_maintenance_claim
    ON file_node(maintenance_not_before, deleted_at, id)
    WHERE status IN ('trashed', 'purging') AND trashed_root_id = id;

CREATE TABLE tenant_invitation (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    issuer text NOT NULL CHECK (issuer <> ''),
    subject text NOT NULL CHECK (subject <> ''),
    display_name text NOT NULL DEFAULT '',
    role text NOT NULL CHECK (role IN ('owner', 'admin', 'editor', 'viewer')),
    token_hash char(64) NOT NULL UNIQUE,
    status text NOT NULL CHECK (status IN ('pending', 'accepted', 'revoked', 'expired')),
    accepted_principal_id uuid,
    created_by uuid NOT NULL,
    expires_at timestamptz NOT NULL,
    accepted_at timestamptz,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (tenant_id, id),
    CONSTRAINT tenant_invitation_state_shape CHECK (
        (status = 'pending' AND accepted_principal_id IS NULL AND accepted_at IS NULL AND revoked_at IS NULL) OR
        (status = 'accepted' AND accepted_principal_id IS NOT NULL AND accepted_at IS NOT NULL AND revoked_at IS NULL) OR
        (status = 'revoked' AND accepted_principal_id IS NULL AND accepted_at IS NULL AND revoked_at IS NOT NULL) OR
        (status = 'expired' AND accepted_principal_id IS NULL AND accepted_at IS NULL AND revoked_at IS NULL)
    )
);

CREATE UNIQUE INDEX tenant_invitation_pending_identity
    ON tenant_invitation(tenant_id, issuer, subject)
    WHERE status = 'pending';

CREATE INDEX tenant_invitation_list
    ON tenant_invitation(tenant_id, created_at DESC, id DESC);

CREATE INDEX tenant_invitation_expiry
    ON tenant_invitation(expires_at, id)
    WHERE status = 'pending';

CREATE TABLE tenant_group (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    display_name text NOT NULL,
    normalized_name text NOT NULL,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (tenant_id, id),
    UNIQUE (tenant_id, normalized_name)
);

CREATE INDEX tenant_group_list
    ON tenant_group(tenant_id, normalized_name, id);

CREATE TABLE tenant_group_member (
    tenant_id uuid NOT NULL,
    group_id uuid NOT NULL,
    principal_id uuid NOT NULL,
    added_by uuid NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, group_id, principal_id),
    CONSTRAINT tenant_group_member_group_fk FOREIGN KEY (tenant_id, group_id)
        REFERENCES tenant_group(tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT tenant_group_member_principal_fk FOREIGN KEY (tenant_id, principal_id)
        REFERENCES tenant_member(tenant_id, principal_id) ON DELETE CASCADE
);

CREATE INDEX tenant_group_member_principal
    ON tenant_group_member(tenant_id, principal_id, group_id);

CREATE TABLE node_acl (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL,
    node_id uuid NOT NULL,
    subject_type text NOT NULL CHECK (subject_type IN ('principal', 'group')),
    principal_id uuid,
    group_id uuid,
    role text NOT NULL CHECK (role IN ('reader', 'contributor', 'manager')),
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (tenant_id, id),
    CONSTRAINT node_acl_node_fk FOREIGN KEY (tenant_id, node_id)
        REFERENCES file_node(tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT node_acl_principal_fk FOREIGN KEY (tenant_id, principal_id)
        REFERENCES tenant_member(tenant_id, principal_id) ON DELETE CASCADE,
    CONSTRAINT node_acl_group_fk FOREIGN KEY (tenant_id, group_id)
        REFERENCES tenant_group(tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT node_acl_subject_shape CHECK (
        (subject_type = 'principal' AND principal_id IS NOT NULL AND group_id IS NULL) OR
        (subject_type = 'group' AND principal_id IS NULL AND group_id IS NOT NULL)
    )
);

CREATE UNIQUE INDEX node_acl_principal_unique
    ON node_acl(tenant_id, node_id, principal_id)
    WHERE subject_type = 'principal';

CREATE UNIQUE INDEX node_acl_group_unique
    ON node_acl(tenant_id, node_id, group_id)
    WHERE subject_type = 'group';

CREATE INDEX node_acl_node_lookup
    ON node_acl(tenant_id, node_id);

CREATE TABLE audit_event (
    sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    id uuid NOT NULL UNIQUE,
    tenant_id uuid NOT NULL REFERENCES tenant(id) ON DELETE RESTRICT,
    actor_principal_id uuid,
    action text NOT NULL CHECK (action ~ '^[a-z][a-z0-9_.]{2,127}$'),
    target_type text NOT NULL CHECK (target_type ~ '^[a-z][a-z0-9_]{1,63}$'),
    target_id uuid,
    request_id text NOT NULL DEFAULT '' CHECK (length(request_id) <= 128),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    occurred_at timestamptz NOT NULL,
    CHECK (jsonb_typeof(metadata) = 'object'),
    CHECK (octet_length(metadata::text) <= 4096)
);

CREATE INDEX audit_event_tenant_cursor
    ON audit_event(tenant_id, occurred_at, sequence);

CREATE OR REPLACE FUNCTION asteria_reject_audit_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'audit events are append-only';
END;
$$;

CREATE TRIGGER audit_event_append_only
BEFORE UPDATE OR DELETE ON audit_event
FOR EACH ROW EXECUTE FUNCTION asteria_reject_audit_mutation();
