// Package config carrega e valida o config.toml.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"gerador-ideias"
)

// SupportedLanguages são os idiomas que a v1 sabe detectar (design, seção 5.1).
var SupportedLanguages = []string{"en", "pt", "es", "de", "fr"}

// Config espelha o config.toml.
type Config struct {
	DBPath    string   `toml:"db_path"`
	Languages []string `toml:"languages"`
	Pain      Pain     `toml:"pain"`
	Brands    Brands   `toml:"brands"`
	Rubric    Rubric   `toml:"rubric"`
	AI        AI       `toml:"ai"`
	Collect   Collect  `toml:"collect"`
	Sources   Sources  `toml:"sources"`

	// Source é de onde a configuração veio: o caminho do arquivo ou "embutido".
	Source string `toml:"-"`
}

// Pain define as frases de dor e os pesos por tipo.
type Pain struct {
	EngagementBonusMax float64 `toml:"engagement_bonus_max"`
	// Weights: tipo de frase -> peso.
	Weights map[string]float64 `toml:"weights"`
	// Phrases: idioma -> tipo de frase -> frases.
	Phrases map[string]map[string][]string `toml:"phrases"`
}

// Brands guarda a lista-semente de marcas.
type Brands struct {
	Seed []string `toml:"seed"`
}

// Rubric são os pesos (%) da rubrica de viabilidade (design, seção 6).
type Rubric struct {
	Frequency    int `toml:"frequency"`
	Payment      int `toml:"payment"`
	Distribution int `toml:"distribution"`
	B2B          int `toml:"b2b"`
	Global       int `toml:"global"`
	Solo         int `toml:"solo"`
	Sellability  int `toml:"sellability"`
	Infra        int `toml:"infra"`
}

// Sum devolve a soma dos pesos.
func (r Rubric) Sum() int {
	return r.Frequency + r.Payment + r.Distribution + r.B2B + r.Global + r.Solo + r.Sellability + r.Infra
}

// AI configura o uso do OmniRoute. URL e chave ficam nos segredos.
type AI struct {
	GroupModel       string `toml:"group_model"`
	ScoreModel       string `toml:"score_model"`
	WeeklyTokenLimit int64  `toml:"weekly_token_limit"`
}

// Collect controla a janela de coleta (design, 5.1 passo 1).
type Collect struct {
	// Sem cursor (primeira execução ou cursor apagado), busca os últimos N dias.
	InitialLookbackDays int `toml:"initial_lookback_days"`
	// Re-busca os últimos N minutos da janela anterior (atraso de indexação).
	OverlapMinutes int `toml:"overlap_minutes"`
}

// InitialLookback devolve InitialLookbackDays como duração.
func (c Collect) InitialLookback() time.Duration {
	return time.Duration(c.InitialLookbackDays) * 24 * time.Hour
}

// Overlap devolve OverlapMinutes como duração.
func (c Collect) Overlap() time.Duration {
	return time.Duration(c.OverlapMinutes) * time.Minute
}

// Sources guarda opções por fonte.
type Sources struct {
	HN HNSource `toml:"hn"`
}

// HNSource configura o coletor do Hacker News.
type HNSource struct {
	// Idiomas cujas frases de dor são buscadas no HN (quase tudo lá é inglês).
	Languages []string `toml:"languages"`
}

// PhrasesFor devolve as frases (sem repetição, em ordem estável) dos idiomas dados.
func (c *Config) PhrasesFor(langs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, lang := range langs {
		byType := c.Pain.Phrases[lang]
		for _, typ := range sortedKeys(byType) {
			for _, p := range byType[typ] {
				if !seen[p] {
					seen[p] = true
					out = append(out, p)
				}
			}
		}
	}
	return out
}

// EmbeddedSource é o valor de Config.Source quando se usa o config.example.toml embutido.
const EmbeddedSource = "embutido (config.example.toml)"

// SearchPaths devolve os caminhos procurados, em ordem de prioridade.
func SearchPaths() []string {
	paths := []string{"config.toml"}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".config", "ideias", "config.toml"))
	}
	return paths
}

// Load lê a configuração. Se explicit não for vazio, só esse arquivo é aceito.
// Senão, usa o primeiro de SearchPaths que existir; se nenhum existir, usa o
// config.example.toml embutido.
func Load(explicit string) (*Config, error) {
	if explicit != "" {
		return LoadFile(explicit)
	}
	for _, p := range SearchPaths() {
		if _, err := os.Stat(p); err == nil {
			return LoadFile(p)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("config: %w", err)
		}
	}
	cfg, err := Parse(ideias.ExampleConfig)
	if err != nil {
		return nil, fmt.Errorf("config embutido: %w", err)
	}
	cfg.Source = EmbeddedSource
	return cfg, nil
}

