package config

import (
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	Mode string

	HTTPPort string

	MaxErrorCount int
	SkipEmptyLine bool

	LogLevel string

	DB DBConfig
}

type DBConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	Name     string
}

// LoadConfig
// ----------
// Load .env → environment
func LoadConfig() (*Config, error) {
	// load .env (ignore error in prod)
	_ = godotenv.Load()

	cfg := &Config{
		Mode:          getEnv("MODE", "cli"),
		HTTPPort:      getEnv("HTTP_PORT", "8080"),
		LogLevel:      getEnv("LOG_LEVEL", "INFO"),
		MaxErrorCount: getEnvAsInt("MAX_ERROR_COUNT", 0),
		SkipEmptyLine: getEnvAsBool("SKIP_EMPTY_LINE", true),
		DB: DBConfig{
			Host:     getEnv("DB_HOST", ""),
			Port:     getEnvAsInt("DB_PORT", 1433),
			User:     getEnv("DB_USER", ""),
			Password: getEnv("DB_PASSWORD", ""),
			Name:     getEnv("DB_NAME", ""),
		},
	}

	return cfg, nil
}

// =========================
// Helpers
// =========================

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func getEnvAsInt(key string, defaultVal int) int {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal
	}
	if i, err := strconv.Atoi(val); err == nil {
		return i
	}
	return defaultVal
}

func getEnvAsBool(key string, defaultVal bool) bool {
	val := strings.ToLower(os.Getenv(key))
	if val == "true" || val == "1" || val == "yes" {
		return true
	}
	if val == "false" || val == "0" || val == "no" {
		return false
	}
	return defaultVal
}
