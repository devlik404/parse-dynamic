package parser

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"

	"parser-engine/internal/config"
	"parser-engine/internal/model"
)

type csvParser struct {
	cfg       config.ParserConfig
	delimiter rune
}

type framedCSVRecord struct {
	data       []byte
	startLine  int64
	tooLarge   bool
	blank      bool
	peakCap    int
	malformed  bool
	tooComplex bool
}

// csvRecordFramer locates RFC-4180 logical boundaries before encoding/csv is
// allowed to allocate field strings. It retains at most maxRecordBytes and can
// drain/resynchronize an oversized multiline record.
type csvRecordFramer struct {
	reader       *bufio.Reader
	delimiter    []byte
	maxBytes     int
	line         int64
	fieldStart   bool
	inQuotes     bool
	quotePending bool
	delimiterPos int
	malformed    bool
	quotedCR     bool
	maxFields    int
	fieldCount   int
	tooComplex   bool
}

func newCSVRecordFramer(input io.Reader, delimiter rune, maxBytes int, configuredFields ...int) *csvRecordFramer {
	maxFields := 0
	if len(configuredFields) > 0 {
		maxFields = configuredFields[0]
	}
	reader := bufio.NewReaderSize(input, boundedReaderSize(maxBytes))
	if prefix, _ := reader.Peek(3); bytes.Equal(prefix, []byte{0xef, 0xbb, 0xbf}) {
		_, _ = reader.Discard(3)
	}
	return &csvRecordFramer{
		reader:     reader,
		delimiter:  []byte(string(delimiter)),
		maxBytes:   configuredMaxRecordBytes(maxBytes),
		maxFields:  configuredMaxFields(maxFields),
		line:       1,
		fieldStart: true,
	}
}

func (f *csvRecordFramer) next() (framedCSVRecord, error) {
	startLine := f.line
	buffer := newBoundedBytes(f.maxBytes)
	haveData := false
	f.fieldStart = true
	f.inQuotes = false
	f.quotePending = false
	f.delimiterPos = 0
	f.malformed = false
	f.quotedCR = false
	f.fieldCount = 1
	f.tooComplex = false
	crOverflowOnly := false

	for {
		value, err := f.reader.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				if !haveData {
					return framedCSVRecord{}, io.EOF
				}
				if f.inQuotes && !f.quotePending {
					f.malformed = true
				}
				return framedCSVRecord{data: buffer.data, startLine: startLine, tooLarge: buffer.exceeded, peakCap: buffer.peakCap, malformed: f.malformed, tooComplex: f.tooComplex}, nil
			}
			return framedCSVRecord{}, err
		}

		// A newline outside a quoted field terminates the record and is not
		// charged to the logical payload. A preceding CR is retained so
		// encoding/csv can apply its normal CRLF behavior.
		outside := !f.inQuotes || f.quotePending
		if f.quotedCR {
			if value != '\n' {
				f.malformed = true
			}
			f.quotedCR = false
		}
		if value == '\n' && outside {
			f.line++
			if crOverflowOnly {
				// The CR is part of the CRLF terminator, not the logical payload.
				buffer.exceeded = false
			}
			data := buffer.data
			if len(data) > 0 && data[len(data)-1] == '\r' {
				data = data[:len(data)-1]
			}
			return framedCSVRecord{
				data: data, startLine: startLine, tooLarge: buffer.exceeded,
				blank: len(data) == 0, peakCap: buffer.peakCap,
				malformed:  f.malformed,
				tooComplex: f.tooComplex,
			}, nil
		}

		haveData = true
		wasExceeded := buffer.exceeded
		lengthBefore := len(buffer.data)
		if !f.tooComplex {
			buffer.append(value)
		}
		crOverflowOnly = value == '\r' && outside && !wasExceeded && lengthBefore == buffer.limit && buffer.exceeded
		if value == '\n' {
			f.line++
		}

		if f.inQuotes {
			if f.quotePending {
				if value == '"' {
					// Escaped quote.
					f.quotePending = false
					continue
				}
				f.inQuotes = false
				f.quotePending = false
				if len(f.delimiter) == 0 || (value != f.delimiter[0] && value != '\r') {
					f.malformed = true
				}
				f.quotedCR = value == '\r'
				f.processOutside(value)
				continue
			}
			if value == '"' {
				f.quotePending = true
			}
			continue
		}
		f.processOutside(value)
	}
}

