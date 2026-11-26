package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func clearEnv(t *testing.T) {
	for _, k := range Keys {
		t.Setenv(k, "")
	}
}

func TestParseEnv(t *testing.T) {
	src := `
# comentário
BSKY_HANDLE=fulano.bsky.social
export GITHUB_TOKEN="ghp_abc # não é comentário"
TELEGRAM_CHAT_ID='123'
OMNIROUTE_API_KEY=sk-1 # comentário no fim
`
	got, err := parseEnv(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"BSKY_HANDLE":       "fulano.bsky.social",
		"GITHUB_TOKEN":      "ghp_abc # não é comentário",
		"TELEGRAM_CHAT_ID":  "123",
		"OMNIROUTE_API_KEY": "sk-1",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if _, err := parseEnv(strings.NewReader("SEM_IGUAL")); err == nil {
		t.Error("linha sem = deveria falhar")
	}
}

func TestLoadEnvOverridesFile(t *testing.T) {
	clearEnv(t)
	file := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(file, []byte("GITHUB_TOKEN=do-arquivo\nBSKY_HANDLE=h\nOUTRA=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(GithubToken, "do-ambiente")

	s, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Get(GithubToken); got != "do-ambiente" {
		t.Errorf("GITHUB_TOKEN = %q", got)
	}
	if !s.Has(BskyHandle) || s.Has(TelegramBotToken) {
		t.Error("Has() inconsistente")
	}
	if s.Get("OUTRA") != "" {
		t.Error("variáveis desconhecidas não devem ser carregadas")
	}
	if s.File != file {
		t.Errorf("File = %q", s.File)
	}
}

func TestLoadMissingFileAndDefaults(t *testing.T) {
	clearEnv(t)
	s, err := Load(filepath.Join(t.TempDir(), "nao-existe"))
	if err != nil {
		t.Fatal(err)
	}
	if s.File != "" {
		t.Errorf("File = %q", s.File)
	}
	if s.OmnirouteURL() != DefaultOmnirouteBaseURL {
		t.Errorf("OmnirouteURL = %q", s.OmnirouteURL())
	}
}
