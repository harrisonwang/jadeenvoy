# M3 — Mature（M1 后 6 月）

**目标**：企业级、SaaS-ready、能对外提供 hosting 服务。

---

## 1. 完整多租户

### F-M3-001 — 多租户隔离

- **What**: 完整 tenant 模型，DB 行级隔离
- **DB**: 所有业务表加 `tenant_id`，索引带前导 tenant_id
- **API**: 路由层注入 `tenant_id` 上下文
- **隔离测试**: 自动化 fuzz —— 跨 tenant 调用必拒
- **依赖**: M2 团队模型升级 / 重设计

### F-M3-002 — Tenant 配额

- 每 tenant：
  - max sessions
  - max LLM token / day
  - max storage GB
- 命中限额：429 + `Retry-After`

### F-M3-003 — 计费集成（可选）

- 出账接口（Stripe / 银联 / 内部计费系统）
- 用 token / session count / GB-hour 计费

---

## 2. 高级 Vault

### F-M3-004 — `mcp_oauth` 凭据 + 自动 refresh

**当前状态（2026-06-02）**：基础 `mcp_oauth` credential、refresh token 自动刷新、validate endpoint 已实现。
未实现：授权码启动流程、401 后重试、CAS 写回、refresh failed webhook。

- **What**: OAuth access token 自动 refresh
- **Spec**: [vaults#mcp-oauth](https://platform.claude.com/docs/en/managed-agents/vaults.md)
- **流程**:
  - 401 时按 `token_endpoint` 调 refresh
  - CAS 写回新 token（防并发）
  - 失败 emit `vault_credential.refresh_failed` webhook
- **支持的 `token_endpoint_auth.type`**: `none` / `client_secret_basic` / `client_secret_post`
- **校验端点**: `POST /v1/vaults/:id/credentials/:cred_id/mcp_oauth_validate`

### F-M3-005 — `cap_cli` 凭据（CLI 注入）

- **What**: 给 CLI 工具（`gh` / `glab` / `aws`）注入 env var 或修改请求 header
- **依据**: OMA 的 cap 仓库
- **匹配**: 沙箱里 `gh` / `glab` 命令调用时按 cli_id 注入
- **实现**: subprocess `Exec` 时检测命令前缀，inject env

---

## 3. Memory 版本化 + 合规

### F-M3-006 — Memory 版本化

- **What**: 每次写 memory 创不可变 version
- **DB**: `memory_versions(id, memory_id, content, content_sha256, operation, created_at)`
- **API**:
  - `GET /v1/memory_stores/:id/memory_versions?memory_id=`
  - `GET /v1/memory_stores/:id/memory_versions/:ver_id`
- **保留**: 30 天 + 每 memory 最新永久

### F-M3-007 — CAS（content_sha256 precondition）

- **What**: 乐观并发，update 时 sha256 不匹配 → 409
- **API**: `precondition: {type: "content_sha256", content_sha256: "..."}`

### F-M3-008 — Memory Version Redact

- **What**: 擦除历史 version 内容，保留 audit
- **API**: `POST /v1/memory_stores/:id/memory_versions/:ver_id/redact`
- **限制**: 不能 redact 当前 head version

---

## 4. Multi-Agent

### F-M3-009 — Coordinator + sub-agent threads

**当前状态（2026-06-02）**：agent `multiagent` 配置保存、session snapshot 保留、事件写入/查询/harness
上下文已支持 `session_thread_id` 隔离。未实现：自动 sub-agent selection、并发调度、coordinator 汇总。

- **What**: 一个 agent 委派到子 agents，并行执行
- **Spec**: [multi-agent](https://platform.claude.com/docs/en/managed-agents/multi-agent.md)
- **限制**:
  - 协调器只能委派 1 层
  - 20 unique agents / coordinator
  - 25 concurrent threads / session
- **共享**: 容器 + vault（agent 隔离 MCP server）

### F-M3-010 — Thread API

**当前状态（2026-06-02）**：`POST /v1/sessions/:id/events` 与 `GET /v1/sessions/:id/events`
已支持 `session_thread_id` 写入/过滤。专门的 `/threads` 管理 API 与 thread stream 未实现。

- `GET /v1/sessions/:id/threads` list
- `POST /v1/sessions/:id/threads/:tid/archive`
- `GET /v1/sessions/:id/threads/:tid/stream`
- `GET /v1/sessions/:id/threads/:tid/events`
- `user.interrupt` 带 `session_thread_id` 打断指定 thread

### F-M3-011 — Multi-agent events

新增事件类型：
- `session.thread_created/idle/running/terminated`
- `agent.thread_message_sent/received`
- `agent.thread_context_compacted`

---

## 5. Permission Policies

### F-M3-012 — `always_ask` 流程

**当前状态（2026-06-02）**：内置工具与 MCP 工具的 `always_ask` 暂停、`user.tool_confirmation`
allow/deny 恢复、dashboard 计数已实现。

- **What**: 工具调用要 user 批准
- **Spec**: [permission-policies](https://platform.claude.com/docs/en/managed-agents/permission-policies.md)
- **事件流**:
  - agent 发 `agent.tool_use`
  - session 进 `idle{stop_reason: requires_action, event_ids: [...]}`
  - client 发 `user.tool_confirmation{tool_use_id, result, deny_message?}`
  - 解除 → `running`
- **配置位置**:
  - Toolset 默认: `default_config.permission_policy`
  - 单工具覆盖: `configs[].permission_policy`
  - MCP toolset: 同上，默认 `always_ask`

---

## 6. 高级集成

### F-M3-013 — GitHub repository resource type

- **What**: `resources: [{type: "github_repository", url, mount_path, authorization_token}]`
- **行为**: session 启动时自动 clone，token 接入 vault
- **创建 PR**: 配合 GitHub MCP server

### F-M3-014 — GitLab repository resource type

- **What**: 对应 GitLab 自建场景
- **行为**: 同上但 host 可配
- **vault 集成**: 自动加 static_bearer 凭据匹配该 host

### F-M3-015 — web_fetch 工具

- **依赖**: Tavily / Brave Search / 自建抓取
- **配置**: `JE_WEB_FETCH_PROVIDER`
- **沙箱集成**: 走代理出公网（如允许）

### F-M3-016 — web_search 工具

- **依赖**: Tavily / Bing Web Search API
- **配置**: `JE_WEB_SEARCH_PROVIDER`

---

## 7. 安全 + 合规

### F-M3-017 — 审计日志

- 所有 admin 操作（agent create / vault create / user login / API key 创建/删除）落审计表
- 不可改 + 长期保留
- 导出 CSV / 接 SIEM

### F-M3-018 — 数据加密增强

- 全字段加密（不仅 vault token，agent system prompt 也加密落库）
- KMS 集成（AWS KMS / 阿里云 KMS）

### F-M3-019 — 沙箱网络隔离强化

- Docker 沙箱默认 disable 出站，白名单 + MCP server 显式允许
- 跟 Anthropic cloud `networking.limited` 模式对齐

---

## 8. Console UI 重写

### F-M3-020 — UI v2

- **What**: 从 fork 状态切换到自主代码库
- **框架**: SvelteKit 或 Solid（轻量）
- **设计**: 重新设计交互，不再受 OMA UI 限制
- **保留**: 核心页面布局，但全新组件库
- **依赖**: M2 加的所有页面都要 port 过来

---

## 9. 高级 sandbox

### F-M3-021 — containerd 直跑

- **What**: 不依赖 dockerd，直接对 containerd RPC
- **库**: `containerd/containerd`
- **优势**: 更轻，无 docker daemon 依赖

### F-M3-022 — Firecracker（强隔离）

- **What**: 每 session 一个 microVM
- **依据**: AWS Firecracker，类似 LiteBox（OMA 用过）
- **场景**: 跑不可信代码 / 跨租户隔离强保证

---

## M3 验收

- 多个 production tenant，至少 1 个外部客户
- 多团队场景跑稳
- 通过基础安全审计（OWASP top 10、Vault 合规）
- 性能：单节点 1000 concurrent sessions
- 可用性：99.9% SLA
