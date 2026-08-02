package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/baicie/asteria-drive/internal/auth"
	"github.com/baicie/asteria-drive/internal/config"
	"github.com/baicie/asteria-drive/internal/drive"
	"github.com/baicie/asteria-drive/internal/memory"
	"github.com/baicie/asteria-drive/internal/postgres"
	"github.com/baicie/asteria-drive/internal/s3store"
	"github.com/baicie/asteria-drive/internal/server"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("configuration is invalid", "error", err)
		os.Exit(1)
	}
	startupCtx, startupCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer startupCancel()
	var repository drive.Repository
	switch cfg.MetadataDriver {
	case "memory":
		repository = memory.NewRepository()
	case "postgres":
		if cfg.AutoMigrate {
			if err := postgres.Migrate(startupCtx, cfg.DatabaseURL); err != nil {
				logger.Error("database migration failed", "error", err)
				os.Exit(1)
			}
		}
		repository, err = postgres.New(startupCtx, cfg.DatabaseURL)
		if err != nil {
			logger.Error("PostgreSQL initialization failed", "error", err)
			os.Exit(1)
		}
	}
	defer repository.Close()
	var storage drive.StorageProvider
	switch cfg.StorageDriver {
	case "memory":
		storage = memory.NewStorage("asteria-memory")
	case "s3":
		storage, err = s3store.New(startupCtx, s3store.Options{
			Endpoint: cfg.S3Endpoint, Region: cfg.S3Region, Bucket: cfg.S3Bucket,
			AccessKey: cfg.S3AccessKey, SecretKey: cfg.S3SecretKey, UsePathStyle: cfg.S3PathStyle,
			AutoCreateBucket: cfg.S3AutoCreate, EnableChecksumHeaders: cfg.S3ChecksumHeaders,
		})
		if err != nil {
			logger.Error("S3 initialization failed", "error", err)
			os.Exit(1)
		}
	}
	cursor, err := drive.NewCursorCodec(cfg.CursorKey)
	if err != nil {
		logger.Error("cursor configuration is invalid", "error", err)
		os.Exit(1)
	}
	service, err := drive.NewService(drive.ServiceOptions{
		Repository: repository, Storage: storage, Cursor: cursor, MaxFileSize: cfg.MaxFileSize,
		PartSize: cfg.PartSize, UploadTTL: cfg.UploadTTL, UploadSignTTL: cfg.UploadSignTTL, DownloadSignTTL: cfg.DownloadSignTTL,
	})
	if err != nil {
		logger.Error("service initialization failed", "error", err)
		os.Exit(1)
	}
	principals := make(map[string]auth.Principal, len(cfg.Tokens))
	for token, configured := range cfg.Tokens {
		principals[token] = auth.Principal{
			Identity:          drive.Identity{TenantID: configured.TenantID, PrincipalID: configured.PrincipalID},
			TenantDisplayName: configured.TenantName,
		}
		if _, err := service.EnsureTenant(startupCtx, configured.TenantID, configured.TenantName); err != nil {
			logger.Error("tenant initialization failed", "tenant_id", configured.TenantID, "error", err)
			os.Exit(1)
		}
	}
	authenticator, err := auth.NewTrusted(principals)
	if err != nil {
		logger.Error("authentication configuration is invalid", "error", err)
		os.Exit(1)
	}
	httpServer, err := server.New(server.Options{
		Address: cfg.Address, Service: service, Authenticator: authenticator, Logger: logger,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout, ReadTimeout: cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout, IdleTimeout: cfg.IdleTimeout,
	})
	if err != nil {
		logger.Error("HTTP server initialization failed", "error", err)
		os.Exit(1)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			logger.Error("graceful shutdown failed", "error", err)
		}
	}()

	logger.Info("Asteria Drive server listening", "address", cfg.Address, "metadata_driver", cfg.MetadataDriver, "storage_driver", cfg.StorageDriver)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("HTTP server stopped unexpectedly", "error", err)
		os.Exit(1)
	}
}