// LoadFile lê e valida um arquivo específico.
func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	cfg.Source = path
	return cfg, nil
}

// Parse decodifica, normaliza e valida o TOML.
func Parse(data []byte) (*Config, error) {
	var cfg Config
	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return nil, err
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("chaves desconhecidas: %s", strings.Join(keys, ", "))
	}
	cfg.normalize()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) normalize() {
	for i, l := range c.Languages {
		c.Languages[i] = strings.ToLower(strings.TrimSpace(l))
	}
	for i, l := range c.Sources.HN.Languages {
		c.Sources.HN.Languages[i] = strings.ToLower(strings.TrimSpace(l))
	}
	phrases := make(map[string]map[string][]string, len(c.Pain.Phrases))
	for lang, byType := range c.Pain.Phrases {
		norm := make(map[string][]string, len(byType))
		for typ, list := range byType {
			for _, p := range list {
				if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
					norm[typ] = append(norm[typ], p)
				}
			}
		}
		phrases[strings.ToLower(lang)] = norm
	}
	c.Pain.Phrases = phrases
}

// Validate confere as regras que o resto do programa assume.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if strings.TrimSpace(c.DBPath) == "" {
		add("db_path vazio")
	}
	if len(c.Languages) == 0 {
		add("languages vazio")
	}
	enabled := map[string]bool{}
	for _, l := range c.Languages {
		if !contains(SupportedLanguages, l) {
			add("idioma %q não suportado (use: %s)", l, strings.Join(SupportedLanguages, ", "))
		}
		if enabled[l] {
			add("idioma %q repetido", l)
		}
		enabled[l] = true
	}

	if c.Pain.EngagementBonusMax < 0 {
		add("pain.engagement_bonus_max negativo")
	}
	if len(c.Pain.Weights) == 0 {
		add("pain.weights vazio")
	}
	for typ, w := range c.Pain.Weights {
		if w <= 0 {
			add("pain.weights.%s precisa ser maior que zero", typ)
		}
	}
	for _, lang := range sortedKeys(c.Pain.Phrases) {
		if !enabled[lang] {
			add("pain.phrases.%s: idioma fora de languages", lang)
		}
		for _, typ := range sortedKeys(c.Pain.Phrases[lang]) {
			if _, ok := c.Pain.Weights[typ]; !ok {
				add("pain.phrases.%s.%s: tipo sem peso em pain.weights", lang, typ)
			}
		}
	}
	for _, lang := range c.Languages {
		if countPhrases(c.Pain.Phrases[lang]) == 0 {
			add("idioma %q sem frases de dor em pain.phrases.%s", lang, lang)
		}
	}

	r := c.Rubric
	for name, w := range map[string]int{
		"frequency": r.Frequency, "payment": r.Payment, "distribution": r.Distribution, "b2b": r.B2B, "global": r.Global,
		"solo": r.Solo, "sellability": r.Sellability, "infra": r.Infra,
	} {
		if w < 0 {
			add("rubric.%s negativo", name)
		}
	}
	if s := r.Sum(); s != 100 {
		add("os pesos de rubric somam %d, precisam somar 100", s)
	}

	if strings.TrimSpace(c.AI.GroupModel) == "" {
		add("ai.group_model vazio")
	}
	if strings.TrimSpace(c.AI.ScoreModel) == "" {
		add("ai.score_model vazio")
	}
	if c.AI.WeeklyTokenLimit <= 0 {
		add("ai.weekly_token_limit precisa ser maior que zero")
	}

	if c.Collect.InitialLookbackDays < 1 {
		add("collect.initial_lookback_days precisa ser pelo menos 1")
	}
	if c.Collect.OverlapMinutes < 0 {
		add("collect.overlap_minutes negativo")
	}
	if len(c.Sources.HN.Languages) == 0 {
		add("sources.hn.languages vazio")
	}
	for _, l := range c.Sources.HN.Languages {
		if !enabled[l] {
			add("sources.hn.languages: idioma %q fora de languages", l)
		}
	}
	return errors.Join(errs...)
}

// ResolvedDBPath devolve db_path com "~" expandido.
func (c *Config) ResolvedDBPath() (string, error) {
	return ExpandHome(c.DBPath)
}

// ExpandHome troca um "~" inicial pela pasta do usuário.
func ExpandHome(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, `~\`) {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, p[1:]), nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func countPhrases(byType map[string][]string) int {
	n := 0
	for _, l := range byType {
		n += len(l)
	}
	return n
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
