package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock avança só quando o cliente "dorme".
type fakeClock struct {
	now    time.Time
	sleeps []time.Duration
}

func newTestClient(minInterval time.Duration) (*Client, *fakeClock) {
	clk := &fakeClock{now: time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)}
	c := New(minInterval)
	c.Now = func() time.Time { return clk.now }
	c.Sleep = func(ctx context.Context, d time.Duration) error {
		clk.sleeps = append(clk.sleeps, d)
		clk.now = clk.now.Add(d)
		return ctx.Err()
	}
	return c, clk
}

func TestRetriesOn5xxAnd429(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); ua != UserAgent {
			t.Errorf("User-Agent = %q", ua)
		}
		switch calls.Add(1) {
		case 1:
			w.WriteHeader(http.StatusBadGateway)
		case 2:
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			w.Write([]byte(`{"ok": true}`))
		}
	}))
	defer srv.Close()

	c, clk := newTestClient(0)
	var out struct{ OK bool }
	if _, err := c.GetJSON(context.Background(), srv.URL, &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || calls.Load() != 3 {
		t.Fatalf("ok=%v chamadas=%d", out.OK, calls.Load())
	}
	if len(clk.sleeps) != 2 {
		t.Fatalf("esperas = %v", clk.sleeps)
	}
	if d := clk.sleeps[0]; d < 2*time.Second || d > 2500*time.Millisecond {
		t.Errorf("1º backoff = %v, esperava ~2 s", d)
	}
	if d := clk.sleeps[1]; d != 7*time.Second {
		t.Errorf("Retry-After ignorado: esperou %v", d)
	}
}

func TestNoRetryOn4xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	c, _ := newTestClient(0)
	_, err := c.GetJSON(context.Background(), srv.URL, &struct{}{})
	var se *StatusError
	if !errors.As(err, &se) || se.Status != 404 {
		t.Fatalf("err = %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("chamadas = %d", calls.Load())
	}
}

func TestGivesUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c, clk := newTestClient(0)
	c.MaxRetries = 3
	_, err := c.GetJSON(context.Background(), srv.URL, &struct{}{})
	var se *StatusError
	if !errors.As(err, &se) || se.Status != 503 {
		t.Fatalf("err = %v", err)
	}
	if len(clk.sleeps) != 3 {
		t.Fatalf("esperas = %v", clk.sleeps)
	}
	for i := 1; i < len(clk.sleeps); i++ {
		if clk.sleeps[i] < clk.sleeps[i-1] {
			t.Errorf("backoff não cresce: %v", clk.sleeps)
		}
	}
}

func TestMinInterval(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c, clk := newTestClient(500 * time.Millisecond)
	for i := 0; i < 3; i++ {
		if _, err := c.GetJSON(context.Background(), srv.URL, &struct{}{}); err != nil {
			t.Fatal(err)
		}
	}
	// 1ª requisição sai na hora; as outras esperam o intervalo.
	if len(clk.sleeps) != 2 || clk.sleeps[0] != 500*time.Millisecond || clk.sleeps[1] != 500*time.Millisecond {
		t.Errorf("esperas = %v", clk.sleeps)
	}
}

func TestContextCancelStopsRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c, _ := newTestClient(0)
	ctx, cancel := context.WithCancel(context.Background())
	c.Sleep = func(ctx context.Context, d time.Duration) error {
		cancel()
		return ctx.Err()
	}
	_, err := c.GetJSON(ctx, srv.URL, &struct{}{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	if d := parseRetryAfter("120", now); d != 2*time.Minute {
		t.Errorf("segundos: %v", d)
	}
	if d := parseRetryAfter(now.Add(30*time.Second).Format(http.TimeFormat), now); d != 30*time.Second {
		t.Errorf("data: %v", d)
	}
	if d := parseRetryAfter("lixo", now); d != 0 {
		t.Errorf("inválido: %v", d)
	}
}

func TestGitHub403RateLimitWaitsForReset(t *testing.T) {
	c, clk := newTestClient(0)
	reset := clk.now.Add(40 * time.Second)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
			http.Error(w, `{"message":"API rate limit exceeded"}`, http.StatusForbidden)
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	if _, err := c.GetJSON(context.Background(), srv.URL, &struct{}{}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("chamadas = %d", calls.Load())
	}
	var total time.Duration
	for _, d := range clk.sleeps {
		total += d
	}
	if total < 40*time.Second || total > 45*time.Second {
		t.Errorf("esperou %v (%v), esperava ~41 s até o reset", total, clk.sleeps)
	}
}

func TestExhaustedQuotaDelaysNextRequest(t *testing.T) {
	c, clk := newTestClient(0)
	reset := clk.now.Add(90 * time.Second)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			// Formato do Bluesky: sem o prefixo X-.
			w.Header().Set("RateLimit-Remaining", "0")
			w.Header().Set("RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		}
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	for i := 0; i < 2; i++ {
		if _, err := c.GetJSON(context.Background(), srv.URL, &struct{}{}); err != nil {
			t.Fatal(err)
		}
	}
	if len(clk.sleeps) != 1 || clk.sleeps[0] != 90*time.Second {
		t.Errorf("esperas = %v, esperava 90 s antes da 2ª requisição", clk.sleeps)
	}
}

func TestRateWaitTooLongFails(t *testing.T) {
	c, clk := newTestClient(0)
	c.MaxRateWait = time.Minute
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(clk.now.Add(time.Hour).Unix(), 10))
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	_, err := c.GetJSON(context.Background(), srv.URL, &struct{}{})
	if err == nil {
		t.Fatal("esperava erro por espera longa demais")
	}
	if len(clk.sleeps) != 0 {
		t.Errorf("não deveria ter dormido: %v", clk.sleeps)
	}
}

func TestSendJSONHeadersAndBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer x" ||
			r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("requisição: %s %v", r.Method, r.Header)
		}
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		w.Write([]byte(`{"echo":"` + in["a"] + `"}`))
	}))
	defer srv.Close()
	c, _ := newTestClient(0)
	var out struct{ Echo string }
	h := http.Header{"Authorization": {"Bearer x"}}
	if _, err := c.SendJSON(context.Background(), http.MethodPost, srv.URL, h, map[string]string{"a": "oi"}, &out); err != nil {
		t.Fatal(err)
	}
	if out.Echo != "oi" {
		t.Errorf("echo = %q", out.Echo)
	}
}
