package report

import (
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"gerador-ideias/internal/fmtx"
)

// Nomes legíveis dos tipos de sinal (config pain.weights).
var signalNames = map[string]string{
	"request": "pedido", "alternative": "alternativa", "payment": "pagamento",
	"complaint": "reclamação", "churn": "cancelamento", "distribution": "distribuição",
}

// Markdown devolve o relatório no formato da seção 7 do design. Com as dores
// da IA, traz o "Top 10 dores"; sem IA, os candidatos com maior pain_score.
func (r *Report) Markdown() string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	w("# Ideias — semana %s\n", r.Week)
	w("%s\n", r.collectLine())
	w("IA: %s\n", r.AINote)
	w("Período: %s a %s (UTC) · gerado em %s\n", brDate(r.First), brDate(r.Last),
		r.GeneratedAt.Local().Format("02/01/2006 15:04"))

	if r.WithPains {
		r.writePains(w)
	} else {
		r.writeCandidates(w)
	}
	w("\n## Rankings\n")
	r.writeTrends(w, "Palavras-chave em alta", "Termo", r.Keywords,
		"Termos que mais cresceram contra a média das semanas anteriores.")
	r.writeTrends(w, "Marcas mais citadas", "Marca", r.Brands, "")
	r.writeNiches(w)
	r.writeCuts(w)
	r.writeHealth(w)
	return b.String()
}

func (r *Report) collectLine() string {
	parts := make([]string, 0, len(r.Sources))
	for _, s := range r.Sources {
		parts = append(parts, SourceNames[s.Key]+" "+fmtx.Int(s.Items))
	}
	line := fmt.Sprintf("Coleta: %s → %s candidatos", strings.Join(parts, " · "), fmtx.Int(r.TotalCandidates))
	if r.WithPains {
		line += fmt.Sprintf(" → %s dores", fmtx.Int(r.PainCount))
	}
	return line
}

func (r *Report) writeCandidates(w func(string, ...any)) {
	w("\n## Top candidatos\n")
	w("_Os %d posts com maior pain_score da semana. Ainda não agrupados em dores (isso é feito pela IA)._\n", len(r.Candidates))
	if len(r.Candidates) == 0 {
		w("\nNenhum candidato nesta semana.\n")
		return
	}
	for i, c := range r.Candidates {
		head := []string{fmtx.Decimal(c.Score, 2), SourceNames[c.Source], strings.ToUpper(c.Lang)}
		if len(c.Brands) > 0 {
			head = append(head, strings.Join(c.Brands, ", "))
		}
		w("\n### %d. %s\n", i+1, strings.Join(head, " · "))
		w("> %s\n\n", Excerpt(c.Text, 240))
		sigs := make([]string, len(c.Signals))
		for k, s := range c.Signals {
			sigs[k] = nameOr(signalNames, s)
		}
		w("Sinais: %s · País est.: %s · Engajamento: %d · %s · [abrir](%s)\n",
			orDash(strings.Join(sigs, ", ")), c.Country, c.Engagement, c.CreatedAt.Format("02/01"), c.URL)
	}
}

func (r *Report) writeTrends(w func(string, ...any), title, label string, list []Trend, note string) {
	w("\n### %s\n", title)
	if note != "" {
		w("_%s %s_\n", note, r.historyNote())
	}
	if len(list) == 0 {
		w("\nSem dados suficientes nesta semana.\n")
		return
	}
	w("\n| %s | Semana | Média ant. | Variação |\n|---|---:|---:|---|\n", label)
	for _, t := range list {
		w("| %s | %s | %s | %s |\n", escapeCell(t.Term), fmtx.Int(t.Week), fmtx.Decimal(t.PrevAvg, 1), r.change(t))
	}
}

func (r *Report) historyNote() string {
	switch r.HistoryWeeks {
	case 0:
		return "Ainda não há semanas anteriores: a lista mostra só os termos mais frequentes."
	case 4:
		return "Base: 4 semanas anteriores."
	default:
		return fmt.Sprintf("Base: só %d semana(s) anterior(es) com dados.", r.HistoryWeeks)
	}
}

func (r *Report) change(t Trend) string {
	if r.HistoryWeeks == 0 {
		return "—"
	}
	if t.IsNew() {
		return "novo"
	}
	return fmt.Sprintf("%+.0f%%", math.Round(t.Change()*100))
}

