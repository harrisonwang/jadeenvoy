package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type dialect interface {
	Name() string
	Schema(base string) string
	Bind(query string) string
	InsertDefaultEnvironmentSQL() string
	SelectSessionNextSeqSQL() string
	JSONBoolTrue(column, field string) string
	JSONTextEquals(column string, path []string, value string) string
}

var dialects = map[string]dialect{
	"sqlite":   sqliteDialect{},
	"postgres": postgresDialect{},
}

func dialectForDriver(driver string) (dialect, error) {
	d, ok := dialects[driver]
	if !ok {
		return nil, fmt.Errorf("unsupported store driver %s", driver)
	}
	return d, nil
}

type sqliteDialect struct{}

func (sqliteDialect) Name() string { return "sqlite" }

func (sqliteDialect) Schema(base string) string { return base }

func (sqliteDialect) Bind(query string) string { return query }

func (sqliteDialect) InsertDefaultEnvironmentSQL() string {
	return `INSERT OR IGNORE INTO environment (id, tenant_id, name, config, created_at, updated_at)
		 VALUES ('default', ?, 'default', '{"type":"cloud"}', ?, ?)`
}

func (sqliteDialect) SelectSessionNextSeqSQL() string {
	return `SELECT next_seq FROM session WHERE id = ?`
}

func (sqliteDialect) JSONBoolTrue(column, field string) string {
	return fmt.Sprintf("json_extract(%s, '$.%s') = 1", column, field)
}

func (sqliteDialect) JSONTextEquals(column string, path []string, value string) string {
	return fmt.Sprintf("json_extract(%s, '$.%s') = %s", column, strings.Join(path, "."), sqlStringLiteral(value))
}

type postgresDialect struct{}

func (postgresDialect) Name() string { return "postgres" }

func (postgresDialect) Schema(base string) string { return postgresSchema(base) }

func (postgresDialect) Bind(query string) string { return postgresBind(query) }

func (postgresDialect) InsertDefaultEnvironmentSQL() string {
	return `INSERT INTO environment (id, tenant_id, name, config, created_at, updated_at)
		 VALUES ('default', ?, 'default', '{"type":"cloud"}', ?, ?)
		 ON CONFLICT (id) DO NOTHING`
}

func (postgresDialect) SelectSessionNextSeqSQL() string {
	return `SELECT next_seq FROM session WHERE id = ? FOR UPDATE`
}

func (postgresDialect) JSONBoolTrue(column, field string) string {
	return fmt.Sprintf("(%s->>'%s')::boolean = true", column, field)
}

func (postgresDialect) JSONTextEquals(column string, path []string, value string) string {
	return fmt.Sprintf("(%s #>> '{%s}') = %s", column, strings.Join(path, ","), sqlStringLiteral(value))
}

func (s *Store) bind(query string) string {
	d, err := s.ensureDialect()
	if err != nil {
		return query
	}
	return d.Bind(query)
}

func (s *Store) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return s.DB.ExecContext(ctx, s.bind(query), args...)
}

func (s *Store) query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return s.DB.QueryContext(ctx, s.bind(query), args...)
}

func (s *Store) queryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return s.DB.QueryRowContext(ctx, s.bind(query), args...)
}

func (s *Store) txExec(ctx context.Context, tx *sql.Tx, query string, args ...any) (sql.Result, error) {
	return tx.ExecContext(ctx, s.bind(query), args...)
}

func (s *Store) txQueryRow(ctx context.Context, tx *sql.Tx, query string, args ...any) *sql.Row {
	return tx.QueryRowContext(ctx, s.bind(query), args...)
}

// QueryContext exposes dialect-aware querying for admin/reporting code that
// does not belong in a narrow store repository yet.
func (s *Store) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return s.query(ctx, query, args...)
}

// QueryRowContext exposes dialect-aware single-row querying for aggregate
// control-plane code.
func (s *Store) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return s.queryRow(ctx, query, args...)
}

func (s *Store) JSONBoolTrue(column, field string) string {
	d, err := s.ensureDialect()
	if err != nil {
		return "1 = 0"
	}
	return d.JSONBoolTrue(column, field)
}

func (s *Store) JSONTextEquals(column string, path []string, value string) string {
	d, err := s.ensureDialect()
	if err != nil {
		return "1 = 0"
	}
	return d.JSONTextEquals(column, path, value)
}

func (s *Store) ensureDialect() (dialect, error) {
	if s.dialect != nil {
		return s.dialect, nil
	}
	d, err := dialectForDriver(s.Driver)
	if err != nil {
		return nil, err
	}
	s.dialect = d
	return d, nil
}

func postgresBind(query string) string {
	var b strings.Builder
	b.Grow(len(query) + 8)
	arg := 1
	inSingle := false
	for i := 0; i < len(query); i++ {
		ch := query[i]
		if ch == '\'' {
			b.WriteByte(ch)
			if inSingle && i+1 < len(query) && query[i+1] == '\'' {
				i++
				b.WriteByte(query[i])
				continue
			}
			inSingle = !inSingle
			continue
		}
		if ch == '?' && !inSingle {
			b.WriteString(fmt.Sprintf("$%d", arg))
			arg++
			continue
		}
		b.WriteByte(ch)
	}
	return b.String()
}

func sqlStringLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func postgresSchema(sqlite string) string {
	replacer := strings.NewReplacer(
		`TEXT NOT NULL CHECK (json_valid(model))`, `JSONB NOT NULL`,
		`TEXT NOT NULL CHECK (json_valid(agent_snapshot))`, `JSONB NOT NULL`,
		`TEXT NOT NULL CHECK (json_valid(payload))`, `JSONB NOT NULL`,
		`TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(tools))`, `JSONB NOT NULL DEFAULT '[]'::jsonb`,
		`TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(mcp_servers))`, `JSONB NOT NULL DEFAULT '[]'::jsonb`,
		`TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(skills))`, `JSONB NOT NULL DEFAULT '[]'::jsonb`,
		`TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(vault_ids))`, `JSONB NOT NULL DEFAULT '[]'::jsonb`,
		`TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(event_types))`, `JSONB NOT NULL DEFAULT '[]'::jsonb`,
		`TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(files_json))`, `JSONB NOT NULL DEFAULT '[]'::jsonb`,
		`TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata))`, `JSONB NOT NULL DEFAULT '{}'::jsonb`,
		`TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(config))`, `JSONB NOT NULL DEFAULT '{}'::jsonb`,
		`BLOB`, `BYTEA`,
		` INTEGER`, ` BIGINT`,
	)
	return replacer.Replace(sqlite)
}
