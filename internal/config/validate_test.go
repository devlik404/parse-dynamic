package config

import (
	"strings"
	"testing"
	"time"
)

func TestValidateAcceptsSupportedDrivers(t *testing.T) {
	for _, driver := range []string{"postgres", "pgx", "mysql", "sqlserver"} {
		t.Run(driver, func(t *testing.T) {
			cfg := validJobConfig()
			cfg.DB.Driver = driver
			cfg.DB.DSN = "configured-externally"
			cfg.DB.Port = 0
			if err := Validate(cfg); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestValidateCrossFieldRules(t *testing.T) {
	tests := []struct {
		name string
		edit func(*JobConfig)
		want []string
	}{
		{
			name: "input and glob",
			edit: func(cfg *JobConfig) { cfg.Input.Path = ""; cfg.Input.FilePattern = "[" },
			want: []string{"INPUT_PATH", "FILE_PATTERN"},
		},
		{
			name: "file pattern cannot escape input directory",
			edit: func(cfg *JobConfig) { cfg.Input.FilePattern = "../*.csv" },
			want: []string{"FILE_PATTERN must be a basename-only pattern within INPUT_PATH"},
		},
		{
			name: "unsupported file and data types",
			edit: func(cfg *JobConfig) { cfg.Parser.FileType = "PDF"; cfg.Parser.Columns[0].Type = "money" },
			want: []string{"PARSER_FILE_TYPE", "PARSER_TYPES"},
		},
		{
			name: "duplicate parser columns and mapping targets",
			edit: func(cfg *JobConfig) {
				cfg.Parser.Columns = append(cfg.Parser.Columns, cfg.Parser.Columns[0])
				cfg.Parser.Mappings = append(cfg.Parser.Mappings, FieldMapping{Source: "1", Target: "raw"})
			},
			want: []string{"PARSER_COLUMNS contains duplicate", "PARSER_MAPPING contains duplicate target"},
		},
		{
			name: "mapping cardinality and source index",
			edit: func(cfg *JobConfig) {
				cfg.Parser.Columns = append(cfg.Parser.Columns, ColumnSpec{Name: "other", Type: TypeString})
				cfg.Parser.Mappings[0].Source = "not-an-index"
			},
			want: []string{"exactly one mapping", "zero-based", "does not map canonical field"},
		},
		{
			name: "unknown convenience fields and conflict",
			edit: func(cfg *JobConfig) {
				cfg.Parser.UppercaseFields = []string{"raw", "missing"}
				cfg.Parser.LowercaseFields = []string{"raw"}
				cfg.Parser.RequiredFields = []string{"absent"}
				cfg.Parser.DefaultValues = map[string]string{"unknown": "value"}
			},
			want: []string{"PARSER_UPPERCASE_FIELDS references unknown", "both PARSER_UPPERCASE_FIELDS", "PARSER_REQUIRED_FIELDS", "PARSER_DEFAULT_VALUES"},
		},
		{
			name: "transform operations",
			edit: func(cfg *JobConfig) {
				cfg.Parser.Transforms = []TransformRule{
					{Field: "missing", Operation: "HASH"},
					{Field: "raw", Operation: TransformReplace},
					{Field: "raw", Operation: TransformSubstring, Start: -1, Length: 0},
				}
			},
			want: []string{"unknown canonical field", "unsupported operation", "REPLACE requires", "start must be non-negative", "length must be greater"},
		},
		{
			name: "date timezone and record limit",
			edit: func(cfg *JobConfig) {
				cfg.Parser.DateFormat = ""
				cfg.Parser.DateTimeFormat = ""
				cfg.Parser.Timezone = "Mars/Olympus"
				cfg.Parser.MaxRecordBytes = 0
			},
			want: []string{"PARSER_DATE_FORMAT", "PARSER_DATETIME_FORMAT", "PARSER_TIMEZONE", "PARSER_MAX_RECORD_BYTES"},
		},
		{
			name: "record limit cannot disable memory protection",
			edit: func(cfg *JobConfig) { cfg.Parser.MaxRecordBytes = int(^uint(0) >> 1) },
			want: []string{"PARSER_MAX_RECORD_BYTES must not exceed"},
		},
		{
			name: "field limit cannot disable memory protection",
			edit: func(cfg *JobConfig) { cfg.Parser.MaxFields = 100_001 },
			want: []string{"PARSER_MAX_FIELDS must not exceed"},
		},
		{
			name: "configured fields respect cardinality limit",
			edit: func(cfg *JobConfig) { cfg.Parser.MaxFields = 0 },
			want: []string{"PARSER_MAX_FIELDS must be greater than zero"},
		},
		{
			name: "database connection and sizing",
			edit: func(cfg *JobConfig) {
				cfg.DB.Driver = "oracle"
				cfg.DB.Host = ""
				cfg.DB.Name = ""
				cfg.DB.Port = 70000
				cfg.DB.BatchSize = 0
				cfg.DB.ConnectTimeout = 0
			},
			want: []string{"DB_DRIVER", "DB_HOST", "DB_NAME", "DB_PORT", "DB_BATCH_SIZE", "DB_CONNECT_TIMEOUT"},
		},
		{
			name: "database batch cannot allocate unbounded memory",
			edit: func(cfg *JobConfig) { cfg.DB.BatchSize = int(^uint(0) >> 1) },
			want: []string{"DB_BATCH_SIZE must not exceed"},
		},
		{
			name: "database batch byte budget cannot allocate unbounded memory",
			edit: func(cfg *JobConfig) { cfg.DB.BatchMaxBytes = int(^uint(0) >> 1) },
			want: []string{"DB_BATCH_MAX_BYTES must not exceed"},
		},
		{
			name: "database TLS mode is driver-specific",
			edit: func(cfg *JobConfig) { cfg.DB.SSLMode = "trust-everything" },
			want: []string{"DB_SSL_MODE"},
		},
		{
			name: "DSN owns its TLS configuration",
			edit: func(cfg *JobConfig) { cfg.DB.DSN = "configured-externally"; cfg.DB.SSLMode = "require" },
			want: []string{"DB_SSL_MODE must be empty when DB_DSN is set"},
		},
		{
			name: "unsafe and duplicate database identifiers",
			edit: func(cfg *JobConfig) {
				cfg.DB.Schema = "public.audit"
				cfg.DB.Table = "records;drop"
				cfg.DB.Columns = []string{"raw", "raw", "bad-name"}
			},
			want: []string{"DB_SCHEMA", "DB_TABLE", "duplicate column", "safe SQL identifier"},
		},
		{
			name: "database mapping references and coverage",
			edit: func(cfg *JobConfig) {
				cfg.DB.Columns = []string{"raw", "destination"}
				cfg.DB.ColumnMappings = []FieldMapping{{Source: "missing", Target: "elsewhere"}}
			},
			want: []string{"exactly one mapping", "not a canonical parser field", "not declared in DB_COLUMNS", "does not map DB column"},
		},
		{
			name: "partial requires durable sink and nonnegative max",
			edit: func(cfg *JobConfig) {
				cfg.Error.Mode = ErrorModePartial
				cfg.Error.OutputPath = ""
				cfg.Error.MaxCount = -1
			},
			want: []string{"ERROR_OUTPUT_PATH", "ERROR_MAX_COUNT"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validJobConfig()
			test.edit(&cfg)
			err := Validate(cfg)
			if err == nil {
				t.Fatal("Validate() error = nil")
			}
			for _, want := range test.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Validate() error does not contain %q:\n%s", want, err)
				}
			}
		})
	}
}

func TestValidateFixedWidthRules(t *testing.T) {
	cfg := validJobConfig()
	cfg.Parser.FileType = FileTypeFixedWidth
	cfg.Parser.FixedWidthUnit = "CODEPOINT"
	cfg.Parser.Columns = []ColumnSpec{{Name: "first", Type: TypeString}, {Name: "second", Type: TypeString}}
	cfg.Parser.FixedWidthFields = []FixedWidthField{
		{Name: "first", Start: 0, Length: 5, Type: TypeString},
		{Name: "second", Start: 4, Length: 2, Type: "binary"},
		{Name: "second", Start: -1, Length: 0, Type: TypeString},
	}
	cfg.Parser.Mappings = []FieldMapping{{Source: "unknown", Target: "first"}, {Source: "second", Target: "second"}}
	cfg.DB.Columns = []string{"first", "second"}
	cfg.DB.ColumnMappings = []FieldMapping{{Source: "first", Target: "first"}, {Source: "second", Target: "second"}}

	err := Validate(cfg)
	if err == nil {
		t.Fatal("Validate() error = nil")
	}
	for _, want := range []string{"PARSER_FIXED_WIDTH_UNIT", "overlap", "duplicate field", "start must be at least 1", "length must be greater", "unsupported type", "not declared in PARSER_FIXED_WIDTH_FIELDS"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Validate() error does not contain %q:\n%s", want, err)
		}
	}
}

func TestValidateRejectsOverflowingFixedWidthRange(t *testing.T) {
	cfg := validJobConfig()
	cfg.Parser.FileType = FileTypeFixedWidth
	cfg.Parser.Columns = []ColumnSpec{{Name: "raw", Type: TypeString}}
	cfg.Parser.FixedWidthFields = []FixedWidthField{{Name: "raw", Start: 1, Length: int(^uint(0) >> 1), Type: TypeString}}
	cfg.Parser.Mappings = []FieldMapping{{Source: "raw", Target: "raw"}}
	err := Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "range must fit within PARSER_MAX_RECORD_BYTES") {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateFormatSpecificRules(t *testing.T) {
	tests := []struct {
		name string
		edit func(*JobConfig)
		want string
	}{
		{"delimited delimiter required", func(cfg *JobConfig) { cfg.Parser.Delimiter = "" }, "PARSER_DELIMITER is required"},
		{"delimited delimiter rejects newline", func(cfg *JobConfig) { cfg.Parser.Delimiter = "|\n" }, "cannot be NUL"},
		{"CSV comma", func(cfg *JobConfig) { cfg.Parser.FileType = FileTypeCSV; cfg.Parser.Delimiter = ";" }, "must be comma"},
		{"TSV tab", func(cfg *JobConfig) { cfg.Parser.FileType = FileTypeTSV; cfg.Parser.Delimiter = "  " }, "must be tab"},
		{"JSON mode", func(cfg *JobConfig) { cfg.Parser.FileType = FileTypeJSON; cfg.Parser.JSONMode = "OBJECTS" }, "PARSER_JSON_MODE"},
		{"XML record path", func(cfg *JobConfig) { cfg.Parser.FileType = FileTypeXML; cfg.Parser.XMLRecordPath = "" }, "PARSER_XML_RECORD_PATH"},
		{"RAW source", func(cfg *JobConfig) { cfg.Parser.FileType = FileTypeRaw; cfg.Parser.Mappings[0].Source = "line" }, "source must be \"raw\""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validJobConfig()
			test.edit(&cfg)
			err := Validate(cfg)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestConfigErrorDeduplicatesAndSortsCopy(t *testing.T) {
	err := newConfigError([]string{"z problem", "a problem", "z problem"})
	if len(err.Problems) != 2 {
		t.Fatalf("Problems = %#v", err.Problems)
	}
	sorted := err.SortedProblems()
	if strings.Join(sorted, ",") != "a problem,z problem" {
		t.Fatalf("SortedProblems = %#v", sorted)
	}
	sorted[0] = "mutated"
	if err.Problems[0] != "z problem" {
		t.Fatal("SortedProblems returned internal storage")
	}
}

func validJobConfig() JobConfig {
	return JobConfig{
		Input: InputConfig{Path: "/data/input", FilePattern: "*", SkipEmptyLine: true},
		Parser: ParserConfig{
			FileType:       FileTypeDelimited,
			Delimiter:      "||",
			Columns:        []ColumnSpec{{Name: "raw", Type: TypeString}},
			Mappings:       []FieldMapping{{Source: "0", Target: "raw"}},
			FixedWidthUnit: "BYTE",
			JSONMode:       JSONModeAuto,
			DateFormat:     "2006-01-02",
			DateTimeFormat: time.RFC3339,
			Timezone:       "UTC",
			DefaultValues:  map[string]string{},
			MaxRecordBytes: 1024 * 1024,
			MaxFields:      10_000,
			SkipEmptyLine:  true,
		},
		DB: DBConfig{
			Driver:         "postgres",
			Host:           "localhost",
			Port:           5432,
			Name:           "database",
			Schema:         "public",
			Table:          "records",
			Columns:        []string{"raw"},
			ColumnMappings: []FieldMapping{{Source: "raw", Target: "raw"}},
			BatchSize:      1000,
			BatchMaxBytes:  64 * 1024 * 1024,
			ConnectTimeout: 30 * time.Second,
		},
		Error: ErrorConfig{Mode: ErrorModeStrict},
	}
}

func TestDatabaseParameterLimit(t *testing.T) {
	if got := databaseParameterLimit("sqlserver"); got != 2_100 {
		t.Fatalf("SQL Server parameter limit = %d", got)
	}
	if got := databaseParameterLimit("postgres"); got != 65_535 {
		t.Fatalf("PostgreSQL parameter limit = %d", got)
	}
}
