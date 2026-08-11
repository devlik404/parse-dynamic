package parser

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"parser-engine/internal/config"
	"parser-engine/internal/model"
)

const maxJSONNestingDepth = 1000

type jsonParser struct {
	cfg        config.ParserConfig
	mode       config.JSONMode
	recordPath []string
}

type framedJSONValue struct {
	data       []byte
	tooLarge   bool
	tooComplex bool
	peakCap    int
}

func (p *jsonParser) Parse(ctx context.Context, r io.Reader, fileName string, yield YieldSource) error {
	if r == nil {
		return fmt.Errorf("parse %s: reader is nil", fileName)
	}
	if yield == nil {
		return fmt.Errorf("parse %s: yield callback is nil", fileName)
	}
	if p.mode == config.JSONModeNDJSON {
		return p.parseNDJSON(ctx, r, fileName, yield)
	}

	scanner, err := newJSONLexScanner(contextReader{ctx: ctx, r: r}, p.cfg.MaxRecordBytes, p.cfg.MaxFields)
	if err != nil {
		return fmt.Errorf("parse %s JSON: %w", fileName, err)
	}
	maxBytes := configuredMaxRecordBytes(p.cfg.MaxRecordBytes)
	var recordNumber int64
	emitFrame := func(frame framedJSONValue) error {
		recordNumber++
		if frame.tooComplex {
			recordErr := parseError(fileName, recordNumber, 0, "", "RECORD_TOO_COMPLEX",
				"logical record exceeds PARSER_MAX_FIELDS", tooComplexRawMarker)
			return yield(model.SourceRecord{File: fileName, RecordNumber: recordNumber, Raw: tooComplexRawMarker, Error: recordErr})
		}
		if frame.tooLarge {
			recordErr := parseError(fileName, recordNumber, 0, "", "RECORD_TOO_LARGE",
				"logical record exceeds PARSER_MAX_RECORD_BYTES", oversizedRawMarker)
			return yield(model.SourceRecord{File: fileName, RecordNumber: recordNumber, Raw: oversizedRawMarker, Error: recordErr})
		}
		value, err := decodeJSONValue(frame.data)
		if err != nil {
			return fmt.Errorf("record %d is malformed: %w", recordNumber, err)
		}
		return emitJSONSource(ctx, fileName, recordNumber, 0, value, yield)
	}

	if len(p.recordPath) > 0 {
		found, err := scanner.walkRecordPath(p.recordPath, 0, p.mode, maxBytes, emitFrame, 0)
		if err != nil {
			return fmt.Errorf("parse %s JSON path %q: %w", fileName, strings.Join(p.recordPath, "."), err)
		}
		if !found {
			return fmt.Errorf("parse %s JSON: record path %q was not found", fileName, strings.Join(p.recordPath, "."))
		}
		if err := scanner.requireEOF(); err != nil {
			return fmt.Errorf("parse %s JSON: %w", fileName, err)
		}
		return nil
	}

	first, err := scanner.peekNonSpace()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("parse %s JSON: empty input", fileName)
		}
		return err
	}
	switch p.mode {
	case config.JSONModeArray:
		if first != '[' {
			return fmt.Errorf("parse %s JSON: ARRAY mode requires a top-level array", fileName)
		}
		if err := scanner.streamArray(maxBytes, emitFrame, 0); err != nil {
			return fmt.Errorf("parse %s JSON array: %w", fileName, err)
		}
		return scanner.requireEOF()
	case config.JSONModeSingle:
		frame, err := scanner.readBoundedValue(maxBytes, 0)
		if err != nil {
			return fmt.Errorf("parse %s JSON: %w", fileName, err)
		}
		if err := emitFrame(frame); err != nil {
			return err
		}
		return scanner.requireEOF()
	case config.JSONModeAuto:
		if first == '[' {
			if err := scanner.streamArray(maxBytes, emitFrame, 0); err != nil {
				return fmt.Errorf("parse %s JSON array: %w", fileName, err)
			}
			return scanner.requireEOF()
		}
		for {
			if err := checkContext(ctx); err != nil {
				return err
			}
			_, err := scanner.peekNonSpace()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("parse %s JSON: %w", fileName, err)
			}
			frame, err := scanner.readBoundedValue(maxBytes, 0)
			if err != nil {
				return fmt.Errorf("parse %s JSON record %d: %w", fileName, recordNumber+1, err)
			}
			if err := emitFrame(frame); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("parse %s JSON: unsupported mode %q", fileName, p.mode)
	}
}

