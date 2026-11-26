package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"gerador-ideias/internal/week"
)

// Candidate é um item que passou no filtro, com o resultado do processamento.
type Candidate struct {
	Item
	Lang      string
	Country   string
	PainScore float64
	Signals   []string
}

// DayKey agrupa contagens por dia (do post), fonte, idioma e país estimado.
type DayKey struct {
	Day, Source, Lang, Country string
}

// TermKey é a chave de daily_terms e daily_brands.
type TermKey struct {
	DayKey
	Term string
}

// DayCount são as contagens de daily_items.
type DayCount struct {
	Items, Candidates int
}

// Batch acumula o resultado de uma execução para gravar tudo de uma vez.
type Batch struct {
	Candidates []Candidate
	Seen       []SeenKey
	Days       map[DayKey]*DayCount
	Terms      map[TermKey]int
	Brands     map[TermKey]int
}

// SeenKey identifica um item já processado.
type SeenKey struct {
	Source    string
	Key       int64
	CreatedAt time.Time
}

// NewBatch cria um lote vazio.
func NewBatch() *Batch {
	return &Batch{Days: map[DayKey]*DayCount{}, Terms: map[TermKey]int{}, Brands: map[TermKey]int{}}
}

// Add registra um item processado.
func (b *Batch) Add(it Item, lang, country string, candidate *Candidate, terms, brands []string) {
	dk := DayKey{Day: week.Day(it.CreatedAt), Source: it.Source, Lang: lang, Country: country}
	dc := b.Days[dk]
	if dc == nil {
		dc = &DayCount{}
		b.Days[dk] = dc
	}
	dc.Items++
	if candidate != nil {
		dc.Candidates++
		b.Candidates = append(b.Candidates, *candidate)
	}
	for _, t := range terms {
		b.Terms[TermKey{dk, t}]++
	}
	for _, br := range brands {
		b.Brands[TermKey{dk, br}]++
	}
	b.Seen = append(b.Seen, SeenKey{Source: it.Source, Key: SeenKeyOf(it.ExternalID), CreatedAt: it.CreatedAt})
}

// SeenKeyOf devolve a impressão de 64 bits de um external_id.
func SeenKeyOf(externalID string) int64 {
	sum := sha256.Sum256([]byte(externalID))
	return int64(binary.BigEndian.Uint64(sum[:8]))
}

// IsSeen diz se o item já foi processado numa execução anterior.
func IsSeen(ctx context.Context, db *sql.DB, source, externalID string) (bool, error) {
	var one int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM seen WHERE source = ? AND key = ?`,
		source, SeenKeyOf(externalID)).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

// SaveBatch grava o lote numa transação. Termos com contagem menor que
// minTermCount não são gravados (é o que segura o tamanho de daily_terms).
func SaveBatch(ctx context.Context, db *sql.DB, b *Batch, minTermCount int, now time.Time) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // sem efeito depois do Commit

	if err := insertCandidates(ctx, tx, b.Candidates, now); err != nil {
		return err
	}
	if err := execEach(ctx, tx, `INSERT INTO seen (source, key, created_at) VALUES (?, ?, ?)
		ON CONFLICT (source, key) DO NOTHING`, len(b.Seen), func(i int) []any {
		s := b.Seen[i]
		return []any{s.Source, s.Key, s.CreatedAt.Unix()}
	}); err != nil {
		return fmt.Errorf("seen: %w", err)
	}

	days := make([]DayKey, 0, len(b.Days))
	for k := range b.Days {
		days = append(days, k)
	}
	if err := execEach(ctx, tx, `INSERT INTO daily_items (day, source, lang, country_est, items, candidates)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (day, source, lang, country_est) DO UPDATE SET
		items = items + excluded.items, candidates = candidates + excluded.candidates`, len(days), func(i int) []any {
		k := days[i]
		return []any{k.Day, k.Source, k.Lang, k.Country, b.Days[k].Items, b.Days[k].Candidates}
	}); err != nil {
		return fmt.Errorf("daily_items: %w", err)
	}

	if err := upsertCounts(ctx, tx, "daily_terms", "term", b.Terms, minTermCount); err != nil {
		return err
	}
	if err := upsertCounts(ctx, tx, "daily_brands", "brand", b.Brands, 1); err != nil {
		return err
	}
	return tx.Commit()
}

func insertCandidates(ctx context.Context, tx *sql.Tx, cs []Candidate, now time.Time) error {
	return execEach(ctx, tx, `INSERT INTO items
		(source, external_id, url, created_at, week, collected_at, text, author_hash, lang_hint, engagement,
		 brands, lang, country_est, pain_score, signals)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (source, external_id) DO NOTHING`, len(cs), func(i int) []any {
		c := cs[i]
		return []any{c.Source, c.ExternalID, c.URL, c.CreatedAt.Unix(), week.Of(c.CreatedAt), now.Unix(),
			Truncate(c.Text, MaxTextRunes), c.AuthorHash, c.LangHint, c.Engagement,
			strings.Join(c.Brands, ","), c.Lang, c.Country, c.PainScore, strings.Join(c.Signals, ",")}
	})
}

// upsertCounts soma as contagens em daily_terms ou daily_brands.
func upsertCounts(ctx context.Context, tx *sql.Tx, table, col string, counts map[TermKey]int, min int) error {
	keys := make([]TermKey, 0, len(counts))
	for k, n := range counts {
		if n >= min {
			keys = append(keys, k)
		}
	}
	// table e col são constantes do código, não entrada do usuário.
	q := fmt.Sprintf(`INSERT INTO %s (day, source, lang, country_est, %s, count) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (day, source, lang, country_est, %s) DO UPDATE SET count = count + excluded.count`, table, col, col)
	if err := execEach(ctx, tx, q, len(keys), func(i int) []any {
		k := keys[i]
		return []any{k.Day, k.Source, k.Lang, k.Country, k.Term, counts[k]}
	}); err != nil {
		return fmt.Errorf("%s: %w", table, err)
	}
	return nil
}

func execEach(ctx context.Context, tx *sql.Tx, q string, n int, args func(int) []any) error {
	if n == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, q)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for i := 0; i < n; i++ {
		if _, err := stmt.ExecContext(ctx, args(i)...); err != nil {
			return err
		}
	}
	return nil
}

// PruneSeen apaga as impressões de posts anteriores a before: a próxima
// janela da fonte começa depois disso, então eles não voltam mais.
func PruneSeen(ctx context.Context, db *sql.DB, source string, before time.Time) (int64, error) {
	res, err := db.ExecContext(ctx, `DELETE FROM seen WHERE source = ? AND created_at < ?`, source, before.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
