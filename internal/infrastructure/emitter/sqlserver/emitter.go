// Package sqlserver contains the optional legacy JSON-staging emitter. The
// universal production path uses internal/repository/sqlrepo instead.
package sqlserver

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"parser-engine/internal/domain/parser"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Target makes every legacy staging identifier explicit rather than embedding
// a business table or schema in source code.
type Target struct {
	Schema           string
	Table            string
	RuleIDColumn     string
	SectionColumn    string
	LineNumberColumn string
	PayloadColumn    string
	ParsedAtColumn   string
}

type Emitter struct {
	db        *sql.DB
	statement string
}

func NewEmitter(db *sql.DB, target Target) (*Emitter, error) {
	if db == nil {
		return nil, errors.New("database is required")
	}
	identifiers := []string{
		target.Table,
		target.RuleIDColumn,
		target.SectionColumn,
		target.LineNumberColumn,
		target.PayloadColumn,
		target.ParsedAtColumn,
	}
	if target.Schema != "" {
		identifiers = append(identifiers, target.Schema)
	}
	for _, identifier := range identifiers {
		if !identifierPattern.MatchString(identifier) {
			return nil, fmt.Errorf("unsafe SQL Server identifier %q", identifier)
		}
	}
	table := quote(target.Table)
	if target.Schema != "" {
		table = quote(target.Schema) + "." + table
	}
	columns := []string{
		quote(target.RuleIDColumn),
		quote(target.SectionColumn),
		quote(target.LineNumberColumn),
		quote(target.PayloadColumn),
		quote(target.ParsedAtColumn),
	}
	return &Emitter{
		db: db,
		statement: fmt.Sprintf(
			"INSERT INTO %s (%s) VALUES (@p1,@p2,@p3,@p4,@p5)",
			table,
			strings.Join(columns, ","),
		),
	}, nil
}

func (e *Emitter) Emit(record parser.Record) error {
	if e == nil || e.db == nil {
		return errors.New("legacy SQL Server emitter is not configured")
	}
	payload, err := json.Marshal(record.Fields)
	if err != nil {
		return err
	}
	_, err = e.db.Exec(
		e.statement,
		record.RuleID,
		record.Section,
		record.LineNumber,
		string(payload),
		record.ParsedAt,
	)
	return err
}

func quote(identifier string) string { return "[" + identifier + "]" }
