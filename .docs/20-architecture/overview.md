# 系统架构总览

## 产品定位

JadeEnvoy 的 Agent 不是一次性 prompt wrapper，而是**可编排、可托管、可观测、可审计、可恢复、可安全执行、可替换基础设施的系统组件**。

这句话对应到代码层的约束是：

- **可编排**：Agent、Session、Event、Tool、MCP、Memory、Vault 通过事件日志和 harness loop 组合，而不是把状态藏进进程内对象。
- **可托管**：`jed` 提供 `/v1/*` runtime API 与 `/admin/*` 控制面，Console/CLI/Adapter 都走同一个 HTTP 入口。
- **可观测**：append-only event log、SSE、metrics、`/admin/dashboard` 共同暴露模型调用、工具执行、状态迁移和失败面。
- **可审计**：session event、agent versions、memory versions、vault credential 生命周期都保留可追溯记录；敏感内容通过 redact/archive 而不是静默覆盖。
- **可恢复**：进程重启后从 DB/event log 重建上下文；running/rescheduling session 有启动恢复 sweep，LLM 临时错误有 reschedule。
- **可安全执行**：sandbox、tool guardrails、vault 静态凭据注入、webhook SSRF 防护、Auth/API key 共同约束执行边界。
- **可替换基础设施**：持久化层按 dialect/adapter 隔离 SQLite 与 Postgres 差异，上层业务只面向 `store.Store`。

## 组件图

```
┌────────────────────────────────────────────────────────────────────────┐
│                          JadeEnvoy 部署单元                            │
│                                                                        │
│  ┌──────────────┐    ┌────────────┐    ┌──────────────────────────┐    │
│  │ Console UI   │    │  je CLI    │    │  Webhook adapter         │    │
│  │ (React SPA)  │    │ (cobra)    │    │  (GitLab/GitHub/Slack…) │    │
│  └──────┬───────┘    └─────┬──────┘    └────────────┬─────────────┘    │
│         │                  │                        │                  │
│         │ HTTP/SSE         │ REST                   │ POST /v1/sessions/│
│         │                  │                        │ :id/events       │
│         └──────────────────┼────────────────────────┘                  │
│                            │                                           │
│              ┌─────────────▼─────────────┐                             │
│              │       jed (daemon)        │                             │
│              │   github.com/harrisonwang/   │                             │
│              │       jadeenvoy           │                             │
│              │                           │                             │
│              │  ┌───────────────────┐    │                             │
│              │  │ chi HTTP router   │    │                             │
│              │  │ /v1/* + /admin/*  │    │                             │
│              │  └───────┬───────────┘    │                             │
│              │  ┌───────▼───────────┐    │                             │
│              │  │ session orchestr. │    │                             │
│              │  │ (state machine)   │    │                             │
│              │  └───────┬───────────┘    │                             │
│              │  ┌───────▼───────────┐    │                             │
│              │  │ harness loop      │    │                             │
│              │  │ (agent + tools)   │    │                             │
│              │  └───┬───────┬───────┘    │                             │
│              │      │       │            │                             │
│              │      ▼       ▼            │                             │
│              │ ┌────────┐ ┌──────────┐   │                             │
│              │ │provider│ │ sandbox  │◄──┼──── 启动 subprocess/docker  │
│              │ │ (LLM)  │ │ orches.  │   │                             │
│              │ └───┬────┘ └────┬─────┘   │                             │
│              │     │           │         │                             │
│              │ ┌───▼─────────────────┐   │                             │
│              │ │  store (sqlc + DB)  │   │                             │
│              │ └───┬─────────────────┘   │                             │
│              └────│ ──────────────────  │                              │
│                   │                                                    │
│         ┌─────────▼──────┐    ┌──────────────────────────┐             │
│         │ SQLite or PG   │    │  je-vault (sidecar)       │             │
│         │  + R2/S3/FS    │    │  HTTPS MITM proxy         │             │
│         └────────────────┘    │  goproxy + CA inject      │             │
│                               └────────────┬─────────────┘             │
│                                            │                            │
└────────────────────────────────────────────┼────────────────────────────┘
                                             │
                                             ▼
                              Sandbox 进程出站请求
                              (HTTPS_PROXY=je-vault)
                                             │
                                             ▼
                                    外部 service / API
                                    (GitLab, MCP, LLM 网关...)
```

## 关键数据流

### 1. 创建 session + 发消息

