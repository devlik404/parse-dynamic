package parser

import (
	"context"
	"fmt"
	"io"

	"github.com/xuri/excelize/v2"

	"parser-engine/internal/config"
)

type xlsxParser struct {
	cfg config.ParserConfig
}

func (p *xlsxParser) Parse(ctx context.Context, r io.Reader, fileName string, yield YieldSource) (err error) {
	if r == nil {
		return fmt.Errorf("parse %s: reader is nil", fileName)
	}
	if yield == nil {
		return fmt.Errorf("parse %s: yield callback is nil", fileName)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("parse %s XLSX: decoder failed", fileName)
		}
	}()

	file, _, cleanup, err := spoolDocument(ctx, r, p.cfg.MaxDocumentBytes)
	if err != nil {
		return fmt.Errorf("parse %s XLSX: %w", fileName, err)
	}
	defer cleanup()
	maxDocument := int64(configuredMaxDocumentBytes(p.cfg.MaxDocumentBytes))
	workbook, err := excelize.OpenFile(file.Name(), excelize.Options{
		UnzipSizeLimit:    maxDocument,
		UnzipXMLSizeLimit: min(maxDocument, int64(16*1024*1024)),
	})
	if err != nil {
		return fmt.Errorf("parse %s XLSX: %w", fileName, err)
	}
	defer workbook.Close()

	sheetName, err := selectXLSXSheet(workbook, p.cfg.SpreadsheetSheet)
	if err != nil {
		return fmt.Errorf("parse %s XLSX: %w", fileName, err)
	}
	rows, err := workbook.Rows(sheetName)
	if err != nil {
		return fmt.Errorf("parse %s XLSX worksheet %q: %w", fileName, sheetName, err)
	}
	defer rows.Close()
	emitter := spreadsheetEmitter{cfg: p.cfg, fileName: fileName, sheetName: sheetName}
	var rowNumber int64
	for rows.Next() {
		rowNumber++
		if err := checkContext(ctx); err != nil {
			return err
		}
		fields, err := rows.Columns()
		if err != nil {
			return fmt.Errorf("parse %s XLSX worksheet %q row %d: %w", fileName, sheetName, rowNumber, err)
		}
		if err := emitter.emit(ctx, rowNumber, fields, yield); err != nil {
			return err
		}
	}
	if err := rows.Error(); err != nil {
		return fmt.Errorf("parse %s XLSX worksheet %q: %w", fileName, sheetName, err)
	}
	return emitter.finish()
}

func selectXLSXSheet(workbook *excelize.File, selector string) (string, error) {
	if index, numeric := spreadsheetSheetIndex(selector); numeric {
		name := workbook.GetSheetName(index)
		if name == "" {
			return "", fmt.Errorf("worksheet index %d was not found", index)
		}
		return name, nil
	}
	index, err := workbook.GetSheetIndex(selector)
	if err != nil || index < 0 {
		return "", fmt.Errorf("worksheet %q was not found", selector)
	}
	return selector, nil
}
