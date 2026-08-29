package config

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxApplicationBatchSize    = 10_000
	maxApplicationBatchBytes   = 128 * 1024 * 1024
	maxApplicationRecordSize   = 8 * 1024 * 1024
	maxApplicationDocumentSize = 512 * 1024 * 1024
	maxApplicationFields       = 100_000
	maxApplicationTransforms   = 256
	maxConfigProblems          = 128
	maxConfigProblemBytes      = 1024
	maxConfigLabelBytes        = 128
)

const omittedConfigProblems = "additional configuration problems were omitted"

// ConfigError contains every discovered configuration problem so operators can
// fix an ENV deployment in one pass. Values (and therefore secrets) are never
// included in its messages.
type ConfigError struct {
	Problems []string
}

func (e *ConfigError) Error() string {
	if e == nil || len(e.Problems) == 0 {
		return "invalid configuration"
	}
	return "invalid configuration:\n - " + strings.Join(e.Problems, "\n - ")
}

func newConfigError(problems []string) *ConfigError {
	seen := make(map[string]struct{}, len(problems))
	unique := make([]string, 0, len(problems))
	for _, problem := range problems {
		if problem == "" {
			continue
		}
		if _, exists := seen[problem]; exists {
			continue
		}
		seen[problem] = struct{}{}
		unique = appendConfigProblem(unique, problem)
	}
	return &ConfigError{Problems: unique}
}

func appendConfigProblem(problems []string, problem string) []string {
	if len(problem) > maxConfigProblemBytes {
		end := maxConfigProblemBytes
		for end > 0 && !utf8.RuneStart(problem[end]) {
			end--
		}
		problem = problem[:end] + "...[truncated]"
	}
	if len(problems) < maxConfigProblems {
		return append(problems, problem)
	}
	if len(problems) == maxConfigProblems || problems[len(problems)-1] != omittedConfigProblems {
		return append(problems[:maxConfigProblems], omittedConfigProblems)
	}
	return problems
}

func configProblemLimitReached(problems []string) bool {
	return len(problems) > maxConfigProblems && problems[len(problems)-1] == omittedConfigProblems
}

// Validate verifies a programmatically-created configuration using the same
// invariants as Load. It does not mutate cfg.
func Validate(cfg JobConfig) error {
	problems := validateProblems(cfg)
	if len(problems) == 0 {
		return nil
	}
	return newConfigError(problems)
}

// ValidateParser validates a parser-only configuration without requiring
// filesystem, database, or error-sink settings. This is used by preview APIs
// that never persist records.
func ValidateParser(cfg ParserConfig) error {
	problems := validateParserProblems(cfg)
	if len(problems) == 0 {
		return nil
	}
	return newConfigError(problems)
}

