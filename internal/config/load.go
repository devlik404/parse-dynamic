package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	defaultMaxRecordBytes = 1024 * 1024
	defaultMaxFields      = 10_000
	defaultBatchSize      = 1000
	defaultBatchMaxBytes  = 64 * 1024 * 1024
	defaultConnectTimeout = 30 * time.Second
)

// LookupFunc makes configuration loading deterministic and easy to test. Its
// contract is the same as os.LookupEnv.
type LookupFunc func(key string) (value string, found bool)

// LoadFromEnv loads, normalizes, and validates a complete job configuration.
func LoadFromEnv() (JobConfig, error) {
	return Load(os.LookupEnv)
}

// Load reads configuration through lookup. Invalid values are reported rather
// than silently replaced by defaults.
func Load(lookup LookupFunc) (JobConfig, error) {
	if lookup == nil {
		return JobConfig{}, &ConfigError{Problems: []string{"configuration lookup function must not be nil"}}
	}

	l := envLoader{lookup: lookup}
	cfg := JobConfig{
		Input: InputConfig{
			FilePattern:   "*",
			SkipEmptyLine: true,
		},
		Parser: ParserConfig{
			FixedWidthUnit: "BYTE",
			JSONMode:       JSONModeAuto,
			DateFormat:     "2006-01-02",
			DateTimeFormat: time.RFC3339,
			Timezone:       "UTC",
			DefaultValues:  make(map[string]string),
			MaxRecordBytes: defaultMaxRecordBytes,
			MaxFields:      defaultMaxFields,
		},
		DB: DBConfig{
			BatchSize:      defaultBatchSize,
			BatchMaxBytes:  defaultBatchMaxBytes,
			ConnectTimeout: defaultConnectTimeout,
		},
		Error: ErrorConfig{Mode: ErrorModeStrict},
	}

	cfg.Input.Path = l.stringValue("INPUT_PATH", "")
	cfg.Input.FilePattern = l.stringValue("FILE_PATTERN", cfg.Input.FilePattern)
	cfg.Input.SkipEmptyLine = l.boolAlias("INPUT_SKIP_EMPTY_LINE", "SKIP_EMPTY_LINE", cfg.Input.SkipEmptyLine)
	cfg.Parser.SkipEmptyLine = cfg.Input.SkipEmptyLine

	cfg.Parser.FileType = FileType(strings.ToUpper(l.stringValue("PARSER_FILE_TYPE", "")))
	cfg.Parser.Delimiter = l.delimiter("PARSER_DELIMITER")
	cfg.Parser.HasHeader = l.boolValue("PARSER_HAS_HEADER", false)
	cfg.Parser.FixedWidthUnit = strings.ToUpper(l.stringValue("PARSER_FIXED_WIDTH_UNIT", cfg.Parser.FixedWidthUnit))
	cfg.Parser.JSONMode = JSONMode(strings.ToUpper(l.stringValue("PARSER_JSON_MODE", string(cfg.Parser.JSONMode))))
	cfg.Parser.JSONRecordPath = l.stringValue("PARSER_JSON_RECORD_PATH", "")
	cfg.Parser.XMLRecordPath = l.stringValue("PARSER_XML_RECORD_PATH", "")
	cfg.Parser.DateFormat = l.stringValue("PARSER_DATE_FORMAT", cfg.Parser.DateFormat)
	cfg.Parser.DateTimeFormat = l.stringValue("PARSER_DATETIME_FORMAT", cfg.Parser.DateTimeFormat)
	cfg.Parser.Timezone = l.stringValue("PARSER_TIMEZONE", cfg.Parser.Timezone)
	cfg.Parser.TrimSpace = l.boolValue("PARSER_TRIM_SPACE", false)
	cfg.Parser.NullIfEmpty = l.boolValue("PARSER_NULL_IF_EMPTY", false)
	cfg.Parser.AllowExtraColumns = l.boolValue("PARSER_ALLOW_EXTRA_COLUMNS", false)
	cfg.Parser.MaxRecordBytes = l.intValue("PARSER_MAX_RECORD_BYTES", cfg.Parser.MaxRecordBytes)
	cfg.Parser.MaxFields = l.intValue("PARSER_MAX_FIELDS", cfg.Parser.MaxFields)
	cfg.Parser.UppercaseFields = l.list("PARSER_UPPERCASE_FIELDS")
	cfg.Parser.LowercaseFields = l.list("PARSER_LOWERCASE_FIELDS")
	cfg.Parser.RequiredFields = l.list("PARSER_REQUIRED_FIELDS")
	cfg.Parser.DefaultValues = l.stringMap("PARSER_DEFAULT_VALUES")
	cfg.Parser.Transforms = l.transforms()

	columnNames := l.list("PARSER_COLUMNS")
	columnTypes := l.dataTypes("PARSER_TYPES")
	if len(columnNames) != len(columnTypes) && (len(columnNames) > 0 || len(columnTypes) > 0) {
		l.problem("PARSER_COLUMNS and PARSER_TYPES must contain the same number of entries")
	}
	for index, name := range columnNames {
		dataType := DataType("")
		if index < len(columnTypes) {
			dataType = columnTypes[index]
		}
		cfg.Parser.Columns = append(cfg.Parser.Columns, ColumnSpec{Name: name, Type: dataType})
	}

	genericMappings, genericSet := l.mappings("PARSER_MAPPING")
	jsonMappings, jsonSet := l.mappings("PARSER_JSON_MAPPING")
	xmlMappings, xmlSet := l.mappings("PARSER_XML_MAPPING")
	switch cfg.Parser.FileType {
	case FileTypeJSON:
		if jsonSet {
			cfg.Parser.Mappings = jsonMappings
		} else {
			cfg.Parser.Mappings = genericMappings
		}
	case FileTypeXML:
		if xmlSet {
			cfg.Parser.Mappings = xmlMappings
		} else {
			cfg.Parser.Mappings = genericMappings
		}
	default:
		cfg.Parser.Mappings = genericMappings
		if jsonSet {
			l.problem("PARSER_JSON_MAPPING is only valid when PARSER_FILE_TYPE=JSON")
		}
		if xmlSet {
			l.problem("PARSER_XML_MAPPING is only valid when PARSER_FILE_TYPE=XML")
		}
	}
	if genericSet && jsonSet && cfg.Parser.FileType == FileTypeJSON {
		l.problem("set only one of PARSER_MAPPING and PARSER_JSON_MAPPING for JSON input")
	}
	if genericSet && xmlSet && cfg.Parser.FileType == FileTypeXML {
		l.problem("set only one of PARSER_MAPPING and PARSER_XML_MAPPING for XML input")
	}

	cfg.Parser.FixedWidthFields = l.fixedWidthFields("PARSER_FIXED_WIDTH_FIELDS")
	normalizeParser(&cfg.Parser)

	cfg.DB.Driver = normalizeDriver(l.stringValue("DB_DRIVER", ""))
	cfg.DB.DSN = l.rawString("DB_DSN", "")
	cfg.DB.Host = l.stringValue("DB_HOST", "")
	cfg.DB.Port = l.intValue("DB_PORT", defaultPort(cfg.DB.Driver))
	cfg.DB.User = l.stringValue("DB_USER", "")
	cfg.DB.Password = l.rawString("DB_PASSWORD", "")
	cfg.DB.Name = l.stringValue("DB_NAME", "")
	cfg.DB.Schema = l.stringValue("DB_SCHEMA", "")
	cfg.DB.Table = l.stringValue("DB_TABLE", "")
	cfg.DB.Columns = l.list("DB_COLUMNS")
	cfg.DB.ColumnMappings, _ = l.mappings("DB_COLUMN_MAPPING")
	cfg.DB.BatchSize = l.intValue("DB_BATCH_SIZE", cfg.DB.BatchSize)
	cfg.DB.BatchMaxBytes = l.intValue("DB_BATCH_MAX_BYTES", cfg.DB.BatchMaxBytes)
	cfg.DB.SSLMode = l.stringValue("DB_SSL_MODE", "")
	cfg.DB.ConnectTimeout = l.durationValue("DB_CONNECT_TIMEOUT", cfg.DB.ConnectTimeout)
	if len(cfg.DB.ColumnMappings) == 0 {
		for _, column := range cfg.DB.Columns {
			cfg.DB.ColumnMappings = append(cfg.DB.ColumnMappings, FieldMapping{Source: column, Target: column})
		}
	}

	cfg.Error.Mode = ErrorMode(strings.ToUpper(l.stringValue("ERROR_MODE", string(cfg.Error.Mode))))
	cfg.Error.OutputPath = l.stringValue("ERROR_OUTPUT_PATH", "")
	cfg.Error.MaxCount = l.intValue("ERROR_MAX_COUNT", 0)

	problems := append([]string(nil), l.problems...)
	problems = append(problems, validateProblems(cfg)...)
	if len(problems) > 0 {
		return JobConfig{}, newConfigError(problems)
	}
	return cfg, nil
}

