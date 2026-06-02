# API 表面

JadeEnvoy 当前实际暴露的 HTTP 路由清单。

路由分组：
- **`/v1/*`** —— Anthropic Managed Agents 兼容 API
- **`/admin/*`** —— JadeEnvoy 私有管理 API（API keys / webhooks 等）
- **`/api/auth/*`** —— 浏览器登录态 API
- **`/health`、`/metrics`** —— 运维端点

> 本文件描述**当前代码已挂载的端点**。规划中但未实现的端点不放进主清单，避免误导调用方。

## 通用约定

- Body: `application/json`，文件上传除外
- 错误响应：
  ```json
  { "error": { "type": "string", "message": "string", "code": "string" } }
  ```
- 认证：
  - 浏览器：cookie session
  - 程序化：`x-api-key: <key>` 或 `Authorization: Bearer <key>`
  - `JE_AUTH_MODE=bypass` 时未认证请求按 default user / default tenant 处理
- 鉴权：tenant / user 上下文由 auth middleware 注入
- `/v1/*` 和 `/admin/*` 当前挂载 `RequireAuth` middleware
- Anthropic 兼容 header（如 `anthropic-version`、`anthropic-beta`）当前接受但不强制

---

## 运维端点

| 方法 | 路径 | 描述 |
|---|---|---|
| GET | `/health` | 健康检查 |
| GET | `/metrics` | Prometheus metrics |

---

## `/api/auth/*` 浏览器登录

所有 auth mode 下都会挂载这些路由。`bypass` 模式下返回虚拟 default user，避免 Console 被登录页卡住。

| 方法 | 路径 | 描述 |
|---|---|---|
| GET | `/api/auth/session` | 当前登录态；未登录返回 401，bypass 返回虚拟 session |
| POST | `/api/auth/signup` | email/password 注册；注册成功后自动登录 |
| POST | `/api/auth/login` | email/password 登录并下发 cookie |
| POST | `/api/auth/logout` | 删除服务端 session 并清理 cookie |

---

## `/v1/*` 兼容 API

### Environments

`config.type` 仅支持 `cloud`；`self_hosted` 返回 501（ADR-0023）。

| 方法 | 路径 | 描述 |
|---|---|---|
| POST | `/v1/environments` | 创建 environment（cloud） |
| GET | `/v1/environments` | 列出 environments |
| GET | `/v1/environments/{id}` | 获取单个 environment |
| DELETE | `/v1/environments/{id}` | 删除 environment（`default` 不可删） |

### Agents

Agent request/response 支持保存 `multiagent` 配置；session 创建时该配置进入 agent snapshot。

| 方法 | 路径 | 描述 |
|---|---|---|
| POST | `/v1/agents` | 创建 agent |
| GET | `/v1/agents` | 列出 agents |
| GET | `/v1/agents/{id}` | 获取单个 agent |
| POST | `/v1/agents/{id}` | 更新 agent |
| DELETE | `/v1/agents/{id}` | 删除 agent |
| POST | `/v1/agents/{id}/archive` | 归档 agent |
| GET | `/v1/agents/{id}/versions` | 列出 agent versions |

### Sessions

`POST /events` 支持在单个事件上带 `session_thread_id`；省略时为 `primary`。`GET /events`
可用 `?session_thread_id=<id>` 过滤单个 thread 的事件。`user.message`、
`user.custom_tool_result`、`user.tool_confirmation` 会触发对应 thread 的 harness turn。

| 方法 | 路径 | 描述 |
|---|---|---|
| POST | `/v1/sessions` | 创建 session |
| GET | `/v1/sessions` | 列出 sessions |
| GET | `/v1/sessions/{id}` | 获取单个 session |
| POST | `/v1/sessions/{id}` | 更新 session |
| DELETE | `/v1/sessions/{id}` | 删除 session |
| POST | `/v1/sessions/{id}/archive` | 归档 session |
| POST | `/v1/sessions/{id}/events` | 写入 user events，并异步触发 harness turn |
| GET | `/v1/sessions/{id}/events` | 列出 session events |
| GET | `/v1/sessions/{id}/events/stream` | SSE 订阅 session events；支持 `?since=<seq>` / `Last-Event-ID` 回放补发 |

### Session resources

| 方法 | 路径 | 描述 |
|---|---|---|
| GET | `/v1/sessions/{id}/resources` | 列出 session resources |
| POST | `/v1/sessions/{id}/resources` | 添加 session resource |
| GET | `/v1/sessions/{id}/resources/{resId}` | 获取 session resource |
| POST | `/v1/sessions/{id}/resources/{resId}` | 更新 session resource |
| DELETE | `/v1/sessions/{id}/resources/{resId}` | 删除 session resource |

