package calibrate

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"gerador-ideias/internal/db"
	"gerador-ideias/internal/process"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if _, err := db.Migrate(context.Background(), conn); err != nil {
		t.Fatal(err)
	}
	return conn
}

func TestBuild(t *testing.T) {
	conn := openDB(t)
	n := 0
	add := func(wk, src, text string, score float64, verdict any) {
		n++
		if _, err := conn.Exec(`INSERT INTO items (source, external_id, url, created_at, week, collected_at, text, pain_score, ai_verdict)
			VALUES (?, ?, 'u', 1, ?, 1, ?, ?, ?)`, src, fmt.Sprint(n), wk, text, score, verdict); err != nil {
			t.Fatal(err)
		}
	}
	// "willing to pay" no Bluesky: 12 julgados, só 1 dor.
	for i := 0; i < 12; i++ {
		v := 0
		if i == 0 {
			v = 1
		}
		add("2026-48", "bsky", "honestly people are willing to pay anything for tickets", 2.0, v)
	}
	// "is there an app" no GitHub: 10 julgados, 8 dores.
	for i := 0; i < 10; i++ {
		v := 1
		if i >= 8 {
			v = 0
		}
		add("2026-48", "gh", "Is there an app that exports invoices to CSV?", 4.5, v)
	}
	// Não julgado (além de max_items) e de outra semana: fora da conta.
	add("2026-48", "bsky", "willing to pay", 1.0, nil)
	add("2026-46", "gh", "is there an app", 1.0, 1)

	weeks, err := Weeks("2026-48", 2)
	if err != nil || strings.Join(weeks, ",") != "2026-47,2026-48" {
		t.Fatalf("weeks = %v %v", weeks, err)
	}
	res, err := Build(context.Background(), conn, process.Default(), weeks)
	if err != nil {
		t.Fatal(err)
	}
	if res.Judged != 22 || res.Pains != 9 {
		t.Errorf("julgados=%d dores=%d", res.Judged, res.Pains)
	}
	if len(res.BySource) != 2 || res.BySource[0].Key != "bsky" || res.BySource[0].Pains != 1 {
		t.Errorf("por fonte = %+v", res.BySource)
	}
	noisy, good := res.Noisy(), res.Good()
	if len(noisy) != 1 || noisy[0].Key != "bsky · willing to pay" {
		t.Errorf("ruidosas = %+v", noisy)
	}
	if len(good) == 0 || !strings.HasPrefix(good[0].Key, "gh · is there an app") {
		t.Errorf("boas = %+v", good)
	}
	txt := res.Text(20)
	for _, want := range []string{"22 candidatos julgados", "Por faixa de pain_score", "4 ou mais", "Quase só ruído", "bsky · willing to pay", "1 de 12"} {
		if !strings.Contains(txt, want) {
			t.Errorf("texto sem %q:\n%s", want, txt)
		}
	}

	empty, _ := Build(context.Background(), conn, process.Default(), []string{"2026-30"})
	if !strings.Contains(empty.Text(10), "nenhum candidato julgado") {
		t.Error("semana sem julgados deveria explicar o que fazer")
	}
}
