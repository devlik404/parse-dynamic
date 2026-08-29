package parser

import (
	"context"
	"fmt"
	"io"

	xlslib "github.com/extrame/xls"

	"parser-engine/internal/config"
)

type xlsParser struct {
	cfg config.ParserConfig
}

func (p *xlsParser) Parse(ctx context.Context, r io.Reader, fileName string, yield YieldSource) (err error) {
	if r == nil {
		return fmt.Errorf("parse %s: reader is nil", fileName)
	}
	if yield == nil {
		return fmt.Errorf("parse %s: yield callback is nil", fileName)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("parse %s XLS: decoder failed", fileName)
		}
	}()

	file, _, cleanup, err := spoolDocument(ctx, r, p.cfg.MaxDocumentBytes)
	if err != nil {
		return fmt.Errorf("parse %s XLS: %w", fileName, err)
	}
	defer cleanup()
	workbook, err := xlslib.OpenReader(file, "utf-8")
	if err != nil {
		return fmt.Errorf("parse %s XLS: %w", fileName, err)
	}
	if workbook == nil || workbook.NumSheets() == 0 {
		return fmt.Errorf("parse %s XLS: workbook contains no worksheets", fileName)
	}
	sheet, err := selectXLSSheet(workbook, p.cfg.SpreadsheetSheet)
	if err != nil {
		return fmt.Errorf("parse %s XLS: %w", fileName, err)
	}
	emitter := spreadsheetEmitter{cfg: p.cfg, fileName: fileName, sheetName: sheet.Name}
	for index := 0; index <= int(sheet.MaxRow); index++ {
		if err := checkContext(ctx); err != nil {
			return err
		}
		fields, exists, err := readXLSRow(sheet, index, configuredMaxFields(p.cfg.MaxFields))
		if err != nil {
			return fmt.Errorf("parse %s XLS worksheet %q row %d: %w", fileName, sheet.Name, index+1, err)
		}
		if !exists {
			if p.cfg.SkipEmptyLine {
				continue
			}
			fields = []string{}
		}
		if err := emitter.emit(ctx, int64(index+1), fields, yield); err != nil {
			return err
		}
	}
	return emitter.finish()
}

func selectXLSSheet(workbook *xlslib.WorkBook, selector string) (*xlslib.WorkSheet, error) {
	if index, numeric := spreadsheetSheetIndex(selector); numeric {
		sheet := workbook.GetSheet(index)
		if sheet == nil {
			return nil, fmt.Errorf("worksheet index %d was not found", index)
		}
		return sheet, nil
	}
	for index := 0; index < workbook.NumSheets(); index++ {
		sheet := workbook.GetSheet(index)
		if sheet != nil && sheet.Name == selector {
			return sheet, nil
		}
	}
	return nil, fmt.Errorf("worksheet %q was not found", selector)
}

func readXLSRow(sheet *xlslib.WorkSheet, index, maxFields int) (fields []string, exists bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			fields = nil
			exists = false
			err = nil
		}
	}()
	row := sheet.Row(index)
	if row == nil {
		return nil, false, nil
	}
	lastColumn := row.LastCol()
	if lastColumn < 0 {
		return []string{}, true, nil
	}
	if lastColumn > maxFields {
		return make([]string, maxFields+1), true, nil
	}
	fields = make([]string, lastColumn)
	for column := 0; column < lastColumn; column++ {
		fields[column] = row.Col(column)
	}
	return fields, true, nil
}
