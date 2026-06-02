# ADR-0027: 补齐 Managed Agents 产品控制面

## Status

`Accepted`

日期: 2026-06-01

## Context

JadeEnvoy 的 V1 runtime core 已经具备 managed-agents 的关键内核：Agent、Environment、Session、Event、
append-only event log、harness loop、subprocess sandbox、内置文件/命令工具、MCP、Vault、Auth、Webhook、
Files、Skills、Memory stores 等。

产品定位上，Agent 应被视为**可编排、可托管、可观测、可审计、可恢复、可安全执行、可替换基础设施的系统组件**。
其中“可审计/可恢复/可替换基础设施”是对“可编排、可托管、可观测、可安全执行”的补充：

- 没有审计，长任务与工具执行无法解释和追责。
- 没有恢复，managed agent 仍然是脆弱的一次性进程。
- 没有可替换基础设施，dev SQLite 与 production Postgres 会迫使业务层维护两套实现。

但当前 Console 与若干后端 API 仍处于工程可用而非产品可用状态：

- 开发入口仍以 `mock` 为主，真实 OpenAI-compatible gateway 需要手动拼环境变量。
- Console 的 Dashboard 只显示资源计数，没有体现研发/运营团队真正关注的运行状态、成本、失败、工具调用、
  sandbox/resource、memory 与 webhook 健康。
- 部分已建表或已挂载的资源只有子集 API，例如 session resources 只有 add/delete，memory 缺少版本审计，
  environment/vault/credential 缺少 update/archive 的完整控制面。
- Claude Managed Agents 文档把工具确认、custom tool handoff、memory version audit、SSE observability
  作为长任务运行的关键产品面；JadeEnvoy 已有部分内部事件，但还没有完整 UI 与 API。
- AGENTS.md 中曾把 multi-agent threads / always_ask / memory versioning 等列为 V1 不做。现在用户明确要求
  按 Claude Managed Agents 与 Open Managed Agents 方向补齐，因此这些能力应从 parked 进入后续里程碑，
  但仍需分阶段落地，避免一次性重写 runtime。

参考上游：

- Anthropic Managed Agents 核心概念是 Agent、Environment、Session、Event；Agent 配置包含 model、system、
  tools、MCP servers、skills、multiagent；Session 通过事件驱动并通过 SSE 暴露可观测性。
- Anthropic engineering blog 强调 session log、harness、sandbox 的解耦。
- OMA 的产品叙事强调 BYOK、Managed Agents API 形状兼容、自托管运行面。

## Decision

我们决定按“后端单模块可验证完成，再暴露前端功能”的方式补齐产品控制面。

优先级如下：

1. **真实模型默认入口**：新增安全的配置样例和开发命令，让 `openai_compat` + `tw-agent-max` 成为真实联调路径；
   不把 API key 写入代码、文档或提交历史。
2. **Dashboard 运行视图**：提供管理面聚合数据，展示 session 状态、近期 turn、token usage、失败/终态、
   工具/MCP/custom tool 事件、webhook/vault/memory/resource 资源健康。
3. **API surface 完整化**：对已实现数据模型补齐 CRUD/Archive/Get/List：
   - session resources: list/get/update/delete/add
   - environments: update/archive
   - vaults/credentials: update/archive/get
   - memory stores: update/archive
   - memories: create/update/delete/list/get，创建与更新语义与上游区分
   - memory versions: list/get/redact
4. **Guardrails / tool confirmation**：实现 `permission_policy` 的 `always_allow` 与 `always_ask`，当需要确认时发布
   `agent.tool_use` 或 `agent.mcp_tool_use` 后进入 `session.status_idle{stop_reason:requires_action}`，等待
   `user.tool_confirmation`。
5. **Custom tool handoff**：把 custom tool 暂停语义对齐为
   `session.status_idle{stop_reason:{type:"requires_action",event_ids:[...]}}`，并让前端可提交
   `user.custom_tool_result`。
6. **Handoffs / multiagent**：先保持 schema 兼容并在 Console 暴露配置与状态；真正的 session threads、
   subagent routing、thread stream 作为下一阶段实现，必须单独 ADR 细化事件模型和并发语义。

## Consequences

### 正面

- Console 会从“资源表单集合”变成研发/运营能判断平台是否健康的控制面。
- 每个后端模块都有对应前端入口和测试，减少“挂了路由但没人能用”的漂移。
- 真实 LLM gateway 变为显式支持路径，同时不泄露用户 API key。
- Memory version audit 与 redact 给合规和调试留下可追溯面。
- Guardrails 与 custom handoff 对齐 Claude Managed Agents 的事件流语义。

### 负面

- API surface 扩大后，e2e 测试数量和维护成本上升。
- `always_ask` 会让 harness 状态机更复杂，需要处理确认、拒绝、恢复和 interrupt 的交错。
- Memory versioning 会增加写放大；每次 create/update/delete 都会产生版本行。
- Multiagent/handoff 完整实现不是单个 handler 能解决的问题，需要后续设计 thread event log 与调度。

### 中性

- `mock` 仍保留给测试；真实开发路径通过 env 启用 `openai_compat`。
- 不实现的上游 endpoint 仍应返回 404 或明确 501，不能伪造 `{data: []}`。
- Console 可以展示“未实现/设计中”的 capability 状态，但不能把它伪装成已完成能力。

## Alternatives considered

### 只改前端

拒绝。前端无法弥补缺失的 memory versions、tool confirmation 或资源 CRUD，继续堆 UI 会制造假能力。

### 一次性实现完整 Claude parity

拒绝。Self-hosted workers、multiagent threads、outcomes、dreams、OAuth refresh 等需要更大设计面，
一次性实现会破坏现有稳定内核。应先补齐已建模资源和运行可观测性，再推进独立复杂能力。

### 把 API key 写进仓库默认配置

拒绝。真实网关配置必须通过环境变量、未跟踪的本地 env 文件或进程管理器注入。

## References

- Anthropic Managed Agents overview
- Anthropic Managed Agents agent setup / sessions / events / tools / memory / MCP connector docs
- Anthropic Engineering: Scaling Managed Agents
- Open Managed Agents public site and API compatibility claims
- ADR-0011 LLM provider abstraction
- ADR-0021 Context compaction
- ADR-0022 Error recovery state machine
- ADR-0024 MCP client
- ADR-0025 user interrupt
- ADR-0026 MCP vault auth

## Open Questions

- Multiagent session threads 是否复用 `session_event.thread_id`，还是需要独立 `session_thread` 表。
- Tool confirmation 的 pending actions 是否需要单独持久化表，还是可以从 unresolved event log 推导。
- Memory versions 是否需要 rollback endpoint；上游目前只提供 list/get/redact。
- Dashboard 聚合接口应保持 `/admin/dashboard` 私有，还是未来发布只读 `/v1/observability`。