type envLoader struct {
	lookup   LookupFunc
	problems []string
}

func (l *envLoader) problem(message string) {
	l.problems = append(l.problems, message)
}

func (l *envLoader) rawString(key, fallback string) string {
	value, ok := l.lookup(key)
	if !ok {
		return fallback
	}
	return value
}

func (l *envLoader) stringValue(key, fallback string) string {
	value, ok := l.lookup(key)
	if !ok {
		return fallback
	}
	return strings.TrimSpace(value)
}

func (l *envLoader) boolValue(key string, fallback bool) bool {
	value, ok := l.lookup(key)
	if !ok {
		return fallback
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		l.problem(key + " must be a valid boolean (true or false)")
		return fallback
	}
	return parsed
}

func (l *envLoader) boolAlias(primary, legacy string, fallback bool) bool {
	primaryRaw, primarySet := l.lookup(primary)
	legacyRaw, legacySet := l.lookup(legacy)
	if primarySet && legacySet && strings.TrimSpace(primaryRaw) != strings.TrimSpace(legacyRaw) {
		l.problem(primary + " conflicts with legacy alias " + legacy)
	}
	if primarySet {
		return l.parseBool(primary, primaryRaw, fallback)
	}
	if legacySet {
		return l.parseBool(legacy, legacyRaw, fallback)
	}
	return fallback
}

