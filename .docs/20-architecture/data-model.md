# 数据模型（DB schema 草案）

V1 SQL（同时跑 SQLite 和 Postgres，sqlc 生成各自代码）。

## ID 约定

- ULID（26 字符）后缀，前缀按类型：
  - `agt-` agent
  - `env-` environment
  - `sess-` session
  - `evt-` event
  - `vlt-` vault
  - `crd-` credential
  - `mst-` memory store
  - `mem-` memory
  - `mver-` memory version
  - `skl-` skill
  - `tnt-` tenant
  - `usr-` user
  - `key-` api key
  - `whk-` webhook

## 时间戳

`created_at` / `updated_at` 都用 `BIGINT`（unix epoch ms），跨 SQLite/PG 兼容。

## 多租户

V1 所有业务表带 `tenant_id`，**默认填 `tnt-default`**（单租户 dev 模式）。
索引前导 `tenant_id`。M3 真多租户时改成行级 RLS（Postgres）+ 应用层校验。

---

## Tenants + Users + Auth（M1）

```sql
CREATE TABLE tenant (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    created_at  BIGINT NOT NULL
);

CREATE TABLE user_account (
    id              TEXT PRIMARY KEY,
    email           TEXT NOT NULL UNIQUE,
    name            TEXT,
    password_hash   TEXT NOT NULL,  -- bcrypt
    created_at      BIGINT NOT NULL,
    updated_at      BIGINT NOT NULL
);

CREATE TABLE membership (
    user_id     TEXT NOT NULL REFERENCES user_account(id) ON DELETE CASCADE,
    tenant_id   TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    role        TEXT NOT NULL CHECK (role IN ('owner','admin','developer','viewer')),
    created_at  BIGINT NOT NULL,
    PRIMARY KEY (user_id, tenant_id)
);

CREATE TABLE auth_session (
    id              TEXT PRIMARY KEY,    -- cookie value
    user_id         TEXT NOT NULL REFERENCES user_account(id) ON DELETE CASCADE,
    tenant_id       TEXT NOT NULL,
    expires_at      BIGINT NOT NULL,
    created_at      BIGINT NOT NULL
);
CREATE INDEX auth_session_user ON auth_session(user_id);

CREATE TABLE api_key (
    id              TEXT PRIMARY KEY,
    tenant_id       TEXT NOT NULL,
    user_id         TEXT,    -- 可空（无主 key）
    name            TEXT NOT NULL,
    prefix          TEXT NOT NULL,        -- 显示用前 8 字符
    hash            TEXT NOT NULL UNIQUE, -- sha256(full key)
    created_at      BIGINT NOT NULL,
    revoked_at      BIGINT
);
```

---

## Agent（M1）

```sql
CREATE TABLE agent (
    id              TEXT PRIMARY KEY,
    tenant_id       TEXT NOT NULL,
    name            TEXT NOT NULL,
    archived_at     BIGINT,
    created_at      BIGINT NOT NULL,
    updated_at      BIGINT NOT NULL,
    current_version INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX agent_tenant_name ON agent(tenant_id, name);

CREATE TABLE agent_version (
    agent_id        TEXT NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    version         INTEGER NOT NULL,
    model           JSONB NOT NULL,     -- {id, speed?}
    system          TEXT,
    tools           JSONB NOT NULL DEFAULT '[]'::jsonb,
    mcp_servers     JSONB NOT NULL DEFAULT '[]'::jsonb,
    skills          JSONB NOT NULL DEFAULT '[]'::jsonb,
    multiagent      JSONB,
    description     TEXT,
    metadata        JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at      BIGINT NOT NULL,
    PRIMARY KEY (agent_id, version)
);
```

> SQLite 用 `TEXT` 替代 `JSONB`，加 `CHECK(json_valid(...))`。
> SQLite 不支持 `::jsonb` cast，迁移文件按方言分开。

---

## Environment（M1 极简）

V1 我们硬编码一个 `default` environment，**不进数据库**。
M2 实现 environment 时加表：

```sql
-- M2
CREATE TABLE environment (
    id              TEXT PRIMARY KEY,
    tenant_id       TEXT NOT NULL,
    name            TEXT NOT NULL,
    config          JSONB NOT NULL,
    archived_at     BIGINT,
    created_at      BIGINT NOT NULL,
    updated_at      BIGINT NOT NULL,
    UNIQUE (tenant_id, name)
);
```

---

## Session + Events（M1）

