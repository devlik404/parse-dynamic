package parser

import (
	"fmt"
	"strings"

	"parser-engine/internal/config"
)

// staticRecordFraming handles delimited files whose first-class record codes
// distinguish file header, data, and file footer rows. The codes are entirely
// configuration-driven; this type contains no product-specific values.
type staticRecordFraming struct {
	recordTypeIndex int
	fileHeaderCode  string
	dataCode        string
	fileFooterCode  string
	sawHeader       bool
	sawData         bool
	sawFooter       bool
}

func newStaticRecordFraming(cfg config.ParserConfig) *staticRecordFraming {
	dataCode := strings.TrimSpace(cfg.Sectioned.DataCode)
	if cfg.FileType != config.FileTypeDelimited || dataCode == "" {
		return nil
	}
	headerCode := strings.TrimSpace(cfg.Sectioned.FileHeaderCode)
	return &staticRecordFraming{
		recordTypeIndex: cfg.Sectioned.RecordTypeIndex,
		fileHeaderCode:  headerCode,
		dataCode:        dataCode,
		fileFooterCode:  strings.TrimSpace(cfg.Sectioned.FileFooterCode),
		sawHeader:       headerCode == "",
	}
}

func (f *staticRecordFraming) accept(fields []string, lineNumber int64) (bool, error) {
	if f == nil {
		return true, nil
	}
	if f.sawFooter {
		return false, fmt.Errorf("record appears after file footer")
	}
	if f.recordTypeIndex < 0 || f.recordTypeIndex >= len(fields) {
		return false, fmt.Errorf("record type field index %d is out of range", f.recordTypeIndex)
	}
	recordType := strings.TrimSpace(fields[f.recordTypeIndex])
	switch recordType {
	case f.fileHeaderCode:
		if f.fileHeaderCode == "" {
			return false, fmt.Errorf("unrecognized record type at line %d", lineNumber)
		}
		if f.sawHeader || f.sawData {
			return false, fmt.Errorf("file header is duplicated or out of sequence")
		}
		f.sawHeader = true
		return false, nil
	case f.dataCode:
		if !f.sawHeader {
			return false, fmt.Errorf("data record appears before file header")
		}
		f.sawData = true
		return true, nil
	case f.fileFooterCode:
		if f.fileFooterCode == "" {
			return false, fmt.Errorf("unrecognized record type at line %d", lineNumber)
		}
		if !f.sawHeader {
			return false, fmt.Errorf("file footer appears before file header")
		}
		f.sawFooter = true
		return false, nil
	default:
		return false, fmt.Errorf("unrecognized record type")
	}
}

func (f *staticRecordFraming) finish() error {
	if f == nil {
		return nil
	}
	if !f.sawHeader {
		return fmt.Errorf("file header is missing")
	}
	if f.fileFooterCode != "" && !f.sawFooter {
		return fmt.Errorf("file footer is missing")
	}
	return nil
}