func (l *envLoader) parseBool(key, value string, fallback bool) bool {
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		l.problem(key + " must be a valid boolean (true or false)")
		return fallback
	}
	return parsed
}

func (l *envLoader) intValue(key string, fallback int) int {
	value, ok := l.lookup(key)
	if !ok {
		return fallback
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		l.problem(key + " must be a valid integer")
		return fallback
	}
	return parsed
}

func (l *envLoader) durationValue(key string, fallback time.Duration) time.Duration {
	value, ok := l.lookup(key)
	if !ok {
		return fallback
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		l.problem(key + " must be a valid duration such as 30s or 2m")
		return fallback
	}
	return parsed
}

func (l *envLoader) list(key string) []string {
	value, ok := l.lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for index, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			l.problem(fmt.Sprintf("%s entry %d must not be empty", key, index+1))
			continue
		}
		result = append(result, part)
	}
	return result
}

func (l *envLoader) dataTypes(key string) []DataType {
	items := l.list(key)
	result := make([]DataType, 0, len(items))
	for _, item := range items {
		result = append(result, DataType(strings.ToLower(item)))
	}
	return result
}

func (l *envLoader) mappings(key string) ([]FieldMapping, bool) {
	value, set := l.lookup(key)
	if !set || strings.TrimSpace(value) == "" {
		return nil, set
	}
	items := strings.Split(value, ",")
	result := make([]FieldMapping, 0, len(items))
	for index, item := range items {
		parts := strings.SplitN(item, ":", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			l.problem(fmt.Sprintf("%s entry %d must use non-empty source:target syntax", key, index+1))
			continue
		}
		result = append(result, FieldMapping{
			Source: strings.TrimSpace(parts[0]),
			Target: strings.TrimSpace(parts[1]),
		})
	}
	return result, true
}

func (l *envLoader) fixedWidthFields(key string) []FixedWidthField {
	value, ok := l.lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return nil
	}
	items := strings.Split(value, ",")
	result := make([]FixedWidthField, 0, len(items))
	for index, item := range items {
		parts := strings.Split(item, ":")
		if len(parts) != 4 {
			l.problem(fmt.Sprintf("%s entry %d must use name:start:length:type syntax", key, index+1))
			continue
		}
		name := strings.TrimSpace(parts[0])
		start, startErr := strconv.Atoi(strings.TrimSpace(parts[1]))
		length, lengthErr := strconv.Atoi(strings.TrimSpace(parts[2]))
		if name == "" {
			l.problem(fmt.Sprintf("%s entry %d name must not be empty", key, index+1))
		}
		if startErr != nil {
			l.problem(fmt.Sprintf("%s entry %d start must be a valid integer", key, index+1))
			start = 0
		}
		if lengthErr != nil {
			l.problem(fmt.Sprintf("%s entry %d length must be a valid integer", key, index+1))
			length = 0
		}
		normalizedStart := -1
		if startErr == nil && start > 0 {
			normalizedStart = start - 1
		}
		result = append(result, FixedWidthField{
			Name:   name,
			Start:  normalizedStart,
			Length: length,
			Type:   DataType(strings.ToLower(strings.TrimSpace(parts[3]))),
		})
	}
	return result
}

func (l *envLoader) stringMap(key string) map[string]string {
	result := make(map[string]string)
	value, ok := l.lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return result
	}
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "{") {
		decoder := json.NewDecoder(strings.NewReader(trimmed))
		if err := decoder.Decode(&result); err != nil {
			l.problem(key + " must be a JSON string map or comma-separated field:value pairs")
			return make(map[string]string)
		}
		if err := ensureJSONEOF(decoder); err != nil {
			l.problem(key + " must contain exactly one JSON object")
			return make(map[string]string)
		}
		clean := make(map[string]string, len(result))
		for mapKey, mapValue := range result {
			mapKey = strings.TrimSpace(mapKey)
			if mapKey == "" {
				l.problem(key + " contains an empty field name")
				continue
			}
			clean[mapKey] = mapValue
		}
		return clean
	}

	for index, item := range strings.Split(value, ",") {
		parts := strings.SplitN(item, ":", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
			l.problem(fmt.Sprintf("%s entry %d must use field:value syntax", key, index+1))
			continue
		}
		result[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}
	return result
}

