// Package cli implementa os subcomandos do binário `ideias` (design, seção 5).
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"gerador-ideias/internal/config"
	"gerador-ideias/internal/db"
	"gerador-ideias/internal/secrets"
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
  report  [--week AAAA-WW] [--no-ai]      relatório semanal (IA via OmniRoute)
  export  [--week AAAA-WW]                pacote para o Claude web (modo manual)
  import  [--week AAAA-WW] resposta.json  importa a resposta do Claude web
  stats                                   saúde: tamanho do banco, linhas, últimas coletas
  migrate                                 aplica as migrações do banco

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
}

// Run executa a linha de comando (sem o nome do programa) e devolve o código de saída.
func Run(args []string) int {
	return (&App{Stdout: os.Stdout, Stderr: os.Stderr, Now: time.Now}).Run(context.Background(), args)
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

var weekRe = regexp.MustCompile(`^(\d{4})-(\d{2})$`)

// validWeek aceita "" (semana corrente, definida na fase que usar) ou AAAA-WW.
func validWeek(w string) error {
	if w == "" {
		return nil
	}
	m := weekRe.FindStringSubmatch(w)
	if m == nil {
		return usagef("--week %q inválida: use AAAA-WW (ex.: 2026-48)", w)
	}
	var n int
	fmt.Sscanf(m[2], "%d", &n)
	if n < 1 || n > 53 {
		return usagef("--week %q inválida: semana deve ir de 01 a 53", w)
	}
	return nil
}

var validSources = []string{"hn", "bsky", "gh", "all"}

var errNotImplemented = errors.New("não implementado")

func (a *App) notImplemented(cmd, phase string) error {
	fmt.Fprintf(a.Stdout, "ideias %s: não implementado (previsto para a fase %s)\n", cmd, phase)
	return errNotImplemented
}

func (a *App) collect(_ context.Context, args []string) error {
	fs, _ := a.newFlagSet("collect", "collect [--source hn|bsky|gh|all]")
	source := fs.String("source", "all", "fonte a coletar: "+strings.Join(validSources, ", "))
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usagef("argumento inesperado: %q", fs.Arg(0))
	}
	ok := false
	for _, s := range validSources {
		ok = ok || s == *source
	}
	if !ok {
		return usagef("--source %q inválida: use %s", *source, strings.Join(validSources, ", "))
	}
	return a.notImplemented("collect", "F1–F3")
}

func (a *App) report(_ context.Context, args []string) error {
	fs, _ := a.newFlagSet("report", "report [--week AAAA-WW] [--no-ai]")
	week := fs.String("week", "", "semana ISO a gerar (padrão: a semana corrente)")
	fs.Bool("no-ai", false, "gera só os rankings locais, sem IA")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usagef("argumento inesperado: %q", fs.Arg(0))
	}
	if err := validWeek(*week); err != nil {
		return err
	}
	return a.notImplemented("report", "F4–F5")
}

func (a *App) export(_ context.Context, args []string) error {
	fs, _ := a.newFlagSet("export", "export [--week AAAA-WW]")
	week := fs.String("week", "", "semana ISO a exportar (padrão: a semana corrente)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usagef("argumento inesperado: %q", fs.Arg(0))
	}
	if err := validWeek(*week); err != nil {
		return err
	}
	return a.notImplemented("export", "F5")
}

func (a *App) importCmd(_ context.Context, args []string) error {
	fs, _ := a.newFlagSet("import", "import [--week AAAA-WW] resposta.json")
	week := fs.String("week", "", "semana ISO da resposta (padrão: a semana corrente)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return usagef("informe exatamente um arquivo de resposta (resposta.json)")
	}
	if err := validWeek(*week); err != nil {
		return err
	}
	return a.notImplemented("import", "F5")
}

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
	size := "Tamanho: " + humanBytes(s.FileBytes)
	if s.WALBytes > 0 {
		size += fmt.Sprintf(" (+ WAL %s)", humanBytes(s.WALBytes))
	}
	fmt.Fprintf(w, "%s · em uso pelo SQLite: %s\n", size, humanBytes(s.PageBytes))
	fmt.Fprintf(w, "Esquema: versão %d", s.SchemaVersion)
	if s.Pending > 0 {
		fmt.Fprintf(w, " · %d migração(ões) pendente(s): rode \"ideias migrate\"", s.Pending)
	}
	fmt.Fprintln(w)
	if s.SchemaVersion == 0 {
		return
	}

	fmt.Fprintln(w, "\nLinhas por tabela:")
	for _, t := range s.Tables {
		fmt.Fprintf(w, "  %-14s %10s\n", t.Table, groupThousands(t.Rows))
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
		groupThousands(s.MonthTokens), groupThousands(e.cfg.AI.WeeklyTokenLimit))

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

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return strings.Replace(fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp]), ".", ",", 1)
}

func groupThousands(n int64) string {
	s := fmt.Sprintf("%d", n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
