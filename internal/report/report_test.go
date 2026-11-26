package report

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gerador-ideias/internal/db"
	"gerador-ideias/internal/week"
)

// Semana do relatório: 2026-48 (23 a 29/11). "Agora" é quinta, 26/11.
var now = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

type fixture struct {
	t    *testing.T
	conn *sql.DB
	path string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ideias.db")
	conn, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if _, err := db.Migrate(context.Background(), conn); err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, conn: conn, path: path}
}

func (f *fixture) exec(q string, args ...any) {
	f.t.Helper()
	if _, err := f.conn.Exec(q, args...); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) day(d time.Time, src, lang, country string, items, cands int) {
	f.exec(`INSERT INTO daily_items VALUES (?, ?, ?, ?, ?, ?)`, week.Day(d), src, lang, country, items, cands)
}

func (f *fixture) term(table string, d time.Time, src, term string, n int) {
	f.exec(`INSERT INTO `+table+` VALUES (?, ?, 'en', 'desconhecido', ?, ?)
		ON CONFLICT DO UPDATE SET count = count + excluded.count`, week.Day(d), src, term, n)
}

func (f *fixture) item(id string, at time.Time, src string, score float64, text string) {
	f.exec(`INSERT INTO items (source, external_id, url, created_at, week, collected_at, text, lang,
		country_est, pain_score, signals, brands, engagement)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'en', 'desconhecido', ?, 'payment,request', 'Notion', 7)`,
		src, id, "https://example.com/"+id, at.Unix(), week.Of(at), at.Unix(), text, score)
}

func (f *fixture) run(src string, at time.Time, status, errText string) {
	f.exec(`INSERT INTO runs (command, source, started_at, finished_at, status, error_text)
		VALUES ('collect', ?, ?, ?, ?, ?)`, src, at.Unix(), at.Unix()+60, status, errText)
}

// seed monta 4 semanas anteriores + a semana do relatório.
func seed(f *fixture) {
	weekStart := time.Date(2026, 11, 23, 6, 0, 0, 0, time.UTC)
	for w := 4; w >= 1; w-- {
		d := weekStart.AddDate(0, 0, -7*w)
		f.day(d, "hn", "en", "desconhecido", 100, 10)
		f.term("daily_terms", d, "hn", "invoice", 10)    // estável: 10 por semana
		f.term("daily_terms", d, "hn", "spreadsheet", 2) // cresce para 12
		f.term("daily_brands", d, "hn", "Notion", 5)
	}
	for i := 0; i < 4; i++ { // seg a qui
		d := weekStart.AddDate(0, 0, i)
		f.day(d, "hn", "en", "desconhecido", 50, 5)
		f.day(d, "bsky", "pt", "br", 20, 4)
		if i < 2 {
			f.day(d, "bsky", "es", "desconhecido", 10, 1)
		}
	}
	d := weekStart.AddDate(0, 0, 1)
	f.term("daily_terms", d, "hn", "invoice", 10)
	f.term("daily_terms", d, "hn", "spreadsheet", 12)
	f.term("daily_terms", d, "hn", "multi currency", 6) // novo
	f.term("daily_terms", d, "hn", "raro", 1)           // abaixo do mínimo
	f.term("daily_brands", d, "bsky", "Trello", 9)
	f.term("daily_brands", d, "hn", "Notion", 4)

	f.item("a", d, "hn", 3.5, "I would pay for a tool that tracks invoices in multiple currencies.")
	f.item("b", d, "bsky", 4.2, "Tem algum app que | faça isso?\n\n# não é título")
	f.item("c", d.AddDate(0, 0, -7), "hn", 9.9, "semana passada: não entra")
	f.item("d", d, "gh", 1.0, strings.Repeat("palavra ", 100))

	// HN coletou seg, ter e qua; quinta (hoje) falhou. Bluesky ok em todos os dias.
	// GitHub nunca coletou.
	for i := 0; i < 4; i++ {
		at := weekStart.AddDate(0, 0, i)
		if i == 3 {
			f.run("hn", at, "error", "HTTP 503 em https://hn.algolia.com")
		} else {
			f.run("hn", at, "ok", "")
		}
		f.run("bsky", at, "ok", "")
	}
}

