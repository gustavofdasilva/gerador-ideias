package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"gerador-ideias/internal/week"
)

// CompactOptions são os limites de armazenamento (config [storage]).
type CompactOptions struct {
	MaxCandidatesPerDay int
	MaxTextChars        int
	KeepTermWeeks       int
	Now                 time.Time
}

// CompactResult resume o que a compactação fez.
type CompactResult struct {
	Candidates int64 // candidatos excedentes apagados
	Texts      int64 // textos encurtados
	TermRows   int64 // linhas antigas de daily_terms e daily_brands apagadas
	Vacuumed   bool
	FreedBytes int64
}

// Summary é uma linha legível, vazia se nada mudou.
func (r CompactResult) Summary() string {
	if r.Candidates == 0 && r.Texts == 0 && r.TermRows == 0 && !r.Vacuumed {
		return ""
	}
	s := fmt.Sprintf("compactação: %d candidatos excedentes, %d textos encurtados, %d contagens antigas", r.Candidates, r.Texts, r.TermRows)
	if r.Vacuumed {
		s += fmt.Sprintf(" · VACUUM liberou %d KiB", r.FreedBytes/1024)
	}
	return s
}

// Compact aplica os limites de armazenamento ao banco inteiro:
//   - mantém com texto só os MaxCandidatesPerDay candidatos de maior
//     pain_score por fonte e dia (os que a IA já agrupou ficam sempre); os
//     demais continuam contados em daily_items;
//   - encurta textos acima de MaxTextChars;
//   - apaga daily_terms e daily_brands mais antigos que KeepTermWeeks;
//   - roda VACUUM se o espaço livre passar de 8 MiB e de 25% do arquivo.
func Compact(ctx context.Context, conn *sql.DB, o CompactOptions) (CompactResult, error) {
	var r CompactResult
	res, err := conn.ExecContext(ctx, `DELETE FROM items WHERE id IN (
		SELECT id FROM (
			SELECT id, cluster_id, row_number() OVER (
				PARTITION BY source, date(created_at, 'unixepoch')
				ORDER BY pain_score DESC, engagement DESC, id) AS rn
			FROM items)
		WHERE rn > ? AND cluster_id IS NULL)`, o.MaxCandidatesPerDay)
	if err != nil {
		return r, fmt.Errorf("limitar candidatos: %w", err)
	}
	r.Candidates, _ = res.RowsAffected()

	res, err = conn.ExecContext(ctx, `UPDATE items SET text = substr(text, 1, ?) || '…' WHERE length(text) > ?`,
		o.MaxTextChars, o.MaxTextChars+1)
	if err != nil {
		return r, fmt.Errorf("encurtar textos: %w", err)
	}
	r.Texts, _ = res.RowsAffected()

	cutoff := week.Day(o.Now.AddDate(0, 0, -7*o.KeepTermWeeks))
	for _, table := range []string{"daily_terms", "daily_brands"} {
		res, err := conn.ExecContext(ctx, `DELETE FROM `+table+` WHERE day < ?`, cutoff)
		if err != nil {
			return r, fmt.Errorf("limpar %s: %w", table, err)
		}
		n, _ := res.RowsAffected()
		r.TermRows += n
	}

	var free, pages, size int64
	if err := conn.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&free); err != nil {
		return r, err
	}
	if err := conn.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pages); err != nil {
		return r, err
	}
	if err := conn.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&size); err != nil {
		return r, err
	}
	if free*size > 8<<20 && free*4 > pages {
		if _, err := conn.ExecContext(ctx, `VACUUM`); err != nil {
			return r, fmt.Errorf("VACUUM: %w", err)
		}
		r.Vacuumed, r.FreedBytes = true, free*size
	}
	return r, nil
}

// TableSizes devolve os bytes ocupados por tabela (somando os índices dela),
// pela tabela virtual dbstat do SQLite.
func TableSizes(ctx context.Context, conn *sql.DB) (map[string]int64, error) {
	rows, err := conn.QueryContext(ctx, `SELECT coalesce(m.tbl_name, d.name), sum(d.pgsize)
		FROM dbstat d LEFT JOIN sqlite_master m ON m.name = d.name
		GROUP BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var name string
		var n int64
		if err := rows.Scan(&name, &n); err != nil {
			return nil, err
		}
		out[name] = n
	}
	return out, rows.Err()
}
