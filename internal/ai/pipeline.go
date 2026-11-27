package ai

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gerador-ideias/internal/config"
	"gerador-ideias/internal/fmtx"
)

// ErrBudget: a próxima chamada estouraria o limite semanal de tokens.
var ErrBudget = errors.New("limite semanal de tokens")

// ErrOffline: o OmniRoute não respondeu ao GET /models.
var ErrOffline = errors.New("OmniRoute fora do ar")

// ShortReason resume uma falha da IA para o cabeçalho do relatório.
func ShortReason(err error) string {
	switch {
	case errors.Is(err, ErrOffline):
		return ErrOffline.Error()
	case errors.Is(err, ErrBudget):
		return ErrBudget.Error() + " atingido"
	default:
		return "falha na IA"
	}
}

// Runner executa o modo automático.
type Runner struct {
	DB     *sql.DB
	Client *Client
	Cfg    *config.Config
	Now    func() time.Time
	// Log recebe o progresso (uma linha por chamada). Pode ser nil.
	Log func(format string, args ...any)

	week     string
	clusters []Cluster
	assign   map[int64]string
	created  map[string]NewCluster
	nextRef  int
	Spent    int64 // tokens gastos nesta execução
}

func (r *Runner) logf(format string, args ...any) {
	if r.Log != nil {
		r.Log(format, args...)
	}
}

// Check confirma que o OmniRoute responde (GET /models) e avisa se os
// modelos configurados não aparecem na lista (combos podem não aparecer).
func (r *Runner) Check(ctx context.Context) error {
	ids, err := r.Client.Models(ctx)
	if err != nil {
		return fmt.Errorf("%w em %s: %w", ErrOffline, r.Client.BaseURL, err)
	}
	for _, m := range []string{r.Cfg.AI.GroupModel, r.Cfg.AI.ScoreModel} {
		if !contains(ids, m) {
			r.logf("aviso: o modelo %q não aparece em /models (se for um combo, tudo bem)", m)
		}
	}
	return nil
}

// Run agrupa e pontua a semana e devolve o plano, sem gravar nada.
func (r *Runner) Run(ctx context.Context, w *Week) (*Plan, error) {
	r.week = w.Week
	r.clusters = append([]Cluster(nil), w.Clusters...)
	r.assign = map[int64]string{}
	r.created = map[string]NewCluster{}
	if len(w.Items) == 0 {
		return &Plan{Week: w.Week, New: r.created, Assign: r.assign}, nil
	}

	batch := r.Cfg.AI.BatchSize
	for start := 0; start < len(w.Items); start += batch {
		items := w.Items[start:min(start+batch, len(w.Items))]
		label := fmt.Sprintf("%d de %d", start/batch+1, (len(w.Items)+batch-1)/batch)
		if err := r.groupSplit(ctx, items, label); err != nil {
			return nil, err
		}
	}

	targets := r.topClusters(w.Items)
	var scores []Score
	if len(targets) > 0 {
		var err error
		if scores, err = r.score(ctx, w, targets); err != nil {
			return nil, err
		}
	}
	judged := make([]int64, len(w.Items))
	for i, it := range w.Items {
		judged[i] = it.ID // cada candidato enviado foi julgado: a validação exige que a IA responda sobre todos
	}
	return &Plan{Week: w.Week, New: r.created, Assign: r.assign, Judged: judged, Scores: scores}, nil
}

// groupSplit agrupa um lote; se a resposta vier cortada pelo limite de
// saída, divide o lote ao meio e tenta cada metade (até 1 item).
func (r *Runner) groupSplit(ctx context.Context, items []Item, label string) error {
	err := r.groupBatch(ctx, items, label)
	if !errors.Is(err, ErrTruncated) || len(items) < 2 {
		return err
	}
	half := len(items) / 2
	r.logf("agrupamento: lote %s cortado pelo limite de saída; dividindo (%d + %d)", label, half, len(items)-half)
	if err := r.groupSplit(ctx, items[:half], label+"a"); err != nil {
		return err
	}
	return r.groupSplit(ctx, items[half:], label+"b")
}

