package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/baicie/asteria-drive/internal/buildinfo"
	"github.com/baicie/asteria-drive/internal/config"
	"github.com/baicie/asteria-drive/internal/postgres"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println(buildinfo.String("asteria-migrate"))
		return
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	connection, err := config.LoadDatabaseURL()
	if err != nil {
		logger.Error("database configuration is invalid", "error", err)
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
