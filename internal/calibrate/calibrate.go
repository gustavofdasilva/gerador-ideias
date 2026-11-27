// Package calibrate mede o filtro local contra o veredito da IA (items.ai_verdict):
// de cada fonte, frase e faixa de pain_score, quanto a IA aproveitou como dor e
// quanto descartou como ruído. É a base da calibração (roadmap F7).
package calibrate

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"gerador-ideias/internal/db"
	"gerador-ideias/internal/fmtx"
	"gerador-ideias/internal/process"
	"gerador-ideias/internal/week"
)

// Row é um recorte: quantos candidatos a IA julgou e quantos viraram dor.
type Row struct {
	Key    string
	Judged int
	Pains  int
}

// Rate é a fração aproveitada.
func (r Row) Rate() float64 {
	if r.Judged == 0 {
		return 0
	}
	return float64(r.Pains) / float64(r.Judged)
}

// Result é a calibração de um período.
type Result struct {
	Weeks     []string
	Judged    int
	Pains     int
	BySource  []Row
	ByPhrase  []Row // Key = "fonte · frase"
	ByScore   []Row // faixas de pain_score
	NoPhrase  int   // julgados que não casam nenhuma frase do config atual
	MinJudged int   // mínimo de julgados para uma frase entrar nas sugestões
}

// Weeks devolve as n semanas que terminam em last (a mais antiga primeiro).
func Weeks(last string, n int) ([]string, error) {
	start, err := week.Start(last)
	if err != nil {
		return nil, err
	}
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[n-1-i] = week.Of(start.AddDate(0, 0, -7*i))
	}
	return out, nil
}

var bands = []struct {
	label string
	upTo  float64 // exclusivo; o último é aberto
}{
	{"< 1,5", 1.5}, {"1,5 a 2", 2}, {"2 a 3", 3}, {"3 a 4", 4}, {"4 ou mais", 1e9},
}

