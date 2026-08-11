package mapping

import (
	"strings"
	"testing"
	"unsafe"

	"parser-engine/internal/config"
	"parser-engine/internal/model"
)

func TestMapNestedExactKeyArrayAndXMLText(t *testing.T) {
	cfg := config.ParserConfig{Mappings: []config.FieldMapping{
		{Source: "literal.dot", Target: "literal"},
		{Source: "transaction.items.1.id", Target: "item_id"},
		{Source: "amount", Target: "amount"},
	}}
	source := model.SourceRecord{
		File: "x", RecordNumber: 2, LineNumber: 3,
		Values: map[string]any{
			"literal.dot": "exact",
			"transaction": map[string]any{"items": []any{
				map[string]any{"id": "a"}, map[string]any{"id": "b"},
			}},
			"amount": map[string]any{"@currency": "IDR", "#text": "10"},
		},
	}
	record, recordErr := New(cfg).Map(source)
	if recordErr != nil {
		t.Fatal(recordErr)
	}
	if record.Fields["literal"] != "exact" || record.Fields["item_id"] != "b" || record.Fields["amount"] != "10" {
		t.Fatalf("fields = %#v", record.Fields)
	}
}

func TestMapDetachesSelectedStringFromLargeSourceBacking(t *testing.T) {
	backing := strings.Repeat("x", 1024*1024)
	selected := backing[:2]
	mapper := New(config.ParserConfig{Mappings: []config.FieldMapping{{Source: "0", Target: "id"}}})
	record, recordErr := mapper.Map(model.SourceRecord{Values: map[string]any{"0": selected}})
	if recordErr != nil {
		t.Fatal(recordErr)
	}
	mapped := record.Fields["id"].(string)
	if mapped != selected {
		t.Fatalf("mapped value = %q", mapped)
	}
	if unsafe.StringData(mapped) == unsafe.StringData(selected) {
		t.Fatal("canonical field retained the source record backing string")
	}
}

func TestMapMissingSourceAndParserErrorPassThrough(t *testing.T) {
	mapper := New(config.ParserConfig{Mappings: []config.FieldMapping{{Source: "missing", Target: "id"}}})
	_, recordErr := mapper.Map(model.SourceRecord{File: "x", RecordNumber: 1, Values: map[string]any{}})
	if recordErr == nil || recordErr.Code != "MISSING_SOURCE" || recordErr.Field != "id" {
		t.Fatalf("error = %#v", recordErr)
	}
	parseErr := &model.RecordError{Code: "BAD", Message: "bad"}
	_, recordErr = mapper.Map(model.SourceRecord{File: "x", RecordNumber: 4, LineNumber: 8, Error: parseErr})
	if recordErr == parseErr || recordErr.File != "x" || recordErr.RecordNumber != 4 || recordErr.Phase != "parse" {
		t.Fatalf("normalized error = %#v", recordErr)
	}
}

func TestMapImplicitColumnsAndIdentity(t *testing.T) {
	mapper := New(config.ParserConfig{Columns: []config.ColumnSpec{{Name: "id"}, {Name: "name"}}})
	record, recordErr := mapper.Map(model.SourceRecord{Values: map[string]any{"0": "1", "1": "A"}})
	if recordErr != nil || record.Fields["id"] != "1" || record.Fields["name"] != "A" {
		t.Fatalf("record/error = %#v/%v", record, recordErr)
	}
	identity, recordErr := New(config.ParserConfig{}).Map(model.SourceRecord{Values: map[string]any{"dynamic": 1}})
	if recordErr != nil || identity.Fields["dynamic"] != 1 {
		t.Fatalf("identity/error = %#v/%v", identity, recordErr)
	}
}
