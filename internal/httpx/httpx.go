// Package httpx é o cliente HTTP comum dos coletores: timeout, User-Agent
// genérico, intervalo mínimo entre requisições, backoff exponencial e respeito
// aos cabeçalhos de limite (X-RateLimit-* do GitHub, RateLimit-* do Bluesky).
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// UserAgent é genérico de propósito: sem nome, e-mail ou URL do dono (design, seção 3).
const UserAgent = "ideias-collector/0.1"

// Client faz requisições educadas. O zero value não serve: use New.
type Client struct {
	HTTP        *http.Client
	UserAgent   string
	MinInterval time.Duration // intervalo mínimo entre o início de duas requisições
	MaxRetries  int           // novas tentativas depois da primeira
	BaseBackoff time.Duration // espera antes da 1ª nova tentativa; dobra a cada uma
	MaxBackoff  time.Duration // teto de cada espera de nova tentativa
	// MaxRateWait é o máximo que o cliente aceita esperar a cota da API voltar
	// (cabeçalhos de limite ou Retry-After). Acima disso, desiste com erro.
	MaxRateWait time.Duration

	// Now e Sleep existem para os testes não esperarem de verdade.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error

	mu        sync.Mutex
	last      time.Time
	notBefore time.Time // a API pediu para esperar até aqui (cota esgotada)
}

// New devolve um cliente com padrões razoáveis para APIs públicas.
func New(minInterval time.Duration) *Client {
	return &Client{
		HTTP:        &http.Client{Timeout: 30 * time.Second},
		UserAgent:   UserAgent,
		MinInterval: minInterval,
		MaxRetries:  4,
		BaseBackoff: 2 * time.Second,
		MaxBackoff:  5 * time.Minute,
		MaxRateWait: 15 * time.Minute,
		Now:         time.Now,
		Sleep:       SleepCtx,
	}
}

// SleepCtx espera d ou até o contexto ser cancelado.
func SleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// StatusError é uma resposta HTTP que não deu certo, mesmo depois das tentativas.
type StatusError struct {
	URL    string
	Status int
	Body   string // trecho do corpo, para diagnóstico
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("HTTP %d em %s: %s", e.Status, e.URL, e.Body)
}

// Do envia a requisição montada por build (chamada de novo a cada tentativa)
// e devolve a resposta (com o corpo já lido e fechado) e o corpo.
// Tenta de novo em erro de rede, 429 e 5xx. Outros status viram *StatusError.
func (c *Client) Do(ctx context.Context, build func(context.Context) (*http.Request, error)) (*http.Response, []byte, error) {
	var lastErr error
	for attempt := 0; attempt <= c.MaxRetries; attempt++ {
		if attempt > 0 {
			wait := c.backoff(attempt)
			var se *retryAfterError
			if errors.As(lastErr, &se) && se.wait > 0 {
				if se.wait > c.MaxRateWait {
					return nil, nil, fmt.Errorf("a API pediu %v de espera (máximo aceito: %v): %w",
						se.wait.Round(time.Second), c.MaxRateWait, &se.StatusError)
				}
				wait = se.wait
			}
			if err := c.Sleep(ctx, wait); err != nil {
				return nil, nil, err
			}
		}
		if err := c.throttle(ctx); err != nil {
			return nil, nil, err
		}
		req, err := build(ctx)
		if err != nil {
			return nil, nil, err
		}
		req.Header.Set("User-Agent", c.UserAgent)
		resp, err := c.HTTP.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			lastErr = err
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		exhausted, reset := rateLimitState(resp.Header)
		if exhausted {
			c.mu.Lock()
			if reset.After(c.notBefore) {
				c.notBefore = reset
			}
			c.mu.Unlock()
		}
		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			return resp, body, nil
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 ||
			// O GitHub responde 403 (e não 429) quando a cota acaba.
			(resp.StatusCode == http.StatusForbidden && (exhausted || resp.Header.Get("Retry-After") != "")):
			wait := parseRetryAfter(resp.Header.Get("Retry-After"), c.Now())
			if exhausted && wait == 0 {
				wait = reset.Sub(c.Now()) + time.Second
				c.mu.Lock()
				c.notBefore = time.Time{} // a espera já vai acontecer aqui
				c.mu.Unlock()
			}
			lastErr = &retryAfterError{
				StatusError: StatusError{URL: req.URL.Redacted(), Status: resp.StatusCode, Body: snippet(body)},
				wait:        wait,
			}
		default:
			return resp, body, &StatusError{URL: req.URL.Redacted(), Status: resp.StatusCode, Body: snippet(body)}
		}
	}
	var se *retryAfterError
	if errors.As(lastErr, &se) {
		return nil, nil, fmt.Errorf("desisti depois de %d tentativas: %w", c.MaxRetries+1, &se.StatusError)
	}
	return nil, nil, fmt.Errorf("desisti depois de %d tentativas: %w", c.MaxRetries+1, lastErr)
}

