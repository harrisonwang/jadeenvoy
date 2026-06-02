# ADR-0029: 支持 MCP OAuth 凭据自动 Refresh

## Status

`Accepted`

日期: 2026-06-02

## Context

ADR-0015 将 Vault V1 收敛为 `static_bearer`，避免先实现完整 OAuth 授权流。但现在 MCP server 的生产接入需要
长期运行的凭据：access token 会过期，手动更新会破坏 managed agent 的无人值守运行能力。

JadeEnvoy 已经有 Vault AES-256-GCM 加密存储、按 MCP host 解析凭据、`je-vault` MITM 注入、以及 harness
侧 MCP Authorization 注入。OAuth refresh 可以复用这些边界，不需要把 token 暴露给 API 响应或业务层。

## Decision

我们决定支持 `mcp_oauth` 凭据类型，但只实现 token refresh，不实现浏览器授权码启动流程。

创建/更新 credential 时，`auth.type = "mcp_oauth"` 接受：

- `mcp_server_url`
- `access_token`
- `expires_at`（RFC3339，可空）
- `refresh.token_endpoint`
- `refresh.refresh_token`
- `refresh.client_id`
- `refresh.scope`（可空）
- `refresh.token_endpoint_auth.type`：`none` / `client_secret_basic` / `client_secret_post`

所有 token、refresh token、client secret 与 refresh 配置作为一个 JSON secret 加密到现有
`vault_credential.cipher` 中；响应只返回 auth type 与 MCP server URL。

`vault.Service.Resolve` 在发现 `mcp_oauth` 凭据过期或将在 1 分钟内过期时，使用 refresh token 调 token endpoint，
成功后把新 access token / refresh token / expires_at 重新加密写回，再返回 fresh bearer token 给 MCP 或
MITM 注入路径。

## Consequences

### 正面

- 长任务和定时运行不会因为 access token 到期而立刻失效。
- API 响应继续零 secret 泄漏。
- MCP 与 MITM 注入路径不需要知道 static bearer 与 OAuth 的差异。

### 负面

- 尚未实现完整 OAuth 授权码 flow，用户仍需先提供初始 access/refresh token。
- refresh 失败时当前调用会降级为无凭据或失败，需要后续在 UI 中暴露更明确的健康状态。

### 中性

- 现有唯一约束仍按 `(vault_id, mcp_server_host)` 生效，同一 host 只能有一条活跃凭据。

## References

- [ADR-0015: Vault credential types](0015-vault-credential-types.md)
- [ADR-0026: MCP Vault auth](0026-mcp-vault-auth.md)