func validateProblems(cfg JobConfig) []string {
	var problems []string
	add := func(format string, args ...any) {
		if configProblemLimitReached(problems) {
			return
		}
		problems = appendConfigProblem(problems, fmt.Sprintf(format, args...))
	}

	if strings.TrimSpace(cfg.Input.Path) == "" {
		add("INPUT_PATH is required")
	}
	if strings.TrimSpace(cfg.Input.FilePattern) == "" {
		add("FILE_PATTERN is required")
	} else {
		pattern := cfg.Input.FilePattern
		if filepath.IsAbs(pattern) || filepath.Base(pattern) != pattern || pattern == "." || pattern == ".." || strings.ContainsAny(pattern, `/\`) {
			add("FILE_PATTERN must be a basename-only pattern within INPUT_PATH")
		}
		if _, err := filepath.Match(pattern, "probe"); err != nil {
			add("FILE_PATTERN must be a valid file glob")
		}
	}

	for _, problem := range validateParserProblems(cfg.Parser) {
		problems = appendConfigProblem(problems, problem)
	}
	columnNames := make(map[string]struct{}, len(cfg.Parser.Columns))
	for _, column := range cfg.Parser.Columns {
		if strings.TrimSpace(column.Name) != "" {
			columnNames[column.Name] = struct{}{}
		}
	}
	validateDB(cfg, columnNames, add)
	validateError(cfg.Error, add)

	return problems
}

func validateParserProblems(parser ParserConfig) []string {
	var problems []string
	add := func(format string, args ...any) {
		if configProblemLimitReached(problems) {
			return
		}
		problems = appendConfigProblem(problems, fmt.Sprintf(format, args...))
	}

	validFileTypes := map[FileType]bool{
		FileTypeDelimited: true, FileTypeCSV: true, FileTypeTSV: true,
		FileTypeFixedWidth: true, FileTypeJSON: true, FileTypeXML: true,
		FileTypeRaw: true, FileTypeText: true, FileTypeHTML: true, FileTypeHTM: true,
		FileTypePDF: true, FileTypeXLS: true, FileTypeXLSX: true, FileTypeSectionedDelimited: true,
	}
	if !validFileTypes[parser.FileType] {
		add("PARSER_FILE_TYPE must be one of DELIMITED, CSV, TSV, FIXED_WIDTH, JSON, XML, RAW, TEXT, HTML, HTM, PDF, XLS, XLSX, SECTIONED_DELIMITED")
	}

	validTypes := map[DataType]bool{
		TypeAny: true, TypeString: true, TypeInteger: true, TypeInt64: true, TypeDecimal: true,
		TypeFloat: true, TypeBoolean: true, TypeDate: true, TypeDateTime: true,
		TypeUUID: true, TypeJSON: true,
	}
	columnNames := make(map[string]struct{}, len(parser.Columns))
	for index, column := range parser.Columns {
		if strings.TrimSpace(column.Name) == "" {
			add("PARSER_COLUMNS entry %d must not be empty", index+1)
		} else if _, exists := columnNames[column.Name]; exists {
			add("PARSER_COLUMNS contains duplicate field %q", configLabel(column.Name))
		} else {
			columnNames[column.Name] = struct{}{}
		}
		if !validTypes[column.Type] {
			add("PARSER_TYPES entry %d must be one of string, integer, int64, decimal, float, boolean, date, datetime, uuid, json (any is reserved for inferred fields)", index+1)
		}
	}
	if len(parser.Columns) == 0 && validFileTypes[parser.FileType] {
		add("PARSER_COLUMNS (or a format mapping from which columns can be inferred) is required")
	}

	validateDelimiter(parser, add)
	validateFormatSpecific(parser, validTypes, add)
	validateMappings(parser, columnNames, add)
	validateParserOptions(parser, columnNames, add)
	return problems
}

func validateDelimiter(parser ParserConfig, add func(string, ...any)) {
	switch parser.FileType {
	case FileTypeDelimited, FileTypeSectionedDelimited:
		if parser.Delimiter == "" {
			add("PARSER_DELIMITER is required when PARSER_FILE_TYPE=DELIMITED or SECTIONED_DELIMITED")
		}
	case FileTypeCSV:
		if parser.Delimiter != "," {
			add("PARSER_DELIMITER must be comma when PARSER_FILE_TYPE=CSV")
		}
	case FileTypeTSV:
		if parser.Delimiter != "\t" {
			add("PARSER_DELIMITER must be tab when PARSER_FILE_TYPE=TSV")
		}
	default:
		return
	}
	if parser.Delimiter == "" {
		return
	}
	if !utf8.ValidString(parser.Delimiter) {
		add("PARSER_DELIMITER must contain valid UTF-8")
		return
	}
	if parser.FileType != FileTypeDelimited && parser.FileType != FileTypeSectionedDelimited && utf8.RuneCountInString(parser.Delimiter) != 1 {
		add("PARSER_DELIMITER must be exactly one valid UTF-8 character for CSV or TSV")
		return
	}
	if strings.ContainsRune(parser.Delimiter, 0) || strings.ContainsRune(parser.Delimiter, '\r') || strings.ContainsRune(parser.Delimiter, '\n') || strings.ContainsRune(parser.Delimiter, utf8.RuneError) {
		add("PARSER_DELIMITER cannot be NUL, CR, LF, or the Unicode replacement character")
	}
}

func validateFormatSpecific(parser ParserConfig, validTypes map[DataType]bool, add func(string, ...any)) {
	if parser.FileType == FileTypeFixedWidth {
		if len(parser.FixedWidthFields) == 0 {
			add("PARSER_FIXED_WIDTH_FIELDS is required when PARSER_FILE_TYPE=FIXED_WIDTH")
		}
		if parser.FixedWidthUnit != "BYTE" && parser.FixedWidthUnit != "RUNE" {
			add("PARSER_FIXED_WIDTH_UNIT must be BYTE or RUNE")
		}
		names := make(map[string]struct{}, len(parser.FixedWidthFields))
		for index, field := range parser.FixedWidthFields {
			if field.Name == "" {
				add("PARSER_FIXED_WIDTH_FIELDS entry %d name must not be empty", index+1)
			} else if _, exists := names[field.Name]; exists {
				add("PARSER_FIXED_WIDTH_FIELDS contains duplicate field %q", configLabel(field.Name))
			} else {
				names[field.Name] = struct{}{}
			}
			if field.Start < 0 {
				add("PARSER_FIXED_WIDTH_FIELDS entry %d start must be at least 1", index+1)
			}
			if field.Length <= 0 {
				add("PARSER_FIXED_WIDTH_FIELDS entry %d length must be greater than zero", index+1)
			}
			if parser.MaxRecordBytes > 0 && field.Start >= 0 && field.Length > 0 &&
				(field.Start > parser.MaxRecordBytes || field.Length > parser.MaxRecordBytes-field.Start) {
				add("PARSER_FIXED_WIDTH_FIELDS entry %d range must fit within PARSER_MAX_RECORD_BYTES", index+1)
			}
			if !validTypes[field.Type] {
				add("PARSER_FIXED_WIDTH_FIELDS entry %d has unsupported type", index+1)
			}
		}
		type indexedFixedRange struct {
			field FixedWidthField
			index int
		}
		validRanges := make([]indexedFixedRange, 0, len(parser.FixedWidthFields))
		for index, field := range parser.FixedWidthFields {
			if validFixedRange(field, parser.MaxRecordBytes) {
				validRanges = append(validRanges, indexedFixedRange{field: field, index: index})
			}
		}
		sort.SliceStable(validRanges, func(left, right int) bool {
			if validRanges[left].field.Start != validRanges[right].field.Start {
				return validRanges[left].field.Start < validRanges[right].field.Start
			}
			if validRanges[left].field.Length != validRanges[right].field.Length {
				return validRanges[left].field.Length > validRanges[right].field.Length
			}
			return validRanges[left].index < validRanges[right].index
		})
		maximumEnd := -1
		maximumIndex := -1
		for _, item := range validRanges {
			if item.field.Start < maximumEnd {
				add("PARSER_FIXED_WIDTH_FIELDS entries %d and %d overlap", maximumIndex+1, item.index+1)
			}
			end := item.field.Start + item.field.Length
			if end > maximumEnd {
				maximumEnd = end
				maximumIndex = item.index
			}
		}
	}

	validJSONModes := map[JSONMode]bool{
		JSONModeAuto: true, JSONModeSingle: true, JSONModeArray: true, JSONModeNDJSON: true,
	}
	if parser.FileType == FileTypeJSON && !validJSONModes[parser.JSONMode] {
		add("PARSER_JSON_MODE must be AUTO, SINGLE, ARRAY, or NDJSON")
	}
	if parser.FileType == FileTypeXML && strings.TrimSpace(parser.XMLRecordPath) == "" {
		add("PARSER_XML_RECORD_PATH is required when PARSER_FILE_TYPE=XML")
	}
	if parser.FileType == FileTypeSectionedDelimited {
		validateSectionedDelimited(parser, add)
	}
}

func validateSectionedDelimited(parser ParserConfig, add func(string, ...any)) {
	cfg := parser.Sectioned
	indices := []struct {
		key   string
		value int
	}{
		{"PARSER_RECORD_TYPE_INDEX", cfg.RecordTypeIndex},
		{"PARSER_SECTION_KEY_INDEX", cfg.SectionKeyIndex},
		{"PARSER_DYNAMIC_HEADER_START_INDEX", cfg.HeaderStartIndex},
		{"PARSER_DATA_START_INDEX", cfg.DataStartIndex},
	}
	for _, item := range indices {
		if item.value < 0 {
			add("%s must be zero or greater", item.key)
		} else if parser.MaxFields > 0 && item.value >= parser.MaxFields {
			add("%s must be less than PARSER_MAX_FIELDS", item.key)
		}
	}
	if cfg.RecordTypeIndex == cfg.SectionKeyIndex {
		add("PARSER_RECORD_TYPE_INDEX and PARSER_SECTION_KEY_INDEX must be different")
	}
	controlIndex := cfg.RecordTypeIndex
	if cfg.SectionKeyIndex > controlIndex {
		controlIndex = cfg.SectionKeyIndex
	}
	if cfg.HeaderStartIndex <= controlIndex {
		add("PARSER_DYNAMIC_HEADER_START_INDEX must be after the record type and section key indexes")
	}
	if cfg.DataStartIndex <= controlIndex {
		add("PARSER_DATA_START_INDEX must be after the record type and section key indexes")
	}
	if parser.HasHeader {
		add("PARSER_HAS_HEADER must be false for SECTIONED_DELIMITED; headers are introduced by section-header records")
	}

	codes := []struct {
		key      string
		value    string
		required bool
	}{
		{"PARSER_FILE_HEADER_CODE", cfg.FileHeaderCode, false},
		{"PARSER_SECTION_HEADER_CODE", cfg.SectionHeaderCode, true},
		{"PARSER_DATA_CODE", cfg.DataCode, true},
		{"PARSER_SECTION_FOOTER_CODE", cfg.SectionFooterCode, false},
		{"PARSER_FILE_FOOTER_CODE", cfg.FileFooterCode, false},
	}
	seen := make(map[string]string, len(codes))
	for _, item := range codes {
		value := strings.TrimSpace(item.value)
		if value == "" {
			if item.required {
				add("%s is required for SECTIONED_DELIMITED", item.key)
			}
			continue
		}
		if strings.Contains(value, parser.Delimiter) || strings.ContainsAny(value, "\x00\r\n") {
			add("%s must not contain the delimiter, NUL, CR, or LF", item.key)
		}
		if previous, duplicate := seen[value]; duplicate {
			add("%s must differ from %s", item.key, previous)
		} else {
			seen[value] = item.key
		}
	}
	if cfg.DuplicateHeaderPolicy != DuplicateHeaderError && cfg.DuplicateHeaderPolicy != DuplicateHeaderSuffixIndex {
		add("PARSER_DUPLICATE_HEADER_POLICY must be ERROR or SUFFIX_INDEX")
	}
}

func validFixedRange(field FixedWidthField, maximum int) bool {
	return maximum > 0 && field.Start >= 0 && field.Length > 0 &&
		field.Start <= maximum && field.Length <= maximum-field.Start
}

func validateMappings(parser ParserConfig, columns map[string]struct{}, add func(string, ...any)) {
	key := parserMappingKey(parser.FileType)
	if len(parser.Mappings) == 0 {
		add("%s is required", key)
		return
	}
	if len(parser.Mappings) != len(parser.Columns) {
		add("%s must contain exactly one mapping for each PARSER_COLUMNS entry", key)
	}
	sources := make(map[string]struct{}, len(parser.Mappings))
	targets := make(map[string]struct{}, len(parser.Mappings))
	fixedNames := make(map[string]struct{}, len(parser.FixedWidthFields))
	for _, field := range parser.FixedWidthFields {
		fixedNames[field.Name] = struct{}{}
	}
	for index, mapping := range parser.Mappings {
		if strings.TrimSpace(mapping.Source) == "" || strings.TrimSpace(mapping.Target) == "" {
			add("%s entry %d must have non-empty source and target", key, index+1)
			continue
		}
		if _, exists := sources[mapping.Source]; exists {
			add("%s contains duplicate source %q", key, configLabel(mapping.Source))
		}
		sources[mapping.Source] = struct{}{}
		if _, exists := targets[mapping.Target]; exists {
			add("%s contains duplicate target %q", key, configLabel(mapping.Target))
		}
		targets[mapping.Target] = struct{}{}
		if _, exists := columns[mapping.Target]; !exists {
			add("%s target %q is not declared in PARSER_COLUMNS", key, configLabel(mapping.Target))
		}

		switch parser.FileType {
		case FileTypeDelimited, FileTypeCSV, FileTypeTSV, FileTypeHTML, FileTypeHTM, FileTypeXLS, FileTypeXLSX:
			if !parser.HasHeader {
				position, err := strconv.Atoi(mapping.Source)
				if err != nil || position < 0 {
					add("%s source %q must be a zero-based non-negative index when PARSER_HAS_HEADER=false", key, configLabel(mapping.Source))
				}
			}
		case FileTypeFixedWidth:
			if _, exists := fixedNames[mapping.Source]; !exists {
				add("%s source %q is not declared in PARSER_FIXED_WIDTH_FIELDS", key, configLabel(mapping.Source))
			}
		case FileTypeRaw, FileTypeText:
			if mapping.Source != "raw" {
				add("%s source must be %q when PARSER_FILE_TYPE=RAW or TEXT", key, "raw")
			}
		case FileTypePDF:
			if mapping.Source != "page" && mapping.Source != "text" && mapping.Source != "raw" && mapping.Source != "value" {
				add("%s source %q must be page, text, raw, or value when PARSER_FILE_TYPE=PDF", key, configLabel(mapping.Source))
			}
		}
	}
	for column := range columns {
		if _, exists := targets[column]; !exists {
			add("%s does not map canonical field %q", key, configLabel(column))
		}
	}
}

func validateParserOptions(parser ParserConfig, columns map[string]struct{}, add func(string, ...any)) {
	if strings.TrimSpace(parser.DateFormat) == "" {
		add("PARSER_DATE_FORMAT must not be empty")
	}
	if strings.TrimSpace(parser.DateTimeFormat) == "" {
		add("PARSER_DATETIME_FORMAT must not be empty")
	}
	if strings.TrimSpace(parser.Timezone) == "" {
		add("PARSER_TIMEZONE must not be empty")
	} else if _, err := time.LoadLocation(parser.Timezone); err != nil {
		add("PARSER_TIMEZONE must be a valid IANA timezone name")
	}
	if parser.MaxRecordBytes <= 0 {
		add("PARSER_MAX_RECORD_BYTES must be greater than zero")
	} else if parser.MaxRecordBytes > maxApplicationRecordSize {
		add("PARSER_MAX_RECORD_BYTES must not exceed %d", maxApplicationRecordSize)
	}
	if parser.FileType == FileTypePDF || parser.FileType == FileTypeXLS || parser.FileType == FileTypeXLSX {
		if parser.MaxDocumentBytes <= 0 {
			add("PARSER_MAX_DOCUMENT_BYTES must be greater than zero")
		} else if parser.MaxDocumentBytes > maxApplicationDocumentSize {
			add("PARSER_MAX_DOCUMENT_BYTES must not exceed %d", maxApplicationDocumentSize)
		}
	}
	if parser.FileType == FileTypeXLS || parser.FileType == FileTypeXLSX {
		if strings.TrimSpace(parser.SpreadsheetSheet) == "" {
			add("PARSER_SPREADSHEET_SHEET must be a worksheet name or zero-based index")
		} else if index, err := strconv.Atoi(parser.SpreadsheetSheet); err == nil && index < 0 {
			add("PARSER_SPREADSHEET_SHEET index must be zero or greater")
		}
	}
	if (parser.FileType == FileTypeHTML || parser.FileType == FileTypeHTM) && parser.HTMLTableIndex < 0 {
		add("PARSER_HTML_TABLE_INDEX must be zero or greater")
	}
	if parser.MaxFields <= 0 {
		add("PARSER_MAX_FIELDS must be greater than zero")
	} else if parser.MaxFields > maxApplicationFields {
		add("PARSER_MAX_FIELDS must not exceed %d", maxApplicationFields)
	} else {
		if len(parser.Columns) > parser.MaxFields {
			add("PARSER_COLUMNS must not contain more than PARSER_MAX_FIELDS entries")
		}
		if len(parser.FixedWidthFields) > parser.MaxFields {
			add("PARSER_FIXED_WIDTH_FIELDS must not contain more than PARSER_MAX_FIELDS entries")
		}
	}

	validateFieldList := func(key string, values []string) {
		seen := make(map[string]struct{}, len(values))
		for _, field := range values {
			if _, duplicate := seen[field]; duplicate {
				add("%s contains duplicate field %q", key, configLabel(field))
			}
			seen[field] = struct{}{}
			if _, exists := columns[field]; !exists {
				add("%s references unknown canonical field %q", key, configLabel(field))
			}
		}
	}
	validateFieldList("PARSER_UPPERCASE_FIELDS", parser.UppercaseFields)
	validateFieldList("PARSER_LOWERCASE_FIELDS", parser.LowercaseFields)
	validateFieldList("PARSER_REQUIRED_FIELDS", parser.RequiredFields)
	upper := make(map[string]struct{}, len(parser.UppercaseFields))
	for _, field := range parser.UppercaseFields {
		upper[field] = struct{}{}
	}
	for _, field := range parser.LowercaseFields {
		if _, conflict := upper[field]; conflict {
			add("field %q cannot appear in both PARSER_UPPERCASE_FIELDS and PARSER_LOWERCASE_FIELDS", configLabel(field))
		}
	}
	for field, value := range parser.DefaultValues {
		if _, exists := columns[field]; !exists {
			add("PARSER_DEFAULT_VALUES references unknown canonical field %q", configLabel(field))
		}
		if parser.MaxRecordBytes > 0 && len(value) > parser.MaxRecordBytes {
			add("PARSER_DEFAULT_VALUES value for %q exceeds PARSER_MAX_RECORD_BYTES", configLabel(field))
		}
	}

	validOperations := map[TransformOperation]bool{
		TransformTrim: true, TransformUppercase: true, TransformLowercase: true,
		TransformReplace: true, TransformSubstring: true, TransformDefaultValue: true,
		TransformNullIfEmpty: true,
	}
	if len(parser.Transforms) > maxApplicationTransforms {
		add("PARSER_TRANSFORMS_JSON must not contain more than %d rules", maxApplicationTransforms)
	}
	for index, rule := range parser.Transforms {
		if _, exists := columns[rule.Field]; !exists {
			add("PARSER_TRANSFORMS_JSON entry %d references unknown canonical field %q", index+1, configLabel(rule.Field))
		}
		if !validOperations[rule.Operation] {
			add("PARSER_TRANSFORMS_JSON entry %d has unsupported operation", index+1)
		}
		if rule.Operation == TransformReplace && rule.From == "" {
			add("PARSER_TRANSFORMS_JSON entry %d REPLACE requires a non-empty from value", index+1)
		}
		if parser.MaxRecordBytes > 0 && (len(rule.From) > parser.MaxRecordBytes || len(rule.To) > parser.MaxRecordBytes || len(rule.Value) > parser.MaxRecordBytes) {
			add("PARSER_TRANSFORMS_JSON entry %d contains a value exceeding PARSER_MAX_RECORD_BYTES", index+1)
		}
		if rule.Operation == TransformSubstring {
			if rule.Start < 0 {
				add("PARSER_TRANSFORMS_JSON entry %d SUBSTRING start must be non-negative", index+1)
			}
			if rule.Length <= 0 {
				add("PARSER_TRANSFORMS_JSON entry %d SUBSTRING length must be greater than zero", index+1)
			}
		}
	}
}

func validateDB(cfg JobConfig, parserColumns map[string]struct{}, add func(string, ...any)) {
	db := cfg.DB
	validDrivers := map[string]bool{"postgres": true, "pgx": true, "mysql": true, "sqlserver": true}
	if !validDrivers[db.Driver] {
		add("DB_DRIVER must be one of postgres, pgx, mysql, sqlserver")
	}
	if strings.TrimSpace(db.DSN) == "" {
		if strings.TrimSpace(db.Host) == "" {
			add("DB_HOST is required when DB_DSN is not set")
		}
		if strings.TrimSpace(db.Name) == "" {
			add("DB_NAME is required when DB_DSN is not set")
		}
		if db.Port <= 0 || db.Port > 65535 {
			add("DB_PORT must be between 1 and 65535 when DB_DSN is not set")
		}
	} else if db.Port < 0 || db.Port > 65535 {
		add("DB_PORT must be between 0 and 65535 when DB_DSN is set")
	}
	validateDBSSLMode(db, add)
	if db.BatchSize <= 0 {
		add("DB_BATCH_SIZE must be greater than zero")
	} else if maximum := safeBatchSize(db.Driver, len(db.Columns)); db.BatchSize > maximum {
		add("DB_BATCH_SIZE must not exceed %d for the configured driver and DB_COLUMNS", maximum)
	}
	if db.BatchMaxBytes <= 0 {
		add("DB_BATCH_MAX_BYTES must be greater than zero")
	} else if db.BatchMaxBytes > maxApplicationBatchBytes {
		add("DB_BATCH_MAX_BYTES must not exceed %d", maxApplicationBatchBytes)
	}
	if db.ConnectTimeout <= 0 {
		add("DB_CONNECT_TIMEOUT must be greater than zero")
	}
	if db.Table == "" {
		add("DB_TABLE is required")
	} else if !safeIdentifier(db.Table) {
		add("DB_TABLE must be a safe SQL identifier")
	}
	if db.Schema != "" && !safeIdentifier(db.Schema) {
		add("DB_SCHEMA must be a safe SQL identifier")
	}
	if len(db.Columns) == 0 {
		add("DB_COLUMNS is required")
	} else if maximum := databaseParameterLimit(db.Driver); maximum > 0 && len(db.Columns) > maximum {
		add("DB_COLUMNS must not exceed the %s parameter limit of %d columns", db.Driver, maximum)
	}
	dbColumns := make(map[string]struct{}, len(db.Columns))
	for _, column := range db.Columns {
		if !safeIdentifier(column) {
			add("DB_COLUMNS entry %q must be a safe SQL identifier", configLabel(column))
		}
		if _, duplicate := dbColumns[column]; duplicate {
			add("DB_COLUMNS contains duplicate column %q", configLabel(column))
		}
		dbColumns[column] = struct{}{}
	}
	if len(db.ColumnMappings) != len(db.Columns) {
		add("DB_COLUMN_MAPPING must contain exactly one mapping for each DB_COLUMNS entry")
	}
	sources := make(map[string]struct{}, len(db.ColumnMappings))
	targets := make(map[string]struct{}, len(db.ColumnMappings))
	for index, mapping := range db.ColumnMappings {
		if mapping.Source == "" || mapping.Target == "" {
			add("DB_COLUMN_MAPPING entry %d must have non-empty source and target", index+1)
			continue
		}
		if _, exists := parserColumns[mapping.Source]; !exists {
			add("DB_COLUMN_MAPPING source %q is not a canonical parser field", configLabel(mapping.Source))
		}
		if _, exists := dbColumns[mapping.Target]; !exists {
			add("DB_COLUMN_MAPPING target %q is not declared in DB_COLUMNS", configLabel(mapping.Target))
		}
		if _, duplicate := sources[mapping.Source]; duplicate {
			add("DB_COLUMN_MAPPING contains duplicate source %q", configLabel(mapping.Source))
		}
		if _, duplicate := targets[mapping.Target]; duplicate {
			add("DB_COLUMN_MAPPING contains duplicate target %q", configLabel(mapping.Target))
		}
		sources[mapping.Source] = struct{}{}
		targets[mapping.Target] = struct{}{}
	}
	for column := range dbColumns {
		if _, mapped := targets[column]; !mapped {
			add("DB_COLUMN_MAPPING does not map DB column %q", configLabel(column))
		}
	}
}

func validateDBSSLMode(db DBConfig, add func(string, ...any)) {
	mode := strings.ToLower(strings.TrimSpace(db.SSLMode))
	if strings.TrimSpace(db.DSN) != "" && mode != "" {
		add("DB_SSL_MODE must be empty when DB_DSN is set; configure TLS in DB_DSN")
		return
	}
	valid := map[string]bool{"": true}
	switch db.Driver {
	case "postgres", "pgx":
		for _, value := range []string{"disable", "allow", "prefer", "require", "verify-ca", "verify-full"} {
			valid[value] = true
		}
	case "mysql":
		for _, value := range []string{"disable", "disabled", "false", "require", "required", "true", "skip-verify", "skip_verify", "preferred"} {
			valid[value] = true
		}
	case "sqlserver":
		for _, value := range []string{"disable", "disabled", "false", "require", "required", "true", "skip-verify", "skip_verify"} {
			valid[value] = true
		}
	default:
		return
	}
	if !valid[mode] {
		add("DB_SSL_MODE is not supported for the configured DB_DRIVER")
	}
}

func safeBatchSize(driver string, columnCount int) int {
	if columnCount < 1 {
		columnCount = 1
	}
	parameterLimit := 65_535
	maximum := maxApplicationBatchSize
	if driver == "sqlserver" {
		parameterLimit = 2_100
		maximum = 1_000
	}
	if byParameters := parameterLimit / columnCount; byParameters < maximum {
		maximum = byParameters
	}
	if maximum < 1 {
		return 1
	}
	return maximum
}

func databaseParameterLimit(driver string) int {
	if driver == "sqlserver" {
		return 2_100
	}
	if driver == "postgres" || driver == "pgx" || driver == "mysql" {
		return 65_535
	}
	return 0
}

func validateError(errorConfig ErrorConfig, add func(string, ...any)) {
	if errorConfig.Mode != ErrorModeStrict && errorConfig.Mode != ErrorModePartial {
		add("ERROR_MODE must be STRICT or PARTIAL")
	}
	if errorConfig.Mode == ErrorModePartial && strings.TrimSpace(errorConfig.OutputPath) == "" {
		add("ERROR_OUTPUT_PATH is required when ERROR_MODE=PARTIAL")
	}
	if errorConfig.MaxCount < 0 {
		add("ERROR_MAX_COUNT must be zero (unlimited) or greater")
	}
}

func parserMappingKey(fileType FileType) string {
	switch fileType {
	case FileTypeJSON:
		return "PARSER_JSON_MAPPING or PARSER_MAPPING"
	case FileTypeXML:
		return "PARSER_XML_MAPPING or PARSER_MAPPING"
	default:
		return "PARSER_MAPPING"
	}
}

func safeIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for index, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' || (index > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

// configLabel bounds attacker-controlled labels before they are interpolated
// into diagnostics. Validation is also used by the preview API, so a malformed
// configuration object must not be able to amplify a small error into a large
// allocation or response.
func configLabel(value string) string {
	if !utf8.ValidString(value) {
		return "[invalid UTF-8]"
	}
	if len(value) <= maxConfigLabelBytes {
		return value
	}
	end := maxConfigLabelBytes
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + "...[truncated]"
}

// SortedProblems is useful to callers that need stable structured diagnostics.
// ConfigError.Error intentionally preserves validation order for readability.
func (e *ConfigError) SortedProblems() []string {
	if e == nil {
		return nil
	}
	result := append([]string(nil), e.Problems...)
	sort.Strings(result)
	return result
}
