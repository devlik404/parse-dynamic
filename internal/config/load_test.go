package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoadDelimitedComplete(t *testing.T) {
	env := validDelimitedEnv()
	env["INPUT_SKIP_EMPTY_LINE"] = "false"
	env["FILE_PATTERN"] = "*.dat"
	env["PARSER_DELIMITER"] = "||"
	env["PARSER_HAS_HEADER"] = "true"
	env["PARSER_MAPPING"] = "id:identifier,amount:amount"
	env["PARSER_DATE_FORMAT"] = "20060102"
	env["PARSER_DATETIME_FORMAT"] = "2006-01-02 15:04:05"
	env["PARSER_TIMEZONE"] = "Asia/Jakarta"
	env["PARSER_TRIM_SPACE"] = "true"
	env["PARSER_NULL_IF_EMPTY"] = "true"
	env["PARSER_ALLOW_EXTRA_COLUMNS"] = "true"
	env["PARSER_MAX_RECORD_BYTES"] = "2048"
	env["PARSER_MAX_FIELDS"] = "5000"
	env["PARSER_UPPERCASE_FIELDS"] = "identifier"
	env["PARSER_REQUIRED_FIELDS"] = "identifier, amount"
	env["PARSER_DEFAULT_VALUES"] = `{"amount":"0"}`
	env["PARSER_TRANSFORMS_JSON"] = `[{"field":"identifier","operation":"replace","from":"-","to":""}]`
	env["DB_DRIVER"] = "postgresql"
	env["DB_SSL_MODE"] = "require"
	env["DB_CONNECT_TIMEOUT"] = "45s"
	env["DB_BATCH_SIZE"] = "25"
	env["DB_BATCH_MAX_BYTES"] = "1048576"
	env["ERROR_MODE"] = "PARTIAL"
	env["ERROR_OUTPUT_PATH"] = "/tmp/parser-errors.jsonl"
	env["ERROR_MAX_COUNT"] = "7"

	cfg, err := Load(mapLookup(env))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Input.Path != "/data/input" || cfg.Input.FilePattern != "*.dat" {
		t.Fatalf("unexpected input config: %+v", cfg.Input)
	}
	if cfg.Input.SkipEmptyLine || cfg.Parser.SkipEmptyLine {
		t.Fatal("skip-empty setting was not copied to input and parser configs")
	}
	if cfg.Parser.Delimiter != "||" || !cfg.Parser.HasHeader {
		t.Fatalf("unexpected delimiter/header config: %+v", cfg.Parser)
	}
	if cfg.Parser.DateFormat != "20060102" || cfg.Parser.Timezone != "Asia/Jakarta" {
		t.Fatalf("unexpected date config: %+v", cfg.Parser)
	}
	if cfg.Parser.MaxRecordBytes != 2048 || cfg.Parser.MaxFields != 5000 || !cfg.Parser.TrimSpace || !cfg.Parser.NullIfEmpty || !cfg.Parser.AllowExtraColumns {
		t.Fatalf("parser flags were not loaded: %+v", cfg.Parser)
	}
	wantMappings := []FieldMapping{{Source: "id", Target: "identifier"}, {Source: "amount", Target: "amount"}}
	if !reflect.DeepEqual(cfg.Parser.Mappings, wantMappings) {
		t.Fatalf("Mappings = %#v, want %#v", cfg.Parser.Mappings, wantMappings)
	}
	if len(cfg.Parser.Transforms) != 1 || cfg.Parser.Transforms[0].Operation != TransformReplace {
		t.Fatalf("Transforms = %#v", cfg.Parser.Transforms)
	}
	if cfg.DB.Driver != "postgres" || cfg.DB.Port != 5432 || cfg.DB.BatchSize != 25 || cfg.DB.BatchMaxBytes != 1048576 || cfg.DB.ConnectTimeout != 45*time.Second {
		t.Fatalf("unexpected DB normalization: %+v", cfg.DB)
	}
	if cfg.Error.Mode != ErrorModePartial || cfg.Error.MaxCount != 7 {
		t.Fatalf("unexpected error config: %+v", cfg.Error)
	}
}

