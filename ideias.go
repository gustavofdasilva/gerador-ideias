// Package ideias guarda os arquivos da raiz do repositório que vão embutidos no binário.
package ideias

import _ "embed"

// ExampleConfig é o config.example.toml. Serve de documentação e de configuração
// padrão quando nenhum config.toml é encontrado.
//
//go:embed config.example.toml
var ExampleConfig []byte
