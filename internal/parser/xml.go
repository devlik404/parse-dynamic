package parser

import (
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"parser-engine/internal/config"
	"parser-engine/internal/model"
)

const maxXMLNestingDepth = 1000

type xmlParser struct {
	cfg        config.ParserConfig
	recordPath []string
}

type framedXMLRecord struct {
	data       []byte
	tooLarge   bool
	tooComplex bool
	peakCap    int
}

func (p *xmlParser) Parse(ctx context.Context, r io.Reader, fileName string, yield YieldSource) error {
	if r == nil {
		return fmt.Errorf("parse %s: reader is nil", fileName)
	}
	if yield == nil {
		return fmt.Errorf("parse %s: yield callback is nil", fileName)
	}
	framer, err := newXMLRecordFramer(contextReader{ctx: ctx, r: r}, p.recordPath, p.cfg.MaxRecordBytes, p.cfg.MaxFields)
	if err != nil {
		return fmt.Errorf("parse %s XML: %w", fileName, err)
	}
	var recordNumber int64
	for {
		if err := checkContext(ctx); err != nil {
			return err
		}
		frame, err := framer.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("parse %s XML record %d: %w", fileName, recordNumber+1, err)
		}
		recordNumber++
		if frame.tooComplex {
			recordErr := parseError(fileName, recordNumber, 0, "", "RECORD_TOO_COMPLEX",
				"logical record exceeds PARSER_MAX_FIELDS", tooComplexRawMarker)
			if err := yield(model.SourceRecord{File: fileName, RecordNumber: recordNumber, Raw: tooComplexRawMarker, Error: recordErr}); err != nil {
				return err
			}
			continue
		}
		if frame.tooLarge {
			recordErr := parseError(fileName, recordNumber, 0, "", "RECORD_TOO_LARGE",
				"logical record exceeds PARSER_MAX_RECORD_BYTES", oversizedRawMarker)
			if err := yield(model.SourceRecord{File: fileName, RecordNumber: recordNumber, Raw: oversizedRawMarker, Error: recordErr}); err != nil {
				return err
			}
			continue
		}
		value, err := decodeXMLRecord(frame.data)
		if err != nil {
			return fmt.Errorf("parse %s XML record %d: %w", fileName, recordNumber, err)
		}
		values, ok := value.(map[string]any)
		if !ok {
			values = map[string]any{"$": value, "value": value, "0": value}
		}
		if err := yield(model.SourceRecord{File: fileName, RecordNumber: recordNumber, Values: values, Raw: value}); err != nil {
			return err
		}
	}
	if recordNumber == 0 {
		return fmt.Errorf("parse %s XML: record path %q was not found", fileName, "/"+strings.Join(p.recordPath, "/"))
	}
	return nil
}

func decodeXMLRecord(data []byte) (any, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = true
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		switch typed := token.(type) {
		case xml.StartElement:
			value, err := decodeXMLNode(decoder, typed, 1)
			if err != nil {
				return nil, err
			}
			for {
				trailing, err := decoder.Token()
				if errors.Is(err, io.EOF) {
					return value, nil
				}
				if err != nil {
					return nil, err
				}
				if text, ok := trailing.(xml.CharData); !ok || strings.TrimSpace(string(text)) != "" {
					return nil, fmt.Errorf("unexpected content after XML record")
				}
			}
		case xml.CharData:
			if strings.TrimSpace(string(typed)) != "" {
				return nil, fmt.Errorf("unexpected text before XML record")
			}
		}
	}
}

