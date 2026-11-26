# ideias

Coletor leve que roda no celular (Termux), lê conversas públicas do Hacker News, do Bluesky e de issues do GitHub em busca de dores reais e gera um relatório semanal com as dores ranqueadas por viabilidade como micro-SaaS.

O design completo fica em [docs/design.md](docs/design.md), e as fases de implementação em [docs/roadmap.md](docs/roadmap.md).

> Estado atual: **F5**. Todos os comandos funcionam: `collect` (Hacker News, Bluesky e GitHub Issues), `report` com IA via OmniRoute (ou `--no-ai`), `export`/`import` para o Claude web, `stats` e `migrate`. Falta a operação no Termux (F6).

## Compilar no PC

Requer Go 1.26 ou mais novo. Se a sua versão for mais antiga, o `go` baixa o toolchain certo sozinho. O código é Go puro (`CGO_ENABLED=0`), então não é preciso ter um compilador C.

```sh
make build          # bin/ideias (bin/ideias.exe no Windows)
make test           # go test ./...
make build-android  # bin/ideias-android-arm64, para copiar para o celular
```

Sem `make`:

```sh
CGO_ENABLED=0 go build -o bin/ideias ./cmd/ideias
CGO_ENABLED=0 GOOS=android GOARCH=arm64 go build -o bin/ideias-android-arm64 ./cmd/ideias
```

## Compilar no Termux

```sh
pkg install golang git make
git clone <url-do-repositório> gerador-ideias
cd gerador-ideias
make build
```

Para atualizar depois: `git pull && make build`.

## Configurar

```sh
mkdir -p ~/.config/ideias
cp config.example.toml ~/.config/ideias/config.toml   # frases de dor, marcas, pesos, modelos
cp env.example ~/.config/ideias/env                   # tokens e senhas
chmod 600 ~/.config/ideias/env
```

- O `config.toml` é procurado primeiro na pasta atual (`./config.toml`) e depois em `~/.config/ideias/config.toml`. Se nenhum existir, valem os padrões do `config.example.toml`, que vem embutido no binário. Para usar outro arquivo, passe `--config arquivo`.
- Os segredos vêm de variáveis de ambiente ou de `~/.config/ideias/env`. Se a mesma variável estiver nos dois lugares, vale a do ambiente. Nunca coloque segredos no `config.toml`.

## Usar

```sh
bin/ideias migrate   # cria ou atualiza o banco (padrão ~/.local/share/ideias/ideias.db)
bin/ideias collect   # coleta desde a última execução (o collect também aplica migrações pendentes)
bin/ideias stats     # tamanho do banco, linhas por tabela, últimas coletas, tokens de IA
bin/ideias help      # todos os comandos
```

Todos os comandos aceitam `--db arquivo` para usar outro banco.

## Como a coleta retoma

Cada fonte tem um cursor: o fim da última janela coletada **sem erros**. A próxima coleta busca do cursor (menos `overlap_minutes`) até agora, por maior que seja o intervalo, então dias com o celular desligado são recuperados. Se alguma busca falhar, o cursor não avança e a janela inteira é repetida na próxima vez; os itens já gravados são ignorados pela chave `(source, external_id)`. Sem cursor, a coleta volta `initial_lookback_days` dias.

Cada execução fica registrada na tabela `runs`, e a última de cada fonte aparece no `ideias stats`. Em `collect --source all`, a falha de uma fonte não impede as outras; o comando sai com código 1 e o erro fica em `runs`.

## O que a coleta guarda

Cada item coletado passa pelo processamento local, sem IA: idioma, frases de dor, marcas, palavras-chave e país estimado.

- **Candidatos** (frases de dor somando pelo menos `pain.min_score`) vão para a tabela `items`, com o texto cortado em 2.000 caracteres, `pain_score`, sinais (`request`, `payment`, `distribution`...), marcas, idioma e país estimado.
- **Não-candidatos** não guardam texto: só somam nas contagens diárias (`daily_items`, `daily_terms`, `daily_brands`).
- O país é sempre uma **estimativa**, feita a partir de moeda, TLD de links, países e cidades citados e idioma, com `desconhecido` como padrão.