```sql
CREATE TABLE session (
    id                  TEXT PRIMARY KEY,
    tenant_id           TEXT NOT NULL,
    agent_id            TEXT NOT NULL,
    agent_version       INTEGER NOT NULL,
    agent_snapshot      JSONB NOT NULL,    -- 创建时的完整 agent 配置
    environment_id      TEXT NOT NULL,     -- M1 永远是 'default'
    status              TEXT NOT NULL CHECK (status IN ('idle','running','rescheduling','terminated','requires_action')),
    title               TEXT,
    metadata            JSONB NOT NULL DEFAULT '{}'::jsonb,
    archived_at         BIGINT,
    terminated_at       BIGINT,
    created_at          BIGINT NOT NULL,
    updated_at          BIGINT NOT NULL,
    -- usage 累计
    usage_input_tokens          BIGINT NOT NULL DEFAULT 0,
    usage_output_tokens         BIGINT NOT NULL DEFAULT 0,
    usage_cache_create_tokens   BIGINT NOT NULL DEFAULT 0,
    usage_cache_read_tokens     BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX session_tenant ON session(tenant_id, created_at DESC);
CREATE INDEX session_agent ON session(agent_id);

CREATE TABLE session_event (
    id              TEXT PRIMARY KEY,         -- evt-<ULID>
    session_id      TEXT NOT NULL REFERENCES session(id) ON DELETE CASCADE,
    thread_id       TEXT NOT NULL DEFAULT 'primary',  -- M3 multi-agent
    seq             INTEGER NOT NULL,
    type            TEXT NOT NULL,
    payload         JSONB NOT NULL,
    processed_at    BIGINT,
    created_at      BIGINT NOT NULL,
    UNIQUE (session_id, seq)
);
CREATE INDEX session_event_session_seq ON session_event(session_id, seq);
CREATE INDEX session_event_session_type ON session_event(session_id, type);
```

`agent_snapshot`：session 创建时锁住 agent 当前 version 的完整配置，
后续 agent 升版本不影响进行中 session。

---

## Vault + Credentials（M1）

```sql
CREATE TABLE vault (
    id              TEXT PRIMARY KEY,
    tenant_id       TEXT NOT NULL,
    display_name    TEXT NOT NULL,
    metadata        JSONB NOT NULL DEFAULT '{}'::jsonb,
    archived_at     BIGINT,
    created_at      BIGINT NOT NULL,
    updated_at      BIGINT NOT NULL
);
CREATE INDEX vault_tenant ON vault(tenant_id);

CREATE TABLE vault_credential (
    id                  TEXT PRIMARY KEY,
    vault_id            TEXT NOT NULL REFERENCES vault(id) ON DELETE CASCADE,
    tenant_id           TEXT NOT NULL,
    display_name        TEXT NOT NULL,
    auth_type           TEXT NOT NULL CHECK (auth_type IN ('static_bearer','mcp_oauth')),
    mcp_server_url      TEXT NOT NULL,
    mcp_server_host     TEXT NOT NULL,     -- 提取的 host，用于匹配
    auth_meta           JSONB NOT NULL DEFAULT '{}'::jsonb,  -- 非 secret 字段
    cipher              BYTEA NOT NULL,    -- AES-GCM(secret_fields_json)，SQLite 方言中为 BLOB
    cipher_nonce        BYTEA NOT NULL,
    cipher_label        TEXT NOT NULL,     -- 加密上下文
    archived_at         BIGINT,
    created_at          BIGINT NOT NULL,
    updated_at          BIGINT NOT NULL,
    UNIQUE (vault_id, mcp_server_host) WHERE archived_at IS NULL  -- 同 host 唯一活跃
);
CREATE INDEX vault_credential_vault ON vault_credential(vault_id);
CREATE INDEX vault_credential_host ON vault_credential(tenant_id, mcp_server_host);
```

> PG 与 SQLite 都支持 partial unique index；应用层仍先查重，用 DB 约束兜底并发。

---

## Session ↔ Vault 绑定（M1）

```sql
CREATE TABLE session_vault (
    session_id  TEXT NOT NULL REFERENCES session(id) ON DELETE CASCADE,
    vault_id    TEXT NOT NULL REFERENCES vault(id) ON DELETE CASCADE,
    ord         INTEGER NOT NULL,  -- 多 vault 时优先级（小的先匹配）
    PRIMARY KEY (session_id, vault_id)
);
```

---

## Memory Stores（M2）