// decodeXMLNode consumes through start's matching end element and represents
// child paths as nested maps. Attributes use @name and mixed text uses #text.
func decodeXMLNode(decoder *xml.Decoder, start xml.StartElement, depth int) (any, error) {
	if depth > maxXMLNestingDepth {
		return nil, fmt.Errorf("XML nesting exceeds safe limit")
	}
	object := make(map[string]any, len(start.Attr)+2)
	for _, attribute := range start.Attr {
		object["@"+attribute.Name.Local] = attribute.Value
	}
	var text strings.Builder
	childCount := 0
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		switch typed := token.(type) {
		case xml.StartElement:
			childCount++
			value, err := decodeXMLNode(decoder, typed, depth+1)
			if err != nil {
				return nil, err
			}
			addXMLValue(object, typed.Name.Local, value)
		case xml.CharData:
			text.Write([]byte(typed))
		case xml.EndElement:
			if typed.Name != start.Name {
				return nil, fmt.Errorf("unexpected closing element %q while reading %q", typed.Name.Local, start.Name.Local)
			}
			content := text.String()
			if childCount == 0 && len(start.Attr) == 0 {
				return content, nil
			}
			if strings.TrimSpace(content) != "" {
				object["#text"] = content
				object["$"] = content
			}
			return object, nil
		}
	}
}

func addXMLValue(object map[string]any, key string, value any) {
	previous, exists := object[key]
	if !exists {
		object[key] = value
		return
	}
	if values, ok := previous.([]any); ok {
		object[key] = append(values, value)
		return
	}
	object[key] = []any{previous, value}
}

type xmlMarkupKind uint8

const (
	xmlMarkupStart xmlMarkupKind = iota
	xmlMarkupEnd
	xmlMarkupOther
)

type xmlMarkup struct {
	raw             []byte
	tooLarge        bool
	tooComplex      bool
	peakCap         int
	kind            xmlMarkupKind
	qname           string
	localName       string
	selfClose       bool
	attributeCount  int
	cdata           bool
	cdataHasContent bool
}

// xmlRecordFramer scans XML markup without decoding record content. Once the
// configured element starts, exact raw subtree bytes are retained only up to
// the cap; an oversized subtree is drained to its matching end and can be
// rejected without poisoning the next record.
type xmlRecordFramer struct {
	reader           *bufio.Reader
	recordPath       []string
	maxBytes         int
	maxFields        int
	stack            []string
	localStack       []string
	capture          *boundedBytes
	recordStack      []string
	recordDepth      int
	stackBytes       int
	recordBytes      int
	recordFields     int
	recordTooComplex bool
	recordIsRoot     bool
	found            int64
	sawRoot          bool
	rootClosed       bool
}

func newXMLRecordFramer(input io.Reader, recordPath []string, maxBytes int, configuredFields ...int) (*xmlRecordFramer, error) {
	maxFields := 0
	if len(configuredFields) > 0 {
		maxFields = configuredFields[0]
	}
	reader := bufio.NewReaderSize(input, boundedReaderSize(maxBytes))
	if prefix, _ := reader.Peek(3); bytes.Equal(prefix, []byte{0xef, 0xbb, 0xbf}) {
		_, _ = reader.Discard(3)
	}
	return &xmlRecordFramer{
		reader: reader, recordPath: append([]string(nil), recordPath...),
		maxBytes: configuredMaxRecordBytes(maxBytes), maxFields: configuredMaxFields(maxFields),
	}, nil
}

func (f *xmlRecordFramer) readByte() (byte, error) {
	value, err := f.reader.ReadByte()
	if err == nil && f.capture != nil && !f.recordTooComplex {
		f.capture.append(value)
	}
	return value, err
}

func (f *xmlRecordFramer) peekByte() (byte, error) {
	values, err := f.reader.Peek(1)
	if err != nil {
		return 0, err
	}
	return values[0], nil
}

// addRecordFields accounts for the pieces that decodeXMLRecord can turn into
// map/list entries. Once the configured bound is crossed, raw capture stops,
// while the lexical framer keeps draining and validating element balance so a
// following record can still be processed.
func (f *xmlRecordFramer) addRecordFields(count int) {
	if f.capture == nil || count <= 0 || f.recordTooComplex {
		return
	}
	if count > f.maxFields-f.recordFields {
		f.recordTooComplex = true
		f.capture.disable()
		return
	}
	f.recordFields += count
}

