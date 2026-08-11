package transformation

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"

	"parser-engine/internal/config"
	"parser-engine/internal/model"
)

func TestApplyOrderingRulesAndConversions(t *testing.T) {
	cfg := config.ParserConfig{
		Columns: []config.ColumnSpec{
			{Name: "integer", Type: config.TypeInteger},
			{Name: "int64", Type: config.TypeInt64},
			{Name: "decimal", Type: config.TypeDecimal},
			{Name: "float", Type: config.TypeFloat},
			{Name: "boolean", Type: config.TypeBoolean},
			{Name: "date", Type: config.TypeDate},
			{Name: "datetime", Type: config.TypeDateTime},
			{Name: "uuid", Type: config.TypeUUID},
			{Name: "json", Type: config.TypeJSON},
			{Name: "any", Type: config.TypeAny},
			{Name: "name", Type: config.TypeString},
		},
		DateFormat:      "20060102",
		DateTimeFormat:  "20060102 15:04:05",
		Timezone:        "Asia/Jakarta",
		TrimSpace:       true,
		NullIfEmpty:     true,
		DefaultValues:   map[string]string{"name": "unknown"},
		UppercaseFields: []string{"name"},
		Transforms: []config.TransformRule{
			{Field: "name", Operation: config.TransformReplace, From: "UNKNOWN", To: "KNOWN"},
			{Field: "name", Operation: config.TransformSubstring, Start: 1, Length: 3},
		},
	}
	engine, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	anyValue := map[string]any{"number": json.Number("1.20")}
	record, recordErr := engine.Apply(model.Record{File: "x", RecordNumber: 1, Fields: map[string]any{
		"integer": "42", "int64": "9223372036854775807", "decimal": "001.2300", "float": "2.5",
		"boolean": "true", "date": "20260811", "datetime": "20260811 10:20:30",
		"uuid": "550E8400-E29B-41D4-A716-446655440000", "json": `{"x":1}`, "any": anyValue, "name": "   ",
	}})
	if recordErr != nil {
		t.Fatal(recordErr)
	}
	if record.Fields["integer"] != 42 || record.Fields["int64"] != int64(9223372036854775807) {
		t.Fatalf("integers = %#v/%#v", record.Fields["integer"], record.Fields["int64"])
	}
	if decimal := record.Fields["decimal"].(json.Number); decimal.String() != "1.2300" {
		t.Fatalf("decimal = %q", decimal)
	}
	if record.Fields["float"] != 2.5 || record.Fields["boolean"] != true {
		t.Fatalf("float/bool = %#v/%#v", record.Fields["float"], record.Fields["boolean"])
	}
	if date := record.Fields["date"].(model.Date); date.Format("2006-01-02") != "2026-08-11" {
		t.Fatalf("date = %v", date)
	}
	if datetime := record.Fields["datetime"].(time.Time); datetime.Location().String() != "Asia/Jakarta" {
		t.Fatalf("datetime location = %v", datetime.Location())
	}
	if record.Fields["uuid"] != model.UUID("550e8400-e29b-41d4-a716-446655440000") || record.Fields["name"] != "NOW" {
		t.Fatalf("uuid/name = %#v/%#v", record.Fields["uuid"], record.Fields["name"])
	}
	if !reflect.DeepEqual(record.Fields["any"], anyValue) {
		t.Fatalf("any was not passed through: %#v", record.Fields["any"])
	}
	if raw := record.Fields["json"].(json.RawMessage); string(raw) != `{"x":1}` {
		t.Fatalf("json value = %s", raw)
	}
}

func TestTrimDetachesSmallResultFromLargeBacking(t *testing.T) {
	input := strings.Repeat(" ", 1024*1024) + "id"
	engine, err := New(config.ParserConfig{TrimSpace: true, MaxRecordBytes: len(input) + 1})
	if err != nil {
		t.Fatal(err)
	}
	record, recordErr := engine.Apply(model.Record{Fields: map[string]any{"value": input}})
	if recordErr != nil {
		t.Fatal(recordErr)
	}
	trimmed := record.Fields["value"].(string)
	if trimmed != "id" || unsafe.StringData(trimmed) == unsafe.StringData(input) {
		t.Fatal("trimmed canonical value retained its large input backing")
	}
}