func (l *envLoader) transforms() []TransformRule {
	const primary = "PARSER_TRANSFORMS_JSON"
	const legacy = "PARSER_TRANSFORMS"
	value, set := l.lookup(primary)
	legacyValue, legacySet := l.lookup(legacy)
	if set && legacySet && strings.TrimSpace(value) != strings.TrimSpace(legacyValue) {
		l.problem(primary + " conflicts with alias " + legacy)
	}
	key := primary
	if !set {
		value, set, key = legacyValue, legacySet, legacy
	}
	if !set || strings.TrimSpace(value) == "" {
		return nil
	}

	decoder := json.NewDecoder(bytes.NewBufferString(value))
	decoder.DisallowUnknownFields()
	var rules []TransformRule
	if err := decoder.Decode(&rules); err != nil {
		l.problem(key + " must be a valid JSON array of transform rules")
		return nil
	}
	if err := ensureJSONEOF(decoder); err != nil {
		l.problem(key + " must contain exactly one JSON array")
		return nil
	}
	for index := range rules {
		rules[index].Field = strings.TrimSpace(rules[index].Field)
		rules[index].Operation = TransformOperation(strings.ToUpper(strings.TrimSpace(string(rules[index].Operation))))
	}
	return rules
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return fmt.Errorf("unexpected trailing JSON value")
	}
	return err
}

func (l *envLoader) delimiter(key string) string {
	value, ok := l.lookup(key)
	if !ok {
		return ""
	}
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case `\T`, "TAB":
		return "\t"
	case `\N`:
		return "\n"
	case `\R`:
		return "\r"
	}
	if value == "" {
		return ""
	}
	if !utf8.ValidString(value) {
		l.problem(key + " must contain valid UTF-8")
	}
	return value
}

func normalizeParser(parser *ParserConfig) {
	if parser.FileType == FileTypeCSV && parser.Delimiter == "" {
		parser.Delimiter = ","
	}
	if parser.FileType == FileTypeTSV && parser.Delimiter == "" {
		parser.Delimiter = "\t"
	}

	if len(parser.Columns) == 0 && len(parser.FixedWidthFields) > 0 {
		for _, field := range parser.FixedWidthFields {
			parser.Columns = append(parser.Columns, ColumnSpec{Name: field.Name, Type: field.Type})
		}
	}
	if len(parser.Mappings) == 0 {
		switch parser.FileType {
		case FileTypeDelimited, FileTypeCSV, FileTypeTSV:
			for index, column := range parser.Columns {
				source := column.Name
				if !parser.HasHeader {
					source = strconv.Itoa(index)
				}
				parser.Mappings = append(parser.Mappings, FieldMapping{Source: source, Target: column.Name})
			}
		case FileTypeFixedWidth:
			for _, field := range parser.FixedWidthFields {
				parser.Mappings = append(parser.Mappings, FieldMapping{Source: field.Name, Target: field.Name})
			}
		case FileTypeRaw:
			if len(parser.Columns) == 0 {
				parser.Columns = []ColumnSpec{{Name: "raw", Type: TypeString}}
			}
			if len(parser.Columns) == 1 {
				parser.Mappings = []FieldMapping{{Source: "raw", Target: parser.Columns[0].Name}}
			}
		case FileTypeJSON, FileTypeXML:
			for _, column := range parser.Columns {
				parser.Mappings = append(parser.Mappings, FieldMapping{Source: column.Name, Target: column.Name})
			}
		}
	}
	if len(parser.Columns) == 0 && len(parser.Mappings) > 0 {
		for _, mapping := range parser.Mappings {
			parser.Columns = append(parser.Columns, ColumnSpec{Name: mapping.Target, Type: TypeAny})
		}
	}
}

func normalizeDriver(driver string) string {
	switch strings.ToLower(strings.TrimSpace(driver)) {
	case "postgresql":
		return "postgres"
	case "mariadb":
		return "mysql"
	default:
		return strings.ToLower(strings.TrimSpace(driver))
	}
}

func defaultPort(driver string) int {
	switch driver {
	case "postgres", "pgx":
		return 5432
	case "mysql":
		return 3306
	case "sqlserver":
		return 1433
	default:
		return 0
	}
}
