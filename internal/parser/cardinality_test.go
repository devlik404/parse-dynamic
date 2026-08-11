package parser

import (
	"strings"
	"testing"

	"parser-engine/internal/config"
	"parser-engine/internal/model"
)

func assertTooComplexRecord(t *testing.T, record model.SourceRecord) {
	t.Helper()
	if record.Error == nil {
		t.Fatalf("record error = nil; record = %#v", record)
	}
	if record.Error.Code != "RECORD_TOO_COMPLEX" {
		t.Fatalf("record error code = %q, want RECORD_TOO_COMPLEX; record = %#v", record.Error.Code, record)
	}
	if record.Raw != tooComplexRawMarker {
		t.Fatalf("record raw = %#v, want %q", record.Raw, tooComplexRawMarker)
	}
	if record.Error.RawValue != tooComplexRawMarker {
		t.Fatalf("record error raw value = %#v, want %q", record.Error.RawValue, tooComplexRawMarker)
	}
}

func TestDelimitedMaxFieldsIsRecoverableAndUsesLiteralDelimiter(t *testing.T) {
	cfg := config.ParserConfig{
		FileType:       config.FileTypeDelimited,
		Delimiter:      "||",
		MaxRecordBytes: 1024,
		MaxFields:      3,
		Columns: []config.ColumnSpec{
			{Name: "id"},
			{Name: "note"},
			{Name: "status"},
		},
	}
	records, err := collectSources(t, cfg, "1||a|b||ready||unexpected\nok||x|y||done\n")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("record count = %d, want 2; records = %#v", len(records), records)
	}
	assertTooComplexRecord(t, records[0])
	if records[0].RecordNumber != 1 || records[0].LineNumber != 1 {
		t.Fatalf("complex record position = record %d, line %d", records[0].RecordNumber, records[0].LineNumber)
	}
	if records[1].Error != nil || records[1].RecordNumber != 2 || records[1].LineNumber != 2 {
		t.Fatalf("resynchronized record = %#v", records[1])
	}
	if records[1].Values["id"] != "ok" || records[1].Values["note"] != "x|y" || records[1].Values["status"] != "done" {
		t.Fatalf("resynchronized values = %#v", records[1].Values)
	}
}

func TestDelimitedMaxFieldsHeaderIsFatal(t *testing.T) {
	cfg := config.ParserConfig{
		FileType:       config.FileTypeDelimited,
		Delimiter:      "||",
		HasHeader:      true,
		MaxRecordBytes: 1024,
		MaxFields:      3,
	}
	records, err := collectSources(t, cfg, "id||name||status||extra\n1||A||ok\n")
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "header") || !strings.Contains(err.Error(), "PARSER_MAX_FIELDS") {
		t.Fatalf("records/error = %#v/%v, want fatal oversized header", records, err)
	}
	if len(records) != 0 {
		t.Fatalf("yielded %d records before rejecting header: %#v", len(records), records)
	}
}

func TestCSVMaxFieldsQuotedMultibyteDelimiterResynchronizes(t *testing.T) {
	cfg := config.ParserConfig{
		FileType:       config.FileTypeCSV,
		Delimiter:      "§",
		MaxRecordBytes: 1024,
		MaxFields:      3,
		Columns: []config.ColumnSpec{
			{Name: "id"},
			{Name: "note"},
			{Name: "status"},
		},
	}
	input := "1§\"quoted§delimiter\"§ready§unexpected\nok§\"still§one\"§done\n"
	records, err := collectSources(t, cfg, input)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("record count = %d, want 2; records = %#v", len(records), records)
	}
	assertTooComplexRecord(t, records[0])
	if records[1].Error != nil || records[1].Values["id"] != "ok" || records[1].Values["note"] != "still§one" || records[1].Values["status"] != "done" {
		t.Fatalf("resynchronized record = %#v", records[1])
	}
}

func TestCSVMaxFieldsHeaderIsFatal(t *testing.T) {
	cfg := config.ParserConfig{
		FileType:       config.FileTypeCSV,
		Delimiter:      "§",
		HasHeader:      true,
		MaxRecordBytes: 1024,
		MaxFields:      3,
	}
	records, err := collectSources(t, cfg, "\"full§name\"§id§status§extra\nA§1§ok\n")
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "header") || !strings.Contains(err.Error(), "PARSER_MAX_FIELDS") {
		t.Fatalf("records/error = %#v/%v, want fatal oversized header", records, err)
	}
	if len(records) != 0 {
		t.Fatalf("yielded %d records before rejecting header: %#v", len(records), records)
	}
}

