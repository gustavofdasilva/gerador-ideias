package db

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"gerador-ideias/internal/week"
)

func TestCompact(t *testing.T) {
	ctx := context.Background()
	conn, _ := openTemp(t)
	if _, err := Migrate(ctx, conn); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 27, 6, 0, 0, 0, time.UTC)
	day1 := now.Add(-30 * time.Hour)
	day2 := now.Add(-6 * time.Hour)
	if _, err := conn.Exec(`INSERT INTO clusters (pain, first_seen_week, last_seen_week, created_at, updated_at)
		VALUES ('dor', '2026-48', '2026-48', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	ins := func(src string, at time.Time, i int, score float64, text string, cluster any) {
		t.Helper()
		if _, err := conn.Exec(`INSERT INTO items (source, external_id, url, created_at, week, collected_at, text, pain_score, cluster_id)
			VALUES (?, ?, 'u', ?, ?, 1, ?, ?, ?)`, src, fmt.Sprintf("%s-%d", src, i), at.Unix(), week.Of(at), text, score, cluster); err != nil {
			t.Fatal(err)
		}
	}
	// 10 candidatos do Bluesky em cada um de 2 dias; 3 do HN no dia 1.
	for i := 0; i < 10; i++ {
		ins("bsky", day1, i, float64(i), "curto", nil)
		ins("bsky", day2, 100+i, float64(i), strings.Repeat("x", 500), nil)
	}
	for i := 0; i < 3; i++ {
		ins("hn", day1, i, 1, "hn", nil)
	}
	// Um candidato de score baixo já agrupado pela IA: não pode sumir.
	ins("bsky", day1, 999, -5, "agrupado", 1)

	old := week.Day(now.AddDate(0, 0, -70))
	recent := week.Day(now.AddDate(0, 0, -3))
	for _, d := range []string{old, recent} {
		conn.Exec(`INSERT INTO daily_terms VALUES (?, 'bsky', 'en', 'desconhecido', 'invoice', 5)`, d)
		conn.Exec(`INSERT INTO daily_brands VALUES (?, 'bsky', 'en', 'desconhecido', 'Notion', 5)`, d)
	}

	r, err := Compact(ctx, conn, CompactOptions{MaxCandidatesPerDay: 4, MaxTextChars: 200, KeepTermWeeks: 8, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if r.Candidates != 12 || r.Texts != 4 || r.TermRows != 2 {
		t.Errorf("resultado = %+v", r)
	}
	var n int
	conn.QueryRow(`SELECT count(*) FROM items WHERE source = 'bsky'`).Scan(&n)
	if n != 9 { // 4 + 4 + o agrupado
		t.Errorf("bsky = %d", n)
	}
	var minScore float64
	conn.QueryRow(`SELECT min(pain_score) FROM items WHERE source = 'bsky' AND cluster_id IS NULL`).Scan(&minScore)
	if minScore != 6 {
		t.Errorf("ficaram candidatos de score baixo: min = %v", minScore)
	}
	conn.QueryRow(`SELECT count(*) FROM items WHERE source = 'hn'`).Scan(&n)
	if n != 3 {
		t.Errorf("hn abaixo do limite foi mexido: %d", n)
	}
	var maxLen int
	conn.QueryRow(`SELECT max(length(text)) FROM items`).Scan(&maxLen)
	if maxLen != 201 {
		t.Errorf("texto maior = %d", maxLen)
	}
	conn.QueryRow(`SELECT count(*) FROM daily_terms`).Scan(&n)
	if n != 1 {
		t.Errorf("daily_terms = %d", n)
	}

	// De novo: nada a fazer.
	r, _ = Compact(ctx, conn, CompactOptions{MaxCandidatesPerDay: 4, MaxTextChars: 200, KeepTermWeeks: 8, Now: now})
	if r.Summary() != "" {
		t.Errorf("segunda compactação mudou algo: %+v", r)
	}

	sizes, err := TableSizes(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	if sizes["items"] == 0 || sizes["daily_terms"] == 0 {
		t.Errorf("tamanhos = %v", sizes)
	}
	if _, ok := sizes["items_week_source"]; ok {
		t.Error("índices deveriam somar na tabela")
	}
}
