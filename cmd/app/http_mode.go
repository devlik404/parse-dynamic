package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"parser-engine/internal/config"
	"parser-engine/internal/miniogateway"
	"parser-engine/internal/preview"
)

const (
	applicationModeJob  = "JOB"
	applicationModeHTTP = "HTTP"
)

type httpRuntimeConfig struct {
	Address           string
	MinIO             miniogateway.Config
	Parser            config.ParserConfig
	Preview           preview.Options
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
}

func applicationMode(lookup config.LookupFunc) (string, error) {
	if lookup == nil {
		return "", fmt.Errorf("configuration lookup function must not be nil")
	}
	if value, found := lookup("APP_MODE"); found {
		mode := strings.ToUpper(strings.TrimSpace(value))
		switch mode {
		case applicationModeJob, applicationModeHTTP:
			return mode, nil
		default:
			return "", fmt.Errorf("APP_MODE must be JOB or HTTP")
		}
	}

	// MODE predates the HTTP service and was commonly set to deployment labels
	// such as "production". Preserve that behavior: only the explicit HTTP
	// value opts into the server; every other legacy value remains a JOB.
	legacy, found := lookup("MODE")
	if !found {
		return applicationModeJob, nil
	}
	switch strings.ToUpper(strings.TrimSpace(legacy)) {
	case applicationModeHTTP:
		return applicationModeHTTP, nil
	case "CLI", applicationModeJob:
		return applicationModeJob, nil
	default:
		return applicationModeJob, nil
	}
}

func loadHTTPRuntimeConfig(lookup config.LookupFunc) (httpRuntimeConfig, error) {
	if lookup == nil {
		return httpRuntimeConfig{}, fmt.Errorf("configuration lookup function must not be nil")
	}
	l := &httpEnvLoader{lookup: lookup}
	cfg := httpRuntimeConfig{
		Address: ":8080",
		MinIO: miniogateway.Config{
			Timeout: 5 * time.Minute,
		},
		Preview: preview.Options{
			DefaultLimit:          20,
			MaxLimit:              100,
			MaxInputBytes:         64 * 1024 * 1024,
			MaxRequestBytes:       16 * 1024,
			MaxResponseBytes:      2 * 1024 * 1024,
			MaxValueBytes:         1024,
			RequestTimeout:        30 * time.Second,
			MaxConcurrentRequests: 4,
		},
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       60 * time.Second,
		ShutdownTimeout:   10 * time.Second,
	}

	cfg.Address = l.stringValue("HTTP_ADDR", cfg.Address)
	cfg.MinIO.BaseURL = l.rawValue("MINIO_BASE_URL", "")
	cfg.MinIO.BucketName = l.rawValue("MINIO_BUCKET_NAME", "")
	cfg.MinIO.Timeout = l.durationValue("MINIO_HTTP_TIMEOUT", cfg.MinIO.Timeout)
	pathPrefix := l.rawValue("MINIO_PATH_PREFIX", "")
	if pathPrefix != "" {
		cfg.Preview.AllowedPathPrefixes = []string{pathPrefix}
		if err := miniogateway.ValidateReference(pathPrefix, "validation-probe"); err != nil {
			l.problem("MINIO_PATH_PREFIX must be a safe relative MinIO path")
		}
	}
	cfg.Preview.RequireBearerAuth = l.boolValue("PREVIEW_REQUIRE_AUTH", false)
	cfg.Preview.BearerToken = l.rawValue("PREVIEW_BEARER_TOKEN", "")
	cfg.Preview.DefaultLimit = l.intValue("PREVIEW_DEFAULT_LIMIT", cfg.Preview.DefaultLimit)
	cfg.Preview.MaxLimit = l.intValue("PREVIEW_MAX_LIMIT", cfg.Preview.MaxLimit)
	cfg.Preview.MaxInputBytes = l.int64Value("PREVIEW_MAX_INPUT_BYTES", cfg.Preview.MaxInputBytes)
	cfg.Preview.MaxRequestBytes = l.int64Value("PREVIEW_MAX_REQUEST_BYTES", cfg.Preview.MaxRequestBytes)
	cfg.Preview.MaxResponseBytes = l.int64Value("PREVIEW_MAX_RESPONSE_BYTES", cfg.Preview.MaxResponseBytes)
	cfg.Preview.MaxValueBytes = l.intValue("PREVIEW_MAX_VALUE_BYTES", cfg.Preview.MaxValueBytes)
	cfg.Preview.RequestTimeout = l.durationValue("PREVIEW_REQUEST_TIMEOUT", cfg.Preview.RequestTimeout)
	cfg.Preview.MaxConcurrentRequests = l.intValue("PREVIEW_MAX_CONCURRENCY", cfg.Preview.MaxConcurrentRequests)
	cfg.ReadHeaderTimeout = l.durationValue("HTTP_READ_HEADER_TIMEOUT", cfg.ReadHeaderTimeout)
	cfg.ReadTimeout = l.durationValue("HTTP_READ_TIMEOUT", cfg.ReadTimeout)
	cfg.WriteTimeout = l.durationValue("HTTP_WRITE_TIMEOUT", cfg.WriteTimeout)
	cfg.IdleTimeout = l.durationValue("HTTP_IDLE_TIMEOUT", cfg.IdleTimeout)
	cfg.ShutdownTimeout = l.durationValue("HTTP_SHUTDOWN_TIMEOUT", cfg.ShutdownTimeout)

	parserConfig, parserErr := config.LoadParser(lookup)
	if parserErr != nil {
		l.problem("parser configuration is invalid: " + parserErr.Error())
	} else {
		cfg.Parser = parserConfig
	}

	if err := validateListenAddress(cfg.Address); err != nil {
		l.problem("HTTP_ADDR must be a valid host:port with port between 1 and 65535")
	}
	for _, item := range []struct {
		key   string
		value time.Duration
	}{
		{"HTTP_READ_HEADER_TIMEOUT", cfg.ReadHeaderTimeout},
		{"HTTP_READ_TIMEOUT", cfg.ReadTimeout},
		{"HTTP_WRITE_TIMEOUT", cfg.WriteTimeout},
		{"HTTP_IDLE_TIMEOUT", cfg.IdleTimeout},
		{"HTTP_SHUTDOWN_TIMEOUT", cfg.ShutdownTimeout},
		{"MINIO_HTTP_TIMEOUT", cfg.MinIO.Timeout},
	} {
		if item.value <= 0 {
			l.problem(item.key + " must be greater than zero")
		}
	}
	if err := cfg.MinIO.Validate(); err != nil {
		l.problem(err.Error())
	}
	if cfg.Preview.RequireBearerAuth && cfg.Preview.BearerToken == "" {
		l.problem("PREVIEW_BEARER_TOKEN is required when PREVIEW_REQUIRE_AUTH=true")
	}
	if cfg.Preview.RequestTimeout > 0 && cfg.WriteTimeout > 0 && cfg.Preview.RequestTimeout > cfg.WriteTimeout {
		l.problem("PREVIEW_REQUEST_TIMEOUT must not exceed HTTP_WRITE_TIMEOUT")
	}
	if len(l.problems) > 0 {
		return httpRuntimeConfig{}, fmt.Errorf("invalid HTTP configuration:\n - %s", strings.Join(uniqueStrings(l.problems), "\n - "))
	}
	return cfg, nil
}

