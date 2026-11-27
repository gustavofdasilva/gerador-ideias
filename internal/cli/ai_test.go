package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"gerador-ideias/internal/ai"
	"gerador-ideias/internal/collect"
	"gerador-ideias/internal/secrets"
)

var reRefs = regexp.MustCompile(`(?m)^### (i\d+) · `)

// fakeOmniRoute: põe cada candidato num cluster só e dá notas fixas.
func fakeOmniRoute(t *testing.T, down *bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if *down {
			http.Error(w, "offline", http.StatusBadGateway)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Write([]byte(`{"data":[{"id":"combo-agrupar"},{"id":"combo-pontuar"}]}`))
			return
		}
		var req struct{ Messages []ai.Message }
		json.NewDecoder(r.Body).Decode(&req)
		content := answerFor(req.Messages[0].Content, req.Messages[1].Content)
		json.NewEncoder(w).Encode(map[string]any{
			"model":   "modelo-x",
			"choices": []map[string]any{{"message": map[string]string{"content": content}, "finish_reason": "stop"}},
			"usage":   map[string]int{"total_tokens": 91234},
		})
	}))
}

func answerFor(system, user string) string {
	if strings.Contains(system, "Etapa: agrupar") {
		resp := map[string]any{
			"novos_clusters": []map[string]string{{"ref": "novo1", "dor": "Freelancers sem jeito simples de cobrar em várias moedas", "nicho": "faturamento p/ freelancers", "resumo": "x"}},
		}
		var as []map[string]any
		for _, m := range reRefs.FindAllStringSubmatch(user, -1) {
			as = append(as, map[string]any{"item": m[1], "cluster": "novo1"})
		}
		resp["atribuicoes"] = as
		b, _ := json.Marshal(resp)
		return string(b)
	}
	ref := regexp.MustCompile(`(?m)^### ([cn]\d+) — `).FindStringSubmatch(user)[1]
	return scoreJSON(ref)
}

func scoreJSON(ref string) string {
	b, _ := json.Marshal(map[string]any{"pontuacoes": []map[string]any{{
		"cluster": ref,
		"notas": map[string]int{"frequencia": 4, "pagamento": 5, "distribuicao": 4, "b2b": 4,
			"global": 5, "solo": 4, "vendabilidade": 4, "infra": 5},
		"penalidades":         []any{},
		"bonus_concorrencia":  map[string]any{"aplica": true, "motivo": "Harvest é caro"},
		"canal_provavel":      "SEO programático (\"invoice template for {país}\")",
		"indicio_canal":       "procurei no Google e não achei",
		"mvp":                 "Um app web que emite faturas em várias moedas.",
		"por_que_pode_falhar": "Stripe pode lançar algo parecido.",
	}}})
	return string(b)
}

func setupAI(t *testing.T) (app *App, dir, dbPath string, down *bool, tg *fakeTelegram) {
	t.Helper()
	dir = isolate(t)
	dbPath = filepath.Join(dir, "ideias.db")
	down = new(bool)
	srv := fakeOmniRoute(t, down)
	t.Cleanup(srv.Close)
	t.Setenv(secrets.OmnirouteBaseURL, srv.URL+"/v1")
	t.Setenv(secrets.TelegramBotToken, "1:x")
	t.Setenv(secrets.TelegramChatID, "42")
	tg = &fakeTelegram{docs: map[string]string{}}
	app, _, _ = newReportApp(t, tg)
	app.AIClient = func(base, key string, timeout time.Duration) *ai.Client {
		c := ai.NewClient(base, key, timeout)
		c.HTTP.MinInterval, c.HTTP.MaxRetries = 0, 0
		return c
	}
	app.Collectors = func(*env) map[string]collect.Collector {
		return map[string]collect.Collector{"hn": stubCollector{source: "hn"}}
	}
	if code := app.Run(context.Background(), []string{"collect", "--source", "hn", "--db", dbPath}); code != exitOK {
		t.Fatal("collect falhou")
	}
	return app, dir, dbPath, down, tg
}