func (p *jsonParser) parseNDJSON(ctx context.Context, r io.Reader, fileName string, yield YieldSource) error {
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
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(p.recordPath) > 0 {
			pathScanner, pathErr := newJSONLexScanner(strings.NewReader(line), p.cfg.MaxRecordBytes, p.cfg.MaxFields)
			callbackFailed := false
			found := false
			if pathErr == nil {
				found, pathErr = pathScanner.walkRecordPath(
					p.recordPath, 0, p.mode, configuredMaxRecordBytes(p.cfg.MaxRecordBytes),
					func(frame framedJSONValue) error {
						recordNumber++
						if frame.tooComplex {
							recordErr := parseError(fileName, recordNumber, lineNumber, "", "RECORD_TOO_COMPLEX",
								"logical record exceeds PARSER_MAX_FIELDS", tooComplexRawMarker)
							if err := yield(model.SourceRecord{File: fileName, RecordNumber: recordNumber, LineNumber: lineNumber, Raw: tooComplexRawMarker, Error: recordErr}); err != nil {
								callbackFailed = true
								return err
							}
							return nil
						}
						if frame.tooLarge {
							recordErr := parseError(fileName, recordNumber, lineNumber, "", "RECORD_TOO_LARGE",
								"logical record exceeds PARSER_MAX_RECORD_BYTES", oversizedRawMarker)
							if err := yield(model.SourceRecord{File: fileName, RecordNumber: recordNumber, LineNumber: lineNumber, Raw: oversizedRawMarker, Error: recordErr}); err != nil {
								callbackFailed = true
								return err
							}
							return nil
						}
						value, err := decodeJSONValue(frame.data)
						if err != nil {
							return err
						}
						if err := emitJSONSource(ctx, fileName, recordNumber, lineNumber, value, yield); err != nil {
							callbackFailed = true
							return err
						}
						return nil
					}, 0,
				)
			}
			if callbackFailed {
				return pathErr
			}
			if pathErr == nil {
				pathErr = pathScanner.requireEOF()
			}
			if pathErr != nil {
				code := "INVALID_JSON"
				field := ""
				message := "line is not valid JSON"
				validator, validationErr := newJSONLexScanner(strings.NewReader(line), p.cfg.MaxRecordBytes, p.cfg.MaxFields)
				if validationErr == nil {
					validationErr = validator.skipValue(0)
				}
				if validationErr == nil {
					validationErr = validator.requireEOF()
				}
				if validationErr == nil {
					code = "JSON_RECORD_PATH_NOT_FOUND"
					field = strings.Join(p.recordPath, ".")
					message = "record path was not found"
				}
				recordNumber++
				recordErr := parseError(fileName, recordNumber, lineNumber, field, code, message, line)
				if callbackErr := yield(model.SourceRecord{File: fileName, RecordNumber: recordNumber, LineNumber: lineNumber, Raw: line, Error: recordErr}); callbackErr != nil {
					return callbackErr
				}
				continue
			}
			if !found {
				recordNumber++
				recordErr := parseError(fileName, recordNumber, lineNumber, strings.Join(p.recordPath, "."), "JSON_RECORD_PATH_NOT_FOUND",
					"record path was not found", line)
				if callbackErr := yield(model.SourceRecord{File: fileName, RecordNumber: recordNumber, LineNumber: lineNumber, Raw: line, Error: recordErr}); callbackErr != nil {
					return callbackErr
				}
			}
			continue
		}
		lexical, lexicalErr := newJSONLexScanner(strings.NewReader(line), p.cfg.MaxRecordBytes, p.cfg.MaxFields)
		var frame framedJSONValue
		if lexicalErr == nil {
			frame, lexicalErr = lexical.readBoundedValue(p.cfg.MaxRecordBytes, 0)
		}
		if lexicalErr == nil {
			lexicalErr = lexical.requireEOF()
		}
		if lexicalErr == nil && frame.tooComplex {
			recordNumber++
			recordErr := parseError(fileName, recordNumber, lineNumber, "", "RECORD_TOO_COMPLEX",
				"logical record exceeds PARSER_MAX_FIELDS", tooComplexRawMarker)
			if callbackErr := yield(model.SourceRecord{File: fileName, RecordNumber: recordNumber, LineNumber: lineNumber, Raw: tooComplexRawMarker, Error: recordErr}); callbackErr != nil {
				return callbackErr
			}
			continue
		}
		var value any
		err := lexicalErr
		if err == nil {
			value, err = decodeJSONValue([]byte(line))
		}
		if err != nil {
			recordNumber++
			recordErr := parseError(fileName, recordNumber, lineNumber, "", "INVALID_JSON", "line is not valid JSON", line)
			if callbackErr := yield(model.SourceRecord{File: fileName, RecordNumber: recordNumber, LineNumber: lineNumber, Raw: line, Error: recordErr}); callbackErr != nil {
				return callbackErr
			}
			continue
		}

		values := []any{value}
		if len(p.recordPath) > 0 {
			selected, ok := lookupJSONPath(value, p.recordPath)
			if !ok {
				recordNumber++
				recordErr := parseError(fileName, recordNumber, lineNumber, strings.Join(p.recordPath, "."), "JSON_RECORD_PATH_NOT_FOUND",
					"record path was not found", line)
				if callbackErr := yield(model.SourceRecord{File: fileName, RecordNumber: recordNumber, LineNumber: lineNumber, Raw: line, Error: recordErr}); callbackErr != nil {
					return callbackErr
				}
				continue
			}
			if array, ok := selected.([]any); ok {
				values = array
			} else {
				values = []any{selected}
			}
		}
		for _, item := range values {
			recordNumber++
			if err := emitJSONSource(ctx, fileName, recordNumber, lineNumber, item, yield); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("parse %s NDJSON at line %d: %w", fileName, lineNumber+1, err)
	}
	return nil
}

