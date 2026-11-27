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
	Keywords  Keywords `toml:"keywords"`
	Report    Report   `toml:"report"`
	Alerts    Alerts   `toml:"alerts"`
	Storage   Storage  `toml:"storage"`
	Backup    Backup   `toml:"backup"`

	// Source é de onde a configuração veio: o caminho do arquivo ou "embutido".
	Source string `toml:"-"`
}

// Pain define as frases de dor e os pesos por tipo.
type Pain struct {
	EngagementBonusMax float64 `toml:"engagement_bonus_max"`
	// MinScore: pontuação mínima das frases (+ bônus de marca) para o item
	// virar candidato. O engajamento só ordena, não decide.
	MinScore float64 `toml:"min_score"`
	// BrandBonus soma à pontuação quando o item casa uma frase e cita marca.
	BrandBonus float64 `toml:"brand_bonus"`
	// BrandRequired: tipos de frase que só contam se o item citar uma marca.
	BrandRequired []string `toml:"brand_required"`
	// Weights: tipo de frase -> peso.
	Weights map[string]float64 `toml:"weights"`
	// Phrases: idioma -> tipo de frase -> frases.
	Phrases map[string]map[string][]string `toml:"phrases"`
	// Exclude: idioma -> trechos que anulam as frases que caem dentro deles
	// (falsos positivos, ex.: "what you are willing to pay").
	Exclude map[string][]string `toml:"exclude"`
}

// Brands guarda a lista-semente e os padrões de extração de marcas.
type Brands struct {
	Seed []string `toml:"seed"`
	// Patterns: idioma -> padrões com {marca}, ex.: "alternative to {marca}".
	Patterns map[string][]string `toml:"patterns"`
}

// BrandPlaceholder marca a posição da marca num padrão.
const BrandPlaceholder = "{marca}"

// Report controla o relatório semanal (design, seção 7).
type Report struct {
	Dir string `toml:"dir"` // pasta dos AAAA-WW.md ("~" é expandido)
	// ExportDir: pasta dos pacotes do modo manual (Claude web).
	ExportDir     string `toml:"export_dir"`
	TopCandidates int    `toml:"top_candidates"`
	TopKeywords   int    `toml:"top_keywords"`
	TopBrands     int    `toml:"top_brands"`
	// Termos com menos ocorrências que isso na semana não entram em "em alta".
	MinKeywordCount int `toml:"min_keyword_count"`
}

// ResolvedDir devolve Dir com "~" expandido.
func (r Report) ResolvedDir() (string, error) { return ExpandHome(r.Dir) }

// ResolvedExportDir devolve ExportDir com "~" expandido.
func (r Report) ResolvedExportDir() (string, error) { return ExpandHome(r.ExportDir) }

// Keywords controla a contagem de palavras-chave (n-gramas) em daily_terms.
type Keywords struct {
	MaxN       int `toml:"max_n"`        // maior n-grama (1 a 3)
	MaxPerItem int `toml:"max_per_item"` // n-gramas distintos contados por item
	// MinCount: numa execução, termos com menos ocorrências que isso (por dia,
	// fonte, idioma e país) não são gravados. Segura o crescimento do banco.
	MinCount int `toml:"min_count"`
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

	// MaxItems: candidatos da semana enviados à IA (os de maior pain_score).
	MaxItems int `toml:"max_items"`
	// BatchSize: candidatos por chamada de agrupamento.
	BatchSize int `toml:"batch_size"`
	// ItemChars: caracteres de texto de cada candidato enviados à IA.
	ItemChars int `toml:"item_chars"`
	// MaxClusters: clusters existentes mostrados à IA (os vistos mais recentemente).
	MaxClusters int `toml:"max_clusters"`
	// ScoreTop: quantas dores são pontuadas por semana (design: ~20).
	ScoreTop int `toml:"score_top"`
	// ScoreBatchSize: dores por chamada de pontuação (cada uma gera uma resposta longa).
	ScoreBatchSize int `toml:"score_batch_size"`
	// MaxOutputTokens: limite de tokens de saída de cada chamada.
	MaxOutputTokens int `toml:"max_output_tokens"`
	// PenaltyPoints e BonusPoints ajustam a nota final (0 a 100).
	PenaltyPoints int `toml:"penalty_points"`
	BonusPoints   int `toml:"bonus_points"`
	// TimeoutSeconds: tempo máximo de cada chamada (modelos gratuitos são lentos).
	TimeoutSeconds int `toml:"timeout_seconds"`
	// ManualPartChars: tamanho máximo de cada parte do candidatos.md do modo manual.
	ManualPartChars int `toml:"manual_part_chars"`
}

