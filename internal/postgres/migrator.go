package postgres

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Migration struct {
	Version string
	Name    string
	Up      string
	Down    string
}

type Migrator struct {
	pool       *pgxpool.Pool
	migrations []Migration
}

func NewMigrator(pool *pgxpool.Pool, fsys fs.FS) (*Migrator, error) {
	migrations, err := loadMigrations(fsys)
	if err != nil {
		return nil, err
	}
	return &Migrator{pool: pool, migrations: migrations}, nil
}

func loadMigrations(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.Glob(fsys, "*.up.sql")
	if err != nil {
		return nil, fmt.Errorf("listing migrations: %w", err)
	}
	sort.Strings(entries)

	migrations := make([]Migration, 0, len(entries))
	for _, upName := range entries {
		base := strings.TrimSuffix(upName, ".up.sql")
		version, name, _ := strings.Cut(base, "_")

		upSQL, err := fs.ReadFile(fsys, upName)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", upName, err)
		}

		downSQL, err := fs.ReadFile(fsys, base+".down.sql")
		if err != nil {
			return nil, fmt.Errorf("reading down migration for %s: %w", base, err)
		}

		migrations = append(migrations, Migration{
			Version: version,
			Name:    name,
			Up:      string(upSQL),
			Down:    string(downSQL),
		})
	}

	if len(migrations) == 0 {
		return nil, fmt.Errorf("no migrations found")
	}
	return migrations, nil
}

func (m *Migrator) ensureTable(ctx context.Context) error {
	_, err := m.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT        PRIMARY KEY,
			name       TEXT        NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`)
	if err != nil {
		return fmt.Errorf("creating schema_migrations: %w", err)
	}
	return nil
}

func (m *Migrator) applied(ctx context.Context) (map[string]bool, error) {
	rows, err := m.pool.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("querying applied migrations: %w", err)
	}
	defer rows.Close()

	result := make(map[string]bool)
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return nil, err
		}
		result[version] = true
	}
	return result, rows.Err()
}

func (m *Migrator) Up(ctx context.Context) ([]string, error) {
	if err := m.ensureTable(ctx); err != nil {
		return nil, err
	}

	applied, err := m.applied(ctx)
	if err != nil {
		return nil, err
	}

	executed := make([]string, 0)
	for _, migration := range m.migrations {
		if applied[migration.Version] {
			continue
		}

		err := m.inTx(ctx, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, migration.Up); err != nil {
				return err
			}
			_, err := tx.Exec(ctx,
				`INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`,
				migration.Version, migration.Name)
			return err
		})
		if err != nil {
			return executed, fmt.Errorf("applying migration %s: %w", migration.Version, err)
		}

		executed = append(executed, migration.Version+"_"+migration.Name)
	}

	return executed, nil
}

func (m *Migrator) Down(ctx context.Context) (string, error) {
	if err := m.ensureTable(ctx); err != nil {
		return "", err
	}

	var version string
	err := m.pool.QueryRow(ctx,
		`SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&version)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", nil
		}
		return "", fmt.Errorf("finding last migration: %w", err)
	}

	var target *Migration
	for i := range m.migrations {
		if m.migrations[i].Version == version {
			target = &m.migrations[i]
			break
		}
	}
	if target == nil {
		return "", fmt.Errorf("migration %s not found in embedded files", version)
	}

	err = m.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, target.Down); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM schema_migrations WHERE version = $1`, version)
		return err
	})
	if err != nil {
		return "", fmt.Errorf("reverting migration %s: %w", version, err)
	}

	return target.Version + "_" + target.Name, nil
}

func (m *Migrator) Status(ctx context.Context) ([]string, error) {
	if err := m.ensureTable(ctx); err != nil {
		return nil, err
	}

	applied, err := m.applied(ctx)
	if err != nil {
		return nil, err
	}

	status := make([]string, 0, len(m.migrations))
	for _, migration := range m.migrations {
		mark := "pending"
		if applied[migration.Version] {
			mark = "applied"
		}
		status = append(status, fmt.Sprintf("%s_%s: %s", migration.Version, migration.Name, mark))
	}
	return status, nil
}

func (m *Migrator) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
