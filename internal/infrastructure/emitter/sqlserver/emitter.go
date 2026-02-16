package sqlserver

import (
	"database/sql"
	"encoding/json"

	"parser-engine/internal/domain/parser"
)

/*
====================================================
SQL SERVER RECORD EMITTER
====================================================

Tanggung jawab:
- Menyimpan hasil parsing ke tabel staging
- Tidak tahu rule
- Tidak tahu engine
*/

type Emitter struct {
	db *sql.DB
}

// NewEmitter
// ----------
// Constructor yang dipanggil di main.go
func NewEmitter(db *sql.DB) *Emitter {
	return &Emitter{
		db: db,
	}
}

// Emit
// ----
// Implement application port: RecordEmitter
func (e *Emitter) Emit(r parser.Record) error {
	payload, err := json.Marshal(r.Fields)
	if err != nil {
		return err
	}

	_, err = e.db.Exec(`
		INSERT INTO parsed_record_staging
			(rule_id, section_name, line_number, payload, parsed_at)
		VALUES
			(@p1, @p2, @p3, @p4, @p5)
	`,
		r.RuleID,
		r.Section,
		r.LineNumber,
		string(payload),
		r.ParsedAt,
	)

	return err
}
