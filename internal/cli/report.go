package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gerador-ideias/internal/ai"
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

func (a *App) newAIClient(e *env) *ai.Client {
	timeout := time.Duration(e.cfg.AI.TimeoutSeconds) * time.Second
	base, key := e.secrets.OmnirouteURL(), e.secrets.Get(secrets.OmnirouteAPIKey)
	if a.AIClient != nil {
		return a.AIClient(base, key, timeout)
	}
	return ai.NewClient(base, key, timeout)
}

// session é o que report, export e import têm em comum: config, banco e semana.
type session struct {
	e    *env
	conn *sql.DB
	week string
	now  time.Time
}

func (a *App) open(ctx context.Context, c *common, wk string) (*session, error) {
	e, err := a.load(c)
	if err != nil {
		return nil, err
	}
	conn, err := db.Open(e.dbPath)
	if err != nil {
		return nil, err
	}
	if _, err := db.Migrate(ctx, conn); err != nil {
		conn.Close()
		return nil, err
	}
	s := &session{e: e, conn: conn, week: wk, now: a.Now()}
	if s.week == "" {
		s.week = week.Of(s.now)
	}
	return s, nil
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
	s, err := a.open(ctx, c, *weekFlag)
	if err != nil {
		return err
	}
	defer s.conn.Close()

	out := output{aiNote: "não usada (--no-ai): só rankings locais", send: !*noTelegram}
	var aiErr error
	if !*noAI {
		out, aiErr = a.runAI(ctx, s, out)
	}
	return a.finish(ctx, s, "report", out, aiErr)
}

// output descreve o relatório a gerar.
type output struct {
	aiNote    string
	withPains bool
	send      bool
	warning   string // aviso extra enviado pelo Telegram antes do relatório
}

// runAI roda o modo automático. Se o OmniRoute estiver fora do ar, se as
// chamadas falharem ou se o limite de tokens não comportar o trabalho, nada é
// gravado: o relatório sai sem IA, o pacote do modo manual é gerado e o
// Telegram recebe um aviso.
func (a *App) runAI(ctx context.Context, s *session, out output) (output, error) {
	cfg := s.e.cfg
	runner := &ai.Runner{
		DB: s.conn, Client: a.newAIClient(s.e), Cfg: cfg, Now: a.Now,
		Log: func(format string, args ...any) { fmt.Fprintf(a.Stderr, "ia: "+format+"\n", args...) },
	}
	err := runner.Check(ctx)
	var plan *ai.Plan
	if err == nil {
		var w *ai.Week
		if w, err = ai.LoadWeek(ctx, s.conn, s.week, cfg.AI.MaxItems, cfg.AI.MaxClusters); err == nil {
			plan, err = runner.Run(ctx, w)
		}
	}
	if err == nil {
		err = ai.Apply(ctx, s.conn, cfg, plan, a.Now())
	}
	if err == nil {
		used, uerr := ai.WeekUsage(ctx, s.conn, a.Now())
		if uerr != nil {
			return out, uerr
		}
		out.aiNote = fmt.Sprintf("OmniRoute · %s tokens (limite semanal %s) · modo automático",
			kTokens(used), kTokens(cfg.AI.WeeklyTokenLimit))
		out.withPains = true
		return out, nil
	}
	if ctx.Err() != nil {
		return out, err
	}

	// Fallback: relatório sem IA + pacote manual + aviso.
	reason := err.Error()
	fmt.Fprintf(a.Stderr, "aviso: IA indisponível, relatório sem IA: %v\n", err)
	dir, derr := s.e.cfg.Report.ResolvedExportDir()
	if derr != nil {
		return out, derr
	}
	files, xerr := ai.Export(ctx, s.conn, cfg, s.week, dir)
	if xerr != nil {
		return out, fmt.Errorf("gerar o pacote manual: %w (IA: %v)", xerr, err)
	}
	fmt.Fprint(a.Stdout, ai.ManualHelp(files, s.week))
	pkg := filepath.Dir(files[0])
	out.aiNote = fmt.Sprintf("indisponível (%s) · relatório sem IA; pacote do modo manual em %s", ai.ShortReason(err), pkg)
	out.warning = fmt.Sprintf("⚠️ IA indisponível na semana %s: %s\n\nO relatório abaixo saiu sem IA. "+
		"Para agrupar e pontuar no Claude web, use o pacote em %s e depois rode:\nideias import --week %s resposta.json",
		s.week, firstLine(reason, 300), pkg, s.week)
	return out, errFallback
}

// errFallback: a IA falhou, mas o relatório sem IA saiu (não é erro do comando).
var errFallback = errors.New("relatório sem IA (fallback)")

