# Gerador de Ideias — Documento de Design

> Fonte da verdade do projeto. Mudanças de rumo são registradas aqui primeiro (seção "Registro de decisões"), depois no código.
> Última atualização: 2026-11-26 · **Status: aprovado (v1)**

## 1. Intenção

Um coletor leve, rodando no celular (Termux), que lê diariamente conversas públicas da internet em busca de **dores reais**: gente reclamando de apps, pedindo funcionalidades ou procurando alternativas. Com o tempo ele acumula uma base histórica e, uma vez por semana, entrega um **relatório de dores ranqueadas**, com uma nota de viabilidade feita para o perfil do dono.

**Por quê:** encontrar ideias de micro-SaaS para um dev solo construir nas horas vagas, levar até ~R$ 2 mil líquidos de MRR e **vender** depois. A meta não é tocar uma empresa por anos.

### Perfil do dono (base da nota de viabilidade)
- 8 a 10 horas por semana por projeto.
- Stack atual: Go, PostgreSQL, Flutter e Vue.js. Topa outras stacks.
- Quer projetos leves e baratos, que comecem rodando no Termux e só vão para uma VPS quando houver clientes pagantes.

### A ideia perfeita
- Nicho pequeno, com pouca concorrência. ~500 usuários já é sucesso.
- MRR de R$ 2 mil líquidos, o que dá ≈ US$ 400–450, ou seja ≈ 50–60 pagantes a ~US$ 9/mês.
- Público global, cobrando em dólar.
- Vendável: B2B ou "prosumer" (freelancer, pequeno negócio), com pouco suporte, sem depender do dono e sem risco de plataforma.
- Infraestrutura barata: roda num binário pequeno e não exige GPU, muito armazenamento ou chamada de IA a cada uso.
- **Divulgação quase toda automática.** Os clientes chegam sozinhos, sem que o dono tenha que caçar leads e mandar mensagem privada para cada um. Canais desejados, em ordem:
  1. **SEO**, inclusive SEO programático: o público já pesquisa a dor no Google ("alternativa ao X", "como fazer Y", "template de Z"), e muitas páginas podem ser geradas a partir de dados.
  2. **Marketplaces e diretórios com busca própria**: lojas de apps e plugins (Shopify, Chrome Web Store, WordPress, VS Code, Figma, Notion, Slack, Zapier etc.), onde o cliente procura e instala sozinho.
  3. **Conteúdo para redes sociais gerado e postado de forma automatizada** (o próprio produto gera algo que vale postar, ou dá para criar posts a partir dos dados dele).
  4. **Crescimento embutido no produto**: o uso normal expõe o produto a novos usuários (link "feito com", arquivo ou página compartilhável).

  Excluídos: venda outbound (cold e-mail, DM, LinkedIn), vendas enterprise com reunião ou demo e dependência de comunidade que o dono teria de animar manualmente.

## 2. Objetivos, não-objetivos e anti-objetivos

**Objetivos**
- Coletar todo dia de **Hacker News, Bluesky e GitHub Issues**.
- Guardar um histórico que permita medir frequência e tendência das dores ao longo de semanas.
- Produzir rankings de palavras-chave, marcas e nichos, filtráveis por idioma e por país estimado.
- Entregar um relatório semanal com as ~10 melhores dores, cada uma com nota, evidências (links) e uma ideia de MVP.
- Rodar no Termux ocupando pouco espaço, pouca bateria e pouca memória.

**Não-objetivos (v1)**
- Reddit. A API pública está fechada para novos pedidos desde 31/10/2026 e o que resta será desligado em março de 2027. Raspar o HTML vai contra os termos de uso.
- Painel web e consultas interativas (podem vir depois; o banco já permite).
- Coleta em tempo real ou streaming (firehose).
- Validação automática de ideias (landing pages, anúncios).
- Uso por várias pessoas.

**Anti-objetivos (se acontecer, o projeto falhou mesmo funcionando)**
- O topo do relatório vira reclamação genérica de big tech ("o Windows trava", "redes sociais são ruins").
- O topo do relatório traz ideias que só vendem com prospecção manual (DM, cold e-mail, demo).
- O projeto passa a ter custo fixo de IA. A meta é custo zero, usando os provedores gratuitos via OmniRoute.
- O celular fica perceptivelmente mais lento, ou o banco passa de ~500 MB em um ano.
- Violar os termos de uso de alguma fonte ou guardar dados pessoais além do necessário.
- O relatório demora mais de ~15 minutos para ler.

