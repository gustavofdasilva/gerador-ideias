package process

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// CountryUnknown é o padrão quando não há sinal (design 5.4). Todo país em
// country_est é ESTIMATIVA, nunca dado informado pela fonte.
const CountryUnknown = "desconhecido"

// Moedas com país claro. "$" sozinho e "€" sozinho são ambíguos: o € só
// conta junto com o idioma (ver abaixo).
var currencies = []struct {
	re      *regexp.Regexp
	country string
}{
	{regexp.MustCompile(`R\$\s?\d|\bBRL\b|\breais\b`), "br"},
	{regexp.MustCompile(`£\s?\d|\d\s?£|\bGBP\b`), "gb"},
	{regexp.MustCompile(`₹\s?\d|\bINR\b|\brupees?\b`), "in"},
	{regexp.MustCompile(`\b(?:A\$|AU\$)\s?\d|\bAUD\b`), "au"},
	{regexp.MustCompile(`\b(?:C\$|CA\$)\s?\d|\bCAD\b`), "ca"},
	{regexp.MustCompile(`\bMXN\b|\bpesos mexicanos\b`), "mx"},
	{regexp.MustCompile(`\bARS\b|\bpesos argentinos\b`), "ar"},
}

var reEuro = regexp.MustCompile(`€\s?\d|\d\s?€|\bEUR\b|\beuros?\b`)

// País pelo TLD dos links citados. Domínios "de vaidade" (.io, .co, .ai, .me,
// .tv, .ly, .fm, .app, .dev) não dizem nada sobre o país e ficam de fora.
var tlds = map[string]string{
	"br": "br", "pt": "pt", "de": "de", "fr": "fr", "es": "es", "mx": "mx", "ar": "ar",
	"uk": "gb", "in": "in", "ca": "ca", "au": "au", "it": "it", "nl": "nl", "be": "be",
	"ch": "ch", "at": "at", "cl": "cl", "pe": "pe", "uy": "uy", "jp": "jp", "pl": "pl",
	"se": "se", "no": "no", "dk": "dk", "fi": "fi", "ie": "ie", "nz": "nz", "za": "za",
}

var reLinkHost = regexp.MustCompile(`(?i)\bhttps?://([a-z0-9.-]+)`)

// Nomes de países e cidades grandes (sem acento, em minúsculas), em EN/PT/ES/DE/FR.
var places = map[string][]string{
	"br": {"brasil", "brazil", "brasilien", "bresil", "sao paulo", "rio de janeiro", "belo horizonte", "brasilia", "curitiba", "porto alegre", "recife", "salvador da bahia", "florianopolis"},
	"pt": {"portugal", "lisboa", "lisbon", "lissabon", "lisbonne"},
	"es": {"espana", "spain", "spanien", "espagne", "madrid", "barcelona", "valencia", "sevilla"},
	"mx": {"mexico", "mexiko", "cdmx", "guadalajara", "monterrey"},
	"ar": {"argentina", "argentinien", "argentine", "buenos aires", "cordoba argentina"},
	"de": {"germany", "deutschland", "alemanha", "alemania", "allemagne", "berlin", "munchen", "munich", "hamburg", "frankfurt", "koln"},
	"fr": {"france", "frankreich", "franca", "francia", "paris", "lyon", "marseille", "toulouse"},
	"gb": {"united kingdom", "uk", "england", "britain", "london", "londres", "manchester", "edinburgh"},
	"us": {"united states", "usa", "estados unidos", "new york", "nyc", "san francisco", "los angeles", "seattle", "chicago", "texas", "california", "boston"},
	"in": {"india", "bangalore", "bengaluru", "mumbai", "delhi", "hyderabad", "pune"},
	"ca": {"canada", "toronto", "vancouver", "montreal"},
	"au": {"australia", "sydney", "melbourne", "brisbane"},
	"co": {"colombia", "bogota", "medellin"},
	"cl": {"chile", "santiago de chile"},
}

// Palavras típicas do português europeu (sem acento).
var ptPT = []string{"telemovel", "ecra", "autocarro", "comboio", "utilizador", "utilizadores", "equipa", "facto", "casa de banho", "pequeno-almoco", "frigorifico", "rapariga", "giro"}

// EstimateCountry estima o país do autor a partir do texto e do idioma.
// Sinais explícitos (moeda, TLD, país/cidade citados) somam votos; o idioma
// só decide sozinho no caso do português (Brasil, a menos que haja sinais de
// Portugal) e serve de desempate para o euro.
func EstimateCountry(raw, lang string) (country, reason string) {
	votes := map[string]float64{}
	why := map[string][]string{}
	vote := func(c string, w float64, r string) {
		votes[c] += w
		why[c] = append(why[c], r)
	}

	for _, cur := range currencies {
		if cur.re.MatchString(raw) {
			vote(cur.country, 2, "moeda")
		}
	}
	if reEuro.MatchString(raw) {
		switch lang {
		case "de", "fr", "es", "pt":
			vote(map[string]string{"de": "de", "fr": "fr", "es": "es", "pt": "pt"}[lang], 2, "euro+idioma")
		}
	}

	for _, m := range reLinkHost.FindAllStringSubmatch(raw, -1) {
		host := strings.ToLower(m[1])
		if u, err := url.Parse("http://" + host); err == nil {
			host = u.Hostname()
		}
		parts := strings.Split(host, ".")
		if c, ok := tlds[parts[len(parts)-1]]; ok {
			vote(c, 1.5, "tld")
		}
	}

	ns := norms(Tokenize(raw))
	padded := " " + strings.Join(ns, " ") + " "
	for c, names := range places {
		for _, name := range names {
			if strings.Contains(padded, " "+name+" ") {
				vote(c, 1, "lugar")
				break
			}
		}
	}

	if lang == "pt" {
		pt := false
		for _, w := range ptPT {
			pt = pt || strings.Contains(padded, " "+w+" ")
		}
		if pt {
			vote("pt", 1.5, "pt-PT")
		} else {
			vote("br", 1, "idioma pt")
		}
	}

	if len(votes) == 0 {
		return CountryUnknown, ""
	}
	type cv struct {
		c string
		v float64
	}
	var list []cv
	for c, v := range votes {
		list = append(list, cv{c, v})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].v != list[j].v {
			return list[i].v > list[j].v
		}
		return list[i].c < list[j].c
	})
	if len(list) > 1 && list[0].v == list[1].v {
		return CountryUnknown, "empate"
	}
	return list[0].c, strings.Join(why[list[0].c], ",")
}
