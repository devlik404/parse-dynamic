// Package mapping maps format-specific source selectors to canonical fields.
package mapping

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"parser-engine/internal/config"
	"parser-engine/internal/model"
)

type Engine struct {
	mappings []config.FieldMapping
	columns  []config.ColumnSpec
}

func New(cfg config.ParserConfig) *Engine {
	return &Engine{
		mappings: append([]config.FieldMapping(nil), cfg.Mappings...),
		columns:  append([]config.ColumnSpec(nil), cfg.Columns...),
	}
}

func (e *Engine) Map(source model.SourceRecord) (model.Record, *model.RecordError) {
	record := model.Record{
		File:         source.File,
		RecordNumber: source.RecordNumber,
		LineNumber:   source.LineNumber,
		Fields:       make(map[string]any),
	}
	if source.Error != nil {
		return record, normalizeSourceError(source)
	}

	mappings := e.mappings
	if len(mappings) == 0 && len(e.columns) > 0 {
		mappings = make([]config.FieldMapping, 0, len(e.columns))
		for index, column := range e.columns {
			selector := column.Name
			if _, exists := source.Values[selector]; !exists {
				selector = strconv.Itoa(index)
			}
			mappings = append(mappings, config.FieldMapping{Source: selector, Target: column.Name})
		}
	}

	if len(mappings) == 0 {
		for key, value := range source.Values {
			record.Fields[key] = detachSelectedValue(value)
		}
		return record, nil
	}

	for _, mapping := range mappings {
		value, found := Select(source.Values, mapping.Source)
		if !found {
			return record, &model.RecordError{
				File:         source.File,
				RecordNumber: source.RecordNumber,
				LineNumber:   source.LineNumber,
				Field:        mapping.Target,
				RawValue:     source.Raw,
				Code:         "MISSING_SOURCE",
				Phase:        "mapping",
				Message:      fmt.Sprintf("source selector %q was not found", mapping.Source),
			}
		}
		record.Fields[mapping.Target] = detachSelectedValue(unwrapXMLText(value))
	}
	return record, nil
}

// Delimited and encoding/csv parsers may return short field strings backed by
// the complete logical-record buffer. Detach selected scalar values at the
// canonical boundary so an ignored multi-megabyte column cannot stay retained
// until the database batch flushes.
func detachSelectedValue(value any) any {
	switch typed := value.(type) {
	case string:
		return strings.Clone(typed)
	case json.RawMessage:
		return json.RawMessage(append([]byte(nil), typed...))
	case []byte:
		return append([]byte(nil), typed...)
	default:
		return value
	}
}

func normalizeSourceError(source model.SourceRecord) *model.RecordError {
	copy := *source.Error
	if copy.File == "" {
		copy.File = source.File
	}
	if copy.RecordNumber == 0 {
		copy.RecordNumber = source.RecordNumber
	}
	if copy.LineNumber == 0 {
		copy.LineNumber = source.LineNumber
	}
	if copy.Phase == "" {
		copy.Phase = "parse"
	}
	return &copy
}

// Select resolves an exact key first and then a simple dot/slash path. Array
// indexes may be used as path segments. Exact lookup first preserves JSON keys
// that themselves contain dots.
func Select(values map[string]any, selector string) (any, bool) {
	if values == nil {
		return nil, false
	}
	if value, ok := values[selector]; ok {
		return value, true
	}
	segments := splitSelector(selector)
	if len(segments) == 0 {
		return nil, false
	}
	var current any = values
	for _, segment := range segments {
		switch typed := current.(type) {
		case map[string]any:
			value, exists := typed[segment]
			if !exists {
				return nil, false
			}
			current = value
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(typed) {
				return nil, false
			}
			current = typed[index]
		default:
			// Also accept named map/slice types produced by callers.
			value := reflect.ValueOf(current)
			if !value.IsValid() {
				return nil, false
			}
			if value.Kind() == reflect.Map && value.Type().Key().Kind() == reflect.String {
				entry := value.MapIndex(reflect.ValueOf(segment).Convert(value.Type().Key()))
				if !entry.IsValid() {
					return nil, false
				}
				current = entry.Interface()
				continue
			}
			return nil, false
		}
	}
	return current, true
}

func splitSelector(selector string) []string {
	selector = strings.TrimSpace(selector)
	selector = strings.TrimPrefix(selector, "$")
	selector = strings.Trim(selector, "./ ")
	if selector == "" {
		return nil
	}
	return strings.FieldsFunc(selector, func(r rune) bool { return r == '.' || r == '/' })
}

func unwrapXMLText(value any) any {
	if object, ok := value.(map[string]any); ok {
		if text, exists := object["#text"]; exists {
			return text
		}
	}
	return value
}