func (f *xmlRecordFramer) next() (framedXMLRecord, error) {
	for {
		value, err := f.peekByte()
		if errors.Is(err, io.EOF) {
			if f.capture != nil || len(f.stack) > 0 {
				return framedXMLRecord{}, fmt.Errorf("unexpected end of XML input")
			}
			return framedXMLRecord{}, io.EOF
		}
		if err != nil {
			return framedXMLRecord{}, err
		}
		if value != '<' {
			if err := f.scanText(); err != nil {
				return framedXMLRecord{}, err
			}
			continue
		}

		markup, err := f.scanMarkup()
		if err != nil {
			return framedXMLRecord{}, err
		}
		if markup.cdata && f.capture == nil && len(f.stack) == 0 {
			return framedXMLRecord{}, fmt.Errorf("CDATA is not allowed outside the XML root element")
		}
		candidateRecord := false
		if f.capture == nil && markup.kind == xmlMarkupStart {
			candidateRecord = pathMatchesElement(f.localStack, markup.localName, f.recordPath)
		}
		if markup.tooComplex {
			if f.capture == nil && !candidateRecord {
				return framedXMLRecord{}, fmt.Errorf("XML markup exceeds PARSER_MAX_FIELDS")
			}
		} else if !markup.tooLarge {
			if err := validateXMLMarkup(markup.raw); err != nil {
				return framedXMLRecord{}, err
			}
		} else if f.capture == nil && !candidateRecord {
			// Non-record markup still has to be structurally validated. It cannot
			// be buffered beyond the record cap, so abort safely.
			return framedXMLRecord{}, fmt.Errorf("XML markup token exceeds PARSER_MAX_RECORD_BYTES")
		}

		if f.capture != nil {
			if markup.tooComplex {
				f.addRecordFields(f.maxFields + 1)
			}
			if markup.cdataHasContent {
				f.addRecordFields(1)
			}
			switch markup.kind {
			case xmlMarkupStart:
				// The record element itself is implicit. Every descendant
				// element and every attribute can create decoded structure.
				f.addRecordFields(1 + markup.attributeCount)
				if !markup.selfClose {
					if f.recordDepth >= maxXMLNestingDepth {
						return framedXMLRecord{}, fmt.Errorf("XML nesting exceeds safe limit")
					}
					if f.recordBytes > f.maxBytes-len(markup.qname) {
						return framedXMLRecord{}, fmt.Errorf("XML element stack exceeds PARSER_MAX_RECORD_BYTES")
					}
					f.recordDepth++
					f.recordStack = append(f.recordStack, markup.qname)
					f.recordBytes += len(markup.qname)
				}
			case xmlMarkupEnd:
				if len(f.recordStack) == 0 || f.recordStack[len(f.recordStack)-1] != markup.qname {
					return framedXMLRecord{}, fmt.Errorf("unexpected closing element %q", markup.qname)
				}
				f.recordBytes -= len(f.recordStack[len(f.recordStack)-1])
				f.recordStack = f.recordStack[:len(f.recordStack)-1]
				f.recordDepth--
				if f.recordDepth == 0 {
					capture := f.capture
					tooComplex := f.recordTooComplex
					f.capture = nil
					f.recordStack = nil
					f.recordBytes = 0
					f.recordFields = 0
					f.recordTooComplex = false
					if f.recordIsRoot {
						f.rootClosed = true
						f.recordIsRoot = false
					}
					f.found++
					return framedXMLRecord{
						data: capture.data, tooLarge: capture.exceeded,
						tooComplex: tooComplex, peakCap: capture.peakCap,
					}, nil
				}
			}
			continue
		}

		switch markup.kind {
		case xmlMarkupStart:
			if candidateRecord {
				isRoot := len(f.stack) == 0
				if isRoot {
					if f.sawRoot || f.rootClosed {
						return framedXMLRecord{}, fmt.Errorf("XML contains multiple root elements")
					}
					f.sawRoot = true
				}
				capture := newBoundedBytes(f.maxBytes)
				f.capture = capture
				f.recordFields = 0
				f.recordTooComplex = false
				if markup.tooComplex {
					f.addRecordFields(f.maxFields + 1)
				} else {
					f.addRecordFields(markup.attributeCount)
				}
				if !f.recordTooComplex && !markup.tooLarge {
					capture.append(markup.raw...)
				} else if markup.tooLarge {
					capture.exceeded = true
				}
				if markup.selfClose {
					tooComplex := f.recordTooComplex
					f.capture = nil
					f.recordFields = 0
					f.recordTooComplex = false
					if isRoot {
						f.rootClosed = true
					}
					f.found++
					return framedXMLRecord{
						data: capture.data, tooLarge: capture.exceeded,
						tooComplex: tooComplex, peakCap: capture.peakCap,
					}, nil
				}
				f.recordDepth = 1
				f.recordStack = []string{markup.qname}
				f.recordBytes = len(markup.qname)
				f.recordIsRoot = isRoot
				continue
			}
			if !f.sawRoot {
				f.sawRoot = true
			} else if len(f.stack) == 0 && f.rootClosed {
				return framedXMLRecord{}, fmt.Errorf("XML contains multiple root elements")
			}
			if !markup.selfClose {
				if len(f.stack) >= maxXMLNestingDepth {
					return framedXMLRecord{}, fmt.Errorf("XML nesting exceeds safe limit")
				}
				if f.stackBytes > f.maxBytes-len(markup.qname) {
					return framedXMLRecord{}, fmt.Errorf("XML element stack exceeds PARSER_MAX_RECORD_BYTES")
				}
				f.stack = append(f.stack, markup.qname)
				f.localStack = append(f.localStack, markup.localName)
				f.stackBytes += len(markup.qname)
			} else if len(f.stack) == 0 {
				f.rootClosed = true
			}
		case xmlMarkupEnd:
			if len(f.stack) == 0 || f.stack[len(f.stack)-1] != markup.qname {
				return framedXMLRecord{}, fmt.Errorf("unexpected closing element %q", markup.qname)
			}
			f.stackBytes -= len(f.stack[len(f.stack)-1])
			f.stack = f.stack[:len(f.stack)-1]
			f.localStack = f.localStack[:len(f.localStack)-1]
			if len(f.stack) == 0 {
				f.rootClosed = true
			}
		}
	}
}