func TestLoadParserDoesNotRequireInputOrDatabaseConfiguration(t *testing.T) {
	env := map[string]string{
		"PARSER_FILE_TYPE":  "DELIMITED",
		"PARSER_DELIMITER":  "|",
		"PARSER_HAS_HEADER": "true",
		"PARSER_COLUMNS":    "id,amount",
		"PARSER_TYPES":      "string,decimal",
		"PARSER_MAPPING":    "ID:id,AMOUNT:amount",
		"PARSER_TRIM_SPACE": "true",
		"SKIP_EMPTY_LINE":   "false",
		"DB_PASSWORD":       "must-be-ignored",
		"ERROR_OUTPUT_PATH": "/must/not/be/required",
	}

	cfg, err := LoadParser(mapLookup(env))
	if err != nil {
		t.Fatalf("LoadParser() error = %v", err)
	}
	if cfg.FileType != FileTypeDelimited || cfg.Delimiter != "|" || cfg.SkipEmptyLine || !cfg.TrimSpace {
		t.Fatalf("unexpected parser config: %+v", cfg)
	}
	assertMappings(t, cfg.Mappings, []FieldMapping{{"ID", "id"}, {"AMOUNT", "amount"}})
}

func TestLoadParserStillFailsFastForParserErrors(t *testing.T) {
	env := map[string]string{
		"PARSER_FILE_TYPE":  "CSV",
		"PARSER_HAS_HEADER": "invalid",
		"PARSER_COLUMNS":    "id",
		"PARSER_TYPES":      "unknown",
	}

	_, err := LoadParser(mapLookup(env))
	if err == nil {
		t.Fatal("LoadParser() error = nil")
	}
	for _, key := range []string{"PARSER_HAS_HEADER", "PARSER_TYPES"} {
		if !strings.Contains(err.Error(), key) {
			t.Fatalf("LoadParser() error = %v, want %s", err, key)
		}
	}
}

func TestLoadParserSectionedDelimited(t *testing.T) {
	env := map[string]string{
		"PARSER_FILE_TYPE":               "sectioned_delimited",
		"PARSER_DELIMITER":               "|",
		"PARSER_COLUMNS":                 "record_type,section_key,payload",
		"PARSER_TYPES":                   "string,string,json",
		"PARSER_FILE_HEADER_CODE":        "RH",
		"PARSER_SECTION_HEADER_CODE":     "SH",
		"PARSER_DATA_CODE":               "SB",
		"PARSER_SECTION_FOOTER_CODE":     "SF",
		"PARSER_FILE_FOOTER_CODE":        "RF",
		"PARSER_DUPLICATE_HEADER_POLICY": "suffix_index",
	}

	cfg, err := LoadParser(mapLookup(env))
	if err != nil {
		t.Fatalf("LoadParser() error = %v", err)
	}
	if cfg.FileType != FileTypeSectionedDelimited || cfg.Sectioned.RecordTypeIndex != 0 || cfg.Sectioned.SectionKeyIndex != 1 {
		t.Fatalf("unexpected section indexes: %+v", cfg.Sectioned)
	}
	if cfg.Sectioned.FileHeaderCode != "RH" || cfg.Sectioned.SectionHeaderCode != "SH" || cfg.Sectioned.DataCode != "SB" || cfg.Sectioned.SectionFooterCode != "SF" || cfg.Sectioned.FileFooterCode != "RF" {
		t.Fatalf("unexpected section codes: %+v", cfg.Sectioned)
	}
	if cfg.Sectioned.HeaderStartIndex != 2 || cfg.Sectioned.DataStartIndex != 2 || cfg.Sectioned.DuplicateHeaderPolicy != DuplicateHeaderSuffixIndex {
		t.Fatalf("unexpected section settings: %+v", cfg.Sectioned)
	}
	assertMappings(t, cfg.Mappings, []FieldMapping{{"record_type", "record_type"}, {"section_key", "section_key"}, {"payload", "payload"}})
}

