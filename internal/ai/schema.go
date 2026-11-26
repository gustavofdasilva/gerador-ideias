// Package ai faz a parte de IA do relatório (design 5.2 e 5.5): agrupar os
// candidatos em dores e pontuar as mais relevantes. Os dois modos, automático
// (OmniRoute) e manual (Claude web), usam os mesmos prompts e este mesmo
// esquema de resposta.
package ai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Response é o esquema de resposta. No modo automático, o agrupamento devolve
// NovosClusters e Atribuicoes e a pontuação devolve Pontuacoes; no modo
// manual, uma resposta traz as três partes.
type Response struct {
	NovosClusters []NewCluster `json:"novos_clusters"`
	Atribuicoes   []Assignment `json:"atribuicoes"`
	Pontuacoes    []Score      `json:"pontuacoes"`
}

// NewCluster é uma dor nova criada pela IA.
type NewCluster struct {
	Ref    string `json:"ref"`
	Dor    string `json:"dor"`
	Nicho  string `json:"nicho"`
	Resumo string `json:"resumo"`
}

// Assignment liga um candidato a um cluster (nil = não é dor).
type Assignment struct {
	Item    string  `json:"item"`
	Cluster *string `json:"cluster"`
}

// Score é a avaliação de uma dor pela rubrica (design, seção 6).
type Score struct {
	Cluster           string         `json:"cluster"`
	Notas             map[string]int `json:"notas"`
	Penalidades       []Penalty      `json:"penalidades"`
	BonusConcorrencia Bonus          `json:"bonus_concorrencia"`
	CanalProvavel     string         `json:"canal_provavel"`
	IndicioCanal      string         `json:"indicio_canal"`
	MVP               string         `json:"mvp"`
	PorQuePodeFalhar  string         `json:"por_que_pode_falhar"`
}

// Penalty é uma penalidade da rubrica.
type Penalty struct {
	Tipo   string `json:"tipo"`
	Motivo string `json:"motivo"`
}

// Bonus é o bônus de concorrência.
type Bonus struct {
	Aplica bool   `json:"aplica"`
	Motivo string `json:"motivo"`
}

// Criteria são as chaves de "notas", na ordem da rubrica.
var Criteria = []string{"frequencia", "pagamento", "distribuicao", "b2b", "global", "solo", "vendabilidade", "infra"}

// PenaltyTypes são os tipos aceitos em "penalidades".
var PenaltyTypes = []string{"big_tech", "efeito_rede", "regulado", "api_fragil", "prospeccao_manual"}

var reFence = regexp.MustCompile("(?s)```(?:json|JSON)?\\s*\n?(.*?)```")

// Parse extrai e decodifica o JSON de uma resposta, tolerando cercas
// ```json e texto antes ou depois (como o Claude web costuma responder).
func Parse(raw []byte) (*Response, error) {
	text := strings.TrimSpace(string(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))))
	candidates := []string{text}
	for _, m := range reFence.FindAllStringSubmatch(text, -1) {
		candidates = append(candidates, strings.TrimSpace(m[1]))
	}
	if i, j := strings.Index(text, "{"), strings.LastIndex(text, "}"); i >= 0 && j > i {
		candidates = append(candidates, text[i:j+1])
	}
	var firstErr error
	for _, c := range candidates {
		var r Response
		dec := json.NewDecoder(strings.NewReader(c))
		dec.DisallowUnknownFields()
		err := dec.Decode(&r)
		if err == nil {
			return &r, nil
		}
		if firstErr == nil || strings.HasPrefix(c, "{") {
			firstErr = err
		}
	}
	if !strings.Contains(text, "{") {
		return nil, errors.New("a resposta não contém um objeto JSON")
	}
	return nil, fmt.Errorf("JSON inválido: %w", firstErr)
}

// Context é o que a validação precisa saber: quais candidatos e clusters a
// IA recebeu.
type Context struct {
	Items    map[string]bool // refs de candidatos enviados ("i123")
	Clusters map[string]bool // refs de clusters existentes ("c12", "n3")
}

// ValidationError junta todos os problemas encontrados, cada um apontando o
// campo a corrigir.
type ValidationError struct{ Problems []string }

func (e *ValidationError) Error() string {
	return "a resposta não segue o esquema:\n- " + strings.Join(e.Problems, "\n- ")
}

