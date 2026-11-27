package process

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"gerador-ideias"
	"gerador-ideias/internal/config"
	"gerador-ideias/internal/db"
)

func newProcessor(t *testing.T) *Processor {
	t.Helper()
	cfg, err := config.Parse(ideias.ExampleConfig)
	if err != nil {
		t.Fatal(err)
	}
	return New(cfg)
}

func item(text string) db.Item {
	return db.Item{Source: "bsky", ExternalID: "x", Text: text, CreatedAt: time.Now()}
}

func TestCandidateFilter(t *testing.T) {
	p := newProcessor(t)
	cases := []struct {
		name      string
		text      string
		hint      string
		candidate bool
		signals   []string // nil = não confere
	}{
		// Inglês
		{"pedido explícito", "Is there an app that tracks invoices in several currencies?", "", true, []string{"request"}},
		{"pagamento + alternativa", "I'd pay for a simple alternative to Harvest, honestly.", "", true, []string{"alternative", "payment"}},
		{"distribuição", "I googled for hours, is there a plugin for Figma that exports tokens?", "", true, []string{"distribution"}},
		{"cancelamento", "Finally cancelled my subscription, the new pricing is absurd", "", true, []string{"churn"}},
		{"reclamação com marca", "Notion sucks on mobile, takes forever to load", "", true, []string{"complaint"}},
		{"reclamação genérica sem marca", "Honestly this whole week sucks", "", false, nil},
		{"falso positivo: preço", "If the espresso machine costs more than what you're willing to pay, try a moka pot.", "", false, nil},
		{"falso positivo: aspirador", "This vacuum sucks up everything", "", false, nil},
		{"falso positivo: 'that sucks'", "Oh no, that sucks, sorry to hear", "", false, nil},
		{"palavra dentro de outra não casa", "Our success sucksess metric is fine", "", false, nil},
		{"sem frase nenhuma", "Shipped a new release of my side project today", "", false, nil},
		// Português
		{"pt pedido sem acento", "alguem conhece um app pra controlar estoque de loja pequena?", "", true, []string{"request"}},
		{"pt pagamento", "Eu pagaria por um app que juntasse todas as notas fiscais.", "pt", true, []string{"payment"}},
		{"pt churn", "Cancelei minha assinatura do app de treino, caro demais", "", true, []string{"churn"}},
		{"pt falso positivo", "Não tem alternativa para isso, é a vida", "", false, nil},
		// Espanhol
		{"es pedido", "¿Hay alguna app para dividir gastos entre compañeros de piso?", "", true, []string{"request"}},
		{"es alternativa", "Busco una alternativa a Trello más barata para mi equipo", "", true, []string{"alternative"}},
		{"es falso positivo", "No hay alternativa a esto, hay que esperar", "", false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			it := item(tc.text)
			it.LangHint = tc.hint
			r := p.Process(it)
			if r.Candidate != tc.candidate {
				t.Errorf("candidato = %v, want %v (score %.2f, frases %v, marcas %v)",
					r.Candidate, tc.candidate, r.PhraseScore, r.Matches, r.Brands)
			}
			if tc.signals != nil && !reflect.DeepEqual(r.Signals, tc.signals) {
				t.Errorf("sinais = %v, want %v", r.Signals, tc.signals)
			}
		})
	}
}

func TestScoreAndEngagement(t *testing.T) {
	p := newProcessor(t)
	low := p.Process(db.Item{Text: "I would pay for a better invoicing tool", Engagement: 0})
	high := p.Process(db.Item{Text: "I would pay for a better invoicing tool", Engagement: 500})
	if low.PhraseScore != 2.0 || low.Score != 2.0 {
		t.Errorf("sem engajamento: %+v", low)
	}
	if high.Score != 4.0 || !high.Candidate {
		t.Errorf("engajamento alto deveria somar o bônus máximo (2.0): %+v", high)
	}
	// A mesma frase repetida conta uma vez.
	rep := p.Process(db.Item{Text: "I would pay for this. Seriously, I would pay for this."})
	if rep.PhraseScore != 2.0 {
		t.Errorf("frase repetida contou duas vezes: %v", rep.PhraseScore)
	}
}