func TestCSVMaxFieldsStopsCaptureAndStillFindsNextRecord(t *testing.T) {
	const maxRecordBytes = 1 << 20
	input := "a§\"b§inside\"§c§d" + strings.Repeat("x", 128*1024) + "\nnext§ok\n"
	framer := newCSVRecordFramer(strings.NewReader(input), '§', maxRecordBytes, 3)

	first, err := framer.next()
	if err != nil {
		t.Fatalf("first next() error = %v", err)
	}
	if !first.tooComplex || first.tooLarge {
		t.Fatalf("first frame flags: tooComplex=%t tooLarge=%t len=%d cap=%d peak=%d", first.tooComplex, first.tooLarge, len(first.data), cap(first.data), first.peakCap)
	}
	if cap(first.data) > 256 || first.peakCap > 256 {
		t.Fatalf("complex CSV capture grew after the field cap: len=%d cap=%d peak=%d", len(first.data), cap(first.data), first.peakCap)
	}

	second, err := framer.next()
	if err != nil {
		t.Fatalf("second next() error = %v", err)
	}
	if second.tooComplex || second.tooLarge || string(second.data) != "next§ok" {
		t.Fatalf("second frame = %#v (data %q)", second, second.data)
	}
}

func TestJSONMaxFieldsAcrossStreamingModes(t *testing.T) {
	complexValue := `{"root":{"leaf":1},"items":[1,2]}` // 5 members/elements.
	validValue := `{"id":2}`
	tests := []struct {
		name      string
		cfg       config.ParserConfig
		input     string
		wantCount int
	}{
		{
			name: "single",
			cfg: config.ParserConfig{
				FileType: config.FileTypeJSON, JSONMode: config.JSONModeSingle,
				MaxRecordBytes: 1 << 20, MaxFields: 4,
			},
			input: complexValue, wantCount: 1,
		},
		{
			name: "array",
			cfg: config.ParserConfig{
				FileType: config.FileTypeJSON, JSONMode: config.JSONModeArray,
				MaxRecordBytes: 1 << 20, MaxFields: 4,
			},
			input: `[` + complexValue + `,` + validValue + `]`, wantCount: 2,
		},
		{
			name: "auto value stream",
			cfg: config.ParserConfig{
				FileType: config.FileTypeJSON, JSONMode: config.JSONModeAuto,
				MaxRecordBytes: 1 << 20, MaxFields: 4,
			},
			input: complexValue + "\n" + validValue, wantCount: 2,
		},
		{
			name: "record path ignores envelope cardinality",
			cfg: config.ParserConfig{
				FileType: config.FileTypeJSON, JSONMode: config.JSONModeArray,
				JSONRecordPath: "payload.records", MaxRecordBytes: 1 << 20, MaxFields: 4,
			},
			input: `{"metadata":[0,1,2,3,4,5,6,7],"payload":{"records":[` + complexValue + `,` + validValue + `]}}`, wantCount: 2,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			records, err := collectSources(t, test.cfg, test.input)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if len(records) != test.wantCount {
				t.Fatalf("record count = %d, want %d; records = %#v", len(records), test.wantCount, records)
			}
			assertTooComplexRecord(t, records[0])
			if test.wantCount > 1 {
				if records[1].Error != nil || records[1].Values["id"] == nil || records[1].RecordNumber != 2 {
					t.Fatalf("resynchronized record = %#v", records[1])
				}
			}
		})
	}
}

func TestJSONNDJSONMaxFieldsIsRecoverable(t *testing.T) {
	cfg := config.ParserConfig{
		FileType: config.FileTypeJSON, JSONMode: config.JSONModeNDJSON,
		MaxRecordBytes: 1 << 20, MaxFields: 4,
	}
	input := "{\"root\":{\"leaf\":1},\"items\":[1,2]}\n{\"id\":2}\n"
	records, err := collectSources(t, cfg, input)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("record count = %d, want 2; records = %#v", len(records), records)
	}
	assertTooComplexRecord(t, records[0])
	if records[0].LineNumber != 1 || records[1].LineNumber != 2 || records[1].Error != nil || records[1].Values["id"] == nil {
		t.Fatalf("NDJSON resynchronization = %#v", records)
	}
}

func TestJSONNDJSONPathCountsSelectedRecordsNotEnvelope(t *testing.T) {
	cfg := config.ParserConfig{
		FileType: config.FileTypeJSON, JSONMode: config.JSONModeNDJSON,
		JSONRecordPath: "payload.records", MaxRecordBytes: 1 << 20, MaxFields: 4,
	}
	complexValue := `{"root":{"leaf":1},"items":[1,2]}`
	input := `{"metadata":[0,1,2,3,4,5,6,7],"payload":{"records":[` + complexValue + `,{"id":2}]}}` + "\n"
	records, err := collectSources(t, cfg, input)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("record count = %d, want 2; records = %#v", len(records), records)
	}
	assertTooComplexRecord(t, records[0])
	if records[1].Error != nil || records[1].Values["id"] == nil {
		t.Fatalf("selected valid record = %#v", records[1])
	}
}

func TestJSONMaxFieldsStopsLexicalCapture(t *testing.T) {
	const maxRecordBytes = 1 << 20
	input := `{"a":1,"b":[1],"c":3,"d":"` + strings.Repeat("x", 128*1024) + `"}`
	scanner, err := newJSONLexScanner(strings.NewReader(input), maxRecordBytes, 4)
	if err != nil {
		t.Fatal(err)
	}
	frame, err := scanner.readBoundedValue(maxRecordBytes, 0)
	if err != nil {
		t.Fatalf("readBoundedValue() error = %v", err)
	}
	if !frame.tooComplex || frame.tooLarge {
		t.Fatalf("frame flags: tooComplex=%t tooLarge=%t len=%d cap=%d peak=%d", frame.tooComplex, frame.tooLarge, len(frame.data), cap(frame.data), frame.peakCap)
	}
	if cap(frame.data) > 256 || frame.peakCap > 256 {
		t.Fatalf("complex JSON capture grew after the field cap: len=%d cap=%d peak=%d", len(frame.data), cap(frame.data), frame.peakCap)
	}
	if err := scanner.requireEOF(); err != nil {
		t.Fatalf("scanner did not drain the complex value: %v", err)
	}
}

