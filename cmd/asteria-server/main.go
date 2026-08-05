package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/baicie/asteria-drive/internal/auth"
	"github.com/baicie/asteria-drive/internal/buildinfo"
	"github.com/baicie/asteria-drive/internal/config"
	"github.com/baicie/asteria-drive/internal/drive"
	"github.com/baicie/asteria-drive/internal/maintenance"
	"github.com/baicie/asteria-drive/internal/memory"
	"github.com/baicie/asteria-drive/internal/observability"
	"github.com/baicie/asteria-drive/internal/postgres"
	"github.com/baicie/asteria-drive/internal/s3store"
	"github.com/baicie/asteria-drive/internal/server"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println(buildinfo.String("asteria-server"))
		return
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	metrics := observability.NewMetrics()
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
	storage = observability.InstrumentStorage(storage, metrics)
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
	var authenticator auth.Authenticator
	switch cfg.AuthMode {
	case "trusted-dev":
		principals := make(map[string]auth.Principal, len(cfg.Tokens))
		for token, configured := range cfg.Tokens {
			principals[token] = auth.Principal{
				Identity:          drive.Identity{TenantID: configured.TenantID, PrincipalID: configured.PrincipalID},
				TenantDisplayName: configured.TenantName, Role: drive.RoleOwner,
			}
			if _, err := service.EnsureTenant(startupCtx, configured.TenantID, configured.TenantName); err != nil {
				logger.Error("tenant initialization failed", "tenant_id", configured.TenantID, "error", err)
				os.Exit(1)
			}
		}
		authenticator, err = auth.NewTrusted(principals)
		if err != nil {
			logger.Error("authentication configuration is invalid", "error", err)
			os.Exit(1)
		}
	case "oidc":
		for _, configured := range cfg.OIDCBootstrap {
			if _, err := service.EnsureTenant(startupCtx, configured.TenantID, configured.TenantName); err != nil {
				logger.Error("tenant initialization failed", "tenant_id", configured.TenantID, "error", err)
				os.Exit(1)
			}
			if _, err := repository.EnsureOIDCMember(startupCtx, drive.OIDCMemberSeed{
				PrincipalID: configured.PrincipalID, TenantID: configured.TenantID,
				Issuer: configured.Issuer, Subject: configured.Subject,
				DisplayName: configured.DisplayName, Role: drive.AccessRole(configured.Role), Now: time.Now().UTC(),
			}); err != nil {
				logger.Error("OIDC member bootstrap failed", "tenant_id", configured.TenantID, "error", err)
				os.Exit(1)
			}
		}
		resolver := func(ctx context.Context, issuer, subject, tenantID string) (auth.Principal, error) {
			record, err := repository.ResolveOIDCPrincipal(ctx, issuer, subject, tenantID)
			if err != nil {
				return auth.Principal{}, err
			}
			return auth.Principal{
				Identity: record.Identity, TenantDisplayName: record.TenantDisplayName, Role: record.Role,
			}, nil
		}
		authenticator, err = auth.NewOIDC(startupCtx, cfg.OIDCIssuer, cfg.OIDCClientID, resolver)
		if err != nil {
			logger.Error("OIDC initialization failed", "error", err)
			os.Exit(1)
		}
	default:
		logger.Error("authentication mode is unsupported", "mode", cfg.AuthMode)
		os.Exit(1)
	}
	httpServer, err := server.New(server.Options{
		Address: cfg.Address, Service: service, Authenticator: authenticator, Logger: logger,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout, ReadTimeout: cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout, IdleTimeout: cfg.IdleTimeout,
		Metrics: metrics,
	})
	if err != nil {
		logger.Error("HTTP server initialization failed", "error", err)
		os.Exit(1)
	}
	var maintenanceLoop *maintenance.Loop
	var maintenanceCancel context.CancelFunc
	if cfg.MaintenanceEnabled {
		maintenanceLoop, err = maintenance.New(maintenance.Options{
			Repository: repository, Storage: storage, Metrics: metrics,
			Interval: cfg.MaintenanceInterval, LeaseDuration: cfg.MaintenanceLease,
			StaleAfter: cfg.MaintenanceStaleAfter, RecycleRetention: cfg.RecycleRetention,
			BatchSize: cfg.MaintenanceBatchSize,
		})
		if err != nil {
			logger.Error("maintenance initialization failed", "error", err)
			os.Exit(1)
		}
		maintenanceCtx, cancel := context.WithCancel(context.Background())
		maintenanceCancel = cancel
		maintenanceLoop.Start(maintenanceCtx)
	}

	metricsServer := observability.NewServer(cfg.MetricsAddress, metrics.Handler())
	type serverResult struct {
		name string
		err  error
	}
	results := make(chan serverResult, 2)
	var servers sync.WaitGroup
	servers.Add(2)
	go func() {
		defer servers.Done()
		results <- serverResult{name: "api", err: httpServer.ListenAndServe()}
	}()
	go func() {
		defer servers.Done()
		results <- serverResult{name: "metrics", err: metricsServer.ListenAndServe()}
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)

	logger.Info("Asteria Drive server listening", "address", cfg.Address, "metrics_address", cfg.MetricsAddress,
		"metadata_driver", cfg.MetadataDriver, "storage_driver", cfg.StorageDriver)
	failed := false
	select {
	case <-stop:
	case result := <-results:
		if result.err != nil && !errors.Is(result.err, http.ErrServerClosed) {
			logger.Error("server stopped unexpectedly", "server", result.name, "error", result.err)
			failed = true
		}
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	if maintenanceCancel != nil {
		maintenanceCancel()
		maintenanceLoop.Wait()
	}
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("API graceful shutdown failed", "error", err)
		failed = true
	}
	if err := metricsServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("metrics graceful shutdown failed", "error", err)
		failed = true
	}
	servers.Wait()
	if failed {
		os.Exit(1)
	}
}