func (f *xmlRecordFramer) scanText() error {
	entity := make([]byte, 0, 16)
	inEntity := false
	hasContent := false
	markContent := func() {
		if !hasContent {
			hasContent = true
			f.addRecordFields(1)
		}
	}
	var previous [2]rune
	for {
		value, err := f.peekByte()
		if errors.Is(err, io.EOF) || value == '<' {
			if inEntity {
				return fmt.Errorf("unterminated XML entity")
			}
			return nil
		}
		if err != nil {
			return err
		}
		character, err := f.readXMLRune()
		if err != nil {
			return err
		}
		if !validXMLCharacter(character) {
			return fmt.Errorf("invalid XML character")
		}
		if f.capture == nil && len(f.stack) == 0 && !isXMLSpaceRune(character) {
			return fmt.Errorf("text is not allowed outside the XML root element")
		}
		if previous[0] == ']' && previous[1] == ']' && character == '>' {
			return fmt.Errorf("CDATA terminator is not allowed in XML text")
		}
		previous[0], previous[1] = previous[1], character
		if !inEntity {
			if character == '&' {
				inEntity = true
				entity = entity[:0]
				markContent()
			} else if !isXMLSpaceRune(character) {
				markContent()
			}
			continue
		}
		if character == ';' {
			if !validXMLEntity(entity) {
				return fmt.Errorf("invalid or unsupported XML entity")
			}
			inEntity = false
			continue
		}
		if character > 0x7f || len(entity) >= 64 || character == '&' || character == '<' || isXMLSpaceRune(character) {
			return fmt.Errorf("invalid XML entity")
		}
		entity = append(entity, byte(character))
	}
}

