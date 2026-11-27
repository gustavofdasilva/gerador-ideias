package ai

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"gerador-ideias/internal/db"
	"gerador-ideias/internal/week"
)

var now = time.Date(2026, 11, 29, 12, 0, 0, 0, time.UTC) // domingo da semana 2026-48

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if _, err := db.Migrate(context.Background(), conn); err != nil {
		t.Fatal(err)
	}
	return conn
}

// seedItems cria candidatos numa semana: textos com "invoice" e "notion"
// formam duas dores; o resto é ruído.
func seedItems(t *testing.T, conn *sql.DB, wk string, n int) {
	t.Helper()
	start, _ := week.Start(wk)
	for i := 0; i < n; i++ {
		var text, lang, src string
		switch i % 3 {
		case 0:
			text, lang, src = fmt.Sprintf("I would pay for an invoice tool with multiple currencies #%d", i), "en", "hn"
		case 1:
			text, lang, src = fmt.Sprintf("Alguém conhece uma alternativa ao Notion mais leve? #%d", i), "pt", "bsky"
		default:
			text, lang, src = fmt.Sprintf("what you are willing to pay for a car #%d", i), "en", "hn"
		}
		at := start.Add(time.Duration(i) * time.Hour)
		if _, err := conn.Exec(`INSERT INTO items (source, external_id, url, created_at, week, collected_at, text,
			lang, country_est, pain_score, signals, brands) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'desconhecido', ?, 'payment', '')`,
			src, fmt.Sprintf("%s-%d", wk, i), fmt.Sprintf("https://x/%s/%d", wk, i), at.Unix(), wk, at.Unix(),
			text, lang, float64(10-i%10)); err != nil {
			t.Fatal(err)
		}
	}
}

// fakeLLM imita um endpoint compatível com OpenAI. Agrupa por palavra-chave e
// pontua com notas fixas; pode devolver JSON inválido uma vez.
type fakeLLM struct {
	t           *testing.T
	mu          sync.Mutex
	calls       map[string]int // "group" / "score"
	invalidOnce bool
	down        bool
	failChat    bool
	prompts     []string
	// cutAbove > 0: respostas para mais itens (ou dores) que isso vêm
	// cortadas pelo limite de saída, como um modelo que "pensa" demais.
	cutAbove int
}

var (
	reItemRef    = regexp.MustCompile(`(?m)^### (i\d+) · .*\n(?:.*\n)?(.*)$`)
	reClusterRef = regexp.MustCompile(`(?m)^- ([cn]\d+) · dor: ([^·]+)`)
	reScoreRef   = regexp.MustCompile(`(?m)^### ([cn]\d+) — `)
)