func TestLanguage(t *testing.T) {
	p := newProcessor(t)
	cases := []struct {
		text, hint, want string
	}{
		{"Is there an app that does this for small teams? I would really like it.", "", "en"},
		{"Alguém conhece um app que faça isso? Eu não acho nenhum que seja bom.", "", "pt"},
		{"¿Alguien conoce una app que haga esto? No encuentro ninguna que sea buena.", "", "es"},
		{"Gibt es eine App, die das für kleine Teams macht? Ich finde keine.", "", "de"},
		{"Existe-t-il une app qui fait ça pour les petites équipes ? Je ne trouve rien.", "", "fr"},
		{"whatever text", "pt", "pt"},           // o idioma da fonte vence
		{"Is there an app for it?", "ja", "en"}, // idioma fora do config: detecta
		{"Figma Notion Slack", "", Unknown},
		{"is there an app", "", "en"}, // sem stopwords suficientes: idioma da frase casada
	}
	for _, tc := range cases {
		if got := p.Process(db.Item{Text: tc.text, LangHint: tc.hint}).Lang; got != tc.want {
			t.Errorf("%q (hint %q): lang = %s, want %s", tc.text, tc.hint, got, tc.want)
		}
	}
}

func TestBrands(t *testing.T) {
	p := newProcessor(t)
	cases := []struct {
		text       string
		fromSource []string
		want       []string
	}{
		{"Looking for an alternative to Calendly, it got too expensive", nil, []string{"Calendly"}},
		{"any alternative to the Linear app?", nil, []string{"Linear"}},
		{"I switched from Google Docs last year", nil, []string{"Google Docs"}},
		{"NOTION sucks and so does notion's search", nil, []string{"Notion"}},
		{"ClickUp sucks so much", nil, []string{"ClickUp"}},
		{"iCloud is too expensive for families", nil, []string{"iCloud"}},
		{"Alguém conhece uma alternativa ao Trello? Saí do Asana ano passado", nil, []string{"Asana", "Trello"}},
		{"Busco alternativa a Monday porque es muy caro", nil, []string{"Monday"}},
		// falsos positivos: palavra comum depois do padrão
		{"There is no alternative to this approach", nil, nil},
		{"Everything sucks today", nil, nil},
		{"alternative to it would be nice", nil, nil},
		{"moving away from cloud to on-prem", nil, nil},
		// marca informada pela fonte + citada no texto
		{"Feature request: export to Notion", []string{"owner/repo"}, []string{"Notion", "owner/repo"}},
	}
	for _, tc := range cases {
		it := item(tc.text)
		it.Brands = tc.fromSource
		got := p.Process(it).Brands
		if len(got) == 0 {
			got = nil
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q: marcas = %v, want %v", tc.text, got, tc.want)
		}
	}
}

func TestCountry(t *testing.T) {
	cases := []struct {
		text, lang, want string
	}{
		{"Pago R$ 49 por mês nisso", "pt", "br"},
		{"Alguém conhece um app de finanças?", "pt", "br"},
		{"Preciso de uma app para o telemóvel da equipa", "pt", "pt"},
		{"It costs £30 a month here", "en", "gb"},
		{"Paying ₹999 per month is too much", "en", "in"},
		{"Kostet 20 € im Monat", "de", "de"},
		{"Cuesta 20 € al mes", "es", "es"},
		{"It costs 20€ a month", "en", CountryUnknown}, // euro em inglês: ambíguo
		{"Any app for freelancers in Buenos Aires?", "en", "ar"},
		{"see https://www.loja.com.br/produto", "en", "br"},
		{"see https://cool.io and https://x.ai", "en", CountryUnknown}, // TLDs de vaidade
		{"Hay alguna app para esto?", "es", CountryUnknown},            // espanhol sozinho não define país
		{"Is there an app for this?", "en", CountryUnknown},
		{"I moved from London to Berlin", "en", CountryUnknown}, // empate
	}
	for _, tc := range cases {
		got, reason := EstimateCountry(tc.text, tc.lang)
		if got != tc.want {
			t.Errorf("%q (%s): país = %s (%s), want %s", tc.text, tc.lang, got, reason, tc.want)
		}
	}
}