func TestLoadParserSectionedDelimitedRequiresConfiguredRecordCodes(t *testing.T) {
	env := map[string]string{
		"PARSER_FILE_TYPE": "SECTIONED_DELIMITED",
		"PARSER_DELIMITER": "|",
		"PARSER_COLUMNS":   "payload",
		"PARSER_TYPES":     "json",
		"PARSER_MAPPING":   "payload:payload",
	}
	_, err := LoadParser(mapLookup(env))
	if err == nil || !strings.Contains(err.Error(), "PARSER_SECTION_HEADER_CODE") || !strings.Contains(err.Error(), "PARSER_DATA_CODE") {
		t.Fatalf("LoadParser() error = %v", err)
	}
}

func TestLoadNormalizesEveryFormat(t *testing.T) {
	tests := []struct {
		name  string
		env   map[string]string
		check func(*testing.T, JobConfig)
	}{
		{
			name: "CSV defaults comma and index mappings",
			env: formatEnv(map[string]string{
				"PARSER_FILE_TYPE": "csv",
				"PARSER_COLUMNS":   "id,amount",
				"PARSER_TYPES":     "string,decimal",
			}),
			check: func(t *testing.T, cfg JobConfig) {
				if cfg.Parser.Delimiter != "," {
					t.Fatalf("delimiter = %q", cfg.Parser.Delimiter)
				}
				assertMappings(t, cfg.Parser.Mappings, []FieldMapping{{"0", "id"}, {"1", "amount"}})
			},
		},
		{
			name: "TSV accepts escaped tab and identity header mapping",
			env: formatEnv(map[string]string{
				"PARSER_FILE_TYPE":  "TSV",
				"PARSER_DELIMITER":  `\t`,
				"PARSER_HAS_HEADER": "true",
				"PARSER_COLUMNS":    "id,amount",
				"PARSER_TYPES":      "string,decimal",
			}),
			check: func(t *testing.T, cfg JobConfig) {
				if cfg.Parser.Delimiter != "\t" {
					t.Fatalf("delimiter = %q", cfg.Parser.Delimiter)
				}
				assertMappings(t, cfg.Parser.Mappings, []FieldMapping{{"id", "id"}, {"amount", "amount"}})
			},
		},
		{
			name: "fixed width positions become zero based",
			env: formatEnv(map[string]string{
				"PARSER_FILE_TYPE":          "FIXED_WIDTH",
				"PARSER_FIXED_WIDTH_FIELDS": "id:1:6:string,date:7:8:date,amount:15:12:decimal",
				"DB_COLUMNS":                "id,date,amount",
			}),
			check: func(t *testing.T, cfg JobConfig) {
				want := []FixedWidthField{{"id", 0, 6, TypeString}, {"date", 6, 8, TypeDate}, {"amount", 14, 12, TypeDecimal}}
				if !reflect.DeepEqual(cfg.Parser.FixedWidthFields, want) {
					t.Fatalf("FixedWidthFields = %#v, want %#v", cfg.Parser.FixedWidthFields, want)
				}
				if cfg.Parser.FixedWidthUnit != "BYTE" {
					t.Fatalf("unit = %q", cfg.Parser.FixedWidthUnit)
				}
				if len(cfg.Parser.Columns) != 3 || cfg.Parser.Columns[1].Type != TypeDate {
					t.Fatalf("columns were not inferred from fixed fields: %#v", cfg.Parser.Columns)
				}
			},
		},
		{
			name: "JSON mapping infers pass-through columns",
			env: formatEnv(map[string]string{
				"PARSER_FILE_TYPE":        "JSON",
				"PARSER_JSON_MODE":        "array",
				"PARSER_JSON_RECORD_PATH": "payload.items",
				"PARSER_JSON_MAPPING":     "transaction.id:id,transaction.amount:amount",
			}),
			check: func(t *testing.T, cfg JobConfig) {
				if cfg.Parser.JSONMode != JSONModeArray || cfg.Parser.JSONRecordPath != "payload.items" {
					t.Fatalf("unexpected JSON config: %+v", cfg.Parser)
				}
				if len(cfg.Parser.Columns) != 2 || cfg.Parser.Columns[1].Type != TypeAny {
					t.Fatalf("JSON inferred columns must preserve native values: %#v", cfg.Parser.Columns)
				}
			},
		},
		{
			name: "XML mapping and record path",
			env: formatEnv(map[string]string{
				"PARSER_FILE_TYPE":       "XML",
				"PARSER_XML_RECORD_PATH": "/transactions/transaction",
				"PARSER_XML_MAPPING":     "id:id,amount:amount",
			}),
			check: func(t *testing.T, cfg JobConfig) {
				if cfg.Parser.XMLRecordPath != "/transactions/transaction" || cfg.Parser.Columns[0].Type != TypeAny {
					t.Fatalf("unexpected XML config: %+v", cfg.Parser)
				}
			},
		},
		{
			name: "RAW supplies raw schema",
			env: formatEnv(map[string]string{
				"PARSER_FILE_TYPE": "RAW",
				"DB_COLUMNS":       "raw",
			}),
			check: func(t *testing.T, cfg JobConfig) {
				wantColumns := []ColumnSpec{{Name: "raw", Type: TypeString}}
				if !reflect.DeepEqual(cfg.Parser.Columns, wantColumns) {
					t.Fatalf("Columns = %#v", cfg.Parser.Columns)
				}
				assertMappings(t, cfg.Parser.Mappings, []FieldMapping{{"raw", "raw"}})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg, err := Load(mapLookup(test.env))
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			test.check(t, cfg)
		})
	}
}

