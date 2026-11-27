package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gerador-ideias"
)

func TestExampleConfigIsValid(t *testing.T) {
	cfg, err := Parse(ideias.ExampleConfig)
	if err != nil {
		t.Fatalf("config.example.toml inválido: %v", err)
	}
	if got := cfg.Rubric.Sum(); got != 100 {
		t.Errorf("rubrica soma %d", got)
	}
	if len(cfg.Languages) != len(SupportedLanguages) {
		t.Errorf("languages = %v", cfg.Languages)
	}
	for _, lang := range cfg.Languages {
		if len(cfg.Pain.Phrases[lang]) == 0 {
			t.Errorf("sem frases para %s", lang)
		}
	}
	if len(cfg.Brands.Seed) == 0 {
		t.Error("sem marcas-semente")
	}
}

const minimal = `
db_path = "x.db"
languages = ["en"]
[pain]
min_score = 1.0
[storage]
max_candidates_per_day = 100
max_text_chars = 1200
keep_term_weeks = 8
[alerts]
failing_days = 2
stale_hours = 48
repeat_hours = 24
[backup]
dir = ""
[report]
dir = "reports"
export_dir = "export"
top_candidates = 15
top_keywords = 15
top_brands = 15
min_keyword_count = 3
[keywords]
max_n = 3
max_per_item = 40
min_count = 2
[pain.weights]
request = 1.0
[pain.phrases.en]
request = ["  Is There An App  "]
[rubric]
frequency = 10
payment = 20
distribution = 15
b2b = 10
global = 10
solo = 15
sellability = 10
infra = 10
[ai]
group_model = "a"
score_model = "b"
weekly_token_limit = 1000
max_items = 400
batch_size = 50
item_chars = 500
max_clusters = 150
score_top = 20
penalty_points = 15
bonus_points = 5
timeout_seconds = 300
manual_part_chars = 120000
[collect]
initial_lookback_days = 7
overlap_minutes = 60
sources = ["hn", "bsky", "gh"]
[sources.hn]
languages = ["EN"]
[sources.bsky]
languages = ["en"]
[sources.gh]
languages = ["en"]
labels = ["enhancement"]
`

func TestPhrasesFor(t *testing.T) {
	cfg, err := Parse(ideias.ExampleConfig)
	if err != nil {
		t.Fatal(err)
	}
	en := cfg.PhrasesFor([]string{"en"})
	if len(en) == 0 {
		t.Fatal("sem frases em inglês")
	}
	seen := map[string]bool{}
	for _, p := range en {
		if seen[p] {
			t.Errorf("frase repetida: %q", p)
		}
		seen[p] = true
	}
	if !seen["alternative to"] || seen["pagaria por"] {
		t.Errorf("frases inesperadas: %v", en)
	}
	if got := cfg.PhrasesFor(cfg.Sources.HN.Languages); len(got) != len(en) {
		t.Errorf("sources.hn.languages do exemplo deveria ser só en")
	}
}

func TestParseNormalizesPhrases(t *testing.T) {
	cfg, err := Parse([]byte(minimal))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Pain.Phrases["en"]["request"]; len(got) != 1 || got[0] != "is there an app" {
		t.Errorf("frases = %q", got)
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(string) string
		wantErr string
	}{
		{"idioma não suportado", func(s string) string {
			return strings.Replace(s, `languages = ["en"]`, `languages = ["en", "it"]`, 1)
		}, `"it" não suportado`},
		{"rubrica não soma 100", func(s string) string {
			return strings.Replace(s, "infra = 10", "infra = 11", 1)
		}, "somam 101"},
		{"tipo sem peso", func(s string) string {
			return strings.Replace(s, `request = ["  Is`, `payment = ["x"]`+"\n"+`request = ["  Is`, 1)
		}, "pain.phrases.en.payment: tipo sem peso"},
		{"chave desconhecida", func(s string) string {
			return "dbpath = \"y\"\n" + s
		}, "chaves desconhecidas: dbpath"},
		{"limite de tokens zero", func(s string) string {
			return strings.Replace(s, "weekly_token_limit = 1000", "weekly_token_limit = 0", 1)
		}, "weekly_token_limit"},
		{"hn com idioma desabilitado", func(s string) string {
			return strings.Replace(s, `languages = ["EN"]`, `languages = ["pt"]`, 1)
		}, "sources.hn.languages sem nenhum idioma de languages"},
		{"lookback zero", func(s string) string {
			return strings.Replace(s, "initial_lookback_days = 7", "initial_lookback_days = 0", 1)
		}, "initial_lookback_days"},
		{"sem db_path", func(s string) string {
			return strings.Replace(s, `db_path = "x.db"`, `db_path = ""`, 1)
		}, "db_path vazio"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.mutate(minimal)))
			if err == nil {
				t.Fatal("esperava erro")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("erro %q não contém %q", err, tc.wantErr)
			}
		})
	}
}