## 3. Restrições

- **Ambiente:** Termux em Android arm64, sem root. O build é feito no próprio Termux (`pkg install golang`), com o repositório sincronizado via git.
- **Go puro, `CGO_ENABLED=0`.** SQLite via driver puro em Go (ex.: `modernc.org/sqlite`).
- **Agendamento:** `cronie` com `termux-services` e `termux-wake-lock` durante a execução. O Android pode matar processos ou o aparelho pode estar desligado, então cada coletor **retoma de onde parou** (cursor por fonte).
- **Rede:** respeitar os limites de requisição de cada API, com backoff exponencial e User-Agent genérico (sem nome ou e-mail do dono).
- **Segredos:** em `~/.config/ideias/env` (fora do repositório): app password do Bluesky, token do GitHub, URL e chave do OmniRoute, token e chat ID do Telegram.
- **Privacidade:** o autor de cada post é guardado só como hash. Perfis não são raspados.

## 4. Abordagens consideradas

| Abordagem | Prós | Contras | Decisão |
|---|---|---|---|
| **A. Go + SQLite no Termux** | Stack do dono, binário único, leve, sem dependências nativas; migra para VPS ou CI sem reescrever | O Android pode interromper a coleta (resolvido com cursor e retomada) | **Escolhida** |
| B. Python + SQLite no Termux | Bibliotecas de linguagem natural ricas (spaCy, PRAW) | Pesado e difícil de instalar no Android, mais memória, fora da stack | Descartada |
| C. Go + SQLite no GitHub Actions | Não depende do celular | Sai do Termux; banco versionado em repo ou artifacts fica desajeitado | Plano B: mesmo código |
| n8n ou scraper com navegador | Montagem rápida | Pesado demais para o celular; frágil | Descartada |

## 5. Arquitetura

Um binário `ideias` com subcomandos:

```
ideias collect [--source hn|bsky|gh|all]   # diário: coleta, filtra e agrega
ideias report  [--week AAAA-WW] [--no-ai]   # semanal, IA via OmniRoute
ideias export  [--week AAAA-WW]             # pacote para o Claude web (manual)
ideias import  [--week AAAA-WW] resposta.json   # traz a resposta do Claude web e gera o relatório
ideias stats                                # saúde: tamanho do banco, última coleta por fonte, custo de IA do mês
ideias migrate                              # aplica migrações do banco
ideias check                                # alertas pelo Telegram: fonte falhando 2 dias seguidos, nenhuma coleta há 48 h
ideias backup                               # backup comprimido do banco (também roda sozinho depois de cada relatório)
```

No Termux, o cron chama os comandos por `scripts/run.sh`, que segura o wake lock durante a execução e grava um log rotacionado.

### 5.1 Fluxo diário (`collect`)
1. Para cada fonte, lê o cursor (último timestamp ou ID) e busca tudo desde então, recuperando dias perdidos.
2. Normaliza num formato comum: `source, external_id, url, created_at, text, author_hash, lang_hint, engagement` (pontos, likes ou reações 👍).
3. Remove duplicados por `(source, external_id)`.
4. Detecta o idioma com uma biblioteca leve restrita a EN, PT, ES, DE e FR. Se a fonte já informa o idioma (Bluesky `langs`), usa essa informação.
5. **Filtro de candidatos (sem IA):** busca frases de dor e de pedido por idioma, definidas num arquivo de configuração editável. Exemplos: "is there an app", "alternative to", "I'd pay for", "I wish there was", "why doesn't X", "cancelled my subscription", "tem algum app que", "alguém conhece uma alternativa", "pagaria por". Cada candidato ganha um `pain_score` simples (quantas frases casaram, peso por tipo, engajamento).
6. Extrai **marcas** (lista-semente editável, padrões como "alternative to X" e "X sucks", nome do repositório no GitHub) e **palavras-chave** (1–3-gramas sem stopwords).
7. **Armazenamento econômico:**
   - Candidatos: guarda o texto (truncado em ~2.000 caracteres) e os metadados.
   - Não-candidatos: o texto é descartado. Só alimentam contagens agregadas por dia, fonte e idioma (termos e marcas).
