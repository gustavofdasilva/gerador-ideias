package gh

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
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

func TestQuery(t *testing.T) {
	c := New("t", nil, []string{"enhancement", "feature request"})
	got := c.Query(`i'd "pay" for`, base.Add(-time.Hour), base)
	want := `is:issue "i'd pay for" label:enhancement,"feature request" created:2026-11-26T11:00:01Z..2026-11-26T12:00:00Z`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	c.Labels = nil
	if strings.Contains(c.Query("x", base, base), "label:") {
		t.Error("sem labels não deveria filtrar por label")
	}
}

func TestToItemsFromRecordedResponse(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "search_issues.json"))
	if err != nil {
		t.Fatal(err)
	}
	var resp searchResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatal(err)
	}
	items := toItems(resp.Items, func(a string) string { return "h(" + a + ")" })
	bots := 0
	for _, is := range resp.Items {
		if is.User.Type == "Bot" {
			bots++
		}
	}
	if bots == 0 || len(items) != len(resp.Items)-bots {
		t.Fatalf("%d itens de %d (bots: %d)", len(items), len(resp.Items), bots)
	}
	byID := map[string]issue{}
	for _, is := range resp.Items {
		byID[strconv.FormatInt(is.ID, 10)] = is
	}
	for _, it := range items {
		is := byID[it.ExternalID]
		if it.URL != is.HTMLURL || !strings.HasPrefix(it.URL, "https://github.com/") {
			t.Errorf("URL = %s", it.URL)
		}
		if len(it.Brands) != 1 || !strings.Contains(is.RepositoryURL, "/repos/") ||
			!strings.EqualFold(it.Brands[0], strings.SplitN(is.RepositoryURL, "/repos/", 2)[1]) {
			t.Errorf("marca = %v (repo %s)", it.Brands, is.RepositoryURL)
		}
		if it.Engagement != is.Reactions.PlusOne+is.Comments {
			t.Errorf("engajamento = %d", it.Engagement)
		}
		if !strings.HasPrefix(it.Text, strings.TrimSpace(is.Title)) || it.AuthorHash != "h("+is.User.Login+")" {
			t.Errorf("texto/autor: %q %q", it.Text[:min(40, len(it.Text))], it.AuthorHash)
		}
	}
	if got := repoName("https://api.github.com/repos/Rust-Lang/Leadership-Council"); got != "rust-lang/leadership-council" {
		t.Errorf("repoName = %q", got)
	}
}

// fakeGitHub imita /search/issues: interpreta frase, labels e o intervalo
// created, ordena do mais novo ao mais antigo e só deixa paginar até 1.000.
type fakeGitHub struct {
	t        *testing.T
	mu       sync.Mutex
	issues   map[string][]issue // frase -> issues
	requests int
	windows  map[string]bool // intervalos created consultados
}

var reCreated = regexp.MustCompile(`created:(\S+)\.\.(\S+)`)

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	if r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("User-Agent") != httpx.UserAgent {
		f.t.Errorf("cabeçalhos: %v", r.Header)
	}
	q := r.URL.Query().Get("q")
	m := reCreated.FindStringSubmatch(q)
	if m == nil || !strings.HasPrefix(q, "is:issue \"") || !strings.Contains(q, "label:enhancement") {
		f.t.Errorf("q = %s", q)
		http.Error(w, "bad", 422)
		return
	}
	f.windows[m[0]] = true
	from, _ := time.Parse(time.RFC3339, m[1])
	to, _ := time.Parse(time.RFC3339, m[2])
	phrase := strings.SplitN(q, `"`, 3)[1]
	var match []issue
	for _, is := range f.issues[phrase] {
		if !is.CreatedAt.Before(from) && !is.CreatedAt.After(to) {
			match = append(match, is)
		}
	}
	sort.SliceStable(match, func(i, j int) bool { return match[i].CreatedAt.After(match[j].CreatedAt) })
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	start := (page - 1) * per
	if start >= maxResults {
		http.Error(w, `{"message":"Only the first 1000 search results are available"}`, 422)
		return
	}
	resp := searchResponse{TotalCount: len(match), Items: []issue{}}
	if start < len(match) {
		resp.Items = match[start:min(start+per, len(match), maxResults)]
	}
	w.Header().Set("X-RateLimit-Remaining", "29")
	json.NewEncoder(w).Encode(resp)
}

