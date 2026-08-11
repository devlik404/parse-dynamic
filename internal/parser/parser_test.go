package parser

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"parser-engine/internal/config"
	"parser-engine/internal/model"
)

func collectSources(t *testing.T, cfg config.ParserConfig, input string) ([]model.SourceRecord, error) {
	t.Helper()
	decoder, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	var records []model.SourceRecord
	err = decoder.Parse(context.Background(), strings.NewReader(input), "input.dat", func(record model.SourceRecord) error {
		records = append(records, record)
		return nil
	})
	return records, err
}

func TestDelimitedLiteralMultiCharacterAndRecoverableCount(t *testing.T) {
	cfg := config.ParserConfig{
		FileType:      config.FileTypeDelimited,
		Delimiter:     "||",
		HasHeader:     true,
		SkipEmptyLine: true,
		Mappings: []config.FieldMapping{
			{Source: "id", Target: "identifier"},
			{Source: "note", Target: "description"},
		},
	}
	records, err := collectSources(t, cfg, "id||note\n\n1||a|b\n2\n")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2", len(records))
	}
	if got := records[0].Values["note"]; got != "a|b" {
		t.Fatalf("literal delimiter split note = %#v", got)
	}
	if records[0].RecordNumber != 1 || records[0].LineNumber != 3 {
		t.Fatalf("first position = record %d line %d", records[0].RecordNumber, records[0].LineNumber)
	}
	if records[1].Error == nil || records[1].Error.Code != "COLUMN_COUNT_MISMATCH" {
		t.Fatalf("second error = %#v", records[1].Error)
	}
}

func TestDelimitedHeaderMismatchIsFatalBeforeRecords(t *testing.T) {
	cfg := config.ParserConfig{
		FileType:  config.FileTypeDelimited,
		Delimiter: "|",
		HasHeader: true,
		Mappings:  []config.FieldMapping{{Source: "missing", Target: "value"}},
	}
	records, err := collectSources(t, cfg, "actual\nvalue\n")
	if err == nil || !strings.Contains(err.Error(), "not present in the header") {
		t.Fatalf("error = %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("yielded %d records before header validation", len(records))
	}
}

func TestCSVQuotedDelimiterAndMultilineWithHeader(t *testing.T) {
	cfg := config.ParserConfig{
		FileType:  config.FileTypeCSV,
		HasHeader: true,
		Mappings: []config.FieldMapping{
			{Source: "id", Target: "id"},
			{Source: "note", Target: "note"},
		},
	}
	records, err := collectSources(t, cfg, "id,note\r\n1,\"hello,\nworld\"\r\n2,ok\r\n")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(records) != 2 || records[0].Values["note"] != "hello,\nworld" {
		t.Fatalf("records = %#v", records)
	}
	if records[0].LineNumber != 2 || records[1].LineNumber != 4 {
		t.Fatalf("line numbers = %d, %d", records[0].LineNumber, records[1].LineNumber)
	}
}

func TestTSVAlwaysUsesTab(t *testing.T) {
	cfg := config.ParserConfig{FileType: config.FileTypeTSV, Delimiter: ",", Columns: []config.ColumnSpec{{Name: "a"}, {Name: "b"}}}
	records, err := collectSources(t, cfg, "x\ty\n")
	if err != nil || len(records) != 1 {
		t.Fatalf("records/error = %#v/%v", records, err)
	}
	if records[0].Values["b"] != "y" {
		t.Fatalf("b = %#v", records[0].Values["b"])
	}
}

func TestFixedWidthByteRuneAndRangeError(t *testing.T) {
	byteCfg := config.ParserConfig{
		FileType:       config.FileTypeFixedWidth,
		FixedWidthUnit: "BYTE",
		FixedWidthFields: []config.FixedWidthField{
			{Name: "id", Start: 0, Length: 2},
			{Name: "code", Start: 2, Length: 3},
		},
	}
	records, err := collectSources(t, byteCfg, "01ABC\n02X\n")
	if err != nil || len(records) != 2 {
		t.Fatalf("records/error = %#v/%v", records, err)
	}
	if records[0].Values["code"] != "ABC" {
		t.Fatalf("code = %#v", records[0].Values["code"])
	}
	if records[1].Error == nil || records[1].Error.Field != "code" {
		t.Fatalf("range error = %#v", records[1].Error)
	}

	runeCfg := config.ParserConfig{
		FileType:       config.FileTypeFixedWidth,
		FixedWidthUnit: "RUNE",
		FixedWidthFields: []config.FixedWidthField{
			{Name: "symbol", Start: 0, Length: 1},
			{Name: "letter", Start: 1, Length: 1},
		},
	}
	records, err = collectSources(t, runeCfg, "界A\n")
	if err != nil || records[0].Values["letter"] != "A" {
		t.Fatalf("rune records/error = %#v/%v", records, err)
	}
}

func TestJSONSingleArrayNestedPathAutoAndNumbers(t *testing.T) {
	cases := []struct {
		name  string
		cfg   config.ParserConfig
		input string
		want  int
	}{
		{"single", config.ParserConfig{FileType: config.FileTypeJSON, JSONMode: config.JSONModeSingle}, `{"id":1}`, 1},
		{"array", config.ParserConfig{FileType: config.FileTypeJSON, JSONMode: config.JSONModeArray}, `[{"id":1},{"id":2}]`, 2},
		{"nested", config.ParserConfig{FileType: config.FileTypeJSON, JSONMode: config.JSONModeArray, JSONRecordPath: "payload.records"}, `{"ignored":[1,2,3],"payload":{"records":[{"id":1},{"id":2}]}}`, 2},
		{"auto-bom", config.ParserConfig{FileType: config.FileTypeJSON, JSONMode: config.JSONModeAuto}, "\ufeff  [{\"id\":1}]", 1},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			records, err := collectSources(t, test.cfg, test.input)
			if err != nil || len(records) != test.want {
				t.Fatalf("records/error = %#v/%v", records, err)
			}
			if number, ok := records[0].Values["id"].(json.Number); !ok || number.String() != "1" {
				t.Fatalf("id type/value = %T/%v", records[0].Values["id"], records[0].Values["id"])
			}
		})
	}
}

