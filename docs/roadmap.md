# Gerador de Ideias: roadmap e prompts para o Claude Code

> Cada fase tem um prompt pronto para colar no Claude Code, aberto na raiz do repositório `gerador-ideias`.
> Regra de ouro: **uma fase por sessão**. Revise e faça o commit antes de passar para a próxima.
> A fonte da verdade é `docs/design.md`. Se uma fase exigir mudar o design, o documento é atualizado primeiro.

| Fase | Entrega | Estimativa (8–10 h/sem) | Status |
|---|---|---|---|
| F0 | Esqueleto: CLI, config, SQLite, migrações | 1 semana | ⬜ |
| F1 | Coletor do Hacker News com cursor e retomada | 1 semana | ⬜ |
| F2 | Coletores do Bluesky e do GitHub Issues | 1–2 semanas | ⬜ |
| F3 | Processamento local: idioma, candidatos, marcas, termos, país estimado | 1–2 semanas | ⬜ |
| F4 | Relatório sem IA e envio pelo Telegram | 1 semana | ⬜ |
| F5 | IA via OmniRoute + modo manual para o Claude web (export/import) | 1–2 semanas | ⬜ |
| F6 | Termux: agendamento, wake lock, alertas e backup | 0,5–1 semana | ⬜ |
| F7 | Calibração depois de 4 semanas de dados | contínuo | ⬜ |

A partir da F6, a coleta já roda de verdade todo dia. A F7 começa ~4 semanas depois.

---

## F0: Esqueleto

```
Leia docs/design.md inteiro antes de começar. Ele é a fonte da verdade deste projeto.

Implemente apenas a FASE F0 (esqueleto) do projeto "ideias":
- Módulo Go (versão estável atual), binário único `ideias`, CGO_ENABLED=0 (precisa compilar no Termux, Android arm64).
- CLI com os subcomandos collect, report, export, import, stats e migrate (seção 5 do design). Por enquanto, collect, report, export e import só imprimem "não implementado".
- Config em `config.toml` (lido de ./config.toml ou ~/.config/ideias/config.toml) com: idiomas, frases de dor por idioma, marcas-semente, pesos da rubrica, modelos do OmniRoute (agrupar e pontuar), limite semanal de tokens e caminho do banco. Inclua um `config.example.toml` comentado.
- Segredos lidos de variáveis de ambiente ou de ~/.config/ideias/env. Nunca versionar segredos; adicionar ao .gitignore.
- SQLite com driver puro em Go (sem CGO) e migrações versionadas e embutidas no binário, criando as tabelas da seção 5.6 (items, daily_terms, daily_brands, clusters, cursors, runs, ai_usage). Escolha índices pensando nas consultas por semana, fonte e idioma.
- `ideias stats` mostrando o tamanho do banco e a contagem de linhas por tabela.
- Makefile ou script com: build, test, e build-android (GOOS=android GOARCH=arm64).
- README curto explicando como compilar no PC e no Termux (pkg install golang git).

Critérios de aceite: `go test ./...` passa; `ideias migrate && ideias stats` funciona com um banco novo; o build para android/arm64 compila.
Não implemente coletores nem IA nesta fase. No fim, liste o que ficou decidido por você (bibliotecas, esquema) para eu registrar no design.
```

## F1: Coletor do Hacker News

```
Leia docs/design.md (principalmente as seções 3, 5.1 e 5.3) e o código existente.

Implemente apenas a FASE F1: o coletor do Hacker News.
- Defina a interface `Collector` comum a todas as fontes (seção 5.3), retornando itens no formato normalizado da seção 5.1 passo 2.
- Use a API pública do Algolia (search_by_date) para stories e comments, buscando as frases de dor do config.toml desde o cursor salvo (created_at_i).
- Cursor por fonte na tabela `cursors`. Se o celular ficou dias sem rodar, recupere todo o período perdido, paginando.
- Remova duplicados por (source, external_id). Guarde o autor só como hash.
- Rate limit educado, backoff exponencial, timeout, User-Agent genérico (sem nome ou e-mail pessoal).
- Registre cada execução em `runs` (início, fim, itens novos, erros).
- `ideias collect --source hn` funcionando. Nesta fase, salve TODOS os itens coletados em `items` (o filtro de candidatos vem na F3).

Testes: use respostas HTTP gravadas (fixtures), sem bater na rede durante `go test`. Teste paginação, retomada de cursor e deduplicação.
Critério de aceite: duas execuções seguidas não duplicam itens; apagar o cursor e rodar de novo recupera o período.
```

## F2: Coletores do Bluesky e do GitHub Issues