func (r *Runner) groupBatch(ctx context.Context, items []Item, label string) error {
	var user strings.Builder
	fmt.Fprintf(&user, "Semana %s · lote %s.\n\n", r.week, label)
	shown := r.clusters
	if limit := r.Cfg.AI.MaxClusters; len(shown) > limit {
		// Os criados nesta execução ficam no fim da lista: mostra sempre os
		// mais recentes.
		shown = shown[len(shown)-limit:]
	}
	user.WriteString(ClustersBlock(shown))
	user.WriteString("\n## Candidatos\n\n")
	vctx := Context{Items: map[string]bool{}, Clusters: map[string]bool{}}
	for _, it := range items {
		user.WriteString(ItemBlock(it, r.Cfg.AI.ItemChars))
		vctx.Items[it.Ref()] = true
	}
	for _, c := range shown {
		vctx.Clusters[c.Ref] = true
	}

	resp, err := r.chatJSON(ctx, "group", r.Cfg.AI.GroupModel, GroupPrompt(), user.String(), r.Cfg.AI.MaxOutputTokens,
		func(resp *Response) error { return ValidateGrouping(resp, vctx, true) })
	if err != nil {
		return fmt.Errorf("agrupamento (lote %s): %w", label, err)
	}

	// "novoN" desta resposta vira "nK", único na execução, e passa a ser um
	// cluster existente para os próximos lotes.
	local := map[string]string{}
	for _, c := range resp.NovosClusters {
		r.nextRef++
		ref := fmt.Sprintf("n%d", r.nextRef)
		local[c.Ref] = ref
		r.created[ref] = c
		r.clusters = append(r.clusters, Cluster{Ref: ref, Dor: c.Dor, Nicho: c.Nicho, Resumo: c.Resumo})
	}
	ids := map[string]int64{}
	for _, it := range items {
		ids[it.Ref()] = it.ID
	}
	for _, a := range resp.Atribuicoes {
		if a.Cluster == nil {
			continue
		}
		ref := *a.Cluster
		if g, ok := local[ref]; ok {
			ref = g
		}
		r.assign[ids[a.Item]] = ref
	}
	r.logf("agrupamento: lote %s ok (%d novos clusters)", label, len(resp.NovosClusters))
	return nil
}

// clusterStats resume os candidatos da semana de um cluster.
type clusterStats struct {
	ref      string
	mentions int
	score    float64
	items    []Item
}

// topClusters escolhe as dores mais relevantes da semana: mais candidatos,
// depois maior soma de pain_score.
func (r *Runner) topClusters(items []Item) []*clusterStats {
	by := map[string]*clusterStats{}
	for _, it := range items {
		ref, ok := r.assign[it.ID]
		if !ok {
			continue
		}
		s := by[ref]
		if s == nil {
			s = &clusterStats{ref: ref}
			by[ref] = s
		}
		s.mentions++
		s.score += it.Score
		s.items = append(s.items, it)
	}
	list := make([]*clusterStats, 0, len(by))
	for _, s := range by {
		list = append(list, s)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].mentions != list[j].mentions {
			return list[i].mentions > list[j].mentions
		}
		if list[i].score != list[j].score {
			return list[i].score > list[j].score
		}
		return list[i].ref < list[j].ref
	})
	return list[:min(len(list), r.Cfg.AI.ScoreTop)]
}