func TestLoadPrecedence(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Source != EmbeddedSource {
		t.Errorf("sem arquivos, Source = %q", cfg.Source)
	}

	homeCfg := filepath.Join(dir, ".config", "ideias", "config.toml")
	if err := os.MkdirAll(filepath.Dir(homeCfg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(homeCfg, []byte(strings.Replace(minimal, "x.db", "home.db", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DBPath != "home.db" {
		t.Errorf("esperava o config da home, veio %q", cfg.DBPath)
	}

	if err := os.WriteFile("config.toml", []byte(strings.Replace(minimal, "x.db", "local.db", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DBPath != "local.db" {
		t.Errorf("./config.toml deveria vencer, veio %q", cfg.DBPath)
	}

	if _, err := Load(filepath.Join(dir, "nao-existe.toml")); err == nil {
		t.Error("--config apontando para arquivo inexistente deveria falhar")
	}
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	got, _ := ExpandHome("~/a/b.db")
	if want := filepath.Join(home, "a", "b.db"); got != want {
		t.Errorf("got %q want %q", got, want)
	}
	if got, _ := ExpandHome("/abs/x.db"); got != "/abs/x.db" {
		t.Errorf("caminho absoluto alterado: %q", got)
	}
}

func TestDisabledLanguageTablesAreIgnored(t *testing.T) {
	src := minimal + "\n[pain.phrases.pt]\nrequest = [\"tem algum app\"]\n[brands.patterns]\npt = [\"alternativa ao {marca}\"]\n"
	if _, err := Parse([]byte(src)); err != nil {
		t.Fatalf("idioma desligado deveria ser ignorado: %v", err)
	}
}

// Um config.toml antigo, sem as seções que versões novas acrescentam,
// continua valendo: o que falta vem do config.example.toml embutido.
func TestOldConfigGetsDefaults(t *testing.T) {
	old := strings.NewReplacer(
		"[storage]\nmax_candidates_per_day = 100\nmax_text_chars = 1200\nkeep_term_weeks = 8\n", "",
		"[alerts]\nfailing_days = 2\nstale_hours = 48\nrepeat_hours = 24\n", "",
	).Replace(minimal)
	if strings.Contains(old, "[storage]") {
		t.Fatal("o teste não removeu [storage]")
	}
	if _, err := Parse([]byte(old)); err == nil {
		t.Fatal("sem padrões, o config antigo deveria falhar")
	}
	cfg, err := ParseOverDefaults([]byte(old))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Storage.MaxCandidatesPerDay != 100 || cfg.Alerts.StaleHours != 48 {
		t.Errorf("padrões não aplicados: %+v %+v", cfg.Storage, cfg.Alerts)
	}
	// O que o arquivo define vale; tabelas por idioma substituem as do padrão.
	if cfg.DBPath != "x.db" || len(cfg.Languages) != 1 {
		t.Errorf("valores do arquivo perdidos: %q %v", cfg.DBPath, cfg.Languages)
	}
	if len(cfg.Pain.Phrases) != 1 || len(cfg.Pain.Weights) != 1 {
		t.Errorf("frases/pesos somados aos padrões: %v %v", cfg.Pain.Phrases, cfg.Pain.Weights)
	}
}