8. Atualiza o cursor e registra a execução (`runs`: início, fim, itens, erros).

### 5.2 Fluxo semanal (`report`, domingo)
1. Seleciona os candidatos da semana.
2. **Agrupamento por IA, em lotes:** junta candidatos que descrevem a mesma dor, dá nome à dor e ao nicho e associa cada uma a um **cluster persistente** já existente ou cria um novo. Os clusters vivem no banco, o que permite medir a tendência semana a semana.
3. **Pontuação por IA**, só nos ~20 clusters mais relevantes, usando a rubrica da seção 6.
4. Junta os rankings locais (palavras-chave, marcas, nichos, por idioma e país estimado, tendência contra a média das 4 semanas anteriores).
5. Gera `reports/AAAA-WW.md` e envia pelo **Telegram** (resumo na mensagem e o .md como anexo).
6. `--no-ai` gera só os rankings locais, sem IA.
7. **Modo manual (Claude pela web):** `ideias export --week AAAA-WW` gera um pacote para usar no claude.ai, sem nenhuma API:
   - `export/AAAA-WW/prompt.md`: as instruções de agrupamento e pontuação (mesma rubrica da seção 6) e o **formato JSON exato** da resposta esperada;
   - `export/AAAA-WW/candidatos.md`: os candidatos da semana e os clusters existentes, para anexar como arquivo na conversa (se for grande demais, dividido em partes numeradas, com instruções para a IA só responder depois da última).

   Depois, `ideias import --week AAAA-WW resposta.json` valida o JSON colado, atualiza os `clusters` como no fluxo automático e gera o mesmo relatório. Ou seja, o resultado manual e o automático são intercambiáveis e o histórico de tendência não quebra.

### 5.3 Fontes (v1)

| Fonte | Como acessar | Sinal útil | Observações |
|---|---|---|---|
| Hacker News | API pública do Algolia (`search_by_date`, filtro por `created_at_i`), stories e comments | Pontos e nº de comentários; posts "Ask HN" | Quase tudo em inglês, público dev e B2B. Sem país. |
| Bluesky | `app.bsky.feed.searchPosts` autenticado com app password; parâmetros `q`, `since` e `lang` | Likes e reposts; campo `langs` | Público global, com muitos brasileiros. Sem país; estimativa por idioma e conteúdo. |
| GitHub Issues | Search API (`is:issue`, labels de enhancement ou feature request, frases de dor, `created:>`) com token | Reações 👍 e comentários; repositório = marca | Limite de 1.000 resultados por busca, então dividir por janelas de tempo. Sinal forte de pedido de funcionalidade. |

Cada fonte implementa a mesma interface (`Collector`), para plugar outras depois (Stack Exchange, Lemmy, Mastodon).

### 5.4 País estimado
Nenhuma das fontes informa o país. A v1 usa heurística e marca o resultado como **estimativa**:
- o idioma (PT → provável BR, a menos que haja sinais de PT-PT);
- menções a moeda (R$, €, £, ₹), cidades ou países no texto;
- domínio ou TLD de links citados.

A v2 pode consultar o campo `location` do perfil do GitHub, mas apenas dos autores dos clusters do top 10.