```
client → POST /v1/sessions       → store: session row + status=idle
                                  → 不起容器（lazy provision）

client → POST /v1/sessions/:id/events { user.message }
                                  → store: event row, seq=1
                                  → broker.publish(session_id, event)
                                  → orchestrator.startTurn(session_id)
                                       → sandbox.provision(session_id)
                                       → harness.run(session_id)
                                             ├ history = store.events(session_id)
                                             ├ provider.Stream(...)
                                             ├   for chunk: store.append + broker.publish
                                             └   on tool_use:
                                                   ├ sandbox.Exec(...)
                                                   ├ store.append agent.tool_result
                                                   └ continue loop
                                       → store.session.status=idle

client → GET /v1/sessions/:id/events/stream (SSE)
                                  → broker.subscribe(session_id)
                                  → 流式推送所有事件
```

### 2. Vault 凭据注入

```
session creation:
  client → POST /v1/sessions { vault_ids: [...] }
         → store.session_vaults insert

sandbox 启动 subprocess:
  env vars:
    HTTPS_PROXY=http://127.0.0.1:14322
    HTTP_PROXY=http://127.0.0.1:14322
    SSL_CERT_FILE=/path/to/je-vault-ca.crt
    JE_SESSION_ID=sess-...        ← je-vault 用此识别 session

sandbox 里 curl https://git.talkweb.com.cn/api/v4/...:
  → 走 HTTPS_PROXY (je-vault)
  → je-vault: MITM 解密
            : 拿 JE_SESSION_ID
            : 查 store.session_vaults → vaults
            : pickCredByHost("git.talkweb.com.cn")
            : 剥离客户端 Authorization
            : 注入 "Authorization: Bearer <real-token>"
            : 重加密发出
  → GitLab API 收到带正确 token 的请求
```

### 3. 三类客户端，同一个 REST 入口

```
                 ┌──────────────┐
                 │   jed REST   │
                 │   /v1/*      │
                 └──────┬───────┘
        ┌───────────────┼────────────────┐
        │               │                │
   ┌────▼─────┐   ┌────▼────┐    ┌──────▼──────┐
   │ Console  │   │   je    │    │  Adapter    │
   │  (UI)    │   │  (CLI)  │    │ (webhook)   │
   └──────────┘   └─────────┘    └─────────────┘
   人类操作        脚本/CI         自动化触发
```

## 部署形态

| 形态 | 场景 | 复杂度 |
|---|---|---|
| **单 binary + SQLite + FS** | dev / 小团队 | ⭐ |
| **docker-compose: jed + je-vault + sqlite** | 默认 self-host | ⭐⭐ |
| **docker-compose: + postgres** | 中规模 | ⭐⭐ |
| **K8s: jed Deployment + je-vault Sidecar + PG StatefulSet + S3** | 生产 | ⭐⭐⭐⭐ |

V1 我们只交付前两种。K8s 部署到 M3 再做。

## 进程/二进制划分

| 二进制 | 角色 | 启动方式 |
|---|---|---|
| `jed` | 主守护进程，REST API + 编排 + harness | systemd / docker compose |
| `je-vault` | MITM 代理 sidecar | systemd / docker compose |
| `je` | CLI 客户端 | 用户调用 |
| `je-worker`（V3） | 自托管 worker 模式 | 可选 |

V1 `jed` + `je-vault` 必装；`je` 客户端可选。

## 关键不变量

1. **Append-only event log**：`session_events` 永不 UPDATE，只 INSERT
2. **凭据明文不出存储层**：DB 里只有 cipher，logging 永不打 token
3. **沙箱出站必走 vault 代理**：直接 outbound 视为 bug
4. **同 host 单凭据**：vault 层硬约束，防"老凭据先匹配"
5. **lazy provision**：session 创建不起容器，第一个 user message 触发
6. **stateful = 数据库 + 文件系统，无 in-memory state**：进程重启不丢

## 失败模式

| 故障 | 处理 |
|---|---|
| LLM API 临时错误 | session.status_rescheduled，自动退避重试 |
| LLM 超时 / 网络断 | 重试 N 次后 terminated，写明 error.type |
| Sandbox 命令超时 | bash tool 返回 timeout error，agent 继续 |
| Sandbox 启动失败 | session.error，可重试 |
| Vault 凭据缺失 | outbound 不注入，目标 service 返 401，agent 看到正常错误 |
| jed 进程崩溃 | 重启后从 event log 续上，所有 idle session 状态保持 |
| DB 不可达 | jed 拒绝新请求，已 running 的 session 保持内存状态，写失败重试 |
