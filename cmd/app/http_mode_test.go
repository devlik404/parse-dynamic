package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"parser-engine/internal/config"
)

func TestApplicationModeDefaultsAndAliases(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "default job", env: map[string]string{}, want: applicationModeJob},
		{name: "job", env: map[string]string{"APP_MODE": "job"}, want: applicationModeJob},
		{name: "http", env: map[string]string{"APP_MODE": " HTTP "}, want: applicationModeHTTP},
		{name: "scheduler", env: map[string]string{"APP_MODE": " scheduler "}, want: applicationModeScheduler},
		{name: "legacy cli", env: map[string]string{"MODE": "cli"}, want: applicationModeJob},
		{name: "legacy http", env: map[string]string{"MODE": "http"}, want: applicationModeHTTP},
		{name: "legacy production", env: map[string]string{"MODE": "production"}, want: applicationModeJob},
		{name: "legacy api remains job", env: map[string]string{"MODE": "api"}, want: applicationModeJob},
		{name: "app mode takes precedence", env: map[string]string{"APP_MODE": "HTTP", "MODE": "production"}, want: applicationModeHTTP},
		{name: "app job takes precedence", env: map[string]string{"APP_MODE": "JOB", "MODE": "HTTP"}, want: applicationModeJob},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := applicationMode(testLookup(test.env))
			if err != nil || got != test.want {
				t.Fatalf("applicationMode() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestApplicationModeRejectsUnknownAndConflictingValues(t *testing.T) {
	for _, env := range []map[string]string{
		{"APP_MODE": "worker"},
		{"APP_MODE": "CLI"},
		{"APP_MODE": "API"},
		{"APP_MODE": ""},
	} {
		if _, err := applicationMode(testLookup(env)); err == nil {
			t.Fatalf("applicationMode(%v) error = nil", env)
		}
	}
}

func TestLoadHTTPRuntimeConfig(t *testing.T) {
	env := validHTTPEnv()
	env["HTTP_ADDR"] = "127.0.0.1:9090"
	env["MINIO_BASE_URL"] = "https://minio.internal.example/gateway"
	env["MINIO_BUCKET_NAME"] = "cashrecon-files"
	env["MINIO_PATH_PREFIX"] = "input/atm-bersama"
	env["MINIO_HTTP_TIMEOUT"] = "40s"
	env["PREVIEW_REQUIRE_AUTH"] = "true"
	env["PREVIEW_BEARER_TOKEN"] = "0123456789abcdef0123456789abcdef"
	env["PREVIEW_DEFAULT_LIMIT"] = "5"
	env["PREVIEW_MAX_LIMIT"] = "25"
	env["PREVIEW_MAX_INPUT_BYTES"] = "8388608"
	env["PREVIEW_MAX_RESPONSE_BYTES"] = "1048576"
	env["PREVIEW_REQUEST_TIMEOUT"] = "12s"
	env["PREVIEW_MAX_CONCURRENCY"] = "7"
	env["HTTP_WRITE_TIMEOUT"] = "45s"
	env["LOG_LEVEL"] = "debug"
	env["LOG_FORMAT"] = "json"

	cfg, err := loadHTTPRuntimeConfig(testLookup(env))
	if err != nil {
		t.Fatalf("loadHTTPRuntimeConfig() error = %v", err)
	}
	if cfg.Address != "127.0.0.1:9090" || cfg.MinIO.BaseURL != "https://minio.internal.example/gateway" || cfg.MinIO.BucketName != "cashrecon-files" {
		t.Fatalf("unexpected HTTP/MinIO config: %+v", cfg)
	}
	if cfg.MinIO.Timeout != 40*time.Second {
		t.Fatalf("MinIO timeout = %v", cfg.MinIO.Timeout)
	}
	if cfg.Preview.DefaultLimit != 5 || cfg.Preview.MaxLimit != 25 {
		t.Fatalf("unexpected preview config: %+v", cfg.Preview)
	}
	if !cfg.Preview.RequireBearerAuth || cfg.Preview.BearerToken != env["PREVIEW_BEARER_TOKEN"] {
		t.Fatalf("unexpected preview security config: %+v", cfg.Preview)
	}
	if len(cfg.Preview.AllowedPathPrefixes) != 1 || cfg.Preview.AllowedPathPrefixes[0] != "input/atm-bersama" {
		t.Fatalf("unexpected MinIO path policy: %+v", cfg.Preview.AllowedPathPrefixes)
	}
	if cfg.Preview.MaxInputBytes != 8388608 || cfg.Preview.RequestTimeout != 12*time.Second || cfg.Preview.MaxConcurrentRequests != 7 {
		t.Fatalf("unexpected preview resource policy: %+v", cfg.Preview)
	}
	if cfg.WriteTimeout != 45*time.Second {
		t.Fatalf("write timeout = %v", cfg.WriteTimeout)
	}
	if cfg.LogLevel != slog.LevelDebug || cfg.LogFormat != "json" {
		t.Fatalf("logging config = %v/%q", cfg.LogLevel, cfg.LogFormat)
	}
	if cfg.Parser.FileType != config.FileTypeSectionedDelimited || cfg.Parser.Delimiter != "|" || cfg.Parser.Sectioned.DataCode != "SB" {
		t.Fatalf("unexpected startup parser config: %+v", cfg.Parser)
	}
}

func TestNewHTTPLoggerSupportsStructuredJSON(t *testing.T) {
	var output bytes.Buffer
	cfg := httpRuntimeConfig{LogLevel: slog.LevelInfo, LogFormat: "json"}
	logger := newHTTPLoggerTo(cfg, &output)
	logger.Info("server_started", "address", ":8083")
	var event map[string]any
	if err := json.Unmarshal(output.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if event["msg"] != "server_started" || event["component"] != "parser_http" || event["address"] != ":8083" {
		t.Fatalf("event = %#v", event)
	}
}

func TestLoadHTTPRuntimeConfigAllowsOptionalAuthAndPathPrefix(t *testing.T) {
	env := validHTTPEnv()
	delete(env, "PREVIEW_BEARER_TOKEN")
	delete(env, "MINIO_PATH_PREFIX")

	cfg, err := loadHTTPRuntimeConfig(testLookup(env))
	if err != nil {
		t.Fatalf("loadHTTPRuntimeConfig() error = %v", err)
	}
	if cfg.Preview.RequireBearerAuth || cfg.Preview.BearerToken != "" || len(cfg.Preview.AllowedPathPrefixes) != 0 {
		t.Fatalf("optional security/path settings were unexpectedly enabled: %+v", cfg.Preview)
	}
}

func TestLoadHTTPRuntimeConfigRequiresTokenOnlyWhenAuthRequired(t *testing.T) {
	env := validHTTPEnv()
	env["PREVIEW_REQUIRE_AUTH"] = "true"

	_, err := loadHTTPRuntimeConfig(testLookup(env))
	if err == nil || !strings.Contains(err.Error(), "PREVIEW_BEARER_TOKEN") {
		t.Fatalf("loadHTTPRuntimeConfig() error = %v", err)
	}
}

func TestLoadHTTPRuntimeConfigRequiresParserConfiguration(t *testing.T) {
	env := map[string]string{
		"MINIO_BASE_URL":    "https://minio.internal.example/gateway",
		"MINIO_BUCKET_NAME": "cashrecon-files",
	}

	_, err := loadHTTPRuntimeConfig(testLookup(env))
	if err == nil || !strings.Contains(err.Error(), "PARSER_FILE_TYPE") {
		t.Fatalf("loadHTTPRuntimeConfig() error = %v", err)
	}
}

func TestLoadHTTPRuntimeConfigRejectsRequestTimeoutBeyondWriteTimeout(t *testing.T) {
	env := validHTTPEnv()
	env["PREVIEW_REQUEST_TIMEOUT"] = "2m"
	env["HTTP_WRITE_TIMEOUT"] = "1m"
	_, err := loadHTTPRuntimeConfig(testLookup(env))
	if err == nil || !strings.Contains(err.Error(), "PREVIEW_REQUEST_TIMEOUT") {
		t.Fatalf("loadHTTPRuntimeConfig() error = %v", err)
	}
}

func TestHTTPServerBaseContextCancelsActiveRequests(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	server := newHTTPServer(ctx, httpRuntimeConfig{}, http.NotFoundHandler())
	requestContext := server.BaseContext(nil)

	cancel()
	select {
	case <-requestContext.Done():
	case <-time.After(time.Second):
		t.Fatal("server base context was not canceled")
	}
}

func TestLoadHTTPRuntimeConfigRejectsInvalidValuesWithoutSecrets(t *testing.T) {
	env := validHTTPEnv()
	secret := "very-secret-value-never-echoed"
	env["MINIO_BASE_URL"] = "https://operator:" + secret + "@minio.internal.example/gateway"
	env["PREVIEW_BEARER_TOKEN"] = secret
	env["PREVIEW_REQUIRE_AUTH"] = "sometimes"
	env["HTTP_ADDR"] = "localhost"
	env["PREVIEW_MAX_LIMIT"] = "many"
	env["MINIO_HTTP_TIMEOUT"] = "later"
	env["HTTP_IDLE_TIMEOUT"] = "forever"
	env["LOG_LEVEL"] = "verbose"
	env["LOG_FORMAT"] = "xml"

	_, err := loadHTTPRuntimeConfig(testLookup(env))
	if err == nil {
		t.Fatal("loadHTTPRuntimeConfig() error = nil")
	}
	message := err.Error()
	for _, key := range []string{"MINIO_BASE_URL", "PREVIEW_REQUIRE_AUTH", "HTTP_ADDR", "PREVIEW_MAX_LIMIT", "MINIO_HTTP_TIMEOUT", "HTTP_IDLE_TIMEOUT", "LOG_LEVEL", "LOG_FORMAT"} {
		if !strings.Contains(message, key) {
			t.Errorf("error %q does not mention %s", message, key)
		}
	}
	if strings.Contains(message, secret) || strings.Contains(message, "very-secret") {
		t.Fatalf("configuration error leaked secret: %v", err)
	}
}

func validHTTPEnv() map[string]string {
	return map[string]string{
		"MINIO_BASE_URL":                    "https://minio.internal.example/gateway",
		"MINIO_BUCKET_NAME":                 "cashrecon-files",
		"PARSER_FILE_TYPE":                  "SECTIONED_DELIMITED",
		"PARSER_DELIMITER":                  "|",
		"PARSER_COLUMNS":                    "record_type,section_key,payload",
		"PARSER_TYPES":                      "string,string,json",
		"PARSER_MAPPING":                    "record_type:record_type,section_key:section_key,payload:payload",
		"PARSER_FILE_HEADER_CODE":           "RH",
		"PARSER_SECTION_HEADER_CODE":        "SH",
		"PARSER_DATA_CODE":                  "SB",
		"PARSER_SECTION_FOOTER_CODE":        "SF",
		"PARSER_FILE_FOOTER_CODE":           "RF",
		"PARSER_RECORD_TYPE_INDEX":          "0",
		"PARSER_SECTION_KEY_INDEX":          "1",
		"PARSER_DYNAMIC_HEADER_START_INDEX": "2",
		"PARSER_DATA_START_INDEX":           "2",
		"PARSER_DUPLICATE_HEADER_POLICY":    "SUFFIX_INDEX",
	}
}

func testLookup(values map[string]string) config.LookupFunc {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
