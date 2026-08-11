// Package transformation applies configuration-driven canonical field
// operations and type conversions.
package transformation

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
	"unicode/utf8"

	"parser-engine/internal/config"
	"parser-engine/internal/model"
)

var decimalPattern = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

const (
	defaultMaxRecordBytes = 1024 * 1024
	hardMaxRecordBytes    = 8 * 1024 * 1024
)

type Engine struct {
	cfg       config.ParserConfig
	location  *time.Location
	typeByKey map[string]config.DataType
	maxBytes  int64
}

func New(cfg config.ParserConfig) (*Engine, error) {
	location := time.UTC
	if strings.TrimSpace(cfg.Timezone) != "" {
		loaded, err := time.LoadLocation(cfg.Timezone)
		if err != nil {
			return nil, fmt.Errorf("invalid PARSER_TIMEZONE %q: %w", cfg.Timezone, err)
		}
		location = loaded
	}
	types := make(map[string]config.DataType, len(cfg.Columns)+len(cfg.FixedWidthFields))
	for _, column := range cfg.Columns {
		types[column.Name] = column.Type
	}
	for _, field := range cfg.FixedWidthFields {
		if _, exists := types[field.Name]; !exists {
			types[field.Name] = field.Type
		}
	}
	maxBytes := cfg.MaxRecordBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxRecordBytes
	} else if maxBytes > hardMaxRecordBytes {
		maxBytes = hardMaxRecordBytes
	}
	return &Engine{cfg: cfg, location: location, typeByKey: types, maxBytes: int64(maxBytes)}, nil
}

func (e *Engine) Apply(input model.Record) (model.Record, *model.RecordError) {
	record := input
	record.Fields = cloneFields(input.Fields)
	if e.exceedsRecordLimit(record) {
		return record, outputLimitError(record, "", nil)
	}

	if e.cfg.TrimSpace {
		for key, value := range record.Fields {
			if text, ok := value.(string); ok {
				record.Fields[key] = strings.Clone(strings.TrimSpace(text))
			}
		}
	}
	if e.cfg.NullIfEmpty {
		for key, value := range record.Fields {
			if text, ok := value.(string); ok && text == "" {
				record.Fields[key] = nil
			}
		}
	}

	defaultKeys := make([]string, 0, len(e.cfg.DefaultValues))
	for field := range e.cfg.DefaultValues {
		defaultKeys = append(defaultKeys, field)
	}
	sort.Strings(defaultKeys)
	for _, field := range defaultKeys {
		if isEmpty(record.Fields[field]) {
			record.Fields[field] = e.cfg.DefaultValues[field]
		}
	}
	for _, field := range e.cfg.UppercaseFields {
		if value, ok := record.Fields[field].(string); ok {
			converted, ok := boundedCase(value, true, e.maxBytes)
			if !ok {
				return record, outputLimitError(record, field, value)
			}
			record.Fields[field] = converted
		}
	}
	for _, field := range e.cfg.LowercaseFields {
		if value, ok := record.Fields[field].(string); ok {
			converted, ok := boundedCase(value, false, e.maxBytes)
			if !ok {
				return record, outputLimitError(record, field, value)
			}
			record.Fields[field] = converted
		}
	}
	if e.exceedsRecordLimit(record) {
		return record, outputLimitError(record, "", nil)
	}

	for _, rule := range e.cfg.Transforms {
		if recordErr := e.applyRule(&record, rule); recordErr != nil {
			return record, recordErr
		}
		if e.exceedsRecordLimit(record) {
			return record, outputLimitError(record, rule.Field, record.Fields[rule.Field])
		}
	}

	keys := make([]string, 0, len(e.typeByKey))
	for key := range e.typeByKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, field := range keys {
		value, exists := record.Fields[field]
		if !exists || (value == nil && e.typeByKey[field] != config.TypeJSON) {
			continue
		}
		converted, err := e.convert(value, e.typeByKey[field])
		if err != nil {
			return record, recordError(record, field, "TYPE_CONVERSION", err.Error(), value, "transformation")
		}
		record.Fields[field] = converted
		if e.exceedsRecordLimit(record) {
			return record, outputLimitError(record, field, value)
		}
	}
	return record, nil
}