func (f *xmlRecordFramer) readXMLRune() (rune, error) {
	first, err := f.peekByte()
	if err != nil {
		return 0, err
	}
	width := 1
	switch {
	case first < utf8.RuneSelf:
		width = 1
	case first&0xe0 == 0xc0:
		width = 2
	case first&0xf0 == 0xe0:
		width = 3
	case first&0xf8 == 0xf0:
		width = 4
	default:
		return 0, fmt.Errorf("invalid UTF-8 in XML text")
	}
	encoded, err := f.reader.Peek(width)
	if err != nil {
		return 0, fmt.Errorf("invalid UTF-8 in XML text")
	}
	character, decodedWidth := utf8.DecodeRune(encoded)
	if decodedWidth != width || (character == utf8.RuneError && width == 1) {
		return 0, fmt.Errorf("invalid UTF-8 in XML text")
	}
	for index := 0; index < width; index++ {
		if _, err := f.readByte(); err != nil {
			return 0, err
		}
	}
	return character, nil
}

func (f *xmlRecordFramer) scanMarkup() (xmlMarkup, error) {
	token := newBoundedBytes(f.maxBytes)
	read := func() (byte, error) {
		value, err := f.readByte()
		if err == nil {
			token.append(value)
		}
		return value, err
	}
	if value, err := read(); err != nil || value != '<' {
		return xmlMarkup{}, fmt.Errorf("expected XML markup")
	}
	next, err := read()
	if err != nil {
		return xmlMarkup{}, err
	}
	if next == '?' {
		if err := scanUntil(read, []byte("?>")); err != nil {
			return xmlMarkup{}, err
		}
		return xmlMarkup{raw: token.data, tooLarge: token.exceeded, peakCap: token.peakCap, kind: xmlMarkupOther}, nil
	}
	if next == '!' {
		cdata, cdataHasContent, err := f.scanDeclaration(read, token)
		if err != nil {
			return xmlMarkup{}, err
		}
		return xmlMarkup{
			raw: token.data, tooLarge: token.exceeded, peakCap: token.peakCap,
			kind: xmlMarkupOther, cdata: cdata, cdataHasContent: cdataHasContent,
		}, nil
	}

	kind := xmlMarkupStart
	if next == '/' {
		kind = xmlMarkupEnd
	}
	quote := byte(0)
	lastNonSpace := byte(0)
	attributeCount := 0
	tooComplex := false
	for {
		value, err := read()
		if err != nil {
			return xmlMarkup{}, err
		}
		if quote != 0 {
			if value == quote {
				quote = 0
			}
			continue
		}
		if value == '\'' || value == '"' {
			quote = value
			continue
		}
		if value == '>' {
			break
		}
		if kind == xmlMarkupStart && value == '=' {
			if attributeCount <= f.maxFields {
				attributeCount++
			}
			if attributeCount > f.maxFields {
				tooComplex = true
				token.disable()
				if f.capture != nil {
					f.recordTooComplex = true
					f.capture.disable()
				}
			}
		}
		if !isXMLSpace(value) {
			lastNonSpace = value
		}
	}
	qname, local, selfClose, err := classifyElementMarkup(token.data, kind)
	if err != nil {
		return xmlMarkup{}, err
	}
	if kind == xmlMarkupStart {
		selfClose = lastNonSpace == '/'
	}
	return xmlMarkup{
		raw: token.data, tooLarge: token.exceeded, peakCap: token.peakCap,
		tooComplex: tooComplex, kind: kind, qname: qname, localName: local,
		selfClose: selfClose, attributeCount: attributeCount,
	}, nil
}

func (f *xmlRecordFramer) scanDeclaration(read func() (byte, error), _ *boundedBytes) (bool, bool, error) {
	// Comments, CDATA, and directives have different terminators. Read a small
	// prefix first; it is already bounded by the token collector.
	prefix := make([]byte, 0, 7)
	for len(prefix) < 7 {
		value, err := read()
		if err != nil {
			return false, false, err
		}
		prefix = append(prefix, value)
		joined := string(prefix)
		if joined == "--" {
			return false, false, scanXMLComment(read)
		}
		if joined == "[CDATA[" {
			hasContent, err := scanCDATA(read)
			return true, hasContent, err
		}
		if joined == "DOCTYPE" {
			return false, false, fmt.Errorf("DOCTYPE is not supported")
		}
		if !strings.HasPrefix("[CDATA[", joined) && !strings.HasPrefix("--", joined) && !strings.HasPrefix("DOCTYPE", joined) {
			return false, false, fmt.Errorf("unsupported XML declaration")
		}
	}
	return false, false, fmt.Errorf("unsupported XML declaration")
}

