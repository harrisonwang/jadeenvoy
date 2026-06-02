# ADR-0030: 用 session_thread_id 隔离 Multi-agent Thread 上下文

## Status

`Accepted`

日期: 2026-06-02

## Context

JadeEnvoy 的 `session_event` 表从早期 schema 起就有 `thread_id`，但 API 写入与 harness 运行一直固定为
`primary`。这导致 `multiagent.coordinator` 配置即使能被前端提交，也无法在事件日志中表达不同 worker /
handoff thread 的独立上下文。

Multi-agent 的完整自动调度仍然需要更多设计：subagent 如何选择、是否并发、如何汇总、失败如何恢复。但 thread
级事件隔离是无论哪种调度策略都必须先具备的底座。

## Decision

我们决定先落地 thread-aware event log 与 harness routing：

- `POST /v1/sessions/{id}/events` 读取每个事件的 `session_thread_id`；缺省仍为 `primary`。
- 触发 turn 时按 thread 调用 `Harness.RunThread(session_id, thread_id)`；原 `RunTurn` 保持为 primary 兼容入口。
- `harness.buildMessages`、tool confirmation、context compaction 都只读取当前 thread 的事件。
- harness 产生的 status/span/agent/tool 事件写回同一个 `session_thread_id`。
- `GET /v1/sessions/{id}/events?session_thread_id=...` 支持按 thread 过滤。
- Agent create/update 保存 `multiagent` 配置，session snapshot 也保留该配置。

## Consequences

### 正面

- Event log 仍是唯一真相源，但同一 session 内可以承载多个独立上下文。
- 后续实现 coordinator/subagent routing 时，不需要再改事件表或基础 API。
- primary thread 完全兼容既有客户端。

### 负面

- 当前仍是显式 thread routing，不包含自动 subagent selection。
- Session status 仍是 session 级字段，多 thread 并发调度需要后续设计避免状态互相覆盖。

### 中性

- 当前实现仍串行化同一 session 的 turn，优先保证状态一致性；真正并发执行留给后续 ADR。

## References

- [ADR-0027: 补齐 Managed Agents 产品控制面](0027-managed-agents-product-surface.md)
