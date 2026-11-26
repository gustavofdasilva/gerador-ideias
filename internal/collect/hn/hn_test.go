package hn

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gerador-ideias/internal/collect"
	"gerador-ideias/internal/db"
	"gerador-ideias/internal/httpx"
	"gerador-ideias/internal/process"
)

// loadFixture lê uma resposta gravada da API real (autores anonimizados).
func loadFixture(t *testing.T, name string) searchResponse {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var r searchResponse
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestToItemFromRecordedResponses(t *testing.T) {
	page := loadFixture(t, "search_page.json")
	if len(page.Hits) == 0 {
		t.Fatal("fixture vazia")
	}
	hash := func(a string) string { return "h(" + a + ")" }
	for _, h := range page.Hits {
		it, ok := toItem(h, hash)
		if !ok {
			t.Errorf("hit %s descartado", h.ObjectID)
			continue
		}
		if it.URL != "https://news.ycombinator.com/item?id="+h.ObjectID {
			t.Errorf("URL = %s", it.URL)
		}
		if it.AuthorHash != "h("+h.Author+")" {
			t.Errorf("autor não passou pelo hash: %q", it.AuthorHash)
		}
		if strings.Contains(it.Text, "<p>") || strings.Contains(it.Text, "&#x") || strings.Contains(it.Text, "&quot;") {
			t.Errorf("HTML sobrou no texto de %s: %q", h.ObjectID, it.Text)
		}
		if !strings.Contains(strings.ToLower(it.Text), "alternative to") {
			t.Errorf("texto de %s não contém a frase buscada: %q", h.ObjectID, it.Text)
		}
		if it.CreatedAt.Unix() != h.CreatedAtI || it.CreatedAt.Location() != time.UTC {
			t.Errorf("created_at = %v", it.CreatedAt)
		}
	}

	stories := loadFixture(t, "search_stories.json")
	st := stories.Hits[0]
	it, _ := toItem(st, nil)
	if !strings.HasPrefix(it.Text, st.Title) {
		t.Errorf("story deveria começar pelo título: %q", it.Text)
	}
	if want := *st.Points + *st.NumComments; it.Engagement != want {
		t.Errorf("engajamento da story = %d, want %d", it.Engagement, want)
	}
}

func TestHTMLToText(t *testing.T) {
	in := `I&#x27;d pay for this.<p>See <a href="https:&#x2F;&#x2F;example.de&#x2F;a?b=1&amp;c=2" rel="nofollow">https:&#x2F;&#x2F;example.de&#x2F;a?b=1...</a> and <i>this</i>.`
	want := "I'd pay for this.\n\nSee https://example.de/a?b=1&c=2 and this."
	if got := htmlToText(in); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestCommentEngagementUsesReplies(t *testing.T) {
	h := hit{ObjectID: "1", CreatedAtI: 1, CommentText: "x", Children: []int64{2, 3}, Tags: []string{"comment"}}
	it, ok := toItem(h, nil)
	if !ok || it.Engagement != 2 {
		t.Errorf("engajamento = %d", it.Engagement)
	}
	if _, ok := toItem(hit{ObjectID: "2", CreatedAtI: 1, Tags: []string{"comment"}}, nil); ok {
		t.Error("comment sem texto (apagado) deveria ser descartado")
	}
}

// fakeAlgolia imita o search_by_date: filtro por created_at_i, ordem do mais
// novo para o mais antigo, paginação e o teto de resultados por consulta.
type fakeAlgolia struct {
	t     *testing.T
	limit int // paginationLimitedTo (1.000 no HN real)

	mu       sync.Mutex
	byQuery  map[string][]hit // chave: frase sem aspas
	requests int
}

var reFilter = regexp.MustCompile(`^created_at_i>(\d+),created_at_i<=(\d+)$`)

func (f *fakeAlgolia) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	q := r.URL.Query()
	if r.URL.Path != "/search_by_date" {
		http.NotFound(w, r)
		return
	}
	if ua := r.Header.Get("User-Agent"); ua != httpx.UserAgent {
		f.t.Errorf("User-Agent = %q", ua)
	}
	if q.Get("advancedSyntax") != "true" || q.Get("tags") != "(story,comment)" {
		f.t.Errorf("parâmetros inesperados: %s", r.URL.RawQuery)
	}
	query := q.Get("query")
	if len(query) < 2 || query[0] != '"' || query[len(query)-1] != '"' {
		f.t.Errorf("frase não veio entre aspas: %q", query)
	}
	m := reFilter.FindStringSubmatch(q.Get("numericFilters"))
	if m == nil {
		f.t.Errorf("numericFilters = %q", q.Get("numericFilters"))
		http.Error(w, "bad filter", 400)
		return
	}
	since, _ := strconv.ParseInt(m[1], 10, 64)
	upper, _ := strconv.ParseInt(m[2], 10, 64)
	hpp, _ := strconv.Atoi(q.Get("hitsPerPage"))
	page, _ := strconv.Atoi(q.Get("page"))

	var match []hit
	for _, h := range f.byQuery[strings.Trim(query, `"`)] {
		if h.CreatedAtI > since && h.CreatedAtI <= upper {
			match = append(match, h)
		}
	}
	sort.SliceStable(match, func(i, j int) bool { return match[i].CreatedAtI > match[j].CreatedAtI })
	reachable := min(len(match), f.limit)
	resp := searchResponse{
		NbHits:  len(match),
		NbPages: int(math.Ceil(float64(reachable) / float64(hpp))),
		Page:    page,
		Hits:    []hit{},
	}
	if start := page * hpp; start < reachable {
		resp.Hits = match[start:min(start+hpp, reachable)]
	}
	json.NewEncoder(w).Encode(resp)
}

func (f *fakeAlgolia) add(phrase string, hits ...hit) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byQuery[phrase] = append(f.byQuery[phrase], hits...)
}

