package process

import (
	"sort"
	"strings"
	"unicode"
)

// Palavras que começam com maiúscula mas não são marcas (início de frase,
// pronomes, termos genéricos), além das stopwords de todos os idiomas.
var notBrands = map[string]bool{
	"i": true, "it": true, "this": true, "that": true, "the": true, "my": true, "our": true,
	"something": true, "anything": true, "everything": true, "nothing": true, "someone": true,
	"life": true, "work": true, "people": true, "software": true, "tool": true, "tools": true,
	"app": true, "apps": true, "email": true, "edit": true, "update": true,
	"ai": true, "llm": true, "api": true, "saas": true, "web": true, "online": true,
	"isso": true, "aquilo": true, "esse": true, "essa": true, "esto": true, "eso": true,
	"ele": true, "ela": true, "el": true, "ella": true, "tudo": true, "todo": true,
}

var articles = map[string]bool{
	"the": true, "a": true, "an": true, "o": true, "os": true, "as": true, "um": true, "uma": true,
	"el": true, "la": true, "los": true, "las": true, "un": true, "una": true,
	"le": true, "les": true, "der": true, "die": true, "das": true,
}

// brands junta as marcas da lista-semente, dos padrões e as que a fonte já
// informou (owner/repo do GitHub), sem repetir (comparação sem caixa).
func (p *Processor) brands(toks []Token, fromSource []string) []string {
	ns := norms(toks)
	found := map[string]string{} // chave normalizada -> nome exibido
	add := func(name string) {
		key := Fold(name)
		if _, ok := found[key]; !ok && key != "" {
			found[key] = name
		}
	}
	for _, b := range fromSource {
		add(b)
	}
	for _, s := range p.seeds {
		for i := range ns {
			// Marca que também é palavra comum ("notion", "harvest", "slack")
			// só conta escrita com maiúscula, como nome próprio.
			if hasAt(ns, i, s.toks) && (hasUpper(toks[i].Raw) || !hasUpper(s.name)) {
				add(s.name)
				break
			}
		}
	}
	for _, pat := range p.patterns {
		for i := range ns {
			if name := p.capture(toks, ns, i, pat); name != "" {
				add(name)
			}
		}
	}
	out := make([]string, 0, len(found))
	for _, name := range found {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// capture tenta casar o padrão a partir de i e devolve a marca capturada.
func (p *Processor) capture(toks []Token, ns []string, i int, pat pattern) string {
	switch {
	case len(pat.before) > 0:
		// "alternative to {marca}": a marca vem depois.
		if !hasAt(ns, i, pat.before) {
			return ""
		}
		j := i + len(pat.before)
		for j < len(toks) && articles[ns[j]] {
			j++
		}
		if j >= len(toks) || !p.brandLike(toks[j]) {
			return ""
		}
		name := p.canonical(toks[j : j+1])
		// Marca de duas palavras ("Google Docs").
		if j+1 < len(toks) && toks[j+1].Spaced && p.brandLike(toks[j+1]) && isUpperStart(toks[j+1].Raw) {
			name = p.canonical(toks[j : j+2])
		}
		return name
	default:
		// "{marca} sucks": a marca vem antes.
		if i == 0 || !hasAt(ns, i, pat.after) {
			return ""
		}
		j := i - 1
		if !p.brandLike(toks[j]) {
			return ""
		}
		if j > 0 && toks[j].Spaced && p.brandLike(toks[j-1]) && isUpperStart(toks[j-1].Raw) && isUpperStart(toks[j].Raw) {
			return p.canonical(toks[j-1 : j+1])
		}
		return p.canonical(toks[j : j+1])
	}
}

// brandLike: parece nome próprio (maiúscula em algum lugar, ex.: "Notion",
// "iCloud", "QuickBooks") ou está na lista-semente; e não é palavra comum.
func (p *Processor) brandLike(t Token) bool {
	if len([]rune(t.Raw)) < 2 || notBrands[t.Norm] || isStopword(t.Norm) || isNumber(t.Norm) {
		return false
	}
	for _, s := range p.seeds {
		if len(s.toks) == 1 && s.toks[0] == t.Norm {
			return true
		}
	}
	// No início de frase, "Progress sucks" tem maiúscula só pela gramática:
	// ali só vale caixa mista ou sigla ("ClickUp", "iCloud", "AWS").
	if t.Start && isTitleCase(t.Raw) {
		return false
	}
	return hasUpper(t.Raw)
}

func hasUpper(s string) bool { return strings.IndexFunc(s, unicode.IsUpper) >= 0 }

// isTitleCase: só a primeira letra é maiúscula ("Progress").
func isTitleCase(s string) bool {
	rs := []rune(s)
	return len(rs) > 1 && unicode.IsUpper(rs[0]) && string(rs[1:]) == strings.ToLower(string(rs[1:]))
}

// canonical devolve o nome da lista-semente, se houver; senão normaliza a
// caixa: "NOTION"/"notion" viram "Notion", "iCloud" fica como está.
func (p *Processor) canonical(toks []Token) string {
	key := strings.Join(norms(toks), " ")
	for _, s := range p.seeds {
		if strings.Join(s.toks, " ") == key {
			return s.name
		}
	}
	parts := make([]string, len(toks))
	for i, t := range toks {
		parts[i] = fixCase(t.Raw)
	}
	return strings.Join(parts, " ")
}

func fixCase(s string) string {
	if s == strings.ToLower(s) || s == strings.ToUpper(s) && len([]rune(s)) > 4 {
		rs := []rune(strings.ToLower(s))
		rs[0] = unicode.ToUpper(rs[0])
		return string(rs)
	}
	return s
}

func isUpperStart(s string) bool {
	for _, r := range s {
		return unicode.IsUpper(r)
	}
	return false
}

func isStopword(n string) bool {
	for _, sw := range Stopwords {
		if sw[n] {
			return true
		}
	}
	return false
}

func isNumber(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return !unicode.IsDigit(r) }) < 0
}
