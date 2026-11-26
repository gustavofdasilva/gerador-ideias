package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"gerador-ideias/internal/week"
)

// TableCount é a contagem de linhas de uma tabela.
type TableCount struct {
	Table string
	Rows  int64
}

// LastRun resume a execução mais recente de uma fonte.
type LastRun struct {
	Source     string
	StartedAt  time.Time
	FinishedAt *time.Time
	Status     string
	ItemsNew   int64
	Errors     int64
}

// Stats é o retrato de saúde mostrado por `ideias stats`.
type Stats struct {
	Path          string
	FileBytes     int64 // arquivo principal
	WALBytes      int64 // arquivo -wal, se existir
	PageBytes     int64 // page_count × page_size (o que o SQLite usa de fato)
	SchemaVersion int
	Pending       int
	Tables        []TableCount
	LastRuns      []LastRun
	MonthTokens   int64
	// Itens processados e candidatos por fonte (histórico inteiro) e por fonte
	// e idioma (últimos RateDays dias), a partir de daily_items.
	BySource []SourceCount
	Rates    []SourceCount
	// Dias de posts cobertos pelo banco (de daily_items) e o crescimento
	// estimado em bytes por mês (PageBytes ÷ dias × 30).
	DataDays      int
	MonthlyGrowth int64
}

// RateDays é a janela da taxa de candidatos no stats.
const RateDays = 30

// SourceCount conta itens processados e candidatos.
type SourceCount struct {
	Source     string
	Lang       string // vazio no total por fonte
	Items      int64
	Candidates int64
}

// Rate é a fração de candidatos (0 a 1).
func (s SourceCount) Rate() float64 {
	if s.Items == 0 {
		return 0
	}
	return float64(s.Candidates) / float64(s.Items)
}

// CollectStats lê o retrato do banco. now define o mês do total de tokens.
func CollectStats(ctx context.Context, db *sql.DB, path string, now time.Time) (*Stats, error) {
	s := &Stats{Path: path}
	if fi, err := os.Stat(path); err == nil {
		s.FileBytes = fi.Size()
	}
	if fi, err := os.Stat(path + "-wal"); err == nil {
		s.WALBytes = fi.Size()
	}

	var pageCount, pageSize int64
	if err := db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pageCount); err != nil {
		return nil, err
	}
	if err := db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize); err != nil {
		return nil, err
	}
	s.PageBytes = pageCount * pageSize

	applied, err := AppliedVersions(ctx, db)
	if err != nil {
		return nil, err
	}
	for v := range applied {
		s.SchemaVersion = max(s.SchemaVersion, v)
	}
	pending, err := Pending(ctx, db)
	if err != nil {
		return nil, err
	}
	s.Pending = len(pending)
	if len(applied) == 0 {
		return s, nil
	}

	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name <> 'schema_migrations'
		ORDER BY name`)
	if err != nil {
		return nil, err
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, err
		}
		tables = append(tables, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, t := range tables {
		var n int64
		// O nome vem do sqlite_master, não do usuário.
		if err := db.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*) FROM %q`, t)).Scan(&n); err != nil {
			return nil, err
		}
		s.Tables = append(s.Tables, TableCount{Table: t, Rows: n})
	}

	if s.LastRuns, err = lastRuns(ctx, db); err != nil {
		return nil, err
	}
	if s.BySource, err = countItems(ctx, db, false, ""); err != nil {
		return nil, err
	}
	since := week.Day(now.AddDate(0, 0, -RateDays))
	if s.Rates, err = countItems(ctx, db, true, since); err != nil {
		return nil, err
	}
	var first, last sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT min(day), max(day) FROM daily_items`).Scan(&first, &last); err != nil {
		return nil, err
	}
	if first.Valid && last.Valid {
		f, _ := time.Parse("2006-01-02", first.String)
		l, _ := time.Parse("2006-01-02", last.String)
		s.DataDays = int(l.Sub(f).Hours()/24) + 1
		// Desconta o esquema vazio: cada tabela e índice ocupa ao menos uma página.
		var btrees int64
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE rootpage > 0`).Scan(&btrees); err != nil {
			return nil, err
		}
		data := max(s.PageBytes-btrees*pageSize, 0)
		s.MonthlyGrowth = data * 30 / int64(s.DataDays)
	}

	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	if err := db.QueryRowContext(ctx,
		`SELECT coalesce(sum(total_tokens), 0) FROM ai_usage WHERE created_at >= ?`,
		monthStart.Unix()).Scan(&s.MonthTokens); err != nil {
		return nil, err
	}
	return s, nil
}

// countItems soma daily_items por fonte (e idioma, se byLang) a partir do dia since.
func countItems(ctx context.Context, db *sql.DB, byLang bool, since string) ([]SourceCount, error) {
	lang := "''"
	group := "source"
	if byLang {
		lang, group = "lang", "source, lang"
	}
	// lang e group são constantes do código.
	rows, err := db.QueryContext(ctx, `SELECT source, `+lang+`, sum(items), sum(candidates)
		FROM daily_items WHERE day >= ? GROUP BY `+group+` ORDER BY `+group, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SourceCount
	for rows.Next() {
		var sc SourceCount
		if err := rows.Scan(&sc.Source, &sc.Lang, &sc.Items, &sc.Candidates); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

func lastRuns(ctx context.Context, db *sql.DB) ([]LastRun, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT r.source, r.started_at, r.finished_at, r.status, r.items_new, r.errors
		FROM runs r
		WHERE r.command = 'collect' AND r.id = (
			SELECT id FROM runs WHERE command = 'collect' AND source = r.source
			ORDER BY started_at DESC, id DESC LIMIT 1)
		ORDER BY r.source`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LastRun
	for rows.Next() {
		var lr LastRun
		var started int64
		var finished sql.NullInt64
		if err := rows.Scan(&lr.Source, &started, &finished, &lr.Status, &lr.ItemsNew, &lr.Errors); err != nil {
			return nil, err
		}
		lr.StartedAt = time.Unix(started, 0)
		if finished.Valid {
			t := time.Unix(finished.Int64, 0)
			lr.FinishedAt = &t
		}
		out = append(out, lr)
	}
	return out, rows.Err()
}