func decodeJSONValue(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("multiple JSON values")
		}
		return nil, err
	}
	return value, nil
}

func emitJSONSource(ctx context.Context, file string, record, line int64, raw any, yield YieldSource) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	values := make(map[string]any)
	if object, ok := raw.(map[string]any); ok {
		values = object
	} else {
		values["$"] = raw
		values["value"] = raw
		values["0"] = raw
	}
	return yield(model.SourceRecord{File: file, RecordNumber: record, LineNumber: line, Values: values, Raw: raw})
}

// jsonLexScanner validates and frames raw JSON values without materializing
// them. Capture is enabled only for a selected logical record and is clipped at
// PARSER_MAX_RECORD_BYTES.
type jsonLexScanner struct {
	reader     *bufio.Reader
	capture    *boundedBytes
	maxFields  int
	fieldCount int
	counting   bool
	tooComplex bool
}

func newJSONLexScanner(input io.Reader, configuredMax ...int) (*jsonLexScanner, error) {
	maxBytes := 0
	maxFields := 0
	if len(configuredMax) > 0 {
		maxBytes = configuredMax[0]
	}
	if len(configuredMax) > 1 {
		maxFields = configuredMax[1]
	}
	reader := bufio.NewReaderSize(input, boundedReaderSize(maxBytes))
	if prefix, _ := reader.Peek(3); bytes.Equal(prefix, []byte{0xef, 0xbb, 0xbf}) {
		_, _ = reader.Discard(3)
	}
	return &jsonLexScanner{reader: reader, maxFields: configuredMaxFields(maxFields)}, nil
}

func (s *jsonLexScanner) readByte() (byte, error) {
	value, err := s.reader.ReadByte()
	if err == nil && s.capture != nil && !s.tooComplex {
		s.capture.append(value)
	}
	return value, err
}

func (s *jsonLexScanner) peekByte() (byte, error) {
	values, err := s.reader.Peek(1)
	if err != nil {
		return 0, err
	}
	return values[0], nil
}

