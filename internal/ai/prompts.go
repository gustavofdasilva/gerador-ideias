package ai

import (
	"fmt"
	"strings"

	"gerador-ideias"
	"gerador-ideias/internal/config"
)

// Linhas da tabela da rubrica (design, seção 6). Os pesos vêm do config.
var rubricRows = []struct {
	key, name, five string
	weight          func(config.Rubric) int
}{
	{"frequencia", "Frequência e tendência da dor", "Aparece toda semana, em mais de uma fonte, e está crescendo", func(r config.Rubric) int { return r.Frequency }},
	{"pagamento", "Sinal de pagamento", "Menções a preço, \"I'd pay\", cancelamento de concorrente pago", func(r config.Rubric) int { return r.Payment }},
	{"distribuicao", "Distribuição automática", "O público acha o produto sozinho: busca a dor no Google (cabe SEO programático), procura num marketplace ou diretório de apps/plugins, ou o produto gera conteúdo ou links compartilháveis que divulgam por si", func(r config.Rubric) int { return r.Distribution }},
	{"b2b", "Público B2B ou prosumer", "Pequenos negócios, freelancers ou times; uso ligado a ganhar dinheiro", func(r config.Rubric) int { return r.B2B }},
	{"global", "Global e em USD", "Dor sem fronteira de idioma ou país, público fora do BR", func(r config.Rubric) int { return r.Global }},
	{"solo", "Construível solo", "MVP em até ~6 semanas a 8–10 h/semana, sem app nativo complexo nem integrações frágeis", func(r config.Rubric) int { return r.Solo }},
	{"vendabilidade", "Vendabilidade", "Pouco suporte, roda sozinho, sem depender do dono nem de uma única plataforma ou API", func(r config.Rubric) int { return r.Sellability }},
	{"infra", "Infraestrutura barata", "Cabe num binário pequeno no Termux ou numa VPS mínima; sem IA ou GPU a cada uso", func(r config.Rubric) int { return r.Infra }},
}

// Weight devolve o peso (%) de um critério.
func Weight(r config.Rubric, key string) int {
	for _, row := range rubricRows {
		if row.key == key {
			return row.weight(r)
		}
	}
	return 0
}

func readPrompt(name string) string {
	data, err := ideias.Prompts.ReadFile("prompts/" + name)
	if err != nil {
		panic(err) // embutido no binário: só falha se o arquivo sumir do repositório
	}
	return string(data)
}

// GroupPrompt é o prompt de sistema do agrupamento.
func GroupPrompt() string { return readPrompt("agrupar.md") }

// ScorePrompt é o prompt de sistema da pontuação, com os pesos do config.
func ScorePrompt(cfg *config.Config) string {
	var rows []string
	for _, r := range rubricRows {
		rows = append(rows, fmt.Sprintf("| `%s` | %s | %d%% | %s |", r.key, r.name, r.weight(cfg.Rubric), r.five))
	}
	return strings.NewReplacer(
		"{{PESOS}}", strings.Join(rows, "\n"),
		"{{PENALIDADE}}", fmt.Sprint(cfg.AI.PenaltyPoints),
		"{{BONUS}}", fmt.Sprint(cfg.AI.BonusPoints),
	).Replace(readPrompt("pontuar.md"))
}

// ManualPrompt é o prompt.md do modo manual: as duas etapas, com os mesmos
// textos do modo automático, e a instrução de responder com um só JSON.
func ManualPrompt(cfg *config.Config, week string, parts []string) string {
	var partes string
	if len(parts) == 1 {
		partes = fmt.Sprintf("Os candidatos e os clusters existentes estão no arquivo anexado **%s**.", parts[0])
	} else {
		partes = fmt.Sprintf("Os candidatos vêm em **%d arquivos** anexados (%s). "+
			"**Só responda depois de receber a parte %d de %d**; antes disso, responda apenas \"ok, aguardando\".",
			len(parts), strings.Join(parts, ", "), len(parts), len(parts))
	}
	return strings.NewReplacer(
		"{{SEMANA}}", week,
		"{{PARTES}}", partes,
		"{{TOP}}", fmt.Sprint(cfg.AI.ScoreTop),
		"{{AGRUPAR}}", GroupPrompt(),
		"{{PONTUAR}}", ScorePrompt(cfg),
	).Replace(readPrompt("manual.md"))
}