func scanXMLComment(read func() (byte, error)) error {
	hyphens := 0
	for {
		value, err := read()
		if err != nil {
			return err
		}
		if value == '-' {
			hyphens++
			if hyphens > 2 {
				return fmt.Errorf("double hyphen is not allowed in XML comments")
			}
			continue
		}
		if hyphens == 2 {
			if value == '>' {
				return nil
			}
			return fmt.Errorf("double hyphen is not allowed in XML comments")
		}
		hyphens = 0
	}
}

func scanUntil(read func() (byte, error), terminator []byte) error {
	window := make([]byte, 0, len(terminator))
	for {
		value, err := read()
		if err != nil {
			return err
		}
		if len(window) == cap(window) {
			copy(window, window[1:])
			window = window[:len(window)-1]
		}
		window = append(window, value)
		if len(window) == len(terminator) && bytes.Equal(window, terminator) {
			return nil
		}
	}
}

func scanCDATA(read func() (byte, error)) (bool, error) {
	terminator := []byte("]]>")
	window := make([]byte, 0, len(terminator))
	hasContent := false
	for {
		value, err := read()
		if err != nil {
			return false, err
		}
		if len(window) == cap(window) {
			if !isXMLSpace(window[0]) {
				hasContent = true
			}
			copy(window, window[1:])
			window = window[:len(window)-1]
		}
		window = append(window, value)
		if len(window) == len(terminator) && bytes.Equal(window, terminator) {
			return hasContent, nil
		}
	}
}

func classifyElementMarkup(raw []byte, kind xmlMarkupKind) (qname, local string, selfClose bool, err error) {
	index := 1
	if kind == xmlMarkupEnd {
		index++
	}
	start := index
	for index < len(raw) && raw[index] != ' ' && raw[index] != '\t' && raw[index] != '\r' && raw[index] != '\n' && raw[index] != '/' && raw[index] != '>' {
		index++
	}
	if index == start {
		return "", "", false, fmt.Errorf("XML element name is missing")
	}
	qname = string(raw[start:index])
	local = qname
	if separator := strings.LastIndexByte(qname, ':'); separator >= 0 {
		local = qname[separator+1:]
	}
	if kind == xmlMarkupStart {
		end := len(raw) - 2
		for end >= 0 && isXMLSpace(raw[end]) {
			end--
		}
		selfClose = end >= 0 && raw[end] == '/'
	}
	return qname, local, selfClose, nil
}

func validateXMLMarkup(raw []byte) error {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	decoder.Strict = true
	for {
		_, err := decoder.RawToken()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func equalPath(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func pathMatchesElement(parents []string, localName string, path []string) bool {
	if len(parents)+1 != len(path) || len(path) == 0 || localName != path[len(path)-1] {
		return false
	}
	for index := range parents {
		if parents[index] != path[index] {
			return false
		}
	}
	return true
}

func validXMLEntity(value []byte) bool {
	switch string(value) {
	case "lt", "gt", "amp", "apos", "quot":
		return true
	}
	if len(value) < 2 || value[0] != '#' {
		return false
	}
	start := 1
	hex := false
	if value[start] == 'x' {
		hex = true
		start++
	}
	if start >= len(value) {
		return false
	}
	for _, character := range value[start:] {
		if hex {
			if !isHex(character) {
				return false
			}
		} else if character < '0' || character > '9' {
			return false
		}
	}
	base := 10
	if hex {
		base = 16
	}
	codepoint, err := strconv.ParseUint(string(value[start:]), base, 32)
	return err == nil && codepoint <= utf8.MaxRune && validXMLCharacter(rune(codepoint))
}

func isXMLSpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n'
}

func isXMLSpaceRune(value rune) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n'
}

func validXMLCharacter(value rune) bool {
	return value == 0x9 || value == 0xa || value == 0xd ||
		(value >= 0x20 && value <= 0xd7ff) ||
		(value >= 0xe000 && value <= 0xfffd) ||
		(value >= 0x10000 && value <= utf8.MaxRune)
}
