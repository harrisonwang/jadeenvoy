# ADR-0005: SQL 栈 — pgx + modernc/sqlite + sqlc + goose

## Status

Superseded by ADR-0028 — 2026-06-02

## Context

JadeEnvoy 需要持久化层，要求：
- 同一份业务代码跑 SQLite（dev / 小团队）和 Postgres（生产）
- 类型安全（避免运行时 SQL 错误）
- 跨平台编译简单（不依赖 CGo，方便 docker build / cross-compile）
- 迁移版本化
- 性能足够（Postgres 用上 prepared statement / connection pool）

Go 持久化层的选择：
- **ORM** —— GORM / Ent / Bun
- **Query builder** —— Squirrel / Goqu
- **SQL-first 生成** —— sqlc
- **手写 SQL + 手写扫描** —— database/sql

OMA 用 Drizzle ORM + 5 个 schema config（cf-auth、cf-router、cf-integrations、
node-pg、node-sqlite），跨抽象层各种 mapper 代码。

## Decision

**SQL-first：用 `sqlc-dev/sqlc` 生成查询函数，drivers 选 pure Go 实现。**

栈：
- **Postgres 驱动**: `jackc/pgx/v5`（最快、原生 LISTEN/NOTIFY、最现代）
- **SQLite 驱动**: `modernc.org/sqlite`（pure Go，无 CGo）
- **代码生成**: `sqlc-dev/sqlc` 从 SQL 文件生成 Go 类型 + 查询函数
- **迁移**: `pressly/goose`，SQL-first，CLI 友好

```sql
-- queries/agent.sql
-- name: CreateAgent :one
INSERT INTO agent (id, tenant_id, name, ...)
VALUES ($1, $2, $3, ...)
RETURNING *;

-- name: GetAgent :one
SELECT * FROM agent WHERE id = $1 AND tenant_id = $2;
```

```go
// sqlc 生成
func (q *Queries) CreateAgent(ctx context.Context, arg CreateAgentParams) (Agent, error)
func (q *Queries) GetAgent(ctx context.Context, arg GetAgentParams) (Agent, error)
```

迁移：
```sql
-- migrations/pg/0001_init.sql
-- +goose Up
CREATE TABLE agent (...);
CREATE TABLE session (...);
-- +goose Down
DROP TABLE session;
DROP TABLE agent;
```

## Consequences

### 正面

- **零运行时 ORM 开销**——生成代码就是普通 Go 函数
- **类型安全**——sqlc 检查 SQL 语法，参数类型，返回类型
- **pure Go**——`modernc.org/sqlite` 不需要 CGo，跨平台 build 干净
- **同一份 SQL 用两个方言**——sqlc 支持 `-- engine: postgresql` / `sqlite`
- **迁移版本化**——goose 文件编号 + up/down
- **PG LISTEN/NOTIFY**——pgx 原生支持，V2 跨进程 pub-sub 可用
- **Schema 演进可控**——所有变更都在 migrations/ 里，PR review 看得清

### 负面

- **动态查询难写**——sqlc 不支持运行时拼 WHERE。复杂筛选只能写多个变体或回退到手写
- **复杂 JOIN 跨语法差异**——SQLite 不支持 PG 的某些 syntax（partial index where、jsonb 操作符），
  要在 SQL 层用 portability 子集
- **JSONB 操作**——SQLite 的 JSON 函数跟 PG 不同名，sqlc 不抹平
- **学习曲线**——团队没用过 sqlc 要先看文档（但很简单，半小时上手）

### 中性

- 跟 pgx prepared statement 集成需要一点配置
- 每加一个新查询要 `sqlc generate`，集成到 Makefile
- SQLite 单写并发限制（只能 1 个并发写），不影响 V1 dev 但要文档说明

## Alternatives considered

### GORM

- **拒因**:
  - 反射开销大
  - 错误处理糟（链式 API 错过 .Error 静默吞）
  - 复杂查询写起来比手写 SQL 还啰嗦
  - 跟 PG/SQLite 方言差异处理不彻底
- **何时考虑**: 永不。

### Ent (entgo.io)

- **拒因**:
  - 自带 schema DSL，跟 sqlc 比相当于 ORM 跟 query builder
  - 生成代码更多更"魔法"
  - 多 backend 抽象有泄漏
- **何时考虑**: 团队偏爱 schema-as-code 时。

### Bun

- **拒因**: ORM 心智，类型安全弱于 sqlc。

### `database/sql` + 手写 scan

- **拒因**: 类型安全靠运行时 reflection，每个查询要写 ~20 行 boilerplate。
  小项目可以，我们 30+ 表撑不住。
- **何时考虑**: 单表 service。

### `mattn/go-sqlite3`（CGo）

- **拒因**: 需要 CGo，跨平台编译需要交叉编译工具链。Docker multi-arch build 麻烦。
- **何时考虑**: 永不（modernc.org/sqlite 性能差异已经很小）。

### Squirrel（query builder）

- **拒因**: 跟 sqlc 是不同范式。Squirrel 是运行时拼 SQL，sqlc 是编译时生成。
  我们大部分查询是固定的，sqlc 更适合。
- **何时考虑**: 真有大量动态查询场景时。

## References

- [sqlc-dev/sqlc](https://docs.sqlc.dev/)
- [jackc/pgx](https://github.com/jackc/pgx)
- [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite)
- [pressly/goose](https://github.com/pressly/goose)
- [Why pgx over database/sql/driver](https://github.com/jackc/pgx#choosing-between-the-pgx-and-databasesql-interfaces)

## Open Questions

- 多语言 ORM（Python / TS SDK）共享 schema？V2 看需求。
- 跨 backend 测试策略——CI 跑两遍（一遍 SQLite 一遍 PG）。
