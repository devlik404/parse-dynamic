package sqlserver

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/denisenkom/go-mssqldb"
)

/*
====================================================
SQL SERVER CONNECTION
====================================================

Tanggung jawab:
- Membuat & menguji koneksi DB
- Tidak tahu rule
- Tidak tahu engine
*/

type Config struct {
	Host     string
	Port     int
	User     string
	Password string
	Database string
}

// NewConnection
// -------------
// Factory untuk koneksi SQL Server
func NewConnection(cfg Config) (*sql.DB, error) {
	if cfg.Host == "" || cfg.Database == "" {
		return nil, fmt.Errorf("invalid db config")
	}

	dsn := fmt.Sprintf(
		"sqlserver://%s:%s@%s:%d?database=%s",
		cfg.User,
		cfg.Password,
		cfg.Host,
		cfg.Port,
		cfg.Database,
	)

	db, err := sql.Open("sqlserver", dsn)
	if err != nil {
		return nil, err
	}

	// =========================
	// Connection pool tuning
	// =========================
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(30 * time.Minute)

	// =========================
	// Test connection
	// =========================
	if err := db.Ping(); err != nil {
		return nil, err
	}

	return db, nil
}
