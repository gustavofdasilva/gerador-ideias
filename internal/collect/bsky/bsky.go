// Package bsky coleta posts do Bluesky com app.bsky.feed.searchPosts,
// autenticado com app password (a busca pública exige login).
package bsky

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"gerador-ideias/internal/collect"
	"gerador-ideias/internal/httpx"
)

// Source é o identificador da fonte.
const Source = "bsky"

// DefaultBaseURL é o servidor de entrada das contas hospedadas pelo Bluesky.
// Depois do login, as chamadas vão para o PDS da conta (informado no didDoc).
const DefaultBaseURL = "https://bsky.social"

// A AppView aceita 3.000 requisições a cada 5 minutos por IP; 3 por segundo
// fica muito abaixo. Os cabeçalhos RateLimit-* cobrem o resto.
const DefaultMinInterval = 350 * time.Millisecond

const (
	pageLimit = 100 // máximo do searchPosts
	// Depois de tantas páginas na mesma consulta, recomeça com "until" recuado
	// até o post mais antigo já visto, como no HN, para não depender de quão
	// fundo o cursor de paginação da busca consegue ir.
	maxPagesPerWindow = 50
)

// Query é uma frase de dor buscada num idioma.
type Query struct {
	Phrase string
	Lang   string
}

// Collector implementa collect.Collector para o Bluesky.
type Collector struct {
	HTTP        *httpx.Client
	BaseURL     string
	Handle      string
	AppPassword string
	Queries     []Query

	mu  sync.Mutex
	ses *session
}

// New cria o coletor com os padrões de produção.
func New(handle, appPassword string, queries []Query) *Collector {
	return &Collector{
		HTTP:        httpx.New(DefaultMinInterval),
		BaseURL:     DefaultBaseURL,
		Handle:      handle,
		AppPassword: appPassword,
		Queries:     queries,
	}
}

// Source implementa collect.Collector.
func (c *Collector) Source() string { return Source }

// Collect faz login uma vez e busca cada frase no seu idioma.
func (c *Collector) Collect(ctx context.Context, req collect.Request, emit func([]collect.Item) error) error {
	if c.Handle == "" || c.AppPassword == "" {
		return errors.New("BSKY_HANDLE e BSKY_APP_PASSWORD precisam estar definidos (variável de ambiente ou ~/.config/ideias/env)")
	}
	if err := c.login(ctx); err != nil {
		return err
	}
	var errs []error
	for _, q := range c.Queries {
		if err := c.collectQuery(ctx, q, req, emit); err != nil {
			if ctx.Err() != nil {
				return errors.Join(append(errs, err)...)
			}
			errs = append(errs, fmt.Errorf("frase %q (%s): %w", q.Phrase, q.Lang, err))
		}
	}
	return errors.Join(errs...)
}

func (c *Collector) collectQuery(ctx context.Context, q Query, req collect.Request, emit func([]collect.Item) error) error {
	until := req.Until
	for until.After(req.Since) {
		oldest := until
		cursor := ""
		pages := 0
		for {
			resp, err := c.search(ctx, q, req.Since, until, cursor)
			if err != nil {
				return err
			}
			pages++
			items := make([]collect.Item, 0, len(resp.Posts))
			for _, p := range resp.Posts {
				it, ok := toItem(p, req.HashAuthor)
				if !ok {
					continue
				}
				if it.CreatedAt.Before(oldest) {
					oldest = it.CreatedAt
				}
				items = append(items, it)
			}
			if err := emit(items); err != nil {
				return err
			}
			if resp.Cursor == "" || len(resp.Posts) == 0 {
				return nil // a busca acabou: a janela inteira foi coberta
			}
			cursor = resp.Cursor
			if pages >= maxPagesPerWindow {
				break
			}
		}
		// Recomeça a busca do post mais antigo visto (inclusive: os
		// repetidos são descartados), sempre recuando pelo menos 1 ms.
		next := oldest.Add(time.Millisecond)
		if !next.Before(until) {
			next = until.Add(-time.Millisecond)
		}
		until = next
	}
	return nil
}

// --- sessão ----------------------------------------------------------------

type session struct {
	AccessJwt  string `json:"accessJwt"`
	RefreshJwt string `json:"refreshJwt"`
	DID        string `json:"did"`
	DIDDoc     struct {
		Service []struct {
			ID              string `json:"id"`
			Type            string `json:"type"`
			ServiceEndpoint string `json:"serviceEndpoint"`
		} `json:"service"`
	} `json:"didDoc"`
}

// pds devolve o servidor da conta (onde as chamadas autenticadas devem ir).
func (s *session) pds(fallback string) string {
	for _, svc := range s.DIDDoc.Service {
		if svc.ID == "#atproto_pds" || svc.Type == "AtprotoPersonalDataServer" {
			if u := strings.TrimRight(svc.ServiceEndpoint, "/"); strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
				return u
			}
		}
	}
	return fallback
}

func (c *Collector) login(ctx context.Context) error {
	var s session
	body := map[string]string{"identifier": c.Handle, "password": c.AppPassword}
	_, err := c.HTTP.SendJSON(ctx, http.MethodPost, c.BaseURL+"/xrpc/com.atproto.server.createSession", nil, body, &s)
	if err != nil {
		var se *httpx.StatusError
		if errors.As(err, &se) && (se.Status == 401 || strings.Contains(se.Body, "AuthenticationRequired")) {
			return fmt.Errorf("login no Bluesky recusado: confira BSKY_HANDLE e BSKY_APP_PASSWORD (%w)", err)
		}
		return fmt.Errorf("login no Bluesky: %w", err)
	}
	if s.AccessJwt == "" {
		return errors.New("login no Bluesky: resposta sem accessJwt")
	}
	c.mu.Lock()
	c.ses = &s
	c.mu.Unlock()
	return nil
}