func TestLoadUsesDefaultsAndAliases(t *testing.T) {
	env := validDelimitedEnv()
	delete(env, "PARSER_DELIMITER")
	env["PARSER_DELIMITER"] = "|"
	env["SKIP_EMPTY_LINE"] = "false"
	env["DB_DRIVER"] = "mariadb"
	env["DB_DSN"] = "user:password@tcp(localhost)/database"
	delete(env, "DB_HOST")
	delete(env, "DB_NAME")

	cfg, err := Load(mapLookup(env))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Input.FilePattern != "*" || cfg.Input.SkipEmptyLine || cfg.Parser.SkipEmptyLine {
		t.Fatalf("unexpected input defaults/alias: %+v", cfg.Input)
	}
	if cfg.Parser.JSONMode != JSONModeAuto || cfg.Parser.FixedWidthUnit != "BYTE" || cfg.Parser.MaxRecordBytes != 1024*1024 || cfg.Parser.MaxFields != 10_000 {
		t.Fatalf("unexpected parser defaults: %+v", cfg.Parser)
	}
	if cfg.DB.Driver != "mysql" || cfg.DB.Port != 3306 || cfg.DB.BatchSize != 1000 || cfg.DB.BatchMaxBytes != 64*1024*1024 || cfg.DB.ConnectTimeout != 30*time.Second {
		t.Fatalf("unexpected DB defaults: %+v", cfg.DB)
	}
	if cfg.Error.Mode != ErrorModeStrict || cfg.Error.MaxCount != 0 {
		t.Fatalf("unexpected error defaults: %+v", cfg.Error)
	}
}

func TestLoadRejectsMalformedScalarInsteadOfFallingBack(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{"boolean", "PARSER_HAS_HEADER", "perhaps"},
		{"integer", "DB_BATCH_SIZE", "one thousand"},
		{"batch bytes", "DB_BATCH_MAX_BYTES", "64MB"},
		{"max bytes", "PARSER_MAX_RECORD_BYTES", "1MB"},
		{"max fields", "PARSER_MAX_FIELDS", "many"},
		{"duration", "DB_CONNECT_TIMEOUT", "30"},
		{"error maximum", "ERROR_MAX_COUNT", "many"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := validDelimitedEnv()
			env[test.key] = test.value
			_, err := Load(mapLookup(env))
			if err == nil || !strings.Contains(err.Error(), test.key) {
				t.Fatalf("Load() error = %v, want error naming %s", err, test.key)
			}
		})
	}
}

