// Package process faz o processamento local de cada item, sem IA (design,
// 5.1 passos 4 a 7): idioma, filtro de candidatos, sinais, marcas,
// palavras-chave e país estimado.
package process

import (
	"embed"
	"regexp"
	"strings"
	"unicode"
)

// Token é uma palavra do texto: Raw preserva a caixa original (útil para
// reconhecer marcas); Norm está em minúsculas e sem acentos.
type Token struct {
	Raw  string
	Norm string
	// Spaced: entre o token anterior e este só há espaço (sem pontuação).
	// Marcas de duas palavras ("Google Docs") só juntam tokens assim.
	Spaced bool
	// Start: primeira palavra do texto ou de uma frase (depois de . ! ? : >
	// ou quebra de linha). Maiúscula ali não indica nome próprio.
	Start bool
}

var (
	reURL       = regexp.MustCompile(`(?i)\bhttps?://\S+|\bwww\.\S+`)
	reCodeFence = regexp.MustCompile("(?s)```.*?```")
	reCodeSpan  = regexp.MustCompile("`[^`\n]*`")
)

// stripNoise remove blocos de código (comuns em issues do GitHub) e URLs,
// que só atrapalham idioma e palavras-chave.
func stripNoise(s string) string {
	s = reCodeFence.ReplaceAllString(s, " ")
	s = reCodeSpan.ReplaceAllString(s, " ")
	return reURL.ReplaceAllString(s, " ")
}

// Tokenize separa o texto em palavras: sequências de letras e dígitos, com
// apóstrofos e hífens internos ("i'd", "existe-t-il").
func Tokenize(s string) []Token {
	s = strings.NewReplacer("’", "'", "‘", "'", "ʼ", "'").Replace(s)
	var out []Token
	rs := []rune(s)
	isWord := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
	prevEnd := 0
	for i := 0; i < len(rs); {
		if !isWord(rs[i]) {
			i++
			continue
		}
		j := i
		for j < len(rs) {
			if isWord(rs[j]) {
				j++
				continue
			}
			// apóstrofo ou hífen entre duas letras/dígitos continua a palavra
			if (rs[j] == '\'' || rs[j] == '-') && j+1 < len(rs) && isWord(rs[j+1]) {
				j++
				continue
			}
			break
		}
		raw := string(rs[i:j])
		// Possessivo inglês: "Google's" vira "Google" ("it's" vira "it", que
		// também é stopword).
		if n := len(raw); n > 3 && (strings.HasSuffix(raw, "'s") || strings.HasSuffix(raw, "'S")) {
			raw = raw[:n-2]
		}
		gap := string(rs[prevEnd:i])
		spaced := len(out) > 0 && strings.TrimSpace(gap) == ""
		start := len(out) == 0 || strings.ContainsAny(gap, ".!?:>\n")
		out = append(out, Token{Raw: raw, Norm: Fold(raw), Spaced: spaced, Start: start})
		i, prevEnd = j, j
	}
	return out
}

var foldMap = map[rune]string{
	'á': "a", 'à': "a", 'â': "a", 'ã': "a", 'ä': "a", 'å': "a",
	'é': "e", 'è': "e", 'ê': "e", 'ë': "e",
	'í': "i", 'ì': "i", 'î': "i", 'ï': "i",
	'ó': "o", 'ò': "o", 'ô': "o", 'õ': "o", 'ö': "o",
	'ú': "u", 'ù': "u", 'û': "u", 'ü': "u",
	'ç': "c", 'ñ': "n", 'ß': "ss", 'œ': "oe", 'æ': "ae", 'ÿ': "y",
}

// Fold põe em minúsculas e tira os acentos latinos, para casar "alguém" com
// "alguem" (muita gente escreve sem acento).
func Fold(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if f, ok := foldMap[r]; ok {
			b.WriteString(f)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// norms devolve só as formas normalizadas.
func norms(toks []Token) []string {
	out := make([]string, len(toks))
	for i, t := range toks {
		out[i] = t.Norm
	}
	return out
}

//go:embed stopwords/*.txt
var stopwordFS embed.FS

// Stopwords por idioma, já normalizadas com Fold.
var Stopwords = loadStopwords()

func loadStopwords() map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, lang := range []string{"en", "pt", "es", "de", "fr"} {
		data, err := stopwordFS.ReadFile("stopwords/" + lang + ".txt")
		if err != nil {
			panic(err)
		}
		set := map[string]bool{}
		for _, w := range strings.Fields(string(data)) {
			set[Fold(w)] = true
		}
		out[lang] = set
	}
	return out
}

// Caracteres que praticamente só aparecem num dos idiomas suportados.
var langMarks = map[string]string{
	"pt": "ãõ",
	"es": "ñ¿¡",
	"de": "ßäöü",
	"fr": "œèêëîûù",
}

// DetectLang escolhe o idioma pela contagem de stopwords e de caracteres
// típicos, só entre os idiomas permitidos. Devolve "" se não há evidência
// suficiente.
func DetectLang(raw string, toks []Token, allowed []string) string {
	score := map[string]float64{}
	for _, lang := range allowed {
		sw := Stopwords[lang]
		for _, t := range toks {
			if sw[t.Norm] {
				score[lang]++
			}
		}
		if marks := langMarks[lang]; marks != "" {
			for _, r := range strings.ToLower(raw) {
				if strings.ContainsRune(marks, r) {
					score[lang] += 1.5
				}
			}
		}
	}
	best, top, second := "", 0.0, 0.0
	for _, lang := range allowed {
		s := score[lang]
		if best == "" || s > top {
			best, top, second = lang, s, top
		} else if s > second {
			second = s
		}
	}
	if best == "" {
		return ""
	}
	// Pede um mínimo de evidência e alguma margem sobre o segundo colocado
	// (pt e es dividem muitas palavras curtas).
	if top < 2 || top < second*1.25 {
		return ""
	}
	return best
}

// StorageText prepara o texto de um candidato para guardar: sem blocos de
// código (comuns e longos em issues), espaços juntados e no máximo n caracteres.
func StorageText(s string, n int) string {
	s = reCodeFence.ReplaceAllString(s, " [código] ")
	s = strings.Join(strings.Fields(s), " ")
	if rs := []rune(s); n > 0 && len(rs) > n {
		return string(rs[:n]) + "…"
	}
	return s
}