func (f *fakeLLM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		http.Error(w, "connection refused", http.StatusBadGateway)
		return
	}
	switch r.URL.Path {
	case "/v1/models":
		w.Write([]byte(`{"object":"list","data":[{"id":"combo-agrupar"},{"id":"combo-pontuar"}]}`))
		return
	case "/v1/chat/completions":
	default:
		http.NotFound(w, r)
		return
	}
	if f.failChat {
		http.Error(w, "upstream quota exceeded", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Model    string    `json:"model"`
		Messages []Message `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		f.t.Fatal(err)
	}
	system, user := req.Messages[0].Content, req.Messages[1].Content
	f.prompts = append(f.prompts, user)
	var content string
	switch {
	case strings.Contains(system, "Etapa: agrupar"):
		f.calls["group"]++
		if f.invalidOnce && len(req.Messages) == 2 {
			f.invalidOnce = false
			content = "Claro! Aqui está: {\"atribuicoes\": [{\"item\": \"i999\", \"cluster\": null}]}"
			break
		}
		content = groupAnswer(user)
	case strings.Contains(system, "Etapa: pontuar"):
		f.calls["score"]++
		var scores []Score
		for _, m := range reScoreRef.FindAllStringSubmatch(user, -1) {
			s := goodScore(m[1])
			if strings.Contains(user, m[1]+" — Alternativa ao Notion") {
				s.Penalidades = []Penalty{{Tipo: "prospeccao_manual", Motivo: "times grandes compram por demo"}}
			}
			scores = append(scores, s)
		}
		b, _ := json.Marshal(map[string]any{"pontuacoes": scores})
		content = "```json\n" + string(b) + "\n```"
	default:
		f.t.Fatalf("prompt desconhecido: %.80s", system)
	}
	finish := "stop"
	if n := len(reItemRef.FindAllString(user, -1)) + len(reScoreRef.FindAllString(user, -1)); f.cutAbove > 0 && n > f.cutAbove {
		finish, content = "length", content[:len(content)/2]
	}
	json.NewEncoder(w).Encode(map[string]any{
		"model":   req.Model + "-real",
		"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": content}, "finish_reason": finish}},
		"usage":   map[string]int{"prompt_tokens": 1000, "completion_tokens": 200, "total_tokens": 1200},
	})
}

// groupAnswer reaproveita clusters existentes com a mesma dor; senão cria.
func groupAnswer(user string) string {
	existing := map[string]string{}
	for _, m := range reClusterRef.FindAllStringSubmatch(user, -1) {
		existing[strings.TrimSpace(m[2])] = m[1]
	}
	resp := Response{NovosClusters: []NewCluster{}, Atribuicoes: []Assignment{}}
	refFor := func(dor, nicho, local string) string {
		if ref, ok := existing[dor]; ok {
			return ref
		}
		for _, c := range resp.NovosClusters {
			if c.Dor == dor {
				return c.Ref
			}
		}
		resp.NovosClusters = append(resp.NovosClusters, NewCluster{Ref: local, Dor: dor, Nicho: nicho, Resumo: "resumo de " + dor})
		return local
	}
	for _, m := range reItemRef.FindAllStringSubmatch(user, -1) {
		text := strings.ToLower(m[2])
		a := Assignment{Item: m[1]}
		switch {
		case strings.Contains(text, "invoice"):
			ref := refFor("Faturas em várias moedas", "faturamento p/ freelancers", "novo1")
			a.Cluster = &ref
		case strings.Contains(text, "notion"):
			ref := refFor("Alternativa ao Notion", "notas p/ times pequenos", "novo2")
			a.Cluster = &ref
		}
		resp.Atribuicoes = append(resp.Atribuicoes, a)
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

func newRunner(t *testing.T, conn *sql.DB, f *fakeLLM) *Runner {
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL+"/v1", "", 5*time.Second)
	c.HTTP.MinInterval = 0
	c.HTTP.MaxRetries = 0
	c.HTTP.Sleep = func(ctx context.Context, d time.Duration) error { return ctx.Err() }
	cfg := exampleConfig(t)
	cfg.AI.BatchSize = 4
	return &Runner{DB: conn, Client: c, Cfg: cfg, Now: func() time.Time { return now }}
}

func runWeek(t *testing.T, r *Runner, wk string) (*Plan, error) {
	t.Helper()
	ctx := context.Background()
	if err := r.Check(ctx); err != nil {
		return nil, err
	}
	w, err := LoadWeek(ctx, r.DB, wk, r.Cfg.AI.MaxItems, r.Cfg.AI.MaxClusters)
	if err != nil {
		t.Fatal(err)
	}
	return r.Run(ctx, w)
}

func q(t *testing.T, conn *sql.DB, query string, args ...any) string {
	t.Helper()
	var s sql.NullString
	if err := conn.QueryRow(query, args...).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s.String
}

func TestAutomaticModeEndToEnd(t *testing.T) {
	conn := openDB(t)
	seedItems(t, conn, "2026-47", 6)
	seedItems(t, conn, "2026-48", 12)
	f := &fakeLLM{t: t, calls: map[string]int{}, invalidOnce: true}
	r := newRunner(t, conn, f)
	ctx := context.Background()

	// Semana anterior cria os dois clusters.
	r.Now = func() time.Time { return now.AddDate(0, 0, -7) }
	plan, err := runWeek(t, r, "2026-47")
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(ctx, conn, r.Cfg, plan, now.AddDate(0, 0, -7)); err != nil {
		t.Fatal(err)
	}
	if got := q(t, conn, `SELECT count(*) FROM clusters`); got != "2" {
		t.Fatalf("clusters = %s", got)
	}

	// Semana do relatório: 12 candidatos em lotes de 4, os clusters são
	// reaproveitados (nenhum novo), o ruído é descartado.
	r.Now = func() time.Time { return now }
	plan, err = runWeek(t, r, "2026-48")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.New) != 0 || len(plan.Assign) != 8 || len(plan.Scores) != 2 {
		t.Fatalf("plano: novos=%d atribuídos=%d notas=%d", len(plan.New), len(plan.Assign), len(plan.Scores))
	}
	if err := Apply(ctx, conn, r.Cfg, plan, now); err != nil {
		t.Fatal(err)
	}
	if got := q(t, conn, `SELECT group_concat(pain || ':' || total_mentions, ',') FROM (SELECT * FROM clusters ORDER BY pain)`); got != "Alternativa ao Notion:6,Faturas em várias moedas:6" {
		t.Errorf("clusters = %s", got)
	}
	if got := q(t, conn, `SELECT count(*) FROM items WHERE week = '2026-48' AND cluster_id IS NULL`); got != "4" {
		t.Errorf("ruído não descartado: %s itens sem cluster", got)
	}
	// Penalidade de prospecção manual tira 15 pontos.
	fat := q(t, conn, `SELECT score FROM cluster_weeks cw JOIN clusters c ON c.id = cw.cluster_id WHERE week = '2026-48' AND pain LIKE 'Faturas%'`)
	notion := q(t, conn, `SELECT score FROM cluster_weeks cw JOIN clusters c ON c.id = cw.cluster_id WHERE week = '2026-48' AND pain LIKE 'Alternativa%'`)
	if fat != "82" || notion != "67" { // base 82 (ver TestFinalScore); −15 pela penalidade
		t.Errorf("notas: faturas=%s notion=%s", fat, notion)
	}
	if d := q(t, conn, `SELECT score_detail FROM cluster_weeks WHERE score = 67`); !strings.Contains(d, "prospeccao_manual") || !strings.Contains(d, `"final":67`) {
		t.Errorf("detalhe: %s", d)
	}

	// Uso registrado: a 1ª resposta veio inválida e foi corrigida.
	if f.calls["group"] != 2+3+1 || f.calls["score"] != 2 {
		t.Errorf("chamadas = %v", f.calls)
	}
	if got := q(t, conn, `SELECT count(*) || '/' || sum(total_tokens) || '/' || min(model) FROM ai_usage WHERE mode = 'omniroute'`); got != "8/9600/combo-agrupar-real" {
		t.Errorf("ai_usage = %s", got)
	}

	// Regerar a mesma semana não duplica nada.
	plan, err = runWeek(t, r, "2026-48")
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(ctx, conn, r.Cfg, plan, now); err != nil {
		t.Fatal(err)
	}
	if got := q(t, conn, `SELECT count(*) || '/' || sum(total_mentions) FROM clusters`); got != "2/12" {
		t.Errorf("depois de regerar: %s", got)
	}
}

func TestNewClustersCarryAcrossBatches(t *testing.T) {
	conn := openDB(t)
	seedItems(t, conn, "2026-48", 12)
	f := &fakeLLM{t: t, calls: map[string]int{}}
	r := newRunner(t, conn, f)
	plan, err := runWeek(t, r, "2026-48")
	if err != nil {
		t.Fatal(err)
	}
	// 3 lotes, mas só 2 clusters: os criados no 1º lote aparecem como
	// existentes ("n1", "n2") nos seguintes.
	if len(plan.New) != 2 || !strings.Contains(f.prompts[1], "- n1 · dor: Faturas em várias moedas") {
		t.Errorf("novos=%v; prompt do 2º lote:\n%s", plan.New, f.prompts[1])
	}
}

func TestFailuresAndBudget(t *testing.T) {
	conn := openDB(t)
	seedItems(t, conn, "2026-48", 6)

	f := &fakeLLM{t: t, calls: map[string]int{}, down: true}
	if _, err := runWeek(t, newRunner(t, conn, f), "2026-48"); err == nil || !strings.Contains(err.Error(), "OmniRoute fora do ar") {
		t.Errorf("fora do ar: %v", err)
	}

	f = &fakeLLM{t: t, calls: map[string]int{}, failChat: true}
	if _, err := runWeek(t, newRunner(t, conn, f), "2026-48"); err == nil || !strings.Contains(err.Error(), "agrupamento (lote 1") {
		t.Errorf("chamada falhando: %v", err)
	}

	// JSON inválido duas vezes: desiste.
	f = &fakeLLM{t: t, calls: map[string]int{}}
	r := newRunner(t, conn, f)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "/models") {
			w.Write([]byte(`{"data":[]}`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"não sei"},"finish_reason":"stop"}],"usage":{"total_tokens":10}}`))
	}))
	defer srv.Close()
	r.Client.BaseURL = srv.URL
	if _, err := runWeek(t, r, "2026-48"); err == nil || !strings.Contains(err.Error(), "mesmo depois de pedir correção") {
		t.Errorf("inválido duas vezes: %v", err)
	}

	// Limite de tokens: a semana já gastou quase tudo.
	f = &fakeLLM{t: t, calls: map[string]int{}}
	r = newRunner(t, conn, f)
	if _, err := conn.Exec(`INSERT INTO ai_usage (created_at, report_week, stage, total_tokens) VALUES (?, '2026-48', 'group', ?)`,
		now.Add(-time.Hour).Unix(), r.Cfg.AI.WeeklyTokenLimit-100); err != nil {
		t.Fatal(err)
	}
	_, err := runWeek(t, r, "2026-48")
	if !errors.Is(err, ErrBudget) || f.calls["group"] != 0 {
		t.Errorf("limite: err=%v chamadas=%v", err, f.calls)
	}
}

