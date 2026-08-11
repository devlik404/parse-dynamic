package parser

import (
	"context"
	"fmt"
	"io"
	"strings"

	"parser-engine/internal/config"
	"parser-engine/internal/model"
)

type fixedWidthParser struct {
	cfg  config.ParserConfig
	unit string
}

func (p *fixedWidthParser) Parse(ctx context.Context, r io.Reader, fileName string, yield YieldSource) error {
	if r == nil {
		return fmt.Errorf("parse %s: reader is nil", fileName)
	}
	if yield == nil {
		return fmt.Errorf("parse %s: yield callback is nil", fileName)
	}

	scanner := newLineScanner(r, p.cfg.MaxRecordBytes)
	var lineNumber int64
	var recordNumber int64
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
		recordNumber++
		values := make(map[string]any, len(p.cfg.FixedWidthFields)*2)
		var recordErr *model.RecordError
		if p.unit == "RUNE" {
			runes := []rune(line)
			for index, field := range p.cfg.FixedWidthFields {
				if field.Start < 0 || field.Length <= 0 || field.Start > len(runes) || field.Length > len(runes)-field.Start {
					recordErr = parseError(fileName, recordNumber, lineNumber, field.Name, "FIXED_WIDTH_OUT_OF_RANGE",
						fmt.Sprintf("configured field range exceeds record length %d runes", len(runes)), line)
					break
				}
				end := field.Start + field.Length
				value := string(runes[field.Start:end])
				values[field.Name] = value
				values[fmt.Sprintf("%d", index)] = value
			}
		} else {
			bytes := []byte(line)
			for index, field := range p.cfg.FixedWidthFields {
				if field.Start < 0 || field.Length <= 0 || field.Start > len(bytes) || field.Length > len(bytes)-field.Start {
					recordErr = parseError(fileName, recordNumber, lineNumber, field.Name, "FIXED_WIDTH_OUT_OF_RANGE",
						fmt.Sprintf("configured field range exceeds record length %d bytes", len(bytes)), line)
					break
				}
				end := field.Start + field.Length
				value := string(bytes[field.Start:end])
				values[field.Name] = value
				values[fmt.Sprintf("%d", index)] = value
			}
		}

		source := model.SourceRecord{File: fileName, RecordNumber: recordNumber, LineNumber: lineNumber, Values: values, Raw: line, Error: recordErr}
		if err := yield(source); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("parse %s at line %d: %w", fileName, lineNumber+1, err)
	}
	return nil
}
