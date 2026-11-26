# ideias

Coletor leve que roda no celular (Termux), lê conversas públicas do Hacker News, do Bluesky e de issues do GitHub em busca de dores reais e gera um relatório semanal com as dores ranqueadas por viabilidade como micro-SaaS.

O design completo fica em [docs/design.md](docs/design.md), e as fases de implementação em [docs/roadmap.md](docs/roadmap.md).

> Estado atual: **F0 (esqueleto)**. `migrate` e `stats` funcionam; `collect`, `report`, `export` e `import` ainda respondem "não implementado".

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
bin/ideias stats     # tamanho do banco, linhas por tabela, últimas coletas, tokens de IA
bin/ideias help      # todos os comandos
```

Todos os comandos aceitam `--db arquivo` para usar outro banco.
