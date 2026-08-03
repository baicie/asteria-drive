package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	pool *pgxpool.Pool
}

func New(ctx context.Context, connection string) (*Repository, error) {
	config, err := pgxpool.ParseConfig(connection)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	config.MaxConnLifetime = 30 * time.Minute
	config.MaxConnIdleTime = 5 * time.Minute
	config.HealthCheckPeriod = 30 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create PostgreSQL pool: %w", err)
	}
	repository := &Repository{pool: pool}
	if err := repository.Ready(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return repository, nil
}

func (r *Repository) Ready(ctx context.Context) error { return r.pool.Ping(ctx) }

func (r *Repository) Close() { r.pool.Close() }

func mapError(err error, fallback drive.ErrorCode, message string) error {
	if err == nil {
		return nil
	}
	if drive.CodeOf(err) != drive.CodeInternal {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return drive.E(drive.CodeNotFound, "resource was not found")
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			if pgErr.ConstraintName == "file_node_active_name_unique" {
				if fallback == drive.CodeRestoreConflict {
					return drive.E(drive.CodeRestoreConflict, "original location is unavailable")
				}
				return drive.E(drive.CodeNameConflict, "an active item with this name already exists")
			}
			return drive.E(fallback, message, err)
		case "23503":
			return drive.E(drive.CodeNotFound, "related resource was not found")
		case "22P02":
			return drive.E(drive.CodeInvalidRequest, "identifier must be a valid UUID")
		case "40001", "40P01", "55P03":
			return drive.Retryable(drive.CodeDependencyUnavailable, "database transaction must be retried", err)
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return drive.Retryable(drive.CodeDependencyUnavailable, "database operation was canceled", err)
	}
	return drive.E(fallback, message, err)
}

func commit(tx pgx.Tx, ctx context.Context) error {
	if err := tx.Commit(ctx); err != nil {
		return mapError(err, drive.CodeInternal, "database commit failed")
	}
	return nil
}

var _ drive.Repository = (*Repository)(nil)
