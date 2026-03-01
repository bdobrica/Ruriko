package sqliteutil

import (
	"database/sql"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"
)

// MigrationOptions configures migration execution.
type MigrationOptions struct {
	ReadDir  func(string) ([]fs.DirEntry, error)
	ReadFile func(string) ([]byte, error)
	Dir      string

	ValidateUniqueVersions bool
	RecordAppliedAt        bool
	Now                    func() time.Time
	OnApplied              func(version int, description string)
}

// RunMigrations applies pending SQL migrations from opts.Dir.
func RunMigrations(db *sql.DB, opts MigrationOptions) error {
	if db == nil {
		return fmt.Errorf("nil database")
	}
	if opts.ReadDir == nil || opts.ReadFile == nil {
		return fmt.Errorf("migration readers must be configured")
	}
	dir := opts.Dir
	if dir == "" {
		dir = "migrations"
	}
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}

	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			description TEXT NOT NULL
		)
	`)
	if err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	var currentVersion int
	err = db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&currentVersion)
	if err != nil {
		return fmt.Errorf("get current schema version: %w", err)
	}

	entries, err := opts.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	if opts.ValidateUniqueVersions {
		seenVersions := make(map[int]string, len(entries))
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
				continue
			}

			version, _, ok := parseMigrationName(entry.Name())
			if !ok {
				continue
			}

			if prev, exists := seenVersions[version]; exists {
				return fmt.Errorf("duplicate migration version %04d: %q and %q", version, prev, entry.Name())
			}
			seenVersions[version] = entry.Name()
		}
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		version, description, ok := parseMigrationName(entry.Name())
		if !ok {
			continue
		}
		if version <= currentVersion {
			continue
		}

		content, err := opts.ReadFile(path.Join(dir, entry.Name()))
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}

		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin transaction for migration %d: %w", version, err)
		}

		if _, err := tx.Exec(string(content)); err != nil {
			tx.Rollback()
			return fmt.Errorf("execute migration %d: %w", version, err)
		}

		if opts.RecordAppliedAt {
			_, err = tx.Exec(
				"INSERT INTO schema_migrations (version, applied_at, description) VALUES (?, ?, ?)",
				version, nowFn(), description,
			)
		} else {
			_, err = tx.Exec(
				"INSERT INTO schema_migrations (version, description) VALUES (?, ?)",
				version, description,
			)
		}
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %d: %w", version, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", version, err)
		}

		if opts.OnApplied != nil {
			opts.OnApplied(version, description)
		}
	}

	return nil
}

func parseMigrationName(name string) (version int, description string, ok bool) {
	parts := strings.SplitN(name, "_", 2)
	if len(parts) < 2 {
		return 0, "", false
	}
	if _, err := fmt.Sscanf(parts[0], "%d", &version); err != nil {
		return 0, "", false
	}
	return version, strings.TrimSuffix(parts[1], ".sql"), true
}