func (s *jsonLexScanner) skipWhitespace() error {
	for {
		value, err := s.peekByte()
		if err != nil {
			return err
		}
		if value != ' ' && value != '\t' && value != '\r' && value != '\n' {
			return nil
		}
		if _, err := s.readByte(); err != nil {
			return err
		}
	}
}

func (s *jsonLexScanner) peekNonSpace() (byte, error) {
	err := s.skipWhitespace()
	if err != nil {
		return 0, err
	}
	return s.peekByte()
}

func (s *jsonLexScanner) requireEOF() error {
	if err := s.skipWhitespace(); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return fmt.Errorf("multiple top-level JSON values are not allowed in this mode")
}

func (s *jsonLexScanner) expect(expected byte) error {
	value, err := s.readByte()
	if err != nil {
		return err
	}
	if value != expected {
		return fmt.Errorf("expected %q", expected)
	}
	return nil
}

func (s *jsonLexScanner) readBoundedValue(limit, depth int) (framedJSONValue, error) {
	if s.capture != nil {
		return framedJSONValue{}, fmt.Errorf("nested JSON capture")
	}
	capture := newBoundedBytes(limit)
	s.capture = capture
	s.fieldCount = 0
	s.tooComplex = false
	s.counting = true
	err := s.skipValue(depth)
	s.counting = false
	s.capture = nil
	if err != nil {
		return framedJSONValue{}, err
	}
	return framedJSONValue{data: capture.data, tooLarge: capture.exceeded, tooComplex: s.tooComplex, peakCap: capture.peakCap}, nil
}

func (s *jsonLexScanner) skipValue(depth int) error {
	if depth > maxJSONNestingDepth {
		return fmt.Errorf("JSON nesting exceeds safe limit")
	}
	if err := s.skipWhitespace(); err != nil {
		return err
	}
	value, err := s.peekByte()
	if err != nil {
		return err
	}
	switch value {
	case '{':
		return s.skipObject(depth + 1)
	case '[':
		return s.skipArray(depth + 1)
	case '"':
		return s.scanString(nil)
	case 't':
		return s.scanLiteral("true")
	case 'f':
		return s.scanLiteral("false")
	case 'n':
		return s.scanLiteral("null")
	default:
		if value == '-' || (value >= '0' && value <= '9') {
			return s.scanNumber()
		}
		return fmt.Errorf("invalid JSON value")
	}
}

func (s *jsonLexScanner) skipObject(depth int) error {
	if depth > maxJSONNestingDepth {
		return fmt.Errorf("JSON nesting exceeds safe limit")
	}
	if err := s.expect('{'); err != nil {
		return err
	}
	if err := s.skipWhitespace(); err != nil {
		return err
	}
	if value, _ := s.peekByte(); value == '}' {
		return s.expect('}')
	}
	for {
		s.incrementComplexity()
		if value, err := s.peekByte(); err != nil || value != '"' {
			return fmt.Errorf("object key must be a string")
		}
		if err := s.scanString(nil); err != nil {
			return err
		}
		if err := s.skipWhitespace(); err != nil {
			return err
		}
		if err := s.expect(':'); err != nil {
			return err
		}
		if err := s.skipValue(depth); err != nil {
			return err
		}
		if err := s.skipWhitespace(); err != nil {
			return err
		}
		separator, err := s.readByte()
		if err != nil {
			return err
		}
		switch separator {
		case '}':
			return nil
		case ',':
			if err := s.skipWhitespace(); err != nil {
				return err
			}
		default:
			return fmt.Errorf("expected comma or closing object")
		}
	}
}

func (s *jsonLexScanner) skipArray(depth int) error {
	if depth > maxJSONNestingDepth {
		return fmt.Errorf("JSON nesting exceeds safe limit")
	}
	if err := s.expect('['); err != nil {
		return err
	}
	if err := s.skipWhitespace(); err != nil {
		return err
	}
	if value, _ := s.peekByte(); value == ']' {
		return s.expect(']')
	}
	for {
		s.incrementComplexity()
		if err := s.skipValue(depth); err != nil {
			return err
		}
		if err := s.skipWhitespace(); err != nil {
			return err
		}
		separator, err := s.readByte()
		if err != nil {
			return err
		}
		switch separator {
		case ']':
			return nil
		case ',':
			if err := s.skipWhitespace(); err != nil {
				return err
			}
		default:
			return fmt.Errorf("expected comma or closing array")
		}
	}
}

