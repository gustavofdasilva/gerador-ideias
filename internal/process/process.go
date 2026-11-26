package process

import (
	"math"
	"sort"
	"strings"

	"gerador-ideias"
	"gerador-ideias/internal/config"
	"gerador-ideias/internal/db"
)

// Unknown é o idioma de um item sem evidência suficiente.
const Unknown = "und"

// Result é o que o processamento descobre sobre um item.
type Result struct {
	Lang          string
	Country       string // estimativa (design 5.4); CountryUnknown por padrão
	CountryReason string // de onde veio a estimativa (só para depuração/testes)
	Signals       []string
	Matches       []string // frases casadas (normalizadas)
	PhraseScore   float64  // soma dos pesos das frases (+ bônus de marca)
	Score         float64  // PhraseScore + bônus de engajamento
	Candidate     bool
	Brands        []string
	Terms         []string
}

// Processor aplica as regras do config a cada item. É seguro para uso
// concorrente depois de criado.
type Processor struct {
	languages     []string
	weights       map[string]float64
	minScore      float64
	brandBonus    float64
	brandRequired map[string]bool
	engagementMax float64
	phrases       []phrase
	byFirst       map[string][]int // primeira palavra -> índices em phrases
	excludes      [][]string
	phraseVocab   map[string]bool
	seeds         []seed
	patterns      []pattern
	maxN          int
	maxPerItem    int
}

type phrase struct {
	toks []string
	typ  string
	lang string
}

type seed struct {
	toks []string
	name string
}

// pattern é um padrão de marca dividido em volta de {marca}.
type pattern struct {
	before []string
	after  []string
	lang   string
}

// New compila as regras do config.
func New(cfg *config.Config) *Processor {
	p := &Processor{
		languages:     cfg.Languages,
		weights:       cfg.Pain.Weights,
		minScore:      cfg.Pain.MinScore,
		brandBonus:    cfg.Pain.BrandBonus,
		engagementMax: cfg.Pain.EngagementBonusMax,
		byFirst:       map[string][]int{},
		phraseVocab:   map[string]bool{},
		brandRequired: map[string]bool{},
		maxN:          cfg.Keywords.MaxN,
		maxPerItem:    cfg.Keywords.MaxPerItem,
	}
	for _, lang := range cfg.Languages {
		byType := cfg.Pain.Phrases[lang]
		types := make([]string, 0, len(byType))
		for t := range byType {
			types = append(types, t)
		}
		sort.Strings(types)
		for _, typ := range types {
			for _, s := range byType[typ] {
				toks := norms(Tokenize(s))
				if len(toks) == 0 {
					continue
				}
				p.byFirst[toks[0]] = append(p.byFirst[toks[0]], len(p.phrases))
				p.phrases = append(p.phrases, phrase{toks: toks, typ: typ, lang: lang})
				for _, t := range toks {
					p.phraseVocab[t] = true
				}
			}
		}
		for _, s := range cfg.Pain.Exclude[lang] {
			if toks := norms(Tokenize(s)); len(toks) > 0 {
				p.excludes = append(p.excludes, toks)
			}
		}
		for _, s := range cfg.Brands.Patterns[lang] {
			before, after, _ := strings.Cut(s, config.BrandPlaceholder)
			p.patterns = append(p.patterns, pattern{
				before: norms(Tokenize(before)),
				after:  norms(Tokenize(after)),
				lang:   lang,
			})
		}
	}
	for _, typ := range cfg.Pain.BrandRequired {
		p.brandRequired[typ] = true
	}
	for _, name := range cfg.Brands.Seed {
		if toks := norms(Tokenize(name)); len(toks) > 0 {
			p.seeds = append(p.seeds, seed{toks: toks, name: strings.TrimSpace(name)})
		}
	}
	return p
}