func TestLoadReportsAggregatedErrorsWithoutSecrets(t *testing.T) {
	env := validDelimitedEnv()
	env["INPUT_PATH"] = ""
	env["PARSER_FILE_TYPE"] = "unknown"
	env["PARSER_HAS_HEADER"] = "invalid"
	env["DB_TABLE"] = "transactions; DROP TABLE audit"
	env["DB_PASSWORD"] = "super-secret-password"
	env["DB_DSN"] = "postgres://secret-user:secret-pass@host/db"

	_, err := Load(mapLookup(env))
	if err == nil {
		t.Fatal("Load() error = nil")
	}
	var configErr *ConfigError
	if !errors.As(err, &configErr) {
		t.Fatalf("error type = %T, want *ConfigError", err)
	}
	message := err.Error()
	for _, want := range []string{"INPUT_PATH", "PARSER_FILE_TYPE", "PARSER_HAS_HEADER", "DB_TABLE"} {
		if !strings.Contains(message, want) {
			t.Errorf("error does not contain %s: %s", want, message)
		}
	}
	for _, secret := range []string{"super-secret-password", "secret-user", "secret-pass"} {
		if strings.Contains(message, secret) {
			t.Errorf("error leaked secret %q: %s", secret, message)
		}
	}
	if len(configErr.Problems) < 4 {
		t.Fatalf("got only %d aggregated problems: %#v", len(configErr.Problems), configErr.Problems)
	}
}

func TestLoadRejectsMalformedCompoundValues(t *testing.T) {
	tests := []struct {
		name string
		edit func(map[string]string)
		key  string
	}{
		{"mapping", func(env map[string]string) { env["PARSER_MAPPING"] = "missing-separator" }, "PARSER_MAPPING"},
		{"fixed field", func(env map[string]string) {
			env["PARSER_FILE_TYPE"] = "FIXED_WIDTH"
			env["PARSER_FIXED_WIDTH_FIELDS"] = "id:first:6:string"
			delete(env, "PARSER_MAPPING")
		}, "PARSER_FIXED_WIDTH_FIELDS"},
		{"transform JSON", func(env map[string]string) { env["PARSER_TRANSFORMS_JSON"] = `[{"field":"identifier",}]` }, "PARSER_TRANSFORMS_JSON"},
		{"unknown transform property", func(env map[string]string) {
			env["PARSER_TRANSFORMS_JSON"] = `[{"field":"identifier","operation":"TRIM","surprise":true}]`
		}, "PARSER_TRANSFORMS_JSON"},
		{"default values", func(env map[string]string) { env["PARSER_DEFAULT_VALUES"] = `{not-json}` }, "PARSER_DEFAULT_VALUES"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := validDelimitedEnv()
			test.edit(env)
			_, err := Load(mapLookup(env))
			if err == nil || !strings.Contains(err.Error(), test.key) {
				t.Fatalf("Load() error = %v, want key %s", err, test.key)
			}
		})
	}
}

func TestLoadFromEnv(t *testing.T) {
	for key, value := range validDelimitedEnv() {
		t.Setenv(key, value)
	}
	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}
	if cfg.Parser.FileType != FileTypeDelimited || cfg.DB.Table != "transactions" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadNilLookup(t *testing.T) {
	_, err := Load(nil)
	if err == nil || !strings.Contains(err.Error(), "lookup") {
		t.Fatalf("Load(nil) error = %v", err)
	}
}

func validDelimitedEnv() map[string]string {
	return map[string]string{
		"INPUT_PATH":       "/data/input",
		"PARSER_FILE_TYPE": "DELIMITED",
		"PARSER_DELIMITER": "|",
		"PARSER_COLUMNS":   "identifier,amount",
		"PARSER_TYPES":     "string,decimal",
		"PARSER_MAPPING":   "0:identifier,1:amount",
		"DB_DRIVER":        "postgres",
		"DB_HOST":          "localhost",
		"DB_NAME":          "reconciliation",
		"DB_SCHEMA":        "public",
		"DB_TABLE":         "transactions",
		"DB_COLUMNS":       "identifier,amount",
	}
}

func formatEnv(values map[string]string) map[string]string {
	env := map[string]string{
		"INPUT_PATH": "/data/input",
		"DB_DRIVER":  "postgres",
		"DB_HOST":    "localhost",
		"DB_NAME":    "reconciliation",
		"DB_SCHEMA":  "public",
		"DB_TABLE":   "transactions",
		"DB_COLUMNS": "id,amount",
	}
	for key, value := range values {
		env[key] = value
	}
	return env
}

func mapLookup(values map[string]string) LookupFunc {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func assertMappings(t *testing.T, got, want []FieldMapping) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Mappings = %#v, want %#v", got, want)
	}
}
