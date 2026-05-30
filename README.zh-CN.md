<p align="center">
  <strong>JadeEnvoy</strong>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/Go-1.23+-00ADD8?logo=go&logoColor=white" alt="Go 1.23+" />
  <img src="https://img.shields.io/badge/License-Apache_2.0-blue.svg" alt="Apache 2.0 License" />
  <img src="https://img.shields.io/badge/Status-开发中-orange" alt="WIP" />
</p>

# JadeEnvoy

> 开源托管 agent 平台 —— 自托管优先，Go 语言实现。

> [English README](README.md)

**状态: 🚧 开发中。V1 runtime 核心已实现并有 e2e 覆盖；部分 M2 功能处于实验可用状态。**

当前已实现：

- `jed` 守护进程：SQLite 持久化、event log、SSE、metrics、mock 与 OpenAI-compatible provider。
- Agent/session runtime loop：subprocess sandbox，内置 `bash`、`read`、`write`、`edit`、`glob`、`grep` 工具。
- 核心 e2e 闭环：user message → model request → tool use → tool result → final agent message。
- 实验性 M2 API：files、memory stores、skills、custom tools、session resources、outbound webhooks。

尚未完成：

- `je` CLI 与 `je-vault` MITM proxy 目前只是可编译占位。
- Vault 凭据 CRUD / 注入尚未实现；`/api/auth/*` 目前只支持 bypass 模式下的 Console 兼容。
- V1 runtime 路径之外的部分兼容端点可能仍不完整。

JadeEnvoy 是一个 managed-agents 运行时：你写 agent（模型 + 系统提示词 + 工具），
平台处理 agent loop —— 会话管理、沙箱执行、流式事件、凭据注入、历史持久化。

API 与 [Anthropic Managed Agents](https://platform.claude.com/docs/en/managed-agents/)
兼容，**完全跑在你自己的基础设施上**。

## 为什么有这个项目

Anthropic 的 [open-managed-agents](https://github.com/open-ma/open-managed-agents)
(OMA) 设计优秀但 TypeScript + Cloudflare 优先；Node 自托管模式对完全内网部署
有结构性 gap（model_cards stub、Cloudflare 假设渗透、pnpm 版本锁定等，详见
[`.docs/00-motivation/oma-gaps-encountered.md`](.docs/00-motivation/oma-gaps-encountered.md)）。

JadeEnvoy 是 **Go 语言的清洁重新实现**，专注于：

- **自托管优先** —— 无 Cloudflare 依赖，单二进制 + Docker
- **内网 LLM 友好** —— OpenAI 兼容 first-class，对公司内部网关无缝
- **运维简单** —— pure Go，无 CGo，无 pnpm 版本折腾
- **API 兼容** —— 现有 Anthropic SDK 改 base_url 就能用

完整动机见 [`.docs/00-motivation/why-jadeenvoy.md`](.docs/00-motivation/why-jadeenvoy.md)。

## 架构（概览）

```
┌──────────────┐    ┌──────────────┐    ┌──────────────────┐
│ Console UI   │    │   je CLI     │    │ Webhook adapter  │
│  (React)     │    │  (cobra)     │    │  (GitLab/Slack…) │
└──────┬───────┘    └──────┬───────┘    └─────────┬────────┘
       │                   │                      │
       └───────────────────┼──────────────────────┘
                           │ HTTP / SSE
                  ┌────────▼────────┐
                  │      jed         │  ← Go 守护进程
                  │  /v1/*  /admin/* │
                  └────────┬─────────┘
                ┌──────────┴──────────┐
                ▼                     ▼
       ┌──────────────┐      ┌──────────────┐
       │   je-vault    │     │  Sandbox     │
       │  MITM 代理    │     │ subprocess   │
       └───────────────┘     └──────────────┘
                ▼
       SQLite 或 Postgres
```

详见 [`.docs/20-architecture/overview.md`](.docs/20-architecture/overview.md)。

## 路线图

| 里程碑 | 时间 | 主题 |
|---|---|---|
| **M1 — V1 MVP** | 4 周 | GitLab code review bot 端到端 |
| **M2 — Post-MVP** | +2-3 月 | 完整工具集、Memory、Skills、MCP、Docker 沙箱、Webhook |
| **M3 — Mature** | +6 月 | 多租户、OAuth 凭据、多 agent、UI 重写 |

详细 backlog: [`.docs/10-feature-backlog/`](.docs/10-feature-backlog/)。

## 组件

| 二进制 | 角色 |
|---|---|
| `jed` | 主守护进程 —— REST API、agent 编排、harness 循环 |
| `je` | CLI 客户端 —— 当前为占位，尚未实现 |
| `je-vault` | HTTPS MITM 代理 sidecar —— 当前为占位，尚未实现 |

## 快速开始

```bash
make build
JE_AUTH_MODE=bypass JE_LLM_PROVIDER=mock go run ./cmd/jed
curl localhost:8787/health
```

更完整的本地闭环可以直接看 e2e 测试：

```bash
go test ./test/e2e/... -run TestE2E_BashToolUse -v -count=1
```

连接 OpenAI-compatible 内网网关：

```bash
JE_LLM_PROVIDER=openai_compat \
JE_LLM_BASE_URL=https://your-gateway.example.com/v1 \
JE_LLM_API_KEY=... \
go run ./cmd/jed
```

## 文档

- **用户文档** —— V1 发版时一起出（`docs/`，发布到 `docs.jadeenvoy.com`）
- **内部工程文档** —— [`.docs/`](.docs/)
  - [动机](.docs/00-motivation/)
  - [按里程碑组织的功能 backlog](.docs/10-feature-backlog/)
  - [架构](.docs/20-architecture/)
  - [ADR](.docs/30-adr/)
  - [实现笔记](.docs/40-implementation-notes/)

## 贡献

V1 发版后会出 `CONTRIBUTING.md` 含贡献流程 + DCO 要求。

目前阶段：设计 / 范围讨论欢迎以 issue 形式提出。

## 命名由来

- **Envoy** —— 使者、特使、被派遣执行任务的有授权个体
- **Jade** —— 玉令、玉帝、天庭体系，秩序感

详见 [`.docs/30-adr/0002-project-name.md`](.docs/30-adr/0002-project-name.md)。

## License

[Apache 2.0](LICENSE)。

JadeEnvoy Console UI fork 自 [open-ma/open-managed-agents](https://github.com/open-ma/open-managed-agents)
(Apache 2.0)。Attribution 见 [NOTICE](NOTICE)。
