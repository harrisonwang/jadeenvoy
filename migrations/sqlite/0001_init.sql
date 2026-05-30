-- JadeEnvoy V1 初始 schema (SQLite 方言)

CREATE TABLE IF NOT EXISTS agent (
    id              TEXT PRIMARY KEY,
    tenant_id       TEXT NOT NULL DEFAULT 'tnt-default',
    name            TEXT NOT NULL,
    archived_at     INTEGER,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,
    current_version INTEGER NOT NULL DEFAULT 1
);

CREATE INDEX IF NOT EXISTS agent_tenant ON agent(tenant_id);

CREATE TABLE IF NOT EXISTS agent_version (
    agent_id        TEXT NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    version         INTEGER NOT NULL,
    name            TEXT NOT NULL,
    model           TEXT NOT NULL CHECK (json_valid(model)),
    system          TEXT,
    description     TEXT,
    tools           TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(tools)),
    mcp_servers     TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(mcp_servers)),
    skills          TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(skills)),
    multiagent      TEXT CHECK (multiagent IS NULL OR json_valid(multiagent)),
    metadata        TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata)),
    created_at      INTEGER NOT NULL,
    PRIMARY KEY (agent_id, version)
);

CREATE TABLE IF NOT EXISTS session (
    id                  TEXT PRIMARY KEY,
    tenant_id           TEXT NOT NULL DEFAULT 'tnt-default',
    agent_id            TEXT NOT NULL,
    agent_version       INTEGER NOT NULL,
    agent_snapshot      TEXT NOT NULL CHECK (json_valid(agent_snapshot)),
    environment_id      TEXT NOT NULL DEFAULT 'default',
    status              TEXT NOT NULL CHECK (status IN ('idle','running','rescheduling','terminated','requires_action')),
    title               TEXT,
    metadata            TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata)),
    vault_ids           TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(vault_ids)),
    archived_at         INTEGER,
    terminated_at       INTEGER,
    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL,
    usage_input_tokens          INTEGER NOT NULL DEFAULT 0,
    usage_output_tokens         INTEGER NOT NULL DEFAULT 0,
    usage_cache_create_tokens   INTEGER NOT NULL DEFAULT 0,
    usage_cache_read_tokens     INTEGER NOT NULL DEFAULT 0,
    next_seq                    INTEGER NOT NULL DEFAULT 1
);

CREATE INDEX IF NOT EXISTS session_tenant ON session(tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS session_agent ON session(agent_id);

CREATE TABLE IF NOT EXISTS session_event (
    id              TEXT PRIMARY KEY,
    session_id      TEXT NOT NULL REFERENCES session(id) ON DELETE CASCADE,
    thread_id       TEXT NOT NULL DEFAULT 'primary',
    seq             INTEGER NOT NULL,
    type            TEXT NOT NULL,
    payload         TEXT NOT NULL CHECK (json_valid(payload)),
    processed_at    INTEGER,
    created_at      INTEGER NOT NULL,
    UNIQUE (session_id, seq)
);

CREATE INDEX IF NOT EXISTS session_event_session_seq ON session_event(session_id, seq);
CREATE INDEX IF NOT EXISTS session_event_session_type ON session_event(session_id, type);

CREATE TABLE IF NOT EXISTS vault (
    id              TEXT PRIMARY KEY,
    tenant_id       TEXT NOT NULL DEFAULT 'tnt-default',
    display_name    TEXT NOT NULL,
    metadata        TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata)),
    archived_at     INTEGER,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS vault_credential (
    id                  TEXT PRIMARY KEY,
    vault_id            TEXT NOT NULL REFERENCES vault(id) ON DELETE CASCADE,
    tenant_id           TEXT NOT NULL DEFAULT 'tnt-default',
    display_name        TEXT NOT NULL,
    auth_type           TEXT NOT NULL CHECK (auth_type IN ('static_bearer')),
    mcp_server_url      TEXT NOT NULL,
    mcp_server_host     TEXT NOT NULL,
    cipher              BLOB NOT NULL,
    cipher_nonce        BLOB NOT NULL,
    cipher_label        TEXT NOT NULL,
    archived_at         INTEGER,
    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS vault_credential_vault ON vault_credential(vault_id);
CREATE INDEX IF NOT EXISTS vault_credential_host ON vault_credential(tenant_id, mcp_server_host);