func (s *jsonLexScanner) incrementComplexity() {
	if !s.counting || s.tooComplex {
		return
	}
	s.fieldCount++
	if s.fieldCount > s.maxFields {
		s.tooComplex = true
		if s.capture != nil {
			s.capture.disable()
		}
	}
}

func (s *jsonLexScanner) scanString(raw *boundedBytes) error {
	value, err := s.readByte()
	if err != nil || value != '"' {
		return fmt.Errorf("expected JSON string")
	}
	if raw != nil {
		raw.append(value)
	}
	for {
		value, err = s.readByte()
		if err != nil {
			return err
		}
		if raw != nil {
			raw.append(value)
		}
		switch {
		case value == '"':
			return nil
		case value < 0x20:
			return fmt.Errorf("control character in JSON string")
		case value == '\\':
			escape, err := s.readByte()
			if err != nil {
				return err
			}
			if raw != nil {
				raw.append(escape)
			}
			if strings.ContainsRune(`"\\/bfnrt`, rune(escape)) {
				continue
			}
			if escape != 'u' {
				return fmt.Errorf("invalid JSON string escape")
			}
			for index := 0; index < 4; index++ {
				hex, err := s.readByte()
				if err != nil {
					return err
				}
				if raw != nil {
					raw.append(hex)
				}
				if !isHex(hex) {
					return fmt.Errorf("invalid Unicode escape")
				}
			}
		}
	}
}

func (s *jsonLexScanner) scanLiteral(literal string) error {
	for index := range literal {
		value, err := s.readByte()
		if err != nil {
			return err
		}
		if value != literal[index] {
			return fmt.Errorf("invalid JSON literal")
		}
	}
	return s.requireValueBoundary()
}

func (s *jsonLexScanner) scanNumber() error {
	value, err := s.peekByte()
	if err != nil {
		return err
	}
	if value == '-' {
		_, _ = s.readByte()
		value, err = s.peekByte()
		if err != nil {
			return fmt.Errorf("incomplete JSON number")
		}
	}
	if value == '0' {
		_, _ = s.readByte()
		if next, err := s.peekByte(); err == nil && next >= '0' && next <= '9' {
			return fmt.Errorf("leading zero in JSON number")
		}
	} else if value >= '1' && value <= '9' {
		for {
			value, err = s.peekByte()
			if err != nil || value < '0' || value > '9' {
				break
			}
			_, _ = s.readByte()
		}
	} else {
		return fmt.Errorf("invalid JSON number")
	}
	if value, err = s.peekByte(); err == nil && value == '.' {
		_, _ = s.readByte()
		if value, err = s.peekByte(); err != nil || value < '0' || value > '9' {
			return fmt.Errorf("fraction requires a digit")
		}
		for {
			value, err = s.peekByte()
			if err != nil || value < '0' || value > '9' {
				break
			}
			_, _ = s.readByte()
		}
	}
	if value, err = s.peekByte(); err == nil && (value == 'e' || value == 'E') {
		_, _ = s.readByte()
		if value, err = s.peekByte(); err == nil && (value == '+' || value == '-') {
			_, _ = s.readByte()
		}
		if value, err = s.peekByte(); err != nil || value < '0' || value > '9' {
			return fmt.Errorf("exponent requires a digit")
		}
		for {
			value, err = s.peekByte()
			if err != nil || value < '0' || value > '9' {
				break
			}
			_, _ = s.readByte()
		}
	}
	return s.requireValueBoundary()
}

func (s *jsonLexScanner) requireValueBoundary() error {
	value, err := s.peekByte()
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	if value == ' ' || value == '\t' || value == '\r' || value == '\n' || value == ',' || value == ']' || value == '}' {
		return nil
	}
	return fmt.Errorf("invalid character after JSON value")
}

