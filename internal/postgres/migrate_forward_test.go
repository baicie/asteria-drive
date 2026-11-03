package postgres

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// applyMigrationsThrough brings a schema to the published state it had at the given
// migration version, stopping there. It exists so a test can stand up the exact schema a
// deployment is running today and then prove that this branch's migrations apply cleanly
// on top of it. It deliberately reuses the embedded files and the same ordering and
// transaction rules as Migrate, so the "before" picture cannot drift from the real one.
func applyMigrationsThrough(ctx context.Context, connection string, through int64) error {
	conn, err := pgx.Connect(ctx, connection)
	if err != nil {
		return fmt.Errorf("connect for partial migration: %w", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(418535837262519637)`); err != nil {
		return fmt.Errorf("acquire partial migration lock: %w", err)
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock(418535837262519637)`)
	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS asteria_schema_migration (
			version bigint PRIMARY KEY,
			name text NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("create partial migration table: %w", err)
	}
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("read embedded migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, err := strconv.ParseInt(strings.SplitN(entry.Name(), "_", 2)[0], 10, 64)
		if err != nil {
			return fmt.Errorf("partial migration %s has invalid version: %w", entry.Name(), err)
		}
		if version > through {
			continue
		}
		contents, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read partial migration %s: %w", entry.Name(), err)
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin partial migration %s: %w", entry.Name(), err)
		}
		if _, err = tx.Exec(ctx, string(contents)); err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO asteria_schema_migration(version,name) VALUES($1,$2)`, version, entry.Name())
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply partial migration %s: %w", entry.Name(), err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit partial migration %s: %w", entry.Name(), err)
		}
	}
	return nil
}
