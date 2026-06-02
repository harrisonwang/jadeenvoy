# ADR-0026: MCP 静态鉴权 —— 复用 vault static_bearer 注入 Authorization

## Status

`Accepted` — OAuth refresh scope superseded by ADR-0029

日期: 2026-06-01

## Context

[ADR-0024] 的 MCP client 第一刀只做无鉴权，需鉴权的 MCP server（Linear/GitHub 等）
连不上。现在补上**静态鉴权**：把 vault 里已有的 `static_bearer` 凭据注入 MCP 请求的
`Authorization` 头。

已有基础设施可直接复用：

- `vault.Service.Resolve(ctx, tenantID, vaultIDs, host)` 已实现——按 host 在 session
  的 vault 集合里匹配活跃 `static_bearer`，解密返回 token（同 host 唯一、取最新，
  规避 OMA 坑）。这正是 MITM 代理用的同一条路径。
- session 已存 `vault_ids`；MCP server 声明里有 `url`（可取 host）。

约束：`mcp_oauth` 自动 refresh 仍 **parked**（ADR-0024）。本 ADR 只做 static_bearer。

## Decision

**harness 连接 MCP server 时，按 server host 用 vault.Resolve 查 static_bearer，
命中则给该 MCP client 设 `Authorization: Bearer <token>`。**

1. **MCP client 支持鉴权头**：`mcp.Client` 加 `SetAuthorization(string)`，在每个
   HTTP 请求带上（initialize/tools/list/tools/call 全程）。

2. **harness 解耦注入**：`connectMCP` 增加一个 `credResolver` 参数——
   `func(host string) string`（返回 Bearer token，空表示无）。harness 在调用前用
   闭包包住 `vault.Service.Resolve(tenantID, session.vault_ids, host)`，**不让
   harness 直接 import vault 的解密细节**，只拿最终 token 字符串。

3. **匹配粒度**：按 MCP server URL 的 host 匹配 vault credential 的 `mcp_server_host`
   （与 MITM 代理一致的匹配语义）。无匹配则不带鉴权（degraded，仍尝试连）。

4. **不落日志**：token 只在内存流转，不进事件 / 日志。

最小形态：

```go
// harness 装配
resolver := func(host string) string {
    if h.Vault == nil { return "" }
    rc, _ := h.Vault.Resolve(ctx, sess.TenantID, vaultIDs, host)
    if rc != nil { return rc.Token }
    return ""
}
mcpSess := connectMCP(ctx, sess.AgentSnapshot, resolver)
```

## Consequences

### 正面

- 需鉴权的 MCP server 可用，复用现成 vault + 加密路径，零新增加密代码。
- 与 MITM 代理共用同一套 host 匹配 / 取最新语义，行为一致、坑已规避。
- harness 不直接依赖 vault 解密，仅通过 token 字符串闭包解耦。

### 负面

- 仅 static_bearer；OAuth 自动 refresh 仍 parked（token 过期需手动更新 vault）。
- token 注入在 client 内存，subprocess 沙箱里的 agent 代码看不到（注入发生在 jed 进程，
  不经沙箱）——这点反而是优势（凭据不下放沙箱）。

### 中性

- `mcp.Client` 新增可选 Authorization；无鉴权路径不变。
- harness 需要能拿到 session 的 `TenantID` 与 `vault_ids`（已在 SessionRow 内）。

## Alternatives considered

### 方案 A：在 agent mcp_servers 配置里直接写 token
简单。**拒因**：凭据明文进 agent 配置 / DB，违背 vault "凭据集中加密" 的全部初衷。

### 方案 B：走 MITM 代理（像沙箱出站那样）
复用代理注入。**拒因**：MCP 调用发生在 jed 进程内、不经沙箱出站，没有走代理；
直接在 client 设头更简单准确。

### 方案 C（本 ADR）：vault.Resolve + client Authorization 头
复用解密、解耦、与代理语义一致。选它。

## References

- [ADR-0024] MCP client（无鉴权第一刀）
- [ADR-0015] Vault credential types（static_bearer）
- [ADR-0019] 零依赖 crypto
- parked：mcp_oauth 自动 refresh

## Open Questions

- OAuth refresh（401 → 用 refresh_token 换新 token → CAS 写回）后续 ADR。
- 非 Bearer 的鉴权头形态（自定义 header 名）暂不支持。
