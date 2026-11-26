// Comando ideias: coletor de dores e gerador de relatório semanal de ideias.
package main

import (
	"os"

	"gerador-ideias/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
