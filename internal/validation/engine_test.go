package validation

import (
	"testing"

	"parser-engine/internal/config"
	"parser-engine/internal/model"
)

func TestValidateRequiredMissingNilBlankAndPresent(t *testing.T) {
	validator := New(config.ParserConfig{RequiredFields: []string{"a", "b"}})
	tests := []struct {
		name   string
		fields map[string]any
		field  string
	}{
		{"missing", map[string]any{}, "a"},
		{"nil", map[string]any{"a": nil}, "a"},
		{"blank", map[string]any{"a": "  "}, "a"},
		{"second", map[string]any{"a": 0}, "b"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recordErr := validator.Validate(model.Record{File: "x", RecordNumber: 1, Fields: test.fields})
			if recordErr == nil || recordErr.Code != "REQUIRED_FIELD" || recordErr.Field != test.field {
				t.Fatalf("error = %#v", recordErr)
			}
		})
	}
	if recordErr := validator.Validate(model.Record{Fields: map[string]any{"a": 0, "b": false}}); recordErr != nil {
		t.Fatalf("present values rejected: %v", recordErr)
	}
}
