// Package sqliteutil provides shared SQLite bootstrap helpers used by both
// Ruriko and Gitai stores.
package sqliteutil

import (
	"database/sql"
	"fmt"
)

// OpenOptions controls SQLite bootstrap behavior.
type OpenOptions struct {
	Pragmas      []string
	MaxOpenConns int
	MaxIdleConns int
}

// Open creates a SQLite connection, applies pool settings, and executes
// configured pragmas.
func Open(dbPath string, opts OpenOptions) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if opts.MaxOpenConns > 0 {
		db.SetMaxOpenConns(opts.MaxOpenConns)
	}
	if opts.MaxIdleConns > 0 {
		db.SetMaxIdleConns(opts.MaxIdleConns)
	}

	for _, pragma := range opts.Pragmas {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("set pragma: %w", err)
		}
	}

	return db, nil
}
