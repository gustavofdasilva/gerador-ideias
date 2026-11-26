package bsky

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gerador-ideias/internal/collect"
	"gerador-ideias/internal/db"
	"gerador-ideias/internal/httpx"
	"gerador-ideias/internal/process"
)

var base = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

// A fixture é sintética (montada a partir do lexicon app.bsky.feed.searchPosts):
// a busca do Bluesky exige login, então não há resposta real gravada.
func TestToItemFromFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "search_posts_sintetico.json"))
	if err != nil {
		t.Fatal(err)
	}
	var resp searchResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatal(err)
	}
	var items []collect.Item
	for _, p := range resp.Posts {
		if it, ok := toItem(p, func(a string) string { return "h(" + a + ")" }); ok {
			items = append(items, it)
		}
	}
	if len(items) != 3 {
		t.Fatalf("%d itens (o post sem texto deveria ser descartado)", len(items))
	}
	pt := items[0]
	if pt.URL != "https://bsky.app/profile/did:plc:aaaa1111/post/3lnew001" || pt.ExternalID != "at://did:plc:aaaa1111/app.bsky.feed.post/3lnew001" {
		t.Errorf("URL/ID: %s %s", pt.URL, pt.ExternalID)
	}
	if pt.LangHint != "pt" || pt.Engagement != 15 || pt.AuthorHash != "h(did:plc:aaaa1111)" {
		t.Errorf("pt: %+v", pt)
	}
	if want := time.Date(2026, 11, 26, 10, 15, 30, 123e6, time.UTC); !pt.CreatedAt.Equal(want) {
		t.Errorf("created = %v", pt.CreatedAt)
	}
	// createdAt no futuro: vale o indexedAt.
	if want := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC); !items[1].CreatedAt.Equal(want) {
		t.Errorf("createdAt futuro não foi corrigido: %v", items[1].CreatedAt)
	}
	if items[2].LangHint != "" {
		t.Errorf("post sem langs: hint = %q", items[2].LangHint)
	}
}

// fakeBsky imita o servidor de entrada (login) e o PDS da conta (busca e
// renovação), com paginação por cursor e tokens que vencem.
type fakeBsky struct {
	t *testing.T

	mu         sync.Mutex
	posts      []post // todos, qualquer frase
	perPage    int
	logins     int
	refreshes  int
	searches   int
	expireNext bool   // a próxima busca responde ExpiredToken
	refreshBad bool   // a renovação falha (obriga novo login)
	access     string // token válido no momento
	gen        int
	pdsURL     string
	langs      map[string]bool
}

func (f *fakeBsky) newTokens() map[string]any {
	f.gen++
	f.access = "access-" + strconv.Itoa(f.gen)
	return map[string]any{
		"accessJwt": f.access, "refreshJwt": "refresh-" + strconv.Itoa(f.gen), "did": "did:plc:eu",
		"didDoc": map[string]any{"service": []map[string]string{
			{"id": "#atproto_pds", "type": "AtprotoPersonalDataServer", "serviceEndpoint": f.pdsURL},
		}},
	}
}

func (f *fakeBsky) entryway(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path != "/xrpc/com.atproto.server.createSession" || r.Method != http.MethodPost {
		f.t.Errorf("entrada recebeu %s %s (a busca deveria ir ao PDS)", r.Method, r.URL.Path)
		http.NotFound(w, r)
		return
	}
	var in map[string]string
	json.NewDecoder(r.Body).Decode(&in)
	if in["identifier"] != "eu.bsky.social" || in["password"] != "app-pass" {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":"AuthenticationRequired","message":"Invalid identifier or password"}`))
		return
	}
	f.logins++
	json.NewEncoder(w).Encode(f.newTokens())
}

