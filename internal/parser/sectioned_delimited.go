package parser

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"parser-engine/internal/config"
	"parser-engine/internal/model"
)

type sectionedDelimitedParser struct {
	cfg       config.ParserConfig
	sectioned config.SectionedDelimitedConfig
}

type activeSectionSchema struct {
	key     string
	headers []string
}

func newSectionedDelimitedParser(cfg config.ParserConfig) (StreamParser, error) {
	if cfg.Delimiter == "" {
		return nil, fmt.Errorf("PARSER_DELIMITER is required for SECTIONED_DELIMITED")
	}

	sectioned := cfg.Sectioned
	sectioned.FileHeaderCode = strings.TrimSpace(sectioned.FileHeaderCode)
	sectioned.SectionHeaderCode = strings.TrimSpace(sectioned.SectionHeaderCode)
	sectioned.DataCode = strings.TrimSpace(sectioned.DataCode)
	sectioned.SectionFooterCode = strings.TrimSpace(sectioned.SectionFooterCode)
	sectioned.FileFooterCode = strings.TrimSpace(sectioned.FileFooterCode)
	sectioned.DuplicateHeaderPolicy = config.DuplicateHeaderPolicy(strings.ToUpper(strings.TrimSpace(string(sectioned.DuplicateHeaderPolicy))))
	if sectioned.DuplicateHeaderPolicy == "" {
		sectioned.DuplicateHeaderPolicy = config.DuplicateHeaderError
	}

	if sectioned.SectionHeaderCode == "" {
		return nil, fmt.Errorf("PARSER_SECTION_HEADER_CODE is required for SECTIONED_DELIMITED")
	}
	if sectioned.DataCode == "" {
		return nil, fmt.Errorf("PARSER_DATA_CODE is required for SECTIONED_DELIMITED")
	}
	if sectioned.RecordTypeIndex < 0 || sectioned.SectionKeyIndex < 0 || sectioned.HeaderStartIndex < 0 || sectioned.DataStartIndex < 0 {
		return nil, fmt.Errorf("SECTIONED_DELIMITED field indexes must be >= 0")
	}
	maxFields := configuredMaxFields(cfg.MaxFields)
	for name, value := range map[string]int{
		"PARSER_RECORD_TYPE_INDEX":          sectioned.RecordTypeIndex,
		"PARSER_SECTION_KEY_INDEX":          sectioned.SectionKeyIndex,
		"PARSER_DYNAMIC_HEADER_START_INDEX": sectioned.HeaderStartIndex,
		"PARSER_DATA_START_INDEX":           sectioned.DataStartIndex,
	} {
		if value >= maxFields {
			return nil, fmt.Errorf("%s must be less than PARSER_MAX_FIELDS", name)
		}
	}
	if sectioned.RecordTypeIndex == sectioned.SectionKeyIndex {
		return nil, fmt.Errorf("PARSER_RECORD_TYPE_INDEX and PARSER_SECTION_KEY_INDEX must differ")
	}
	if sectioned.DuplicateHeaderPolicy != config.DuplicateHeaderError && sectioned.DuplicateHeaderPolicy != config.DuplicateHeaderSuffixIndex {
		return nil, fmt.Errorf("PARSER_DUPLICATE_HEADER_POLICY must be ERROR or SUFFIX_INDEX")
	}

	codes := map[string]string{}
	for name, code := range map[string]string{
		"PARSER_FILE_HEADER_CODE":    sectioned.FileHeaderCode,
		"PARSER_SECTION_HEADER_CODE": sectioned.SectionHeaderCode,
		"PARSER_DATA_CODE":           sectioned.DataCode,
		"PARSER_SECTION_FOOTER_CODE": sectioned.SectionFooterCode,
		"PARSER_FILE_FOOTER_CODE":    sectioned.FileFooterCode,
	} {
		if code == "" {
			continue
		}
		if strings.Contains(code, cfg.Delimiter) || strings.ContainsAny(code, "\r\n") {
			return nil, fmt.Errorf("%s must not contain the delimiter or a line break", name)
		}
		if previous, exists := codes[code]; exists {
			return nil, fmt.Errorf("%s and %s must use different record codes", previous, name)
		}
		codes[code] = name
	}

	return &sectionedDelimitedParser{cfg: cfg, sectioned: sectioned}, nil
}

