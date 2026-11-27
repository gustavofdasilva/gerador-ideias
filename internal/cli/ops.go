package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"gerador-ideias/internal/calibrate"
	"gerador-ideias/internal/db"
	"gerador-ideias/internal/fmtx"
	"gerador-ideias/internal/health"
	"gerador-ideias/internal/process"
	"gerador-ideias/internal/secrets"
)

// check avalia a saúde da coleta e avisa pelo Telegram (roadmap F6).
func (a *App) check(ctx context.Context, args []string) error {
	fs, c := a.newFlagSet("check", "check [--no-telegram]")
	noTelegram := fs.Bool("no-telegram", false, "só mostra os problemas, sem avisar pelo Telegram")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usagef("argumento inesperado: %q", fs.Arg(0))
	}
	s, err := a.open(ctx, c, "")
	if err != nil {
		return err
	}
	defer s.conn.Close()
	cfg := s.e.cfg
	alerts, err := health.Evaluate(ctx, s.conn, health.Options{
		Sources: cfg.Collect.Sources, FailingDays: cfg.Alerts.FailingDays,
		StaleHours: cfg.Alerts.StaleHours, Now: s.now,
	})
	if err != nil {
		return err
	}
	if len(alerts) == 0 {
		fmt.Fprintln(a.Stdout, "check: coleta saudável")
	}
	for _, al := range alerts {
		fmt.Fprintf(a.Stdout, "ALERTA: %s\n", al.Message)
	}
	if *noTelegram {
		return nil
	}
	token, chat := s.e.secrets.Get(secrets.TelegramBotToken), s.e.secrets.Get(secrets.TelegramChatID)
	if token == "" || chat == "" {
		if len(alerts) > 0 {
			fmt.Fprintln(a.Stderr, "aviso: TELEGRAM_BOT_TOKEN ou TELEGRAM_CHAT_ID não definidos: alertas só no log")
		}
		return nil
	}
	tg := a.newTelegram(token, chat)
	repeat := time.Duration(cfg.Alerts.RepeatHours) * time.Hour
	n, err := health.Notify(ctx, s.conn, alerts, tg.SendMessage, s.now, repeat)
	if err != nil {
		return fmt.Errorf("enviar alertas: %w", err)
	}
	if n > 0 {
		fmt.Fprintf(a.Stdout, "check: %d mensagem(ns) enviada(s) pelo Telegram\n", n)
	}
	return nil
}

// backup grava um backup comprimido do banco (também roda depois de cada relatório).
func (a *App) backup(ctx context.Context, args []string) error {
	fs, c := a.newFlagSet("backup", "backup")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usagef("argumento inesperado: %q", fs.Arg(0))
	}
	s, err := a.open(ctx, c, "")
	if err != nil {
		return err
	}
	defer s.conn.Close()
	if s.e.cfg.Backup.Dir == "" {
		return fmt.Errorf("backup.dir vazio no config: defina a pasta do backup")
	}
	return a.runBackup(ctx, s)
}

func (a *App) runBackup(ctx context.Context, s *session) error {
	dir, err := s.e.cfg.Backup.ResolvedDir()
	if err != nil {
		return err
	}
	path, err := db.Backup(ctx, s.conn, dir, s.e.cfg.Backup.Keep, a.Now())
	if err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	size := ""
	if st, err := fileSize(path); err == nil {
		size = " (" + fmtx.Bytes(st) + ")"
	}
	fmt.Fprintf(a.Stdout, "backup: %s%s\n", path, size)
	return nil
}

func fileSize(path string) (int64, error) {
	st, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

// calibrate mostra o aproveitamento do filtro local segundo o veredito da IA.
func (a *App) calibrate(ctx context.Context, args []string) error {
	fs, c := a.newFlagSet("calibrate", "calibrate [--week AAAA-WW] [--weeks N]")
	weekFlag := fs.String("week", "", "última semana do período (padrão: a semana corrente)")
	weeks := fs.Int("weeks", 1, "quantas semanas, terminando em --week")
	phrases := fs.Int("phrases", 30, "quantas frases listar")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usagef("argumento inesperado: %q", fs.Arg(0))
	}
	if err := validWeek(*weekFlag); err != nil {
		return err
	}
	if *weeks < 1 || *weeks > 52 {
		return usagef("--weeks precisa ir de 1 a 52")
	}
	s, err := a.open(ctx, c, *weekFlag)
	if err != nil {
		return err
	}
	defer s.conn.Close()
	list, err := calibrate.Weeks(s.week, *weeks)
	if err != nil {
		return err
	}
	res, err := calibrate.Build(ctx, s.conn, process.New(s.e.cfg), list)
	if err != nil {
		return err
	}
	fmt.Fprint(a.Stdout, res.Text(*phrases))
	return nil
}
