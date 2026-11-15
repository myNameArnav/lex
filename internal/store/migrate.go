package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Schema migrations are versioned with SQLite's PRAGMA user_version, which
// holds the number of migrations a database has had applied (0 for databases
// from before this scheme, and for new empty ones).
//
// To add a migration:
//
//  1. Append a func to migrations. Its position is its number: the Nth entry
//     moves a database from user_version N-1 to N. Never reorder, edit or
//     remove an entry that has shipped; fix mistakes with a new one.
//  2. Write it against tx only. It runs in its own transaction, and the
//     runner stamps user_version in that same transaction, so a migration
//     is applied fully and exactly once or not at all.
//  3. Add a test that opens a database at the previous version and checks
//     the result.
//
// Foreign keys are off while migrations run, so dropping a table doesn't
// cascade deletes into the tables that reference it. A migration that
// rebuilds a table (create new, copy, drop old, rename) should run
// PRAGMA foreign_key_check(<table>) itself before returning.
//
// The schema constant and lateColumns are the older, additive way to change
// the schema; they are applied by migration 1 and re-applied on every open
// (see migrate), so a new table in schema or a new column in lateColumns
// still reaches databases already at a later version. Use them only for
// additions (tables, indexes, columns with defaults). Anything else (renames,
// drops, rebuilds, backfills) must be a numbered migration, and must not be
// undone by a definition left behind in schema or lateColumns.
var migrations = []func(*sql.Tx) error{
	// 1: bring a database from any earlier Lex version to the schema as it
	// was when versioning was introduced. Idempotent by construction.
	ensureBaseSchema,
}

// ErrSchemaTooNew means the database was migrated by a newer Lex than this
// binary. Running against it could corrupt data this version doesn't know.
var ErrSchemaTooNew = errors.New("database schema is newer than this version of Lex supports")

// dbtx is the query surface shared by *sql.DB and *sql.Tx.
type dbtx interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// ensureBaseSchema creates missing tables and indexes and adds missing
// columns. It never changes or removes anything that exists.
func ensureBaseSchema(tx *sql.Tx) error {
	if _, err := tx.Exec(schema); err != nil {
		return err
	}
	if err := migrateDownloadedSubs(tx); err != nil {
		return err
	}
	return addLateColumns(tx)
}

// migrate brings db to the newest schema this binary knows.
func migrate(ctx context.Context, db *sql.DB) (err error) {
	// One connection throughout: PRAGMA foreign_keys is per connection, and
	// it can't change inside a transaction.
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	var version int
	if err := conn.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version > len(migrations) {
		return fmt.Errorf("%w (database is at schema version %d, this binary knows up to %d); upgrade Lex or restore a backup", ErrSchemaTooNew, version, len(migrations))
	}

	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer func() {
		// The connection goes back to the pool; it must not keep foreign
		// keys off.
		if _, ferr := conn.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`); ferr != nil && err == nil {
			err = ferr
		}
	}()

	// Migration 1 already does this when it runs; otherwise re-apply the
	// additive schema (see the comment on migrations).
	if version >= 1 {
		if err := inTx(ctx, conn, func(tx *sql.Tx) error { return ensureBaseSchema(tx) }); err != nil {
			return fmt.Errorf("schema: %w", err)
		}
	}

	for i := version; i < len(migrations); i++ {
		n := i + 1
		err := inTx(ctx, conn, func(tx *sql.Tx) error {
			// Re-read inside the transaction in case another process
			// migrated the same file meanwhile.
			var cur int
			if err := tx.QueryRow(`PRAGMA user_version`).Scan(&cur); err != nil {
				return err
			}
			if cur >= n {
				return nil
			}
			if err := migrations[i](tx); err != nil {
				return err
			}
			// PRAGMA takes no bound parameters; n is an int we computed.
			_, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, n))
			return err
		})
		if err != nil {
			return fmt.Errorf("migration %d: %w", n, err)
		}
	}
	return nil
}

func inTx(ctx context.Context, conn *sql.Conn, fn func(*sql.Tx) error) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
