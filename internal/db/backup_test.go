package db

import (
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBackupRotationAndRestore(t *testing.T) {
	ctx := context.Background()
	conn, _ := openTemp(t)
	if _, err := Migrate(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO cursors (source, value, updated_at) VALUES ('hn', '123', 1)`); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "shared", "ideias-backup")
	// Um arquivo que não é backup não pode ser apagado pela rotação.
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "outra-coisa.txt"), []byte("x"), 0o644)

	start := time.Date(2026, 11, 29, 9, 0, 0, 0, time.UTC)
	var last string
	for i := 0; i < 6; i++ {
		p, err := Backup(ctx, conn, dir, 4, start.AddDate(0, 0, 7*i))
		if err != nil {
			t.Fatal(err)
		}
		last = p
	}
	list, err := Backups(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 4 || list[0] != last || filepath.Base(list[3]) != "ideias-20261213-090000.db.gz" {
		t.Errorf("backups = %v", list)
	}
	if _, err := os.Stat(filepath.Join(dir, "outra-coisa.txt")); err != nil {
		t.Error("a rotação apagou um arquivo que não era backup")
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, "*.parcial")); len(leftovers) > 0 {
		t.Errorf("sobrou arquivo parcial: %v", leftovers)
	}

	// Restaurar: descomprimir e abrir como banco normal.
	f, err := os.Open(last)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restaurado.db")
	out, _ := os.Create(restored)
	io.Copy(out, zr)
	out.Close()
	r, err := Open(restored)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var v string
	if err := r.QueryRow(`SELECT value FROM cursors WHERE source = 'hn'`).Scan(&v); err != nil || v != "123" {
		t.Errorf("restauração: %q %v", v, err)
	}
	pending, err := Pending(ctx, r)
	if err != nil || len(pending) != 0 {
		t.Errorf("banco restaurado com migrações pendentes: %v %v", pending, err)
	}
}