// Collect controla a janela de coleta (design, 5.1 passo 1).
type Collect struct {
	// Sem cursor (primeira execução ou cursor apagado), busca os últimos N dias.
	InitialLookbackDays int `toml:"initial_lookback_days"`
	// Re-busca os últimos N minutos da janela anterior (atraso de indexação).
	OverlapMinutes int `toml:"overlap_minutes"`
	// Sources: fontes de "collect --source all" (e as que o check vigia).
	Sources []string `toml:"sources"`
}

// KnownSources são as fontes que o binário sabe coletar.
var KnownSources = []string{"hn", "bsky", "gh"}

// Alerts controla os alertas do "ideias check".
type Alerts struct {
	// FailingDays: alerta se a fonte falhou em todos os N últimos dias com execução.
	FailingDays int `toml:"failing_days"`
	// StaleHours: alerta se nenhuma fonte coletou com sucesso há mais que isso.
	StaleHours int `toml:"stale_hours"`
	// RepeatHours: o mesmo alerta só é repetido depois desse intervalo.
	RepeatHours int `toml:"repeat_hours"`
}

// Storage controla quanto o banco guarda (meta: < 200 MB/ano, design 5.6).
type Storage struct {
	// MaxCandidatesPerDay: candidatos com texto guardado por fonte e por dia
	// (os de maior pain_score). Os demais só contam nas agregações.
	MaxCandidatesPerDay int `toml:"max_candidates_per_day"`
	// MaxTextChars: caracteres de texto guardados por candidato.
	MaxTextChars int `toml:"max_text_chars"`
	// KeepTermWeeks: semanas de daily_terms e daily_brands mantidas.
	KeepTermWeeks int `toml:"keep_term_weeks"`
}

// Backup controla o backup feito depois de cada relatório.
type Backup struct {
	Dir  string `toml:"dir"`  // vazio desliga o backup automático
	Keep int    `toml:"keep"` // quantos backups manter
}

// ResolvedDir devolve Dir com "~" expandido.
func (b Backup) ResolvedDir() (string, error) { return ExpandHome(b.Dir) }

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
	HN   LangSource `toml:"hn"`
	Bsky LangSource `toml:"bsky"`
	GH   GHSource   `toml:"gh"`
}

// LangSource é uma fonte configurada só pelos idiomas buscados.
type LangSource struct {
	// Idiomas cujas frases de dor são buscadas na fonte.
	Languages []string `toml:"languages"`
}

// GHSource configura o coletor do GitHub Issues.
type GHSource struct {
	Languages []string `toml:"languages"`
	// A issue precisa ter pelo menos uma destas labels. Vazio = sem filtro.
	Labels []string `toml:"labels"`
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
	cfg, err := ParseOverDefaults(data)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	cfg.Source = path
	return cfg, nil
}

// Parse decodifica, normaliza e valida o TOML sozinho (sem padrões).
func Parse(data []byte) (*Config, error) {
	return parse(data, &Config{})
}

// ParseOverDefaults lê o TOML por cima do config.example.toml embutido:
// chaves ausentes ficam com o padrão. Assim, um config.toml antigo continua
// valendo quando uma versão nova acrescenta opções. Tabelas por idioma ou por
// tipo (frases, pesos, exclusões, padrões de marca) que o arquivo define
// substituem as do padrão por inteiro, em vez de somar.
func ParseOverDefaults(data []byte) (*Config, error) {
	base, err := Parse(ideias.ExampleConfig)
	if err != nil {
		return nil, fmt.Errorf("config embutido: %w", err)
	}
	var probe map[string]any
	md, err := toml.Decode(string(data), &probe)
	if err != nil {
		return nil, err
	}
	if md.IsDefined("pain", "phrases") {
		base.Pain.Phrases = nil
	}
	if md.IsDefined("pain", "weights") {
		base.Pain.Weights = nil
	}
	if md.IsDefined("pain", "exclude") {
		base.Pain.Exclude = nil
	}
	if md.IsDefined("brands", "patterns") {
		base.Brands.Patterns = nil
	}
	return parse(data, base)
}

func parse(data []byte, cfg *Config) (*Config, error) {
	md, err := toml.Decode(string(data), cfg)
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
	return cfg, nil
}

func (c *Config) normalize() {
	for i, l := range c.Languages {
		c.Languages[i] = strings.ToLower(strings.TrimSpace(l))
	}
	// Idiomas das fontes que não estão em languages são ignorados (eles vêm
	// dos padrões quando o config desliga um idioma).
	enabled := map[string]bool{}
	for _, l := range c.Languages {
		enabled[l] = true
	}
	onlyEnabled := func(list []string) []string {
		var out []string
		for _, l := range list {
			if l = strings.ToLower(strings.TrimSpace(l)); enabled[l] {
				out = append(out, l)
			}
		}
		return out
	}
	c.Sources.HN.Languages = onlyEnabled(c.Sources.HN.Languages)
	c.Sources.Bsky.Languages = onlyEnabled(c.Sources.Bsky.Languages)
	c.Sources.GH.Languages = onlyEnabled(c.Sources.GH.Languages)
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