func TestReportWithAI(t *testing.T) {
	app, dir, dbPath, _, tg := setupAI(t)
	if code := app.Run(context.Background(), []string{"report", "--db", dbPath}); code != exitOK {
		t.Fatalf("report: %s", app.Stderr.(interface{ String() string }).String())
	}
	md, _ := os.ReadFile(filepath.Join(dir, "reports", "2026-48.md"))
	s := string(md)
	for _, want := range []string{
		"Coleta: HN 1 · Bluesky 0 · GitHub 0 → 1 candidatos → 1 dores\n",
		"IA: OmniRoute · 182k tokens (limite semanal 500k) · modo automático\n",
		"## Top 10 dores\n",
		"### 1. Freelancers sem jeito simples de cobrar em várias moedas — 93/100 (nova)\n",
		"Nicho: faturamento p/ freelancers · Idiomas: EN 100% · País est.: desconhecido\n",
		"Menções: 1 nesta semana / 1 no total · Marcas: —\n",
		"Notas: pagamento 5 · distribuição 4 · B2B 4 · global 5 · solo 4 · venda 4 · infra 5 · freq. 4\n",
		"Ajustes: +5 concorrente caro ou complexo: Harvest é caro\n",
		"Canal provável: SEO programático (\"invoice template for {país}\") (indício: procurei no Google e não achei)\n",
		"MVP em uma frase: Um app web que emite faturas em várias moedas.\n",
		"Por que pode falhar: Stripe pode lançar algo parecido.\n",
		"Evidências: [1](https://news.ycombinator.com/item?id=1)\n",
		"### Nichos",
		"## Saúde da coleta",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("relatório sem %q", want)
		}
	}
	if strings.Contains(s, "## Top candidatos") {
		t.Error("com IA, a lista de candidatos brutos não deveria aparecer")
	}
	if t.Failed() {
		t.Log(s)
	}
	if tg.messages != 1 {
		t.Errorf("mensagens = %d", tg.messages)
	}
}

func TestReportFallbackAndManualImport(t *testing.T) {
	app, dir, dbPath, down, tg := setupAI(t)
	*down = true
	ctx := context.Background()
	if code := app.Run(ctx, []string{"report", "--db", dbPath}); code != exitOK {
		t.Fatalf("fallback deveria sair com sucesso: %s", app.Stderr.(interface{ String() string }).String())
	}
	md, _ := os.ReadFile(filepath.Join(dir, "reports", "2026-48.md"))
	if !strings.Contains(string(md), "IA: indisponível (OmniRoute fora do ar") || !strings.Contains(string(md), "## Top candidatos") {
		t.Errorf("relatório de fallback:\n%s", md)
	}
	prompt := filepath.Join(dir, "export", "2026-48", "prompt.md")
	cands := filepath.Join(dir, "export", "2026-48", "candidatos.md")
	if _, err := os.Stat(prompt); err != nil {
		t.Fatal("pacote manual não gerado")
	}
	if tg.messages != 2 {
		t.Errorf("esperava aviso + resumo no Telegram, veio %d mensagens", tg.messages)
	}

	// O usuário responde no Claude web e importa.
	data, _ := os.ReadFile(cands)
	group := answerFor("Etapa: agrupar", string(data))
	var resp map[string]any
	json.Unmarshal([]byte(group), &resp)
	var score map[string]any
	json.Unmarshal([]byte(scoreJSON("novo1")), &score)
	resp["pontuacoes"] = score["pontuacoes"]
	b, _ := json.Marshal(resp)
	answer := filepath.Join(dir, "resposta.json")
	os.WriteFile(answer, []byte("Claro, aqui está:\n```json\n"+string(b)+"\n```"), 0o644)

	if code := app.Run(ctx, []string{"import", "--db", dbPath, answer}); code != exitOK {
		t.Fatalf("import: %s", app.Stderr.(interface{ String() string }).String())
	}
	md, _ = os.ReadFile(filepath.Join(dir, "reports", "2026-48.md"))
	s := string(md)
	if !strings.Contains(s, "IA: Claude web (manual)\n") || !strings.Contains(s, "— 93/100 (nova)") {
		t.Errorf("relatório do import:\n%s", s)
	}

	// O veredito do import alimenta o calibrate.
	outBuf := app.Stdout.(interface {
		String() string
		Reset()
	})
	outBuf.Reset()
	if code := app.Run(ctx, []string{"calibrate", "--db", dbPath}); code != exitOK {
		t.Fatal("calibrate falhou")
	}
	if !strings.Contains(outBuf.String(), "1 candidatos julgados pela IA · 1 viraram dor") {
		t.Errorf("calibrate:\n%s", outBuf.String())
	}

	// Resposta com erro: mensagem clara, nada aplicado.
	os.WriteFile(answer, []byte(`{"novos_clusters":[],"atribuicoes":[{"item":"i1","cluster":"c999"}],"pontuacoes":[]}`), 0o644)
	errOut := app.Stderr.(interface {
		String() string
		Reset()
	})
	errOut.Reset()
	if code := app.Run(ctx, []string{"import", "--db", dbPath, answer}); code != exitError {
		t.Errorf("import inválido deveria falhar")
	}
	if !strings.Contains(errOut.String(), `"c999" não existe`) || !strings.Contains(errOut.String(), "Corrija o JSON") {
		t.Errorf("erro do import: %s", errOut.String())
	}
}
