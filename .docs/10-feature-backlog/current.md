# Current feature status

更新时间：2026-06-02

JadeEnvoy 当前按“managed agent 是可编排、可托管、可观测、可审计、可恢复、可安全执行、可替换基础设施的系统组件”推进。
这一定义是判断功能是否进入当前里程碑的主要产品准绳：功能必须增强这些系统属性之一，而不是只增加表面 endpoint。

本页描述当前仓库代码已经实现或实验性挂载的功能状态。若与早期 M1/M2/M3 文档或 ADR 正文冲突，以代码和本页为准；ADR 保留历史背景。

## 已实现的 V1 runtime core

- `jed` daemon：HTTP API、事件流、metrics、SQLite/Postgres 持久化；数据库差异通过 Store dialect adapter 隔离。
- Agent CRUD、归档、版本列表。
- Environment CRUD（仅 `cloud`；`self_hosted` 返回 501）+ session 创建校验 environment_id（[ADR-0023](../30-adr/0023-environments-resource.md)）。
- Session CRUD、归档、事件写入、事件历史、SSE 订阅。
  - `session_thread_id`：事件写入、历史查询与 harness 上下文按 thread 隔离；agent `multiagent`
    配置会进入 agent/session snapshot，支持 `multiagent.coordinator` 控制面落地。
- Harness loop：从 append-only event log 重建上下文，调用 provider，执行工具，再写回事件。
  - Context compaction（[ADR-0021](../30-adr/0021-context-compaction.md)）：历史超 token 预算时在 turn 边界
    用 LLM 摘要压成 `agent.thread_context_compacted` checkpoint 事件写回 event log（`JE_COMPACT_THRESHOLD_TOKENS`，默认 150000；`<=0` 关闭）。
  - `session.status_idle` 的 `stop_reason` 反映真实停止原因（`end_turn` / `max_turns`）。
  - Anthropic provider 默认给 system + tools 前缀打 `cache_control: ephemeral`（prompt caching）。
  - 错误恢复（[ADR-0022](../30-adr/0022-error-recovery-state-machine.md)）：瞬时错误（5xx/429/网络）
    publish `session.status_rescheduled` 自动重试（`JE_LLM_MAX_RETRIES`，默认 2）；任何未恢复错误
    兜底 publish `session.status_terminated` + 落库，不再把 session 卡在 `running`。
  - 启动恢复 sweep：jed 启动时把上次崩溃遗留的 `running`/`rescheduling` session 标记 `terminated`
    并补发终态事件，消灭僵尸 session。
  - `user.interrupt`（[ADR-0025](../30-adr/0025-user-interrupt.md)）：打断运行中的 turn，
    回到 clean `idle{stop_reason:interrupt}`（非 terminated）。
  - `permission_policy.always_ask`：内置工具与 MCP 工具可在执行前暂停为
    `session.status_idle{stop_reason:requires_action}`，等待 `user.tool_confirmation` 后恢复。
- SSE 事件流 `GET /v1/sessions/{id}/events/stream` 支持 `?since=<seq>` / `Last-Event-ID` 回放补发，
  晚连接 / 断线重连不再死锁（消灭官方点名的 SSE deadlock）。
- LLM provider：`mock`、`openai_compat`、`anthropic` / `anthropic_compat` thin clients。
- Sandbox：subprocess provider。
- 内置工具：`bash`、`read`、`write`、`edit`、`glob`、`grep`。
- MCP client（[ADR-0024](../30-adr/0024-mcp-client.md)）：Streamable HTTP transport，
  agent `mcp_servers` 工具以 `mcp__<server>__<tool>` 暴露，调用发 `agent.mcp_tool_use`/`agent.mcp_tool_result`。
  - Vault 鉴权（[ADR-0026](../30-adr/0026-mcp-vault-auth.md)）：按 server host 用 vault 凭据注入
    `Authorization: Bearer`（复用 session vault_ids）；支持 `static_bearer` 与 `mcp_oauth` 过期自动 refresh。
- Webhook 投递 SSRF 防护：默认拒私网/环回/链路本地目标（创建期 + 投递期双校验），
  `JE_WEBHOOK_ALLOW_PRIVATE=1` 放行内网目标。
- Vault：`static_bearer` / `mcp_oauth` credential、AES-256-GCM 存储、`je-vault` HTTPS MITM 注入。
- Auth：cookie session、API key、`required` / `optional` / `bypass` 三种模式。
- `je` CLI：基于 stdlib `flag`，覆盖 agents / sessions / vaults / api-keys 等常用操作。
- E2E 测试覆盖 runtime、auth、vault、metrics、webhook 与 M2 experimental API 的关键路径。

## 实验性或 M2-ish API 已挂载

这些 API 已在 `internal/api/api.go` 或对应 `Mount*Routes` 中挂载，但仍应视为实验性能力，文档、兼容性与产品化程度可能不如 V1 runtime core：

- Files API：`/v1/files*`
- Skills API：`/v1/skills*`
- Memory stores：`/v1/memory_stores*`
- Session resources：`/v1/sessions/{id}/resources*`
- Outbound webhooks：`/admin/webhooks*`

## 尚未完成或未开始

- Console UI fork / productization。
- Managed Agents 产品控制面补齐（Dashboard、完整资源 CRUD、memory version audit、
  custom tool handoff、真实 LLM 联调入口）已通过
  [ADR-0027](../30-adr/0027-managed-agents-product-surface.md) 从 parked/promote 为当前推进方向。
- Multi-agent 的自动 subagent routing / 并发调度策略仍需后续迭代；当前已具备 thread 级事件日志与上下文隔离。
- Postgres versioned migrations、生产打包与真实集群长跑验证。
- GitLab review adapter demo。
- One-shot docker-compose 本地体验。
- Docker sandbox。
- MCP stdio transport。
- OIDC / SSO。
- 多租户管理面。
- OpenAPI 契约文件与 API reference 发布。

## API 路由真相源

- 实际路由：`internal/api/api.go` 与 `internal/api/*.go`
- 人类可读清单：`.docs/20-architecture/api-surface.md`
- 未来建议：新增 `api/openapi.yaml`，作为机器可读 API 契约。
