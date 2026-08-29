package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"parser-engine/internal/config"
	"parser-engine/internal/dynamicjob"
	"parser-engine/internal/miniogateway"
)

type schedulerRuntimeConfig struct {
	Address           string
	MinIO             miniogateway.Config
	Store             dynamicjob.StoreConfig
	Job               dynamicjob.Options
	Handler           dynamicjob.HandlerOptions
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	LogLevel          slog.Level
	LogFormat         string
}

func loadSchedulerRuntimeConfig(lookup config.LookupFunc) (schedulerRuntimeConfig, error) {
	if lookup == nil {
		return schedulerRuntimeConfig{}, fmt.Errorf("configuration lookup function must not be nil")
	}
	l := &httpEnvLoader{lookup: lookup}
	cfg := schedulerRuntimeConfig{
		Address: ":8080",
		MinIO:   miniogateway.Config{Timeout: 10 * time.Minute},
		Store: dynamicjob.StoreConfig{
			Port: 1433, TargetPort: 1433, ConnectTimeout: 30 * time.Second,
			Schema: "dbo", ParamParseFileTable: "ParamParseFile",
			MappingTable: "ParamParseFileMappingColumn", ProductTable: "Product",
			ReaderTable: "ParamReadFile", ReaderColumnTable: "ParamReadFileColumn",
			PasswordDecryptFunction: "fnDecrypt", BatchSize: 500,
			BatchMaxBytes: 64 * 1024 * 1024,
		},
		Job: dynamicjob.Options{
			MaxInputBytes: 512 * 1024 * 1024, MaxRecordBytes: 8 * 1024 * 1024,
			MaxDocumentBytes: 512 * 1024 * 1024, MaxFields: 100_000,
			DefaultBatchSize: 500, DefaultBatchMaxBytes: 64 * 1024 * 1024,
		},
		Handler: dynamicjob.HandlerOptions{
			MaxRequestBytes: 16 * 1024, RequestTimeout: 10 * time.Minute,
			MaxConcurrentRequests: 4,
		},
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      15 * time.Minute,
		IdleTimeout:       60 * time.Second,
		ShutdownTimeout:   30 * time.Second,
		LogLevel:          slog.LevelInfo,
		LogFormat:         "text",
	}

	cfg.Address = l.stringValue("HTTP_ADDR", cfg.Address)
	cfg.MinIO.BaseURL = l.rawValue("MINIO_BASE_URL", "")
	cfg.MinIO.BucketName = l.rawValue("MINIO_BUCKET_NAME", "")
	cfg.MinIO.Timeout = l.durationValue("MINIO_HTTP_TIMEOUT", cfg.MinIO.Timeout)
	pathPrefix := l.rawValue("MINIO_PATH_PREFIX", "")
	if pathPrefix != "" {
		cfg.Job.AllowedPathPrefixes = []string{pathPrefix}
		if err := miniogateway.ValidateReference(pathPrefix, "validation-probe"); err != nil {
			l.problem("MINIO_PATH_PREFIX must be a safe relative MinIO path")
		}
	}

	cfg.Store.Host = l.stringValue("SS_RC_HOST", "")
	cfg.Store.Port = l.intValue("SS_RC_PORT", cfg.Store.Port)
	cfg.Store.User = l.stringValue("SS_RC_USER", "")
	cfg.Store.Password = l.rawValue("SS_RC_PASSWORD", "")
	cfg.Store.ParamDatabase = l.stringValue("SS_RC_DB_PARAM_ENGINE", "")
	cfg.Store.ReconciliationDatabase = l.stringValue("SS_RC_DB_RECON_CONFIG", "")
	cfg.Store.EncryptionKey = l.rawValue("SCH_ENC_KEY", "")
	cfg.Store.TargetPort = l.intValue("SS_TR_PORT", cfg.Store.TargetPort)
	cfg.Store.SSLMode = l.stringValue("DYNAMIC_DB_SSL_MODE", "")
	cfg.Store.ConnectTimeout = l.durationValue("DYNAMIC_DB_CONNECT_TIMEOUT", cfg.Store.ConnectTimeout)
	cfg.Store.Schema = l.stringValue("DYNAMIC_PARAM_SCHEMA", cfg.Store.Schema)
	cfg.Store.ParamParseFileTable = l.stringValue("DYNAMIC_EXISTING_PROFILE_TABLE", cfg.Store.ParamParseFileTable)
	cfg.Store.MappingTable = l.stringValue("DYNAMIC_EXISTING_MAPPING_TABLE", cfg.Store.MappingTable)
	cfg.Store.ProductTable = l.stringValue("DYNAMIC_PRODUCT_TABLE", cfg.Store.ProductTable)
	cfg.Store.ReaderTable = l.stringValue("DYNAMIC_READER_TABLE", cfg.Store.ReaderTable)
	cfg.Store.ReaderColumnTable = l.stringValue("DYNAMIC_READER_COLUMN_TABLE", cfg.Store.ReaderColumnTable)
	cfg.Store.PasswordDecryptFunction = l.stringValue("DYNAMIC_PASSWORD_DECRYPT_FUNCTION", cfg.Store.PasswordDecryptFunction)
	cfg.Store.BatchSize = l.intValue("DYNAMIC_BATCH_SIZE", cfg.Store.BatchSize)
	cfg.Store.BatchMaxBytes = l.intValue("DYNAMIC_BATCH_MAX_BYTES", cfg.Store.BatchMaxBytes)

	cfg.Job.MaxInputBytes = l.int64Value("DYNAMIC_MAX_INPUT_BYTES", cfg.Job.MaxInputBytes)
	cfg.Job.MaxRecordBytes = l.intValue("DYNAMIC_MAX_RECORD_BYTES", cfg.Job.MaxRecordBytes)
	cfg.Job.MaxDocumentBytes = l.intValue("DYNAMIC_MAX_DOCUMENT_BYTES", cfg.Job.MaxDocumentBytes)
	cfg.Job.MaxFields = l.intValue("DYNAMIC_MAX_FIELDS", cfg.Job.MaxFields)
	cfg.Job.DefaultBatchSize = cfg.Store.BatchSize
	cfg.Job.DefaultBatchMaxBytes = cfg.Store.BatchMaxBytes

	cfg.Handler.MaxRequestBytes = l.int64Value("DYNAMIC_MAX_REQUEST_BYTES", cfg.Handler.MaxRequestBytes)
	cfg.Handler.RequestTimeout = l.durationValue("DYNAMIC_REQUEST_TIMEOUT", cfg.Handler.RequestTimeout)
	cfg.Handler.MaxConcurrentRequests = l.intValue("DYNAMIC_MAX_CONCURRENCY", cfg.Handler.MaxConcurrentRequests)
	cfg.Handler.RequireBearerAuth = l.boolValue("DYNAMIC_REQUIRE_AUTH", false)
	cfg.Handler.BearerToken = l.rawValue("DYNAMIC_BEARER_TOKEN", "")

	cfg.ReadHeaderTimeout = l.durationValue("HTTP_READ_HEADER_TIMEOUT", cfg.ReadHeaderTimeout)
	cfg.ReadTimeout = l.durationValue("HTTP_READ_TIMEOUT", cfg.ReadTimeout)
	cfg.WriteTimeout = l.durationValue("HTTP_WRITE_TIMEOUT", cfg.WriteTimeout)
	cfg.IdleTimeout = l.durationValue("HTTP_IDLE_TIMEOUT", cfg.IdleTimeout)
	cfg.ShutdownTimeout = l.durationValue("HTTP_SHUTDOWN_TIMEOUT", cfg.ShutdownTimeout)
	cfg.LogLevel = l.logLevel("LOG_LEVEL", cfg.LogLevel)
	cfg.LogFormat = strings.ToLower(l.stringValue("LOG_FORMAT", cfg.LogFormat))

	if err := validateListenAddress(cfg.Address); err != nil {
		l.problem("HTTP_ADDR must be a valid host:port with port between 1 and 65535")
	}
	if err := cfg.MinIO.Validate(); err != nil {
		l.problem(err.Error())
	}
	if err := cfg.Store.Validate(); err != nil {
		l.problem(err.Error())
	}
	if cfg.Handler.RequireBearerAuth && cfg.Handler.BearerToken == "" {
		l.problem("DYNAMIC_BEARER_TOKEN is required when DYNAMIC_REQUIRE_AUTH=true")
	}
	if cfg.Handler.BearerToken != "" && len(cfg.Handler.BearerToken) < 32 {
		l.problem("DYNAMIC_BEARER_TOKEN must contain at least 32 bytes")
	}
	if cfg.Handler.RequestTimeout > cfg.WriteTimeout {
		l.problem("DYNAMIC_REQUEST_TIMEOUT must not exceed HTTP_WRITE_TIMEOUT")
	}
	if cfg.LogFormat != "text" && cfg.LogFormat != "json" {
		l.problem("LOG_FORMAT must be text or json")
	}
	for name, value := range map[string]int64{
		"DYNAMIC_MAX_INPUT_BYTES":    int64(cfg.Job.MaxInputBytes),
		"DYNAMIC_MAX_RECORD_BYTES":   int64(cfg.Job.MaxRecordBytes),
		"DYNAMIC_MAX_DOCUMENT_BYTES": int64(cfg.Job.MaxDocumentBytes),
		"DYNAMIC_MAX_FIELDS":         int64(cfg.Job.MaxFields),
		"DYNAMIC_BATCH_SIZE":         int64(cfg.Store.BatchSize),
		"DYNAMIC_BATCH_MAX_BYTES":    int64(cfg.Store.BatchMaxBytes),
		"DYNAMIC_MAX_CONCURRENCY":    int64(cfg.Handler.MaxConcurrentRequests),
	} {
		if value <= 0 {
			l.problem(name + " must be greater than zero")
		}
	}
	for _, bound := range []struct {
		name  string
		value int64
		max   int64
	}{
		{"DYNAMIC_MAX_INPUT_BYTES", cfg.Job.MaxInputBytes, 2 * 1024 * 1024 * 1024},
		{"DYNAMIC_MAX_RECORD_BYTES", int64(cfg.Job.MaxRecordBytes), 8 * 1024 * 1024},
		{"DYNAMIC_MAX_DOCUMENT_BYTES", int64(cfg.Job.MaxDocumentBytes), 512 * 1024 * 1024},
		{"DYNAMIC_MAX_FIELDS", int64(cfg.Job.MaxFields), 100_000},
		{"DYNAMIC_BATCH_SIZE", int64(cfg.Store.BatchSize), 10_000},
		{"DYNAMIC_BATCH_MAX_BYTES", int64(cfg.Store.BatchMaxBytes), 128 * 1024 * 1024},
		{"DYNAMIC_MAX_REQUEST_BYTES", cfg.Handler.MaxRequestBytes, 1024 * 1024},
		{"DYNAMIC_MAX_CONCURRENCY", int64(cfg.Handler.MaxConcurrentRequests), 256},
	} {
		if bound.value > bound.max {
			l.problem(fmt.Sprintf("%s must not exceed %d", bound.name, bound.max))
		}
	}
	if len(l.problems) > 0 {
		return schedulerRuntimeConfig{}, fmt.Errorf("invalid scheduler configuration:\n - %s", strings.Join(uniqueStrings(l.problems), "\n - "))
	}
	return cfg, nil
}

