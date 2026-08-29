package config

import "time"

// FileType selects a decoder. Business schemas never select Go code directly.
type FileType string

const (
	FileTypeDelimited  FileType = "DELIMITED"
	FileTypeCSV        FileType = "CSV"
	FileTypeTSV        FileType = "TSV"
	FileTypeFixedWidth FileType = "FIXED_WIDTH"
	FileTypeJSON       FileType = "JSON"
	FileTypeXML        FileType = "XML"
	FileTypeRaw        FileType = "RAW"
	FileTypeText       FileType = "TEXT"
	FileTypeHTML       FileType = "HTML"
	FileTypeHTM        FileType = "HTM"
	FileTypePDF        FileType = "PDF"
	FileTypeXLS        FileType = "XLS"
	FileTypeXLSX       FileType = "XLSX"
	// FileTypeSectionedDelimited decodes files whose control records introduce
	// a section-specific header followed by one or more data records.
	FileTypeSectionedDelimited FileType = "SECTIONED_DELIMITED"
)

// DuplicateHeaderPolicy controls how repeated dynamic column names in a
// section header are represented in source values.
type DuplicateHeaderPolicy string

const (
	DuplicateHeaderError       DuplicateHeaderPolicy = "ERROR"
	DuplicateHeaderSuffixIndex DuplicateHeaderPolicy = "SUFFIX_INDEX"
)

type JSONMode string

const (
	JSONModeAuto   JSONMode = "AUTO"
	JSONModeSingle JSONMode = "SINGLE"
	JSONModeArray  JSONMode = "ARRAY"
	JSONModeNDJSON JSONMode = "NDJSON"
)

type ErrorMode string

const (
	ErrorModeStrict  ErrorMode = "STRICT"
	ErrorModePartial ErrorMode = "PARTIAL"
)

type DataType string

const (
	// TypeAny is an internal pass-through type used when structured input
	// mappings omit PARSER_COLUMNS/PARSER_TYPES. It preserves JSON native values.
	TypeAny      DataType = "any"
	TypeString   DataType = "string"
	TypeInteger  DataType = "integer"
	TypeInt64    DataType = "int64"
	TypeDecimal  DataType = "decimal"
	TypeFloat    DataType = "float"
	TypeBoolean  DataType = "boolean"
	TypeDate     DataType = "date"
	TypeDateTime DataType = "datetime"
	TypeUUID     DataType = "uuid"
	TypeJSON     DataType = "json"
)

type TransformOperation string

const (
	TransformTrim         TransformOperation = "TRIM"
	TransformUppercase    TransformOperation = "UPPERCASE"
	TransformLowercase    TransformOperation = "LOWERCASE"
	TransformReplace      TransformOperation = "REPLACE"
	TransformSubstring    TransformOperation = "SUBSTRING"
	TransformDefaultValue TransformOperation = "DEFAULT_VALUE"
	TransformNullIfEmpty  TransformOperation = "NULL_IF_EMPTY"
)

type JobConfig struct {
	Input  InputConfig
	Parser ParserConfig
	DB     DBConfig
	Error  ErrorConfig
}

type InputConfig struct {
	Path          string
	FilePattern   string
	SkipEmptyLine bool
}

type ParserConfig struct {
	FileType          FileType
	Delimiter         string
	HasHeader         bool
	Columns           []ColumnSpec
	Mappings          []FieldMapping
	FixedWidthFields  []FixedWidthField
	FixedWidthUnit    string
	JSONMode          JSONMode
	JSONRecordPath    string
	XMLRecordPath     string
	DateFormat        string
	DateTimeFormat    string
	Timezone          string
	TrimSpace         bool
	NullIfEmpty       bool
	UppercaseFields   []string
	LowercaseFields   []string
	DefaultValues     map[string]string
	Transforms        []TransformRule
	RequiredFields    []string
	MaxRecordBytes    int
	MaxDocumentBytes  int
	MaxFields         int
	AllowExtraColumns bool
	SkipEmptyLine     bool
	SpreadsheetSheet  string
	HTMLTableIndex    int
	Sectioned         SectionedDelimitedConfig
}

// SectionedDelimitedConfig describes record framing only. Record codes are
// deliberately configuration data so the parser has no business-format
// knowledge. Empty file-header/footer codes make those frames optional; the
// section-header and data codes are required.
type SectionedDelimitedConfig struct {
	RecordTypeIndex       int
	SectionKeyIndex       int
	FileHeaderCode        string
	SectionHeaderCode     string
	DataCode              string
	SectionFooterCode     string
	FileFooterCode        string
	HeaderStartIndex      int
	DataStartIndex        int
	DuplicateHeaderPolicy DuplicateHeaderPolicy
}

type ColumnSpec struct {
	Name string
	Type DataType
}

// FieldMapping is always source selector -> canonical field.
type FieldMapping struct {
	Source string
	Target string
}

// Start is normalized to zero-based during configuration loading.
type FixedWidthField struct {
	Name   string
	Start  int
	Length int
	Type   DataType
}

type TransformRule struct {
	Field     string             `json:"field"`
	Operation TransformOperation `json:"operation"`
	From      string             `json:"from,omitempty"`
	To        string             `json:"to,omitempty"`
	Value     string             `json:"value,omitempty"`
	Start     int                `json:"start,omitempty"`
	Length    int                `json:"length,omitempty"`
}

type DBConfig struct {
	Driver         string
	DSN            string
	Host           string
	Port           int
	User           string
	Password       string
	Name           string
	Schema         string
	Table          string
	Columns        []string
	ColumnMappings []FieldMapping
	BatchSize      int
	BatchMaxBytes  int
	SSLMode        string
	ConnectTimeout time.Duration
}

type ErrorConfig struct {
	Mode       ErrorMode
	OutputPath string
	MaxCount   int
}