### 5.5 IA
- **Automático: via [OmniRoute](https://github.com/diegosouzapw/OmniRoute)**, um gateway de IA auto-hospedado que expõe uma API compatível com OpenAI (padrão `http://localhost:20128/v1`) e distribui as chamadas entre vários provedores, com fallback quando um falha ou esgota a cota. O `ideias` só conhece um cliente OpenAI-compatível. Provedores, modelos e combos ficam configurados no próprio OmniRoute.
- Configuração no `config.toml`/env: `OMNIROUTE_BASE_URL`, `OMNIROUTE_API_KEY` (se houver), um modelo para agrupar e outro para pontuar (nomes ou combos do OmniRoute).
- **O OmniRoute já existe e está fora do escopo deste projeto.** Ele roda sempre ativo no Termux, já configurado e compartilhado com outros projetos do dono. O `ideias` não instala, não configura, não liga e não desliga o OmniRoute: apenas consome a API. Antes do `report`, checa `GET /v1/models`; se não responder, cai no fallback manual. A URL continua configurável.
- **Robustez:** respostas em JSON validadas contra um esquema; em caso de JSON inválido, repete uma vez. Se o OmniRoute estiver fora do ar ou todas as chamadas falharem, o relatório sai em modo `--no-ai`, o pacote do modo manual (5.2 passo 7) é gerado automaticamente e o Telegram avisa.
- **Limite:** como a meta é custo zero, o limite é em **tokens por semana** (configurável), para não esgotar as cotas gratuitas. `ai_usage` registra tokens, modelo e provedor usado (quando o OmniRoute informar).
- **Manual: Claude pela web** (claude.ai), nunca pela API da Anthropic. Ver 5.2 passo 7.
- Os prompts de agrupamento e de pontuação ficam em arquivos versionados no repositório (`prompts/`), não no código. Os modos automático e manual usam os mesmos arquivos.

### 5.6 Dados (tabelas principais)
- `items`: candidatos com texto, metadados, `pain_score` e `cluster_id`.
- `daily_terms` e `daily_brands`: contagens agregadas por dia, fonte, idioma, país estimado e termo/marca.
- `clusters`: dor, nicho, primeira e última vez vista, histórico de menções por semana e última nota.
- `cursors`, `runs` e `ai_usage`.

Meta de tamanho: abaixo de 200 MB por ano. O `ideias stats` mostra o tamanho atual.

**Backup:** depois de cada relatório, `VACUUM INTO` gera uma cópia comprimida em `~/storage/shared/ideias-backup/`, mantendo as 4 últimas. Assim o histórico sobrevive a uma reinstalação do Termux.

### 5.7 Repositório e deploy
Repositório git privado (ex.: GitHub), desenvolvido no PC com o Claude Code. No celular: `git pull && go build` dentro do Termux. Configuração editável (frases de dor, marcas-semente, idiomas, pesos da rubrica, modelos do OmniRoute, limite semanal de tokens) em `config.toml`, sem recompilar.

## 6. Rubrica de viabilidade

Cada critério vai de 0 a 5. A nota final é a média ponderada, de 0 a 100.

| Critério | Peso | 5 = |
|---|---|---|
| Frequência e tendência da dor | 10% | Aparece toda semana, em mais de uma fonte, e está crescendo |
| Sinal de pagamento | 20% | Menções a preço, "I'd pay", cancelamento de concorrente pago |
| **Distribuição automática** | 15% | O público acha o produto sozinho: busca a dor no Google (cabe SEO programático), procura num marketplace ou diretório de apps/plugins, ou o produto gera conteúdo ou links compartilháveis que divulgam por si |
| Público B2B ou prosumer | 10% | Pequenos negócios, freelancers ou times; uso ligado a ganhar dinheiro |
| Global e em USD | 10% | Dor sem fronteira de idioma ou país, público fora do BR |
| Construível solo | 15% | MVP em até ~6 semanas a 8–10 h/semana, sem app nativo complexo nem integrações frágeis |
| Vendabilidade | 10% | Pouco suporte, roda sozinho, sem depender do dono nem de uma única plataforma ou API |
| Infraestrutura barata | 10% | Cabe num binário pequeno no Termux ou numa VPS mínima; sem IA ou GPU a cada uso |

Para dar a nota de **distribuição automática**, a IA deve dizer qual é o canal principal provável (SEO, SEO programático, marketplace X, conteúdo automatizado, crescimento embutido) e citar o indício que encontrou nos posts. Exemplos de indício: "procurei no Google e não achei", "is there a plugin for X", dor ligada a uma plataforma com loja de apps, resultado do produto que as pessoas naturalmente postariam.

**Penalidades automáticas:** dor genérica de big tech; exige efeito de rede (marketplace, rede social); setor regulado (saúde, finanças com custódia); depende de API que pode fechar (o próprio caso do Reddit); **só vende com prospecção manual** (outbound, DM, cold e-mail, demo, venda enterprise) ou com uma comunidade que o dono precisaria animar à mão.

**Bônus de concorrência:** existe um concorrente caro ou complexo que as pessoas citam com raiva, o que abre espaço para uma versão "mais simples e barata". Nenhum concorrente e nenhum sinal de pagamento é ponto de atenção, não bônus.

## 7. Formato do relatório semanal

```
# Ideias — semana 2026-48
Coleta: HN 1.240 · Bluesky 3.880 · GitHub 610 → 214 candidatos → 37 dores
IA: OmniRoute · 182k tokens (limite semanal 500k) · modo automático

## Top 10 dores
### 1. Freelancers sem jeito simples de cobrar horas em várias moedas — 78/100 (↑ 3 semanas)
Nicho: faturamento p/ freelancers · Idiomas: EN 70%, PT 20% · País est.: global
Menções: 9 nesta semana / 31 no total · Marcas: Harvest, Toggl, FreshBooks
Notas: pagamento 5 · distribuição 4 · B2B 4 · global 5 · solo 4 · venda 4 · infra 5 · freq. 4
Canal provável: SEO programático ("invoice template for {país/moeda}") + listagem no diretório do Stripe Apps
MVP em uma frase: ...
Por que pode falhar: ...
Evidências: [link] [link] [link]

## Rankings
Palavras-chave em alta · Marcas mais citadas · Nichos · Por idioma · Por país estimado

## Saúde da coleta
Dias sem coleta, erros por fonte, tamanho do banco
```

## 8. Sucesso

- Depois de 4 a 6 semanas de coleta, o relatório trouxe **pelo menos 3 ideias** que o dono consideraria construir.
- A leitura semanal leva menos de 15 minutos.
- Custo de IA zero, banco abaixo de 200 MB/ano, nenhum dia de coleta perdido sem ser recuperado.

## 9. Decisões tomadas pelo dono
- Saída: relatório semanal de dores ranqueadas, com o banco por baixo.
- Abordagem A: Go + SQLite no Termux.
- Fontes: Hacker News, Bluesky e GitHub Issues.
- IA do relatório semanal via **OmniRoute**. A Anthropic entra **apenas pelo Claude web**, num modo manual de exportação e importação. Nada de API da Anthropic.
- O OmniRoute roda no Termux, sempre ativo e já configurado. O projeto só se integra a ele.
- Critérios da ideia perfeita (seção 1), incluindo divulgação quase toda automática (SEO, marketplaces, conteúdo automatizado), sem prospecção manual.
- Esta conversa e este documento são a fonte da verdade; o código é escrito pelo Claude Code a partir de prompts por fase (`docs/roadmap.md`).

## 10. Decisões do designer (revisáveis)
- Telegram como canal de entrega do relatório.
- Relatório gerado aos domingos.
- Limite de IA em tokens por semana (e não em dólares), para preservar as cotas gratuitas do OmniRoute.
- Fallback automático para o pacote manual quando o OmniRoute falha.
- Build direto no Termux em vez de cross-compile.
- Pesos da rubrica (seção 6), a recalibrar depois de 4 semanas.
- Idiomas detectados: EN, PT, ES, DE e FR.

## 11. Fica a critério de quem implementa
- Bibliotecas exatas (detecção de idioma, cliente HTTP, CLI).
- Esquema detalhado das tabelas e índices.
- Redação dos prompts de IA, desde que sigam a rubrica.
- Lista inicial de frases de dor e de marcas-semente (precisa ser editável sem recompilar).

## 12. Registro de decisões
| Data | Decisão | Motivo |
|---|---|---|
| 2026-11-26 | Reddit fora da v1 | API fechada para novos pedidos (31/10/2026); desligamento total em março de 2027 |
| 2026-11-26 | HN + Bluesky + GitHub Issues | Abertas, legais, com sinal de dor; Bluesky informa idioma |
| 2026-11-26 | IA via OmniRoute; Anthropic só pelo Claude web (export/import manual) | Custo zero; nenhuma chave de API paga |
| 2026-11-26 | Novo critério "Distribuição automática" (15%) e penalidade para venda com prospecção manual; pesos de frequência, B2B e vendabilidade caem para 10% | O dono não quer caçar leads manualmente; quer divulgar por SEO ou conteúdo automatizado |
| 2026-11-26 | OmniRoute é infraestrutura externa já existente (sempre ativo no Termux); o projeto só consome a API | Já usado em outros projetos do dono |
| 2026-11-26 | Novos subcomandos `check` (alertas, pelo cron a cada 6 h e depois de cada coleta) e `backup` (em Go, sem depender do binário sqlite3) | Um coletor parado não consegue alertar sobre a própria parada; o backup precisa rodar igual no PC e no celular |