```sql
-- M2
CREATE TABLE memory_store (
    id              TEXT PRIMARY KEY,
    tenant_id       TEXT NOT NULL,
    name            TEXT NOT NULL,
    description     TEXT,
    archived_at     BIGINT,
    created_at      BIGINT NOT NULL,
    updated_at      BIGINT NOT NULL,
    UNIQUE (tenant_id, name)
);

CREATE TABLE memory (
    id              TEXT PRIMARY KEY,
    memory_store_id TEXT NOT NULL REFERENCES memory_store(id) ON DELETE CASCADE,
    tenant_id       TEXT NOT NULL,
    path            TEXT NOT NULL,
    content_sha256  TEXT NOT NULL,
    content_size    INTEGER NOT NULL,
    blob_ref        TEXT NOT NULL,  -- 本地 fs 路径或 S3 key
    created_at      BIGINT NOT NULL,
    updated_at      BIGINT NOT NULL,
    UNIQUE (memory_store_id, path)
);

-- M3 加版本表
CREATE TABLE memory_version (
    id              TEXT PRIMARY KEY,
    memory_id       TEXT NOT NULL REFERENCES memory(id) ON DELETE CASCADE,
    memory_store_id TEXT NOT NULL,
    operation       TEXT NOT NULL CHECK (operation IN ('create','update','rename','delete','redact')),
    path            TEXT NOT NULL,
    content_sha256  TEXT NOT NULL,
    blob_ref        TEXT,           -- redact 后置 NULL
    created_at      BIGINT NOT NULL
);
CREATE INDEX memory_version_memory ON memory_version(memory_id, created_at DESC);
```

---

## Skills（M2）

```sql
-- M2
CREATE TABLE skill (
    id              TEXT PRIMARY KEY,
    tenant_id       TEXT NOT NULL,
    name            TEXT NOT NULL,     -- 来自 SKILL.md frontmatter
    description     TEXT,
    version         TEXT NOT NULL,
    bundle_ref      TEXT NOT NULL,     -- zip 存储位置
    skill_md        TEXT NOT NULL,     -- SKILL.md 全文，挂载时 inline 到 system prompt
    archived_at     BIGINT,
    created_at      BIGINT NOT NULL,
    updated_at      BIGINT NOT NULL,
    UNIQUE (tenant_id, name)
);
```

---

## Files（M2）

```sql
-- M2
CREATE TABLE file_resource (
    id              TEXT PRIMARY KEY,
    tenant_id       TEXT NOT NULL,
    filename        TEXT NOT NULL,
    mime_type       TEXT,
    size_bytes      BIGINT NOT NULL,
    blob_ref        TEXT NOT NULL,
    scope_id        TEXT,    -- 可空；session-scoped 时填 session_id
    created_at      BIGINT NOT NULL
);
CREATE INDEX file_resource_tenant ON file_resource(tenant_id);
CREATE INDEX file_resource_scope ON file_resource(scope_id) WHERE scope_id IS NOT NULL;
```

---

## Session Resources（M2）

```sql
-- M2
CREATE TABLE session_resource (
    id              TEXT PRIMARY KEY,
    session_id      TEXT NOT NULL REFERENCES session(id) ON DELETE CASCADE,
    type            TEXT NOT NULL CHECK (type IN ('file','memory_store','github_repository')),
    payload         JSONB NOT NULL,  -- file_id / memory_store_id / url+mount_path 等
    created_at      BIGINT NOT NULL
);
```

---

## Webhooks（M2）

```sql
-- M2
CREATE TABLE webhook_endpoint (
    id              TEXT PRIMARY KEY,
    tenant_id       TEXT NOT NULL,
    url             TEXT NOT NULL,
    event_types     JSONB NOT NULL DEFAULT '[]'::jsonb,
    signing_secret  TEXT NOT NULL,  -- 创建时返回明文一次，存 cipher
    disabled_at     BIGINT,
    disabled_reason TEXT,
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    created_at      BIGINT NOT NULL,
    updated_at      BIGINT NOT NULL
);

CREATE TABLE webhook_delivery (
    id              TEXT PRIMARY KEY,
    endpoint_id     TEXT NOT NULL REFERENCES webhook_endpoint(id) ON DELETE CASCADE,
    event_id        TEXT NOT NULL,
    payload         JSONB NOT NULL,
    attempt         INTEGER NOT NULL DEFAULT 0,
    next_attempt_at BIGINT,
    delivered_at    BIGINT,
    last_status     INTEGER,
    created_at      BIGINT NOT NULL
);
CREATE INDEX webhook_delivery_pending ON webhook_delivery(next_attempt_at) WHERE delivered_at IS NULL;
```

---

## 总体数量（V1 = M1 + M2）

| 表 | 里程碑 |
|---|---|
| `tenant`, `user_account`, `membership`, `auth_session`, `api_key` | M1 |
| `agent`, `agent_version` | M1 |
| `session`, `session_event`, `session_vault` | M1 |
| `vault`, `vault_credential` | M1 |
| `environment` | M2 |
| `memory_store`, `memory` | M2 |
| `skill` | M2 |
| `file_resource`, `session_resource` | M2 |
| `webhook_endpoint`, `webhook_delivery` | M2 |
| `memory_version` | M3 |

V1 M1：~12 表
V2 M2：+8 表 = ~20 表
V3 M3：+3 表 + 多租户 RLS = ~23 表

可控规模。
