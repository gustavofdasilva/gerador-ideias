package db

import (
	"compress/gzip"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// BackupPrefix e BackupSuffix formam o nome dos backups:
// ideias-AAAAMMDD-HHMMSS.db.gz (a ordem alfabética é a cronológica).
const (
	BackupPrefix = "ideias-"
	BackupSuffix = ".db.gz"
)

// Backup grava uma cópia compacta e consistente do banco (VACUUM INTO) em
// dir, comprimida com gzip, e apaga os backups além dos keep mais novos.
// Devolve o caminho do arquivo criado.
func Backup(ctx context.Context, conn *sql.DB, dir string, keep int, now time.Time) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("pasta de backup %s: %w", dir, err)
	}

	// O VACUUM INTO vai para um arquivo local (rápido) e só a versão
	// comprimida vai para o destino, que no Android é o armazenamento
	// compartilhado (mais lento).
	tmp, err := os.CreateTemp("", "ideias-backup-*.db")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	os.Remove(tmpPath) // o VACUUM INTO exige que o arquivo não exista
	defer os.Remove(tmpPath)
	if _, err := conn.ExecContext(ctx, `VACUUM INTO ?`, tmpPath); err != nil {
		return "", fmt.Errorf("VACUUM INTO: %w", err)
	}

	name := BackupPrefix + now.UTC().Format("20060102-150405") + BackupSuffix
	final := filepath.Join(dir, name)
	partial := final + ".parcial"
	if err := gzipFile(tmpPath, partial); err != nil {
		os.Remove(partial)
		return "", err
	}
	if err := os.Rename(partial, final); err != nil {
		os.Remove(partial)
		return "", err
	}
	if err := pruneBackups(dir, keep); err != nil {
		return final, fmt.Errorf("backup criado, mas a limpeza dos antigos falhou: %w", err)
	}
	return final, nil
}

func gzipFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	zw, err := gzip.NewWriterLevel(out, gzip.BestCompression)
	if err != nil {
		out.Close()
		return err
	}
	if _, err := io.Copy(zw, in); err != nil {
		zw.Close()
		out.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Backups lista os backups de dir, do mais novo para o mais antigo.
func Backups(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() && strings.HasPrefix(n, BackupPrefix) && strings.HasSuffix(n, BackupSuffix) {
			out = append(out, filepath.Join(dir, n))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out, nil
}

func pruneBackups(dir string, keep int) error {
	list, err := Backups(dir)
	if err != nil {
		return err
	}
	for _, old := range list[min(keep, len(list)):] {
		if err := os.Remove(old); err != nil {
			return err
		}
	}
	return nil
}
