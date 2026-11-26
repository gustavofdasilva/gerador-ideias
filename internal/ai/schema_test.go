package ai

import (
	"strings"
	"testing"

	"gerador-ideias"
	"gerador-ideias/internal/config"
)

func TestParseTolerant(t *testing.T) {
	js := `{"novos_clusters":[],"atribuicoes":[{"item":"i1","cluster":null}],"pontuacoes":[]}`
	cases := map[string]string{
		"puro":             js,
		"cerca json":       "```json\n" + js + "\n```",
		"texto em volta":   "Aqui está o resultado:\n\n```json\n" + js + "\n```\n\nQualquer dúvida, me avise.",
		"sem cerca":        "Segue: " + js + " (fim)",
		"BOM":              "\xef\xbb\xbf" + js,
		"cerca sem rótulo": "```\n" + js + "\n```",
	}
	for name, in := range cases {
		r, err := Parse([]byte(in))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(r.Atribuicoes) != 1 || r.Atribuicoes[0].Cluster != nil {
			t.Errorf("%s: %+v", name, r)
		}
	}
	for name, in := range map[string]string{
		"sem json":       "Não consegui processar os anexos.",
		"json quebrado":  `{"atribuicoes": [`,
		"campo estranho": `{"atribuicoes": [], "observacoes": "x"}`,
	} {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("%s: esperava erro", name)
		}
	}
}

func str(s string) *string { return &s }

func TestValidateGrouping(t *testing.T) {
	ctx := Context{
		Items:    map[string]bool{"i1": true, "i2": true, "i3": true},
		Clusters: map[string]bool{"c7": true},
	}
	ok := &Response{
		NovosClusters: []NewCluster{{Ref: "novo1", Dor: "d", Nicho: "n"}},
		Atribuicoes: []Assignment{
			{Item: "i1", Cluster: str("c7")}, {Item: "i2", Cluster: str("novo1")}, {Item: "i3", Cluster: nil},
		},
	}
	if err := ValidateGrouping(ok, ctx, true); err != nil {
		t.Fatalf("resposta válida recusada: %v", err)
	}

	bad := &Response{
		NovosClusters: []NewCluster{{Ref: "x1", Dor: "", Nicho: "n"}, {Ref: "novo2", Dor: "d", Nicho: "n"}},
		Atribuicoes: []Assignment{
			{Item: "i1", Cluster: str("c99")}, {Item: "i1", Cluster: nil}, {Item: "i9", Cluster: nil},
		},
	}
	err := ValidateGrouping(bad, ctx, true)
	if err == nil {
		t.Fatal("esperava erros")
	}
	for _, want := range []string{
		`novos_clusters[0].ref "x1" deve começar com "novo"`,
		"novos_clusters[0].dor vazio",
		`atribuicoes[0].cluster "c99" não existe`,
		`atribuicoes[1].item "i1" aparece mais de uma vez`,
		`atribuicoes[2].item "i9" não é um dos candidatos enviados`,
		`novos_clusters: "novo2" não é usado`,
		"atribuicoes: faltam 2 candidato(s): i2, i3",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("erro sem %q:\n%v", want, err)
		}
	}
}

func goodScore(ref string) Score {
	return Score{
		Cluster: ref,
		Notas: map[string]int{"frequencia": 3, "pagamento": 4, "distribuicao": 4, "b2b": 4,
			"global": 5, "solo": 4, "vendabilidade": 4, "infra": 5},
		CanalProvavel: "SEO programático", IndicioCanal: "procurei no Google", MVP: "um app", PorQuePodeFalhar: "nicho pequeno",
	}
}

func TestValidateScores(t *testing.T) {
	valid := map[string]bool{"c1": true, "novo1": true}
	if err := ValidateScores(&Response{Pontuacoes: []Score{goodScore("c1"), goodScore("novo1")}}, valid); err != nil {
		t.Fatal(err)
	}
	s := goodScore("c2")
	s.Notas["pagamento"] = 7
	delete(s.Notas, "infra")
	s.Notas["preco"] = 3
	s.Penalidades = []Penalty{{Tipo: "caro", Motivo: "x"}}
	s.MVP = " "
	err := ValidateScores(&Response{Pontuacoes: []Score{s, goodScore("c1"), goodScore("c1")}}, valid)
	for _, want := range []string{
		`pontuacoes[0].cluster "c2" não é uma das dores recebidas`,
		"pontuacoes[0].notas.pagamento = 7, precisa ir de 0 a 5",
		"pontuacoes[0].notas.infra faltando",
		"pontuacoes[0].notas.preco não é um critério",
		`pontuacoes[0].penalidades[0].tipo "caro" inválido`,
		"pontuacoes[0].mvp vazio",
		`pontuacoes[2].cluster "c1" pontuado mais de uma vez`,
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("erro sem %q:\n%v", want, err)
		}
	}
}

func exampleConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Parse(ideias.ExampleConfig)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestFinalScore(t *testing.T) {
	cfg := exampleConfig(t)
	all5 := goodScore("c1")
	for k := range all5.Notas {
		all5.Notas[k] = 5
	}
	if d := FinalScore(all5, cfg); d.Base != 100 || d.Final != 100 {
		t.Errorf("tudo 5: %+v", d)
	}
	// Pesos do exemplo: freq 10, pag 20, dist 15, b2b 10, global 10, solo 15, venda 10, infra 10.
	s := goodScore("c1") // 3,4,4,4,5,4,4,5
	want := (10*3 + 20*4 + 15*4 + 10*4 + 10*5 + 15*4 + 10*4 + 10*5) / 5
	d := FinalScore(s, cfg)
	if d.Base != want || d.Final != want {
		t.Errorf("base = %d, want %d", d.Base, want)
	}
	s.Penalidades = []Penalty{{Tipo: "prospeccao_manual", Motivo: "vende por demo"}, {Tipo: "big_tech", Motivo: "x"}}
	s.BonusConcorrencia = Bonus{Aplica: true, Motivo: "Harvest é caro"}
	d = FinalScore(s, cfg)
	if d.Penalized != 30 || d.Bonus != 5 || d.Final != want-30+5 {
		t.Errorf("com ajustes: %+v", d)
	}
	zero := goodScore("c1")
	for k := range zero.Notas {
		zero.Notas[k] = 0
	}
	zero.Penalidades = []Penalty{{Tipo: "regulado"}}
	if d := FinalScore(zero, cfg); d.Final != 0 {
		t.Errorf("nota negativa: %+v", d)
	}
}

func TestPromptsUseConfig(t *testing.T) {
	cfg := exampleConfig(t)
	sp := ScorePrompt(cfg)
	for _, want := range []string{"| `pagamento` | Sinal de pagamento | 20% |", "| `distribuicao` | Distribuição automática | 15% |", "tira 15 pontos", "soma 5 pontos", "prospeccao_manual"} {
		if !strings.Contains(sp, want) {
			t.Errorf("prompt de pontuação sem %q", want)
		}
	}
	if strings.Contains(sp, "{{") {
		t.Error("placeholder não substituído no prompt de pontuação")
	}
	mp := ManualPrompt(cfg, "2026-48", []string{"candidatos-1de2.md", "candidatos-2de2.md"})
	for _, want := range []string{"semana 2026-48", "Só responda depois de receber a parte 2 de 2", "Etapa: agrupar dores", "Etapa: pontuar dores", "até 20 dores"} {
		if !strings.Contains(mp, want) {
			t.Errorf("prompt manual sem %q", want)
		}
	}
	if strings.Contains(mp, "{{") {
		t.Error("placeholder não substituído no prompt manual")
	}
}
