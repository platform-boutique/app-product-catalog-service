package catalog

import (
	"context"
	"embed"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// advisoryLockID serialises schema migrations AND the seed step across replicas that start
// at the same time. Postgres advisory locks are scoped to the current database.
const advisoryLockID int64 = 0x70635f63617431 // "pc_cat1"

// Migrate applies all pending embedded migrations (goose, versions in goose_db_version)
// while holding a Postgres session-level advisory lock. It is a no-op when the schema is current.
func Migrate(ctx context.Context, p *pgxpool.Pool) error {
	sqlDB := stdlib.OpenDBFromPool(p)
	defer sqlDB.Close()

	locker, err := lock.NewPostgresSessionLocker(
		lock.WithLockID(advisoryLockID),
		lock.WithLockTimeout(1, 300), // retry every 1 s for up to 5 min while another replica migrates
	)
	if err != nil {
		return fmt.Errorf("migration locker: %w", err)
	}
	fsys, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return err
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, fsys,
		goose.WithSessionLocker(locker),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		return fmt.Errorf("migration provider: %w", err)
	}
	results, err := provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("migrate up: %w", err)
	}
	for _, r := range results {
		log.Infof("applied migration %s in %s", r.Source.Path, r.Duration)
	}
	v, err := provider.GetDBVersion(ctx)
	if err != nil {
		return fmt.Errorf("migration version: %w", err)
	}
	log.Infof("database schema at version %d (%d migration(s) applied now)", v, len(results))
	return nil
}
