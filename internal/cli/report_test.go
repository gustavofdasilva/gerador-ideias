package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gerador-ideias/internal/collect"
	"gerador-ideias/internal/secrets"
	"gerador-ideias/internal/telegram"
)

type fakeTelegram struct {
	mu       sync.Mutex
	messages int
	docs     map[string]string
	down     bool
}

func (f *fakeTelegram) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		http.Error(w, `{"ok":false,"description":"Bad Gateway"}`, http.StatusBadGateway)
		return
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/sendMessage"):
		f.messages++
	case strings.HasSuffix(r.URL.Path, "/sendDocument"):
		r.ParseMultipartForm(1 << 20)
		file, hdr, err := r.FormFile("document")
		if err == nil {
			data, _ := io.ReadAll(file)
			f.docs[hdr.Filename] = string(data)
		}
	}
	w.Write([]byte(`{"ok":true}`))
}

func newReportApp(t *testing.T, tg *fakeTelegram) (*App, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(tg)
	t.Cleanup(srv.Close)
	var out, errb bytes.Buffer
	app := &App{Stdout: &out, Stderr: &errb, Now: func() time.Time {
		return time.Date(2026, 11, 29, 12, 0, 0, 0, time.UTC) // domingo da semana 2026-48
	}}
	app.Telegram = func(token, chat string) *telegram.Client {
		c := telegram.New(token, chat)
		c.BaseURL = srv.URL
		c.HTTP.MinInterval = 0
		c.HTTP.MaxRetries = 0
		return c
	}
	return app, &out, &errb
}

func TestReportWritesFileAndSends(t *testing.T) {
	dir := isolate(t)
	dbPath := filepath.Join(dir, "ideias.db")
	tg := &fakeTelegram{docs: map[string]string{}}
	app, out, errb := newReportApp(t, tg)
	app.Collectors = func(*env) map[string]collect.Collector {
		return map[string]collect.Collector{"hn": stubCollector{source: "hn"}}
	}
	ctx := context.Background()
	if code := app.Run(ctx, []string{"collect", "--source", "hn", "--db", dbPath}); code != exitOK {
		t.Fatalf("collect: %s", errb.String())
	}

	// Sem credenciais: gera o arquivo, avisa e sai com sucesso.
	out.Reset()
	if code := app.Run(ctx, []string{"report", "--no-ai", "--db", dbPath}); code != exitOK {
		t.Fatalf("report sem Telegram: code=%d err=%s", code, errb.String())
	}
	path := filepath.Join(dir, "reports", "2026-48.md")
	md, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(md), "# Ideias — semana 2026-48") || !strings.Contains(string(md), "is there an app for this?") {
		t.Errorf("relatório:\n%s", md)
	}
	if !strings.Contains(errb.String(), "TELEGRAM_BOT_TOKEN ou TELEGRAM_CHAT_ID não definidos") || tg.messages != 0 {
		t.Errorf("deveria avisar e não enviar: %q", errb.String())
	}

	// Com credenciais: envia o resumo e o arquivo.
	t.Setenv(secrets.TelegramBotToken, "123:abc")
	t.Setenv(secrets.TelegramChatID, "42")
	out.Reset()
	errb.Reset()
	if code := app.Run(ctx, []string{"report", "--no-ai", "--db", dbPath}); code != exitOK {
		t.Fatalf("report: code=%d err=%s", code, errb.String())
	}
	if tg.messages != 1 || !strings.Contains(tg.docs["2026-48.md"], "# Ideias — semana 2026-48") {
		t.Errorf("Telegram: mensagens=%d docs=%v", tg.messages, tg.docs)
	}
	if !strings.Contains(out.String(), "enviado pelo Telegram") {
		t.Errorf("out=%q err=%q", out.String(), errb.String())
	}

	// --no-telegram e --week regeram sem enviar.
	if code := app.Run(ctx, []string{"report", "--no-ai", "--no-telegram", "--week", "2026-47", "--db", dbPath}); code != exitOK {
		t.Fatalf("regerar: %s", errb.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "reports", "2026-47.md")); err != nil || tg.messages != 1 {
		t.Errorf("--week/--no-telegram: %v, mensagens=%d", err, tg.messages)
	}

	// Telegram fora do ar: o arquivo continua gerado, o comando sai com erro.
	os.Remove(path)
	tg.down = true
	errb.Reset()
	if code := app.Run(ctx, []string{"report", "--no-ai", "--db", dbPath}); code != exitError {
		t.Errorf("falha de envio deveria sair com 1, saiu %d", code)
	}
	if _, err := os.Stat(path); err != nil {
		t.Error("o arquivo local deveria existir mesmo com o envio falhando")
	}
	if !strings.Contains(errb.String(), "ficou só no arquivo local") || strings.Contains(errb.String(), "123:abc") {
		t.Errorf("erro de envio: %q", errb.String())
	}
}
