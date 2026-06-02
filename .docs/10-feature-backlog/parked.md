# Parked features

更新时间：2026-06-02

以下功能当前不在近期范围内。需要推进时，先更新 backlog / ADR，再进入实现。

## 明确暂缓

- Cloudflare Workers / D1 / KV / R2 部署形态。
- Anthropic cloud environment 预装包语义。
- Self-hosted environment worker 反向架构（官方 `self_hosted` environment 的 work-queue 协议：
  Anthropic 编排 + 你只执行工具）。**有意不做** —— 与 JadeEnvoy "agent loop / 工具 / 模型整条链
  自托管" 的方向相反，详见 [`../00-motivation/why-jadeenvoy.md`](../00-motivation/why-jadeenvoy.md)
  「官方 self-hosted sandbox GA 之后」。
- Outcomes / grader / `user.define_outcome` 流程。
- Dreams / memory store 离线整理。
- Multi-agent 自动 subagent selection、并发调度与专门 thread API（基础 `session_thread_id`
  事件路由已移到 `current.md`）。
- MCP stdio transport。
- MCP tunnels。
- Memory 版本化 + CAS + redact。
- `web_fetch` / `web_search` 工具。
- Containerd / Firecracker sandbox。

## 原则

- `/v1/*` 未实现的 Anthropic endpoint 应返回 404 或明确错误，不返回假 `{ "data": [] }` stub。
- Parked 功能不应出现在 Console 主路径中误导用户。
- 如果某项功能已经实验性实现，需要从本页移到 `current.md` 并标注稳定性。
