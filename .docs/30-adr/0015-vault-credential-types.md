# ADR-0015: Vault V1 仅 static_bearer

## Status

Superseded by ADR-0029 — 2026-06-02

## Context

Anthropic Vault credential 类型：
- `static_bearer` —— 固定 token，注入 `Authorization: Bearer ...`，无 refresh
- `mcp_oauth` —— OAuth 2.0，自动 refresh 含 401 重试 + CAS 写新 token

OMA 还扩展了:
- `cap_cli` —— 给 CLI 工具（gh / glab / aws）注入 env var 或 header（特定 CLI 适配）

我们 V1 需求：
- GitLab code review bot ↔ static_bearer 足够（GitLab Project Access Token）
- 公司 LLM 网关认证 ↔ `JE_LLM_API_KEY` env var（不走 vault）
- 其他 MCP server 接入 ↔ V2 才有 MCP

V1 实现完整 mcp_oauth：
- OAuth 2.0 + PKCE
- 多种 token_endpoint_auth.type（`none` / `client_secret_basic` / `client_secret_post`）
- 401 + 403 自动 refresh
- CAS 写回 token 防并发
- refresh 失败 → 发 webhook event
- `/v1/vaults/:id/credentials/:cred_id/mcp_oauth_validate` endpoint

大概 ~600 行 + 复杂测试。

`cap_cli` 实现：
- 沙箱里执行 CLI 命令时 detect 命令前缀
- 按 cap 注册表（OMA 有 ~20 个）注入 env var
- 跨平台兼容（不同 CLI 的 env var 名字不同）

## Decision

**V1: 仅实现 `static_bearer`。M3 实现 `mcp_oauth`（refresh + validate endpoint）。`cap_cli` 暂不做。**

### V1 schema

```go
type Credential struct {
    ID             string
    VaultID        string
    DisplayName    string
    AuthType       string  // 仅 "static_bearer"
    MCPServerURL   string
    MCPServerHost  string  // 索引匹配用
    Cipher         []byte  // AES-GCM(token)
    CipherNonce    []byte
    CipherLabel    string  // 加密上下文，例: "vault.credential.token"
    CreatedAt      time.Time
}
```

`mcp_oauth` API endpoint 在 V1 接受请求但返回 **501**（让 client SDK 提示 V1 不支持）。

### MITM 注入逻辑

```go
func (s *Service) PickByHost(ctx, sessionID, host string) (*ResolvedCredential, error) {
    creds := s.repo.ListBySessionAndHost(ctx, sessionID, host)
    for _, c := range creds {
        switch c.AuthType {
        case "static_bearer":
            return &ResolvedCredential{
                Token: s.decrypt(c.Cipher, c.CipherNonce),
                CredentialID: c.ID,
            }, nil
        }
    }
    return nil, ErrNoMatch
}
```

### V1 唯一约束

`UNIQUE (vault_id, mcp_server_host) WHERE archived_at IS NULL`

- 防止 OMA 踩的坑（多条同 host 凭据，pickByHost 永远取最早）
- POST 时 host 已存在 → 409 Conflict + 提示先 archive 老的

### M3 mcp_oauth 实现要点

数据模型扩展：
```sql
ALTER TABLE vault_credential ADD COLUMN refresh_meta JSONB;
-- {token_endpoint, client_id, scope, token_endpoint_auth_type, ...}
ALTER TABLE vault_credential ADD COLUMN refresh_token_cipher BLOB;
ALTER TABLE vault_credential ADD COLUMN expires_at BIGINT;
```

Refresh 流程：
1. MITM 处发现 401/403
2. 用 refresh_token 调 token_endpoint
3. 拿新 access_token + 可能新 refresh_token
4. CAS 写回 DB（`WHERE updated_at = $old`）
5. 重试原请求 1 次
6. 仍失败 → emit `vault_credential.refresh_failed` webhook

## Consequences

### 正面

- V1 实现简单（凭据存储 + 单一 inject 路径）
- 覆盖 V1 主 use case（GitLab PAT）
- 唯一约束硬保证"同 host 只有一条"，防 OMA 那种取错凭据 bug
- M3 升级路径清晰（schema 加列 + 新代码路径）

### 负面

- V1 用户**没法**用 Linear / Slack / Notion MCP server（依赖 OAuth）
  → V1 用户文档明确说"V1 仅 static_bearer 凭据，OAuth 服务等 M3"
  → 急需可以用 PAT 替代（多数服务支持）
- V1 用户**没法**用 cap_cli 风格的 gh/glab/aws CLI 凭据自动注入
  → 文档建议 agent 用 curl + HTTPS_PROXY 路线（vault 同样能注入）

### 中性

- API endpoint `mcp_oauth_validate` 返回 501 提醒用户 V1 限制
- AES-GCM cipher 字段在 schema 里设计时已支持多种 secret（V1 仅 token，M3 加 refresh_token）

## Alternatives considered

### V1 做完整 mcp_oauth

- **拒因**: ~600 行 + 测试 + 各种 IdP corner case，挤压 V1 其他工作。
- **何时考虑**: 用户提强需求时拉前到 M2.5

### V1 做 cap_cli 但不做 mcp_oauth

- **拒因**: cap_cli 是 OMA 扩展，不是 Anthropic spec 一部分。优先做 spec 一致的 mcp_oauth。

### 完全砍 vault V1（用 env var 替代）

- **拒因**: agent 在沙箱里直接看到 token = 安全模型崩。Code review bot 必须有 vault。

## V1 用户提示

文档要说：

> **V1 vault 仅支持 `static_bearer` 凭据类型**：
> - 一个 host 一个 token
> - 无自动 refresh
> - 适合 Personal Access Token / API Key 类
>
> OAuth 类（Linear、Slack、Notion 等）等 V3，或用对应服务的 PAT 替代。

## References

- Anthropic Vaults spec: https://platform.claude.com/docs/en/managed-agents/vaults.md
- OMA cap registry: `packages/cap/`
- [feature-backlog: M3 mcp_oauth](../10-feature-backlog/M3-mature.md)
- [ADR-0006](0006-https-mitm-proxy.md) MITM 实现

## Open Questions

- M2.5 是否提前做 mcp_oauth？看用户 OAuth 服务接入需求强度
- cap_cli 永远不做？看用户用 gh/glab 直接的频率
