package main

import (
	"strings"
	"testing"
	"time"
)

func validSchedulerEnv() map[string]string {
	return map[string]string{
		"MINIO_BASE_URL":        "https://minio.internal.example/gateway",
		"MINIO_BUCKET_NAME":     "cashrecon-files",
		"SS_RC_HOST":            "sqlserver.internal",
		"SS_RC_PORT":            "1433",
		"SS_RC_USER":            "parser-user",
		"SS_RC_PASSWORD":        "parser-password",
		"SS_RC_DB_PARAM_ENGINE": "ParamEngine",
		"SS_RC_DB_RECON_CONFIG": "ReconConfig",
		"SCH_ENC_KEY":           "encryption-key",
		"SS_TR_PORT":            "1433",
	}
}

func TestLoadSchedulerRuntimeConfigDoesNotRequireParserEnv(t *testing.T) {
	env := validSchedulerEnv()
	env["HTTP_ADDR"] = "127.0.0.1:8088"
	env["MINIO_PATH_PREFIX"] = "PRODUCT/input"
	env["DYNAMIC_REQUEST_TIMEOUT"] = "4m"
	env["HTTP_WRITE_TIMEOUT"] = "5m"
	env["DYNAMIC_BATCH_SIZE"] = "250"
	env["DYNAMIC_MAX_CONCURRENCY"] = "3"
	cfg, err := loadSchedulerRuntimeConfig(testLookup(env))
	if err != nil {
		t.Fatalf("loadSchedulerRuntimeConfig() error = %v", err)
	}
	if cfg.Address != "127.0.0.1:8088" || cfg.Store.ParamDatabase != "ParamEngine" {
		t.Fatalf("runtime config = %#v", cfg)
	}
	if cfg.Handler.RequestTimeout != 4*time.Minute || cfg.Store.BatchSize != 250 || cfg.Handler.MaxConcurrentRequests != 3 {
		t.Fatalf("runtime policies = %#v / %#v", cfg.Handler, cfg.Store)
	}
	if len(cfg.Job.AllowedPathPrefixes) != 1 || cfg.Job.AllowedPathPrefixes[0] != "PRODUCT/input" {
		t.Fatalf("path prefixes = %#v", cfg.Job.AllowedPathPrefixes)
	}
}

func TestLoadSchedulerRuntimeConfigRejectsMissingDatabaseRouting(t *testing.T) {
	env := validSchedulerEnv()
	delete(env, "SS_RC_DB_PARAM_ENGINE")
	_, err := loadSchedulerRuntimeConfig(testLookup(env))
	if err == nil || !strings.Contains(err.Error(), "SS_RC_DB_PARAM_ENGINE") {
		t.Fatalf("loadSchedulerRuntimeConfig() error = %v", err)
	}
}

func TestLoadSchedulerRuntimeConfigDoesNotLeakSecrets(t *testing.T) {
	env := validSchedulerEnv()
	secret := "secret-that-must-not-leak"
	env["SS_RC_PASSWORD"] = secret
	env["DYNAMIC_DB_SSL_MODE"] = "unsafe"
	_, err := loadSchedulerRuntimeConfig(testLookup(env))
	if err == nil || !strings.Contains(err.Error(), "DYNAMIC_DB_SSL_MODE") {
		t.Fatalf("loadSchedulerRuntimeConfig() error = %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("configuration error leaked password: %v", err)
	}
}

func TestLoadSchedulerRuntimeConfigRejectsShortBearerToken(t *testing.T) {
	env := validSchedulerEnv()
	env["DYNAMIC_REQUIRE_AUTH"] = "true"
	env["DYNAMIC_BEARER_TOKEN"] = "short"
	_, err := loadSchedulerRuntimeConfig(testLookup(env))
	if err == nil || !strings.Contains(err.Error(), "at least 32 bytes") {
		t.Fatalf("loadSchedulerRuntimeConfig() error = %v", err)
	}
}