func (r *Runner) score(ctx context.Context, w *Week, targets []*clusterStats) ([]Score, error) {
	byRef := map[string]Cluster{}
	for _, c := range r.clusters {
		byRef[c.Ref] = c
	}
	// Em lotes: cada dor gera uma resposta longa (notas, canal, MVP, riscos)
	// e 20 de uma vez estouram o limite de saída de muitos modelos.
	size := r.Cfg.AI.ScoreBatchSize
	total := (len(targets) + size - 1) / size
	var scores []Score
	for n := 0; n*size < len(targets); n++ {
		batch := targets[n*size : min((n+1)*size, len(targets))]
		got, err := r.scoreSplit(ctx, w.Week, batch, byRef, fmt.Sprintf("%d de %d", n+1, total))
		if err != nil {
			return nil, err
		}
		scores = append(scores, got...)
	}
	return scores, nil
}

// scoreSplit pontua um lote; se a resposta vier cortada pelo limite de
// saída, divide o lote ao meio e tenta cada metade (até 1 dor).
func (r *Runner) scoreSplit(ctx context.Context, wk string, batch []*clusterStats, byRef map[string]Cluster, label string) ([]Score, error) {
	var user strings.Builder
	fmt.Fprintf(&user, "Semana %s. Dores a pontuar (%d):\n\n", wk, len(batch))
	valid := map[string]bool{}
	for _, t := range batch {
		valid[t.ref] = true
		user.WriteString(ScoreBlock(byRef[t.ref], t.items, r.Cfg.AI.ItemChars))
	}
	resp, err := r.chatJSON(ctx, "score", r.Cfg.AI.ScoreModel, ScorePrompt(r.Cfg), user.String(), r.Cfg.AI.MaxOutputTokens,
		func(resp *Response) error { return ValidateScores(resp, valid) })
	if errors.Is(err, ErrTruncated) && len(batch) > 1 {
		half := len(batch) / 2
		r.logf("pontuação: lote %s cortado pelo limite de saída; dividindo (%d + %d)", label, half, len(batch)-half)
		first, err := r.scoreSplit(ctx, wk, batch[:half], byRef, label+"a")
		if err != nil {
			return nil, err
		}
		second, err := r.scoreSplit(ctx, wk, batch[half:], byRef, label+"b")
		return append(first, second...), err
	}
	if err != nil {
		return nil, fmt.Errorf("pontuação (lote %s): %w", label, err)
	}
	r.logf("pontuação: lote %s ok (%d dores)", label, len(resp.Pontuacoes))
	return resp.Pontuacoes, nil
}

// ScoreBlock descreve uma dor para a pontuação: dados agregados da semana,
// histórico e algumas amostras de texto.
func ScoreBlock(c Cluster, items []Item, maxChars int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### %s — %s (nicho: %s)\n", c.Ref, c.Dor, c.Nicho)
	if c.Resumo != "" {
		fmt.Fprintf(&b, "Resumo: %s\n", c.Resumo)
	}
	sources, langs, brands, signals := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	for _, it := range items {
		sources[sourceNames[it.Source]]++
		langs[strings.ToUpper(it.Lang)]++
		for _, br := range it.Brands {
			brands[br]++
		}
		for _, s := range it.Signals {
			signals[s]++
		}
	}
	fmt.Fprintf(&b, "Menções nesta semana: %d · antes desta semana: %d em %d semana(s)\n", len(items), c.Mentions, c.Weeks)
	fmt.Fprintf(&b, "Fontes: %s · Idiomas: %s\n", counted(sources), counted(langs))
	if len(brands) > 0 {
		fmt.Fprintf(&b, "Marcas citadas: %s\n", counted(brands))
	}
	if len(signals) > 0 {
		fmt.Fprintf(&b, "Sinais: %s\n", counted(signals))
	}
	sorted := append([]Item(nil), items...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Score > sorted[j].Score })
	b.WriteString("Amostras:\n")
	for _, it := range sorted[:min(len(sorted), 5)] {
		fmt.Fprintf(&b, "- [%s, score %s] %s\n", sourceNames[it.Source], fmtx.Decimal(it.Score, 1),
			cut(strings.Join(strings.Fields(it.Text), " "), maxChars*6/10))
	}
	b.WriteString("\n")
	return b.String()
}

