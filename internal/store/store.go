// Package store 是持久化层，统一 SQLite / Postgres 接口。
// V1 仅 SQLite 实现。
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// Store 是统一持久化接口。
type Store struct {
	DB     *sql.DB
	Driver string // "sqlite" / "postgres"
}

// Open 根据 DatabaseURL 打开 DB 并自动跑迁移。
//
// 支持的 URL:
//   - sqlite:///abs/path/to/file.db
//   - sqlite://./relative/file.db
func Open(ctx context.Context, dbURL string) (*Store, error) {
	if strings.HasPrefix(dbURL, "sqlite://") {
		path := strings.TrimPrefix(dbURL, "sqlite://")
		// 确保 parent dir 存在
		if dir := filepath.Dir(path); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("mkdir %s: %w", dir, err)
			}
		}
		db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
		if err != nil {
			return nil, fmt.Errorf("open sqlite: %w", err)
		}
		if err := db.PingContext(ctx); err != nil {
			db.Close()
			return nil, fmt.Errorf("ping sqlite: %w", err)
		}
		s := &Store{DB: db, Driver: "sqlite"}
		if err := s.Migrate(ctx); err != nil {
			db.Close()
			return nil, err
		}
		return s, nil
	}
	return nil, fmt.Errorf("unsupported database URL scheme: %s", dbURL)
}

func (s *Store) Close() error {
	return s.DB.Close()
}

// Migrate 跑内置 schema。V1 极简：单一 schema，幂等。
func (s *Store) Migrate(ctx context.Context) error {
	var schema string
	switch s.Driver {
	case "sqlite":
		schema = sqliteSchema
	default:
		return fmt.Errorf("no schema for driver %s", s.Driver)
	}
	if _, err := s.DB.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	return nil
}

const sqliteSchema = `
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
    multiagent      TEXT,
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

-- M2: Memory Stores ----------------------------------------------------------

CREATE TABLE IF NOT EXISTS memory_store (
    id              TEXT PRIMARY KEY,
    tenant_id       TEXT NOT NULL DEFAULT 'tnt-default',
    name            TEXT NOT NULL,
    description     TEXT,
    archived_at     INTEGER,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,
    UNIQUE (tenant_id, name)
);

CREATE TABLE IF NOT EXISTS memory (
    id              TEXT PRIMARY KEY,
    memory_store_id TEXT NOT NULL REFERENCES memory_store(id) ON DELETE CASCADE,
    tenant_id       TEXT NOT NULL DEFAULT 'tnt-default',
    path            TEXT NOT NULL,
    content         TEXT NOT NULL DEFAULT '',
    content_sha256  TEXT NOT NULL,
    content_size    INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,
    UNIQUE (memory_store_id, path)
);

CREATE INDEX IF NOT EXISTS memory_store_id_path ON memory(memory_store_id, path);

-- M2: Session resources（挂载到 sandbox 的资源，含 memory_store 引用） -----

CREATE TABLE IF NOT EXISTS session_resource (
    id              TEXT PRIMARY KEY,
    session_id      TEXT NOT NULL REFERENCES session(id) ON DELETE CASCADE,
    type            TEXT NOT NULL CHECK (type IN ('memory_store','file','github_repository')),
    payload         TEXT NOT NULL CHECK (json_valid(payload)),
    created_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS session_resource_session ON session_resource(session_id);

-- M2: Webhook endpoints + delivery queue ------------------------------------

CREATE TABLE IF NOT EXISTS webhook_endpoint (
    id              TEXT PRIMARY KEY,
    tenant_id       TEXT NOT NULL DEFAULT 'tnt-default',
    url             TEXT NOT NULL,
    event_types     TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(event_types)),
    signing_secret  TEXT NOT NULL,
    disabled_at     INTEGER,
    disabled_reason TEXT,
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS webhook_delivery (
    id              TEXT PRIMARY KEY,
    endpoint_id     TEXT NOT NULL REFERENCES webhook_endpoint(id) ON DELETE CASCADE,
    event_id        TEXT NOT NULL,
    event_type      TEXT NOT NULL,
    payload         TEXT NOT NULL CHECK (json_valid(payload)),
    attempt         INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER,
    delivered_at    INTEGER,
    last_status     INTEGER,
    last_error      TEXT,
    created_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS webhook_delivery_pending ON webhook_delivery(next_attempt_at) WHERE delivered_at IS NULL;

-- M2: Files ------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS file (
    id              TEXT PRIMARY KEY,
    tenant_id       TEXT NOT NULL DEFAULT 'tnt-default',
    filename        TEXT NOT NULL,
    content_type    TEXT NOT NULL DEFAULT 'application/octet-stream',
    blob            BLOB NOT NULL,
    size            INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

-- M2: Skills ----------------------------------------------------------------

CREATE TABLE IF NOT EXISTS skill (
    id                  TEXT PRIMARY KEY,
    tenant_id           TEXT NOT NULL DEFAULT 'tnt-default',
    name                TEXT NOT NULL,
    description         TEXT,
    files_json          TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(files_json)),
    skill_md_content    TEXT NOT NULL DEFAULT '',
    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL
);
`
