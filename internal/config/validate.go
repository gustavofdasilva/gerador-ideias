package config

import (
	"errors"
	"fmt"
	"strings"
)

// validator acumula os problemas de todas as seções, para o usuário ver
// tudo de uma vez em vez de corrigir um erro por execução.
type validator struct {
	c       *Config
	enabled map[string]bool
	errs    []error
}

func (v *validator) add(format string, args ...any) {
	v.errs = append(v.errs, fmt.Errorf(format, args...))
}

// Validate confere as regras que o resto do programa assume.
func (c *Config) Validate() error {
	v := &validator{c: c, enabled: map[string]bool{}}
	v.general()
	v.pain()
	v.brands()
	v.keywords()
	v.report()
	v.rubric()
	v.ai()
	v.collect()
	v.sources()
	return errors.Join(v.errs...)
}

func (v *validator) general() {
	if strings.TrimSpace(v.c.DBPath) == "" {
		v.add("db_path vazio")
	}
	if len(v.c.Languages) == 0 {
		v.add("languages vazio")
	}
	for _, l := range v.c.Languages {
		if !contains(SupportedLanguages, l) {
			v.add("idioma %q não suportado (use: %s)", l, strings.Join(SupportedLanguages, ", "))
		}
		if v.enabled[l] {
			v.add("idioma %q repetido", l)
		}
		v.enabled[l] = true
	}
}

func (v *validator) pain() {
	p := v.c.Pain
	if p.EngagementBonusMax < 0 {
		v.add("pain.engagement_bonus_max negativo")
	}
	if p.MinScore <= 0 {
		v.add("pain.min_score precisa ser maior que zero")
	}
	if p.BrandBonus < 0 {
		v.add("pain.brand_bonus negativo")
	}
	if len(p.Weights) == 0 {
		v.add("pain.weights vazio")
	}
	for _, typ := range sortedKeys(p.Weights) {
		if p.Weights[typ] <= 0 {
			v.add("pain.weights.%s precisa ser maior que zero", typ)
		}
	}
	// Frases, exclusões e padrões de idiomas fora de languages são ignorados.
	for _, lang := range sortedKeys(p.Phrases) {
		for _, typ := range sortedKeys(p.Phrases[lang]) {
			v.hasWeight("pain.phrases."+lang+"."+typ, typ)
		}
	}
	for _, lang := range v.c.Languages {
		if countPhrases(p.Phrases[lang]) == 0 {
			v.add("idioma %q sem frases de dor em pain.phrases.%s", lang, lang)
		}
	}
	// brand_required com um tipo que não existe em pain.weights não faz nada
	// (acontece quando o config redefine os pesos e herda brand_required).
}

func (v *validator) brands() {
	for _, lang := range sortedKeys(v.c.Brands.Patterns) {
		for _, p := range v.c.Brands.Patterns[lang] {
			before, after, ok := strings.Cut(p, BrandPlaceholder)
			switch {
			case !ok || strings.Contains(after, BrandPlaceholder):
				v.add("brands.patterns.%s: %q precisa ter exatamente um %s", lang, p, BrandPlaceholder)
			case strings.TrimSpace(before) == "" && strings.TrimSpace(after) == "":
				v.add("brands.patterns.%s: %q precisa de texto além de %s", lang, p, BrandPlaceholder)
			}
		}
	}
}

func (v *validator) keywords() {
	k := v.c.Keywords
	if k.MaxN < 1 || k.MaxN > 3 {
		v.add("keywords.max_n precisa ir de 1 a 3")
	}
	v.atLeastOne("keywords.max_per_item", k.MaxPerItem)
	v.atLeastOne("keywords.min_count", k.MinCount)
}

func (v *validator) report() {
	r := v.c.Report
	if strings.TrimSpace(r.Dir) == "" {
		v.add("report.dir vazio")
	}
	if strings.TrimSpace(r.ExportDir) == "" {
		v.add("report.export_dir vazio")
	}
	v.atLeastOne("report.top_candidates", r.TopCandidates)
	v.atLeastOne("report.top_keywords", r.TopKeywords)
	v.atLeastOne("report.top_brands", r.TopBrands)
	v.atLeastOne("report.min_keyword_count", r.MinKeywordCount)
}