// refresh renova a sessão com o refreshJwt; se não der, faz login de novo.
func (c *Collector) refresh(ctx context.Context) error {
	c.mu.Lock()
	old := c.ses
	c.mu.Unlock()
	if old != nil && old.RefreshJwt != "" {
		var s session
		h := http.Header{"Authorization": {"Bearer " + old.RefreshJwt}}
		_, err := c.HTTP.SendJSON(ctx, http.MethodPost, old.pds(c.BaseURL)+"/xrpc/com.atproto.server.refreshSession", h, nil, &s)
		if err == nil && s.AccessJwt != "" {
			if len(s.DIDDoc.Service) == 0 {
				s.DIDDoc = old.DIDDoc
			}
			c.mu.Lock()
			c.ses = &s
			c.mu.Unlock()
			return nil
		}
	}
	return c.login(ctx)
}

// expired diz se o erro é de token vencido ou inválido.
func expired(err error) bool {
	var se *httpx.StatusError
	if !errors.As(err, &se) {
		return false
	}
	return se.Status == 401 || strings.Contains(se.Body, "ExpiredToken") || strings.Contains(se.Body, "InvalidToken")
}

// --- busca -----------------------------------------------------------------

type searchResponse struct {
	Cursor string `json:"cursor"`
	Posts  []post `json:"posts"`
}

type post struct {
	URI    string `json:"uri"`
	Author struct {
		DID string `json:"did"`
	} `json:"author"`
	Record struct {
		Text      string   `json:"text"`
		CreatedAt string   `json:"createdAt"`
		Langs     []string `json:"langs"`
	} `json:"record"`
	IndexedAt   string `json:"indexedAt"`
	LikeCount   int    `json:"likeCount"`
	RepostCount int    `json:"repostCount"`
}

const isoMillis = "2006-01-02T15:04:05.000Z"

func (c *Collector) search(ctx context.Context, q Query, since, until time.Time, cursor string) (*searchResponse, error) {
	v := url.Values{}
	v.Set("q", `"`+strings.ReplaceAll(q.Phrase, `"`, "")+`"`) // frase exata
	v.Set("sort", "latest")
	// since é inclusivo e until exclusivo na API; os ajustes de 1 ms dão (since, until].
	v.Set("since", since.UTC().Add(time.Millisecond).Format(isoMillis))
	v.Set("until", until.UTC().Add(time.Millisecond).Format(isoMillis))
	if q.Lang != "" {
		v.Set("lang", q.Lang)
	}
	v.Set("limit", strconv.Itoa(pageLimit))
	if cursor != "" {
		v.Set("cursor", cursor)
	}
	for attempt := 0; ; attempt++ {
		c.mu.Lock()
		s := c.ses
		c.mu.Unlock()
		h := http.Header{"Authorization": {"Bearer " + s.AccessJwt}}
		var out searchResponse
		_, err := c.HTTP.SendJSON(ctx, http.MethodGet, s.pds(c.BaseURL)+"/xrpc/app.bsky.feed.searchPosts?"+v.Encode(), h, nil, &out)
		if err == nil {
			return &out, nil
		}
		if attempt == 0 && expired(err) {
			if rerr := c.refresh(ctx); rerr != nil {
				return nil, fmt.Errorf("renovar sessão: %w", rerr)
			}
			continue
		}
		return nil, err
	}
}

func toItem(p post, hash func(string) string) (collect.Item, bool) {
	rkey := p.URI[strings.LastIndex(p.URI, "/")+1:]
	if p.URI == "" || p.Author.DID == "" || rkey == "" || strings.TrimSpace(p.Record.Text) == "" {
		return collect.Item{}, false
	}
	created, ok := sortAt(p.Record.CreatedAt, p.IndexedAt)
	if !ok {
		return collect.Item{}, false
	}
	author := ""
	if hash != nil {
		author = hash(p.Author.DID)
	}
	return collect.Item{
		Source:     Source,
		ExternalID: p.URI,
		URL:        "https://bsky.app/profile/" + p.Author.DID + "/post/" + rkey,
		CreatedAt:  created,
		Text:       strings.TrimSpace(p.Record.Text),
		AuthorHash: author,
		LangHint:   langHint(p.Record.Langs),
		Engagement: p.LikeCount + p.RepostCount,
	}, true
}

// sortAt é a data que a própria busca usa: a menor entre createdAt (escrita
// pelo cliente, pode vir errada ou no futuro) e indexedAt.
func sortAt(createdAt, indexedAt string) (time.Time, bool) {
	c, errC := time.Parse(time.RFC3339Nano, createdAt)
	i, errI := time.Parse(time.RFC3339Nano, indexedAt)
	switch {
	case errC == nil && errI == nil:
		if c.After(i) {
			return i.UTC(), true
		}
		return c.UTC(), true
	case errI == nil:
		return i.UTC(), true
	case errC == nil:
		return c.UTC(), true
	}
	return time.Time{}, false
}

// langHint devolve o primeiro idioma declarado, só o código base ("pt-BR" → "pt").
func langHint(langs []string) string {
	if len(langs) == 0 {
		return ""
	}
	l, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(langs[0])), "-")
	return l
}