```
Leia docs/design.md (seções 3, 5.1 e 5.3) e o código existente, especialmente a interface Collector e o coletor do HN como referência.

Implemente apenas a FASE F2:
1) Bluesky: autentique com app password (BSKY_HANDLE e BSKY_APP_PASSWORD do env), use app.bsky.feed.searchPosts com q, since e lang para cada frase de dor e idioma do config. Aproveite o campo `langs` do post como lang_hint e likes/reposts como engagement. Renove a sessão quando expirar.
2) GitHub Issues: Search API com GITHUB_TOKEN. Busque issues com labels de enhancement ou feature request e as frases de dor, filtrando por created:>cursor. A busca devolve no máximo 1.000 resultados: divida em janelas de tempo menores quando bater no limite. Use reações 👍 e comentários como engagement e owner/repo como marca.
- Os dois respeitam rate limit (leia os headers de limite), backoff e retomada por cursor, como no HN.
- `ideias collect --source all` roda as três fontes; a falha de uma não derruba as outras (registre o erro em runs).

Testes com fixtures, sem rede. Critério de aceite: as três fontes coletam, retomam e deduplicam; `ideias stats` mostra itens por fonte.
```

## F3: Processamento local (sem IA)

```
Leia docs/design.md (seções 5.1 passos 4 a 7, 5.4 e 5.6) e o código existente.

Implemente apenas a FASE F3, o processamento local dentro de `collect`:
- Detecção de idioma leve, restrita aos idiomas do config; quando houver lang_hint, use-o.
- Filtro de candidatos por frases de dor/pedido por idioma (vindas do config.toml), com pain_score = frases casadas × peso do tipo + bônus de engajamento. Pesos no config.
- Marque em cada candidato os tipos de sinal encontrados (dor, pedido, pagamento, distribuição). Sinais de distribuição automática (seção 6 do design) são frases de intenção de busca ou de marketplace, como "googled", "searched everywhere", "is there a plugin/extension/app for", "chrome extension", "shopify app", "wordpress plugin", "procurei no google", "tem alguma extensão". Ficam no config, com equivalentes em PT e ES.
- Extração de marcas: lista-semente + padrões ("alternative to X", "X sucks", "switched from X", e equivalentes em PT e ES) + owner/repo no GitHub. Normalize variações de caixa.
- Palavras-chave: 1 a 3-gramas sem stopwords por idioma.
- País estimado por heurística (seção 5.4), sempre marcado como estimativa, com "desconhecido" como padrão.
- Armazenamento econômico: só os candidatos guardam texto (truncado em ~2.000 caracteres) em items. Os não-candidatos apenas incrementam daily_terms e daily_brands. Ajuste a F1/F2 para não guardar mais tudo em items.
- `ideias stats` passa a mostrar a taxa de candidatos por fonte e idioma e o tamanho estimado de crescimento por mês.

Testes de tabela para o filtro, as marcas e o país estimado, com exemplos em EN, PT e ES (inclua falsos positivos óbvios).
Critério de aceite: com uma amostra real de um dia, o banco cresce pouco e os candidatos fazem sentido ao ler 20 aleatórios. Mostre essa amostra no final.
```

## F4: Relatório sem IA e Telegram

```
Leia docs/design.md (seções 5.2 e 7) e o código existente.

Implemente apenas a FASE F4: `ideias report --no-ai`.
- Gere reports/AAAA-WW.md no formato da seção 7, SEM a parte de dores agrupadas (essa é da F5). Inclua: números da coleta da semana, palavras-chave em alta (tendência contra a média das 4 semanas anteriores), marcas mais citadas, top candidatos por pain_score com links, recortes por idioma e por país estimado, e a saúde da coleta (dias sem coleta, erros por fonte, tamanho do banco).
- Envio pelo Telegram (TELEGRAM_BOT_TOKEN e TELEGRAM_CHAT_ID): uma mensagem com o resumo e o .md como documento anexo. Se o envio falhar, o arquivo local continua gerado.
- `--week AAAA-WW` regera uma semana passada.

Testes com um banco de exemplo gerado nos testes. Critério de aceite: o relatório gerado a partir de dados reais é legível em menos de 5 minutos e chega no Telegram.
```

## F5: IA via OmniRoute + modo manual para o Claude web

```
Leia docs/design.md (seções 5.2, 5.5, 6 e 7) e o código existente.

Implemente apenas a FASE F5: a parte de IA do relatório, em dois modos que usam os MESMOS prompts e o MESMO esquema de resposta.

Modo automático (`ideias report`), via OmniRoute:
- Cliente HTTP compatível com OpenAI (chat completions), apontando para OMNIROUTE_BASE_URL (padrão http://localhost:20128/v1), com OMNIROUTE_API_KEY opcional. Não use SDK de nenhum provedor específico; NÃO integre a API da Anthropic.
- Modelos para agrupar e pontuar vêm do config.toml (nomes ou combos configurados no OmniRoute).
- Antes de começar, verifique GET /v1/models. Se o OmniRoute estiver fora do ar, ou se as chamadas falharem depois dos retries, gere o relatório em modo --no-ai, gere automaticamente o pacote manual (abaixo) e avise pelo Telegram.
- Agrupamento em lotes: envie os candidatos da semana com a lista dos clusters existentes (nome e resumo) e peça para associar cada item a um cluster existente ou criar um novo, com nome da dor e do nicho. Respostas em JSON validadas contra um esquema; repita uma vez se vier inválido.
- Atualize `clusters` com o histórico de menções por semana, para calcular a tendência.
- Pontue só os ~20 clusters mais relevantes com a rubrica da seção 6 (pesos do config), com penalidades e bônus de concorrência, mais "Canal provável" (com o indício encontrado nos posts), "MVP em uma frase" e "por que pode falhar". Ideias que só vendem com prospecção manual (DM, cold e-mail, demo, enterprise) devem ser penalizadas de forma explícita, com o motivo.
- Limite semanal de TOKENS (config). Registre tokens, modelo e provedor (se o OmniRoute informar) em ai_usage. Se a próxima etapa estourar o limite, pare e caia no fallback acima.

Modo manual para o Claude web (claude.ai), sem nenhuma API:
- `ideias export --week AAAA-WW` gera export/AAAA-WW/prompt.md (instruções + rubrica + esquema JSON exato da resposta, pedindo que a resposta seja SÓ um bloco JSON) e candidatos.md (candidatos da semana + clusters existentes). Se candidatos.md passar de um tamanho seguro para anexar, divida em partes numeradas, com instruções para a IA esperar a última parte antes de responder.
- `ideias import --week AAAA-WW resposta.json` aceita o JSON colado (tolerando cercas ```json e texto em volta), valida contra o mesmo esquema, mostra erros claros indicando o que corrigir, atualiza clusters igual ao modo automático e gera o relatório. O resultado deve ser indistinguível do modo automático, exceto pela linha "IA: Claude web (manual)" no cabeçalho.