func (e *Engine) applyRule(record *model.Record, rule config.TransformRule) *model.RecordError {
	value, exists := record.Fields[rule.Field]
	operation := strings.ToUpper(strings.TrimSpace(string(rule.Operation)))
	switch operation {
	case "TRIM":
		if text, ok := value.(string); ok {
			record.Fields[rule.Field] = strings.Clone(strings.TrimSpace(text))
		}
	case "UPPERCASE", "UPPER":
		if text, ok := value.(string); ok {
			converted, bounded := boundedCase(text, true, e.maxBytes)
			if !bounded {
				return outputLimitError(*record, rule.Field, value)
			}
			record.Fields[rule.Field] = converted
		}
	case "LOWERCASE", "LOWER":
		if text, ok := value.(string); ok {
			converted, bounded := boundedCase(text, false, e.maxBytes)
			if !bounded {
				return outputLimitError(*record, rule.Field, value)
			}
			record.Fields[rule.Field] = converted
		}
	case "REPLACE":
		if text, ok := value.(string); ok {
			converted, bounded := boundedReplace(text, rule.From, rule.To, e.maxBytes)
			if !bounded {
				return outputLimitError(*record, rule.Field, value)
			}
			record.Fields[rule.Field] = converted
		}
	case "SUBSTRING":
		if !exists || value == nil {
			return nil
		}
		text, ok := value.(string)
		if !ok {
			return recordError(*record, rule.Field, "TRANSFORM_ERROR", "SUBSTRING requires a string", value, "transformation")
		}
		converted, ok := boundedSubstring(text, rule.Start, rule.Length)
		if !ok {
			return recordError(*record, rule.Field, "TRANSFORM_ERROR", "SUBSTRING range is invalid", value, "transformation")
		}
		record.Fields[rule.Field] = converted
	case "DEFAULT_VALUE", "DEFAULT":
		if !exists || isEmpty(value) {
			record.Fields[rule.Field] = rule.Value
		}
	case "NULL_IF_EMPTY", "NULL":
		if text, ok := value.(string); ok && text == "" {
			record.Fields[rule.Field] = nil
		}
	default:
		return recordError(*record, rule.Field, "TRANSFORM_ERROR", fmt.Sprintf("unsupported transform operation %q", rule.Operation), value, "transformation")
	}
	return nil
}

func (e *Engine) convert(value any, dataType config.DataType) (any, error) {
	typeName := config.DataType(strings.ToLower(strings.TrimSpace(string(dataType))))
	switch typeName {
	case config.TypeAny:
		return value, nil
	case "", config.TypeString:
		return convertString(value, e.maxBytes)
	case config.TypeInteger:
		integer, err := strconv.ParseInt(strings.TrimSpace(toText(value)), 10, strconv.IntSize)
		if err != nil {
			return nil, fmt.Errorf("value is not a valid integer")
		}
		return int(integer), nil
	case config.TypeInt64:
		integer, err := strconv.ParseInt(strings.TrimSpace(toText(value)), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("value is not a valid int64")
		}
		return integer, nil
	case config.TypeDecimal:
		text := strings.TrimSpace(toText(value))
		if !decimalPattern.MatchString(text) {
			return nil, fmt.Errorf("value is not a valid decimal")
		}
		normalized := normalizeDecimal(text)
		if int64(len(normalized)) > e.maxBytes {
			return nil, fmt.Errorf("converted value exceeds PARSER_MAX_RECORD_BYTES")
		}
		return json.Number(normalized), nil
	case config.TypeFloat:
		floating, err := strconv.ParseFloat(strings.TrimSpace(toText(value)), 64)
		if err != nil || math.IsInf(floating, 0) || math.IsNaN(floating) {
			return nil, fmt.Errorf("value is not a valid finite float")
		}
		return floating, nil
	case config.TypeBoolean:
		boolean, err := strconv.ParseBool(strings.TrimSpace(toText(value)))
		if err != nil {
			return nil, fmt.Errorf("value is not a valid boolean")
		}
		return boolean, nil
	case config.TypeDate:
		if date, ok := value.(model.Date); ok {
			return date, nil
		}
		if timestamp, ok := value.(time.Time); ok {
			return model.NewDate(timestamp), nil
		}
		layout := e.cfg.DateFormat
		if layout == "" {
			layout = "2006-01-02"
		}
		parsed, err := time.ParseInLocation(layout, strings.TrimSpace(toText(value)), e.location)
		if err != nil {
			return nil, fmt.Errorf("value is not a valid date for the configured layout")
		}
		return model.NewDate(parsed), nil
	case config.TypeDateTime:
		if timestamp, ok := value.(time.Time); ok {
			return timestamp, nil
		}
		layout := e.cfg.DateTimeFormat
		if layout == "" {
			layout = time.RFC3339
		}
		parsed, err := time.ParseInLocation(layout, strings.TrimSpace(toText(value)), e.location)
		if err != nil {
			return nil, fmt.Errorf("value is not a valid datetime for the configured layout")
		}
		return parsed, nil
	case config.TypeUUID:
		text := strings.ToLower(strings.TrimSpace(toText(value)))
		if !validUUID(text) {
			return nil, fmt.Errorf("value is not a valid UUID")
		}
		return model.UUID(strings.Clone(text)), nil
	case config.TypeJSON:
		return e.convertJSON(value)
	default:
		return nil, fmt.Errorf("unsupported data type %q", dataType)
	}
}

