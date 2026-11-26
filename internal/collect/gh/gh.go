// Package gh coleta issues do GitHub pela Search API, buscando as frases de
// dor em issues com labels de pedido de funcionalidade.
package gh

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"gerador-ideias/internal/collect"
	"gerador-ideias/internal/httpx"
)

// Source é o identificador da fonte.
const Source = "gh"

// DefaultBaseURL é a API REST do GitHub.
const DefaultBaseURL = "https://api.github.com"

// A busca autenticada permite 30 requisições por minuto: uma a cada 2,1 s
// fica abaixo disso; os cabeçalhos X-RateLimit-* cobrem o resto.
const DefaultMinInterval = 2100 * time.Millisecond

const (
	perPage    = 100
	maxResults = 1000 // a busca nunca devolve mais que isso por consulta
)

// Collector implementa collect.Collector para o GitHub Issues.
type Collector struct {
	HTTP    *httpx.Client
	BaseURL string
	Token   string
	Phrases []string
	// Labels: a issue precisa ter pelo menos uma delas. Vazio = sem filtro de label.
	Labels []string
}

// New cria o coletor com os padrões de produção.
func New(token string, phrases, labels []string) *Collector {
	return &Collector{
		HTTP:    httpx.New(DefaultMinInterval),
		BaseURL: DefaultBaseURL,
		Token:   token,
		Phrases: phrases,
		Labels:  labels,
	}
}

// Source implementa collect.Collector.
func (c *Collector) Source() string { return Source }

// Collect busca cada frase na janela. Uma frase que falha não impede as outras.
func (c *Collector) Collect(ctx context.Context, req collect.Request, emit func([]collect.Item) error) error {
	if c.Token == "" {
		return errors.New("GITHUB_TOKEN não definido (variável de ambiente ou ~/.config/ideias/env)")
	}
	var errs []error
	for _, phrase := range c.Phrases {
		err := c.collectWindow(ctx, phrase, req.Since, req.Until, req.HashAuthor, emit)
		if err != nil {
			if ctx.Err() != nil {
				return errors.Join(append(errs, err)...)
			}
			errs = append(errs, fmt.Errorf("frase %q: %w", phrase, err))
		}
	}
	return errors.Join(errs...)
}

// collectWindow busca (since, until]. Se a janela tiver mais resultados do que
// a busca consegue devolver, divide-a ao meio e busca cada metade.
func (c *Collector) collectWindow(ctx context.Context, phrase string, since, until time.Time,
	hash func(string) string, emit func([]collect.Item) error) error {
	first, err := c.search(ctx, phrase, since, until, 1)
	if err != nil {
		return err
	}
	if first.TotalCount > maxResults && until.Sub(since) > 2*time.Second {
		mid := since.Add(until.Sub(since) / 2).Truncate(time.Second)
		if err := c.collectWindow(ctx, phrase, mid, until, hash, emit); err != nil {
			return err
		}
		return c.collectWindow(ctx, phrase, since, mid, hash, emit)
	}
	if err := emit(toItems(first.Items, hash)); err != nil {
		return err
	}
	pages := (min(first.TotalCount, maxResults) + perPage - 1) / perPage
	for page := 2; page <= pages; page++ {
		resp, err := c.search(ctx, phrase, since, until, page)
		if err != nil {
			return err
		}
		if len(resp.Items) == 0 {
			break
		}
		if err := emit(toItems(resp.Items, hash)); err != nil {
			return err
		}
	}
	return nil
}

type searchResponse struct {
	TotalCount        int     `json:"total_count"`
	IncompleteResults bool    `json:"incomplete_results"`
	Items             []issue `json:"items"`
}

type issue struct {
	ID            int64     `json:"id"`
	HTMLURL       string    `json:"html_url"`
	RepositoryURL string    `json:"repository_url"`
	Title         string    `json:"title"`
	Body          string    `json:"body"`
	CreatedAt     time.Time `json:"created_at"`
	Comments      int       `json:"comments"`
	Reactions     struct {
		PlusOne int `json:"+1"`
	} `json:"reactions"`
	User struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"user"`
	PullRequest *struct{} `json:"pull_request"`
}

// Query monta a busca: frase exata, só issues, labels (OU entre elas) e a janela.
// O intervalo do GitHub é fechado nas duas pontas; o segundo seguinte a since
// torna a janela (since, until].
func (c *Collector) Query(phrase string, since, until time.Time) string {
	parts := []string{"is:issue", `"` + strings.ReplaceAll(phrase, `"`, "") + `"`}
	if len(c.Labels) > 0 {
		quoted := make([]string, len(c.Labels))
		for i, l := range c.Labels {
			if strings.ContainsAny(l, " ,") {
				l = `"` + l + `"`
			}
			quoted[i] = l
		}
		parts = append(parts, "label:"+strings.Join(quoted, ","))
	}
	const layout = "2006-01-02T15:04:05Z"
	parts = append(parts, fmt.Sprintf("created:%s..%s",
		since.UTC().Add(time.Second).Format(layout), until.UTC().Format(layout)))
	return strings.Join(parts, " ")
}

func (c *Collector) search(ctx context.Context, phrase string, since, until time.Time, page int) (*searchResponse, error) {
	q := url.Values{}
	q.Set("q", c.Query(phrase, since, until))
	q.Set("sort", "created")
	q.Set("order", "desc")
	q.Set("per_page", strconv.Itoa(perPage))
	q.Set("page", strconv.Itoa(page))
	h := http.Header{
		"Authorization":        {"Bearer " + c.Token},
		"Accept":               {"application/vnd.github+json"},
		"X-Github-Api-Version": {"2022-11-28"},
	}
	var out searchResponse
	// incomplete_results: a busca estourou o tempo no GitHub. Tenta mais uma
	// vez; se continuar incompleta, aceita o que veio (a sobreposição da
	// próxima coleta e a deduplicação cobrem parte da diferença).
	for try := 0; try < 2; try++ {
		out = searchResponse{}
		if _, err := c.HTTP.SendJSON(ctx, http.MethodGet, c.BaseURL+"/search/issues?"+q.Encode(), h, nil, &out); err != nil {
			return nil, err
		}
		if !out.IncompleteResults {
			break
		}
	}
	return &out, nil
}

func toItems(issues []issue, hash func(string) string) []collect.Item {
	items := make([]collect.Item, 0, len(issues))
	for _, is := range issues {
		// Issues abertas por bots (dependabot, autopost...) não são dor de gente.
		if is.PullRequest != nil || is.User.Type == "Bot" || is.ID == 0 {
			continue
		}
		text := strings.TrimSpace(is.Title)
		if body := strings.TrimSpace(is.Body); body != "" {
			text += "\n\n" + body
		}
		author := ""
		if hash != nil {
			author = hash(is.User.Login)
		}
		var brands []string
		if repo := repoName(is.RepositoryURL); repo != "" {
			brands = []string{repo}
		}
		items = append(items, collect.Item{
			Source:     Source,
			ExternalID: strconv.FormatInt(is.ID, 10),
			URL:        is.HTMLURL,
			CreatedAt:  is.CreatedAt.UTC(),
			Text:       text,
			AuthorHash: author,
			Engagement: is.Reactions.PlusOne + is.Comments,
			Brands:     brands,
		})
	}
	return items
}

// repoName extrai "owner/repo" (em minúsculas) de https://api.github.com/repos/owner/repo.
func repoName(repositoryURL string) string {
	_, rest, ok := strings.Cut(repositoryURL, "/repos/")
	if !ok {
		return ""
	}
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return strings.ToLower(parts[0] + "/" + parts[1])
}
