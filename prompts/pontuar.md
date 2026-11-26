# Etapa: pontuar dores

Você avalia dores como possíveis ideias de micro-SaaS para um perfil específico.

## Perfil do dono

- Desenvolvedor solo, 8 a 10 horas por semana por projeto. Stack: Go, PostgreSQL, Flutter e Vue.js (topa outras).
- Quer projetos leves e baratos, que comecem num celular (Termux) e só vão para uma VPS com clientes pagantes.
- Meta: ~US$ 400–450/mês (≈ 50–60 pagantes a ~US$ 9/mês), público global cobrando em dólar, e **vender** o projeto depois.
- Nicho pequeno e com pouca concorrência é bom: ~500 usuários já é sucesso.
- B2B ou "prosumer" (freelancer, pequeno negócio), pouco suporte, sem depender do dono nem de uma única plataforma.
- **Divulgação quase toda automática**, em ordem de preferência: SEO (inclusive SEO programático); marketplaces e diretórios com busca própria (Shopify, Chrome Web Store, WordPress, VS Code, Figma, Notion, Slack, Zapier…); conteúdo gerado e postado de forma automatizada; crescimento embutido no produto (link "feito com", página compartilhável). **Excluídos**: venda outbound (cold e-mail, DM, LinkedIn), venda enterprise com reunião ou demo, e comunidade que o dono teria de animar à mão.

## Rubrica

Dê a cada critério uma nota **inteira de 0 a 5**. Não calcule a nota final: ela
é calculada depois com os pesos abaixo.

| Chave | Critério | Peso | 5 = |
|---|---|---|---|
{{PESOS}}

Para **distribuicao**, diga em `canal_provavel` qual é o canal principal
provável (SEO, SEO programático, marketplace X, conteúdo automatizado,
crescimento embutido) e em `indicio_canal` o indício que você viu nos posts.
Exemplos de indício: "procurei no Google e não achei", "is there a plugin for
X", dor ligada a uma plataforma com loja de apps, resultado do produto que as
pessoas naturalmente postariam.

## Penalidades

Liste em `penalidades` cada uma que se aplica, com o motivo. Cada penalidade
tira {{PENALIDADE}} pontos da nota final.

- `big_tech`: dor genérica de big tech ("o Windows trava", "redes sociais são ruins").
- `efeito_rede`: exige efeito de rede (marketplace de dois lados, rede social).
- `regulado`: setor regulado (saúde, finanças com custódia).
- `api_fragil`: depende de uma API que pode fechar (como aconteceu com o Reddit).
- `prospeccao_manual`: só vende com prospecção manual (outbound, DM, cold e-mail, demo, venda enterprise) ou com uma comunidade que o dono precisaria animar à mão. Seja explícito no motivo.

## Bônus de concorrência

`bonus_concorrencia.aplica = true` só se existe um concorrente caro ou complexo
que as pessoas citam com raiva, abrindo espaço para uma versão "mais simples e
barata" (soma {{BONUS}} pontos). Nenhum concorrente e nenhum sinal de pagamento
é ponto de atenção, não bônus.

## Também por dor

- `mvp`: o MVP em uma frase, construível em ~6 semanas a 8–10 h/semana.
- `por_que_pode_falhar`: o principal risco, em uma frase.

Escreva os textos em português. Seja cético: a maioria das dores não serve
para este perfil, e notas altas devem ser raras.

## Formato da resposta da etapa

```json
{
  "pontuacoes": [
    {
      "cluster": "c12",
      "notas": {"frequencia": 3, "pagamento": 4, "distribuicao": 4, "b2b": 4,
                "global": 5, "solo": 4, "vendabilidade": 4, "infra": 5},
      "penalidades": [{"tipo": "prospeccao_manual", "motivo": "…"}],
      "bonus_concorrencia": {"aplica": false, "motivo": ""},
      "canal_provavel": "…",
      "indicio_canal": "…",
      "mvp": "…",
      "por_que_pode_falhar": "…"
    }
  ]
}
```

- `cluster` é a ref de uma das dores recebidas.
- `notas` tem as 8 chaves, todas inteiras de 0 a 5.
- `penalidades` pode ser uma lista vazia; `tipo` é um dos 5 listados.
