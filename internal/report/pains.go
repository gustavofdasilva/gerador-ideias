package report

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"gerador-ideias/internal/ai"
	"gerador-ideias/internal/fmtx"
	"gerador-ideias/internal/week"
)

// Pain é uma dor pontuada da semana (seção "Top 10 dores").
type Pain struct {
	ClusterID int64
	Dor       string
	Nicho     string
	Score     int
	Trend     string // "↑ 3 semanas", "↓ 1 semana", "→ estável", "nova"
	Mentions  int    // nesta semana
	Total     int    // no histórico inteiro
	Langs     string // "EN 70%, PT 20%"
	Country   string // estimativa
	Brands    []string
	Evidence  []string // links
	Detail    ai.Detail
}

// TopPains é quantas dores entram na seção do relatório.
const TopPains = 10

// pains lê as dores pontuadas da semana e os nichos.
func pains(ctx context.Context, conn *sql.DB, r *Report, _ Options) error {
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM cluster_weeks WHERE week = ?`, r.Week).Scan(&r.PainCount); err != nil {
		return err
	}
	rows, err := conn.QueryContext(ctx, `SELECT c.id, c.pain, c.niche, cw.score, cw.mentions, c.total_mentions,
			cw.score_detail, c.first_seen_week
		FROM cluster_weeks cw JOIN clusters c ON c.id = cw.cluster_id
		WHERE cw.week = ? AND cw.score IS NOT NULL
		ORDER BY cw.score DESC, cw.mentions DESC, c.id LIMIT ?`, r.Week, TopPains)
	if err != nil {
		return err
	}
	var firstSeen []string
	for rows.Next() {
		var p Pain
		var detail sql.NullString
		var first string
		if err := rows.Scan(&p.ClusterID, &p.Dor, &p.Nicho, &p.Score, &p.Mentions, &p.Total, &detail, &first); err != nil {
			rows.Close()
			return err
		}
		if detail.Valid {
			_ = json.Unmarshal([]byte(detail.String), &p.Detail)
		}
		r.Pains = append(r.Pains, p)
		firstSeen = append(firstSeen, first)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range r.Pains {
		if err := painDetails(ctx, conn, r.Week, &r.Pains[i], firstSeen[i]); err != nil {
			return err
		}
	}

	nrows, err := conn.QueryContext(ctx, `SELECT c.niche, count(*), sum(cw.mentions) FROM cluster_weeks cw
		JOIN clusters c ON c.id = cw.cluster_id WHERE cw.week = ?
		GROUP BY c.niche ORDER BY sum(cw.mentions) DESC, c.niche LIMIT ?`, r.Week, TopPains)
	if err != nil {
		return err
	}
	defer nrows.Close()
	for nrows.Next() {
		var c Count // Items = dores, Candidates = menções
		if err := nrows.Scan(&c.Key, &c.Items, &c.Candidates); err != nil {
			return err
		}
		r.Niches = append(r.Niches, c)
	}
	return nrows.Err()
}

func painDetails(ctx context.Context, conn *sql.DB, wk string, p *Pain, firstSeen string) error {
	rows, err := conn.QueryContext(ctx, `SELECT lang, country_est, brands, url FROM items
		WHERE week = ? AND cluster_id = ? ORDER BY pain_score DESC, engagement DESC`, wk, p.ClusterID)
	if err != nil {
		return err
	}
	defer rows.Close()
	langs, countries, brands := map[string]int{}, map[string]int{}, map[string]int{}
	n := 0
	for rows.Next() {
		var lang, country, brandList, url string
		if err := rows.Scan(&lang, &country, &brandList, &url); err != nil {
			return err
		}
		n++
		langs[strings.ToUpper(lang)]++
		if country != "desconhecido" {
			countries[country]++
		}
		for _, b := range splitList(brandList) {
			brands[b]++
		}
		if len(p.Evidence) < 3 {
			p.Evidence = append(p.Evidence, url)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	var parts []string
	for _, l := range topKeys(langs, 3) {
		parts = append(parts, fmt.Sprintf("%s %s", l, pct(langs[l], n)))
	}
	p.Langs = strings.Join(parts, ", ")
	p.Brands = topKeys(brands, 3)
	p.Country = estimateCountry(countries)

	trend, err := painTrend(ctx, conn, p.ClusterID, wk, firstSeen)
	p.Trend = trend
	return err
}

// estimateCountry: um país com 60% ou mais das pistas vence; pistas
// espalhadas viram "global"; nenhuma pista, "desconhecido".
func estimateCountry(c map[string]int) string {
	total := 0
	for _, n := range c {
		total += n
	}
	if total == 0 {
		return "desconhecido"
	}
	top := topKeys(c, 1)[0]
	if float64(c[top]) >= 0.6*float64(total) {
		return strings.ToUpper(top)
	}
	return "global"
}

// painTrend conta semanas seguidas de alta (ou de queda) das menções até a
// semana do relatório. Semanas sem menção contam como zero.
func painTrend(ctx context.Context, conn *sql.DB, id int64, wk, firstSeen string) (string, error) {
	if firstSeen == wk {
		return "nova", nil
	}
	start, err := week.Start(wk)
	if err != nil {
		return "", err
	}
	const span = 9
	oldest := week.Of(start.AddDate(0, 0, -7*(span-1)))
	rows, err := conn.QueryContext(ctx, `SELECT week, mentions FROM cluster_weeks
		WHERE cluster_id = ? AND week BETWEEN ? AND ?`, id, oldest, wk)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	m := map[string]int{}
	for rows.Next() {
		var w string
		var n int
		if err := rows.Scan(&w, &n); err != nil {
			return "", err
		}
		m[w] = n
	}
	series := make([]int, span) // series[0] = semana do relatório
	for i := range series {
		series[i] = m[week.Of(start.AddDate(0, 0, -7*i))]
	}
	up, down := 0, 0
	for i := 0; i+1 < span && series[i] > series[i+1]; i++ {
		up++
	}
	for i := 0; i+1 < span && series[i] < series[i+1]; i++ {
		down++
	}
	switch {
	case up > 0:
		return fmt.Sprintf("↑ %d %s", up, plural(up, "semana", "semanas")), rows.Err()
	case down > 0:
		return fmt.Sprintf("↓ %d %s", down, plural(down, "semana", "semanas")), rows.Err()
	}
	return "→ estável", rows.Err()
}

// Nomes curtos das notas, na ordem do formato da seção 7.
var noteOrder = []struct{ key, label string }{
	{"pagamento", "pagamento"}, {"distribuicao", "distribuição"}, {"b2b", "B2B"}, {"global", "global"},
	{"solo", "solo"}, {"vendabilidade", "venda"}, {"infra", "infra"}, {"frequencia", "freq."},
}

var penaltyNames = map[string]string{
	"big_tech": "big tech genérica", "efeito_rede": "exige efeito de rede", "regulado": "setor regulado",
	"api_fragil": "depende de API frágil", "prospeccao_manual": "prospecção manual",
}

func (r *Report) writePains(w func(string, ...any)) {
	w("\n## Top 10 dores\n")
	if len(r.Pains) == 0 {
		w("\nNenhuma dor pontuada nesta semana.\n")
		return
	}
	for i, p := range r.Pains {
		w("\n### %d. %s — %d/100 (%s)\n", i+1, p.Dor, p.Score, p.Trend)
		w("Nicho: %s · Idiomas: %s · País est.: %s\n", p.Nicho, orDash(p.Langs), p.Country)
		w("Menções: %d nesta semana / %d no total · Marcas: %s\n", p.Mentions, p.Total, orDash(strings.Join(p.Brands, ", ")))
		notes := make([]string, 0, len(noteOrder))
		for _, n := range noteOrder {
			notes = append(notes, fmt.Sprintf("%s %d", n.label, p.Detail.Notas[n.key]))
		}
		w("Notas: %s\n", strings.Join(notes, " · "))
		if adj := adjustments(p.Detail); adj != "" {
			w("Ajustes: %s\n", adj)
		}
		canal := p.Detail.CanalProvavel
		if p.Detail.IndicioCanal != "" {
			canal += " (indício: " + p.Detail.IndicioCanal + ")"
		}
		w("Canal provável: %s\n", orDash(canal))
		w("MVP em uma frase: %s\n", orDash(p.Detail.MVP))
		w("Por que pode falhar: %s\n", orDash(p.Detail.PorQuePodeFalhar))
		links := make([]string, len(p.Evidence))
		for k, u := range p.Evidence {
			links[k] = fmt.Sprintf("[%d](%s)", k+1, u)
		}
		w("Evidências: %s\n", orDash(strings.Join(links, " ")))
	}
}

// adjustments descreve penalidades e bônus ("−15 prospecção manual: motivo").
func adjustments(d ai.Detail) string {
	var parts []string
	if len(d.Penalidades) > 0 {
		each := d.Penalized / len(d.Penalidades)
		for _, p := range d.Penalidades {
			parts = append(parts, fmt.Sprintf("−%d %s: %s", each, nameOr(penaltyNames, p.Tipo), p.Motivo))
		}
	}
	if d.Bonus > 0 {
		parts = append(parts, fmt.Sprintf("+%d concorrente caro ou complexo: %s", d.Bonus, d.BonusConcorrencia.Motivo))
	}
	return strings.Join(parts, " · ")
}

func (r *Report) writeNiches(w func(string, ...any)) {
	if len(r.Niches) == 0 {
		return
	}
	w("\n### Nichos\n\n| Nicho | Dores | Menções |\n|---|---:|---:|\n")
	for _, n := range r.Niches {
		w("| %s | %s | %s |\n", escapeCell(n.Key), fmtx.Int(n.Items), fmtx.Int(n.Candidates))
	}
}

func topKeys(m map[string]int, n int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	return keys[:min(n, len(keys))]
}

func pct(n, total int) string {
	if total == 0 {
		return "0%"
	}
	return fmt.Sprintf("%d%%", (n*100+total/2)/total)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
