package sqlrepo

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/denisenkom/go-mssqldb"
	mysql "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"

	"parser-engine/internal/config"
)

const (
	defaultConnectTimeout = 10 * time.Second
	defaultMaxOpenConns   = 10
	defaultMaxIdleConns   = 5
	defaultConnLifetime   = 30 * time.Minute
	defaultConnIdleTime   = 5 * time.Minute
)

// Open creates and verifies a pooled database connection. DBConfig.DSN, when
// present, is an opaque override (normally sourced from DB_DSN by the config
// layer); it is never logged or included in an error returned by this package.
func Open(cfg config.DBConfig) (*SQLRepository, error) {
	return OpenContext(context.Background(), cfg)
}

// OpenContext is Open with caller-controlled cancellation. ConnectTimeout is
// still applied so a caller without a deadline cannot block startup forever.
func OpenContext(ctx context.Context, cfg config.DBConfig) (*SQLRepository, error) {
	d, err := resolveDialect(cfg.Driver)
	if err != nil {
		return nil, err
	}
	builder, err := newInsertBuilder(cfg, d)
	if err != nil {
		return nil, err
	}
	dsn, err := connectionString(cfg, d)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open(d.driverName, dsn)
	if err != nil {
		return nil, redactConnectionError("open", err, cfg)
	}
	db.SetMaxOpenConns(defaultMaxOpenConns)
	db.SetMaxIdleConns(defaultMaxIdleConns)
	db.SetConnMaxLifetime(defaultConnLifetime)
	db.SetConnMaxIdleTime(defaultConnIdleTime)

	timeout := cfg.ConnectTimeout
	if timeout <= 0 {
		timeout = defaultConnectTimeout
	}
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		closeErr := db.Close()
		pingErr := redactConnectionError("ping", err, cfg)
		if closeErr != nil {
			return nil, fmt.Errorf("%w; close failed: %v", pingErr, redactConnectionError("close", closeErr, cfg))
		}
		return nil, pingErr
	}

	return &SQLRepository{
		db:        db,
		dialect:   d,
		builder:   builder,
		batchSize: normalizedBatchSize(cfg.BatchSize),
	}, nil
}

func connectionString(cfg config.DBConfig, d dialect) (string, error) {
	if strings.TrimSpace(cfg.DSN) != "" {
		return cfg.DSN, nil
	}
	if strings.TrimSpace(cfg.Host) == "" {
		return "", fmt.Errorf("DB_HOST is required when DB_DSN is empty")
	}
	if strings.TrimSpace(cfg.Name) == "" {
		return "", fmt.Errorf("DB_NAME is required when DB_DSN is empty")
	}

	port := cfg.Port
	if port == 0 {
		port = defaultPort(d.name)
	}
	if port < 1 || port > 65_535 {
		return "", fmt.Errorf("DB_PORT must be between 1 and 65535")
	}
	hostPort := net.JoinHostPort(cfg.Host, strconv.Itoa(port))

	switch d.name {
	case dialectPostgres:
		return postgresConnectionString(cfg, hostPort), nil
	case dialectMySQL:
		return mysqlConnectionString(cfg, hostPort), nil
	case dialectSQLServer:
		return sqlServerConnectionString(cfg, hostPort), nil
	default:
		return "", fmt.Errorf("unsupported SQL dialect %q", d.name)
	}
}

func postgresConnectionString(cfg config.DBConfig, hostPort string) string {
	connectionURL := &url.URL{
		Scheme: "postgres",
		Host:   hostPort,
		Path:   "/" + cfg.Name,
	}
	connectionURL.User = urlUser(cfg.User, cfg.Password)
	query := connectionURL.Query()
	if cfg.SSLMode != "" {
		query.Set("sslmode", cfg.SSLMode)
	}
	if cfg.ConnectTimeout > 0 {
		seconds := int64((cfg.ConnectTimeout + time.Second - 1) / time.Second)
		query.Set("connect_timeout", strconv.FormatInt(seconds, 10))
	}
	connectionURL.RawQuery = query.Encode()
	return connectionURL.String()
}

func mysqlConnectionString(cfg config.DBConfig, hostPort string) string {
	mysqlConfig := mysql.NewConfig()
	mysqlConfig.User = cfg.User
	mysqlConfig.Passwd = cfg.Password
	mysqlConfig.Net = "tcp"
	mysqlConfig.Addr = hostPort
	mysqlConfig.DBName = cfg.Name
	mysqlConfig.ParseTime = true
	mysqlConfig.Params = make(map[string]string)
	if cfg.ConnectTimeout > 0 {
		mysqlConfig.Timeout = cfg.ConnectTimeout
	}
	switch strings.ToLower(strings.TrimSpace(cfg.SSLMode)) {
	case "", "disable", "disabled", "false":
		mysqlConfig.TLSConfig = "false"
	case "require", "required", "true":
		mysqlConfig.TLSConfig = "true"
	case "skip-verify", "skip_verify":
		mysqlConfig.TLSConfig = "skip-verify"
	case "preferred":
		mysqlConfig.TLSConfig = "preferred"
	default:
		// Custom TLS configurations registered with the MySQL driver are valid
		// names too, so preserve the configured value.
		mysqlConfig.TLSConfig = cfg.SSLMode
	}
	return mysqlConfig.FormatDSN()
}

func sqlServerConnectionString(cfg config.DBConfig, hostPort string) string {
	connectionURL := &url.URL{
		Scheme: "sqlserver",
		Host:   hostPort,
		User:   urlUser(cfg.User, cfg.Password),
	}
	query := connectionURL.Query()
	query.Set("database", cfg.Name)
	if cfg.ConnectTimeout > 0 {
		seconds := int64((cfg.ConnectTimeout + time.Second - 1) / time.Second)
		query.Set("connection timeout", strconv.FormatInt(seconds, 10))
	}
	switch strings.ToLower(strings.TrimSpace(cfg.SSLMode)) {
	case "disable", "disabled", "false":
		query.Set("encrypt", "disable")
	case "require", "required", "true":
		query.Set("encrypt", "true")
	case "skip-verify", "skip_verify":
		query.Set("encrypt", "true")
		query.Set("TrustServerCertificate", "true")
	}
	connectionURL.RawQuery = query.Encode()
	return connectionURL.String()
}

func urlUser(user, password string) *url.Userinfo {
	if user == "" {
		return nil
	}
	if password == "" {
		return url.User(user)
	}
	return url.UserPassword(user, password)
}

func defaultPort(name dialectName) int {
	switch name {
	case dialectPostgres:
		return 5432
	case dialectMySQL:
		return 3306
	case dialectSQLServer:
		return 1433
	default:
		return 0
	}
}

func redactConnectionError(operation string, err error, cfg config.DBConfig) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	secrets := []string{
		cfg.DSN,
		cfg.Password,
		url.QueryEscape(cfg.Password),
		url.PathEscape(cfg.Password),
	}
	// Replace longer secrets first in case the password is part of the DSN.
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	return fmt.Errorf("database %s failed: %s", operation, message)
}
