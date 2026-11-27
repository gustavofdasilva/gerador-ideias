package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gerador-ideias/internal/collect"
	"gerador-ideias/internal/collect/bsky"
	"gerador-ideias/internal/collect/gh"
	"gerador-ideias/internal/collect/hn"
	"gerador-ideias/internal/db"
	"gerador-ideias/internal/process"
	"gerador-ideias/internal/secrets"
)

// sourceOrder é a ordem em que --source all roda as fontes.
var sourceOrder = []string{"hn", "bsky", "gh"}

func (a *App) defaultCollectors(e *env) map[string]collect.Collector {
	cfg := e.cfg
	var queries []bsky.Query
	for _, lang := range cfg.Sources.Bsky.Languages {
		for _, p := range cfg.PhrasesFor([]string{lang}) {
			queries = append(queries, bsky.Query{Phrase: p, Lang: lang})
		}
	}
	return map[string]collect.Collector{
		hn.Source:   hn.New(cfg.PhrasesFor(cfg.Sources.HN.Languages)),
		bsky.Source: bsky.New(e.secrets.Get(secrets.BskyHandle), e.secrets.Get(secrets.BskyAppPassword), queries),
		gh.Source: gh.New(e.secrets.Get(secrets.GithubToken),
			cfg.PhrasesFor(cfg.Sources.GH.Languages), cfg.Sources.GH.Labels),
	}
}

func (a *App) collect(ctx context.Context, args []string) error {
	fs, c := a.newFlagSet("collect", "collect [--source hn|bsky|gh|all]")
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

	e, err := a.load(c)
	if err != nil {
		return err
	}
	conn, err := db.Open(e.dbPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	// Aplica migrações pendentes sozinho: depois de um "git pull && make build",
	// o cron não pode parar por falta de um "ideias migrate".
	applied, err := db.Migrate(ctx, conn)
	if err != nil {
		return err
	}
	for _, m := range applied {
		fmt.Fprintf(a.Stdout, "migração aplicada: %04d_%s\n", m.Version, m.Name)
	}

	factory := a.defaultCollectors
	if a.Collectors != nil {
		factory = a.Collectors
	}
	collectors := factory(e)

	// "all" = as fontes de collect.sources, na ordem padrão.
	var wanted []string
	for _, s := range sourceOrder {
		for _, enabled := range e.cfg.Collect.Sources {
			if s == enabled {
				wanted = append(wanted, s)
			}
		}
	}
	if *source != "all" {
		wanted = []string{*source}
	}
	runner := &collect.Runner{
		DB:              conn,
		Now:             a.Now,
		Processor:       process.New(e.cfg),
		InitialLookback: e.cfg.Collect.InitialLookback(),
		Overlap:         e.cfg.Collect.Overlap(),
		MinTermCount:    e.cfg.Keywords.MinCount,
		MaxTextChars:    e.cfg.Storage.MaxTextChars,
	}

	var errs []error
	ran := 0
	for _, src := range wanted {
		col, ok := collectors[src]
		if !ok {
			fmt.Fprintf(a.Stdout, "%s: não implementado\n", src)
			continue
		}
		ran++
		// A falha de uma fonte não impede as outras (o erro fica em runs).
		res, err := runner.Run(ctx, col)
		if res != nil {
			fmt.Fprintln(a.Stdout, res.Summary())
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", src, err))
			if ctx.Err() != nil {
				break
			}
		}
	}
	if ran == 0 {
		return errNotImplemented
	}
	// Aplica os limites de armazenamento (inclusive ao que já estava no banco).
	st := e.cfg.Storage
	cr, err := db.Compact(context.WithoutCancel(ctx), conn, db.CompactOptions{
		MaxCandidatesPerDay: st.MaxCandidatesPerDay, MaxTextChars: st.MaxTextChars,
		KeepTermWeeks: st.KeepTermWeeks, Now: a.Now(),
	})
	if err != nil {
		errs = append(errs, fmt.Errorf("compactação: %w", err))
	} else if s := cr.Summary(); s != "" {
		fmt.Fprintln(a.Stdout, s)
	}
	return errors.Join(errs...)
}