func TestManualRoundTrip(t *testing.T) {
	conn := openDB(t)
	seedItems(t, conn, "2026-48", 9)
	cfg := exampleConfig(t)
	cfg.AI.ManualPartChars = 10000
	ctx := context.Background()
	dir := t.TempDir()

	// Força a divisão em partes com textos longos.
	if _, err := conn.Exec(`UPDATE items SET text = text || ' ' || ?`, strings.Repeat("detalhe ", 400)); err != nil {
		t.Fatal(err)
	}
	cfg.AI.ItemChars = 3000
	files, err := Export(ctx, conn, cfg, "2026-48", dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 3 || filepath.Base(files[0]) != "prompt.md" || !strings.Contains(filepath.Base(files[1]), "candidatos-1de") {
		t.Fatalf("arquivos = %v", files)
	}
	prompt, _ := os.ReadFile(files[0])
	last, _ := os.ReadFile(files[len(files)-1])
	first, _ := os.ReadFile(files[1])
	if !strings.Contains(string(prompt), "Só responda depois de receber a parte") ||
		!strings.Contains(string(first), "Ainda não responda") || !strings.Contains(string(last), "(a última)") {
		t.Error("instruções de partes ausentes")
	}
	var all strings.Builder
	for _, f := range files[1:] {
		b, _ := os.ReadFile(f)
		all.Write(b)
	}

	// "Resposta do Claude web": agrupamento + pontuação num JSON só, com
	// texto em volta e cerca de código.
	g := groupAnswer(all.String())
	var resp Response
	json.Unmarshal([]byte(g), &resp)
	resp.Pontuacoes = []Score{goodScore("novo1"), goodScore("novo2")}
	b, _ := json.MarshalIndent(resp, "", "  ")
	answer := "Pronto! Segue a análise:\n\n```json\n" + string(b) + "\n```\n\nSe quiser, posso detalhar."

	res, err := Import(ctx, conn, cfg, "2026-48", []byte(answer))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Plan.Assign) != 6 || res.Discarded != 3 || res.Unassigned != 0 {
		t.Errorf("import: %+v", res)
	}
	if err := Apply(ctx, conn, cfg, res.Plan, now); err != nil {
		t.Fatal(err)
	}
	if err := RecordManual(ctx, conn, "2026-48", now); err != nil {
		t.Fatal(err)
	}
	if got := q(t, conn, `SELECT count(*) FROM cluster_weeks WHERE week = '2026-48' AND score = 82`); got != "2" {
		t.Errorf("notas gravadas = %s", got)
	}

	// Erros claros: ref inexistente e nota fora da faixa.
	resp.Pontuacoes[0].Notas["solo"] = 9
	resp.Atribuicoes[0].Cluster = str("c999")
	b, _ = json.Marshal(resp)
	_, err = Import(ctx, conn, cfg, "2026-48", b)
	var ve *ValidationError
	if !errors.As(err, &ve) || !strings.Contains(err.Error(), `"c999" não existe`) || !strings.Contains(err.Error(), "notas.solo = 9") {
		t.Errorf("erros de validação: %v", err)
	}
}

