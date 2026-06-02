# ADR-0028: 用 Store Dialect Adapter 支持 SQLite 与 Postgres

## Status

`Accepted`

日期: 2026-06-02

## Context

JadeEnvoy 的持久化层必须同时覆盖两个场景：

- SQLite：dev、本机演示、小团队单 binary 部署。
- Postgres：生产部署、跨进程扩展、后续更强的队列与观测能力。

ADR-0005 早期选择了 `pgx` + `modernc/sqlite` + `sqlc` + `goose`。但当前 V1/M2 代码已经形成了手写 SQL +
手写 scan 的 store 层，并且上层业务模块大量依赖 `store.Store`。如果此时直接引入两套 sqlc 生成代码，会让
Agent、Session、Vault、Memory、Webhook 等逻辑被迫分叉，维护成本比收益更高。

同时，SQLite 与 Postgres 的真实差异集中在少数点：

- 占位符：SQLite 使用 `?`，Postgres 使用 `$1` / `$2`。
- JSON：SQLite 使用 `TEXT + json_valid/json_extract`，Postgres 应使用 `JSONB` 与 JSON 操作符。
- 幂等插入：SQLite 使用 `INSERT OR IGNORE`，Postgres 使用 `ON CONFLICT DO NOTHING`。
- 并发锁：SQLite 写事务天然串行，Postgres 对 session `next_seq` 需要显式 `FOR UPDATE` 行锁。
- 二进制列：SQLite `BLOB`，Postgres `BYTEA`。

这些差异不应该泄漏到业务层。

## Decision

我们决定在 `internal/store` 引入轻量 `dialect` adapter：

```go
type dialect interface {
    Name() string
    Schema(base string) string
    Bind(query string) string
    InsertDefaultEnvironmentSQL() string
    SelectSessionNextSeqSQL() string
    JSONBoolTrue(column, field string) string
}
```

`store.Store` 保持为上层唯一入口。业务方法继续写一份 SQL，使用 `?` 作为内部占位符；执行前由 dialect
统一转换。schema 以现有 SQLite schema 为基础，Postgres 版本由集中转换函数生成：

- JSON TEXT 列转换为 `JSONB`。
- `BLOB` 转换为 `BYTEA`。
- unix 毫秒时间戳与计数列转换为 `BIGINT`。
- SQLite/PG 的默认环境插入、JSON boolean 查询和 session seq row lock 由 dialect 提供。

驱动选择：

- SQLite：继续使用 `modernc.org/sqlite`，保持 pure Go / no CGo。
- Postgres：使用 `github.com/jackc/pgx/v5/stdlib`，通过 `database/sql` 接入现有 store 代码。

迁移执行从“一次执行整段 schema”改为逐条执行静态 SQL statement，避免 Postgres prepared statement
对多语句执行的限制。

当前不立即引入 sqlc/goose 生成层。等 schema 稳定且 PG 生产验证完成后，可以开新 ADR 决定是否把 dialect
adapter 下沉为正式 migration/query 生成链路。

## Consequences

### 正面

- 上层业务只维护一份 store 方法，不需要 SQLite/PG 两套 repository。
- 新增数据库差异时只改 dialect 或少数集中 helper，维护边界清晰。
- Postgres 支持可以渐进落地，不打断已有 SQLite e2e 与本地开发体验。
- 事件 seq 在 Postgres 下通过 `FOR UPDATE` 保持同 session 内序号并发安全。
- Dashboard 等控制面聚合查询也通过 `Store.QueryContext` 复用 dialect bind 与 JSON 谓词。

### 负面

- 不是完整 migration system；schema 演进仍需要后续补版本化迁移。
- Postgres schema 目前由 SQLite schema 集中转换得到，复杂 PG-only 优化需要谨慎扩展。
- 手写 scan 仍无法提供 sqlc 级别的编译期 SQL 类型检查。

### 中性

- `JE_TEST_POSTGRES_URL` 控制可选 Postgres smoke test；没有 PG 实例时 CI/本地测试会跳过真实 PG 连接。
- `go test ./...` 默认仍以 SQLite 为确定性路径。

## Alternatives considered

### 立刻引入 sqlc + goose 双方言

这是 ADR-0005 的原始方向，但当前 store 层已覆盖较多业务行为。现在切换会把一次 PG 支持变成大规模迁移，
并推迟用户需要的功能落地。

### 为 SQLite 和 Postgres 写两套 Store 实现

短期直观，但 Agent/Session/Event/Vault/Memory/Webhook 的业务规则会重复，后续 bug fix 容易漏一边。

### 只支持 Postgres，移除 SQLite

不符合自托管优先与单 binary 本地体验。SQLite 仍是 dev、demo 和小规模部署的最低摩擦路径。

## References

- [ADR-0005: SQL 栈](0005-sql-stack.md)
- [ADR-0027: 补齐 Managed Agents 产品控制面](0027-managed-agents-product-surface.md)
- [`internal/store/dialect.go`](../../internal/store/dialect.go)

## Open Questions

- 何时引入正式 versioned migrations。
- 是否在 PG 生产验证后使用 sqlc 生成部分高风险查询。
- 是否在跨进程 broker 落地时引入 PG `LISTEN/NOTIFY`。
