package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gerador-ideias/internal/db"
	"gerador-ideias/internal/report"
	"gerador-ideias/internal/secrets"
	"gerador-ideias/internal/telegram"
	"gerador-ideias/internal/week"
)

// validWeek aceita "" (semana corrente) ou uma semana ISO AAAA-WW que exista.
func validWeek(w string) error {
	if w == "" {
		return nil
	}
	if _, err := week.Start(w); err != nil {
		return usageError{"--" + err.Error()}
	}
	return nil
}

func (a *App) newTelegram(token, chatID string) *telegram.Client {
	if a.Telegram != nil {
		return a.Telegram(token, chatID)
	}
	return telegram.New(token, chatID)
}

func (a *App) report(ctx context.Context, args []string) error {
	fs, c := a.newFlagSet("report", "report [--week AAAA-WW] [--no-ai] [--no-telegram]")
	weekFlag := fs.String("week", "", "semana ISO a gerar (padrão: a semana corrente)")
	noAI := fs.Bool("no-ai", false, "gera só os rankings locais, sem IA")
	noTelegram := fs.Bool("no-telegram", false, "só gera o arquivo, sem enviar pelo Telegram")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usagef("argumento inesperado: %q", fs.Arg(0))
	}
	if err := validWeek(*weekFlag); err != nil {
		return err
	}
	now := a.Now()
	wk := *weekFlag
	if wk == "" {
		wk = week.Of(now)
	}

	e, err := a.load(c)
	if err != nil {
		return err
	}
	conn, err := db.Open(e.dbPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := db.Migrate(ctx, conn); err != nil {
		return err
	}

	aiNote := "não usada (--no-ai): só rankings locais"
	if !*noAI {
		// A parte de IA (dores agrupadas e pontuadas) chega na F5.
		fmt.Fprintln(a.Stderr, "aviso: a IA ainda não está implementada; gerando o relatório sem IA")
		aiNote = "ainda não implementada: só rankings locais"
	}

	run, err := db.StartRun(ctx, conn, "report", "", now)
	if err != nil {
		return err
	}
	path, sendErr, err := a.writeAndSend(ctx, e, conn, wk, now, aiNote, !*noTelegram)
	run.Status = "ok"
	switch {
	case err != nil:
		run.Status, run.Errors, run.ErrorText = "error", 1, err.Error()
	case sendErr != nil:
		run.Status, run.Errors, run.ErrorText = "partial", 1, sendErr.Error()
	}
	if ferr := db.FinishRun(context.WithoutCancel(ctx), conn, run, a.Now()); ferr != nil {
		return errors.Join(err, sendErr, ferr)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "relatório gerado: %s\n", path)
	if sendErr != nil {
		return fmt.Errorf("o relatório ficou só no arquivo local: %w", sendErr)
	}
	return nil
}

// writeAndSend gera o arquivo e envia pelo Telegram. sendErr é a falha de
// envio (o arquivo já está gravado); err é falha ao gerar o relatório.
func (a *App) writeAndSend(ctx context.Context, e *env, conn *sql.DB, wk string, now time.Time, aiNote string, send bool) (path string, sendErr, err error) {
	cfg := e.cfg
	rep, err := report.Build(ctx, conn, report.Options{
		Week: wk, Now: now, DBPath: e.dbPath, AINote: aiNote,
		TopCandidates: cfg.Report.TopCandidates, TopKeywords: cfg.Report.TopKeywords,
		TopBrands: cfg.Report.TopBrands, MinKeywordCount: cfg.Report.MinKeywordCount,
	})
	if err != nil {
		return "", nil, err
	}
	dir, err := cfg.Report.ResolvedDir()
	if err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, err
	}
	md := rep.Markdown()
	path = filepath.Join(dir, wk+".md")
	if err := os.WriteFile(path, []byte(md), 0o644); err != nil {
		return "", nil, err
	}
	if !send {
		return path, nil, nil
	}
	token, chat := e.secrets.Get(secrets.TelegramBotToken), e.secrets.Get(secrets.TelegramChatID)
	if token == "" || chat == "" {
		fmt.Fprintln(a.Stderr, "aviso: TELEGRAM_BOT_TOKEN ou TELEGRAM_CHAT_ID não definidos: relatório só no arquivo local")
		return path, nil, nil
	}
	tg := a.newTelegram(token, chat)
	if err := tg.SendMessage(ctx, rep.Summary()); err != nil {
		return path, err, nil
	}
	if err := tg.SendDocument(ctx, wk+".md", []byte(md), "Relatório da semana "+wk); err != nil {
		return path, err, nil
	}
	fmt.Fprintln(a.Stdout, "enviado pelo Telegram")
	return path, nil, nil
}
