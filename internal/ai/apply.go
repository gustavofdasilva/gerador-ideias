package ai

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"gerador-ideias/internal/config"
)

// Plan é o resultado da IA para uma semana, ainda não gravado. O modo
// automático e o manual produzem o mesmo Plan e gravam com o mesmo Apply.
type Plan struct {
	Week string
	// New: clusters criados, por ref ("n1" no automático, "novo1" no manual).
	New map[string]NewCluster
	// Assign: candidato (id) -> ref do cluster ("c12", "n1"...). Candidatos
	// sem entrada foram descartados pela IA (não são dor).
	Assign map[int64]string
	// Judged: candidatos que a IA julgou (os ligados a uma dor e os
	// descartados com null). Vira items.ai_verdict, base do "calibrate".
	Judged []int64
	Scores []Score
}

// Detail é o que fica gravado em score_detail (JSON) para o relatório.
type Detail struct {
	Score
	Base      int `json:"base"`       // média ponderada das notas, 0 a 100
	Final     int `json:"final"`      // depois de penalidades e bônus
	Penalized int `json:"penalizado"` // pontos tirados
	Bonus     int `json:"bonus"`      // pontos somados
}

// FinalScore calcula a nota de 0 a 100: média ponderada das notas (0 a 5)
// pelos pesos da rubrica, menos as penalidades e mais o bônus.
func FinalScore(s Score, cfg *config.Config) Detail {
	var sum float64
	for _, k := range Criteria {
		sum += float64(Weight(cfg.Rubric, k)) * float64(s.Notas[k]) / 5
	}
	d := Detail{Score: s, Base: int(math.Round(sum))}
	d.Penalized = len(s.Penalidades) * cfg.AI.PenaltyPoints
	if s.BonusConcorrencia.Aplica {
		d.Bonus = cfg.AI.BonusPoints
	}
	d.Final = min(max(d.Base-d.Penalized+d.Bonus, 0), 100)
	return d
}

// Apply grava o plano numa transação: desfaz um agrupamento anterior da
// mesma semana, cria os clusters novos, liga os candidatos, recalcula o
// histórico de menções e grava as notas da semana.
func Apply(ctx context.Context, conn *sql.DB, cfg *config.Config, p *Plan, now time.Time) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // sem efeito depois do Commit
	exec := func(q string, args ...any) error {
		_, err := tx.ExecContext(ctx, q, args...)
		return err
	}

	// 1. Desfaz o agrupamento anterior desta semana (regerar é idempotente).
	steps := []struct {
		q    string
		args []any
	}{
		{`UPDATE items SET cluster_id = NULL, ai_verdict = NULL WHERE week = ?`, []any{p.Week}},
		{`DELETE FROM cluster_weeks WHERE week = ?`, []any{p.Week}},
		{`DELETE FROM clusters WHERE first_seen_week = ? AND id NOT IN (SELECT cluster_id FROM cluster_weeks)`, []any{p.Week}},
	}
	for _, s := range steps {
		if err := exec(s.q, s.args...); err != nil {
			return err
		}
	}

	// 2. Clusters novos.
	ids := map[string]int64{}
	for ref, c := range p.New {
		res, err := tx.ExecContext(ctx, `INSERT INTO clusters (pain, niche, summary, first_seen_week, last_seen_week,
			created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			strings.TrimSpace(c.Dor), strings.TrimSpace(c.Nicho), strings.TrimSpace(c.Resumo), p.Week, p.Week, now.Unix(), now.Unix())
		if err != nil {
			return err
		}
		if ids[ref], err = res.LastInsertId(); err != nil {
			return err
		}
	}
	resolve := func(ref string) (int64, error) {
		if id, ok := ids[ref]; ok {
			return id, nil
		}
		if strings.HasPrefix(ref, "c") {
			if id, err := strconv.ParseInt(ref[1:], 10, 64); err == nil {
				return id, nil
			}
		}
		return 0, fmt.Errorf("cluster %q desconhecido", ref)
	}

	// 3. Liga os candidatos.
	for item, ref := range p.Assign {
		id, err := resolve(ref)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE items SET cluster_id = ? WHERE id = ? AND week = ?`, id, item, p.Week)
		if err != nil {
			return fmt.Errorf("item %d: %w", item, err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return fmt.Errorf("item %d não é da semana %s", item, p.Week)
		}
	}

	// Veredito de cada candidato julgado: 1 = virou dor, 0 = ruído.
	for _, item := range p.Judged {
		verdict := 0
		if _, ok := p.Assign[item]; ok {
			verdict = 1
		}
		if err := exec(`UPDATE items SET ai_verdict = ? WHERE id = ? AND week = ?`, verdict, item, p.Week); err != nil {
			return err
		}
	}

	// 4. Histórico de menções e datas dos clusters.
	if err := exec(`INSERT INTO cluster_weeks (cluster_id, week, mentions)
		SELECT cluster_id, week, count(*) FROM items WHERE week = ? AND cluster_id IS NOT NULL
		GROUP BY cluster_id`, p.Week); err != nil {
		return err
	}
	if err := exec(`UPDATE clusters SET
		total_mentions = (SELECT coalesce(sum(mentions), 0) FROM cluster_weeks WHERE cluster_id = clusters.id),
		first_seen_week = coalesce((SELECT min(week) FROM cluster_weeks WHERE cluster_id = clusters.id), first_seen_week),
		last_seen_week = coalesce((SELECT max(week) FROM cluster_weeks WHERE cluster_id = clusters.id), last_seen_week),
		updated_at = ?
		WHERE id IN (SELECT cluster_id FROM cluster_weeks WHERE week = ?)`, now.Unix(), p.Week); err != nil {
		return err
	}

	// 5. Notas da semana; a nota "atual" do cluster só muda se esta for a
	// semana pontuada mais recente.
	for _, s := range p.Scores {
		id, err := resolve(s.Cluster)
		if err != nil {
			return err
		}
		d := FinalScore(s, cfg)
		js, err := json.Marshal(d)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE cluster_weeks SET score = ?, score_detail = ? WHERE cluster_id = ? AND week = ?`,
			d.Final, string(js), id, p.Week)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return fmt.Errorf("pontuação de %s, que não tem candidatos na semana %s", s.Cluster, p.Week)
		}
		if err := exec(`UPDATE clusters SET last_score = ?, last_score_week = ?, last_score_detail = ?
			WHERE id = ? AND (last_score_week IS NULL OR last_score_week <= ?)`,
			d.Final, p.Week, string(js), id, p.Week); err != nil {
			return err
		}
	}
	return tx.Commit()
}
