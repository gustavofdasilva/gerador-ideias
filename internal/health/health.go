// Package health detecta problemas na coleta e avisa pelo Telegram (design
// 5.7, roadmap F6): fonte falhando dias seguidos ou nenhuma coleta recente.
package health

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Alert é um problema ativo. Key identifica o problema entre execuções,
// para não repetir o aviso a cada verificação.
type Alert struct {
	Key     string
	Message string
}

// Options controla a avaliação.
type Options struct {
	Sources     []string
	FailingDays int
	StaleHours  int
	Now         time.Time
}

var sourceNames = map[string]string{"hn": "HN", "bsky": "Bluesky", "gh": "GitHub"}

// Evaluate devolve os problemas ativos.
func Evaluate(ctx context.Context, conn *sql.DB, o Options) ([]Alert, error) {
	var alerts []Alert
	a, err := stale(ctx, conn, o)
	if err != nil {
		return nil, err
	}
	alerts = append(alerts, a...)
	for _, src := range o.Sources {
		a, err := failing(ctx, conn, src, o)
		if err != nil {
			return nil, err
		}
		alerts = append(alerts, a...)
	}
	return alerts, nil
}

func stale(ctx context.Context, conn *sql.DB, o Options) ([]Alert, error) {
	var last sql.NullInt64
	var runs int
	if err := conn.QueryRowContext(ctx, `SELECT max(CASE WHEN status IN ('ok', 'partial') THEN started_at END), count(*)
		FROM runs WHERE command = 'collect'`).Scan(&last, &runs); err != nil {
		return nil, err
	}
	if runs == 0 {
		return nil, nil // ainda não começou a coletar
	}
	if !last.Valid {
		return []Alert{{Key: "stale", Message: "Nenhuma coleta deu certo até agora: confira o log em ~/.local/state/ideias/."}}, nil
	}
	when := time.Unix(last.Int64, 0)
	if h := o.Now.Sub(when).Hours(); h > float64(o.StaleHours) {
		return []Alert{{Key: "stale", Message: fmt.Sprintf(
			"Nenhuma coleta há %.0f h (última bem-sucedida: %s). O celular pode ter ficado desligado ou o cron parou; a próxima coleta recupera o período.",
			h, when.Local().Format("02/01 15:04"))}}, nil
	}
	return nil, nil
}

// failing: a fonte não teve nenhuma execução ok/partial em cada um dos
// FailingDays dias mais recentes em que rodou.
func failing(ctx context.Context, conn *sql.DB, src string, o Options) ([]Alert, error) {
	rows, err := conn.QueryContext(ctx, `SELECT started_at, status, error_text FROM runs
		WHERE command = 'collect' AND source = ? ORDER BY started_at DESC, id DESC LIMIT 200`, src)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	okByDay := map[string]bool{}
	var days []string
	lastErr := ""
	for rows.Next() {
		var started int64
		var status, text string
		if err := rows.Scan(&started, &status, &text); err != nil {
			return nil, err
		}
		day := time.Unix(started, 0).Local().Format("2006-01-02")
		if _, seen := okByDay[day]; !seen {
			okByDay[day] = false
			days = append(days, day)
		}
		if status == "ok" || status == "partial" {
			okByDay[day] = true
		}
		if lastErr == "" && text != "" {
			lastErr = text
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Sort(sort.Reverse(sort.StringSlice(days)))
	if len(days) < o.FailingDays {
		return nil, nil
	}
	for _, d := range days[:o.FailingDays] {
		if okByDay[d] {
			return nil, nil
		}
	}
	name := sourceNames[src]
	if name == "" {
		name = src
	}
	msg := fmt.Sprintf("%s falhou em todas as coletas dos últimos %d dias.", name, o.FailingDays)
	if lastErr != "" {
		msg += " Último erro: " + firstLine(lastErr, 300)
	}
	return []Alert{{Key: "failing:" + src, Message: msg}}, nil
}

// Notifier manda as mensagens (o Telegram, em produção).
type Notifier func(ctx context.Context, text string) error

type sentAlert struct {
	Sent    int64  `json:"sent"`
	Message string `json:"message"`
}

const metaPrefix = "alert:"

// Notify envia os alertas novos ou cujo último aviso tem mais de repeat, e
// uma mensagem de "resolvido" para os que deixaram de existir. Devolve
// quantas mensagens foram enviadas.
func Notify(ctx context.Context, conn *sql.DB, alerts []Alert, send Notifier, now time.Time, repeat time.Duration) (int, error) {
	prev, err := loadSent(ctx, conn)
	if err != nil {
		return 0, err
	}
	active := map[string]bool{}
	var due []Alert
	for _, a := range alerts {
		active[a.Key] = true
		if p, ok := prev[a.Key]; ok && now.Sub(time.Unix(p.Sent, 0)) < repeat {
			continue
		}
		due = append(due, a)
	}
	var resolved []string
	for key, p := range prev {
		if !active[key] {
			resolved = append(resolved, firstLine(p.Message, 200))
		}
	}
	sort.Strings(resolved)

	sent := 0
	if len(due) > 0 {
		var b strings.Builder
		b.WriteString("⚠️ ideias: problema na coleta\n")
		for _, a := range due {
			b.WriteString("\n• " + a.Message + "\n")
		}
		if err := send(ctx, b.String()); err != nil {
			return sent, err
		}
		sent++
		for _, a := range due {
			if err := saveSent(ctx, conn, a, now); err != nil {
				return sent, err
			}
		}
	}
	if len(resolved) > 0 {
		msg := "✅ ideias: resolvido\n\n• " + strings.Join(resolved, "\n• ")
		if err := send(ctx, msg); err != nil {
			return sent, err
		}
		sent++
		for key := range prev {
			if !active[key] {
				if _, err := conn.ExecContext(ctx, `DELETE FROM meta WHERE key = ?`, metaPrefix+key); err != nil {
					return sent, err
				}
			}
		}
	}
	return sent, nil
}

func loadSent(ctx context.Context, conn *sql.DB) (map[string]sentAlert, error) {
	rows, err := conn.QueryContext(ctx, `SELECT key, value FROM meta WHERE key LIKE 'alert:%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]sentAlert{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		var s sentAlert
		_ = json.Unmarshal([]byte(value), &s)
		out[strings.TrimPrefix(key, metaPrefix)] = s
	}
	return out, rows.Err()
}

func saveSent(ctx context.Context, conn *sql.DB, a Alert, now time.Time) error {
	js, _ := json.Marshal(sentAlert{Sent: now.Unix(), Message: a.Message})
	_, err := conn.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, metaPrefix+a.Key, string(js))
	return err
}

func firstLine(s string, n int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if rs := []rune(s); len(rs) > n {
		return string(rs[:n]) + "…"
	}
	return s
}
