package ai

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"gerador-ideias/internal/fmtx"
)

// Item é um candidato da semana como a IA o vê.
type Item struct {
	ID      int64
	Source  string
	Lang    string
	Score   float64
	Brands  []string
	Signals []string
	Text    string
}

// Ref é o identificador do candidato nos prompts.
func (it Item) Ref() string { return "i" + strconv.FormatInt(it.ID, 10) }

// Cluster é uma dor já conhecida (do banco) ou criada nesta execução.
type Cluster struct {
	ID       int64  // 0 para clusters criados nesta execução
	Ref      string // "c12" (banco) ou "n3" (criado agora)
	Dor      string
	Nicho    string
	Resumo   string
	Mentions int    // menções antes desta semana
	Weeks    int    // semanas com menções antes desta semana
	LastSeen string // última semana vista antes desta
}

// Week reúne o que a IA precisa sobre uma semana.
type Week struct {
	Week     string
	Items    []Item
	Clusters []Cluster
}

// LoadWeek lê os candidatos da semana (os maxItems de maior pain_score; 0 =
// todos) e os maxClusters clusters vistos mais recentemente. Clusters que só
// existem por causa de um agrupamento anterior desta mesma semana ficam de
// fora: serão refeitos.
func LoadWeek(ctx context.Context, conn *sql.DB, week string, maxItems, maxClusters int) (*Week, error) {
	w := &Week{Week: week}
	limit := maxItems
	if limit <= 0 {
		limit = -1
	}
	rows, err := conn.QueryContext(ctx, `SELECT id, source, lang, pain_score, brands, signals, text
		FROM items WHERE week = ? ORDER BY pain_score DESC, engagement DESC, id LIMIT ?`, week, limit)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var it Item
		var brands, signals string
		if err := rows.Scan(&it.ID, &it.Source, &it.Lang, &it.Score, &brands, &signals, &it.Text); err != nil {
			rows.Close()
			return nil, err
		}
		it.Brands, it.Signals = split(brands), split(signals)
		w.Items = append(w.Items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = conn.QueryContext(ctx, `SELECT c.id, c.pain, c.niche, c.summary,
			coalesce(sum(cw.mentions), 0), count(cw.week), coalesce(max(cw.week), '')
		FROM clusters c JOIN cluster_weeks cw ON cw.cluster_id = c.id AND cw.week <> ?
		GROUP BY c.id ORDER BY max(cw.week) DESC, sum(cw.mentions) DESC LIMIT ?`, week, maxClusters)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c Cluster
		if err := rows.Scan(&c.ID, &c.Dor, &c.Nicho, &c.Resumo, &c.Mentions, &c.Weeks, &c.LastSeen); err != nil {
			return nil, err
		}
		c.Ref = "c" + strconv.FormatInt(c.ID, 10)
		w.Clusters = append(w.Clusters, c)
	}
	return w, rows.Err()
}

// ClustersBlock lista os clusters existentes para o prompt.
func ClustersBlock(cs []Cluster) string {
	var b strings.Builder
	b.WriteString("## Clusters existentes\n\n")
	if len(cs) == 0 {
		b.WriteString("Nenhum ainda: crie os clusters necessários.\n")
		return b.String()
	}
	for _, c := range cs {
		fmt.Fprintf(&b, "- %s · dor: %s · nicho: %s", c.Ref, c.Dor, c.Nicho)
		if c.Mentions > 0 {
			fmt.Fprintf(&b, " · histórico: %d menções em %d semana(s), última %s", c.Mentions, c.Weeks, c.LastSeen)
		}
		if c.Resumo != "" {
			fmt.Fprintf(&b, " · resumo: %s", c.Resumo)
		}
		b.WriteString("\n")
	}
	return b.String()
}

var sourceNames = map[string]string{"hn": "HN", "bsky": "Bluesky", "gh": "GitHub"}

// ItemBlock formata um candidato para o prompt.
func ItemBlock(it Item, maxChars int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### %s · %s · %s · score %s\n", it.Ref(), sourceNames[it.Source], strings.ToUpper(it.Lang), fmtx.Decimal(it.Score, 1))
	var meta []string
	if len(it.Brands) > 0 {
		meta = append(meta, "marcas: "+strings.Join(it.Brands, ", "))
	}
	if len(it.Signals) > 0 {
		meta = append(meta, "sinais: "+strings.Join(it.Signals, ", "))
	}
	if len(meta) > 0 {
		b.WriteString(strings.Join(meta, " · ") + "\n")
	}
	b.WriteString(cut(strings.Join(strings.Fields(it.Text), " "), maxChars))
	b.WriteString("\n\n")
	return b.String()
}

func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

func split(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}
