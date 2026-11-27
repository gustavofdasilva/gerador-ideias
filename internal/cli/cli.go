// Package cli implementa os subcomandos do binário `ideias` (design, seção 5).
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gerador-ideias/internal/ai"
	"gerador-ideias/internal/collect"
	"gerador-ideias/internal/config"
	"gerador-ideias/internal/db"
	"gerador-ideias/internal/fmtx"
	"gerador-ideias/internal/secrets"
	"gerador-ideias/internal/telegram"
)

// Códigos de saída.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

const usage = `ideias: coleta dores reais em conversas públicas e gera um relatório semanal de ideias.

Uso:
  ideias <comando> [opções]

Comandos:
  collect [--source hn|bsky|gh|all]       coleta diária, filtra e agrega
  report  [--week AAAA-WW] [--no-ai] [--no-telegram]
                                          relatório semanal (IA via OmniRoute) em reports/ + Telegram
  export  [--week AAAA-WW]                pacote para o Claude web (modo manual)
  import  [--week AAAA-WW] [--no-telegram] resposta.json
                                          importa a resposta do Claude web e gera o relatório
  stats                                   saúde: tamanho do banco, linhas, últimas coletas
  migrate                                 aplica as migrações do banco
  check   [--no-telegram]                 alertas: fonte falhando dias seguidos, coleta parada
  backup                                  backup comprimido do banco (roda sozinho após o relatório)
  calibrate [--week AAAA-WW] [--weeks N] aproveitamento por fonte e frase, segundo a IA

Opções de todos os comandos:
  --config arquivo   config.toml a usar (padrão: ./config.toml ou ~/.config/ideias/config.toml)
  --db arquivo       banco SQLite (padrão: db_path do config)

Use "ideias <comando> --help" para ver as opções de um comando.
`

// App guarda as saídas e o relógio, para os testes.
type App struct {
	Stdout io.Writer
	Stderr io.Writer
	Now    func() time.Time
	// Collectors, se definido, substitui os coletores reais (testes).
	Collectors func(*env) map[string]collect.Collector
	// Telegram, se definido, cria o cliente do Telegram (testes).
	Telegram func(token, chatID string) *telegram.Client
	// AIClient, se definido, cria o cliente do OmniRoute (testes).
	AIClient func(baseURL, apiKey string, timeout time.Duration) *ai.Client
}

// Run executa a linha de comando (sem o nome do programa) e devolve o código de saída.
// SIGINT e SIGTERM (o Android encerrando o Termux) cancelam o contexto, para
// a execução em andamento ser registrada antes de sair.
func Run(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return (&App{Stdout: os.Stdout, Stderr: os.Stderr, Now: time.Now}).Run(ctx, args)
}

// Run executa um subcomando.
func (a *App) Run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(a.Stderr, usage)
		return exitUsage
	}
	cmd, rest := args[0], args[1:]
	var run func(context.Context, []string) error
	switch cmd {
	case "collect":
		run = a.collect
	case "report":
		run = a.report
	case "export":
		run = a.export
	case "import":
		run = a.importCmd
	case "stats":
		run = a.stats
	case "migrate":
		run = a.migrate
	case "check":
		run = a.check
	case "backup":
		run = a.backup
	case "calibrate":
		run = a.calibrate
	case "help", "-h", "--help":
		fmt.Fprint(a.Stdout, usage)
		return exitOK
	default:
		fmt.Fprintf(a.Stderr, "comando desconhecido: %q\n\n%s", cmd, usage)
		return exitUsage
	}

	err := run(ctx, rest)
	var ue usageError
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, flag.ErrHelp):
		return exitOK
	case errors.Is(err, errNotImplemented):
		return exitError // a mensagem já foi impressa
	case errors.As(err, &ue):
		fmt.Fprintf(a.Stderr, "ideias %s: %v\n", cmd, err)
		return exitUsage
	default:
		fmt.Fprintf(a.Stderr, "ideias %s: %v\n", cmd, err)
		return exitError
	}
}

type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error { return usageError{fmt.Sprintf(format, args...)} }

// common são as opções aceitas por todos os subcomandos.
type common struct {
	configPath string
	dbPath     string
}

func (a *App) newFlagSet(name, synopsis string) (*flag.FlagSet, *common) {
	fs := flag.NewFlagSet("ideias "+name, flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	c := &common{}
	fs.StringVar(&c.configPath, "config", "", "config.toml a usar")
	fs.StringVar(&c.dbPath, "db", "", "banco SQLite (sobrepõe db_path)")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Uso: ideias %s\n\nOpções:\n", synopsis)
		fs.PrintDefaults()
	}
	return fs, c
}

func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError{err.Error()}
	}
	return nil
}

// env carrega config e segredos.
type env struct {
	cfg     *config.Config
	secrets *secrets.Secrets
	dbPath  string
}

func (a *App) load(c *common) (*env, error) {
	cfg, err := config.Load(c.configPath)
	if err != nil {
		return nil, err
	}
	sec, err := secrets.Load(secrets.DefaultFile())
	if err != nil {
		return nil, fmt.Errorf("segredos: %w", err)
	}
	path := c.dbPath
	if path == "" {
		if path, err = cfg.ResolvedDBPath(); err != nil {
			return nil, err
		}
	} else if path, err = config.ExpandHome(path); err != nil {
		return nil, err
	}
	return &env{cfg: cfg, secrets: sec, dbPath: path}, nil
}

var validSources = []string{"hn", "bsky", "gh", "all"}

// errNotImplemented: nenhuma das fontes pedidas tem coletor.
var errNotImplemented = errors.New("não implementado")

