// Package validation enforces schema rules on transformed canonical records.
package validation

import (
	"strings"

	"parser-engine/internal/config"
	"parser-engine/internal/model"
)

type Engine struct {
	required []string
}

func New(cfg config.ParserConfig) *Engine {
	return &Engine{required: append([]string(nil), cfg.RequiredFields...)}
}

func (e *Engine) Validate(record model.Record) *model.RecordError {
	for _, field := range e.required {
		value, exists := record.Fields[field]
		if !exists || value == nil || isBlank(value) {
			return &model.RecordError{
				File:         record.File,
				RecordNumber: record.RecordNumber,
				LineNumber:   record.LineNumber,
				Field:        field,
				RawValue:     value,
				Code:         "REQUIRED_FIELD",
				Phase:        "validation",
				Message:      "required field is missing or empty",
			}
		}
	}
	return nil
}

func isBlank(value any) bool {
	text, ok := value.(string)
	return ok && strings.TrimSpace(text) == ""
}