func TestJSONTypeNormalizesEveryScalarWithoutSQLNullConfusion(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{name: "native string", value: "hello", want: `"hello"`},
		{name: "native bool", value: true, want: `true`},
		{name: "native number", value: json.Number("12.50"), want: `12.50`},
		{name: "JSON null", value: nil, want: `null`},
		{name: "object", value: map[string]any{"ok": true}, want: `{"ok":true}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			engine, err := New(config.ParserConfig{FileType: config.FileTypeJSON, Columns: []config.ColumnSpec{{Name: "payload", Type: config.TypeJSON}}})
			if err != nil {
				t.Fatal(err)
			}
			record, recordErr := engine.Apply(model.Record{Fields: map[string]any{"payload": test.value}})
			if recordErr != nil {
				t.Fatal(recordErr)
			}
			if got := string(record.Fields["payload"].(json.RawMessage)); got != test.want {
				t.Fatalf("JSON = %s, want %s", got, test.want)
			}
		})
	}

	engine, _ := New(config.ParserConfig{FileType: config.FileTypeDelimited, Columns: []config.ColumnSpec{{Name: "payload", Type: config.TypeJSON}}})
	if _, recordErr := engine.Apply(model.Record{Fields: map[string]any{"payload": "not-json"}}); recordErr == nil {
		t.Fatal("invalid JSON text from a delimited source was accepted")
	}
}

func TestExplicitAliasesNullDefaultLowerAndRuneSubstring(t *testing.T) {
	cfg := config.ParserConfig{Transforms: []config.TransformRule{
		{Field: "empty", Operation: "NULL"},
		{Field: "default", Operation: "DEFAULT", Value: "VALUE"},
		{Field: "upper", Operation: "UPPER"},
		{Field: "lower", Operation: "LOWER"},
		{Field: "rune", Operation: "SUBSTRING", Start: 1, Length: 1},
	}}
	engine, _ := New(cfg)
	record, recordErr := engine.Apply(model.Record{Fields: map[string]any{"empty": "", "upper": "a", "lower": "B", "rune": "A界B"}})
	if recordErr != nil {
		t.Fatal(recordErr)
	}
	if record.Fields["empty"] != nil || record.Fields["default"] != "VALUE" || record.Fields["upper"] != "A" || record.Fields["lower"] != "b" || record.Fields["rune"] != "界" {
		t.Fatalf("fields = %#v", record.Fields)
	}
}

func TestConversionAndTransformErrorsAreStructured(t *testing.T) {
	secret := "customer-secret-123"
	engine, _ := New(config.ParserConfig{Columns: []config.ColumnSpec{{Name: "n", Type: config.TypeInteger}}})
	_, recordErr := engine.Apply(model.Record{File: "x", RecordNumber: 2, Fields: map[string]any{"n": secret}})
	if recordErr == nil || recordErr.Code != "TYPE_CONVERSION" || recordErr.RawValue != secret {
		t.Fatalf("conversion error = %#v", recordErr)
	}
	if strings.Contains(recordErr.Message, secret) || strings.Contains(recordErr.Error(), secret) {
		t.Fatalf("conversion message leaked raw value: %q", recordErr.Message)
	}
	engine, _ = New(config.ParserConfig{Transforms: []config.TransformRule{{Field: "x", Operation: config.TransformSubstring, Start: 5, Length: 1}}})
	_, recordErr = engine.Apply(model.Record{Fields: map[string]any{"x": "a"}})
	if recordErr == nil || recordErr.Code != "TRANSFORM_ERROR" {
		t.Fatalf("transform error = %#v", recordErr)
	}
}

func TestSubstringHugeLengthReturnsErrorWithoutOverflow(t *testing.T) {
	engine, _ := New(config.ParserConfig{Transforms: []config.TransformRule{{Field: "x", Operation: config.TransformSubstring, Start: 1, Length: int(^uint(0) >> 1)}}})
	if _, recordErr := engine.Apply(model.Record{Fields: map[string]any{"x": "short"}}); recordErr == nil || recordErr.Code != "TRANSFORM_ERROR" {
		t.Fatalf("substring overflow error = %#v", recordErr)
	}
}

func TestReplaceExpansionIsRejectedBeforeAllocation(t *testing.T) {
	engine, err := New(config.ParserConfig{
		MaxRecordBytes: 64,
		Transforms: []config.TransformRule{{
			Field: "x", Operation: config.TransformReplace, From: "a", To: strings.Repeat("b", 32),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, recordErr := engine.Apply(model.Record{Fields: map[string]any{"x": "aaaa"}})
	if recordErr == nil || recordErr.Code != "TRANSFORM_OUTPUT_TOO_LARGE" {
		t.Fatalf("replace expansion error = %#v", recordErr)
	}
}

func TestRepeatedTransformsStopAtCumulativeWorkLimit(t *testing.T) {
	rules := make([]config.TransformRule, 3)
	for index := range rules {
		rules[index] = config.TransformRule{Field: "x", Operation: config.TransformReplace, From: "z", To: "q"}
	}
	engine, err := New(config.ParserConfig{MaxRecordBytes: 1024, Transforms: rules})
	if err != nil {
		t.Fatal(err)
	}
	value := strings.Repeat("a", 1000)
	_, recordErr := engine.Apply(model.Record{Fields: map[string]any{"x": value}})
	if recordErr == nil || recordErr.Code != "TRANSFORM_WORK_LIMIT" {
		t.Fatalf("work-limit error = %#v", recordErr)
	}
	if strings.Contains(recordErr.Message, value) {
		t.Fatal("work-limit message leaked the raw value")
	}
}

func TestConversionExpansionHonorsCanonicalRecordLimit(t *testing.T) {
	engine, err := New(config.ParserConfig{
		FileType:       config.FileTypeJSON,
		MaxRecordBytes: 16,
		Columns:        []config.ColumnSpec{{Name: "payload", Type: config.TypeJSON}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, recordErr := engine.Apply(model.Record{Fields: map[string]any{"payload": strings.Repeat("<", 8)}})
	if recordErr == nil || recordErr.Code != "TYPE_CONVERSION" {
		t.Fatalf("JSON expansion error = %#v", recordErr)
	}
}

func TestJSONSizePreflightNeverUndercountsMarshal(t *testing.T) {
	values := []any{
		"plain",
		"<script>\n\u2028\xff",
		map[string]any{"<key>": []any{"value", json.Number("12.50"), true, nil}},
		[]any{float64(1.25), int64(-9), map[string]any{"nested": "&"}},
	}
	for _, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		estimated, err := jsonEncodedSize(value, 0)
		if err != nil {
			t.Fatal(err)
		}
		if estimated < int64(len(encoded)) {
			t.Fatalf("estimate %d undercounts %d-byte encoding %q", estimated, len(encoded), encoded)
		}
	}
}

func TestNewRejectsTimezone(t *testing.T) {
	if _, err := New(config.ParserConfig{Timezone: "Not/A_Real_Zone"}); err == nil {
		t.Fatal("invalid timezone accepted")
	}
}
