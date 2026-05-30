# AGENTS.md

本文档是 AI 编码助手（Claude Code / Cursor / Codex / Aider / OpenCode 等）
在此仓库工作时的指南。手工写代码的人也可以读，作为入门快查。

## 项目是什么

**JadeEnvoy** 是 Go 实现的 managed-agents 平台 —— API 对齐
[Anthropic Managed Agents](https://platform.claude.com/docs/en/managed-agents/)
spec，自托管优先。定位是 OMA
([open-managed-agents](https://github.com/open-ma/open-managed-agents))
的替代实现，专门修补 OMA Node 自托管模式在完全内网场景下的结构性 gap。

**在建议"直接给 OMA 提 PR"之前，先看
[`.docs/00-motivation/oma-gaps-encountered.md`](.docs/00-motivation/oma-gaps-encountered.md)。**
里面列了我们踩过的 11 个具体坑，决策路径在
[`.docs/00-motivation/why-jadeenvoy.md`](.docs/00-motivation/why-jadeenvoy.md)。

## 常用命令

```bash
# 构建 / 测试
make build              # 构建 jed + je + je-vault 到 bin/
make jed                # 仅 jed
go test ./...           # 全部单测 + 集成测试
go test ./test/e2e/... -v -count=1                          # 端到端测试
go test ./test/e2e/... -run TestE2E_BashToolUse -v -count=1 # 跑单个 e2e
go vet ./...
gofmt -s -w .

# 开发循环
make dev                # 起 jed dev 模式（AUTH_MODE=bypass、SQLite、mock LLM）
JE_LLM_PROVIDER=mock JE_AUTH_MODE=bypass go run ./cmd/jed
curl localhost:8787/health
```

**V1 验收标准是 `test/e2e/e2e_test.go` 里的 `TestE2E_BashToolUse`** ——
进程内起完整栈，跑 mock provider 脚本化的多轮 LLM + 真 subprocess 沙箱 + bash 工具，
验证事件序列。改了核心代码先跑这个测试。

## 架构 — 大图

```
┌─────────────────────────────────────────────────────────────────┐
│ jed (守护进程, cmd/jed/main.go)                                 │
│                                                                 │
│  HTTP (chi) ──▶ session.Service ──▶ harness.RunTurn (循环) ──▶ │
│                                       │                         │
│                                       ├─▶ provider.Provider     │
│                                       │   (mock / oai / ant)    │
│                                       │                         │
│                                       ├─▶ tool.Tool ──▶ sandbox │
│                                       │                         │
│                                       └─▶ event.Broker          │
│                                              │                  │
│                                              ▼                  │
│                                       store (SQLite/PG)         │
│                                       ├─ append-only event log  │
│                                       └─ session.next_seq       │
└─────────────────────────────────────────────────────────────────┘
```

**两个关键不变量必须牢记：**

1. **Event log 是真相源**。`harness.buildMessages` 每轮都从 `session_event`
   表重新拼 LLM 历史。进程内不留任何 session 状态。崩溃重启后，下一条
   `user.message` 完全从 DB 重建上下文。

2. **Harness loop 不阻塞 HTTP handler**。`POST /v1/sessions/:id/events`
   同步落事件，然后 spawn goroutine 跑 `harness.RunTurn`。
   **goroutine 里必须用 `context.Background()`，不能用 `r.Context()`** ——
   `net/http` 在 ServeHTTP 返回时取消 request context，会立刻杀掉 agent
   循环（之前踩过这个坑已经修了，别再引入）。

Agent loop 主体在 `internal/harness/harness.go`：

```
RunTurn:
  publish session.status_running
  for step := 0; step < MaxSteps; step++ {
    messages = buildMessagesFromEventLog()
    publish span.model_request_start
    stream = provider.Stream(req)
    累积 text + pending tool_uses + usage
    publish span.model_request_end
    if 有 text: publish agent.message
    if 有 tool_uses:
      for each tool_use:
        publish agent.tool_use
        result = tool.Execute(sandbox, input)
        publish agent.tool_result
      continue   // 下一轮重新拼 messages（含 tool_result）
    break        // 没工具调用 → end_turn
  }
  publish session.status_idle
```

## API 表面约定

- **`/v1/*` 严格对齐 Anthropic spec 形状**。字段名 (snake_case)、事件类型字符串
  (`session.status_idle`)、错误响应 shape —— 全对齐。用户拿 Anthropic SDK
  改 base_url 就能直接用。
- **`/admin/*` 是 JadeEnvoy 私有**。API keys、tenants、webhooks、自定义集成
  都在这。不要污染 `/v1/*` 路径。
- **不实现的 endpoint 直接 404，绝对不返回 `{data: []}`**。OMA 在
  `/v1/runtimes`、`/v1/model_cards` 这么干，shape 跟前端期望不匹配导致 Console
  崩溃，还把用户卡在死循环 UI 里。详见
  [`.docs/30-adr/0007-api-compatibility.md`](.docs/30-adr/0007-api-compatibility.md)。

## 必须知道的约定

- **ID**: ULID 加类型前缀（`agt-`、`sess-`、`evt-`、`vlt-`、`crd-`、`mst-`、
  `mem-`、`mver-`、`skl-`、`tnt-`、`usr-`、`key-`、`whk-`）。通过
  `store.NewID(prefix)` 生成。
- **DB 时间戳**: `BIGINT` unix 毫秒（跨 SQLite/PG 兼容）。`time.Time` 仅
  在 API 边界用。
- **JSON 列**: SQLite 用 `TEXT` + `CHECK(json_valid(...))`。PG 后续用 `JSONB`
  (M2)。V1 暂未引 sqlc，手写 SQL + prepared statement。
- **Vault credential 唯一性**: `(vault_id, mcp_server_host)` 设计上 UNIQUE。
  不要绕过 —— OMA "第一个匹配的胜出" 是坑
  ([`.docs/00-motivation/oma-gaps-encountered.md`](.docs/00-motivation/oma-gaps-encountered.md) 第 9 条)。
- **AUTH_MODE 三档**: `required` / `optional` / `bypass`。`bypass` 模式下
  `/api/auth/*` 路由**仍然挂载**（返回虚拟 default user），让 Console 不会
  崩。**不要像 OMA 那样按 mode 卸载整个路由**。

## 文档怎么查

- [`.docs/10-feature-backlog/`](.docs/10-feature-backlog/) — 要做什么、什么时候做，
  按 M1/M2/M3/parked 切。**这是"现在该不该做这个 feature"的真相源**。不在当前
  milestone 的功能，需要 PR promote 才能加。
- [`.docs/20-architecture/`](.docs/20-architecture/) — 系统设计、模块布局、
  API 表、数据模型。新人先看 `overview.md`。
- [`.docs/30-adr/`](.docs/30-adr/) — 每个非平凡技术选型（18 篇 ADR）。
  在提"换用 X 替代 Y"之前，先查是不是已经有 ADR 解释为啥选 Y。改决策必须新写
  一篇 ADR，**不要直接改老 ADR**。
- [`.docs/40-implementation-notes/`](.docs/40-implementation-notes/) — 实现期间
  踩坑沉淀。调试出非平凡问题就往这里加。

## V1 明确**不做**的事

(想加这些功能前，先看
[`.docs/10-feature-backlog/parked.md`](.docs/10-feature-backlog/parked.md)
或 `M2-post-mvp.md` / `M3-mature.md`。)

- Cloudflare Workers / D1 / KV / R2（我们只做 Node 风格 + pure Go）
- Anthropic 风格的 cloud environment 预装包
- `outcomes` / grader / `user.define_outcome` 流程
- `dreams`（memory store 离线整理）
- Self-hosted environment worker（反向架构）
- MCP stdio transport / MCP tunnels
- Multi-agent threads（`multiagent.coordinator`）
- `always_ask` permission policy（`requires_action` idle 流）
- OAuth (`mcp_oauth`) 凭据自动 refresh
- Memory 版本化 + CAS + redact
- web_fetch / web_search 工具

## 在这个仓库工作时

- V1 MVP 已实现且 e2e 测试通过。代码量很小（~2,500 行 Go）且干净 ——
  **不要做投机性重构**。
- 加功能时：**先在 `test/e2e/` 写/扩 e2e 测试**，看它失败，再写实现。Mock provider
  (`internal/provider/mock.go`) 就是为脚本化多轮流程设计的。
- 写新 ADR 时拷贝 `.docs/30-adr/template.md` 续编号。不要改老 ADR（除了 status
  字段加 `Superseded by`）。
- 用 `modernc.org/sqlite`（纯 Go、无 CGo）是**有意选择**，为了跨平台编译干净。
  **不要换成 `mattn/go-sqlite3`**。
- 真实 LLM provider 实现是 M2 高优先级任务（V1 只有 mock）。加
  `internal/provider/oaicompat/` 时**不要引 `go-openai`**（依赖太重），
  自己写 thin client，详见
  [`.docs/30-adr/0011-llm-provider-abstraction.md`](.docs/30-adr/0011-llm-provider-abstraction.md)。

## 给各类 agentic 工具的兼容入口

| 工具 | 读取文件 | 怎么生效 |
|---|---|---|
| Claude Code | `CLAUDE.md` | 仓库根的 `CLAUDE.md` 是 symlink → 本文件 |
| OpenAI Codex CLI / GPT-Codex | `AGENTS.md` | 直接读本文件 |
| Cursor | `.cursorrules` 或 `.cursor/rules/*` | 未配置；可建 symlink |
| Aider | `CONVENTIONS.md` | 未配置；可建 symlink |
| OpenCode / Continue / Cline | 通常 `AGENTS.md` 或自家文件 | 直接读本文件 |

新工具要接入：在仓库根加 symlink 指向 `AGENTS.md` 即可，不要复制内容（避免漂移）。
