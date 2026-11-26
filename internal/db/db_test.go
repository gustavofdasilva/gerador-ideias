package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func openTemp(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sub", "ideias.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, path
}

var wantTables = []string{
	"ai_usage", "cluster_weeks", "clusters", "cursors",
	"daily_brands", "daily_items", "daily_terms", "items", "meta", "runs", "seen",
}

func TestMigrationsAreWellFormed(t *testing.T) {
	ms, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) == 0 || ms[0].Version != 1 {
		t.Fatalf("migrações = %+v", ms)
	}
	for i := 1; i < len(ms); i++ {
		if ms[i].Version != ms[i-1].Version+1 {
			t.Errorf("buraco na numeração: %d depois de %d", ms[i].Version, ms[i-1].Version)
		}
	}
}

func TestMigrateFreshAndIdempotent(t *testing.T) {
	ctx := context.Background()
	db, path := openTemp(t)

	s, err := CollectStats(ctx, db, path, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if s.SchemaVersion != 0 || s.Pending == 0 || len(s.Tables) != 0 {
		t.Fatalf("banco novo: %+v", s)
	}

	applied, err := Migrate(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) == 0 {
		t.Fatal("nenhuma migração aplicada")
	}
	again, err := Migrate(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("segunda execução aplicou %d migrações", len(again))
	}

	s, err = CollectStats(ctx, db, path, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if s.Pending != 0 || s.SchemaVersion != applied[len(applied)-1].Version {
		t.Errorf("versão %d, pendentes %d", s.SchemaVersion, s.Pending)
	}
	if len(s.Tables) != len(wantTables) {
		t.Fatalf("tabelas = %+v", s.Tables)
	}
	for i, tc := range s.Tables {
		if tc.Table != wantTables[i] || tc.Rows != 0 {
			t.Errorf("tabela %d = %+v, want %s com 0 linhas", i, tc, wantTables[i])
		}
	}
	if s.FileBytes == 0 && s.WALBytes == 0 {
		t.Error("tamanho do banco zerado")
	}
}

func TestSchemaConstraints(t *testing.T) {
	ctx := context.Background()
	db, path := openTemp(t)
	if _, err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	insertItem := `INSERT INTO items (source, external_id, url, created_at, week, collected_at)
		VALUES (?, ?, 'https://x', 1, '2026-48', 1)`
	if _, err := db.Exec(insertItem, "hn", "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(insertItem, "hn", "1"); err == nil {
		t.Error("(source, external_id) duplicado foi aceito")
	}
	if _, err := db.Exec(insertItem, "bsky", "1"); err != nil {
		t.Errorf("mesmo external_id em outra fonte deveria passar: %v", err)
	}

	if _, err := db.Exec(`UPDATE items SET cluster_id = 999`); err == nil {
		t.Error("chave estrangeira desligada: cluster_id inexistente aceito")
	}

	if _, err := db.Exec(`INSERT INTO runs (command, source, started_at, status) VALUES ('collect', 'hn', 1, 'talvez')`); err == nil {
		t.Error("status inválido em runs foi aceito")
	}

	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	mustExec(t, db, `INSERT INTO runs (command, source, started_at, finished_at, status, items_new)
		VALUES ('collect', 'hn', ?, ?, 'ok', 5), ('collect', 'hn', ?, NULL, 'running', 0),
		       ('collect', 'gh', ?, ?, 'error', 0)`,
		now.Add(-48*time.Hour).Unix(), now.Add(-47*time.Hour).Unix(),
		now.Add(-time.Hour).Unix(),
		now.Add(-2*time.Hour).Unix(), now.Add(-2*time.Hour).Unix())
	mustExec(t, db, `INSERT INTO ai_usage (created_at, report_week, stage, total_tokens)
		VALUES (?, '2026-47', 'group', 1000), (?, '2026-44', 'score', 7777)`,
		now.Add(-24*time.Hour).Unix(), now.AddDate(0, -1, 0).Unix())

	s, err := CollectStats(ctx, db, path, now)
	if err != nil {
		t.Fatal(err)
	}
	if s.MonthTokens != 1000 {
		t.Errorf("tokens no mês = %d, want 1000", s.MonthTokens)
	}
	if len(s.LastRuns) != 2 || s.LastRuns[0].Source != "gh" || s.LastRuns[1].Status != "running" {
		t.Errorf("últimas execuções = %+v", s.LastRuns)
	}
	if s.LastRuns[1].FinishedAt != nil {
		t.Error("execução interrompida deveria ter FinishedAt nulo")
	}
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}
