// Package report monta o relatório semanal (design, seções 5.2 e 7) a partir
// do banco: números da coleta, candidatos, tendências, recortes e saúde.
package report

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"gerador-ideias/internal/db"
	"gerador-ideias/internal/week"
)

// Sources são as fontes da v1, na ordem em que aparecem no relatório.
var Sources = []string{"hn", "bsky", "gh"}

// SourceNames são os nomes exibidos.
var SourceNames = map[string]string{"hn": "HN", "bsky": "Bluesky", "gh": "GitHub"}

// Options controla o que entra no relatório.
type Options struct {
	Week            string // AAAA-WW
	Now             time.Time
	DBPath          string
	AINote          string // linha "IA: ..." do cabeçalho
	TopCandidates   int
	TopKeywords     int
	TopBrands       int
	MinKeywordCount int
}

// Report são os dados de uma semana, prontos para renderizar.
type Report struct {
	Week        string
	First, Last string // dias AAAA-MM-DD
	GeneratedAt time.Time
	AINote      string

	Sources         []Count // itens e candidatos da semana por fonte
	TotalItems      int64
	TotalCandidates int64
	Candidates      []Candidate
	HistoryWeeks    int // semanas anteriores (até 4) com dados, base da tendência
	Keywords        []Trend
	Brands          []Trend
	Langs           []Count
	Countries       []Count
	Health          Health
}

// Count é um recorte com itens processados e candidatos.
type Count struct {
	Key        string
	Items      int64
	Candidates int64
}

// Rate é a fração de candidatos.
func (c Count) Rate() float64 {
	if c.Items == 0 {
		return 0
	}
	return float64(c.Candidates) / float64(c.Items)
}

// Candidate é um item candidato da semana.
type Candidate struct {
	Score      float64
	Source     string
	Lang       string
	Country    string
	Signals    []string
	Brands     []string
	Engagement int
	URL        string
	Text       string
	CreatedAt  time.Time
}

// Trend compara a contagem da semana com a média das semanas anteriores.
type Trend struct {
	Term    string
	Week    int64
	PrevAvg float64
}

// IsNew: não apareceu nas semanas anteriores.
func (t Trend) IsNew() bool { return t.PrevAvg == 0 }

// Change é a variação relativa à média anterior (0,5 = +50%).
func (t Trend) Change() float64 {
	if t.PrevAvg == 0 {
		return math.Inf(1)
	}
	return float64(t.Week)/t.PrevAvg - 1
}

// Health resume a saúde da coleta na semana.
type Health struct {
	// MissingDays: dias (até hoje) sem coleta bem-sucedida, por fonte.
	MissingDays map[string][]string
	// NeverCollected: fontes sem nenhuma coleta registrada no banco.
	NeverCollected map[string]bool
	Errors         []SourceErrors
	DB             *db.Stats
}

// SourceErrors são as execuções com problema de uma fonte na semana.
type SourceErrors struct {
	Source    string
	Runs      int
	Failed    int // status error ou partial
	LastError string
}

