package process

import (
	"strings"
	"unicode"
)

// keywords devolve os 1 a maxN-gramas distintos do texto, na ordem em que
// aparecem, até maxPerItem. Um n-grama não pode conter stopword (de nenhum
// idioma permitido), número puro nem palavra curta; e não pode ser formado só
// por palavras das frases de dor, que aparecem em tudo por construção.
func (p *Processor) keywords(toks []Token, lang string) []string {
	valid := make([]bool, len(toks))
	for i, t := range toks {
		valid[i] = p.keywordToken(t.Norm, lang)
	}
	seen := map[string]bool{}
	var out []string
	for i := range toks {
		for n := 1; n <= p.maxN && i+n <= len(toks); n++ {
			if !valid[i+n-1] {
				break
			}
			if !valid[i] {
				break
			}
			gram := toks[i : i+n]
			onlyPhrase := true
			parts := make([]string, n)
			for k, t := range gram {
				parts[k] = t.Norm
				onlyPhrase = onlyPhrase && p.phraseVocab[t.Norm]
			}
			if onlyPhrase {
				continue
			}
			key := strings.Join(parts, " ")
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, key)
			if len(out) >= p.maxPerItem {
				return out
			}
		}
	}
	return out
}

func (p *Processor) keywordToken(n, lang string) bool {
	if len([]rune(n)) < 3 || strings.IndexFunc(n, unicode.IsLetter) < 0 {
		return false
	}
	if Stopwords[lang][n] {
		return false
	}
	for _, l := range p.languages {
		if Stopwords[l][n] {
			return false
		}
	}
	return true
}
