// Package collect define a interface comum das fontes (design, seção 5.3) e o
// Runner, que cuida do que é igual para todas: cursor, retomada, gravação sem
// duplicados e registro da execução em runs (design, seção 5.1).
package collect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"gerador-ideias/internal/db"
)

// Item é o formato normalizado (design, 5.1 passo 2).
type Item = db.Item

// Request diz a um coletor o que buscar.
type Request struct {
	// Janela de busca: itens criados em (Since, Until].
	Since, Until time.Time
	// HashAuthor transforma o nome do autor num hash. O nome em si nunca é guardado.
	HashAuthor func(author string) string
}

// Collector é uma fonte de posts. Implementações não guardam estado entre
// execuções: o cursor e a deduplicação ficam com o Runner.
type Collector interface {
	// Source é o identificador curto da fonte ("hn", "bsky", "gh").
	Source() string
	// Collect busca os itens da janela e os entrega em lotes a emit, à medida
	// que chegam (assim uma interrupção não perde o que já foi baixado).
	// Pode entregar o mesmo item mais de uma vez. Se parte da busca falhar,
	// continua o que der e devolve os erros juntos (errors.Join) no fim.
	Collect(ctx context.Context, req Request, emit func([]Item) error) error
}

// Runner executa coletores contra o banco.
type Runner struct {
	DB  *sql.DB
	Now func() time.Time
	// InitialLookback é quanto buscar para trás quando a fonte não tem cursor
	// (primeira execução ou cursor apagado).
	InitialLookback time.Duration
	// Overlap re-busca o fim da janela anterior, para pegar itens indexados
	// com atraso. Os repetidos são descartados.
	Overlap time.Duration
}

// Result resume uma execução.
type Result struct {
	Source      string
	Since       time.Time
	Until       time.Time
	Seen        int // itens recebidos da fonte (sem repetição dentro da execução)
	New         int // itens gravados pela primeira vez
	Errors      int
	Status      string // ok, partial ou error
	Err         error
	FromCursor  bool // a janela começou no cursor salvo (e não no InitialLookback)
	CursorSaved bool
}

// Run executa um coletor: lê o cursor, busca a janela até agora, grava os
// itens novos, registra a execução em runs e só avança o cursor se a busca
// terminou sem erros. Assim, uma busca interrompida ou com falha é repetida
// por inteiro na próxima vez, sem duplicar o que já foi gravado.
func (r *Runner) Run(ctx context.Context, c Collector) (*Result, error) {
	source := c.Source()
	now := r.Now().UTC()
	res := &Result{Source: source, Until: now}

	cursor, ok, err := db.GetCursor(ctx, r.DB, source)
	if err != nil {
		return nil, err
	}
	if ok {
		res.Since = cursor.Add(-r.Overlap)
		res.FromCursor = true
	} else {
		res.Since = now.Add(-r.InitialLookback)
	}

	hash, err := db.AuthorHasher(ctx, r.DB)
	if err != nil {
		return nil, err
	}
	run, err := db.StartRun(ctx, r.DB, "collect", source, now)
	if err != nil {
		return nil, err
	}

	seen := map[string]struct{}{}
	emit := func(items []Item) error {
		fresh := items[:0:0]
		for _, it := range items {
			it.Source = source
			if _, dup := seen[it.ExternalID]; dup {
				continue
			}
			seen[it.ExternalID] = struct{}{}
			fresh = append(fresh, it)
		}
		n, err := db.InsertItems(ctx, r.DB, fresh, r.Now())
		res.Seen += len(fresh)
		res.New += n
		return err
	}
	req := Request{
		Since:      res.Since,
		Until:      res.Until,
		HashAuthor: func(a string) string { return hash(source, a) },
	}
	res.Err = c.Collect(ctx, req, emit)

	switch {
	case res.Err == nil:
		res.Status = "ok"
	case res.New > 0 || res.Seen > 0:
		res.Status = "partial"
	default:
		res.Status = "error"
	}
	res.Errors = countErrors(res.Err)

	// Mesmo com o contexto cancelado (Android matou o processo com SIGTERM),
	// tenta registrar o fim da execução.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if res.Err == nil {
		if err := db.SetCursor(saveCtx, r.DB, source, res.Until, r.Now()); err != nil {
			res.Err = fmt.Errorf("gravar cursor: %w", err)
			res.Status, res.Errors = "error", res.Errors+1
		} else {
			res.CursorSaved = true
		}
	}
	run.Status, run.ItemsSeen, run.ItemsNew, run.Errors = res.Status, res.Seen, res.New, res.Errors
	if res.Err != nil {
		run.ErrorText = res.Err.Error()
	}
	if err := db.FinishRun(saveCtx, r.DB, run, r.Now()); err != nil {
		return res, errors.Join(res.Err, fmt.Errorf("registrar execução: %w", err))
	}
	return res, res.Err
}

// countErrors conta os erros de um errors.Join (1 para um erro simples).
func countErrors(err error) int {
	if err == nil {
		return 0
	}
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		n := 0
		for _, e := range j.Unwrap() {
			n += countErrors(e)
		}
		return n
	}
	return 1
}

// Summary é uma linha legível do resultado.
func (r *Result) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s · %d vistos, %d novos", r.Source, r.Status, r.Seen, r.New)
	if r.Errors > 0 {
		fmt.Fprintf(&b, ", %d erro(s)", r.Errors)
	}
	origin := "sem cursor"
	if r.FromCursor {
		origin = "desde o cursor"
	}
	fmt.Fprintf(&b, " · janela %s → %s (%s)",
		r.Since.Local().Format("2006-01-02 15:04"), r.Until.Local().Format("2006-01-02 15:04"), origin)
	if !r.CursorSaved {
		b.WriteString(" · cursor mantido, a janela será repetida")
	}
	return b.String()
}