// Build lê o banco e monta o relatório da semana.
func Build(ctx context.Context, conn *sql.DB, o Options) (*Report, error) {
	first, last, err := week.Range(o.Week)
	if err != nil {
		return nil, err
	}
	r := &Report{Week: o.Week, First: first, Last: last, GeneratedAt: o.Now, AINote: o.AINote}
	steps := []func(context.Context, *sql.DB, *Report, Options) error{
		collectCounts, candidates, history, keywords, brands, health,
	}
	for _, step := range steps {
		if err := step(ctx, conn, r, o); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func collectCounts(ctx context.Context, conn *sql.DB, r *Report, _ Options) error {
	bySource, err := counts(ctx, conn, "source", r.First, r.Last)
	if err != nil {
		return err
	}
	have := map[string]Count{}
	for _, c := range bySource {
		have[c.Key] = c
		r.TotalItems += c.Items
		r.TotalCandidates += c.Candidates
	}
	for _, s := range Sources {
		c := have[s]
		c.Key = s
		r.Sources = append(r.Sources, c)
	}
	if r.Langs, err = counts(ctx, conn, "lang", r.First, r.Last); err != nil {
		return err
	}
	r.Countries, err = counts(ctx, conn, "country_est", r.First, r.Last)
	return err
}

// counts agrupa daily_items da semana pela coluna dada (constante do código).
func counts(ctx context.Context, conn *sql.DB, col, first, last string) ([]Count, error) {
	rows, err := conn.QueryContext(ctx, `SELECT `+col+`, sum(items), sum(candidates) FROM daily_items
		WHERE day BETWEEN ? AND ? GROUP BY `+col+` ORDER BY sum(items) DESC, `+col, first, last)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Count
	for rows.Next() {
		var c Count
		if err := rows.Scan(&c.Key, &c.Items, &c.Candidates); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func candidates(ctx context.Context, conn *sql.DB, r *Report, o Options) error {
	rows, err := conn.QueryContext(ctx, `SELECT pain_score, source, lang, country_est, signals, brands,
		engagement, url, text, created_at FROM items WHERE week = ?
		ORDER BY pain_score DESC, engagement DESC, created_at DESC LIMIT ?`, r.Week, o.TopCandidates)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var c Candidate
		var signals, brands string
		var created int64
		if err := rows.Scan(&c.Score, &c.Source, &c.Lang, &c.Country, &signals, &brands,
			&c.Engagement, &c.URL, &c.Text, &created); err != nil {
			return err
		}
		c.Signals, c.Brands = splitList(signals), splitList(brands)
		c.CreatedAt = time.Unix(created, 0).UTC()
		r.Candidates = append(r.Candidates, c)
	}
	return rows.Err()
}

// history conta quantas das 4 semanas anteriores têm dados. Com menos de 4,
// a média divide só pelas semanas existentes (senão, no começo, tudo
// pareceria "em alta").
func history(ctx context.Context, conn *sql.DB, r *Report, _ Options) error {
	var minDay sql.NullString
	if err := conn.QueryRowContext(ctx, `SELECT min(day) FROM daily_items`).Scan(&minDay); err != nil {
		return err
	}
	if !minDay.Valid || minDay.String >= r.First {
		return nil
	}
	start, _ := time.Parse("2006-01-02", r.First)
	oldest, _ := time.Parse("2006-01-02", minDay.String)
	weeks := int(math.Ceil(start.Sub(oldest).Hours() / 24 / 7))
	r.HistoryWeeks = min(max(weeks, 0), 4)
	return nil
}

func (r *Report) prevFirst() string {
	start, _ := time.Parse("2006-01-02", r.First)
	return week.Day(start.AddDate(0, 0, -28))
}

func keywords(ctx context.Context, conn *sql.DB, r *Report, o Options) error {
	all, err := trends(ctx, conn, "daily_terms", "term", r, o.MinKeywordCount)
	if err != nil {
		return err
	}
	// "Em alta": cresceu em relação à média, ordenado por uma pontuação que
	// favorece crescimento com volume (diferença ÷ raiz da média).
	var up []Trend
	for _, t := range all {
		if r.HistoryWeeks == 0 || float64(t.Week) > t.PrevAvg {
			up = append(up, t)
		}
	}
	sort.SliceStable(up, func(i, j int) bool {
		si, sj := trendScore(up[i]), trendScore(up[j])
		if si != sj {
			return si > sj
		}
		return up[i].Week > up[j].Week
	})
	r.Keywords = up[:min(len(up), o.TopKeywords)]
	return nil
}

func trendScore(t Trend) float64 { return (float64(t.Week) - t.PrevAvg) / math.Sqrt(t.PrevAvg+1) }

func brands(ctx context.Context, conn *sql.DB, r *Report, o Options) error {
	all, err := trends(ctx, conn, "daily_brands", "brand", r, 1)
	if err != nil {
		return err
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Week > all[j].Week })
	r.Brands = all[:min(len(all), o.TopBrands)]
	return nil
}

// trends soma a semana e as 4 anteriores de daily_terms/daily_brands
// (tabela e coluna são constantes do código).
func trends(ctx context.Context, conn *sql.DB, table, col string, r *Report, minWeek int) ([]Trend, error) {
	rows, err := conn.QueryContext(ctx, `SELECT `+col+`,
		sum(CASE WHEN day >= ? THEN count ELSE 0 END) AS w,
		sum(CASE WHEN day < ? THEN count ELSE 0 END) AS p
		FROM `+table+` WHERE day BETWEEN ? AND ? GROUP BY `+col+` HAVING w >= ?`,
		r.First, r.First, r.prevFirst(), r.Last, minWeek)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	div := float64(max(r.HistoryWeeks, 1))
	var out []Trend
	for rows.Next() {
		var t Trend
		var prev int64
		if err := rows.Scan(&t.Term, &t.Week, &prev); err != nil {
			return nil, err
		}
		t.PrevAvg = float64(prev) / div
		out = append(out, t)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Term < out[j].Term })
	return out, rows.Err()
}

func health(ctx context.Context, conn *sql.DB, r *Report, o Options) error {
	h := Health{MissingDays: map[string][]string{}, NeverCollected: map[string]bool{}}
	start, _ := time.Parse("2006-01-02", r.First)
	end := start.AddDate(0, 0, 7)

	okDays := map[string]map[string]bool{}
	errs := map[string]*SourceErrors{}
	rows, err := conn.QueryContext(ctx, `SELECT source, started_at, status, error_text FROM runs
		WHERE command = 'collect' AND started_at >= ? AND started_at < ? ORDER BY started_at`,
		start.Unix(), end.Unix())
	if err != nil {
		return err
	}
	for rows.Next() {
		var src, status, text string
		var started int64
		if err := rows.Scan(&src, &started, &status, &text); err != nil {
			rows.Close()
			return err
		}
		e := errs[src]
		if e == nil {
			e = &SourceErrors{Source: src}
			errs[src] = e
		}
		e.Runs++
		switch status {
		case "ok", "partial":
			if okDays[src] == nil {
				okDays[src] = map[string]bool{}
			}
			okDays[src][week.Day(time.Unix(started, 0))] = true
		}
		if status == "error" || status == "partial" {
			e.Failed++
			e.LastError = text
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	today := week.Day(o.Now)
	for _, src := range Sources {
		var ever int
		if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE command = 'collect' AND source = ?`, src).Scan(&ever); err != nil {
			return err
		}
		if ever == 0 {
			h.NeverCollected[src] = true
			continue
		}
		for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
			day := week.Day(d)
			if day > today {
				break
			}
			if !okDays[src][day] {
				h.MissingDays[src] = append(h.MissingDays[src], day)
			}
		}
		if e := errs[src]; e != nil {
			h.Errors = append(h.Errors, *e)
		}
	}

	if h.DB, err = db.CollectStats(ctx, conn, o.DBPath, o.Now); err != nil {
		return err
	}
	r.Health = h
	return nil
}

func splitList(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// Problems lista os problemas de saúde em frases curtas (vazio = tudo certo).
func (h Health) Problems() []string {
	var out []string
	for _, src := range Sources {
		name := SourceNames[src]
		if h.NeverCollected[src] {
			out = append(out, name+" nunca coletou")
			continue
		}
		if n := len(h.MissingDays[src]); n > 0 {
			out = append(out, fmt.Sprintf("%s sem coleta em %d dia(s)", name, n))
		}
	}
	for _, e := range h.Errors {
		if e.Failed > 0 {
			out = append(out, fmt.Sprintf("%s com %d execução(ões) com erro", SourceNames[e.Source], e.Failed))
		}
	}
	return out
}
