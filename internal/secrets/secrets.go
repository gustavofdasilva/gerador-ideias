// Package secrets lê tokens e senhas das variáveis de ambiente ou de
// ~/.config/ideias/env. Nada disso é versionado nem vai para o config.toml.
package secrets

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Nomes das variáveis reconhecidas.
const (
	BskyHandle       = "BSKY_HANDLE"
	BskyAppPassword  = "BSKY_APP_PASSWORD"
	GithubToken      = "GITHUB_TOKEN"
	OmnirouteBaseURL = "OMNIROUTE_BASE_URL"
	OmnirouteAPIKey  = "OMNIROUTE_API_KEY"
	TelegramBotToken = "TELEGRAM_BOT_TOKEN"
	TelegramChatID   = "TELEGRAM_CHAT_ID"
)

// Keys lista as variáveis conhecidas, na ordem em que o stats mostra.
var Keys = []string{
	BskyHandle, BskyAppPassword, GithubToken,
	OmnirouteBaseURL, OmnirouteAPIKey,
	TelegramBotToken, TelegramChatID,
}

// DefaultOmnirouteBaseURL é usado quando OMNIROUTE_BASE_URL não está definido.
const DefaultOmnirouteBaseURL = "http://localhost:20128/v1"

// Secrets guarda os valores resolvidos. Valor vazio significa "não definido".
type Secrets struct {
	values map[string]string
	// File é o arquivo env lido, ou "" se não existia.
	File string
}

// Get devolve o valor de uma variável ("" se não definida).
func (s *Secrets) Get(key string) string { return s.values[key] }

// Has diz se a variável está definida.
func (s *Secrets) Has(key string) bool { return s.values[key] != "" }

// OmnirouteURL devolve a URL do OmniRoute, com o padrão quando ausente.
func (s *Secrets) OmnirouteURL() string {
	if v := s.Get(OmnirouteBaseURL); v != "" {
		return v
	}
	return DefaultOmnirouteBaseURL
}

// DefaultFile é ~/.config/ideias/env.
func DefaultFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "ideias", "env")
}

// Load lê o arquivo env (se existir) e sobrepõe as variáveis de ambiente,
// que sempre vencem.
func Load(file string) (*Secrets, error) {
	s := &Secrets{values: map[string]string{}}
	if file != "" {
		f, err := os.Open(file)
		switch {
		case err == nil:
			defer f.Close()
			vals, err := parseEnv(f)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", file, err)
			}
			for _, k := range Keys {
				if v, ok := vals[k]; ok {
					s.values[k] = v
				}
			}
			s.File = file
		case !errors.Is(err, os.ErrNotExist):
			return nil, err
		}
	}
	for _, k := range Keys {
		if v := os.Getenv(k); v != "" {
			s.values[k] = v
		}
	}
	return s, nil
}

// parseEnv lê linhas CHAVE=valor, aceitando comentários (#), linhas vazias,
// o prefixo "export " e valores entre aspas simples ou duplas.
func parseEnv(r io.Reader) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("linha %d: esperava CHAVE=valor", n)
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
			val = val[1 : len(val)-1]
		} else if i := strings.Index(val, " #"); i >= 0 {
			val = strings.TrimSpace(val[:i])
		}
		out[key] = val
	}
	return out, sc.Err()
}
