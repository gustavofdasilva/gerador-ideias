package ai

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gerador-ideias/internal/config"
)

// Export gera o pacote do modo manual em dir/AAAA-WW: prompt.md (as duas
// etapas, com os mesmos prompts do modo automático) e candidatos.md
// (dividido em partes numeradas se passar de manual_part_chars). Devolve os
// arquivos gerados, prompt.md primeiro.
func Export(ctx context.Context, conn *sql.DB, cfg *config.Config, week, dir string) ([]string, error) {
	w, err := LoadWeek(ctx, conn, week, cfg.AI.MaxItems, cfg.AI.MaxClusters)
	if err != nil {
		return nil, err
	}
	out := filepath.Join(dir, week)
	if err := os.MkdirAll(out, 0o755); err != nil {
		return nil, err
	}
	// Apaga partes de uma exportação anterior, que podia ter outro número de partes.
	old, _ := filepath.Glob(filepath.Join(out, "candidatos*.md"))
	for _, f := range old {
		os.Remove(f)
	}

	header := fmt.Sprintf("# Candidatos da semana %s\n\n%d candidatos (os de maior pain_score). "+
		"Use as refs `iNNN` dos candidatos e `cNNN` dos clusters na resposta.\n\n", week, len(w.Items))
	chunks := []string{header + ClustersBlock(w.Clusters) + "\n## Candidatos\n\n"}
	limit := cfg.AI.ManualPartChars
	for _, it := range w.Items {
		block := ItemBlock(it, cfg.AI.ItemChars)
		last := len(chunks) - 1
		if len([]rune(chunks[last]))+len([]rune(block)) > limit && len(chunks[last]) > len(header) {
			chunks = append(chunks, "## Candidatos (continuação)\n\n")
			last++
		}
		chunks[last] += block
	}
	if len(w.Items) == 0 {
		chunks[0] += "Nenhum candidato nesta semana.\n"
	}

	names := make([]string, len(chunks))
	for i := range chunks {
		if len(chunks) == 1 {
			names[i] = "candidatos.md"
		} else {
			names[i] = fmt.Sprintf("candidatos-%dde%d.md", i+1, len(chunks))
		}
	}
	files := []string{filepath.Join(out, "prompt.md")}
	if err := os.WriteFile(files[0], []byte(ManualPrompt(cfg, week, names)), 0o644); err != nil {
		return nil, err
	}
	for i, chunk := range chunks {
		if len(chunks) > 1 {
			note := fmt.Sprintf("> **Parte %d de %d.** Ainda não responda: aguarde a parte %d de %d.\n\n", i+1, len(chunks), len(chunks), len(chunks))
			if i == len(chunks)-1 {
				note = fmt.Sprintf("> **Parte %d de %d (a última).** Agora responda seguindo o prompt, com um único JSON.\n\n", i+1, len(chunks))
			}
			chunk = note + chunk
		}
		path := filepath.Join(out, names[i])
		if err := os.WriteFile(path, []byte(chunk), 0o644); err != nil {
			return nil, err
		}
		files = append(files, path)
	}
	return files, nil
}

// ImportResult é o que a importação descobriu, além do plano.
type ImportResult struct {
	Plan       *Plan
	Unassigned int // candidatos que a resposta não mencionou (tratados como descartados)
	Discarded  int // candidatos que a IA marcou como "não é dor" (null)
}

// Import lê a resposta colada do Claude web, valida com o mesmo esquema do
// modo automático e monta o plano. Não grava nada: use Apply.
func Import(ctx context.Context, conn *sql.DB, cfg *config.Config, week string, raw []byte) (*ImportResult, error) {
	resp, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	// Valida contra TODOS os candidatos da semana (não só os exportados): se
	// novos itens chegaram depois do export, a resposta continua válida.
	w, err := LoadWeek(ctx, conn, week, 0, 1_000_000)
	if err != nil {
		return nil, err
	}
	vctx := Context{Items: map[string]bool{}, Clusters: map[string]bool{}}
	ids := map[string]int64{}
	for _, it := range w.Items {
		vctx.Items[it.Ref()] = true
		ids[it.Ref()] = it.ID
	}
	for _, c := range w.Clusters {
		vctx.Clusters[c.Ref] = true
	}

	var problems []string
	if err := ValidateGrouping(resp, vctx, false); err != nil {
		problems = append(problems, err.(*ValidationError).Problems...)
	}
	res := &ImportResult{Plan: &Plan{Week: week, New: map[string]NewCluster{}, Assign: map[int64]string{}}}
	valid := map[string]bool{}
	mentioned := map[string]bool{}
	for _, a := range resp.Atribuicoes {
		mentioned[a.Item] = true
		if a.Cluster == nil {
			res.Discarded++
			continue
		}
		if id, ok := ids[a.Item]; ok {
			res.Plan.Assign[id] = *a.Cluster
			valid[*a.Cluster] = true
		}
	}
	for ref := range vctx.Items {
		if !mentioned[ref] {
			res.Unassigned++
		}
	}
	if err := ValidateScores(resp, valid); err != nil {
		problems = append(problems, err.(*ValidationError).Problems...)
	}
	if len(resp.Pontuacoes) == 0 && len(res.Plan.Assign) > 0 {
		problems = append(problems, "pontuacoes: vazio (a resposta precisa pontuar as dores mais relevantes)")
	}
	if len(problems) > 0 {
		return nil, &ValidationError{Problems: problems}
	}
	for _, c := range resp.NovosClusters {
		res.Plan.New[c.Ref] = c
	}
	res.Plan.Scores = resp.Pontuacoes
	return res, nil
}

// RecordManual registra em ai_usage que a semana foi feita no Claude web
// (sem tokens contados), para a calibração saber quantas semanas foram manuais.
func RecordManual(ctx context.Context, conn *sql.DB, week string, now time.Time) error {
	_, err := conn.ExecContext(ctx, `INSERT INTO ai_usage (created_at, report_week, stage, mode, model)
		VALUES (?, ?, 'import', 'manual', 'claude-web')`, now.Unix(), week)
	return err
}

// ManualHelp explica o passo a passo do modo manual.
func ManualHelp(files []string, week string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Pacote do modo manual em %s:\n", filepath.Dir(files[0]))
	for _, f := range files {
		fmt.Fprintf(&b, "  %s\n", filepath.Base(f))
	}
	fmt.Fprintf(&b, "No claude.ai: cole o conteúdo de prompt.md, anexe os candidatos (na ordem, se houver partes),\n"+
		"salve a resposta num arquivo e rode: ideias import --week %s resposta.json\n", week)
	return b.String()
}