// Process analisa um item já normalizado pela fonte.
func (p *Processor) Process(it db.Item) Result {
	raw := it.Text
	clean := stripNoise(raw)
	toks := Tokenize(clean)
	ns := norms(toks)

	var r Result
	matches := p.matchPhrases(ns)
	r.Lang = p.lang(it.LangHint, clean, toks, matches)
	r.Brands = p.brands(toks, it.Brands)

	sigs := map[string]bool{}
	for _, m := range matches {
		// "alternative to the system" sem marca nenhuma é discussão abstrata.
		if p.brandRequired[m.typ] && len(r.Brands) == 0 {
			continue
		}
		r.Matches = append(r.Matches, m.key)
		r.PhraseScore += p.weights[m.typ]
		sigs[m.typ] = true
	}
	for s := range sigs {
		r.Signals = append(r.Signals, s)
	}
	sort.Strings(r.Signals)
	if r.PhraseScore > 0 && len(r.Brands) > 0 {
		r.PhraseScore += p.brandBonus
	}
	r.Candidate = r.PhraseScore >= p.minScore
	r.Score = round2(r.PhraseScore + p.engagementBonus(it.Engagement))
	r.PhraseScore = round2(r.PhraseScore)

	r.Country, r.CountryReason = EstimateCountry(raw, r.Lang)
	r.Terms = p.keywords(toks, r.Lang)
	return r
}

// lang: o idioma informado pela fonte vence, se estiver entre os permitidos;
// senão, detecção local; sem evidência, o idioma da maioria das frases casadas.
func (p *Processor) lang(hint, text string, toks []Token, matches []match) string {
	if hint != "" && contains(p.languages, hint) {
		return hint
	}
	if l := DetectLang(text, toks, p.languages); l != "" {
		return l
	}
	votes := map[string]int{}
	for _, m := range matches {
		votes[m.lang]++
	}
	best := ""
	for _, l := range p.languages {
		if votes[l] > votes[best] {
			best = l
		}
	}
	if best == "" {
		return Unknown
	}
	return best
}

type match struct {
	key, typ, lang string
}

// matchPhrases acha as frases por palavra inteira, ignorando as que caem
// dentro de um trecho de exclusão. Cada frase conta uma vez.
func (p *Processor) matchPhrases(ns []string) (matches []match) {
	excluded := make([]bool, len(ns))
	for _, ex := range p.excludes {
		for i := range ns {
			if hasAt(ns, i, ex) {
				for k := i; k < i+len(ex); k++ {
					excluded[k] = true
				}
			}
		}
	}
	seen := map[int]bool{}
	keys := map[string]bool{}
	for i, w := range ns {
		for _, idx := range p.byFirst[w] {
			ph := p.phrases[idx]
			if seen[idx] || !hasAt(ns, i, ph.toks) || anyTrue(excluded[i:i+len(ph.toks)]) {
				continue
			}
			seen[idx] = true
			key := strings.Join(ph.toks, " ")
			// A mesma frase pode estar em dois idiomas ("alternativa a"): conta uma vez.
			if keys[key] {
				continue
			}
			keys[key] = true
			matches = append(matches, match{key: key, typ: ph.typ, lang: ph.lang})
		}
	}
	return matches
}

// engagementBonus: escala logarítmica que chega ao máximo com ~100.
func (p *Processor) engagementBonus(e int) float64 {
	if e <= 0 || p.engagementMax <= 0 {
		return 0
	}
	return p.engagementMax * math.Min(1, math.Log1p(float64(e))/math.Log1p(100))
}

func hasAt(ns []string, i int, seq []string) bool {
	if i+len(seq) > len(ns) {
		return false
	}
	for k, s := range seq {
		if ns[i+k] != s {
			return false
		}
	}
	return true
}

func anyTrue(bs []bool) bool {
	for _, b := range bs {
		if b {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

// Default monta o Processor com o config.example.toml embutido. Para testes
// e para quem não tem config próprio.
func Default() *Processor {
	cfg, err := config.Parse(ideias.ExampleConfig)
	if err != nil {
		panic(err)
	}
	return New(cfg)
}
