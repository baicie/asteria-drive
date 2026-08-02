package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/baicie/asteria-drive/internal/postgres"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	connection := os.Getenv("ASTERIA_DATABASE_URL")
	if connection == "" {
		logger.Error("ASTERIA_DATABASE_URL is required")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := postgres.Migrate(ctx, connection); err != nil {
		logger.Error("database migration failed", "error", err)
		os.Exit(1)
	}
	logger.Info("database migrations applied")
}