func (v *validator) rubric() {
	r := v.c.Rubric
	weights := map[string]int{
		"frequency": r.Frequency, "payment": r.Payment, "distribution": r.Distribution, "b2b": r.B2B,
		"global": r.Global, "solo": r.Solo, "sellability": r.Sellability, "infra": r.Infra,
	}
	for _, name := range sortedKeys(weights) {
		if weights[name] < 0 {
			v.add("rubric.%s negativo", name)
		}
	}
	if s := r.Sum(); s != 100 {
		v.add("os pesos de rubric somam %d, precisam somar 100", s)
	}
}

func (v *validator) ai() {
	a := v.c.AI
	if strings.TrimSpace(a.GroupModel) == "" {
		v.add("ai.group_model vazio")
	}
	if strings.TrimSpace(a.ScoreModel) == "" {
		v.add("ai.score_model vazio")
	}
	if a.WeeklyTokenLimit <= 0 {
		v.add("ai.weekly_token_limit precisa ser maior que zero")
	}
	v.atLeastOne("ai.max_items", a.MaxItems)
	v.atLeastOne("ai.batch_size", a.BatchSize)
	v.atLeastOne("ai.max_clusters", a.MaxClusters)
	v.atLeastOne("ai.score_top", a.ScoreTop)
	v.atLeastOne("ai.score_batch_size", a.ScoreBatchSize)
	if a.MaxOutputTokens < 1000 {
		v.add("ai.max_output_tokens precisa ser pelo menos 1000")
	}
	v.atLeastOne("ai.timeout_seconds", a.TimeoutSeconds)
	if a.ItemChars < 100 {
		v.add("ai.item_chars precisa ser pelo menos 100")
	}
	if a.ManualPartChars < 10000 {
		v.add("ai.manual_part_chars precisa ser pelo menos 10000")
	}
	if a.PenaltyPoints < 0 || a.PenaltyPoints > 100 || a.BonusPoints < 0 || a.BonusPoints > 100 {
		v.add("ai.penalty_points e ai.bonus_points precisam ir de 0 a 100")
	}
}

func (v *validator) collect() {
	v.atLeastOne("collect.initial_lookback_days", v.c.Collect.InitialLookbackDays)
	if v.c.Collect.OverlapMinutes < 0 {
		v.add("collect.overlap_minutes negativo")
	}
	if len(v.c.Collect.Sources) == 0 {
		v.add("collect.sources vazio")
	}
	for _, s := range v.c.Collect.Sources {
		if !contains(KnownSources, s) {
			v.add("collect.sources: fonte %q desconhecida (use: %s)", s, strings.Join(KnownSources, ", "))
		}
	}
	a := v.c.Alerts
	v.atLeastOne("alerts.failing_days", a.FailingDays)
	v.atLeastOne("alerts.stale_hours", a.StaleHours)
	v.atLeastOne("alerts.repeat_hours", a.RepeatHours)
	st := v.c.Storage
	v.atLeastOne("storage.max_candidates_per_day", st.MaxCandidatesPerDay)
	if st.MaxTextChars < 200 {
		v.add("storage.max_text_chars precisa ser pelo menos 200")
	}
	if st.KeepTermWeeks < 5 {
		v.add("storage.keep_term_weeks precisa ser pelo menos 5 (a tendência usa a semana + as 4 anteriores)")
	}
	if v.c.Backup.Dir != "" {
		v.atLeastOne("backup.keep", v.c.Backup.Keep)
	}
}

func (v *validator) sources() {
	s := v.c.Sources
	for _, src := range []struct {
		name  string
		langs []string
	}{{"hn", s.HN.Languages}, {"bsky", s.Bsky.Languages}, {"gh", s.GH.Languages}} {
		// Idiomas fora de languages já foram descartados no normalize.
		if len(src.langs) == 0 {
			v.add("sources.%s.languages sem nenhum idioma de languages", src.name)
		}
	}
	for _, l := range s.GH.Labels {
		if strings.TrimSpace(l) == "" {
			v.add("sources.gh.labels: label vazia")
		}
	}
}

func (v *validator) hasWeight(where, typ string) {
	if _, ok := v.c.Pain.Weights[typ]; !ok {
		v.add("%s: tipo sem peso em pain.weights", where)
	}
}

func (v *validator) atLeastOne(name string, n int) {
	if n < 1 {
		v.add("%s precisa ser pelo menos 1", name)
	}
}