func TestJSONMaxFieldsAllowsExactLimitAndIgnoresStringPunctuation(t *testing.T) {
	cfg := config.ParserConfig{
		FileType: config.FileTypeJSON, JSONMode: config.JSONModeSingle,
		MaxRecordBytes: 1024, MaxFields: 3,
	}
	// Two object members plus one nested array element. JSON-looking bytes in
	// strings are content, not structural nodes.
	records, err := collectSources(t, cfg, `{"a":"[{,}:]","b":["{},[]"]}`)
	if err != nil || len(records) != 1 || records[0].Error != nil {
		t.Fatalf("exact JSON cardinality rejected: records/error = %#v/%v", records, err)
	}
}

func TestXMLMaxFieldsCountsAttributesElementsTextAndCDATA(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "attributes",
			input: `<root><item a="1" b="2" c="3"/><item id="ok"/></root>`,
		},
		{
			name:  "descendant elements",
			input: `<root><item><a/><b/><c/></item><item id="ok"/></root>`,
		},
		{
			name:  "non-whitespace text",
			input: `<root><item><a>alpha</a><b/></item><item id="ok"/></root>`,
		},
		{
			name:  "CDATA text",
			input: `<root><item><a/><b><![CDATA[beta]]></b></item><item id="ok"/></root>`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := config.ParserConfig{
				FileType: config.FileTypeXML, XMLRecordPath: "/root/item",
				MaxRecordBytes: 1 << 20, MaxFields: 2,
			}
			records, err := collectSources(t, cfg, test.input)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if len(records) != 2 {
				t.Fatalf("record count = %d, want 2; records = %#v", len(records), records)
			}
			assertTooComplexRecord(t, records[0])
			if records[1].Error != nil || records[1].Values["@id"] != "ok" || records[1].RecordNumber != 2 {
				t.Fatalf("resynchronized XML record = %#v", records[1])
			}
		})
	}
}

func TestXMLMaxFieldsDoesNotCountWhitespaceOnlyText(t *testing.T) {
	cfg := config.ParserConfig{
		FileType: config.FileTypeXML, XMLRecordPath: "/root/item",
		MaxRecordBytes: 1 << 20, MaxFields: 1,
	}
	records, err := collectSources(t, cfg, "<root><item>\n  <child> \t </child>\n</item></root>")
	if err != nil || len(records) != 1 || records[0].Error != nil {
		t.Fatalf("whitespace-only XML text consumed cardinality: records/error = %#v/%v", records, err)
	}
}

func TestXMLMaxFieldsAllowsExactAttributesAndEmptyCDATA(t *testing.T) {
	cfg := config.ParserConfig{
		FileType: config.FileTypeXML, XMLRecordPath: "/root/item",
		MaxRecordBytes: 1024, MaxFields: 2,
	}
	// Equals signs inside quoted values are not extra attributes, and empty
	// CDATA does not emit a nonempty text node.
	records, err := collectSources(t, cfg, `<root><item a="x=y" b="z=q"><![CDATA[]]></item></root>`)
	if err != nil || len(records) != 1 || records[0].Error != nil {
		t.Fatalf("exact XML cardinality rejected: records/error = %#v/%v", records, err)
	}
}

func TestXMLMaxFieldsStopsMarkupCaptureAndResynchronizes(t *testing.T) {
	const maxRecordBytes = 1 << 20
	input := `<root><item a="1" b="2" c="3" d="` + strings.Repeat("x", 128*1024) + `"/><item id="ok"/></root>`
	framer, err := newXMLRecordFramer(strings.NewReader(input), []string{"root", "item"}, maxRecordBytes, 3)
	if err != nil {
		t.Fatal(err)
	}

	first, err := framer.next()
	if err != nil {
		t.Fatalf("first next() error = %v", err)
	}
	if !first.tooComplex || first.tooLarge {
		t.Fatalf("first frame flags: tooComplex=%t tooLarge=%t len=%d cap=%d peak=%d", first.tooComplex, first.tooLarge, len(first.data), cap(first.data), first.peakCap)
	}
	if cap(first.data) > 256 || first.peakCap > 256 {
		t.Fatalf("complex XML capture grew after the field cap: len=%d cap=%d peak=%d", len(first.data), cap(first.data), first.peakCap)
	}

	second, err := framer.next()
	if err != nil {
		t.Fatalf("second next() error = %v", err)
	}
	if second.tooComplex || second.tooLarge || !strings.Contains(string(second.data), `id="ok"`) {
		t.Fatalf("second frame = %#v (data %q)", second, second.data)
	}
}