// ValidateGrouping confere novos_clusters e atribuicoes. requireAll exige que
// todos os candidatos do contexto tenham sido atribuídos.
func ValidateGrouping(r *Response, ctx Context, requireAll bool) error {
	var p problems
	newRefs := map[string]bool{}
	for i, c := range r.NovosClusters {
		where := fmt.Sprintf("novos_clusters[%d]", i)
		switch {
		case !strings.HasPrefix(c.Ref, "novo"):
			p.add("%s.ref %q deve começar com \"novo\" (ex.: \"novo1\")", where, c.Ref)
		case newRefs[c.Ref]:
			p.add("%s.ref %q repetida", where, c.Ref)
		}
		newRefs[c.Ref] = true
		if strings.TrimSpace(c.Dor) == "" {
			p.add("%s.dor vazio", where)
		}
		if strings.TrimSpace(c.Nicho) == "" {
			p.add("%s.nicho vazio", where)
		}
	}
	used := map[string]bool{}
	seen := map[string]bool{}
	for i, a := range r.Atribuicoes {
		where := fmt.Sprintf("atribuicoes[%d]", i)
		if !ctx.Items[a.Item] {
			p.add("%s.item %q não é um dos candidatos enviados", where, a.Item)
		} else if seen[a.Item] {
			p.add("%s.item %q aparece mais de uma vez", where, a.Item)
		}
		seen[a.Item] = true
		if a.Cluster == nil {
			continue
		}
		ref := *a.Cluster
		switch {
		case newRefs[ref]:
			used[ref] = true
		case ctx.Clusters[ref]:
		default:
			p.add("%s.cluster %q não existe (use uma ref de \"Clusters existentes\", de novos_clusters ou null)", where, ref)
		}
	}
	for _, c := range r.NovosClusters {
		if c.Ref != "" && !used[c.Ref] {
			p.add("novos_clusters: %q não é usado em nenhuma atribuição", c.Ref)
		}
	}
	if requireAll {
		var missing []string
		for item := range ctx.Items {
			if !seen[item] {
				missing = append(missing, item)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			p.add("atribuicoes: faltam %d candidato(s): %s", len(missing), abbreviate(missing, 10))
		}
	}
	return p.err()
}

// ValidateScores confere pontuacoes. valid diz quais refs de cluster podem ser
// pontuadas.
func ValidateScores(r *Response, valid map[string]bool) error {
	var p problems
	seen := map[string]bool{}
	for i, s := range r.Pontuacoes {
		where := fmt.Sprintf("pontuacoes[%d]", i)
		if !valid[s.Cluster] {
			p.add("%s.cluster %q não é uma das dores recebidas", where, s.Cluster)
		} else if seen[s.Cluster] {
			p.add("%s.cluster %q pontuado mais de uma vez", where, s.Cluster)
		}
		seen[s.Cluster] = true
		for _, k := range Criteria {
			n, ok := s.Notas[k]
			if !ok {
				p.add("%s.notas.%s faltando", where, k)
			} else if n < 0 || n > 5 {
				p.add("%s.notas.%s = %d, precisa ir de 0 a 5", where, k, n)
			}
		}
		for k := range s.Notas {
			if !contains(Criteria, k) {
				p.add("%s.notas.%s não é um critério (use: %s)", where, k, strings.Join(Criteria, ", "))
			}
		}
		for j, pen := range s.Penalidades {
			if !contains(PenaltyTypes, pen.Tipo) {
				p.add("%s.penalidades[%d].tipo %q inválido (use: %s)", where, j, pen.Tipo, strings.Join(PenaltyTypes, ", "))
			}
		}
		for field, v := range map[string]string{"canal_provavel": s.CanalProvavel, "mvp": s.MVP, "por_que_pode_falhar": s.PorQuePodeFalhar} {
			if strings.TrimSpace(v) == "" {
				p.add("%s.%s vazio", where, field)
			}
		}
	}
	return p.err()
}

type problems struct{ list []string }

func (p *problems) add(format string, args ...any) {
	p.list = append(p.list, fmt.Sprintf(format, args...))
}

func (p *problems) err() error {
	if len(p.list) == 0 {
		return nil
	}
	sort.Strings(p.list)
	return &ValidationError{Problems: p.list}
}

func abbreviate(list []string, n int) string {
	if len(list) <= n {
		return strings.Join(list, ", ")
	}
	return strings.Join(list[:n], ", ") + fmt.Sprintf(" e mais %d", len(list)-n)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