func (f *csvRecordFramer) processOutside(value byte) {
	if value == '"' && f.fieldStart && f.delimiterPos == 0 {
		f.inQuotes = true
		f.fieldStart = false
		return
	}
	if value == '"' {
		f.malformed = true
		f.fieldStart = false
		return
	}
	if len(f.delimiter) > 1 {
		if value == f.delimiter[f.delimiterPos] {
			f.delimiterPos++
			if f.delimiterPos == len(f.delimiter) {
				f.delimiterPos = 0
				f.fieldStart = true
				f.incrementFieldCount()
			}
			return
		}
		f.delimiterPos = 0
		f.fieldStart = false
		return
	}
	switch {
	case value == f.delimiter[0]:
		f.fieldStart = true
		f.incrementFieldCount()
	default:
		f.fieldStart = false
	}
}

func (f *csvRecordFramer) incrementFieldCount() {
	f.fieldCount++
	if f.fieldCount > f.maxFields {
		f.tooComplex = true
	}
}

func (p *csvParser) Parse(ctx context.Context, input io.Reader, fileName string, yield YieldSource) error {
	if input == nil {
		return fmt.Errorf("parse %s: reader is nil", fileName)
	}
	if yield == nil {
		return fmt.Errorf("parse %s: yield callback is nil", fileName)
	}

	framer := newCSVRecordFramer(contextReader{ctx: ctx, r: input}, p.delimiter, p.cfg.MaxRecordBytes, p.cfg.MaxFields)
	var header []string
	var recordNumber int64
	for {
		if err := checkContext(ctx); err != nil {
			return err
		}
		framed, err := framer.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("parse %s CSV input: %w", fileName, err)
		}
		if framed.malformed {
			return fmt.Errorf("parse %s CSV record %d at line %d: malformed quoting", fileName, recordNumber+1, framed.startLine)
		}
		if framed.tooComplex {
			if p.cfg.HasHeader && header == nil {
				return fmt.Errorf("parse %s header: field count exceeds PARSER_MAX_FIELDS", fileName)
			}
			recordNumber++
			recordErr := parseError(fileName, recordNumber, framed.startLine, "", "RECORD_TOO_COMPLEX",
				"logical record exceeds PARSER_MAX_FIELDS", tooComplexRawMarker)
			if err := yield(model.SourceRecord{File: fileName, RecordNumber: recordNumber, LineNumber: framed.startLine, Raw: tooComplexRawMarker, Error: recordErr}); err != nil {
				return err
			}
			continue
		}
		if framed.tooLarge {
			if p.cfg.HasHeader && header == nil {
				return fmt.Errorf("parse %s header: logical record exceeds PARSER_MAX_RECORD_BYTES", fileName)
			}
			recordNumber++
			recordErr := parseError(fileName, recordNumber, framed.startLine, "", "RECORD_TOO_LARGE",
				"logical record exceeds PARSER_MAX_RECORD_BYTES", oversizedRawMarker)
			if err := yield(model.SourceRecord{File: fileName, RecordNumber: recordNumber, LineNumber: framed.startLine, Raw: oversizedRawMarker, Error: recordErr}); err != nil {
				return err
			}
			continue
		}
		if framed.blank {
			// encoding/csv intentionally ignores blank physical lines.
			continue
		}

		fields, err := decodeCSVRecord(framed.data, p.delimiter)
		if err != nil {
			return fmt.Errorf("parse %s CSV record %d at line %d: %w", fileName, recordNumber+1, framed.startLine, err)
		}
		if recordNumber == 0 && header == nil && len(fields) > 0 {
			fields[0] = strings.TrimPrefix(fields[0], "\ufeff")
		}
		if p.cfg.SkipEmptyLine && allFieldsEmpty(fields) {
			continue
		}
		if p.cfg.HasHeader && header == nil {
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
		raw := append([]string(nil), fields...)
		if countMismatch(p.cfg, expected, len(fields)) {
			recordErr := parseError(fileName, recordNumber, framed.startLine, "", "COLUMN_COUNT_MISMATCH",
				fmt.Sprintf("expected %d columns, got %d", expected, len(fields)), raw)
			if err := yield(model.SourceRecord{File: fileName, RecordNumber: recordNumber, LineNumber: framed.startLine, Raw: raw, Error: recordErr}); err != nil {
				return err
			}
			continue
		}

		if err := yield(model.SourceRecord{
			File:         fileName,
			RecordNumber: recordNumber,
			LineNumber:   framed.startLine,
			Values:       fieldsToValues(fields, header, p.cfg.Columns),
			Raw:          raw,
		}); err != nil {
			return err
		}
	}
	if p.cfg.HasHeader && header == nil {
		return fmt.Errorf("parse %s: header is missing", fileName)
	}
	return nil
}

func decodeCSVRecord(data []byte, delimiter rune) ([]string, error) {
	reader := csv.NewReader(bytes.NewReader(data))
	reader.Comma = delimiter
	reader.FieldsPerRecord = -1
	fields, err := reader.Read()
	if err != nil {
		return nil, err
	}
	if _, err := reader.Read(); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("framed input contains multiple records")
		}
		return nil, err
	}
	return fields, nil
}
