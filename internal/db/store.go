package db

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gerador-ideias/internal/week"
)

// MaxTextRunes é o tamanho máximo de texto guardado por item (design, 5.1 passo 7).
const MaxTextRunes = 2000

// Item é o formato normalizado de um post (design, 5.1 passo 2).
type Item struct {
	Source     string
	ExternalID string
	URL        string
	CreatedAt  time.Time
	Text       string
	AuthorHash string
	LangHint   string
	Engagement int
	Brands     []string // marcas já conhecidas pela fonte (ex.: owner/repo no GitHub)
}

// InsertItems grava os itens numa transação, ignorando os que já existem
// (mesmo source e external_id). Devolve quantos eram novos.
func InsertItems(ctx context.Context, db *sql.DB, items []Item, collectedAt time.Time) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // sem efeito depois do Commit
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO items
		(source, external_id, url, created_at, week, collected_at, text, author_hash, lang_hint, engagement, brands)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (source, external_id) DO NOTHING`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	inserted := 0
	for _, it := range items {
		res, err := stmt.ExecContext(ctx, it.Source, it.ExternalID, it.URL,
			it.CreatedAt.Unix(), week.Of(it.CreatedAt), collectedAt.Unix(),
			Truncate(it.Text, MaxTextRunes), it.AuthorHash, it.LangHint, it.Engagement,
			strings.Join(it.Brands, ","))
		if err != nil {
			return 0, fmt.Errorf("item %s/%s: %w", it.Source, it.ExternalID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		inserted += int(n)
	}
	return inserted, tx.Commit()
}

// Truncate corta s em no máximo n runas, sem quebrar caracteres UTF-8.
func Truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}

// GetCursor devolve o cursor de uma fonte como instante. ok=false se não houver.
func GetCursor(ctx context.Context, db *sql.DB, source string) (t time.Time, ok bool, err error) {
	var v string
	err = db.QueryRowContext(ctx, `SELECT value FROM cursors WHERE source = ?`, source).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	sec, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("cursor de %s inválido (%q): %w", source, v, err)
	}
	return time.Unix(sec, 0).UTC(), true, nil
}

// SetCursor grava o cursor (segundos Unix) de uma fonte.
func SetCursor(ctx context.Context, db *sql.DB, source string, t, now time.Time) error {
	_, err := db.ExecContext(ctx, `INSERT INTO cursors (source, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (source) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		source, strconv.FormatInt(t.Unix(), 10), now.Unix())
	return err
}

// DeleteCursor apaga o cursor de uma fonte.
func DeleteCursor(ctx context.Context, db *sql.DB, source string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM cursors WHERE source = ?`, source)
	return err
}

// Run é uma linha de runs.
type Run struct {
	ID        int64
	Command   string
	Source    string
	StartedAt time.Time
	Status    string // running, ok, partial, error
	ItemsSeen int
	ItemsNew  int
	Errors    int
	ErrorText string
}

// StartRun registra o início de uma execução.
func StartRun(ctx context.Context, db *sql.DB, command, source string, now time.Time) (*Run, error) {
	res, err := db.ExecContext(ctx,
		`INSERT INTO runs (command, source, started_at, status) VALUES (?, ?, ?, 'running')`,
		command, source, now.Unix())
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &Run{ID: id, Command: command, Source: source, StartedAt: now, Status: "running"}, nil
}

// FinishRun grava o resultado de uma execução.
func FinishRun(ctx context.Context, db *sql.DB, r *Run, now time.Time) error {
	_, err := db.ExecContext(ctx, `UPDATE runs SET finished_at = ?, status = ?, items_seen = ?,
		items_new = ?, errors = ?, error_text = ? WHERE id = ?`,
		now.Unix(), r.Status, r.ItemsSeen, r.ItemsNew, r.Errors, Truncate(r.ErrorText, 2000), r.ID)
	return err
}

const authorKeyName = "author_hash_key"

// AuthorHasher devolve uma função que transforma (fonte, autor) num hash
// HMAC-SHA256 truncado (16 hex). A chave é gerada uma vez e fica em meta.
func AuthorHasher(ctx context.Context, db *sql.DB) (func(source, author string) string, error) {
	key, err := authorKey(ctx, db)
	if err != nil {
		return nil, err
	}
	return func(source, author string) string {
		if author == "" {
			return ""
		}
		m := hmac.New(sha256.New, key)
		m.Write([]byte(source))
		m.Write([]byte{0})
		m.Write([]byte(author))
		return hex.EncodeToString(m.Sum(nil)[:8])
	}, nil
}

func authorKey(ctx context.Context, db *sql.DB) ([]byte, error) {
	fresh := make([]byte, 32)
	if _, err := rand.Read(fresh); err != nil {
		return nil, err
	}
	// INSERT OR IGNORE: se dois processos correrem juntos, vale a primeira chave.
	if _, err := db.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT (key) DO NOTHING`,
		authorKeyName, hex.EncodeToString(fresh)); err != nil {
		return nil, err
	}
	var v string
	if err := db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, authorKeyName).Scan(&v); err != nil {
		return nil, err
	}
	return hex.DecodeString(v)
}