func synth(id int, at time.Time) hit {
	return hit{
		ObjectID:    fmt.Sprintf("s%d", id),
		CreatedAtI:  at.Unix(),
		Author:      fmt.Sprintf("autor%d", id%7),
		CommentText: fmt.Sprintf("is there an app for thing %d?", id),
		Tags:        []string{"comment"},
	}
}

func newServer(t *testing.T, limit int) (*fakeAlgolia, *Collector) {
	t.Helper()
	fa := &fakeAlgolia{t: t, limit: limit, byQuery: map[string][]hit{}}
	srv := httptest.NewServer(fa)
	t.Cleanup(srv.Close)
	c := New(nil)
	c.BaseURL = srv.URL
	c.HTTP.MinInterval = 0
	c.HTTP.Sleep = func(ctx context.Context, d time.Duration) error { return ctx.Err() }
	return fa, c
}

func collectAll(t *testing.T, c *Collector, req collect.Request) map[string]int {
	t.Helper()
	got := map[string]int{}
	err := c.Collect(context.Background(), req, func(items []collect.Item) error {
		for _, it := range items {
			got[it.ExternalID]++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

var base = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

func TestPaginationAndWindowShift(t *testing.T) {
	fa, c := newServer(t, 10) // teto de 10 resultados por consulta
	c.HitsPerPage = 4
	c.Phrases = []string{"is there an app"}

	var hits []hit
	for i := 0; i < 37; i++ {
		// Vários itens no mesmo segundo, para testar empates no limite da janela.
		hits = append(hits, synth(i, base.Add(-time.Duration(i/3)*time.Minute)))
	}
	old := synth(999, base.Add(-48*time.Hour)) // fora da janela
	fa.add("is there an app", append(hits, old)...)

	got := collectAll(t, c, collect.Request{Since: base.Add(-24 * time.Hour), Until: base})
	if len(got) != 37 {
		t.Fatalf("recebeu %d itens distintos, want 37", len(got))
	}
	if _, ok := got["s999"]; ok {
		t.Error("item fora da janela foi entregue")
	}
	if fa.requests < 10 {
		t.Errorf("só %d requisições: a paginação com teto não foi exercitada", fa.requests)
	}
}

func TestAllSecondsEqualDoesNotLoop(t *testing.T) {
	fa, c := newServer(t, 5)
	c.HitsPerPage = 5
	c.Phrases = []string{"is there an app"}
	for i := 0; i < 12; i++ {
		fa.add("is there an app", synth(i, base.Add(-time.Hour)))
	}
	done := make(chan map[string]int, 1)
	go func() { done <- collectAll(t, c, collect.Request{Since: base.Add(-24 * time.Hour), Until: base}) }()
	select {
	case got := <-done:
		// Mais de 5 itens no mesmo segundo não cabem no teto: perder alguns é
		// aceitável, travar não.
		if len(got) == 0 {
			t.Error("nada coletado")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("laço infinito com vários itens no mesmo segundo")
	}
}

func TestOnePhraseFailingDoesNotStopOthers(t *testing.T) {
	fa, c := newServer(t, 1000)
	c.HTTP.MaxRetries = 1
	c.Phrases = []string{"quebra", "is there an app"}
	fa.add("is there an app", synth(1, base.Add(-time.Hour)))
	fa.add("quebra", hit{ObjectID: "x"}) // ok, mas o servidor abaixo falha nessa frase

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Query().Get("query"), "quebra") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fa.ServeHTTP(w, r)
	}))
	defer srv.Close()
	c.BaseURL = srv.URL

	got := map[string]int{}
	err := c.Collect(context.Background(), collect.Request{Since: base.Add(-24 * time.Hour), Until: base},
		func(items []collect.Item) error {
			for _, it := range items {
				got[it.ExternalID]++
			}
			return nil
		})
	if err == nil || !strings.Contains(err.Error(), `frase "quebra"`) {
		t.Fatalf("err = %v", err)
	}
	if got["s1"] != 1 {
		t.Errorf("a outra frase não foi coletada: %v", got)
	}
}

// --- Fim a fim: coletor + Runner + SQLite --------------------------------

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func newRunner(t *testing.T, clk *clock) (*collect.Runner, func(string) int) {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "ideias.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if _, err := db.Migrate(context.Background(), conn); err != nil {
		t.Fatal(err)
	}
	count := func(q string) int {
		var n int
		if err := conn.QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	return &collect.Runner{
		DB: conn, Now: clk.Now, Processor: process.Default(), MinTermCount: 1,
		InitialLookback: 7 * 24 * time.Hour,
		Overlap:         time.Hour,
	}, count
}

func TestEndToEndNoDuplicatesAndResume(t *testing.T) {
	ctx := context.Background()
	fa, c := newServer(t, 1000)
	c.HitsPerPage = 3
	c.Phrases = []string{"is there an app", "alternative to"}

	// Dados reais gravados, reposicionados nos últimos dias, mais sintéticos.
	rec := loadFixture(t, "search_page.json").Hits
	for i, h := range rec {
		h.CreatedAtI = base.Add(-time.Duration(i+1) * 3 * time.Hour).Unix() // até 60 h antes: cabe nos 7 dias mesmo 3 dias depois
		fa.add("alternative to", h)
	}
	for i := 0; i < 10; i++ {
		fa.add("is there an app", synth(i, base.Add(-time.Duration(i+1)*3*time.Hour)))
	}
	// O mesmo item casando duas frases deve virar uma linha só.
	fa.add("alternative to", synth(0, base.Add(-3*time.Hour)))
	total := len(rec) + 10

	clk := &clock{now: base}
	runner, count := newRunner(t, clk)

	// 1ª execução, sem cursor: busca os últimos 7 dias.
	res, err := runner.Run(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if res.FromCursor || res.New != total || !res.CursorSaved {
		t.Fatalf("1ª execução: %+v", res)
	}
	// Só candidatos guardam texto; todos os itens entram em daily_items.
	cands := res.Candidates
	if cands < 10 || cands > total { // os 10 sintéticos são pedidos explícitos; os reais dependem de citar marca
		t.Errorf("candidatos = %d de %d: o filtro parece errado", cands, total)
	}
	if n := count(`SELECT count(*) FROM items WHERE source = 'hn'`); n != cands {
		t.Fatalf("items = %d, want %d candidatos", n, cands)
	}
	if n := count(`SELECT sum(items) FROM daily_items WHERE source = 'hn'`); n != total {
		t.Fatalf("daily_items = %d, want %d", n, total)
	}
	if n := count(`SELECT count(*) FROM items WHERE author_hash = '' OR author_hash LIKE 'autor%'`); n != 0 {
		t.Errorf("%d itens sem hash ou com o autor em claro", n)
	}

	// 2ª execução logo depois: nada novo, nada duplicado.
	clk.now = base.Add(10 * time.Minute)
	res, err = runner.Run(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if !res.FromCursor || res.New != 0 {
		t.Fatalf("2ª execução: %+v", res)
	}
	if n := count(`SELECT count(*) FROM items`); n != cands {
		t.Fatalf("duplicou: items = %d", n)
	}
	if n := count(`SELECT sum(items) FROM daily_items`); n != total {
		t.Fatalf("contou de novo: daily_items = %d", n)
	}

	// Celular desligado 3 dias: a próxima execução recupera o buraco.
	gap := []hit{
		synth(100, base.Add(30*time.Minute)),
		synth(101, base.Add(26*time.Hour)),
		synth(102, base.Add(70*time.Hour)),
	}
	fa.add("is there an app", gap...)
	clk.now = base.Add(72 * time.Hour)
	res, err = runner.Run(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if res.New != len(gap) {
		t.Fatalf("buraco de 3 dias: %+v", res)
	}

	// Apagar o cursor sem perder dados: a nova execução busca o período de
	// novo, mas não conta nada duas vezes.
	if err := db.DeleteCursor(ctx, runner.DB, Source); err != nil {
		t.Fatal(err)
	}
	res, err = runner.Run(ctx, c)
	if err != nil || res.FromCursor || res.Seen == 0 {
		t.Fatalf("sem cursor: %+v %v", res, err)
	}
	if n := count(`SELECT sum(items) FROM daily_items`); n != total+len(gap) {
		t.Fatalf("contou de novo: daily_items = %d, want %d", n, total+len(gap))
	}

	// Apagar o cursor e perder os dados: rodar de novo recupera o período.
	if err := db.DeleteCursor(ctx, runner.DB, Source); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"items", "seen", "daily_items", "daily_terms", "daily_brands"} {
		if _, err := runner.DB.Exec(`DELETE FROM ` + tbl); err != nil {
			t.Fatal(err)
		}
	}
	res, err = runner.Run(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if res.FromCursor || res.New != total+len(gap) {
		t.Fatalf("sem cursor: %+v (want %d novos)", res, total+len(gap))
	}

	if n := count(`SELECT sum(items) FROM daily_items`); n != total+len(gap) {
		t.Errorf("período não recuperado: daily_items = %d", n)
	}
	if n := count(`SELECT count(*) FROM runs WHERE command = 'collect' AND source = 'hn' AND status = 'ok' AND finished_at IS NOT NULL`); n != 5 {
		t.Errorf("runs ok = %d, want 5", n)
	}
}

func TestEndToEndFailureKeepsCursor(t *testing.T) {
	ctx := context.Background()
	fa, c := newServer(t, 1000)
	c.HTTP.MaxRetries = 0
	c.Phrases = []string{"is there an app"}
	fa.add("is there an app", synth(1, base.Add(-time.Hour)))

	clk := &clock{now: base}
	runner, count := newRunner(t, clk)
	if _, err := runner.Run(ctx, c); err != nil {
		t.Fatal(err)
	}
	cur1, _, _ := db.GetCursor(ctx, runner.DB, Source)

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer down.Close()
	c.BaseURL = down.URL
	clk.now = base.Add(24 * time.Hour)
	res, err := runner.Run(ctx, c)
	if err == nil || res.Status != "error" || res.CursorSaved {
		t.Fatalf("esperava falha: %+v err=%v", res, err)
	}
	cur2, _, _ := db.GetCursor(ctx, runner.DB, Source)
	if !cur2.Equal(cur1) {
		t.Errorf("cursor avançou numa execução com erro: %v → %v", cur1, cur2)
	}
	if n := count(`SELECT count(*) FROM runs WHERE status = 'error' AND errors = 1 AND error_text LIKE '%HTTP 503%'`); n != 1 {
		t.Errorf("execução com erro não registrada em runs")
	}
}
