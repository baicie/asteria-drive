CREATE TABLE tenant (
    id uuid PRIMARY KEY,
    display_name text NOT NULL,
    root_node_id uuid,
    created_at timestamptz NOT NULL
);

CREATE TABLE file_node (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenant(id) ON DELETE RESTRICT,
    parent_id uuid,
    kind text NOT NULL CHECK (kind IN ('directory', 'file')),
    display_name text NOT NULL,
    normalized_name text NOT NULL,
    current_version_id uuid,
    size_bytes bigint NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    mime_type text NOT NULL DEFAULT '',
    status text NOT NULL CHECK (status IN ('active', 'trashed', 'purging')),
    trashed_root_id uuid,
    original_parent_id uuid,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    deleted_at timestamptz,
    UNIQUE (tenant_id, id),
    CONSTRAINT file_node_parent_fk FOREIGN KEY (tenant_id, parent_id)
        REFERENCES file_node(tenant_id, id) DEFERRABLE INITIALLY IMMEDIATE,
    CONSTRAINT file_node_trash_root_fk FOREIGN KEY (tenant_id, trashed_root_id)
        REFERENCES file_node(tenant_id, id) DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT file_node_original_parent_fk FOREIGN KEY (tenant_id, original_parent_id)
        REFERENCES file_node(tenant_id, id) DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT file_node_shape CHECK (
        (kind = 'directory' AND current_version_id IS NULL AND size_bytes = 0 AND mime_type = '') OR
        (kind = 'file' AND current_version_id IS NOT NULL AND size_bytes >= 0 AND mime_type <> '')
    )
);

CREATE UNIQUE INDEX file_node_one_root_per_tenant
    ON file_node(tenant_id) WHERE parent_id IS NULL;

CREATE UNIQUE INDEX file_node_active_name_unique
    ON file_node(tenant_id, parent_id, normalized_name)
    WHERE status = 'active' AND trashed_root_id IS NULL AND parent_id IS NOT NULL;

CREATE INDEX file_node_children_keyset
    ON file_node(tenant_id, parent_id, normalized_name, id)
    WHERE status = 'active' AND trashed_root_id IS NULL;

CREATE INDEX file_node_trash_roots_keyset
    ON file_node(tenant_id, normalized_name, id)
    WHERE status = 'trashed' AND trashed_root_id = id;

CREATE TABLE blob (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenant(id) ON DELETE RESTRICT,
    bucket text NOT NULL,
    object_key text NOT NULL,
    size_bytes bigint NOT NULL CHECK (size_bytes >= 0),
    mime_type text NOT NULL,
    checksum_algorithm text NOT NULL DEFAULT '',
    checksum_value text NOT NULL DEFAULT '',
    checksum_status text NOT NULL CHECK (checksum_status IN ('verified', 'declared', 'unavailable')),
    status text NOT NULL CHECK (status IN ('available', 'pending_delete', 'deleted')),
    reference_count bigint NOT NULL CHECK (reference_count >= 0),
    created_at timestamptz NOT NULL,
    deleted_at timestamptz,
    UNIQUE (tenant_id, id),
    UNIQUE (bucket, object_key)
);

CREATE TABLE file_version (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenant(id) ON DELETE RESTRICT,
    node_id uuid NOT NULL,
    blob_id uuid NOT NULL,
    size_bytes bigint NOT NULL CHECK (size_bytes >= 0),
    mime_type text NOT NULL,
    checksum_algorithm text NOT NULL DEFAULT '',
    checksum_value text NOT NULL DEFAULT '',
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL,
    UNIQUE (tenant_id, id),
    CONSTRAINT file_version_node_fk FOREIGN KEY (tenant_id, node_id)
        REFERENCES file_node(tenant_id, id) DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT file_version_blob_fk FOREIGN KEY (tenant_id, blob_id)
        REFERENCES blob(tenant_id, id) DEFERRABLE INITIALLY IMMEDIATE
);

ALTER TABLE file_node ADD CONSTRAINT file_node_current_version_fk
    FOREIGN KEY (tenant_id, current_version_id)
    REFERENCES file_version(tenant_id, id) DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE tenant ADD CONSTRAINT tenant_root_node_fk
    FOREIGN KEY (id, root_node_id)
    REFERENCES file_node(tenant_id, id) DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE upload_session (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenant(id) ON DELETE RESTRICT,
    principal_id uuid NOT NULL,
    parent_id uuid NOT NULL,
    display_name text NOT NULL,
    normalized_name text NOT NULL,
    expected_size bigint NOT NULL CHECK (expected_size > 0),
    mime_type text NOT NULL,
    declared_checksum_algorithm text NOT NULL DEFAULT '',
    declared_checksum_value text NOT NULL DEFAULT '',
    bucket text NOT NULL,
    object_key text NOT NULL,
    storage_upload_id text NOT NULL,
    status text NOT NULL CHECK (status IN (
        'created', 'uploading', 'completing', 'object_completed',
        'committed', 'aborted', 'expired', 'failed'
    )),
    completion_digest text NOT NULL DEFAULT '',
    committed_node_id uuid,
    failure_code text NOT NULL DEFAULT '',
    part_size bigint NOT NULL CHECK (part_size > 0),
    expires_at timestamptz NOT NULL,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (tenant_id, id),
    UNIQUE (bucket, object_key),
    CONSTRAINT upload_parent_fk FOREIGN KEY (tenant_id, parent_id)
        REFERENCES file_node(tenant_id, id) DEFERRABLE INITIALLY IMMEDIATE,
    CONSTRAINT upload_committed_node_fk FOREIGN KEY (tenant_id, committed_node_id)
        REFERENCES file_node(tenant_id, id) DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT upload_commit_shape CHECK (
        (status = 'committed' AND committed_node_id IS NOT NULL) OR
        (status <> 'committed')
    )
);

CREATE INDEX upload_session_expiry
    ON upload_session(expires_at, id)
    WHERE status IN ('created', 'uploading', 'completing', 'object_completed');

CREATE TABLE upload_part (
    tenant_id uuid NOT NULL,
    upload_session_id uuid NOT NULL,
    part_number integer NOT NULL CHECK (part_number BETWEEN 1 AND 10000),
    etag text NOT NULL,
    checksum_algorithm text NOT NULL DEFAULT '',
    checksum_value text NOT NULL DEFAULT '',
    size_bytes bigint NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    created_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, upload_session_id, part_number),
    CONSTRAINT upload_part_session_fk FOREIGN KEY (tenant_id, upload_session_id)
        REFERENCES upload_session(tenant_id, id) ON DELETE CASCADE
);