func (a *App) migrate(ctx context.Context, args []string) error {
	fs, c := a.newFlagSet("migrate", "migrate")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usagef("argumento inesperado: %q", fs.Arg(0))
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
	applied, err := db.Migrate(ctx, conn)
	for _, m := range applied {
		fmt.Fprintf(a.Stdout, "aplicada: %04d_%s\n", m.Version, m.Name)
	}
	if err != nil {
		return err
	}
	if len(applied) == 0 {
		fmt.Fprintf(a.Stdout, "banco já está atualizado: %s\n", e.dbPath)
	} else {
		fmt.Fprintf(a.Stdout, "banco atualizado: %s\n", e.dbPath)
	}
	return nil
}

func (a *App) stats(ctx context.Context, args []string) error {
	fs, c := a.newFlagSet("stats", "stats")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usagef("argumento inesperado: %q", fs.Arg(0))
	}
	e, err := a.load(c)
	if err != nil {
		return err
	}
	// stats não cria banco: um caminho errado deve aparecer como erro.
	if _, err := os.Stat(e.dbPath); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("banco não encontrado em %s: rode \"ideias migrate\" primeiro", e.dbPath)
	}
	conn, err := db.Open(e.dbPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	s, err := db.CollectStats(ctx, conn, e.dbPath, a.Now())
	if err != nil {
		return err
	}
	a.printStats(e, s)
	return nil
}

func (a *App) printStats(e *env, s *db.Stats) {
	w := a.Stdout
	fmt.Fprintf(w, "Config:  %s\n", e.cfg.Source)
	fmt.Fprintf(w, "Banco:   %s\n", s.Path)
	size := "Tamanho: " + fmtx.Bytes(s.FileBytes)
	if s.WALBytes > 0 {
		size += fmt.Sprintf(" (+ WAL %s)", fmtx.Bytes(s.WALBytes))
	}
	fmt.Fprintf(w, "%s · em uso pelo SQLite: %s\n", size, fmtx.Bytes(s.PageBytes))
	fmt.Fprintf(w, "Esquema: versão %d", s.SchemaVersion)
	if s.Pending > 0 {
		fmt.Fprintf(w, " · %d migração(ões) pendente(s): rode \"ideias migrate\"", s.Pending)
	}
	fmt.Fprintln(w)
	if s.SchemaVersion == 0 {
		return
	}

	fmt.Fprintln(w, "\nTabelas (linhas · espaço com índices):")
	for _, t := range s.Tables {
		mark := ""
		if db.BoundedTables[t.Table] {
			mark = "  (com teto)"
		}
		fmt.Fprintf(w, "  %-14s %10s · %9s%s\n", t.Table, fmtx.Int(t.Rows), fmtx.Bytes(t.Bytes), mark)
	}

	fmt.Fprintln(w, "\nItens processados por fonte (histórico inteiro):")
	if len(s.BySource) == 0 {
		fmt.Fprintln(w, "  nenhum item coletado")
	}
	for _, sc := range s.BySource {
		fmt.Fprintf(w, "  %-5s %10s itens · %s candidatos (%s)\n", sc.Source,
			fmtx.Int(sc.Items), fmtx.Int(sc.Candidates), fmtx.Percent(sc.Rate()))
	}
	if len(s.Rates) > 0 {
		fmt.Fprintf(w, "\nTaxa de candidatos por fonte e idioma (últimos %d dias):\n", db.RateDays)
		for _, sc := range s.Rates {
			fmt.Fprintf(w, "  %-5s %-4s %10s itens · %8s candidatos (%s)\n", sc.Source, sc.Lang,
				fmtx.Int(sc.Items), fmtx.Int(sc.Candidates), fmtx.Percent(sc.Rate()))
		}
	}
	if s.DataDays > 0 {
		// Tabelas com teto: seen fica como está; termos e marcas crescem até
		// completar a retenção e param.
		keepDays := 7 * e.cfg.Storage.KeepTermWeeks
		ceiling := s.BoundedBytes - s.TermBytes + s.TermBytes*int64(max(keepDays, s.DataDays))/int64(s.DataDays)
		yearly := s.MonthlyGrowth*12 + ceiling
		fmt.Fprintf(w, "\nCrescimento estimado: %s/mês nas tabelas que crescem sempre + teto de ~%s nas com retenção\n",
			fmtx.Bytes(s.MonthlyGrowth), fmtx.Bytes(ceiling))
		fmt.Fprintf(w, "  em 1 ano: ~%s (meta < 200 MiB) · base: %d dia(s) de posts\n", fmtx.Bytes(yearly), s.DataDays)
		if s.DataDays < 7 {
			fmt.Fprintln(w, "  (estimativa grosseira: menos de 7 dias de dados)")
		}
	}

	fmt.Fprintln(w, "\nÚltima coleta por fonte:")
	if len(s.LastRuns) == 0 {
		fmt.Fprintln(w, "  nenhuma coleta registrada")
	}
	for _, r := range s.LastRuns {
		when := r.StartedAt.Local().Format("2006-01-02 15:04")
		status := r.Status
		if r.FinishedAt == nil && status == "running" {
			status = "sem fim registrado (interrompida?)"
		}
		fmt.Fprintf(w, "  %-5s %s · %s · %d novos · %d erros\n", r.Source, when, status, r.ItemsNew, r.Errors)
	}

	fmt.Fprintf(w, "\nIA no mês: %s tokens (limite semanal: %s)\n",
		fmtx.Int(s.MonthTokens), fmtx.Int(e.cfg.AI.WeeklyTokenLimit))

	fmt.Fprint(w, "Segredos:")
	for _, k := range secrets.Keys {
		mark := "não definido"
		if e.secrets.Has(k) {
			mark = "ok"
		}
		fmt.Fprintf(w, "\n  %-19s %s", k, mark)
	}
	fmt.Fprintln(w)
}
