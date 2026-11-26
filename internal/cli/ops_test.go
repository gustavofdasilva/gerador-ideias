package cli

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gerador-ideias"
	"gerador-ideias/internal/collect"
	"gerador-ideias/internal/db"
	"gerador-ideias/internal/secrets"
)

func TestCheckAlertsAndBackup(t *testing.T) {
	dir := isolate(t)
	dbPath := filepath.Join(dir, "ideias.db")
	tg := &fakeTelegram{docs: map[string]string{}}
	app, out, _ := newReportApp(t, tg)
	ctx := context.Background()
	failing := func(*env) map[string]collect.Collector {
		return map[string]collect.Collector{
			"hn":   stubCollector{source: "hn"},
			"bsky": stubCollector{source: "bsky", err: errors.New("login no Bluesky recusado")},
			"gh":   stubCollector{source: "gh"},
		}
	}
	app.Collectors = failing

	// Bluesky falha dois dias seguidos.
	day := time.Date(2026, 11, 27, 9, 0, 0, 0, time.Local)
	for i := 0; i < 2; i++ {
		d := day.AddDate(0, 0, i)
		app.Now = func() time.Time { return d }
		app.Run(ctx, []string{"collect", "--db", dbPath})
	}

	// Sem Telegram: só mostra.
	out.Reset()
	if code := app.Run(ctx, []string{"check", "--db", dbPath}); code != exitOK {
		t.Fatal("check falhou")
	}
	if !strings.Contains(out.String(), "ALERTA: Bluesky falhou em todas as coletas dos últimos 2 dias") || tg.messages != 0 {
		t.Errorf("check sem Telegram: %q (mensagens %d)", out.String(), tg.messages)
	}

	// Com Telegram: avisa uma vez; a segunda verificação não repete.
	t.Setenv(secrets.TelegramBotToken, "1:x")
	t.Setenv(secrets.TelegramChatID, "42")
	app.Run(ctx, []string{"check", "--db", dbPath})
	app.Run(ctx, []string{"check", "--db", dbPath})
	if tg.messages != 1 {
		t.Errorf("mensagens = %d, want 1", tg.messages)
	}

	// Tirando o Bluesky de collect.sources, o alerta some e chega o "resolvido".
	cfg := filepath.Join(dir, "config.toml")
	writeFile(t, cfg, strings.Replace(exampleConfigText(t), `sources = ["hn", "bsky", "gh"]`, `sources = ["hn", "gh"]`, 1))
	out.Reset()
	app.Run(ctx, []string{"check", "--config", cfg, "--db", dbPath})
	if !strings.Contains(out.String(), "coleta saudável") || tg.messages != 2 {
		t.Errorf("depois de tirar o bsky: %q (mensagens %d)", out.String(), tg.messages)
	}
	// E o collect --source all deixa de rodar o Bluesky.
	out.Reset()
	app.Run(ctx, []string{"collect", "--config", cfg, "--db", dbPath})
	if strings.Contains(out.String(), "bsky:") || !strings.Contains(out.String(), "gh:") {
		t.Errorf("collect com sources sem bsky:\n%s", out.String())
	}

	// Backup manual.
	out.Reset()
	if code := app.Run(ctx, []string{"backup", "--db", dbPath}); code != exitOK {
		t.Fatal("backup falhou")
	}
	backups, err := db.Backups(filepath.Join(dir, "storage", "shared", "ideias-backup"))
	if err != nil || len(backups) != 1 || !strings.Contains(out.String(), "backup: ") {
		t.Errorf("backups = %v (%v) out=%q", backups, err, out.String())
	}
}

func exampleConfigText(t *testing.T) string {
	t.Helper()
	return string(ideias.ExampleConfig)
}