func synth(id int, at time.Time) issue {
	is := issue{
		ID:            int64(id),
		HTMLURL:       fmt.Sprintf("https://github.com/dono/repo/issues/%d", id),
		RepositoryURL: "https://api.github.com/repos/Dono/Repo",
		Title:         fmt.Sprintf("Feature request %d: i would pay for this", id),
		CreatedAt:     at,
		Comments:      id % 3,
	}
	is.User.Login, is.User.Type = fmt.Sprintf("u%d", id%11), "User"
	return is
}

func newFake(t *testing.T) (*fakeGitHub, *Collector) {
	t.Helper()
	f := &fakeGitHub{t: t, issues: map[string][]issue{}, windows: map[string]bool{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := New("tok", []string{"i would pay"}, []string{"enhancement", "feature request"})
	c.BaseURL = srv.URL
	c.HTTP.MinInterval = 0
	c.HTTP.Sleep = func(ctx context.Context, d time.Duration) error { return ctx.Err() }
	return f, c
}

func TestSplitsWindowsOverThousandResults(t *testing.T) {
	f, c := newFake(t)
	const n = 2500
	for i := 1; i <= n; i++ {
		// 2.500 issues num só dia, várias no mesmo segundo.
		f.issues["i would pay"] = append(f.issues["i would pay"], synth(i, base.Add(-time.Duration(i*30)*time.Second)))
	}
	bot := synth(n+1, base.Add(-time.Minute))
	bot.User.Type = "Bot"
	f.issues["i would pay"] = append(f.issues["i would pay"], bot)

	got := map[string]int{}
	err := c.Collect(context.Background(), collect.Request{Since: base.Add(-48 * time.Hour), Until: base},
		func(items []collect.Item) error {
			for _, it := range items {
				got[it.ExternalID]++
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("recebeu %d issues distintas, want %d", len(got), n)
	}
	if got[strconv.Itoa(n+1)] != 0 {
		t.Error("issue de bot foi entregue")
	}
	if len(f.windows) < 3 {
		t.Errorf("a janela não foi dividida: %v", f.windows)
	}
}

func TestMissingTokenFails(t *testing.T) {
	c := New("", []string{"x"}, nil)
	err := c.Collect(context.Background(), collect.Request{Since: base.Add(-time.Hour), Until: base},
		func([]collect.Item) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "GITHUB_TOKEN") {
		t.Fatalf("err = %v", err)
	}
}

func TestEndToEndResumeAndBrands(t *testing.T) {
	ctx := context.Background()
	f, c := newFake(t)
	for i := 1; i <= 30; i++ {
		f.issues["i would pay"] = append(f.issues["i would pay"], synth(i, base.Add(-time.Duration(i)*time.Hour)))
	}
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

	res, err := r.Run(ctx, c)
	if err != nil || res.New != 30 {
		t.Fatalf("1ª: %+v %v", res, err)
	}
	now = base.Add(2 * time.Hour)
	f.issues["i would pay"] = append(f.issues["i would pay"], synth(99, base.Add(time.Hour)))
	res, err = r.Run(ctx, c)
	if err != nil || res.New != 1 || !res.FromCursor {
		t.Fatalf("2ª: %+v %v", res, err)
	}
	var total int
	var brands string
	conn.QueryRow(`SELECT count(*), max(brands) FROM items WHERE source = 'gh'`).Scan(&total, &brands)
	if total != 31 || brands != "dono/repo" {
		t.Errorf("items=%d brands=%q", total, brands)
	}
}