func (r *Report) writeCuts(w func(string, ...any)) {
	w("\n### Por idioma\n\n| Idioma | Itens | Candidatos | Taxa |\n|---|---:|---:|---:|\n")
	for _, c := range r.Langs {
		w("| %s | %s | %s | %s |\n", strings.ToUpper(c.Key), fmtx.Int(c.Items), fmtx.Int(c.Candidates), fmtx.Percent(c.Rate()))
	}
	w("\n### Por país estimado\n_Estimativa por moeda, links, lugares citados e idioma; a maioria dos posts não dá pista nenhuma._\n")
	w("\n| País | Itens | Candidatos |\n|---|---:|---:|\n")
	for _, c := range r.Countries {
		key := c.Key
		if len(key) == 2 {
			key = strings.ToUpper(key)
		}
		w("| %s | %s | %s |\n", key, fmtx.Int(c.Items), fmtx.Int(c.Candidates))
	}
}

func (r *Report) writeHealth(w func(string, ...any)) {
	h := r.Health
	w("\n## Saúde da coleta\n")
	var missing []string
	for _, src := range Sources {
		name := SourceNames[src]
		switch {
		case h.NeverCollected[src]:
			missing = append(missing, name+": nunca coletou")
		case len(h.MissingDays[src]) == 0:
			missing = append(missing, name+": nenhum")
		default:
			days := make([]string, len(h.MissingDays[src]))
			for i, d := range h.MissingDays[src] {
				days[i] = brDate(d)[:5]
			}
			missing = append(missing, name+": "+strings.Join(days, ", "))
		}
	}
	w("- Dias sem coleta: %s\n", strings.Join(missing, " · "))

	var errs []string
	for _, e := range h.Errors {
		if e.Failed == 0 {
			errs = append(errs, fmt.Sprintf("%s 0 de %d", SourceNames[e.Source], e.Runs))
			continue
		}
		errs = append(errs, fmt.Sprintf("%s %d de %d (último: %s)", SourceNames[e.Source], e.Failed, e.Runs,
			escapeCell(Excerpt(e.LastError, 140))))
	}
	w("- Execuções com erro: %s\n", orDash(strings.Join(errs, " · ")))
	if s := h.DB; s != nil {
		line := fmt.Sprintf("- Banco: %s", fmtx.Bytes(s.FileBytes+s.WALBytes))
		if s.DataDays > 0 {
			line += fmt.Sprintf(" · crescimento estimado %s/mês (%s/ano; meta < 200 MiB/ano)",
				fmtx.Bytes(s.MonthlyGrowth), fmtx.Bytes(s.MonthlyGrowth*12))
		}
		w("%s\n", line)
	}
}

// Summary é a mensagem curta do Telegram (texto puro, bem abaixo de 4.096 caracteres).
func (r *Report) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Ideias — semana %s\n%s\n", r.Week, r.collectLine())
	if r.WithPains {
		if n := min(len(r.Pains), 5); n > 0 {
			b.WriteString("\nTop dores:\n")
			for i, p := range r.Pains[:n] {
				fmt.Fprintf(&b, "%d. %s — %d/100 (%s)\n", i+1, p.Dor, p.Score, p.Trend)
			}
		}
	} else if n := min(len(r.Candidates), 5); n > 0 {
		b.WriteString("\nTop candidatos:\n")
		for i, c := range r.Candidates[:n] {
			fmt.Fprintf(&b, "%d. (%s · %s) %s\n", i+1, fmtx.Decimal(c.Score, 1), SourceNames[c.Source], Excerpt(c.Text, 110))
		}
	}
	if len(r.Keywords) > 0 {
		terms := make([]string, 0, 8)
		for _, t := range r.Keywords[:min(len(r.Keywords), 8)] {
			terms = append(terms, t.Term)
		}
		fmt.Fprintf(&b, "\nEm alta: %s\n", strings.Join(terms, ", "))
	}
	if len(r.Brands) > 0 {
		names := make([]string, 0, 6)
		for _, t := range r.Brands[:min(len(r.Brands), 6)] {
			names = append(names, t.Term)
		}
		fmt.Fprintf(&b, "Marcas: %s\n", strings.Join(names, ", "))
	}
	if p := r.Health.Problems(); len(p) > 0 {
		fmt.Fprintf(&b, "\nSaúde: ⚠️ %s\n", strings.Join(p, "; "))
	} else {
		b.WriteString("\nSaúde: ok\n")
	}
	b.WriteString("\nRelatório completo no arquivo anexo.")
	return b.String()
}

// Excerpt junta os espaços, tira marcações de início de linha e corta em
// até n caracteres sem partir palavra.
func Excerpt(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.TrimLeft(s, "#> ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	rs := []rune(s)[:n]
	cut := string(rs)
	if i := strings.LastIndex(cut, " "); i > n/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,.;:") + "…"
}

func escapeCell(s string) string { return strings.ReplaceAll(s, "|", "\\|") }

func brDate(day string) string {
	if len(day) != 10 {
		return day
	}
	return day[8:10] + "/" + day[5:7] + "/" + day[0:4]
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func nameOr(m map[string]string, k string) string {
	if v, ok := m[k]; ok {
		return v
	}
	return k
}