### Vaults

Vault credential 支持 `static_bearer` 与 `mcp_oauth`。secret 字段永不出现在响应中；
`mcp_oauth` access token 过期时由 Vault resolve 路径自动 refresh 并加密写回。

| 方法 | 路径 | 描述 |
|---|---|---|
| POST | `/v1/vaults` | 创建 vault |
| GET | `/v1/vaults` | 列出 vaults |
| GET | `/v1/vaults/{id}` | 获取单个 vault |
| DELETE | `/v1/vaults/{id}` | 删除 vault |
| POST | `/v1/vaults/{id}/archive` | 归档 vault |
| POST | `/v1/vaults/{id}/credentials` | 添加 credential |
| GET | `/v1/vaults/{id}/credentials` | 列出 credentials（不返回 secret） |
| GET | `/v1/vaults/{id}/credentials/{credId}` | 获取 credential（不返回 secret） |
| POST | `/v1/vaults/{id}/credentials/{credId}` | 更新 credential |
| DELETE | `/v1/vaults/{id}/credentials/{credId}` | 删除 credential |
| POST | `/v1/vaults/{id}/credentials/{credId}/archive` | 归档 credential |
| POST | `/v1/vaults/{id}/credentials/{credId}/mcp_oauth_validate` | 校验 `mcp_oauth` 凭据可解密且 shape 合法 |

### Memory stores

当前路由前缀是 `/v1/memory_stores`。

| 方法 | 路径 | 描述 |
|---|---|---|
| POST | `/v1/memory_stores` | 创建 memory store |
| GET | `/v1/memory_stores` | 列出 memory stores |
| GET | `/v1/memory_stores/{id}` | 获取 memory store |
| POST | `/v1/memory_stores/{id}` | 更新 memory store |
| DELETE | `/v1/memory_stores/{id}` | 删除 memory store |
| POST | `/v1/memory_stores/{id}/archive` | 归档 memory store |
| POST | `/v1/memory_stores/{id}/memories` | upsert memory |
| GET | `/v1/memory_stores/{id}/memories` | 列出 memories |
| GET | `/v1/memory_stores/{id}/memories/{mid}` | 获取单个 memory |
| PATCH | `/v1/memory_stores/{id}/memories/{mid}` | 更新 memory |
| DELETE | `/v1/memory_stores/{id}/memories/{mid}` | 删除 memory |
| GET | `/v1/memory_stores/{id}/memory_versions` | 列出 memory versions |
| GET | `/v1/memory_stores/{id}/memory_versions/{vid}` | 获取 memory version |
| POST | `/v1/memory_stores/{id}/memory_versions/{vid}/redact` | redact memory version |

### Files

| 方法 | 路径 | 描述 |
|---|---|---|
| POST | `/v1/files` | 上传文件 |
| GET | `/v1/files` | 列出文件 |
| GET | `/v1/files/{id}` | 获取文件 metadata |
| GET | `/v1/files/{id}/content` | 下载文件内容 |
| DELETE | `/v1/files/{id}` | 删除文件 |

### Skills

| 方法 | 路径 | 描述 |
|---|---|---|
| POST | `/v1/skills` | 创建 skill |
| GET | `/v1/skills` | 列出 skills |
| GET | `/v1/skills/{id}` | 获取单个 skill |
| DELETE | `/v1/skills/{id}` | 删除 skill |

---

## `/admin/*` 私有管理 API

### API keys

| 方法 | 路径 | 描述 |
|---|---|---|
| POST | `/admin/api_keys` | 创建 API key；明文 key 仅返回一次 |
| GET | `/admin/api_keys` | 列出 API keys |
| DELETE | `/admin/api_keys/{id}` | 吊销 API key |

### Webhooks

| 方法 | 路径 | 描述 |
|---|---|---|
| POST | `/admin/webhooks` | 创建 webhook endpoint |
| GET | `/admin/webhooks` | 列出 webhook endpoints |
| DELETE | `/admin/webhooks/{id}` | 删除 webhook endpoint |

---

## 明确未实现的端点

以下端点目前未在 router 中挂载；如果客户端调用，应得到 404（不要返回空数组 stub）：

- `/v1/models/list`
- `/v1/model_cards*`
- `/v1/sessions/{id}/threads*`
- `POST /v1/skills/upload`
- `/admin/me`
- `/admin/users*`
- `GET /admin/api_keys/{id}`
- `GET /admin/webhooks/{id}`
- `POST /admin/webhooks/{id}`
- `/admin/tenants*`
- `/admin/integrations*`
- `/api/auth/oidc/*`
- `/debug/pprof/*`
