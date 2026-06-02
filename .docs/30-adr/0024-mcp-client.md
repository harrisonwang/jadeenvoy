# ADR-0024: MCP client —— Streamable HTTP transport，无鉴权先行

## Status

`Accepted` — mcp_oauth parked note superseded by ADR-0029

日期: 2026-06-01

## Context

MCP（Model Context Protocol）是 Anthropic 官方与 OMA 的一等公民：agent 可声明
`mcp_servers`，其工具以 `mcp__<server>__<tool>` 暴露给模型。这是"通用 agent 平台"
的关键生态缺口（gap 分析 §5）。立项时 `internal/mcp` 是空目录、`mcp_servers` 配置
存了但没消费。

约束（已与用户确认）：

- **第一刀只做无鉴权 MCP**：内网 MCP server 多数靠网络隔离、不要鉴权。vault 静态
  凭据注入、`mcp_oauth` 自动 refresh **继续 parked**（不动加密子系统）。
- **transport 只做 Streamable HTTP（含 SSE 响应）**，对齐官方 `url` 类型 MCP server。
  **stdio transport 仍 parked**（V1 不做，见 parked.md）。
- 遵循 [ADR-0019] 零第三方依赖：手写 JSON-RPC over HTTP，不引 mcp-go 等 SDK。

## Decision

**新增 `internal/mcp` 包：手写 Streamable HTTP 的 JSON-RPC 客户端，harness 在 turn
开始时连接 agent 声明的 MCP server、发现工具、把它们以 `mcp__<server>__<tool>` 暴露
给 LLM，并把调用路由回对应 server。**

### transport（按 MCP 2025-06-18 spec）

- 每条消息 = 一次 HTTP POST 到 MCP endpoint，`Accept: application/json, text/event-stream`。
- 响应 content-type 二选一，client **都要支持**：
  - `application/json` → 直接解析单个 JSON-RPC response。
  - `text/event-stream` → 读 SSE，取其中的 JSON-RPC response（`id` 匹配）。
- `initialize` 响应头里的 `Mcp-Session-Id` 要在后续所有请求回带；并带
  `MCP-Protocol-Version` 头。
- 握手：`initialize` → `notifications/initialized`（POST，期望 202）→ `tools/list`。

### 工具命名与路由

- 发现的工具名 = `mcp__<serverName>__<toolName>`（对齐官方）。
- harness 把 MCP 工具定义 append 到给 LLM 的 tools 列表（与内置/custom 并列）。
- LLM 调 `mcp__...` 工具 → harness 按前缀路由到对应 client 的 `tools/call`。
- 事件：调用发 `agent.mcp_tool_use`，结果发 `agent.mcp_tool_result`（对齐 spec，
  区别于内置工具的 `agent.tool_use`/`agent.tool_result`）。

### 配置

agent snapshot 的 `mcp_servers: [{type:"url", name, url}]` 在 turn 开始解析。仅
`type:"url"` 生效；其他类型（如未来 stdio）忽略 + 告警。连接/发现失败**不**整轮失败，
仅告警并跳过该 server（degraded，不让一个挂掉的 MCP server 拖死整个 turn）。

### 边界

- 每个 server 默认上限沿用官方语义（≤20），V1 不强制硬限，仅文档说明。
- MCP 调用的超时与重试：复用 harness 现有 per-turn ctx；MCP `tools/call` 失败作为
  `is_error` 的 tool_result 回喂给模型（让模型自行决定），不走 ADR-0022 的 model 重试。

## Consequences

### 正面

- 补齐 MCP 生态接入，agent 可用任意 HTTP MCP server（内网工具、知识库等）。
- 零依赖、与现有 thin-client 风格一致。
- degraded 容错：单个 MCP server 故障不拖垮 turn。

### 负面

- 无鉴权：需要鉴权的公网 MCP server（Linear/GitHub 等）暂连不上 —— 明确是第一刀的
  范围裁剪，后续 ADR 接 vault。
- 每 turn 重新 initialize + tools/list（无连接复用）。V1 可接受，后续可缓存。
- SSE 响应解析增加复杂度，但 spec 要求必须支持。

### 中性

- 新增 `internal/mcp` 包 + harness 的 MCP 装配路径 + 两个事件类型。
- agent_snapshot 已存 `mcp_servers`，无需改 schema。

## Alternatives considered

### 方案 A：只做简单 HTTP JSON-RPC（不支持 SSE 响应）
更简单。**拒因**：spec 允许 server 用 `text/event-stream` 响应 POST，只提供 SSE 的
server 会连不上，兼容面太窄。

### 方案 B：直接做带 vault 鉴权的完整版
更完整。**拒因**：触及加密子系统、工程量翻倍；用户选择先做无鉴权打通主路径。

### 方案 C（本 ADR）：无鉴权 + Streamable HTTP/SSE
打通 MCP 主路径、控制工程量、不动 vault。选它。

## References

- MCP spec 2025-06-18 — Transports（Streamable HTTP）/ Lifecycle
- Anthropic Managed Agents — mcp_servers / `mcp__<server>__<tool>` / `agent.mcp_tool_use`
- [ADR-0019] 零第三方依赖
- parked：stdio transport、mcp_oauth、vault-for-MCP

## Open Questions

- 鉴权（vault static_bearer 注入 → Authorization header）后续 ADR。
- 连接复用 / 工具列表缓存（性能优化）。
- MCP server 发来的 server→client 请求（sampling 等）V1 不处理。