func (s *jsonLexScanner) streamArray(limit int, emit func(framedJSONValue) error, depth int) error {
	if depth > maxJSONNestingDepth {
		return fmt.Errorf("JSON nesting exceeds safe limit")
	}
	if err := s.skipWhitespace(); err != nil {
		return err
	}
	if err := s.expect('['); err != nil {
		return err
	}
	if err := s.skipWhitespace(); err != nil {
		return err
	}
	if value, _ := s.peekByte(); value == ']' {
		return s.expect(']')
	}
	for {
		frame, err := s.readBoundedValue(limit, depth+1)
		if err != nil {
			return err
		}
		if err := emit(frame); err != nil {
			return err
		}
		if err := s.skipWhitespace(); err != nil {
			return err
		}
		separator, err := s.readByte()
		if err != nil {
			return err
		}
		switch separator {
		case ']':
			return nil
		case ',':
			if err := s.skipWhitespace(); err != nil {
				return err
			}
		default:
			return fmt.Errorf("expected comma or closing array")
		}
	}
}

func (s *jsonLexScanner) walkRecordPath(path []string, index int, mode config.JSONMode, limit int, emit func(framedJSONValue) error, depth int) (bool, error) {
	if depth > maxJSONNestingDepth {
		return false, fmt.Errorf("JSON nesting exceeds safe limit")
	}
	if err := s.skipWhitespace(); err != nil {
		return false, err
	}
	if err := s.expect('{'); err != nil {
		return false, fmt.Errorf("path segment %q requires an object", path[index])
	}
	if err := s.skipWhitespace(); err != nil {
		return false, err
	}
	if value, _ := s.peekByte(); value == '}' {
		_ = s.expect('}')
		return false, nil
	}
	found := false
	keyLimit := pathKeyCaptureLimit(path[index])
	for {
		if value, err := s.peekByte(); err != nil || value != '"' {
			return false, fmt.Errorf("object key must be a string")
		}
		rawKey := newBoundedBytes(keyLimit)
		if err := s.scanString(rawKey); err != nil {
			return false, err
		}
		keyMatches := false
		if !rawKey.exceeded {
			var key string
			if err := json.Unmarshal(rawKey.data, &key); err != nil {
				return false, fmt.Errorf("invalid object key")
			}
			keyMatches = key == path[index]
		}
		if err := s.skipWhitespace(); err != nil {
			return false, err
		}
		if err := s.expect(':'); err != nil {
			return false, err
		}
		if keyMatches {
			if found {
				return false, fmt.Errorf("record path occurs more than once")
			}
			var err error
			if index+1 < len(path) {
				found, err = s.walkRecordPath(path, index+1, mode, limit, emit, depth+1)
			} else {
				found = true
				err = s.streamPathTarget(mode, limit, emit, depth+1)
			}
			if err != nil {
				return false, err
			}
		} else if err := s.skipValue(depth + 1); err != nil {
			return false, err
		}
		if err := s.skipWhitespace(); err != nil {
			return false, err
		}
		separator, err := s.readByte()
		if err != nil {
			return false, err
		}
		switch separator {
		case '}':
			return found, nil
		case ',':
			if err := s.skipWhitespace(); err != nil {
				return false, err
			}
		default:
			return false, fmt.Errorf("expected comma or closing object")
		}
	}
}

func (s *jsonLexScanner) streamPathTarget(mode config.JSONMode, limit int, emit func(framedJSONValue) error, depth int) error {
	value, err := s.peekNonSpace()
	if err != nil {
		return err
	}
	if value == '[' {
		if mode == config.JSONModeSingle {
			return fmt.Errorf("SINGLE mode record path must select one value, not an array")
		}
		return s.streamArray(limit, emit, depth)
	}
	if mode == config.JSONModeArray {
		return fmt.Errorf("ARRAY mode record path must select an array")
	}
	frame, err := s.readBoundedValue(limit, depth)
	if err != nil {
		return err
	}
	return emit(frame)
}

func pathKeyCaptureLimit(segment string) int {
	limit := len(segment)*6 + 2
	if limit < 64 {
		limit = 64
	}
	return limit
}

func isHex(value byte) bool {
	return (value >= '0' && value <= '9') || (value >= 'a' && value <= 'f') || (value >= 'A' && value <= 'F')
}

func lookupJSONPath(value any, path []string) (any, bool) {
	current := value
	for _, segment := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[segment]
		if !ok {
			return nil, false
		}
	}
	return current, true
}