// GetJSON faz um GET e decodifica a resposta JSON em out.
func (c *Client) GetJSON(ctx context.Context, url string, out any) (*http.Response, error) {
	return c.SendJSON(ctx, http.MethodGet, url, nil, nil, out)
}

// SendJSON envia uma requisição com cabeçalhos extras e, se in não for nil,
// um corpo JSON; decodifica a resposta em out (se out não for nil).
func (c *Client) SendJSON(ctx context.Context, method, url string, header http.Header, in, out any) (*http.Response, error) {
	var payload []byte
	if in != nil {
		var err error
		if payload, err = json.Marshal(in); err != nil {
			return nil, err
		}
	}
	resp, body, err := c.Do(ctx, func(ctx context.Context) (*http.Request, error) {
		var rd io.Reader
		if payload != nil {
			rd = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, rd)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, vs := range header {
			for _, v := range vs {
				req.Header.Add(k, v)
			}
		}
		return req, nil
	})
	if err != nil {
		return resp, err
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return resp, fmt.Errorf("JSON inválido de %s: %w", url, err)
		}
	}
	return resp, nil
}

type retryAfterError struct {
	StatusError
	wait time.Duration
}

func (c *Client) throttle(ctx context.Context) error {
	c.mu.Lock()
	now := c.Now()
	next := c.last.Add(c.MinInterval)
	if c.notBefore.After(next) {
		next = c.notBefore
	}
	wait := next.Sub(now)
	if wait > c.MaxRateWait {
		c.mu.Unlock()
		return fmt.Errorf("cota da API esgotada até %s (espera maior que %v)", next.Format(time.RFC3339), c.MaxRateWait)
	}
	if wait > 0 {
		c.last = next
	} else {
		c.last = now
	}
	c.notBefore = time.Time{}
	c.mu.Unlock()
	if wait > 0 {
		return c.Sleep(ctx, wait)
	}
	return nil
}

// rateLimitState lê os cabeçalhos de cota. exhausted é true quando restam 0
// requisições; reset é quando a cota volta (segundos Unix nos dois formatos).
func rateLimitState(h http.Header) (exhausted bool, reset time.Time) {
	for _, prefix := range []string{"X-Ratelimit-", "Ratelimit-"} {
		rem := h.Get(prefix + "Remaining")
		if rem == "" {
			continue
		}
		n, err := strconv.Atoi(rem)
		if err != nil || n > 0 {
			return false, time.Time{}
		}
		if sec, err := strconv.ParseInt(h.Get(prefix+"Reset"), 10, 64); err == nil {
			return true, time.Unix(sec, 0)
		}
		return true, time.Time{}
	}
	return false, time.Time{}
}

// backoff: BaseBackoff × 2^(attempt-1), com até 25% de variação aleatória, limitado a MaxBackoff.
func (c *Client) backoff(attempt int) time.Duration {
	d := c.BaseBackoff << (attempt - 1)
	if d <= 0 || d > c.MaxBackoff {
		d = c.MaxBackoff
	}
	jitter := time.Duration(rand.Int64N(int64(d)/4 + 1))
	return min(d+jitter, c.MaxBackoff)
}

func parseRetryAfter(v string, now time.Time) time.Duration {
	if v == "" {
		return 0
	}
	if s, err := strconv.Atoi(v); err == nil && s >= 0 {
		return time.Duration(s) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return t.Sub(now)
	}
	return 0
}

func snippet(b []byte) string {
	const max = 300
	if len(b) > max {
		return string(b[:max]) + "…"
	}
	return string(b)
}
