package parser

import (
	"bufio"
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"parser-engine/internal/config"
	"parser-engine/internal/model"
)

const defaultMaxRecordBytes = 1024 * 1024

func newLineScanner(r interface{ Read([]byte) (int, error) }, configuredMax int) *bufio.Scanner {
	max := configuredMaxRecordBytes(configuredMax)
	initial := 64 * 1024
	if max < initial {
		initial = max
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, initial), max)
	return scanner
}

func checkContext(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func parseError(file string, record, line int64, field, code, message string, raw any) *model.RecordError {
	return &model.RecordError{
		File:         file,
		RecordNumber: record,
		LineNumber:   line,
		Field:        field,
		RawValue:     raw,
		Code:         code,
		Phase:        "parse",
		Message:      message,
	}
}

func normalizeHeader(header []string) ([]string, error) {
	result := append([]string(nil), header...)
	if len(result) > 0 {
		result[0] = strings.TrimPrefix(result[0], "\ufeff")
	}
	seen := make(map[string]struct{}, len(result))
	for i, name := range result {
		if name == "" {
			return nil, fmt.Errorf("header column %d is empty", i+1)
		}
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("duplicate header column %q", name)
		}
		seen[name] = struct{}{}
	}
	return result, nil
}

func validateHeaderMappings(header []string, mappings []config.FieldMapping) error {
	available := make(map[string]struct{}, len(header))
	for _, name := range header {
		available[name] = struct{}{}
	}
	for _, mapping := range mappings {
		selector := strings.TrimSpace(mapping.Source)
		if selector == "" || isIndexSelector(selector) {
			continue
		}
		if _, ok := available[selector]; !ok {
			return fmt.Errorf("mapped source %q is not present in the header", selector)
		}
	}
	return nil
}

func isIndexSelector(value string) bool {
	index, err := strconv.Atoi(value)
	return err == nil && index >= 0
}

func expectedFieldCount(cfg config.ParserConfig, header []string) int {
	if len(header) > 0 {
		return len(header)
	}
	if len(cfg.Columns) > 0 {
		return len(cfg.Columns)
	}
	maxIndex := -1
	for _, mapping := range cfg.Mappings {
		if index, err := strconv.Atoi(mapping.Source); err == nil && index > maxIndex {
			maxIndex = index
		}
	}
	return maxIndex + 1
}

func fieldsToValues(fields, header []string, columns []config.ColumnSpec) map[string]any {
	// Index selectors are always available. Header and configured column aliases
	// are additive, which lets the mapper stay identical for all row formats.
	values := make(map[string]any, len(fields)*2)
	for i, field := range fields {
		values[strconv.Itoa(i)] = field
		if i < len(header) {
			values[header[i]] = field
		} else if i < len(columns) && columns[i].Name != "" {
			values[columns[i].Name] = field
		}
	}
	return values
}

func countMismatch(cfg config.ParserConfig, expected, actual int) bool {
	if expected <= 0 {
		return false
	}
	return actual < expected || (!cfg.AllowExtraColumns && actual > expected)
}

func allFieldsEmpty(fields []string) bool {
	if len(fields) == 0 {
		return true
	}
	for _, field := range fields {
		if strings.TrimSpace(field) != "" {
			return false
		}
	}
	return true
}

func validCSVDelimiter(delimiter rune) bool {
	return delimiter != 0 && delimiter != '"' && delimiter != '\r' && delimiter != '\n' && utf8.ValidRune(delimiter) && delimiter != utf8.RuneError
}
