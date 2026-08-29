package parser

import (
	"context"
	"fmt"
	"io"
	"strings"

	pdflib "github.com/ledongthuc/pdf"

	"parser-engine/internal/config"
	"parser-engine/internal/model"
)

type pdfParser struct {
	cfg config.ParserConfig
}

func (p *pdfParser) Parse(ctx context.Context, r io.Reader, fileName string, yield YieldSource) (err error) {
	if r == nil {
		return fmt.Errorf("parse %s: reader is nil", fileName)
	}
	if yield == nil {
		return fmt.Errorf("parse %s: yield callback is nil", fileName)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("parse %s PDF: decoder failed", fileName)
		}
	}()

	file, size, cleanup, err := spoolDocument(ctx, r, p.cfg.MaxDocumentBytes)
	if err != nil {
		return fmt.Errorf("parse %s PDF: %w", fileName, err)
	}
	defer cleanup()
	if size < 100 {
		return fmt.Errorf("parse %s PDF: invalid or truncated PDF", fileName)
	}
	document, err := pdflib.NewReader(file, size)
	if err != nil {
		return fmt.Errorf("parse %s PDF: %w", fileName, err)
	}
	pageCount := document.NumPage()
	if pageCount < 1 {
		return fmt.Errorf("parse %s PDF: document contains no pages", fileName)
	}
	maxBytes := configuredMaxRecordBytes(p.cfg.MaxRecordBytes)
	for pageNumber := 1; pageNumber <= pageCount; pageNumber++ {
		if err := checkContext(ctx); err != nil {
			return err
		}
		text, extractErr := document.Page(pageNumber).GetPlainText(nil)
		recordNumber := int64(pageNumber)
		if extractErr != nil {
			recordErr := parseError(fileName, recordNumber, 0, "text", "PDF_TEXT_EXTRACTION_FAILED",
				"text could not be extracted from this PDF page", "[omitted: PDF extraction failed]")
			if err := yield(model.SourceRecord{File: fileName, RecordNumber: recordNumber, Raw: recordErr.RawValue, Error: recordErr}); err != nil {
				return err
			}
			continue
		}
		text = strings.TrimSpace(text)
		if text == "" {
			recordErr := parseError(fileName, recordNumber, 0, "text", "PDF_PAGE_HAS_NO_TEXT",
				"PDF page has no extractable text; scanned pages require OCR", "")
			if err := yield(model.SourceRecord{File: fileName, RecordNumber: recordNumber, Raw: "", Error: recordErr}); err != nil {
				return err
			}
			continue
		}
		if len(text) > maxBytes {
			recordErr := parseError(fileName, recordNumber, 0, "text", "RECORD_TOO_LARGE",
				"PDF page text exceeds PARSER_MAX_RECORD_BYTES", oversizedRawMarker)
			if err := yield(model.SourceRecord{File: fileName, RecordNumber: recordNumber, Raw: oversizedRawMarker, Error: recordErr}); err != nil {
				return err
			}
			continue
		}
		values := map[string]any{
			"page":  recordNumber,
			"text":  text,
			"raw":   text,
			"value": text,
		}
		if err := yield(model.SourceRecord{File: fileName, RecordNumber: recordNumber, Values: values, Raw: text}); err != nil {
			return err
		}
	}
	return nil
}
