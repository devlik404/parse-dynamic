package parser

import (
	"context"
	"fmt"
	"io"
	"strings"

	"parser-engine/internal/config"
	"parser-engine/internal/model"
)

type rawParser struct {
	cfg config.ParserConfig
}

func (p *rawParser) Parse(ctx context.Context, r io.Reader, fileName string, yield YieldSource) error {
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
		if err := yield(model.SourceRecord{
			File:         fileName,
			RecordNumber: recordNumber,
			LineNumber:   lineNumber,
			Values:       map[string]any{"0": line, "raw": line, "value": line},
			Raw:          line,
		}); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("parse %s at line %d: %w", fileName, lineNumber+1, err)
	}
	return nil
}
