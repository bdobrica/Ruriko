package sqliteutil

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestOpen_AppliesPragmas(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "open.db")
	db, err := Open(dbPath, OpenOptions{
		Pragmas: []string{
			"PRAGMA foreign_keys = ON",
			"PRAGMA journal_mode = WAL",
		},
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	var fk int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Fatalf("foreign_keys pragma: got %d, want 1", fk)
	}
}

func TestRunMigrations_AppliesPending(t *testing.T) {
	root := t.TempDir()
	migDir := filepath.Join(root, "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatalf("mkdir migrations: %v", err)
	}
	if err := os.WriteFile(filepath.Join(migDir, "0001_init.sql"), []byte(`CREATE TABLE test_items (id INTEGER PRIMARY KEY, name TEXT);`), 0o644); err != nil {
		t.Fatalf("write migration 1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(migDir, "0002_seed.sql"), []byte(`INSERT INTO test_items(name) VALUES ('ok');`), 0o644); err != nil {
		t.Fatalf("write migration 2: %v", err)
	}

	db, err := Open(filepath.Join(root, "migrate.db"), OpenOptions{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	diskFS := os.DirFS(root)
	if err := RunMigrations(db, MigrationOptions{
		ReadDir:  func(name string) ([]fs.DirEntry, error) { return fs.ReadDir(diskFS, name) },
		ReadFile: func(name string) ([]byte, error) { return fs.ReadFile(diskFS, name) },
		Dir:      "migrations",
	}); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM test_items").Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("seed rows: got %d, want 1", count)
	}

	// Idempotent second pass.
	if err := RunMigrations(db, MigrationOptions{
		ReadDir:  func(name string) ([]fs.DirEntry, error) { return fs.ReadDir(diskFS, name) },
		ReadFile: func(name string) ([]byte, error) { return fs.ReadFile(diskFS, name) },
		Dir:      "migrations",
	}); err != nil {
		t.Fatalf("RunMigrations second pass: %v", err)
	}

	if err := db.QueryRow("SELECT COUNT(*) FROM test_items").Scan(&count); err != nil {
		t.Fatalf("count rows second pass: %v", err)
	}
	if count != 1 {
		t.Fatalf("seed rows after second pass: got %d, want 1", count)
	}
}

func TestRunMigrations_DuplicateVersionsRejected(t *testing.T) {
	root := t.TempDir()
	migDir := filepath.Join(root, "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatalf("mkdir migrations: %v", err)
	}
	if err := os.WriteFile(filepath.Join(migDir, "0001_first.sql"), []byte(`SELECT 1;`), 0o644); err != nil {
		t.Fatalf("write migration first: %v", err)
	}
	if err := os.WriteFile(filepath.Join(migDir, "0001_second.sql"), []byte(`SELECT 2;`), 0o644); err != nil {
		t.Fatalf("write migration second: %v", err)
	}

	db, err := Open(filepath.Join(root, "dup.db"), OpenOptions{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	diskFS := os.DirFS(root)
	err = RunMigrations(db, MigrationOptions{
		ReadDir:                func(name string) ([]fs.DirEntry, error) { return fs.ReadDir(diskFS, name) },
		ReadFile:               func(name string) ([]byte, error) { return fs.ReadFile(diskFS, name) },
		Dir:                    "migrations",
		ValidateUniqueVersions: true,
		RecordAppliedAt:        true,
		Now:                    time.Now,
	})
	if err == nil {
		t.Fatal("expected duplicate migration version error, got nil")
	}
}