// finish gera o relatório, envia e registra a execução.
func (a *App) finish(ctx context.Context, s *session, command string, out output, aiErr error) error {
	if aiErr != nil && !errors.Is(aiErr, errFallback) {
		return aiErr
	}
	run, err := db.StartRun(ctx, s.conn, command, "", s.now)
	if err != nil {
		return err
	}
	path, sendErr, err := a.writeAndSend(ctx, s, out)
	run.Status = "ok"
	switch {
	case err != nil:
		run.Status, run.Errors, run.ErrorText = "error", 1, err.Error()
	case sendErr != nil:
		run.Status, run.Errors, run.ErrorText = "partial", 1, sendErr.Error()
	case aiErr != nil:
		run.Status, run.ErrorText = "partial", out.aiNote
	}
	if ferr := db.FinishRun(context.WithoutCancel(ctx), s.conn, run, a.Now()); ferr != nil {
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
func (a *App) writeAndSend(ctx context.Context, s *session, out output) (path string, sendErr, err error) {
	cfg := s.e.cfg
	rep, err := report.Build(ctx, s.conn, report.Options{
		Week: s.week, Now: s.now, DBPath: s.e.dbPath, AINote: out.aiNote, WithPains: out.withPains,
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
	path = filepath.Join(dir, s.week+".md")
	if err := os.WriteFile(path, []byte(md), 0o644); err != nil {
		return "", nil, err
	}
	if !out.send {
		return path, nil, nil
	}
	token, chat := s.e.secrets.Get(secrets.TelegramBotToken), s.e.secrets.Get(secrets.TelegramChatID)
	if token == "" || chat == "" {
		fmt.Fprintln(a.Stderr, "aviso: TELEGRAM_BOT_TOKEN ou TELEGRAM_CHAT_ID não definidos: relatório só no arquivo local")
		return path, nil, nil
	}
	tg := a.newTelegram(token, chat)
	if out.warning != "" {
		if err := tg.SendMessage(ctx, out.warning); err != nil {
			return path, err, nil
		}
	}
	if err := tg.SendMessage(ctx, rep.Summary()); err != nil {
		return path, err, nil
	}
	if err := tg.SendDocument(ctx, s.week+".md", []byte(md), "Relatório da semana "+s.week); err != nil {
		return path, err, nil
	}
	fmt.Fprintln(a.Stdout, "enviado pelo Telegram")
	return path, nil, nil
}

func (a *App) export(ctx context.Context, args []string) error {
	fs, c := a.newFlagSet("export", "export [--week AAAA-WW]")
	weekFlag := fs.String("week", "", "semana ISO a exportar (padrão: a semana corrente)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usagef("argumento inesperado: %q", fs.Arg(0))
	}
	if err := validWeek(*weekFlag); err != nil {
		return err
	}
	s, err := a.open(ctx, c, *weekFlag)
	if err != nil {
		return err
	}
	defer s.conn.Close()
	dir, err := s.e.cfg.Report.ResolvedExportDir()
	if err != nil {
		return err
	}
	files, err := ai.Export(ctx, s.conn, s.e.cfg, s.week, dir)
	if err != nil {
		return err
	}
	fmt.Fprint(a.Stdout, ai.ManualHelp(files, s.week))
	return nil
}

func (a *App) importCmd(ctx context.Context, args []string) error {
	fs, c := a.newFlagSet("import", "import [--week AAAA-WW] [--no-telegram] resposta.json")
	weekFlag := fs.String("week", "", "semana ISO da resposta (padrão: a semana corrente)")
	noTelegram := fs.Bool("no-telegram", false, "só gera o arquivo, sem enviar pelo Telegram")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return usagef("informe exatamente um arquivo de resposta (resposta.json)")
	}
	if err := validWeek(*weekFlag); err != nil {
		return err
	}
	raw, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return err
	}
	s, err := a.open(ctx, c, *weekFlag)
	if err != nil {
		return err
	}
	defer s.conn.Close()

	res, err := ai.Import(ctx, s.conn, s.e.cfg, s.week, raw)
	if err != nil {
		var ve *ai.ValidationError
		if errors.As(err, &ve) {
			return fmt.Errorf("%w\n\nCorrija o JSON (ou peça a correção no claude.ai, colando estes erros) e rode o import de novo", err)
		}
		return err
	}
	if err := ai.Apply(ctx, s.conn, s.e.cfg, res.Plan, a.Now()); err != nil {
		return err
	}
	if err := ai.RecordManual(ctx, s.conn, s.week, a.Now()); err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "importado: %d candidatos agrupados, %d descartados como não-dor, %d dores pontuadas\n",
		len(res.Plan.Assign), res.Discarded, len(res.Plan.Scores))
	if res.Unassigned > 0 {
		fmt.Fprintf(a.Stderr, "aviso: %d candidato(s) da semana não aparecem na resposta (tratados como descartados)\n", res.Unassigned)
	}
	out := output{aiNote: "Claude web (manual)", withPains: true, send: !*noTelegram}
	return a.finish(ctx, s, "import", out, nil)
}

// kTokens formata contagens de tokens como no design ("182k").
func kTokens(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%dk", (n+500)/1000)
}

func firstLine(s string, n int) string {
	for i, r := range s {
		if r == '\n' {
			s = s[:i]
			break
		}
	}
	if rs := []rune(s); len(rs) > n {
		return string(rs[:n]) + "…"
	}
	return s
}