func (f *fakeBsky) pds(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("User-Agent") != httpx.UserAgent {
		f.t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
	}
	switch r.URL.Path {
	case "/xrpc/com.atproto.server.refreshSession":
		if f.refreshBad || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer refresh-") {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":"ExpiredToken","message":"Token has expired"}`))
			return
		}
		f.refreshes++
		tok := f.newTokens()
		delete(tok, "didDoc") // como no servidor real, que pode omitir o didDoc
		json.NewEncoder(w).Encode(tok)
	case "/xrpc/app.bsky.feed.searchPosts":
		f.searches++
		if f.expireNext || r.Header.Get("Authorization") != "Bearer "+f.access {
			f.expireNext = false
			w.WriteHeader(400)
			w.Write([]byte(`{"error":"ExpiredToken","message":"Token has expired"}`))
			return
		}
		f.search(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeBsky) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	phrase := strings.Trim(q.Get("q"), `"`)
	if !strings.HasPrefix(q.Get("q"), `"`) || q.Get("sort") != "latest" {
		f.t.Errorf("parâmetros: %s", r.URL.RawQuery)
	}
	lang := q.Get("lang")
	f.langs[lang] = true
	since, err1 := time.Parse(time.RFC3339Nano, q.Get("since"))
	until, err2 := time.Parse(time.RFC3339Nano, q.Get("until"))
	if err1 != nil || err2 != nil {
		f.t.Errorf("since/until: %s", r.URL.RawQuery)
	}
	var match []post
	for _, p := range f.posts {
		at, _ := sortAt(p.Record.CreatedAt, p.IndexedAt)
		if strings.Contains(strings.ToLower(p.Record.Text), phrase) && (lang == "" || langHint(p.Record.Langs) == lang) &&
			!at.Before(since) && at.Before(until) {
			match = append(match, p)
		}
	}
	sort.SliceStable(match, func(i, j int) bool { return match[i].IndexedAt > match[j].IndexedAt })
	offset, _ := strconv.Atoi(q.Get("cursor"))
	resp := searchResponse{Posts: []post{}}
	if offset < len(match) {
		resp.Posts = match[offset:min(offset+f.perPage, len(match))]
		if offset+f.perPage < len(match) {
			resp.Cursor = strconv.Itoa(offset + f.perPage)
		}
	}
	json.NewEncoder(w).Encode(resp)
}

func mkPost(id int, at time.Time, text, lang string) post {
	var p post
	p.URI = fmt.Sprintf("at://did:plc:u%d/app.bsky.feed.post/r%d", id%5, id)
	p.Author.DID = fmt.Sprintf("did:plc:u%d", id%5)
	p.Record.Text = text
	p.Record.CreatedAt = at.Format(isoMillis)
	p.Record.Langs = []string{lang}
	p.IndexedAt = at.Format(isoMillis)
	p.LikeCount = id % 4
	return p
}

func newFake(t *testing.T) (*fakeBsky, *Collector) {
	t.Helper()
	f := &fakeBsky{t: t, perPage: 3, langs: map[string]bool{}}
	pds := httptest.NewServer(http.HandlerFunc(f.pds))
	t.Cleanup(pds.Close)
	f.pdsURL = pds.URL
	entry := httptest.NewServer(http.HandlerFunc(f.entryway))
	t.Cleanup(entry.Close)

	c := New("eu.bsky.social", "app-pass", []Query{
		{Phrase: "alternative to", Lang: "en"},
		{Phrase: "pagaria por", Lang: "pt"},
	})
	c.BaseURL = entry.URL
	c.HTTP.MinInterval = 0
	c.HTTP.Sleep = func(ctx context.Context, d time.Duration) error { return ctx.Err() }
	return f, c
}

func run(t *testing.T, c *Collector, req collect.Request) (map[string]collect.Item, error) {
	t.Helper()
	got := map[string]collect.Item{}
	err := c.Collect(context.Background(), req, func(items []collect.Item) error {
		for _, it := range items {
			got[it.ExternalID] = it
		}
		return nil
	})
	return got, err
}

