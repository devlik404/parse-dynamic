package parser

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"parser-engine/internal/config"
)

func TestEngineEndToEndAndRecordError(t *testing.T) {
	cfg := config.ParserConfig{
		FileType:  config.FileTypeDelimited,
		Delimiter: "|",
		Columns: []config.ColumnSpec{
			{Name: "id", Type: config.TypeInteger},
			{Name: "bank", Type: config.TypeString},
			{Name: "amount", Type: config.TypeDecimal},
		},
		Mappings: []config.FieldMapping{
			{Source: "0", Target: "id"},
			{Source: "1", Target: "bank"},
			{Source: "2", Target: "amount"},
		},
		TrimSpace:       true,
		UppercaseFields: []string{"bank"},
		RequiredFields:  []string{"bank"},
	}
	engine, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var results []Result
	err = engine.Process(context.Background(), strings.NewReader("1| bca |150000.25\n2||20\nbad|bri|30\n"), "input.txt", func(result Result) error {
		results = append(results, result)
		return nil
	})
	if err != nil || len(results) != 3 {
		t.Fatalf("results/error = %#v/%v", results, err)
	}
	if results[0].Record == nil || results[0].Record.Fields["bank"] != "BCA" {
		t.Fatalf("first result = %#v", results[0])
	}
	if amount, ok := results[0].Record.Fields["amount"].(json.Number); !ok || amount.String() != "150000.25" {
		t.Fatalf("amount = %T/%v", results[0].Record.Fields["amount"], results[0].Record.Fields["amount"])
	}
	if results[1].Error == nil || results[1].Error.Code != "REQUIRED_FIELD" {
		t.Fatalf("second result = %#v", results[1])
	}
	if results[2].Error == nil || results[2].Error.Code != "TYPE_CONVERSION" {
		t.Fatalf("third result = %#v", results[2])
	}
}
