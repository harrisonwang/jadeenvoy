# 30 — Architecture Decision Records (ADR)

每一个不可回退的技术选型，背后的取舍记录。

## 格式

遵循 [MADR](https://adr.github.io/madr/) 风格：

- **Status** — `Accepted` / `Proposed` / `Superseded by ADR-NNNN`
- **Context** — 为什么要做这个决定
- **Decision** — 决定了什么
- **Consequences** — 好处、坏处、中性影响
- **Alternatives considered** — 考虑过的其他方案

每个 ADR 一个文件，编号递增。**不要改老 ADR**（除了 status），新决定写新 ADR 标
`Superseded by`。

## ADR 索引

### 项目级

| 编号 | 标题 | 状态 |
|---|---|---|
| [0001](0001-language-choice.md) | 选 Go 作为主语言 | Accepted |
| [0002](0002-project-name.md) | 项目名 JadeEnvoy | Accepted |
| [0003](0003-monorepo-layout.md) | V1 单仓库 monorepo + 多 binary | Accepted |
| [0016](0016-license-apache2.md) | License Apache 2.0 | Accepted |
| [0017](0017-go-version.md) | Go 1.23+ | Accepted |
| [0018](0018-cli-naming.md) | CLI 叫 `je`，daemon 叫 `jed` | Accepted |
| [0020](0020-api-contract-and-doc-sync.md) | 以代码和当前状态文档收敛 API 契约 | Accepted |

### 技术栈

| 编号 | 标题 | 状态 |
|---|---|---|
| [0004](0004-http-framework.md) | HTTP 框架选 chi | Accepted |
| [0005](0005-sql-stack.md) | SQL 栈：pgx + modernc/sqlite + sqlc + goose | Superseded by ADR-0028 |
| [0006](0006-https-mitm-proxy.md) | HTTPS MITM 用 goproxy | Accepted |
| [0011](0011-llm-provider-abstraction.md) | LLM provider 抽象，不绑 SDK | Accepted |
| [0012](0012-event-broker.md) | 事件 broker V1 进程内 channels | Accepted |
| [0013](0013-auth.md) | Auth 自实现 cookie session + API key | Accepted |
| [0019](0019-zero-dependency-crypto-and-proxy.md) | 用标准库实现 crypto 与 MITM proxy 关键路径 | Accepted |
| [0028](0028-store-dialect-adapter.md) | 用 Store Dialect Adapter 支持 SQLite 与 Postgres | Accepted |
| [0029](0029-mcp-oauth-refresh.md) | 支持 MCP OAuth 凭据自动 Refresh | Accepted |
| [0030](0030-session-thread-routing.md) | 用 session_thread_id 隔离 Multi-agent Thread 上下文 | Accepted |

### 产品 / 范围

| 编号 | 标题 | 状态 |
|---|---|---|
| [0007](0007-api-compatibility.md) | API 严格兼容 Anthropic `/v1/*` | Accepted |
| [0008](0008-v1-scope.md) | V1 范围砍 60% | Historical / Accepted |
| [0009](0009-ui-fork.md) | UI V1 fork OMA Console，V3 重写 | Accepted |
| [0010](0010-sandbox-providers.md) | 沙箱 V1 仅 subprocess，V2 加 Docker | Accepted |
| [0014](0014-memory-cas.md) | Memory CAS 延后到 M3 | Accepted |
| [0015](0015-vault-credential-types.md) | Vault V1 仅 static_bearer | Superseded by ADR-0029 |
| [0021](0021-context-compaction.md) | Context compaction | Accepted |
| [0022](0022-error-recovery-state-machine.md) | 错误恢复状态机 | Accepted |
| [0023](0023-environments-resource.md) | Environments resource | Accepted |
| [0024](0024-mcp-client.md) | MCP client | Accepted |
| [0025](0025-user-interrupt.md) | User interrupt | Accepted |
| [0026](0026-mcp-vault-auth.md) | MCP Vault auth | Accepted |
| [0027](0027-managed-agents-product-surface.md) | 补齐 Managed Agents 产品控制面 | Accepted |

## 写新 ADR

```bash
cp template.md 0021-my-new-decision.md
# 写完后 PR
```

## 改老 ADR

不允许。Status 字段可以改为 `Superseded by ADR-NNNN`，正文不动。
新决定写新 ADR 引用老的。

## 工具

如果想生成 ADR 索引或可视化决策树，推荐 [`adr-tools`](https://github.com/npryce/adr-tools)，
但不强制。手写也行。
