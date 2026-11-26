// Package hn coleta stories e comments do Hacker News pela API pública do
// Algolia (search_by_date), buscando cada frase de dor como frase exata.
package hn

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gerador-ideias/internal/collect"
	"gerador-ideias/internal/httpx"
)

// Source é o identificador da fonte.
const Source = "hn"

// DefaultBaseURL é a API do Algolia para o HN.
const DefaultBaseURL = "https://hn.algolia.com/api/v1"

// DefaultMinInterval mantém o coletor bem abaixo do limite público do
// Algolia (10.000 requisições por hora por IP): no máximo 2 por segundo.
const DefaultMinInterval = 500 * time.Millisecond

// Collector implementa collect.Collector para o HN.
type Collector struct {
	HTTP    *httpx.Client
	BaseURL string
	Phrases []string
	// HitsPerPage: itens por página (o Algolia aceita até 1.000).
	HitsPerPage int
}

// New cria o coletor com os padrões de produção.
func New(phrases []string) *Collector {
	return &Collector{
		HTTP:        httpx.New(DefaultMinInterval),
		BaseURL:     DefaultBaseURL,
		Phrases:     phrases,
		HitsPerPage: 100,
	}
}

// Source implementa collect.Collector.
func (c *Collector) Source() string { return Source }

// Collect busca cada frase na janela. Uma frase que falha não impede as outras.
func (c *Collector) Collect(ctx context.Context, req collect.Request, emit func([]collect.Item) error) error {
	var errs []error
	for _, phrase := range c.Phrases {
		if err := c.collectPhrase(ctx, phrase, req, emit); err != nil {
			if ctx.Err() != nil {
				return errors.Join(append(errs, err)...)
			}
			errs = append(errs, fmt.Errorf("frase %q: %w", phrase, err))
		}
	}
	return errors.Join(errs...)
}

// collectPhrase pagina a busca de uma frase. O Algolia só entrega os 1.000
// primeiros resultados de uma consulta (do mais novo para o mais antigo);
// quando a janela tem mais que isso, a busca recomeça com o limite superior
// recuado até o item mais antigo já recebido, até cobrir a janela inteira.
func (c *Collector) collectPhrase(ctx context.Context, phrase string, req collect.Request, emit func([]collect.Item) error) error {
	since := req.Since.Unix()
	upper := req.Until.Unix()
	for upper > since {
		var (
			received int
			total    int
			oldest   = upper
		)
		for page := 0; ; page++ {
			resp, err := c.search(ctx, phrase, since, upper, page)
			if err != nil {
				return err
			}
			if page == 0 {
				total = resp.NbHits
			}
			items := make([]collect.Item, 0, len(resp.Hits))
			for _, h := range resp.Hits {
				if h.CreatedAtI < oldest {
					oldest = h.CreatedAtI
				}
				if it, ok := toItem(h, req.HashAuthor); ok {
					items = append(items, it)
				}
			}
			received += len(resp.Hits)
			if err := emit(items); err != nil {
				return err
			}
			if len(resp.Hits) == 0 || page+1 >= resp.NbPages {
				break
			}
		}
		if received >= total {
			return nil // a janela coube no limite de paginação
		}
		// Limite inclusivo: os itens do mesmo segundo que o mais antigo voltam
		// e são descartados como repetidos. Se a página toda era desse
		// segundo, recua um segundo para não entrar em laço.
		if oldest >= upper {
			oldest = upper - 1
		}
		upper = oldest
	}
	return nil
}

type searchResponse struct {
	Hits    []hit `json:"hits"`
	NbHits  int   `json:"nbHits"`
	NbPages int   `json:"nbPages"`
	Page    int   `json:"page"`
}

type hit struct {
	ObjectID    string   `json:"objectID"`
	CreatedAtI  int64    `json:"created_at_i"`
	Author      string   `json:"author"`
	Title       string   `json:"title"`
	StoryText   string   `json:"story_text"`
	CommentText string   `json:"comment_text"`
	Points      *int     `json:"points"`
	NumComments *int     `json:"num_comments"`
	Children    []int64  `json:"children"`
	Tags        []string `json:"_tags"`
}

var retrieve = strings.Join([]string{
	"objectID", "created_at_i", "author", "title", "story_text", "comment_text",
	"points", "num_comments", "children", "_tags",
}, ",")

func (c *Collector) search(ctx context.Context, phrase string, since, upper int64, page int) (*searchResponse, error) {
	q := url.Values{}
	q.Set("query", `"`+strings.ReplaceAll(phrase, `"`, "")+`"`) // frase exata
	q.Set("advancedSyntax", "true")
	q.Set("tags", "(story,comment)")
	q.Set("numericFilters", fmt.Sprintf("created_at_i>%d,created_at_i<=%d", since, upper))
	q.Set("hitsPerPage", strconv.Itoa(c.HitsPerPage))
	q.Set("page", strconv.Itoa(page))
	q.Set("attributesToRetrieve", retrieve)
	q.Set("attributesToHighlight", "")
	var out searchResponse
	if _, err := c.HTTP.GetJSON(ctx, c.BaseURL+"/search_by_date?"+q.Encode(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func toItem(h hit, hashAuthor func(string) string) (collect.Item, bool) {
	if h.ObjectID == "" || h.CreatedAtI == 0 {
		return collect.Item{}, false
	}
	var text string
	engagement := 0
	if h.Points != nil {
		engagement += *h.Points
	}
	if hasTag(h.Tags, "comment") {
		text = htmlToText(h.CommentText)
		engagement += len(h.Children) // comments não têm pontos: usa as respostas
	} else {
		text = h.Title
		if body := htmlToText(h.StoryText); body != "" {
			text += "\n\n" + body
		}
		if h.NumComments != nil {
			engagement += *h.NumComments
		}
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return collect.Item{}, false
	}
	author := ""
	if hashAuthor != nil {
		author = hashAuthor(h.Author)
	}
	return collect.Item{
		Source:     Source,
		ExternalID: h.ObjectID,
		URL:        "https://news.ycombinator.com/item?id=" + h.ObjectID,
		CreatedAt:  time.Unix(h.CreatedAtI, 0).UTC(),
		Text:       text,
		AuthorHash: author,
		Engagement: engagement,
	}, true
}

func hasTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

var (
	reLink   = regexp.MustCompile(`(?is)<a\s[^>]*href="([^"]*)"[^>]*>.*?</a>`)
	reBreak  = regexp.MustCompile(`(?i)<p>|<br\s*/?>`)
	reTag    = regexp.MustCompile(`<[^>]+>`)
	reSpaces = regexp.MustCompile(`[ \t]+`)
	reBlank  = regexp.MustCompile(`\n{3,}`)
)

// htmlToText converte o HTML simples do HN em texto: parágrafos viram linhas
// em branco, links viram a URL completa (o texto do link costuma vir cortado)
// e as entidades são decodificadas.
func htmlToText(s string) string {
	if s == "" {
		return ""
	}
	s = reLink.ReplaceAllString(s, "$1")
	s = reBreak.ReplaceAllString(s, "\n\n")
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = reSpaces.ReplaceAllString(s, " ")
	s = reBlank.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}
