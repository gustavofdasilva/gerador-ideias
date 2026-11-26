// Package db abre o SQLite (driver puro em Go, sem CGO) e aplica as migrações.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open abre (e cria, se preciso) o banco em path, com WAL, chaves
// estrangeiras ligadas e espera de até 5 s quando o banco está ocupado.
func Open(path string) (*sql.DB, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("criar pasta do banco: %w", err)
		}
	}
	dsn := path + "?_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// Um só escritor por vez: evita SQLITE_BUSY entre conexões do mesmo processo.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("abrir %s: %w", path, err)
	}
	return db, nil
}

// Migration é um arquivo NNNN_nome.sql embutido.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

// Migrations devolve as migrações embutidas, em ordem.
func Migrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, err
	}
	var out []Migration
	seen := map[int]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		num, rest, ok := strings.Cut(strings.TrimSuffix(name, ".sql"), "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil || v <= 0 {
			return nil, fmt.Errorf("migração com nome inválido: %s (use NNNN_nome.sql)", name)
		}
		if prev, dup := seen[v]; dup {
			return nil, fmt.Errorf("migrações %s e %s têm a mesma versão", prev, name)
		}
		seen[v] = name
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return nil, err
		}
		out = append(out, Migration{Version: v, Name: rest, SQL: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

const createMigrationsTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    name       TEXT    NOT NULL,
    applied_at INTEGER NOT NULL
)`

// AppliedVersions devolve as versões já aplicadas. Num banco novo, nenhuma.
func AppliedVersions(ctx context.Context, db *sql.DB) (map[int]bool, error) {
	var exists int
	err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&exists)
	if err != nil {
		return nil, err
	}
	applied := map[int]bool{}
	if exists == 0 {
		return applied, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

// Pending devolve as migrações ainda não aplicadas.
func Pending(ctx context.Context, db *sql.DB) ([]Migration, error) {
	all, err := Migrations()
	if err != nil {
		return nil, err
	}
	applied, err := AppliedVersions(ctx, db)
	if err != nil {
		return nil, err
	}
	var pending []Migration
	for _, m := range all {
		if !applied[m.Version] {
			pending = append(pending, m)
		}
	}
	return pending, nil
}

// Migrate aplica as migrações pendentes, cada uma na sua transação, e devolve
// as que foram aplicadas. Rodar de novo não faz nada.
func Migrate(ctx context.Context, db *sql.DB) ([]Migration, error) {
	if _, err := db.ExecContext(ctx, createMigrationsTable); err != nil {
		return nil, err
	}
	pending, err := Pending(ctx, db)
	if err != nil {
		return nil, err
	}
	for i, m := range pending {
		if err := apply(ctx, db, m); err != nil {
			return pending[:i], fmt.Errorf("migração %04d_%s: %w", m.Version, m.Name, err)
		}
	}
	return pending, nil
}

func apply(ctx context.Context, db *sql.DB, m Migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // sem efeito depois do Commit
	if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
		m.Version, m.Name, time.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