func TestPaginationLangsAndSessionRenewal(t *testing.T) {
	f, c := newFake(t)
	for i := 0; i < 10; i++ {
		f.posts = append(f.posts, mkPost(i, base.Add(-time.Duration(i+1)*time.Hour), "Any alternative to Trello?", "en"))
	}
	for i := 10; i < 14; i++ {
		f.posts = append(f.posts, mkPost(i, base.Add(-time.Duration(i)*time.Hour), "Eu pagaria por um app assim", "pt-BR"))
	}
	f.posts = append(f.posts, mkPost(99, base.Add(-72*time.Hour), "old alternative to X", "en")) // fora da janela
	f.expireNext = true                                                                          // a 1ª busca encontra o token vencido

	got, err := run(t, c, collect.Request{Since: base.Add(-24 * time.Hour), Until: base, HashAuthor: func(a string) string { return a }})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 14 {
		t.Fatalf("%d posts, want 14", len(got))
	}
	if _, ok := got["at://did:plc:u4/app.bsky.feed.post/r99"]; ok {
		t.Error("post fora da janela foi entregue")
	}
	if f.logins != 1 || f.refreshes != 1 {
		t.Errorf("logins=%d renovações=%d (esperava 1 e 1)", f.logins, f.refreshes)
	}
	if !f.langs["en"] || !f.langs["pt"] {
		t.Errorf("idiomas buscados: %v", f.langs)
	}
	for _, it := range got {
		if strings.Contains(it.Text, "pagaria") && it.LangHint != "pt" {
			t.Errorf("lang_hint = %q", it.LangHint)
		}
	}
}

func TestRefreshFailureLogsInAgain(t *testing.T) {
	f, c := newFake(t)
	f.posts = append(f.posts, mkPost(1, base.Add(-time.Hour), "alternative to x", "en"))
	f.expireNext, f.refreshBad = true, true
	got, err := run(t, c, collect.Request{Since: base.Add(-24 * time.Hour), Until: base})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || f.logins != 2 {
		t.Errorf("posts=%d logins=%d", len(got), f.logins)
	}
}

func TestBadPasswordAndMissingCredentials(t *testing.T) {
	_, c := newFake(t)
	c.AppPassword = "errada"
	_, err := run(t, c, collect.Request{Since: base.Add(-time.Hour), Until: base})
	if err == nil || !strings.Contains(err.Error(), "confira BSKY_HANDLE") {
		t.Errorf("senha errada: %v", err)
	}
	c.Handle = ""
	_, err = run(t, c, collect.Request{Since: base.Add(-time.Hour), Until: base})
	if err == nil || !strings.Contains(err.Error(), "BSKY_HANDLE e BSKY_APP_PASSWORD") {
		t.Errorf("sem credenciais: %v", err)
	}
}

func TestDeepPaginationShiftsWindow(t *testing.T) {
	f, c := newFake(t)
	c.Queries = []Query{{Phrase: "alternative to", Lang: "en"}}
	for i := 0; i < 400; i++ {
		f.posts = append(f.posts, mkPost(i, base.Add(-time.Duration(i+1)*time.Minute), "alternative to y", "en"))
	}
	// 400 posts com 3 por página passam do limite de 50 páginas por janela.
	got, err := run(t, c, collect.Request{Since: base.Add(-24 * time.Hour), Until: base})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 400 {
		t.Errorf("%d posts, want 400", len(got))
	}
}

func TestEndToEndWithRunner(t *testing.T) {
	ctx := context.Background()
	f, c := newFake(t)
	f.posts = append(f.posts,
		mkPost(1, base.Add(-time.Hour), "alternative to Notion?", "en"),
		mkPost(2, base.Add(-2*time.Hour), "eu pagaria por isso", "pt"))
	conn, err := db.Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := db.Migrate(ctx, conn); err != nil {
		t.Fatal(err)
	}
	now := base
	r := &collect.Runner{DB: conn, Now: func() time.Time { return now }, Processor: process.Default(), InitialLookback: 7 * 24 * time.Hour, Overlap: time.Hour, MinTermCount: 1}
	for i := 0; i < 2; i++ {
		res, err := r.Run(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		if want := []int{2, 0}[i]; res.New != want {
			t.Fatalf("execução %d: %+v", i+1, res)
		}
		now = now.Add(time.Hour)
	}
	var langs string
	conn.QueryRow(`SELECT group_concat(lang_hint, ',') FROM (SELECT lang_hint FROM items ORDER BY lang_hint)`).Scan(&langs)
	if langs != "en,pt" {
		t.Errorf("lang_hint gravados = %q", langs)
	}
}
