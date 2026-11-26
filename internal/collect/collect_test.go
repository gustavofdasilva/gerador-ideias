package collect

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"gerador-ideias/internal/db"
)

type fakeCollector struct {
	items []Item
	err   error
	reqs  []Request
}

func (f *fakeCollector) Source() string { return "fake" }

func (f *fakeCollector) Collect(ctx context.Context, req Request, emit func([]Item) error) error {
	f.reqs = append(f.reqs, req)
	var batch []Item
	for _, it := range f.items {
		if it.CreatedAt.After(req.Since) && !it.CreatedAt.After(req.Until) {
			it.AuthorHash = req.HashAuthor("fulano")
			batch = append(batch, it)
		}
	}
	// Entrega duas vezes: o Runner tem de descartar a repetição.
	if err := emit(batch); err != nil {
		return err
	}
	if err := emit(batch); err != nil {
		return err
	}
	return f.err
}

func newRunner(t *testing.T, now *time.Time) *Runner {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if _, err := db.Migrate(context.Background(), conn); err != nil {
		t.Fatal(err)
	}
	return &Runner{DB: conn, Now: func() time.Time { return *now }, InitialLookback: 48 * time.Hour, Overlap: time.Hour}
}

func TestRunnerWindowsAndStatus(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	r := newRunner(t, &now)
	fc := &fakeCollector{items: []Item{
		{ExternalID: "1", URL: "u", CreatedAt: now.Add(-time.Hour), Text: "a"},
		{ExternalID: "2", URL: "u", CreatedAt: now.Add(-72 * time.Hour), Text: "velho"},
	}}

	res, err := r.Run(ctx, fc)
	if err != nil {
		t.Fatal(err)
	}
	if got := fc.reqs[0].Since; !got.Equal(now.Add(-48 * time.Hour)) {
		t.Errorf("sem cursor, Since = %v", got)
	}
	if res.Seen != 1 || res.New != 1 || res.Status != "ok" {
		t.Errorf("1ª: %+v", res)
	}

	// Com cursor: a janela começa 1 h antes dele (sobreposição).
	now = now.Add(6 * time.Hour)
	fc.err = errors.Join(errors.New("frase a"), errors.New("frase b"))
	fc.items = append(fc.items, Item{ExternalID: "3", URL: "u", CreatedAt: now.Add(-time.Minute), Text: "novo"})
	res, err = r.Run(ctx, fc)
	if err == nil {
		t.Fatal("esperava erro")
	}
	if got := fc.reqs[1].Since; !got.Equal(now.Add(-7 * time.Hour)) {
		t.Errorf("com cursor, Since = %v", got)
	}
	if res.Status != "partial" || res.Errors != 2 || res.New != 1 || res.CursorSaved {
		t.Errorf("2ª: %+v", res)
	}

	// Como a 2ª falhou, a 3ª repete a mesma origem de janela.
	fc.err = nil
	now = now.Add(time.Hour)
	if _, err := r.Run(ctx, fc); err != nil {
		t.Fatal(err)
	}
	if !fc.reqs[2].Since.Equal(fc.reqs[1].Since) {
		t.Errorf("cursor avançou depois de falha: %v vs %v", fc.reqs[2].Since, fc.reqs[1].Since)
	}

	var rows, hashed int
	r.DB.QueryRow(`SELECT count(*), count(NULLIF(author_hash, '')) FROM items`).Scan(&rows, &hashed)
	if rows != 2 || hashed != 2 {
		t.Errorf("items=%d com hash=%d", rows, hashed)
	}
	var statuses string
	r.DB.QueryRow(`SELECT group_concat(status, ',') FROM (SELECT status FROM runs ORDER BY id)`).Scan(&statuses)
	if statuses != "ok,partial,ok" {
		t.Errorf("runs = %s", statuses)
	}
}

func TestAuthorHashStableAndKeyed(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	r := newRunner(t, &now)
	h1, err := db.AuthorHasher(ctx, r.DB)
	if err != nil {
		t.Fatal(err)
	}
	h2, _ := db.AuthorHasher(ctx, r.DB)
	if h1("hn", "pg") != h2("hn", "pg") {
		t.Error("hash muda entre chamadas")
	}
	if h1("hn", "pg") == h1("bsky", "pg") {
		t.Error("hash deveria depender da fonte")
	}
	if len(h1("hn", "pg")) != 16 || h1("hn", "") != "" {
		t.Errorf("formato: %q", h1("hn", "pg"))
	}

	other := newRunner(t, &now)
	h3, _ := db.AuthorHasher(ctx, other.DB)
	if h1("hn", "pg") == h3("hn", "pg") {
		t.Error("bancos diferentes deveriam ter chaves diferentes")
	}
}

func TestTruncate(t *testing.T) {
	if got := db.Truncate("ação", 2); got != "aç" {
		t.Errorf("Truncate = %q", got)
	}
	if got := db.Truncate("abc", 5); got != "abc" {
		t.Errorf("Truncate = %q", got)
	}
}