func TestScoringInBatches(t *testing.T) {
	conn := openDB(t)
	seedItems(t, conn, "2026-48", 12)
	f := &fakeLLM{t: t, calls: map[string]int{}}
	r := newRunner(t, conn, f)
	r.Cfg.AI.ScoreBatchSize = 1 // 2 dores → 2 chamadas de pontuação
	plan, err := runWeek(t, r, "2026-48")
	if err != nil {
		t.Fatal(err)
	}
	if f.calls["score"] != 2 || len(plan.Scores) != 2 {
		t.Errorf("chamadas de pontuação = %d, notas = %d", f.calls["score"], len(plan.Scores))
	}
	// Cada chamada só recebe a sua dor.
	for _, p := range f.prompts {
		if strings.Contains(p, "Dores a pontuar") && strings.Count(p, "\n### ") > 1 {
			t.Errorf("lote com mais de uma dor:\n%s", p)
		}
	}
}

func TestTruncatedBatchesAreSplit(t *testing.T) {
	conn := openDB(t)
	seedItems(t, conn, "2026-48", 12)
	f := &fakeLLM{t: t, calls: map[string]int{}, cutAbove: 1}
	r := newRunner(t, conn, f)
	r.Cfg.AI.ScoreBatchSize = 5
	var logs []string
	r.Log = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
	plan, err := runWeek(t, r, "2026-48")
	if err != nil {
		t.Fatalf("deveria dividir os lotes cortados até caber: %v", err)
	}
	// Lotes de 4 candidatos viram 2+2 e depois 1+1; as 2 dores, 1+1.
	if len(plan.Assign) != 8 || len(plan.Scores) != 2 {
		t.Errorf("atribuídos=%d notas=%d", len(plan.Assign), len(plan.Scores))
	}
	all := strings.Join(logs, "\n")
	for _, want := range []string{"agrupamento: lote 1 de 3 cortado", "lote 1 de 3aa ok", "pontuação: lote 1 de 1 cortado", "lote 1 de 1b ok"} {
		if !strings.Contains(all, want) {
			t.Errorf("log sem %q:\n%s", want, all)
		}
	}

	// Um item sozinho ainda cortado: não há mais o que dividir, vira erro.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "/models") {
			w.Write([]byte(`{"data":[]}`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"{"},"finish_reason":"length"}],"usage":{"total_tokens":10}}`))
	}))
	defer srv.Close()
	r2 := newRunner(t, conn, &fakeLLM{t: t, calls: map[string]int{}})
	r2.Client.BaseURL = srv.URL
	if _, err := runWeek(t, r2, "2026-48"); !errors.Is(err, ErrTruncated) {
		t.Errorf("esperava ErrTruncated, veio %v", err)
	}
}