Geral:
- Prompts em arquivos versionados em prompts/, compartilhados pelos dois modos.
- Complete o relatório com a seção "Top 10 dores" exatamente no formato da seção 7.

Testes: servidor OpenAI-compatível falso (httptest) para o modo automático, incluindo OmniRoute fora do ar e JSON inválido; ida e volta export → import com uma resposta de exemplo.
Critério de aceite: uma semana real roda via OmniRoute dentro do limite de tokens; o mesmo pacote exportado, respondido no claude.ai e importado, gera um relatório equivalente; o top 10 não traz reclamação genérica de big tech.
```

## F6: Rodar de verdade no Termux

```
Leia docs/design.md (seções 3, 5.6 e 5.7) e o código existente.

Implemente apenas a FASE F6: operação no Termux.
- Script scripts/termux-setup.sh idempotente: instala golang, git, cronie, termux-services e termux-api; habilita o crond; cria ~/.config/ideias/ com modelos de env e config; roda termux-setup-storage se necessário.
- Script scripts/run.sh que envolve as execuções com termux-wake-lock / termux-wake-unlock, grava log rotacionado em ~/.local/state/ideias/ e nunca deixa o wake lock preso em caso de erro.
- Crontab sugerido: collect todo dia (horário configurável, ex.: 03:30) e report aos domingos (ex.: 09:00).
- Alerta pelo Telegram se alguma fonte falhar 2 dias seguidos ou se não houver coleta há mais de 48 h.
- O OmniRoute já roda sempre ativo no Termux e está FORA do escopo: não instale, não configure, não inicie nem pare o OmniRoute. Apenas documente no README as variáveis OMNIROUTE_BASE_URL e OMNIROUTE_API_KEY e quais nomes de modelo colocar no config.toml.
- Backup depois de cada relatório: VACUUM INTO + gzip em ~/storage/shared/ideias-backup/, mantendo os 4 últimos.
- Documente no README: instalação no celular, como atualizar (git pull && make build), como ver logs e como restaurar um backup. Inclua a dica de desativar a otimização de bateria do Termux.

Critério de aceite: depois de 3 dias rodando no celular, `ideias stats` mostra coletas diárias sem buracos, ou com buracos recuperados.
```

## F7: Calibração (depois de ~4 semanas de dados)

Não é um prompt de código. É uma sessão de revisão, de preferência nesta conversa de design.

```
Leia docs/design.md e os relatórios em reports/ das últimas 4 semanas.
Quero calibrar o gerador de ideias. Analise:
1. Frases de dor que geram muito falso positivo ou quase nenhum candidato (por fonte e idioma).
2. Se o top 10 tem reclamações genéricas que deveriam ser penalizadas, ou ideias que na prática dependeriam de prospecção manual.
2b. Para as 3 melhores ideias, se o "canal provável" se sustenta: existe volume de busca para a dor, ou um marketplace onde o público procura? Sugira como checar isso rápido e de graça.
3. Se os pesos da rubrica refletem os critérios da seção 1 (micro-SaaS vendável, ~US$ 400–450/mês, global, solo, divulgação automática).
4. Tokens de IA usados contra o limite semanal, quantas semanas caíram no fallback manual e o crescimento do banco contra a meta. Compare a qualidade das semanas feitas via OmniRoute com as feitas no Claude web, se houver.
Proponha mudanças concretas no config.toml e nos prompts/, e o que registrar na seção 12 do design. Não altere código sem eu aprovar.
```

---

## Ideias para depois (fora da v1)
- Novas fontes: Stack Exchange, Lemmy, Mastodon e avaliações de extensões ou plugins.
- `location` do perfil do GitHub para os autores do top 10 (país mais confiável).
- Painel web leve em Vue, lendo o mesmo SQLite.
- Para cada ideia do top 3, uma pesquisa automática de concorrentes e preços.
