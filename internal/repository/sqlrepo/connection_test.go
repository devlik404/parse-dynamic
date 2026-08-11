package sqlrepo

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"parser-engine/internal/config"
)

func TestConnectionStringUsesOpaqueDSNOverride(t *testing.T) {
	d, err := resolveDialect("postgres")
	if err != nil {
		t.Fatal(err)
	}
	want := "postgres://opaque:not-inspected@example.invalid/db?sslmode=require"
	got, err := connectionString(config.DBConfig{DSN: want}, d)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("DSN = %q, want exact override %q", got, want)
	}
}

func TestGeneratedConnectionStringsEscapeCredentials(t *testing.T) {
	base := config.DBConfig{
		Host:           "db.internal",
		User:           "user@tenant",
		Password:       "p@ss:/?#& word",
		Name:           "parser_data",
		ConnectTimeout: 1500 * time.Millisecond,
	}

	t.Run("postgres", func(t *testing.T) {
		d, _ := resolveDialect("postgres")
		cfg := base
		cfg.SSLMode = "require"
		got, err := connectionString(cfg, d)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := url.Parse(got)
		if err != nil {
			t.Fatal(err)
		}
		password, _ := parsed.User.Password()
		if parsed.User.Username() != base.User || password != base.Password {
			t.Fatalf("credentials did not round trip safely: %q", got)
		}
		if parsed.Port() != "5432" || parsed.Query().Get("sslmode") != "require" || parsed.Query().Get("connect_timeout") != "2" {
			t.Fatalf("unexpected postgres DSN: %s", got)
		}
	})

	t.Run("mysql", func(t *testing.T) {
		d, _ := resolveDialect("mysql")
		cfg := base
		cfg.SSLMode = "required"
		got, err := connectionString(cfg, d)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := mysql.ParseDSN(got)
		if err != nil {
			t.Fatal(err)
		}
		if parsed.User != base.User || parsed.Passwd != base.Password || parsed.Addr != "db.internal:3306" || parsed.DBName != base.Name {
			t.Fatalf("MySQL DSN did not round trip: %#v", parsed)
		}
		if parsed.TLSConfig != "true" || parsed.Timeout != base.ConnectTimeout || !parsed.ParseTime {
			t.Fatalf("unexpected MySQL DSN options: %#v", parsed)
		}
	})

	t.Run("sqlserver", func(t *testing.T) {
		d, _ := resolveDialect("sqlserver")
		cfg := base
		cfg.SSLMode = "skip-verify"
		got, err := connectionString(cfg, d)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := url.Parse(got)
		if err != nil {
			t.Fatal(err)
		}
		password, _ := parsed.User.Password()
		if parsed.User.Username() != base.User || password != base.Password {
			t.Fatalf("credentials did not round trip safely: %q", got)
		}
		if parsed.Port() != "1433" || parsed.Query().Get("database") != base.Name || parsed.Query().Get("encrypt") != "true" || parsed.Query().Get("TrustServerCertificate") != "true" {
			t.Fatalf("unexpected SQL Server DSN: %s", got)
		}
	})
}

func TestConnectionStringValidatesAddressWithoutLeakingPassword(t *testing.T) {
	d, _ := resolveDialect("postgres")
	_, err := connectionString(config.DBConfig{Host: "", Name: "db", Password: "secret"}, d)
	if err == nil || !strings.Contains(err.Error(), "DB_HOST") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unexpected error: %v", err)
	}
	_, err = connectionString(config.DBConfig{Host: "host", Port: 70_000, Name: "db", Password: "secret"}, d)
	if err == nil || !strings.Contains(err.Error(), "DB_PORT") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRedactConnectionErrorRemovesDSNAndEncodedPassword(t *testing.T) {
	cfg := config.DBConfig{
		DSN:      "postgres://user:s%40cret@example.invalid/db",
		Password: "s@cret",
	}
	err := redactConnectionError("ping", errors.New("bad postgres://user:s%40cret@example.invalid/db password s@cret or s%40cret"), cfg)
	message := err.Error()
	for _, secret := range []string{cfg.DSN, cfg.Password, url.QueryEscape(cfg.Password)} {
		if strings.Contains(message, secret) {
			t.Fatalf("error leaked %q: %s", secret, message)
		}
	}
}
