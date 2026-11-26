// Package telegram envia mensagens e arquivos pela Bot API do Telegram.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"gerador-ideias/internal/httpx"
)

// DefaultBaseURL é a Bot API.
const DefaultBaseURL = "https://api.telegram.org"

// MaxMessage é o limite de caracteres de uma mensagem.
const MaxMessage = 4096

// Client envia para um único chat.
type Client struct {
	HTTP    *httpx.Client
	BaseURL string
	Token   string
	ChatID  string
}

// New cria o cliente com os padrões de produção.
func New(token, chatID string) *Client {
	c := httpx.New(time.Second) // a Bot API aceita ~1 mensagem/s por chat
	c.MaxRetries = 3
	return &Client{HTTP: c, BaseURL: DefaultBaseURL, Token: token, ChatID: chatID}
}

type apiResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
}

// SendMessage envia texto puro (sem formatação, para não depender de escapes).
func (c *Client) SendMessage(ctx context.Context, text string) error {
	if r := []rune(text); len(r) > MaxMessage {
		text = string(r[:MaxMessage-1]) + "…"
	}
	body := map[string]any{"chat_id": c.ChatID, "text": text, "disable_web_page_preview": true}
	var out apiResponse
	_, err := c.HTTP.SendJSON(ctx, http.MethodPost, c.url("sendMessage"), nil, body, &out)
	return c.check("sendMessage", out, err)
}

// SendDocument envia um arquivo com legenda.
func (c *Client) SendDocument(ctx context.Context, filename string, data []byte, caption string) error {
	_, body, err := c.HTTP.Do(ctx, func(ctx context.Context) (*http.Request, error) {
		// O corpo é montado de novo a cada tentativa.
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("chat_id", c.ChatID)
		if caption != "" {
			_ = mw.WriteField("caption", caption)
		}
		fw, err := mw.CreateFormFile("document", filename)
		if err != nil {
			return nil, err
		}
		if _, err := fw.Write(data); err != nil {
			return nil, err
		}
		if err := mw.Close(); err != nil {
			return nil, err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url("sendDocument"), &buf)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", mw.FormDataContentType())
		return req, nil
	})
	var out apiResponse
	if err == nil {
		if jerr := json.Unmarshal(body, &out); jerr != nil {
			err = fmt.Errorf("resposta inválida: %w", jerr)
		}
	}
	return c.check("sendDocument", out, err)
}

func (c *Client) url(method string) string {
	return strings.TrimRight(c.BaseURL, "/") + "/bot" + c.Token + "/" + method
}

// check transforma a resposta num erro, sempre sem o token: ele vai na URL e
// apareceria em mensagens de erro de rede e de status.
func (c *Client) check(method string, out apiResponse, err error) error {
	if err == nil && !out.OK {
		err = fmt.Errorf("Telegram recusou: %s", out.Description)
	}
	if err == nil {
		return nil
	}
	msg := err.Error()
	if c.Token != "" {
		msg = strings.ReplaceAll(msg, c.Token, "<token>")
	}
	return errors.New("telegram " + method + ": " + msg)
}