func runScheduler(ctx context.Context) error {
	cfg, err := loadSchedulerRuntimeConfig(os.LookupEnv)
	if err != nil {
		return err
	}
	logger := newSchedulerLogger(cfg)
	source, err := miniogateway.New(cfg.MinIO)
	if err != nil {
		return fmt.Errorf("initialize MinIO gateway: %w", err)
	}
	store, err := dynamicjob.OpenSQLStore(ctx, cfg.Store)
	if err != nil {
		return fmt.Errorf("initialize dynamic MSSQL store: %w", err)
	}
	defer store.Close()
	service, err := dynamicjob.NewService(source, store, cfg.Job)
	if err != nil {
		return fmt.Errorf("initialize dynamic parser job: %w", err)
	}
	cfg.Handler.Logger = logger
	handler, err := dynamicjob.NewHandler(service, cfg.Handler)
	if err != nil {
		return fmt.Errorf("initialize scheduler endpoint: %w", err)
	}
	listener, err := net.Listen("tcp", cfg.Address)
	if err != nil {
		return fmt.Errorf("listen for scheduler endpoint: %w", err)
	}
	server := &http.Server{
		Addr: cfg.Address, Handler: handler,
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ReadHeaderTimeout: cfg.ReadHeaderTimeout, ReadTimeout: cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout, IdleTimeout: cfg.IdleTimeout, MaxHeaderBytes: 32 * 1024,
	}
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.Serve(listener) }()
	logger.InfoContext(ctx, "server_started", "address", cfg.Address, "route", dynamicjob.Route)
	select {
	case serveErr := <-serverErrors:
		if errors.Is(serveErr, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve scheduler endpoint: %w", serveErr)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		shutdownErr := server.Shutdown(shutdownCtx)
		if shutdownErr != nil {
			return errors.Join(fmt.Errorf("shutdown scheduler endpoint: %w", shutdownErr), server.Close())
		}
		serveErr := <-serverErrors
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return fmt.Errorf("serve scheduler endpoint: %w", serveErr)
		}
		logger.Info("server_stopped")
		return nil
	}
}

func newSchedulerLogger(cfg schedulerRuntimeConfig) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	if cfg.LogFormat == "json" {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts)).With("component", "dynamic_parser_job")
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts)).With("component", "dynamic_parser_job")
}