Para calibrar, edite no `config.toml` as frases (`[pain.phrases.*]`), as exclusões de falsos positivos (`[pain.exclude]`), os tipos que exigem marca (`pain.brand_required`), os padrões de marca (`[brands.patterns]`) e `[keywords]`, que controla o tamanho do banco. O `ideias stats` mostra a taxa de candidatos por fonte e idioma e o crescimento estimado do banco por mês.

## Relatório semanal

```sh
bin/ideias report --no-ai                     # semana corrente → reports/AAAA-WW.md + Telegram
bin/ideias report --no-ai --week 2026-47      # regera uma semana passada
bin/ideias report --no-ai --no-telegram       # só o arquivo, sem enviar
```

O relatório traz:
- os números da coleta da semana;
- os candidatos com maior `pain_score`, com link;
- as palavras-chave em alta, contra a média das 4 semanas anteriores;
- as marcas mais citadas;
- os recortes por idioma e por país estimado;
- a saúde da coleta: dias sem coleta, execuções com erro e tamanho do banco.

O tamanho das listas fica em `[report]` no `config.toml`.

**Telegram:** defina `TELEGRAM_BOT_TOKEN` (do @BotFather) e `TELEGRAM_CHAT_ID`. O comando envia um resumo e o `.md` como documento.
- Sem essas variáveis, só gera o arquivo e avisa.
- Se o envio falhar, o arquivo continua gerado e o comando sai com código 1.

## IA: OmniRoute (automático) ou Claude web (manual)

O `report` sem `--no-ai` agrupa os candidatos em dores e pontua as ~20 mais relevantes com a rubrica do design (seção 6). O resultado sai na seção "Top 10 dores". Os dois modos usam os mesmos prompts de `prompts/` e o mesmo formato de resposta, então o relatório final é igual nos dois. Só muda a linha "IA:" do cabeçalho.

**Automático, via OmniRoute.** O OmniRoute já roda no Termux e é infraestrutura externa: o `ideias` só consome a API dele.
- `OMNIROUTE_BASE_URL`: o padrão é `http://localhost:20128/v1`.
- `OMNIROUTE_API_KEY`: só se o seu OmniRoute exigir chave.
- `[ai] group_model` e `score_model` no `config.toml`: nomes de modelo ou de combo configurados no OmniRoute.
- `[ai] weekly_token_limit`: limite de tokens por semana. Antes de cada chamada, o custo é estimado. O uso fica em `ai_usage` e aparece no `stats`.

**Fallback automático.** Se o OmniRoute estiver fora do ar, se as chamadas falharem ou se o limite de tokens não comportar a semana, nada da IA é gravado. Nesse caso o comando:
1. gera o relatório sem IA;
2. gera o pacote do modo manual;
3. manda um aviso pelo Telegram.

**Manual, pelo claude.ai** (sem API nenhuma):

```sh
bin/ideias export --week 2026-48          # export/2026-48/prompt.md + candidatos.md
# no claude.ai: cole o prompt.md, anexe o(s) candidatos*.md, salve a resposta em resposta.json
bin/ideias import --week 2026-48 resposta.json
```

O `import` aceita a resposta colada do jeito que vier, com cerca ```json e texto em volta. Se algo estiver errado, ele lista cada problema apontando o campo, por exemplo `atribuicoes[3].cluster "c99" não existe`. Dá para colar esses erros de volta no claude.ai e pedir a correção.

Regerar uma semana (`report` ou `import` de novo) refaz o agrupamento daquela semana, sem duplicar clusters.

## Credenciais das fontes

- **Hacker News:** nenhuma.
- **Bluesky:** `BSKY_HANDLE` e `BSKY_APP_PASSWORD`. Crie um app password em Settings › Privacy and security › App passwords (nunca use a senha principal). A busca do Bluesky exige login.
- **GitHub:** `GITHUB_TOKEN`. Um token fine-grained sem nenhuma permissão extra basta para buscar issues públicas.

Para testar uma fonte isolada: `bin/ideias collect --source bsky` (ou `gh`, `hn`).
