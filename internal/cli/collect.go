package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gerador-ideias/internal/collect"
	"gerador-ideias/internal/collect/hn"
	"gerador-ideias/internal/db"
)

// sourceOrder é a ordem em que --source all roda as fontes.
var sourceOrder = []string{"hn", "bsky", "gh"}

// sourcePhase indica em que fase cada fonte ainda não implementada chega.
var sourcePhase = map[string]string{"bsky": "F2", "gh": "F2"}

func (a *App) defaultCollectors(e *env) map[string]collect.Collector {
	return map[string]collect.Collector{
		hn.Source: hn.New(e.cfg.PhrasesFor(e.cfg.Sources.HN.Languages)),
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

	wanted := sourceOrder
	if *source != "all" {
		wanted = []string{*source}
	}
	runner := &collect.Runner{
		DB:              conn,
		Now:             a.Now,
		InitialLookback: e.cfg.Collect.InitialLookback(),
		Overlap:         e.cfg.Collect.Overlap(),
	}

	var errs []error
	ran := 0
	for _, src := range wanted {
		col, ok := collectors[src]
		if !ok {
			fmt.Fprintf(a.Stdout, "%s: não implementado (previsto para a fase %s)\n", src, sourcePhase[src])
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
	return errors.Join(errs...)
}