func build(t *testing.T, f *fixture) *Report {
	t.Helper()
	r, err := Build(context.Background(), f.conn, Options{
		Week: "2026-48", Now: now, DBPath: f.path, AINote: "não usada (--no-ai)",
		TopCandidates: 3, TopKeywords: 10, TopBrands: 10, MinKeywordCount: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestBuild(t *testing.T) {
	f := newFixture(t)
	seed(f)
	r := build(t, f)

	if r.TotalItems != 4*50+4*20+2*10 || r.TotalCandidates != 4*5+4*4+2*1 {
		t.Errorf("totais = %d / %d", r.TotalItems, r.TotalCandidates)
	}
	if len(r.Sources) != 3 || r.Sources[2].Key != "gh" || r.Sources[2].Items != 0 {
		t.Errorf("fontes = %+v", r.Sources)
	}
	// Candidatos: só da semana, ordenados pelo score, limitados a 3.
	if len(r.Candidates) != 3 || r.Candidates[0].Source != "bsky" || r.Candidates[1].Source != "hn" {
		t.Errorf("candidatos = %+v", r.Candidates)
	}
	if r.HistoryWeeks != 4 {
		t.Errorf("semanas de histórico = %d", r.HistoryWeeks)
	}

	trend := map[string]Trend{}
	for _, k := range r.Keywords {
		trend[k.Term] = k
	}
	if _, ok := trend["invoice"]; ok {
		t.Error("termo estável não deveria estar em alta")
	}
	if _, ok := trend["raro"]; ok {
		t.Error("termo abaixo do mínimo entrou")
	}
	if s := trend["spreadsheet"]; s.Week != 12 || s.PrevAvg != 2 {
		t.Errorf("spreadsheet = %+v", s)
	}
	if !trend["multi currency"].IsNew() {
		t.Error("multi currency deveria ser novo")
	}
	if len(r.Brands) != 2 || r.Brands[0].Term != "Trello" || r.Brands[1].PrevAvg != 5 {
		t.Errorf("marcas = %+v", r.Brands)
	}

	h := r.Health
	if !h.NeverCollected["gh"] || len(h.MissingDays["bsky"]) != 0 {
		t.Errorf("saúde: %+v", h)
	}
	if got := h.MissingDays["hn"]; len(got) != 1 || got[0] != "2026-11-26" {
		t.Errorf("dias sem coleta do HN = %v (sexta e sábado ainda não chegaram)", got)
	}
	probs := strings.Join(h.Problems(), "; ")
	for _, want := range []string{"HN sem coleta em 1 dia(s)", "GitHub nunca coletou", "HN com 1 execução(ões) com erro"} {
		if !strings.Contains(probs, want) {
			t.Errorf("problemas sem %q: %s", want, probs)
		}
	}
}

func TestMarkdownAndSummary(t *testing.T) {
	f := newFixture(t)
	seed(f)
	md := build(t, f).Markdown()
	for _, want := range []string{
		"# Ideias — semana 2026-48\n",
		"Coleta: HN 200 · Bluesky 100 · GitHub 0 → 38 candidatos\n",
		"IA: não usada (--no-ai)\n",
		"Período: 23/11/2026 a 29/11/2026 (UTC)",
		"## Top candidatos",
		"### 1. 4,20 · Bluesky · EN · Notion",
		"> Tem algum app que | faça isso? # não é título",
		"Sinais: pagamento, pedido · País est.: desconhecido · Engajamento: 7",
		"[abrir](https://example.com/a)",
		"## Rankings",
		"### Palavras-chave em alta",
		"Base: 4 semanas anteriores.",
		"| spreadsheet | 12 | 2,0 | +500% |",
		"| multi currency | 6 | 0,0 | novo |",
		"| Termo | Semana | Média ant. | Variação |",
		"### Marcas mais citadas",
		"| Marca | Semana | Média ant. | Variação |",
		"| Trello | 9 | 0,0 | novo |",
		"| Notion | 4 | 5,0 | -20% |",
		"### Por idioma",
		"| PT | 80 | 16 | 20,0% |",
		"### Por país estimado",
		"| BR | 80 | 16 |",
		"## Saúde da coleta",
		"- Dias sem coleta: HN: 26/11 · Bluesky: nenhum · GitHub: nunca coletou",
		"HN 1 de 4 (último: HTTP 503",
		"- Banco: ",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("relatório sem %q", want)
		}
	}
	if strings.Contains(md, "%!") {
		t.Error("erro de formatação (%!) no relatório")
	}
	if strings.Contains(md, "semana passada") || strings.Contains(md, "### 4.") {
		t.Error("candidato de outra semana ou além do limite entrou")
	}
	if t.Failed() {
		t.Log(md)
	}

	sum := build(t, f).Summary()
	if len([]rune(sum)) > 1500 || !strings.Contains(sum, "Top candidatos:\n1. (4,2 · Bluesky)") ||
		!strings.Contains(sum, "Em alta: ") || !strings.Contains(sum, "Saúde: ⚠️") {
		t.Errorf("resumo:\n%s", sum)
	}
}

func TestEmptyWeekAndShortHistory(t *testing.T) {
	f := newFixture(t)
	r := build(t, f)
	md := r.Markdown()
	for _, want := range []string{"Nenhum candidato nesta semana.", "Sem dados suficientes", "GitHub: nunca coletou"} {
		if !strings.Contains(md, want) {
			t.Errorf("semana vazia sem %q:\n%s", want, md)
		}
	}

	// Só 1 semana anterior: a média divide por 1, não por 4.
	f2 := newFixture(t)
	prev := time.Date(2026, 11, 17, 6, 0, 0, 0, time.UTC)
	f2.day(prev, "hn", "en", "desconhecido", 10, 1)
	f2.term("daily_terms", prev, "hn", "invoice", 8)
	f2.term("daily_terms", prev.AddDate(0, 0, 7), "hn", "invoice", 16)
	r2 := build(t, f2)
	if r2.HistoryWeeks != 1 || len(r2.Keywords) != 1 || r2.Keywords[0].PrevAvg != 8 {
		t.Errorf("histórico curto: semanas=%d termos=%+v", r2.HistoryWeeks, r2.Keywords)
	}
	if !strings.Contains(r2.Markdown(), "Base: só 1 semana(s) anterior(es) com dados.") {
		t.Error("nota de histórico curto ausente")
	}
}

func TestExcerpt(t *testing.T) {
	if got := Excerpt("  ## Título\n\n  texto   solto ", 100); got != "Título texto solto" {
		t.Errorf("got %q", got)
	}
	got := Excerpt(strings.Repeat("abc ", 50), 20)
	if !strings.HasSuffix(got, "…") || len([]rune(got)) > 21 || strings.HasSuffix(got, " …") {
		t.Errorf("corte: %q", got)
	}
}
