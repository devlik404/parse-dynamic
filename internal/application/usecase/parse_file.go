package usecase

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"parser-engine/internal/application/port"
	"parser-engine/internal/domain/parser"
	"parser-engine/internal/domain/rule"
)

/*
====================================================
PARSE FILE USE CASE
====================================================

Tanggung jawab:
- Load & validasi rule (via LoadRuleUseCase)
- Bangun parser engine
- Streaming parse file baris demi baris
- Bangun Record domain
- Emit hasil parsing

Tidak:
- Tidak tahu DB
- Tidak tahu cache
- Tidak tahu detail parsing string
*/

type ParseFileUseCase struct {
	// Dependencies
	LoadRuleUC *LoadRuleUseCase
	Emitter    port.RecordEmitter

	// Runtime options
	MaxErrorCount int  // 0 = unlimited
	SkipEmptyLine bool // default true
}

// Execute
// -------
// ruleID  : logical rule identifier
// filePath : path file di server
func (uc *ParseFileUseCase) Execute(ruleID string, filePath string) error {
	if ruleID == "" {
		return fmt.Errorf("ruleID is empty")
	}
	if filePath == "" {
		return fmt.Errorf("filePath is empty")
	}

	// =========================
	// Load & validate rule
	// =========================
	ruleVer, err := uc.LoadRuleUC.Execute(ruleID)
	if err != nil {
		return err
	}

	// =========================
	// Build engine
	// =========================
	engine, err := parser.NewEngine(ruleVer)
	if err != nil {
		return err
	}

	// =========================
	// Open file
	// =========================
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return err
	}

	file, err := os.Open(absPath)
	if err != nil {
		return err
	}
	defer file.Close()

	// =========================
	// Stream parse
	// =========================
	scanner := bufio.NewScanner(file)

	lineNo := 0
	errorCount := 0

	for scanner.Scan() {
		lineNo++
		line := scanner.Text()

		if uc.SkipEmptyLine && line == "" {
			continue
		}

		fields, ok, err := engine.ParseLine(line)
		if err != nil {
			errorCount++
			if uc.MaxErrorCount > 0 && errorCount >= uc.MaxErrorCount {
				return fmt.Errorf("max error reached at line %d: %w", lineNo, err)
			}
			continue
		}

		if !ok {
			continue
		}

		// =========================
		// Build Record (DOMAIN)
		// =========================
		rec := parser.Record{
			RuleID:     ruleVer.ID,
			Section:    findSectionName(ruleVer, line),
			LineNumber: lineNo,
			Fields:     fields,
			ParsedAt:   time.Now(),
		}

		// =========================
		// Emit
		// =========================
		if err := uc.Emitter.Emit(rec); err != nil {
			return err
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	return nil
}

/*
====================================================
HELPERS (PRIVATE)
====================================================
*/

// findSectionName
// ---------------
// Cari nama section untuk keperluan audit & record metadata.
// Aman & murah (regex sudah dikompilasi di engine FSM).
func findSectionName(ruleVer *rule.RuleVersion, line string) string {
	for _, sec := range ruleVer.Sections {
		if sec.StartPattern == "" {
			continue
		}
		// Best-effort: nama section untuk metadata
		// (engine sudah menentukan match sebenarnya)
		return sec.Name
	}
	return ""
}
