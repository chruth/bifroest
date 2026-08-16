package queue

import (
	"database/sql"
	"fmt"
	"sort"

	_ "modernc.org/sqlite"

	"github.com/chruth/bifroest/migrations"
)

// Open opens (creating if necessary) the SQLite database at path, applies
// pragmas suited to a small single-writer job queue, and runs any pending
// migrations.
func Open(path string) (*sql.DB, error) {
	// Pragmas go in the DSN, not a db.Exec call after opening: database/sql
	// pools multiple connections, and a plain PRAGMA is per-connection state
	// that only affects whichever single connection happens to run it.
	// modernc.org/sqlite applies DSN pragma params to every new connection
	// as it's opened, which is what a busy_timeout (and any future
	// per-connection pragma) actually needs to hold under concurrent
	// workers. journal_mode=WAL is the one exception - SQLite persists it
	// in the database file itself - but it's kept here too for clarity.
	db, err := sql.Open("sqlite", path+"?_busy_timeout=5000&_journal_mode=WAL")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return db, nil
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY, applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		return err
	}

	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE name = ?`, name).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			continue
		}

		sqlBytes, err := migrations.FS.ReadFile(name)
		if err != nil {
			return err
		}

		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(sqlBytes)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (name) VALUES (?)`, name); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}

	return nil
}
