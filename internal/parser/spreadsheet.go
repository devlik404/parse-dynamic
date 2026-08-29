package parser

import (
	"context"
	"fmt"
	"strconv"

	"parser-engine/internal/config"
	"parser-engine/internal/model"
)

type spreadsheetEmitter struct {
	cfg          config.ParserConfig
	fileName     string
	sheetName    string
	header       []string
	recordNumber int64
	headerSeen   bool
}

func (e *spreadsheetEmitter) emit(ctx context.Context, rowNumber int64, fields []string, yield YieldSource) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if e.cfg.SkipEmptyLine && allFieldsEmpty(fields) {
		return nil
	}
	maxFields := configuredMaxFields(e.cfg.MaxFields)
	tooComplex := len(fields) > maxFields
	tooLarge := spreadsheetRowBytes(fields, configuredMaxRecordBytes(e.cfg.MaxRecordBytes)) < 0
	if tooComplex || tooLarge {
		return e.reject(rowNumber, tooComplex, yield)
	}

	if e.cfg.HasHeader && !e.headerSeen {
		header, err := normalizeHeader(fields)
		if err != nil {
			return fmt.Errorf("parse %s worksheet %q header: %w", e.fileName, e.sheetName, err)
		}
		if err := validateHeaderMappings(header, e.cfg.Mappings); err != nil {
			return fmt.Errorf("parse %s worksheet %q header: %w", e.fileName, e.sheetName, err)
		}
		e.header = header
		e.headerSeen = true
		return nil
	}

	e.recordNumber++
	expected := expectedFieldCount(e.cfg, e.header)
	if len(fields) < expected {
		padded := make([]string, expected)
		copy(padded, fields)
		fields = padded
	}
	if countMismatch(e.cfg, expected, len(fields)) {
		recordErr := parseError(e.fileName, e.recordNumber, rowNumber, "", "COLUMN_COUNT_MISMATCH",
			fmt.Sprintf("expected %d columns, got %d", expected, len(fields)), append([]string(nil), fields...))
		return yield(model.SourceRecord{File: e.fileName, RecordNumber: e.recordNumber, LineNumber: rowNumber, Raw: append([]string(nil), fields...), Error: recordErr})
	}

	values := fieldsToValues(fields, e.header, e.cfg.Columns)
	values["_sheet"] = e.sheetName
	values["_row"] = rowNumber
	return yield(model.SourceRecord{
		File: e.fileName, RecordNumber: e.recordNumber, LineNumber: rowNumber,
		Values: values, Raw: append([]string(nil), fields...),
	})
}

func (e *spreadsheetEmitter) reject(rowNumber int64, tooComplex bool, yield YieldSource) error {
	code := "RECORD_TOO_LARGE"
	message := "spreadsheet row exceeds PARSER_MAX_RECORD_BYTES"
	raw := oversizedRawMarker
	if tooComplex {
		code = "RECORD_TOO_COMPLEX"
		message = "spreadsheet row exceeds PARSER_MAX_FIELDS"
		raw = tooComplexRawMarker
	}
	if e.cfg.HasHeader && !e.headerSeen {
		return fmt.Errorf("parse %s worksheet %q header: %s", e.fileName, e.sheetName, message)
	}
	e.recordNumber++
	recordErr := parseError(e.fileName, e.recordNumber, rowNumber, "", code, message, raw)
	return yield(model.SourceRecord{File: e.fileName, RecordNumber: e.recordNumber, LineNumber: rowNumber, Raw: raw, Error: recordErr})
}

func (e *spreadsheetEmitter) finish() error {
	if e.cfg.HasHeader && !e.headerSeen {
		return fmt.Errorf("parse %s worksheet %q: header is missing", e.fileName, e.sheetName)
	}
	return nil
}

func spreadsheetRowBytes(fields []string, limit int) int {
	total := 0
	for _, field := range fields {
		if len(field) > limit-total {
			return -1
		}
		total += len(field)
	}
	return total
}

func spreadsheetSheetIndex(selector string) (int, bool) {
	index, err := strconv.Atoi(selector)
	return index, err == nil
}