func validateListenAddress(address string) error {
	if address == "" || strings.TrimSpace(address) != address {
		return fmt.Errorf("invalid address")
	}
	for _, r := range address {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return fmt.Errorf("invalid address")
		}
	}
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	value, err := strconv.Atoi(port)
	if err != nil || value < 1 || value > 65535 {
		return fmt.Errorf("invalid port")
	}
	return nil
}

func runHTTP(ctx context.Context) error {
	cfg, err := loadHTTPRuntimeConfig(os.LookupEnv)
	if err != nil {
		return err
	}
	source, err := miniogateway.New(cfg.MinIO)
	if err != nil {
		return fmt.Errorf("initialize MinIO gateway: %w", err)
	}
	handler, err := preview.NewHandler(source, cfg.Parser, cfg.Preview)
	if err != nil {
		return fmt.Errorf("initialize preview endpoint: %w", err)
	}

	server := newHTTPServer(ctx, cfg, handler)
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve preview HTTP endpoint: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		shutdownErr := server.Shutdown(shutdownCtx)
		if shutdownErr != nil {
			closeErr := server.Close()
			return errors.Join(fmt.Errorf("shutdown preview HTTP endpoint: %w", shutdownErr), closeErr)
		}
		serveErr := <-serverErrors
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return fmt.Errorf("serve preview HTTP endpoint: %w", serveErr)
		}
		return nil
	}
}

func newHTTPServer(ctx context.Context, cfg httpRuntimeConfig, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:    cfg.Address,
		Handler: handler,
		BaseContext: func(net.Listener) context.Context {
			return ctx
		},
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    32 * 1024,
	}
}

type httpEnvLoader struct {
	lookup   config.LookupFunc
	problems []string
}

func (l *httpEnvLoader) problem(value string) { l.problems = append(l.problems, value) }

func (l *httpEnvLoader) rawValue(key, fallback string) string {
	value, found := l.lookup(key)
	if !found {
		return fallback
	}
	return value
}

func (l *httpEnvLoader) stringValue(key, fallback string) string {
	return strings.TrimSpace(l.rawValue(key, fallback))
}

func (l *httpEnvLoader) boolValue(key string, fallback bool) bool {
	value, found := l.lookup(key)
	if !found {
		return fallback
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		l.problem(key + " must be true or false")
		return fallback
	}
	return parsed
}

func (l *httpEnvLoader) intValue(key string, fallback int) int {
	value, found := l.lookup(key)
	if !found {
		return fallback
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		l.problem(key + " must be a valid integer")
		return fallback
	}
	return parsed
}

func (l *httpEnvLoader) int64Value(key string, fallback int64) int64 {
	value, found := l.lookup(key)
	if !found {
		return fallback
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		l.problem(key + " must be a valid integer")
		return fallback
	}
	return parsed
}

func (l *httpEnvLoader) durationValue(key string, fallback time.Duration) time.Duration {
	value, found := l.lookup(key)
	if !found {
		return fallback
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		l.problem(key + " must be a valid duration such as 30s or 2m")
		return fallback
	}
	return parsed
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