func (p *sectionedDelimitedParser) Parse(ctx context.Context, r io.Reader, fileName string, yield YieldSource) error {
	if r == nil {
		return fmt.Errorf("parse %s: reader is nil", fileName)
	}
	if yield == nil {
		return fmt.Errorf("parse %s: yield callback is nil", fileName)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	reader := newBoundedPhysicalLineReader(contextReader{ctx: ctx, r: r}, p.cfg.MaxRecordBytes)
	var lineNumber int64
	var recordNumber int64
	var active *activeSectionSchema
	sawFileHeader := p.sectioned.FileHeaderCode == ""
	sawFileFooter := false
	sawSection := false

	for {
		line, oversized, readErr := reader.readLine()
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return fmt.Errorf("parse %s at line %d: %w", fileName, lineNumber+1, readErr)
		}
		if errors.Is(readErr, io.EOF) && line == "" && !oversized {
			break
		}

		lineNumber++
		if err := checkContext(ctx); err != nil {
			return err
		}
		if lineNumber == 1 {
			line = strings.TrimPrefix(line, "\ufeff")
		}
		if !oversized && p.cfg.SkipEmptyLine && strings.TrimSpace(line) == "" {
			if errors.Is(readErr, io.EOF) {
				break
			}
			continue
		}
		if sawFileFooter {
			return fmt.Errorf("parse %s at line %d: record appears after file footer", fileName, lineNumber)
		}

		if oversized {
			recordType, complete := fieldFromBoundedPrefix(line, p.cfg.Delimiter, p.sectioned.RecordTypeIndex)
			if complete && recordType == p.sectioned.DataCode {
				recordNumber++
				if err := yield(p.dataError(fileName, recordNumber, lineNumber, "RECORD_TOO_LARGE",
					"logical record exceeds PARSER_MAX_RECORD_BYTES", oversizedRawMarker)); err != nil {
					return err
				}
			} else {
				return fmt.Errorf("parse %s at line %d: structural record exceeds PARSER_MAX_RECORD_BYTES", fileName, lineNumber)
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
			continue
		}

		fieldCount := strings.Count(line, p.cfg.Delimiter) + 1
		if fieldCount > configuredMaxFields(p.cfg.MaxFields) {
			recordType, _ := fieldFromBoundedPrefix(line, p.cfg.Delimiter, p.sectioned.RecordTypeIndex)
			if recordType == p.sectioned.DataCode {
				recordNumber++
				if err := yield(p.dataError(fileName, recordNumber, lineNumber, "RECORD_TOO_COMPLEX",
					"logical record exceeds PARSER_MAX_FIELDS", tooComplexRawMarker)); err != nil {
					return err
				}
			} else {
				return fmt.Errorf("parse %s at line %d: structural record exceeds PARSER_MAX_FIELDS", fileName, lineNumber)
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
			continue
		}

		fields := strings.Split(line, p.cfg.Delimiter)
		if p.sectioned.RecordTypeIndex >= len(fields) {
			return fmt.Errorf("parse %s at line %d: record type field index %d is out of range", fileName, lineNumber, p.sectioned.RecordTypeIndex)
		}
		recordType := fields[p.sectioned.RecordTypeIndex]
		switch recordType {
		case p.sectioned.FileHeaderCode:
			if p.sectioned.FileHeaderCode == "" {
				return fmt.Errorf("parse %s at line %d: unrecognized record type", fileName, lineNumber)
			}
			if sawFileHeader || sawSection || active != nil {
				return fmt.Errorf("parse %s at line %d: file header is duplicated or out of sequence", fileName, lineNumber)
			}
			sawFileHeader = true

		case p.sectioned.SectionHeaderCode:
			if !sawFileHeader {
				return fmt.Errorf("parse %s at line %d: section header appears before file header", fileName, lineNumber)
			}
			if active != nil && p.sectioned.SectionFooterCode != "" {
				return fmt.Errorf("parse %s at line %d: active section is missing its footer", fileName, lineNumber)
			}
			schema, err := p.parseSectionHeader(fields)
			if err != nil {
				return fmt.Errorf("parse %s section header at line %d: %w", fileName, lineNumber, err)
			}
			active = schema
			sawSection = true

		case p.sectioned.DataCode:
			recordNumber++
			if active == nil {
				if err := yield(p.dataError(fileName, recordNumber, lineNumber, "SECTION_HEADER_MISSING",
					"data record has no active section header", line)); err != nil {
					return err
				}
				break
			}
			if p.sectioned.SectionKeyIndex >= len(fields) {
				if err := yield(p.dataError(fileName, recordNumber, lineNumber, "COLUMN_COUNT_MISMATCH",
					fmt.Sprintf("section key field index %d is out of range", p.sectioned.SectionKeyIndex), line)); err != nil {
					return err
				}
				break
			}
			if fields[p.sectioned.SectionKeyIndex] != active.key {
				if err := yield(p.dataError(fileName, recordNumber, lineNumber, "SECTION_KEY_MISMATCH",
					"data record section key does not match the active section", line)); err != nil {
					return err
				}
				break
			}
			expected := p.sectioned.DataStartIndex + len(active.headers)
			if len(fields) < expected || (!p.cfg.AllowExtraColumns && len(fields) > expected) {
				if err := yield(p.dataError(fileName, recordNumber, lineNumber, "COLUMN_COUNT_MISMATCH",
					fmt.Sprintf("expected %d columns for the active section, got %d", expected, len(fields)), line)); err != nil {
					return err
				}
				break
			}
			if err := yield(p.sourceRecord(fileName, recordNumber, lineNumber, line, fields, active.headers)); err != nil {
				return err
			}

		case p.sectioned.SectionFooterCode:
			if p.sectioned.SectionFooterCode == "" {
				return fmt.Errorf("parse %s at line %d: unrecognized record type", fileName, lineNumber)
			}
			if active == nil {
				return fmt.Errorf("parse %s at line %d: section footer has no active section", fileName, lineNumber)
			}
			key, err := p.sectionKey(fields)
			if err != nil {
				return fmt.Errorf("parse %s at line %d: %w", fileName, lineNumber, err)
			}
			if key != active.key {
				return fmt.Errorf("parse %s at line %d: section footer key does not match the active section", fileName, lineNumber)
			}
			active = nil

		case p.sectioned.FileFooterCode:
			if p.sectioned.FileFooterCode == "" {
				return fmt.Errorf("parse %s at line %d: unrecognized record type", fileName, lineNumber)
			}
			if !sawFileHeader {
				return fmt.Errorf("parse %s at line %d: file footer appears before file header", fileName, lineNumber)
			}
			if active != nil && p.sectioned.SectionFooterCode != "" {
				return fmt.Errorf("parse %s at line %d: file footer appears before the active section footer", fileName, lineNumber)
			}
			active = nil
			sawFileFooter = true

		default:
			return fmt.Errorf("parse %s at line %d: unrecognized record type", fileName, lineNumber)
		}

		if errors.Is(readErr, io.EOF) {
			break
		}
	}

	if active != nil && p.sectioned.SectionFooterCode != "" {
		return fmt.Errorf("parse %s: active section footer is missing", fileName)
	}
	if !sawFileHeader {
		return fmt.Errorf("parse %s: file header is missing", fileName)
	}
	if p.sectioned.FileFooterCode != "" && !sawFileFooter {
		return fmt.Errorf("parse %s: file footer is missing", fileName)
	}
	return nil
}

func (p *sectionedDelimitedParser) parseSectionHeader(fields []string) (*activeSectionSchema, error) {
	key, err := p.sectionKey(fields)
	if err != nil {
		return nil, err
	}
	if p.sectioned.HeaderStartIndex >= len(fields) {
		return nil, fmt.Errorf("dynamic header starts at index %d but the record has %d fields", p.sectioned.HeaderStartIndex, len(fields))
	}
	headers, err := normalizeSectionHeaders(fields[p.sectioned.HeaderStartIndex:], p.sectioned.DuplicateHeaderPolicy)
	if err != nil {
		return nil, err
	}
	return &activeSectionSchema{key: key, headers: headers}, nil
}

func (p *sectionedDelimitedParser) sectionKey(fields []string) (string, error) {
	if p.sectioned.SectionKeyIndex >= len(fields) {
		return "", fmt.Errorf("section key field index %d is out of range", p.sectioned.SectionKeyIndex)
	}
	if fields[p.sectioned.SectionKeyIndex] == "" {
		return "", fmt.Errorf("section key is empty")
	}
	return fields[p.sectioned.SectionKeyIndex], nil
}

func normalizeSectionHeaders(headers []string, policy config.DuplicateHeaderPolicy) ([]string, error) {
	result := make([]string, len(headers))
	used := make(map[string]struct{}, len(headers))
	nextSuffix := make(map[string]int, len(headers))
	for index, name := range headers {
		if name == "" {
			return nil, fmt.Errorf("dynamic header column %d is empty", index+1)
		}
		if reservedSectionSourceName(name) {
			return nil, fmt.Errorf("dynamic header column %d uses a reserved source name", index+1)
		}
		candidate := name
		if _, exists := used[candidate]; exists {
			if policy == config.DuplicateHeaderError {
				return nil, fmt.Errorf("section header contains a duplicate dynamic column")
			}
			suffix := nextSuffix[name]
			if suffix < 2 {
				suffix = 2
			}
			for {
				candidate = name + "__" + strconv.Itoa(suffix)
				suffix++
				if _, collision := used[candidate]; !collision {
					break
				}
			}
			nextSuffix[name] = suffix
		} else {
			nextSuffix[name] = 2
		}
		used[candidate] = struct{}{}
		result[index] = strings.Clone(candidate)
	}
	return result, nil
}

func reservedSectionSourceName(name string) bool {
	switch name {
	case "record_type", "section_key", "payload":
		return true
	}
	position, err := strconv.Atoi(name)
	return err == nil && position >= 0 && strconv.Itoa(position) == name
}

func (p *sectionedDelimitedParser) sourceRecord(fileName string, recordNumber, lineNumber int64, raw string, fields, headers []string) model.SourceRecord {
	values := make(map[string]any, len(fields)+len(headers)+3)
	for index, field := range fields {
		values[strconv.Itoa(index)] = field
	}
	payload := make(map[string]any, len(headers))
	for index, name := range headers {
		value := strings.Clone(fields[p.sectioned.DataStartIndex+index])
		payload[name] = value
		values[name] = value
	}
	values["record_type"] = fields[p.sectioned.RecordTypeIndex]
	values["section_key"] = fields[p.sectioned.SectionKeyIndex]
	values["payload"] = payload
	return model.SourceRecord{
		File: fileName, RecordNumber: recordNumber, LineNumber: lineNumber,
		Values: values, Raw: raw,
	}
}

func (p *sectionedDelimitedParser) dataError(fileName string, recordNumber, lineNumber int64, code, message string, raw any) model.SourceRecord {
	return model.SourceRecord{
		File: fileName, RecordNumber: recordNumber, LineNumber: lineNumber, Raw: raw,
		Error: parseError(fileName, recordNumber, lineNumber, "", code, message, raw),
	}
}

// boundedPhysicalLineReader retains no more than maxBytes for a line and
// drains an oversized line through its newline so the next record is framed
// correctly. Its bufio buffer remains small and fixed.
type boundedPhysicalLineReader struct {
	reader   *bufio.Reader
	maxBytes int
}

func newBoundedPhysicalLineReader(r io.Reader, maxBytes int) *boundedPhysicalLineReader {
	return &boundedPhysicalLineReader{
		reader:   bufio.NewReaderSize(r, boundedReaderSize(maxBytes)),
		maxBytes: configuredMaxRecordBytes(maxBytes),
	}
}

func (r *boundedPhysicalLineReader) readLine() (string, bool, error) {
	buffer := newBoundedBytes(r.maxBytes)
	for {
		chunk, err := r.reader.ReadSlice('\n')
		lastChunk := err == nil || errors.Is(err, io.EOF)
		if lastChunk {
			chunk = bytes.TrimSuffix(chunk, []byte{'\n'})
			chunk = bytes.TrimSuffix(chunk, []byte{'\r'})
			r.appendBounded(buffer, chunk)
		} else {
			r.appendBounded(buffer, chunk)
		}

		switch {
		case err == nil:
			return string(buffer.data), buffer.exceeded, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			return string(buffer.data), buffer.exceeded, io.EOF
		default:
			return string(buffer.data), buffer.exceeded, err
		}
	}
}

func (r *boundedPhysicalLineReader) appendBounded(buffer *boundedBytes, chunk []byte) {
	if buffer.exceeded || len(chunk) == 0 {
		return
	}
	remaining := r.maxBytes - len(buffer.data)
	if len(chunk) <= remaining {
		buffer.append(chunk...)
		return
	}
	if remaining > 0 {
		buffer.append(chunk[:remaining]...)
	}
	buffer.exceeded = true
}

// fieldFromBoundedPrefix extracts a field only when its terminating delimiter
// is present. That lets an oversized data row be identified without trusting
// a truncated record type.
func fieldFromBoundedPrefix(prefix, delimiter string, wanted int) (string, bool) {
	start := 0
	for index := 0; index <= wanted; index++ {
		offset := strings.Index(prefix[start:], delimiter)
		if index == wanted {
			if offset < 0 {
				return "", false
			}
			return prefix[start : start+offset], true
		}
		if offset < 0 {
			return "", false
		}
		start += offset + len(delimiter)
	}
	return "", false
}
