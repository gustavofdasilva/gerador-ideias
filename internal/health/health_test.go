package health

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gerador-ideias/internal/db"
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

func run(t *testing.T, conn *sql.DB, src string, at time.Time, status, text string) {
	t.Helper()
	if _, err := conn.Exec(`INSERT INTO runs (command, source, started_at, finished_at, status, error_text)
		VALUES ('collect', ?, ?, ?, ?, ?)`, src, at.Unix(), at.Unix()+30, status, text); err != nil {
		t.Fatal(err)
	}
}

var now = time.Date(2026, 11, 26, 12, 0, 0, 0, time.Local)

func opts() Options {
	return Options{Sources: []string{"hn", "bsky", "gh"}, FailingDays: 2, StaleHours: 48, Now: now}
}

func keys(as []Alert) string {
	var k []string
	for _, a := range as {
		k = append(k, a.Key)
	}
	return strings.Join(k, ",")
}

func TestEvaluate(t *testing.T) {
	ctx := context.Background()
	conn := openDB(t)

	// Banco novo, sem coleta nenhuma: ainda não começou, sem alerta.
	if as, _ := Evaluate(ctx, conn, opts()); len(as) != 0 {
		t.Errorf("banco novo: %v", as)
	}

	day := func(d int) time.Time { return now.AddDate(0, 0, -d).Add(-8 * time.Hour) }
	// HN ok todo dia. Bluesky falhou ontem e anteontem (duas vezes anteontem).
	// GitHub falhou ontem mas teve partial anteontem.
	for d := 0; d <= 3; d++ {
		run(t, conn, "hn", day(d), "ok", "")
	}
	run(t, conn, "bsky", day(3), "ok", "")
	run(t, conn, "bsky", day(2), "error", "HTTP 500")
	run(t, conn, "bsky", day(2), "error", "HTTP 500")
	run(t, conn, "bsky", day(1), "error", "login no Bluesky recusado")
	run(t, conn, "gh", day(2), "partial", "frase x: timeout")
	run(t, conn, "gh", day(1), "error", "HTTP 503")

	as, err := Evaluate(ctx, conn, opts())
	if err != nil {
		t.Fatal(err)
	}
	if keys(as) != "failing:bsky" {
		t.Fatalf("alertas = %v", as)
	}
	if !strings.Contains(as[0].Message, "Bluesky falhou em todas as coletas dos últimos 2 dias") ||
		!strings.Contains(as[0].Message, "login no Bluesky recusado") {
		t.Errorf("mensagem: %s", as[0].Message)
	}

	// Três dias depois, sem coleta nenhuma: alerta de coleta parada.
	o := opts()
	o.Now = now.Add(72 * time.Hour)
	as, _ = Evaluate(ctx, conn, o)
	if keys(as) != "stale,failing:bsky" || !strings.Contains(as[0].Message, "Nenhuma coleta há") {
		t.Errorf("parado: %v", as)
	}

	// Fonte fora de collect.sources não é vigiada.
	o.Sources = []string{"hn"}
	if as, _ := Evaluate(ctx, conn, o); keys(as) != "stale" {
		t.Errorf("só hn: %v", as)
	}
}

func TestNotifyDedupAndResolve(t *testing.T) {
	ctx := context.Background()
	conn := openDB(t)
	var msgs []string
	send := func(_ context.Context, s string) error { msgs = append(msgs, s); return nil }
	alerts := []Alert{{Key: "failing:bsky", Message: "Bluesky falhou"}, {Key: "stale", Message: "Nenhuma coleta há 50 h"}}

	n, err := Notify(ctx, conn, alerts, send, now, 24*time.Hour)
	if err != nil || n != 1 || !strings.Contains(msgs[0], "Bluesky falhou") || !strings.Contains(msgs[0], "Nenhuma coleta") {
		t.Fatalf("1º aviso: n=%d err=%v msgs=%v", n, err, msgs)
	}
	// 6 h depois, o mesmo problema: não repete.
	if n, _ := Notify(ctx, conn, alerts, send, now.Add(6*time.Hour), 24*time.Hour); n != 0 {
		t.Errorf("repetiu antes de 24 h")
	}
	// 25 h depois: repete só o que continua; o "stale" resolveu.
	n, _ = Notify(ctx, conn, alerts[:1], send, now.Add(25*time.Hour), 24*time.Hour)
	if n != 2 || !strings.Contains(msgs[1], "Bluesky falhou") || strings.Contains(msgs[1], "Nenhuma coleta") ||
		!strings.Contains(msgs[2], "resolvido") || !strings.Contains(msgs[2], "Nenhuma coleta há 50 h") {
		t.Errorf("n=%d msgs=%q", n, msgs)
	}
	// Tudo resolvido.
	Notify(ctx, conn, nil, send, now.Add(26*time.Hour), 24*time.Hour)
	if last := msgs[len(msgs)-1]; !strings.Contains(last, "resolvido") || !strings.Contains(last, "Bluesky") {
		t.Errorf("resolução final: %q", last)
	}
	var left int
	conn.QueryRow(`SELECT count(*) FROM meta WHERE key LIKE 'alert:%'`).Scan(&left)
	if left != 0 {
		t.Errorf("%d alertas ainda registrados", left)
	}
}
