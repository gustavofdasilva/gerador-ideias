package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gerador-ideias/internal/collect"
	"gerador-ideias/internal/secrets"
)

// isolate evita que os testes leiam config.toml ou env reais do usuário.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	for _, k := range secrets.Keys {
		t.Setenv(k, "")
	}
	return dir
}

func run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	app := &App{Stdout: &out, Stderr: &errb, Now: func() time.Time {
		return time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	}}
	code = app.Run(context.Background(), args)
	return code, out.String(), errb.String()
}

func TestMigrateThenStats(t *testing.T) {
	dir := isolate(t)
	dbPath := filepath.Join(dir, "dados", "ideias.db")

	code, out, errOut := run(t, "stats", "--db", dbPath)
	if code != exitError || !strings.Contains(errOut, "rode \"ideias migrate\"") {
		t.Fatalf("stats sem banco: code=%d stderr=%q", code, errOut)
	}

	code, out, errOut = run(t, "migrate", "--db", dbPath)
	if code != exitOK {
		t.Fatalf("migrate: code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "aplicada: 0001_init") {
		t.Errorf("migrate stdout = %q", out)
	}

	code, out, _ = run(t, "migrate", "--db", dbPath)
	if code != exitOK || !strings.Contains(out, "já está atualizado") {
		t.Errorf("segundo migrate: code=%d out=%q", code, out)
	}

	t.Setenv(secrets.GithubToken, "x")
	code, out, errOut = run(t, "stats", "--db", dbPath)
	if code != exitOK {
		t.Fatalf("stats: code=%d stderr=%q", code, errOut)
	}
	for _, want := range []string{
		"Config:  embutido", "Esquema: versão 5", "items", "daily_terms", "ai_usage",
		"nenhuma coleta registrada", "IA no mês: 0 tokens (limite semanal: 500.000)",
		"GITHUB_TOKEN        ok", "BSKY_HANDLE         não definido",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stats não mostra %q:\n%s", want, out)
		}
	}
}

func TestNotImplementedAndUsage(t *testing.T) {
	isolate(t)
	cases := []struct {
		args []string
		code int
		in   string // trecho esperado em stdout+stderr
	}{
		{nil, exitUsage, "Comandos:"},
		{[]string{"help"}, exitOK, "Comandos:"},
		{[]string{"voar"}, exitUsage, "comando desconhecido"},
		{[]string{"collect", "--source", "bsky", "--db", "x.db"}, exitError, "BSKY_HANDLE e BSKY_APP_PASSWORD"},
		{[]string{"collect", "--source", "gh", "--db", "x.db"}, exitError, "GITHUB_TOKEN não definido"},
		{[]string{"collect", "--source", "reddit"}, exitUsage, "--source \"reddit\" inválida"},
		{[]string{"report", "--week", "2026-60"}, exitUsage, "não existe"},
		{[]string{"report", "--week", "2025-53"}, exitUsage, "não existe"},
		{[]string{"report", "--week", "48"}, exitUsage, "use AAAA-WW"},
		{[]string{"export", "--week", "2026-48", "--db", "x.db"}, exitOK, "Pacote do modo manual"},
		{[]string{"import", "--week", "2026-48", "--db", "x.db", "resposta.json"}, exitError, "resposta.json"},
		{[]string{"import"}, exitUsage, "exatamente um arquivo"},
		{[]string{"stats", "--help"}, exitOK, "Uso: ideias stats"},
		{[]string{"migrate", "--bogus"}, exitUsage, "flag provided but not defined"},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			code, out, errOut := run(t, tc.args...)
			if code != tc.code {
				t.Errorf("code = %d, want %d (out=%q err=%q)", code, tc.code, out, errOut)
			}
			if !strings.Contains(out+errOut, tc.in) {
				t.Errorf("saída não contém %q: out=%q err=%q", tc.in, out, errOut)
			}
		})
	}
}

func TestBadConfigFails(t *testing.T) {
	dir := isolate(t)
	cfg := filepath.Join(dir, "ruim.toml")
	writeFile(t, cfg, `languages = ["it"]`)
	code, _, errOut := run(t, "migrate", "--config", cfg, "--db", filepath.Join(dir, "x.db"))
	if code != exitError || !strings.Contains(errOut, "não suportado") {
		t.Errorf("code=%d stderr=%q", code, errOut)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

type stubCollector struct {
	source string
	err    error
}

func (s stubCollector) Source() string { return s.source }

func (s stubCollector) Collect(ctx context.Context, req collect.Request, emit func([]collect.Item) error) error {
	if s.err != nil {
		return s.err
	}
	return emit([]collect.Item{{
		ExternalID: "1", URL: "https://news.ycombinator.com/item?id=1",
		CreatedAt: req.Until.Add(-time.Minute), Text: "is there an app for this?",
		AuthorHash: req.HashAuthor("alguém"),
	}})
}

func TestCollectAllIsolatesFailures(t *testing.T) {
	dir := isolate(t)
	dbPath := filepath.Join(dir, "ideias.db")
	var out, errb bytes.Buffer
	app := &App{Stdout: &out, Stderr: &errb, Now: func() time.Time {
		return time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	}}
	app.Collectors = func(*env) map[string]collect.Collector {
		return map[string]collect.Collector{
			"hn":   stubCollector{source: "hn"},
			"bsky": stubCollector{source: "bsky", err: errors.New("rede caiu")},
			"gh":   stubCollector{source: "gh"},
		}
	}

	// Sem "migrate" antes: o collect aplica as migrações sozinho.
	// A falha do bsky não impede o gh, que roda depois dele.
	if code := app.Run(context.Background(), []string{"collect", "--db", dbPath}); code != exitError {
		t.Fatalf("falha de uma fonte deveria sair com 1: code=%d", code)
	}
	for _, want := range []string{"migração aplicada: 0001_init", "hn: ok · 1 vistos, 1 novos, 1 candidatos", "bsky: error", "gh: ok · 1 vistos, 1 novos, 1 candidatos"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("saída sem %q:\n%s", want, out.String())
		}
	}
	if !strings.Contains(errb.String(), "bsky: rede caiu") {
		t.Errorf("stderr = %q", errb.String())
	}

	out.Reset()
	if code := app.Run(context.Background(), []string{"collect", "--source", "hn", "--db", dbPath}); code != exitOK {
		t.Errorf("só hn: code=%d", code)
	}
	if !strings.Contains(out.String(), "hn: ok · 1 vistos, 0 novos, 0 candidatos") || !strings.Contains(out.String(), "desde o cursor") ||
		strings.Contains(out.String(), "gh:") {
		t.Errorf("2ª coleta:\n%s", out.String())
	}

	out.Reset()
	app.Run(context.Background(), []string{"stats", "--db", dbPath})
	for _, want := range []string{"Itens processados por fonte", "gh             1 itens · 1 candidatos (100,0%)", "hn    en            1 itens", "Crescimento estimado:", "bsky  2026-11-26", "· error ·"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("stats sem %q:\n%s", want, out.String())
		}
	}
}
