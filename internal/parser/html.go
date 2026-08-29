package parser

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"

	"parser-engine/internal/config"
)

type htmlParser struct {
	cfg config.ParserConfig
}

func (p *htmlParser) Parse(ctx context.Context, r io.Reader, fileName string, yield YieldSource) error {
	if r == nil {
		return fmt.Errorf("parse %s: reader is nil", fileName)
	}
	if yield == nil {
		return fmt.Errorf("parse %s: yield callback is nil", fileName)
	}
	normalized, err := charset.NewReader(contextReader{ctx: ctx, r: r}, "")
	if err != nil {
		return fmt.Errorf("parse %s HTML encoding: %w", fileName, err)
	}
	z := html.NewTokenizer(normalized)
	z.SetMaxBuf(configuredMaxRecordBytes(p.cfg.MaxRecordBytes))

	tableDepth := 0
	tableOrdinal := -1
	selectedDepth := 0
	foundTable := false
	inRow := false
	inCell := false
	rowNumber := int64(0)
	var fields []string
	var cell *boundedBytes
	rowBytes := 0
	rowTooLarge := false
	rowTooComplex := false
	emitter := spreadsheetEmitter{cfg: p.cfg, fileName: fileName, sheetName: fmt.Sprintf("table[%d]", p.cfg.HTMLTableIndex)}

	finishCell := func() {
		if !inCell {
			return
		}
		inCell = false
		if rowTooLarge || rowTooComplex {
			cell = nil
			return
		}
		value := strings.TrimSpace(string(cell.data))
		cell = nil
		if len(fields) >= configuredMaxFields(p.cfg.MaxFields) {
			rowTooComplex = true
			fields = nil
			return
		}
		if len(value) > configuredMaxRecordBytes(p.cfg.MaxRecordBytes)-rowBytes {
			rowTooLarge = true
			fields = nil
			return
		}
		rowBytes += len(value)
		fields = append(fields, value)
	}
	finishRow := func() error {
		if !inRow {
			return nil
		}
		finishCell()
		inRow = false
		rowNumber++
		if rowTooComplex {
			err := emitter.reject(rowNumber, true, yield)
			fields = nil
			rowBytes = 0
			rowTooLarge = false
			rowTooComplex = false
			return err
		} else if rowTooLarge {
			err := emitter.reject(rowNumber, false, yield)
			fields = nil
			rowBytes = 0
			rowTooLarge = false
			rowTooComplex = false
			return err
		}
		err := emitter.emit(ctx, rowNumber, fields, yield)
		fields = nil
		rowBytes = 0
		rowTooLarge = false
		rowTooComplex = false
		return err
	}

	for {
		if err := checkContext(ctx); err != nil {
			return err
		}
		tokenType := z.Next()
		switch tokenType {
		case html.ErrorToken:
			err := z.Err()
			if errors.Is(err, io.EOF) {
				if err := finishRow(); err != nil {
					return err
				}
				if !foundTable {
					return fmt.Errorf("parse %s HTML: table index %d was not found", fileName, p.cfg.HTMLTableIndex)
				}
				return emitter.finish()
			}
			return fmt.Errorf("parse %s HTML: %w", fileName, err)
		case html.TextToken:
			if inCell && selectedDepth > 0 && tableDepth == selectedDepth && !rowTooLarge && !rowTooComplex {
				cell.append(z.Text()...)
				if cell.exceeded {
					rowTooLarge = true
					fields = nil
				}
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			token := z.Token()
			if token.Data == "table" {
				tableDepth++
				tableOrdinal++
				if tableOrdinal == p.cfg.HTMLTableIndex {
					selectedDepth = tableDepth
					foundTable = true
				}
				continue
			}
			active := selectedDepth > 0 && tableDepth == selectedDepth
			if active && token.Data == "tr" {
				if err := finishRow(); err != nil {
					return err
				}
				inRow = true
			}
			if active && inRow && (token.Data == "td" || token.Data == "th") {
				finishCell()
				inCell = true
				cell = newBoundedBytes(p.cfg.MaxRecordBytes)
			}
		case html.EndTagToken:
			token := z.Token()
			active := selectedDepth > 0 && tableDepth == selectedDepth
			if active && (token.Data == "td" || token.Data == "th") {
				finishCell()
			}
			if active && token.Data == "tr" {
				if err := finishRow(); err != nil {
					return err
				}
			}
			if token.Data == "table" {
				if selectedDepth == tableDepth {
					if err := finishRow(); err != nil {
						return err
					}
					return emitter.finish()
				}
				if tableDepth > 0 {
					tableDepth--
				}
			}
		}
	}
}
