package parser

import "time"

/*
====================================================
RECORD (DOMAIN OBJECT)
====================================================

Record merepresentasikan 1 baris hasil parsing
yang SUDAH valid menurut rule.

Record:
- immutable (set sekali)
- siap dikirim ke emitter
- tidak tahu DB / MQ / File
*/

type Record struct {
	// Identitas rule
	RuleID string

	// Nama section tempat record berasal
	Section string

	// Nomor baris di file (1-based)
	LineNumber int

	// Data hasil parsing
	Fields map[string]interface{}

	// Waktu parsing
	ParsedAt time.Time
}
