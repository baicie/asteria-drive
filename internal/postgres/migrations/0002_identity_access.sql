CREATE TABLE principal (
    id uuid PRIMARY KEY,
    issuer text NOT NULL CHECK (issuer <> ''),
    subject text NOT NULL CHECK (subject <> ''),
    display_name text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (issuer, subject)
);

CREATE TABLE tenant_member (
    tenant_id uuid NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    principal_id uuid NOT NULL REFERENCES principal(id) ON DELETE CASCADE,
    role text NOT NULL CHECK (role IN ('owner', 'admin', 'editor', 'viewer')),
    status text NOT NULL CHECK (status IN ('active', 'suspended')),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_id, principal_id)
);

CREATE INDEX tenant_member_principal_lookup
    ON tenant_member(principal_id, tenant_id)
    WHERE status = 'active';
