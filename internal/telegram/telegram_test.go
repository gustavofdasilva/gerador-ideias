package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const token = "123456:SEGREDO-do-bot"

type fakeBot struct {
	t        *testing.T
	messages []string
	docs     map[string]string // nome -> conteúdo
	captions []string
	fail     int // responde 500 nas próximas N chamadas
	refuse   bool
}

func (f *fakeBot) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/bot"+token+"/") {
		http.Error(w, `{"ok":false,"description":"Unauthorized"}`, 401)
		return
	}
	if f.fail > 0 {
		f.fail--
		http.Error(w, `{"ok":false}`, 500)
		return
	}
	if f.refuse {
		w.Write([]byte(`{"ok":false,"description":"Bad Request: chat not found"}`))
		return
	}
	switch strings.TrimPrefix(r.URL.Path, "/bot"+token+"/") {
	case "sendMessage":
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		if in["chat_id"] != "42" {
			f.t.Errorf("chat_id = %v", in["chat_id"])
		}
		f.messages = append(f.messages, in["text"].(string))
	case "sendDocument":
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			f.t.Fatal(err)
		}
		if r.FormValue("chat_id") != "42" {
			f.t.Errorf("chat_id = %q", r.FormValue("chat_id"))
		}
		file, hdr, err := r.FormFile("document")
		if err != nil {
			f.t.Fatal(err)
		}
		data, _ := io.ReadAll(file)
		f.docs[hdr.Filename] = string(data)
		f.captions = append(f.captions, r.FormValue("caption"))
	}
	w.Write([]byte(`{"ok":true,"result":{}}`))
}

func newClient(t *testing.T, f *fakeBot) *Client {
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := New(token, "42")
	c.BaseURL = srv.URL
	c.HTTP.MinInterval = 0
	c.HTTP.Sleep = func(ctx context.Context, d time.Duration) error { return ctx.Err() }
	return c
}

func TestSendMessageAndDocument(t *testing.T) {
	f := &fakeBot{t: t, docs: map[string]string{}, fail: 1}
	c := newClient(t, f)
	ctx := context.Background()
	if err := c.SendMessage(ctx, "olá"); err != nil {
		t.Fatal(err)
	}
	if err := c.SendDocument(ctx, "2026-48.md", []byte("# Ideias"), "Relatório 2026-48"); err != nil {
		t.Fatal(err)
	}
	if len(f.messages) != 1 || f.messages[0] != "olá" {
		t.Errorf("mensagens = %v", f.messages)
	}
	if f.docs["2026-48.md"] != "# Ideias" || f.captions[0] != "Relatório 2026-48" {
		t.Errorf("documento = %v legenda = %v", f.docs, f.captions)
	}
}

func TestLongMessageIsCut(t *testing.T) {
	f := &fakeBot{t: t, docs: map[string]string{}}
	c := newClient(t, f)
	if err := c.SendMessage(context.Background(), strings.Repeat("á", 5000)); err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(f.messages[0])); n != MaxMessage {
		t.Errorf("mensagem com %d caracteres", n)
	}
}

func TestErrorsNeverContainToken(t *testing.T) {
	f := &fakeBot{t: t, docs: map[string]string{}, refuse: true}
	c := newClient(t, f)
	err := c.SendMessage(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("err = %v", err)
	}

	// Erro de rede: a URL (com o token) aparece na mensagem do Go.
	c.BaseURL = "http://127.0.0.1:1"
	c.HTTP.MaxRetries = 0
	for _, err := range []error{
		c.SendMessage(context.Background(), "x"),
		c.SendDocument(context.Background(), "a.md", []byte("x"), ""),
	} {
		if err == nil {
			t.Fatal("esperava erro de rede")
		}
		if strings.Contains(err.Error(), "SEGREDO") {
			t.Errorf("token vazou no erro: %v", err)
		}
	}
}
