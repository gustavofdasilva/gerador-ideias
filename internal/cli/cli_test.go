package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		"Config:  embutido", "Esquema: versão 1", "items", "daily_terms", "ai_usage",
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
		{[]string{"collect"}, exitError, "não implementado"},
		{[]string{"collect", "--source", "hn"}, exitError, "não implementado"},
		{[]string{"collect", "--source", "reddit"}, exitUsage, "--source \"reddit\" inválida"},
		{[]string{"report", "--no-ai", "--week", "2026-48"}, exitError, "não implementado"},
		{[]string{"report", "--week", "2026-60"}, exitUsage, "01 a 53"},
		{[]string{"report", "--week", "48"}, exitUsage, "use AAAA-WW"},
		{[]string{"export", "--week", "2026-48"}, exitError, "não implementado"},
		{[]string{"import", "--week", "2026-48", "resposta.json"}, exitError, "não implementado"},
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

func TestFormatting(t *testing.T) {
	if got := groupThousands(1234567); got != "1.234.567" {
		t.Errorf("groupThousands = %q", got)
	}
	if got := groupThousands(999); got != "999" {
		t.Errorf("groupThousands = %q", got)
	}
	if got := humanBytes(1536); got != "1,5 KiB" {
		t.Errorf("humanBytes = %q", got)
	}
	if got := humanBytes(200 << 20); got != "200,0 MiB" {
		t.Errorf("humanBytes = %q", got)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
