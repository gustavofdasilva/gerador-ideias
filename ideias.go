// Package ideias guarda os arquivos da raiz do repositório que vão embutidos no binário.
package ideias

import (
	"embed"
)

// ExampleConfig é o config.example.toml. Serve de documentação e de configuração
// padrão quando nenhum config.toml é encontrado.
//
//go:embed config.example.toml
var ExampleConfig []byte

// Prompts são os prompts de IA de prompts/ (design 5.5), compartilhados pelo
// modo automático (OmniRoute) e pelo modo manual (Claude web).
//
//go:embed prompts/*.md
var Prompts embed.FS