func TestKeywords(t *testing.T) {
	p := newProcessor(t)
	r := p.Process(db.Item{Text: "Is there an app for invoice tracking? Invoice tracking with multiple currencies is painful. https://example.com/x `code here`"})
	terms := strings.Join(r.Terms, "|")
	for _, want := range []string{"invoice", "invoice tracking", "multiple currencies", "painful"} {
		if !strings.Contains("|"+terms+"|", "|"+want+"|") {
			t.Errorf("faltou %q em %v", want, r.Terms)
		}
	}
	for _, bad := range []string{"app", "there", "example", "https", "code", "is there"} {
		if strings.Contains("|"+terms+"|", "|"+bad+"|") {
			t.Errorf("termo indesejado %q em %v", bad, r.Terms)
		}
	}
	seen := map[string]bool{}
	for _, tm := range r.Terms {
		if seen[tm] {
			t.Errorf("termo repetido: %s", tm)
		}
		seen[tm] = true
	}
	long := p.Process(db.Item{Text: strings.Repeat("alpha beta gamma delta epsilon zeta eta theta iota kappa lambda omicron ", 20) + "unique words everywhere"})
	if len(long.Terms) > 40 {
		t.Errorf("max_per_item ignorado: %d termos", len(long.Terms))
	}
}

func TestTokenizeAndFold(t *testing.T) {
	toks := Tokenize("I’d pay — existe-t-il? Alguém, ¿qué tal? R$49")
	var got []string
	for _, tk := range toks {
		got = append(got, tk.Norm)
	}
	want := []string{"i'd", "pay", "existe-t-il", "alguem", "que", "tal", "r", "49"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tokens = %v", got)
	}
	if toks[3].Raw != "Alguém" {
		t.Errorf("Raw perdeu a forma original: %q", toks[3].Raw)
	}
}

func TestFalsePositivesFromRealSample(t *testing.T) {
	p := newProcessor(t)
	cases := []struct {
		text       string
		candidate  bool
		wantBrands []string
	}{
		// "notion" palavra comum não é a marca Notion.
		{"I reject the notion that users are demanding this, it sucks", false, nil},
		// Maiúscula só por início de frase não é marca.
		{"Progress sucks", false, nil},
		{"I agree. Progress sucks", false, nil},
		// Caixa mista no início de frase continua marca.
		{"ClickUp sucks on mobile", true, []string{"ClickUp"}},
		// Alternativa sem marca é discussão abstrata.
		{"Keep in mind SQLite is a fine replacement for a lot of things", false, nil},
		{"it is a good replacement for some of the openrouter users", false, nil},
		// Com marca, vale.
		{"A good replacement for OpenRouter would offer BAA", true, []string{"OpenRouter"}},
		// Possessivo.
		{"Looking for an alternative to Google's Docs", true, []string{"Google Docs"}},
	}
	for _, tc := range cases {
		r := p.Process(item(tc.text))
		got := r.Brands
		if len(got) == 0 {
			got = nil
		}
		if r.Candidate != tc.candidate || !reflect.DeepEqual(got, tc.wantBrands) {
			t.Errorf("%q: candidato=%v marcas=%v (want %v %v; frases %v)",
				tc.text, r.Candidate, got, tc.candidate, tc.wantBrands, r.Matches)
		}
	}
}

func TestStorageText(t *testing.T) {
	in := "Feature request:\n\n```\nlong stack trace\nline 2\n```\n\n  please   add   export  "
	if got := StorageText(in, 1000); got != "Feature request: [código] please add export" {
		t.Errorf("got %q", got)
	}
	if got := StorageText("ação "+strings.Repeat("x", 50), 10); got != "ação xxxxx…" {
		t.Errorf("corte: %q", got)
	}
}
