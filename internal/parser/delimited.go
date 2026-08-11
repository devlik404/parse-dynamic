package parser

import (
	"context"
	"fmt"
	"io"
	"strings"

	"parser-engine/internal/config"
	"parser-engine/internal/model"
)

type delimitedParser struct {
	cfg config.ParserConfig
}

func (p *delimitedParser) Parse(ctx context.Context, r io.Reader, fileName string, yield YieldSource) error {
	if r == nil {
		return fmt.Errorf("parse %s: reader is nil", fileName)
	}
	if yield == nil {
		return fmt.Errorf("parse %s: yield callback is nil", fileName)
	}

	scanner := newLineScanner(r, p.cfg.MaxRecordBytes)
	var lineNumber int64
	var recordNumber int64
	var header []string

	for scanner.Scan() {
		lineNumber++
		if err := checkContext(ctx); err != nil {
			return err
		}

		line := scanner.Text()
		if lineNumber == 1 {
			line = strings.TrimPrefix(line, "\ufeff")
		}
		if p.cfg.SkipEmptyLine && strings.TrimSpace(line) == "" {
			continue
		}
		maxFields := configuredMaxFields(p.cfg.MaxFields)
		if strings.Count(line, p.cfg.Delimiter)+1 > maxFields {
			if p.cfg.HasHeader && header == nil {
				return fmt.Errorf("parse %s header: field count exceeds PARSER_MAX_FIELDS", fileName)
			}
			recordNumber++
			recordErr := parseError(fileName, recordNumber, lineNumber, "", "RECORD_TOO_COMPLEX",
				"logical record exceeds PARSER_MAX_FIELDS", tooComplexRawMarker)
			if err := yield(model.SourceRecord{
				File: fileName, RecordNumber: recordNumber, LineNumber: lineNumber,
				Raw: tooComplexRawMarker, Error: recordErr,
			}); err != nil {
				return err
			}
			continue
		}
		fields := strings.Split(line, p.cfg.Delimiter)
		if p.cfg.HasHeader && header == nil {
			var err error
			header, err = normalizeHeader(fields)
			if err != nil {
				return fmt.Errorf("parse %s header: %w", fileName, err)
			}
			if err := validateHeaderMappings(header, p.cfg.Mappings); err != nil {
				return fmt.Errorf("parse %s header: %w", fileName, err)
			}
			continue
		}

		recordNumber++
		expected := expectedFieldCount(p.cfg, header)
		if countMismatch(p.cfg, expected, len(fields)) {
			recordErr := parseError(fileName, recordNumber, lineNumber, "", "COLUMN_COUNT_MISMATCH",
				fmt.Sprintf("expected %d columns, got %d", expected, len(fields)), line)
			if err := yield(model.SourceRecord{File: fileName, RecordNumber: recordNumber, LineNumber: lineNumber, Raw: line, Error: recordErr}); err != nil {
				return err
			}
			continue
		}

		if err := yield(model.SourceRecord{
			File:         fileName,
			RecordNumber: recordNumber,
			LineNumber:   lineNumber,
			Values:       fieldsToValues(fields, header, p.cfg.Columns),
			Raw:          line,
		}); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("parse %s at line %d: %w", fileName, lineNumber+1, err)
	}
	if p.cfg.HasHeader && header == nil {
		return fmt.Errorf("parse %s: header is missing", fileName)
	}
	return nil
}