func TestJSONNDJSONMalformedLineIsRecoverable(t *testing.T) {
	cfg := config.ParserConfig{FileType: config.FileTypeJSON, JSONMode: config.JSONModeNDJSON}
	records, err := collectSources(t, cfg, "{\"id\":1}\n{broken}\n{\"id\":2}\n")
	if err != nil || len(records) != 3 {
		t.Fatalf("records/error = %#v/%v", records, err)
	}
	if records[1].Error == nil || records[1].Error.Code != "INVALID_JSON" || records[2].Values["id"] == nil {
		t.Fatalf("records = %#v", records)
	}
}

func TestMalformedJSONArrayIsFatal(t *testing.T) {
	cfg := config.ParserConfig{FileType: config.FileTypeJSON, JSONMode: config.JSONModeArray}
	records, err := collectSources(t, cfg, `[{"id":1},broken]`)
	if err == nil || len(records) != 1 {
		t.Fatalf("records/error = %#v/%v", records, err)
	}
}

func TestXMLRecordPathChildrenAttributesAndRepeatedValues(t *testing.T) {
	cfg := config.ParserConfig{FileType: config.FileTypeXML, XMLRecordPath: "/root/items/item"}
	input := `<root><items><item id="1"><name>A</name><tag>x</tag><tag>y</tag><amount currency="IDR">10</amount></item><item id="2"><name>B</name></item></items></root>`
	records, err := collectSources(t, cfg, input)
	if err != nil || len(records) != 2 {
		t.Fatalf("records/error = %#v/%v", records, err)
	}
	if records[0].Values["@id"] != "1" || records[0].Values["name"] != "A" {
		t.Fatalf("first values = %#v", records[0].Values)
	}
	if !reflect.DeepEqual(records[0].Values["tag"], []any{"x", "y"}) {
		t.Fatalf("tags = %#v", records[0].Values["tag"])
	}
	amount := records[0].Values["amount"].(map[string]any)
	if amount["@currency"] != "IDR" || amount["#text"] != "10" {
		t.Fatalf("amount = %#v", amount)
	}
}

func TestMalformedXMLAndMissingRecordPathAreFatal(t *testing.T) {
	malformed := config.ParserConfig{FileType: config.FileTypeXML, XMLRecordPath: "/root/item"}
	if _, err := collectSources(t, malformed, `<root><item></root>`); err == nil {
		t.Fatal("malformed XML did not fail")
	}
	missing := config.ParserConfig{FileType: config.FileTypeXML, XMLRecordPath: "/root/item"}
	if _, err := collectSources(t, missing, `<root><other/></root>`); err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("missing path error = %v", err)
	}
}

func TestStructuredFormatsRejectOversizedLogicalRecords(t *testing.T) {
	tests := []struct {
		name  string
		cfg   config.ParserConfig
		input string
	}{
		{
			name:  "CSV multiline",
			cfg:   config.ParserConfig{FileType: config.FileTypeCSV, MaxRecordBytes: 8},
			input: "\"long\nvalue\"\n",
		},
		{
			name:  "JSON object",
			cfg:   config.ParserConfig{FileType: config.FileTypeJSON, JSONMode: config.JSONModeSingle, MaxRecordBytes: 8},
			input: `{"value":"long"}`,
		},
		{
			name:  "XML subtree",
			cfg:   config.ParserConfig{FileType: config.FileTypeXML, XMLRecordPath: "/root/item", MaxRecordBytes: 8},
			input: `<root><item>long value</item></root>`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			records, err := collectSources(t, test.cfg, test.input)
			if err != nil {
				t.Fatal(err)
			}
			if len(records) != 1 || records[0].Error == nil || records[0].Error.Code != "RECORD_TOO_LARGE" {
				t.Fatalf("records = %#v", records)
			}
			if records[0].Error.RawValue == nil {
				t.Fatal("oversized record error omitted raw_value")
			}
		})
	}
}

func TestRawSkipEmptyAndCallbackError(t *testing.T) {
	cfg := config.ParserConfig{FileType: config.FileTypeRaw, SkipEmptyLine: true}
	decoder, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("stop")
	var got model.SourceRecord
	err = decoder.Parse(context.Background(), strings.NewReader("\nraw\nnext\n"), "raw.txt", func(record model.SourceRecord) error {
		got = record
		return sentinel
	})
	if !errors.Is(err, sentinel) || got.RecordNumber != 1 || got.LineNumber != 2 || got.Values["raw"] != "raw" {
		t.Fatalf("record/error = %#v/%v", got, err)
	}
}

func TestFactoryValidation(t *testing.T) {
	tests := []config.ParserConfig{
		{FileType: config.FileTypeDelimited},
		{FileType: config.FileTypeCSV, Delimiter: "||"},
		{FileType: config.FileTypeFixedWidth},
		{FileType: config.FileTypeJSON, JSONMode: "UNKNOWN"},
		{FileType: config.FileTypeXML},
		{FileType: "X"},
	}
	for _, cfg := range tests {
		if _, err := New(cfg); err == nil {
			t.Fatalf("New(%#v) succeeded", cfg)
		}
	}
}