// Build lê os candidatos julgados das semanas e recalcula as frases com o
// Processor (config atual), para medir também mudanças ainda não coletadas.
func Build(ctx context.Context, conn *sql.DB, proc *process.Processor, weeks []string) (*Result, error) {
	res := &Result{Weeks: weeks, MinJudged: 10}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(weeks)), ",")
	args := make([]any, len(weeks))
	for i, w := range weeks {
		args[i] = w
	}
	rows, err := conn.QueryContext(ctx, `SELECT source, lang_hint, text, brands, engagement, pain_score, ai_verdict
		FROM items WHERE ai_verdict IS NOT NULL AND week IN (`+marks+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	bySource, byPhrase, byScore := map[string]*Row{}, map[string]*Row{}, map[string]*Row{}
	bump := func(m map[string]*Row, key string, pain bool) {
		r := m[key]
		if r == nil {
			r = &Row{Key: key}
			m[key] = r
		}
		r.Judged++
		if pain {
			r.Pains++
		}
	}
	for rows.Next() {
		var src, hint, text, brands string
		var eng int
		var score float64
		var verdict int
		if err := rows.Scan(&src, &hint, &text, &brands, &eng, &score, &verdict); err != nil {
			return nil, err
		}
		pain := verdict == 1
		res.Judged++
		if pain {
			res.Pains++
		}
		bump(bySource, src, pain)
		for _, b := range bands {
			if score < b.upTo {
				bump(byScore, b.label, pain)
				break
			}
		}
		it := db.Item{Source: src, Text: text, LangHint: hint, Engagement: eng, CreatedAt: time.Now()}
		if brands != "" {
			it.Brands = strings.Split(brands, ",")
		}
		matches := proc.Process(it).Matches
		if len(matches) == 0 {
			res.NoPhrase++
		}
		for _, m := range matches {
			bump(byPhrase, src+" · "+m, pain)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	res.BySource = sorted(bySource, func(a, b Row) bool { return a.Key < b.Key })
	res.ByPhrase = sorted(byPhrase, func(a, b Row) bool {
		if a.Judged != b.Judged {
			return a.Judged > b.Judged
		}
		return a.Key < b.Key
	})
	for _, b := range bands {
		if r, ok := byScore[b.label]; ok {
			res.ByScore = append(res.ByScore, *r)
		}
	}
	return res, nil
}

func sorted(m map[string]*Row, less func(a, b Row) bool) []Row {
	out := make([]Row, 0, len(m))
	for _, r := range m {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return less(out[i], out[j]) })
	return out
}

// Noisy são as frases com volume e quase só ruído (candidatas a sair, ou a
// exigir marca em pain.brand_required, ou a ir para pain.exclude).
func (r *Result) Noisy() []Row {
	var out []Row
	for _, p := range r.ByPhrase {
		if p.Judged >= r.MinJudged && p.Rate() < 0.10 {
			out = append(out, p)
		}
	}
	return out
}

// Good são as frases com volume e bom aproveitamento.
func (r *Result) Good() []Row {
	var out []Row
	for _, p := range r.ByPhrase {
		if p.Judged >= r.MinJudged && p.Rate() >= 0.40 {
			out = append(out, p)
		}
	}
	return out
}

// Text formata o resultado para o terminal.
func (r *Result) Text(maxPhrases int) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	period := r.Weeks[0]
	if len(r.Weeks) > 1 {
		period += " a " + r.Weeks[len(r.Weeks)-1]
	}
	if r.Judged == 0 {
		w("Calibração %s: nenhum candidato julgado pela IA.\n", period)
		w("Rode um report com IA ou um import do Claude web para essa semana e tente de novo.\n")
		return b.String()
	}
	total := Row{Judged: r.Judged, Pains: r.Pains}
	w("Calibração %s: %d candidatos julgados pela IA · %d viraram dor (%s) · %d ruído\n",
		period, r.Judged, r.Pains, fmtx.Percent(total.Rate()), r.Judged-r.Pains)

	table := func(title string, rows []Row, limit int) {
		w("\n%s\n", title)
		w("  %-40s %8s %6s %8s\n", "", "julgados", "dores", "aprov.")
		for i, row := range rows {
			if limit > 0 && i >= limit {
				w("  … mais %d linha(s)\n", len(rows)-limit)
				break
			}
			w("  %-40s %8d %6d %8s\n", cut(row.Key, 40), row.Judged, row.Pains, fmtx.Percent(row.Rate()))
		}
	}
	table("Por fonte:", r.BySource, 0)
	table("Por faixa de pain_score:", r.ByScore, 0)
	table("Por frase (fonte · frase), das mais frequentes:", r.ByPhrase, maxPhrases)
	if r.NoPhrase > 0 {
		w("  (%d julgados não casam mais nenhuma frase do config atual)\n", r.NoPhrase)
	}

	if noisy := r.Noisy(); len(noisy) > 0 {
		w("\nQuase só ruído (≥ %d julgados, < 10%% aproveitados): candidatas a sair de pain.phrases,\n"+
			"a exigir marca (pain.brand_required) ou a ganhar exclusões (pain.exclude):\n", r.MinJudged)
		for _, n := range noisy {
			w("  %-40s %d de %d\n", cut(n.Key, 40), n.Pains, n.Judged)
		}
	}
	if good := r.Good(); len(good) > 0 {
		w("\nBom aproveitamento (≥ %d julgados, ≥ 40%%): manter ou dar mais peso:\n", r.MinJudged)
		for _, g := range good {
			w("  %-40s %d de %d\n", cut(g.Key, 40), g.Pains, g.Judged)
		}
	}
	w("\nAs frases são recalculadas com o config atual: edite o config.toml e rode de novo para comparar.\n")
	return b.String()
}

func cut(s string, n int) string {
	if rs := []rune(s); len(rs) > n {
		return string(rs[:n-1]) + "…"
	}
	return s
}