func counted(m map[string]int) string {
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
	parts := make([]string, 0, len(keys))
	for _, k := range keys[:min(len(keys), 8)] {
		parts = append(parts, fmt.Sprintf("%s %d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

// chatJSON chama o modelo, extrai e valida o JSON. Se vier inválido, pede a
// correção uma vez, mostrando os erros. Antes de cada chamada, confere se o
// custo estimado cabe no limite semanal.
func (r *Runner) chatJSON(ctx context.Context, stage, model, system, user string, maxTokens int,
	validate func(*Response) error) (*Response, error) {
	msgs := []Message{{Role: "system", Content: system}, {Role: "user", Content: user}}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if err := r.checkBudget(ctx, msgs, maxTokens); err != nil {
			return nil, err
		}
		res, err := r.Client.Chat(ctx, model, msgs, maxTokens)
		if res != nil {
			if rerr := r.record(ctx, stage, res); rerr != nil {
				return nil, rerr
			}
		}
		if err != nil {
			return nil, err
		}
		resp, err := Parse([]byte(res.Content))
		if err == nil {
			err = validate(resp)
		}
		if err == nil {
			return resp, nil
		}
		lastErr = err
		r.logf("%s: resposta inválida, pedindo correção: %v", stage, firstLine(err.Error()))
		msgs = append(msgs,
			Message{Role: "assistant", Content: res.Content},
			Message{Role: "user", Content: "Sua resposta não pôde ser usada:\n" + err.Error() +
				"\n\nResponda de novo, só com o objeto JSON corrigido, no formato pedido."})
	}
	return nil, fmt.Errorf("JSON inválido mesmo depois de pedir correção: %w", lastErr)
}

// WeekUsage soma os tokens gastos na semana ISO de now.
func WeekUsage(ctx context.Context, conn *sql.DB, now time.Time) (int64, error) {
	y, wk := now.UTC().ISOWeek()
	jan4 := time.Date(y, 1, 4, 0, 0, 0, 0, time.UTC)
	start := jan4.AddDate(0, 0, -((int(jan4.Weekday())+6)%7)+(wk-1)*7)
	var used int64
	err := conn.QueryRowContext(ctx, `SELECT coalesce(sum(total_tokens), 0) FROM ai_usage WHERE created_at >= ?`,
		start.Unix()).Scan(&used)
	return used, err
}

// estimateTokens: ~4 caracteres por token, mais o máximo de saída.
func estimateTokens(msgs []Message, maxTokens int) int64 {
	n := 0
	for _, m := range msgs {
		n += len([]rune(m.Content))
	}
	return int64(n/4 + maxTokens)
}

func (r *Runner) checkBudget(ctx context.Context, msgs []Message, maxTokens int) error {
	used, err := WeekUsage(ctx, r.DB, r.Now())
	if err != nil {
		return err
	}
	need := estimateTokens(msgs, maxTokens)
	if limit := r.Cfg.AI.WeeklyTokenLimit; used+need > limit {
		return fmt.Errorf("%w: já usados %s, a próxima chamada pode gastar até %s, limite %s",
			ErrBudget, fmtx.Int(used), fmtx.Int(need), fmtx.Int(limit))
	}
	return nil
}

func (r *Runner) record(ctx context.Context, stage string, res *ChatResult) error {
	u := res.Usage
	if u.TotalTokens == 0 {
		u.TotalTokens = u.PromptTokens + u.CompletionTokens
	}
	r.Spent += u.TotalTokens
	_, err := r.DB.ExecContext(ctx, `INSERT INTO ai_usage (created_at, report_week, stage, mode, model, provider,
		prompt_tokens, completion_tokens, total_tokens) VALUES (?, ?, ?, 'omniroute', ?, ?, ?, ?, ?)`,
		r.Now().Unix(), r.week, stage, res.Model, res.Provider, u.PromptTokens, u.CompletionTokens, u.TotalTokens)
	return err
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
