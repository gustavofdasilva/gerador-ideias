package ai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"gerador-ideias/internal/httpx"
)

// Client fala com qualquer API compatível com OpenAI (o OmniRoute, no caso).
// Não usa SDK de provedor nenhum: só /models e /chat/completions.
type Client struct {
	HTTP    *httpx.Client
	BaseURL string // ex.: http://localhost:20128/v1
	APIKey  string // opcional
}

// NewClient cria o cliente com o tempo máximo por chamada dado.
func NewClient(baseURL, apiKey string, timeout time.Duration) *Client {
	h := httpx.New(500 * time.Millisecond)
	h.HTTP.Timeout = timeout
	h.MaxRetries = 2
	return &Client{HTTP: h, BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey}
}

func (c *Client) header() http.Header {
	h := http.Header{}
	if c.APIKey != "" {
		h.Set("Authorization", "Bearer "+c.APIKey)
	}
	return h
}

// Models lista os modelos (GET /models). Serve também para checar se o
// OmniRoute está no ar antes de gastar tempo montando o relatório.
func (c *Client) Models(ctx context.Context) ([]string, error) {
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if _, err := c.HTTP.SendJSON(ctx, http.MethodGet, c.BaseURL+"/models", c.header(), nil, &out); err != nil {
		return nil, err
	}
	ids := make([]string, len(out.Data))
	for i, m := range out.Data {
		ids[i] = m.ID
	}
	return ids, nil
}

// Message é uma mensagem do chat.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Usage é o consumo de uma chamada.
type Usage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

// ChatResult é a resposta de uma chamada.
type ChatResult struct {
	Content  string
	Model    string // modelo que respondeu (o OmniRoute pode trocar no fallback)
	Provider string // provedor, se o gateway informar
	Usage    Usage
}

// Chat chama /chat/completions.
func (c *Client) Chat(ctx context.Context, model string, messages []Message, maxTokens int) (*ChatResult, error) {
	body := map[string]any{
		"model":       model,
		"messages":    messages,
		"temperature": 0.2,
		"max_tokens":  maxTokens,
	}
	var out struct {
		Model    string `json:"model"`
		Provider string `json:"provider"`
		Choices  []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage Usage `json:"usage"`
	}
	resp, err := c.HTTP.SendJSON(ctx, http.MethodPost, c.BaseURL+"/chat/completions", c.header(), body, &out)
	if err != nil {
		return nil, err
	}
	if len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		return nil, errors.New("resposta sem conteúdo")
	}
	r := &ChatResult{Content: out.Choices[0].Message.Content, Model: out.Model, Provider: out.Provider, Usage: out.Usage}
	if r.Provider == "" && resp != nil {
		for _, h := range []string{"X-Omniroute-Provider", "X-Provider"} {
			if v := resp.Header.Get(h); v != "" {
				r.Provider = v
				break
			}
		}
	}
	if r.Model == "" {
		r.Model = model
	}
	if out.Choices[0].FinishReason == "length" {
		return r, fmt.Errorf("resposta cortada pelo limite de tokens de saída (%d)", maxTokens)
	}
	return r, nil
}
