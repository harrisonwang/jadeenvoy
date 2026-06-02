package store

import (
	"strings"
	"testing"
)

func TestPostgresBind(t *testing.T) {
	got := postgresBind(`SELECT ? AS a, '?' AS literal, ? AS b, 'it''s ?' AS quoted`)
	want := `SELECT $1 AS a, '?' AS literal, $2 AS b, 'it''s ?' AS quoted`
	if got != want {
		t.Fatalf("bind mismatch:\nwant %s\n got %s", want, got)
	}
}

func TestPostgresSchemaFromSQLite(t *testing.T) {
	schema := postgresSchema(sqliteSchema)
	for _, forbidden := range []string{"json_valid", "BLOB", " INTEGER"} {
		if strings.Contains(schema, forbidden) {
			t.Fatalf("postgres schema still contains %q", forbidden)
		}
	}
	for _, required := range []string{
		"JSONB NOT NULL",
		"DEFAULT '[]'::jsonb",
		"DEFAULT '{}'::jsonb",
		"BYTEA NOT NULL",
		"created_at      BIGINT NOT NULL",
		"WHERE archived_at IS NULL",
	} {
		if !strings.Contains(schema, required) {
			t.Fatalf("postgres schema missing %q", required)
		}
	}
}

func TestDialectSQL(t *testing.T) {
	if d, err := dialectForDriver("sqlite"); err != nil || d.Name() != "sqlite" {
		t.Fatalf("sqlite dialect lookup failed: %v, %#v", err, d)
	}
	if d, err := dialectForDriver("postgres"); err != nil || d.Name() != "postgres" {
		t.Fatalf("postgres dialect lookup failed: %v, %#v", err, d)
	}
	if _, err := dialectForDriver("mysql"); err == nil {
		t.Fatal("unknown dialect lookup should fail")
	}
	if got := (sqliteDialect{}).InsertDefaultEnvironmentSQL(); !strings.Contains(got, "INSERT OR IGNORE") {
		t.Fatalf("sqlite default environment sql should use INSERT OR IGNORE: %s", got)
	}
	if got := (postgresDialect{}).InsertDefaultEnvironmentSQL(); !strings.Contains(got, "ON CONFLICT (id) DO NOTHING") {
		t.Fatalf("postgres default environment sql should use ON CONFLICT: %s", got)
	}
	if got := (sqliteDialect{}).SelectSessionNextSeqSQL(); strings.Contains(got, "FOR UPDATE") {
		t.Fatalf("sqlite next_seq query should not use row locks: %s", got)
	}
	if got := (postgresDialect{}).SelectSessionNextSeqSQL(); !strings.Contains(got, "FOR UPDATE") {
		t.Fatalf("postgres next_seq query should lock row: %s", got)
	}
	if got := (sqliteDialect{}).JSONTextEquals("payload", []string{"stop_reason", "type"}, "requires_action"); !strings.Contains(got, "json_extract") || !strings.Contains(got, "'requires_action'") {
		t.Fatalf("sqlite JSONTextEquals mismatch: %s", got)
	}
	if got := (postgresDialect{}).JSONTextEquals("payload", []string{"stop_reason", "type"}, "requires_action"); !strings.Contains(got, "#>> '{stop_reason,type}'") || !strings.Contains(got, "'requires_action'") {
		t.Fatalf("postgres JSONTextEquals mismatch: %s", got)
	}
}

func TestSchemaStatements(t *testing.T) {
	got := schemaStatements("CREATE TABLE t (v TEXT DEFAULT ';'); INSERT INTO t VALUES ('a'';b');")
	want := []string{"CREATE TABLE t (v TEXT DEFAULT ';')", "INSERT INTO t VALUES ('a'';b')"}
	if len(got) != len(want) {
		t.Fatalf("statement count mismatch: want %d got %d: %#v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("statement %d mismatch:\nwant %s\n got %s", i, want[i], got[i])
		}
	}
}