func (e *Engine) convertJSON(value any) (json.RawMessage, error) {
	var encoded []byte
	var err error
	switch typed := value.(type) {
	case json.RawMessage:
		if int64(len(typed)) > e.maxBytes {
			return nil, fmt.Errorf("JSON value exceeds PARSER_MAX_RECORD_BYTES")
		}
		encoded = append([]byte(nil), typed...)
	case string:
		if e.cfg.FileType == config.FileTypeJSON {
			// A string emitted by the JSON decoder is already a native JSON string,
			// not a second document that should be parsed again.
			if size, sizeErr := jsonEncodedSize(typed, 0); sizeErr != nil || size > e.maxBytes {
				return nil, fmt.Errorf("JSON value exceeds PARSER_MAX_RECORD_BYTES")
			}
			encoded, err = json.Marshal(typed)
		} else {
			trimmed := strings.TrimSpace(typed)
			if int64(len(trimmed)) > e.maxBytes {
				return nil, fmt.Errorf("JSON value exceeds PARSER_MAX_RECORD_BYTES")
			}
			encoded = []byte(trimmed)
		}
	default:
		if size, sizeErr := jsonEncodedSize(typed, 0); sizeErr != nil {
			return nil, fmt.Errorf("value is not valid JSON")
		} else if size > e.maxBytes {
			return nil, fmt.Errorf("JSON value exceeds PARSER_MAX_RECORD_BYTES")
		}
		encoded, err = json.Marshal(typed)
	}
	if err != nil || !json.Valid(encoded) {
		return nil, fmt.Errorf("value is not valid JSON")
	}
	if int64(len(encoded)) > e.maxBytes {
		return nil, fmt.Errorf("JSON value exceeds PARSER_MAX_RECORD_BYTES")
	}
	return json.RawMessage(encoded), nil
}

// normalizeDecimal keeps the exact numeric value and fractional scale while
// producing syntax accepted by encoding/json and database normalization. File
// formats commonly contain leading zeroes or .5, neither of which is valid JSON
// number syntax even though both are valid decimal input.
func normalizeDecimal(value string) string {
	exponent := ""
	if index := strings.IndexAny(value, "eE"); index >= 0 {
		exponent = value[index:]
		value = value[:index]
	}
	sign := ""
	if strings.HasPrefix(value, "+") {
		value = value[1:]
	} else if strings.HasPrefix(value, "-") {
		sign = "-"
		value = value[1:]
	}
	integer, fraction, hasFraction := value, "", false
	if index := strings.IndexByte(value, '.'); index >= 0 {
		integer, fraction, hasFraction = value[:index], value[index+1:], true
	}
	integer = strings.TrimLeft(integer, "0")
	if integer == "" {
		integer = "0"
	}
	if hasFraction {
		if fraction == "" {
			fraction = "0"
		}
		integer += "." + fraction
	}
	return strings.Clone(sign + integer + exponent)
}

func convertString(value any, maxBytes int64) (string, error) {
	switch typed := value.(type) {
	case string:
		if int64(len(typed)) > maxBytes {
			return "", fmt.Errorf("string value exceeds PARSER_MAX_RECORD_BYTES")
		}
		return typed, nil
	case []byte:
		if !utf8.Valid(typed) {
			return "", fmt.Errorf("byte value is not valid UTF-8")
		}
		if int64(len(typed)) > maxBytes {
			return "", fmt.Errorf("string value exceeds PARSER_MAX_RECORD_BYTES")
		}
		return string(typed), nil
	case map[string]any, []any:
		if size, err := jsonEncodedSize(typed, 0); err != nil {
			return "", err
		} else if size > maxBytes {
			return "", fmt.Errorf("string value exceeds PARSER_MAX_RECORD_BYTES")
		}
		encoded, err := json.Marshal(typed)
		return string(encoded), err
	default:
		text := toText(value)
		if int64(len(text)) > maxBytes {
			return "", fmt.Errorf("string value exceeds PARSER_MAX_RECORD_BYTES")
		}
		return text, nil
	}
}

func toText(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	if number, ok := value.(json.Number); ok {
		return number.String()
	}
	return fmt.Sprint(value)
}

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func isEmpty(value any) bool {
	if value == nil {
		return true
	}
	text, ok := value.(string)
	return ok && text == ""
}

func cloneFields(fields map[string]any) map[string]any {
	copy := make(map[string]any, len(fields))
	for key, value := range fields {
		copy[key] = value
	}
	return copy
}

func recordError(record model.Record, field, code, message string, raw any, phase string) *model.RecordError {
	return &model.RecordError{
		File:         record.File,
		RecordNumber: record.RecordNumber,
		LineNumber:   record.LineNumber,
		Field:        field,
		RawValue:     raw,
		Code:         code,
		Phase:        phase,
		Message:      message,
	}
}

func (e *Engine) exceedsRecordLimit(record model.Record) bool {
	return model.EstimateFieldsPayloadBytes(record.Fields) > e.maxBytes
}

func outputLimitError(record model.Record, field string, raw any) *model.RecordError {
	return recordError(record, field, "TRANSFORM_OUTPUT_TOO_LARGE", "transformed record exceeds PARSER_MAX_RECORD_BYTES", raw, "transformation")
}
