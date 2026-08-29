package dynamicjob

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "github.com/denisenkom/go-mssqldb"

	"parser-engine/internal/config"
	"parser-engine/internal/repository/sqlrepo"
)

const (
	defaultSchema                  = "dbo"
	defaultParamParseFileTable     = "ParamParseFile"
	defaultMappingTable            = "ParamParseFileMappingColumn"
	defaultProductTable            = "Product"
	defaultReaderTable             = "ParamReadFile"
	defaultReaderColumnTable       = "ParamReadFileColumn"
	defaultPasswordDecryptFunction = "fnDecrypt"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type StoreConfig struct {
	Host                    string
	Port                    int
	User                    string
	Password                string
	ParamDatabase           string
	ReconciliationDatabase  string
	EncryptionKey           string
	TargetPort              int
	SSLMode                 string
	ConnectTimeout          time.Duration
	Schema                  string
	ParamParseFileTable     string
	MappingTable            string
	ProductTable            string
	ReaderTable             string
	ReaderColumnTable       string
	PasswordDecryptFunction string
	BatchSize               int
	BatchMaxBytes           int
}

func (cfg StoreConfig) Validate() error {
	return validateStoreConfig(normalizeStoreConfig(cfg))
}

type SQLStore struct {
	config  StoreConfig
	paramDB *sql.DB
	reconDB *sql.DB
}

type legacyProfile struct {
	TargetDB      string
	TargetTable   string
	FieldFilename string
	FieldFiledate string
}

type targetConnection struct {
	Host     string
	User     string
	Password string
	Database string
}

type targetMapping struct {
	Col    int
	Target string
}

type readerProfile struct {
	ID                    int64
	FileType              string
	Delimiter             sql.NullString
	HasHeader             bool
	FixedWidthUnit        string
	JSONMode              string
	JSONRecordPath        sql.NullString
	XMLRecordPath         sql.NullString
	DateFormat            string
	DateTimeFormat        string
	Timezone              string
	TrimSpace             bool
	NullIfEmpty           bool
	AllowExtraColumns     bool
	SkipEmptyLine         bool
	MaxRecordBytes        int
	MaxDocumentBytes      int
	MaxFields             int
	SpreadsheetSheet      string
	HTMLTableIndex        int
	RecordTypeIndex       int
	SectionKeyIndex       int
	FileHeaderCode        sql.NullString
	SectionHeaderCode     sql.NullString
	DataCode              sql.NullString
	SectionFooterCode     sql.NullString
	FileFooterCode        sql.NullString
	HeaderStartIndex      int
	DataStartIndex        int
	DuplicateHeaderPolicy string
}

type readerColumn struct {
	Col                 int
	SourceName          string
	SourceSelector      sql.NullString
	DataType            string
	FixedStart          sql.NullInt64
	FixedLength         sql.NullInt64
	IsRequired          bool
	DefaultValue        sql.NullString
	NormalizeCase       string
	TransformConfigJSON sql.NullString
}

func OpenSQLStore(ctx context.Context, cfg StoreConfig) (*SQLStore, error) {
	cfg = normalizeStoreConfig(cfg)
	if err := validateStoreConfig(cfg); err != nil {
		return nil, err
	}
	paramDB, err := openSQLServer(ctx, cfg, cfg.ParamDatabase, cfg.Port, cfg.User, cfg.Password, cfg.Host)
	if err != nil {
		return nil, fmt.Errorf("open parameter database: %w", err)
	}
	reconDB, err := openSQLServer(ctx, cfg, cfg.ReconciliationDatabase, cfg.Port, cfg.User, cfg.Password, cfg.Host)
	if err != nil {
		_ = paramDB.Close()
		return nil, fmt.Errorf("open reconciliation configuration database: %w", err)
	}
	return &SQLStore{config: cfg, paramDB: paramDB, reconDB: reconDB}, nil
}

func normalizeStoreConfig(cfg StoreConfig) StoreConfig {
	if cfg.Port == 0 {
		cfg.Port = 1433
	}
	if cfg.TargetPort == 0 {
		cfg.TargetPort = 1433
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = 30 * time.Second
	}
	if cfg.Schema == "" {
		cfg.Schema = defaultSchema
	}
	if cfg.ParamParseFileTable == "" {
		cfg.ParamParseFileTable = defaultParamParseFileTable
	}
	if cfg.MappingTable == "" {
		cfg.MappingTable = defaultMappingTable
	}
	if cfg.ProductTable == "" {
		cfg.ProductTable = defaultProductTable
	}
	if cfg.ReaderTable == "" {
		cfg.ReaderTable = defaultReaderTable
	}
	if cfg.ReaderColumnTable == "" {
		cfg.ReaderColumnTable = defaultReaderColumnTable
	}
	if cfg.PasswordDecryptFunction == "" {
		cfg.PasswordDecryptFunction = defaultPasswordDecryptFunction
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 500
	}
	if cfg.BatchMaxBytes <= 0 {
		cfg.BatchMaxBytes = 64 * 1024 * 1024
	}
	return cfg
}

func validateStoreConfig(cfg StoreConfig) error {
	var problems []string
	for name, value := range map[string]string{
		"SS_RC_HOST": cfg.Host, "SS_RC_USER": cfg.User,
		"SS_RC_DB_PARAM_ENGINE": cfg.ParamDatabase,
		"SS_RC_DB_RECON_CONFIG": cfg.ReconciliationDatabase,
		"SCH_ENC_KEY":           cfg.EncryptionKey,
	} {
		if strings.TrimSpace(value) == "" {
			problems = append(problems, name+" is required")
		}
	}
	if cfg.Port < 1 || cfg.Port > 65535 || cfg.TargetPort < 1 || cfg.TargetPort > 65535 {
		problems = append(problems, "SQL Server ports must be between 1 and 65535")
	}
	switch strings.ToLower(strings.TrimSpace(cfg.SSLMode)) {
	case "", "disable", "disabled", "false", "require", "required", "true", "skip-verify", "skip_verify":
	default:
		problems = append(problems, "DYNAMIC_DB_SSL_MODE is invalid")
	}
	for name, value := range map[string]string{
		"schema": cfg.Schema, "ParamParseFile table": cfg.ParamParseFileTable,
		"mapping table": cfg.MappingTable, "product table": cfg.ProductTable,
		"reader table": cfg.ReaderTable, "reader column table": cfg.ReaderColumnTable,
		"password decrypt function": cfg.PasswordDecryptFunction,
	} {
		if !identifierPattern.MatchString(value) {
			problems = append(problems, name+" must be a safe SQL identifier")
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("invalid dynamic MSSQL configuration: %s", strings.Join(problems, "; "))
	}
	return nil
}

func (s *SQLStore) Close() error {
	if s == nil {
		return nil
	}
	var result error
	if s.paramDB != nil {
		result = errors.Join(result, s.paramDB.Close())
	}
	if s.reconDB != nil {
		result = errors.Join(result, s.reconDB.Close())
	}
	return result
}

func (s *SQLStore) Load(ctx context.Context, request Request) (*Plan, error) {
	if s == nil || s.paramDB == nil || s.reconDB == nil {
		return nil, errors.New("dynamic SQL store is not open")
	}
	legacy, err := s.loadLegacyProfile(ctx, request)
	if err != nil {
		return nil, classifyStoreLoadError(err)
	}
	mappings, err := s.loadTargetMappings(ctx, request)
	if err != nil {
		return nil, classifyStoreLoadError(err)
	}
	connection, err := s.loadTargetConnection(ctx, request.ProductID, legacy.TargetDB)
	if err != nil {
		return nil, classifyStoreLoadError(err)
	}
	targetDB, err := openSQLServer(ctx, s.config, connection.Database, s.config.TargetPort, connection.User, connection.Password, connection.Host)
	if err != nil {
		return nil, classifyStoreLoadError(fmt.Errorf("connect resolved target database: %w", err))
	}
	closeTarget := true
	defer func() {
		if closeTarget {
			_ = targetDB.Close()
		}
	}()

	profile, err := s.loadReaderProfile(ctx, targetDB, request)
	if err != nil {
		return nil, classifyStoreLoadError(err)
	}
	columns, err := s.loadReaderColumns(ctx, targetDB, profile.ID)
	if err != nil {
		return nil, classifyStoreLoadError(err)
	}
	parserConfig, dbConfig, err := compilePlan(profile, columns, mappings, legacy, connection, s.config)
	if err != nil {
		return nil, parserPlanError(err)
	}
	if err := validateTargetColumns(ctx, targetDB, dbConfig); err != nil {
		return nil, classifyStoreLoadError(err)
	}
	repo, err := sqlrepo.NewWithDB(targetDB, dbConfig)
	if err != nil {
		return nil, parserPlanError(fmt.Errorf("build target repository: %w", err))
	}
	closeTarget = false
	return &Plan{
		Parser:          parserConfig,
		Repository:      repo,
		BatchSize:       dbConfig.BatchSize,
		BatchMaxBytes:   dbConfig.BatchMaxBytes,
		IncludeFileName: legacy.FieldFilename != "",
		IncludeFileDate: legacy.FieldFiledate != "",
		configurationID: profile.ID,
	}, nil
}

func (s *SQLStore) loadLegacyProfile(ctx context.Context, request Request) (legacyProfile, error) {
	query := fmt.Sprintf(`SELECT TOP (1) [TargetDB], [TargetTable], [FieldFilename], [FieldFiledate]
FROM %s
WHERE [ProductId] = @p1 AND [Task] = @p2 AND [Activity] = @p3`, s.qualified(s.config.ParamParseFileTable))
	var profile legacyProfile
	err := s.paramDB.QueryRowContext(ctx, query, request.ProductID, request.Task, request.Activity).Scan(
		&profile.TargetDB, &profile.TargetTable, &profile.FieldFilename, &profile.FieldFiledate,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return profile, parserPlanError(errors.New("existing ParamParseFile configuration was not found"))
	}
	if err != nil {
		return profile, fmt.Errorf("query existing ParamParseFile configuration: %w", err)
	}
	if profile.TargetDB == "" || profile.TargetTable == "" {
		return profile, parserPlanError(errors.New("existing ParamParseFile target configuration is incomplete"))
	}
	return profile, nil
}

func (s *SQLStore) loadTargetMappings(ctx context.Context, request Request) ([]targetMapping, error) {
	query := fmt.Sprintf(`SELECT [Col], [FieldTargetName]
FROM %s
WHERE [ProductId] = @p1 AND [Task] = @p2 AND [Activity] = @p3
ORDER BY [Col]`, s.qualified(s.config.MappingTable))
	rows, err := s.paramDB.QueryContext(ctx, query, request.ProductID, request.Task, request.Activity)
	if err != nil {
		return nil, fmt.Errorf("query existing target mappings: %w", err)
	}
	defer rows.Close()
	var mappings []targetMapping
	for rows.Next() {
		var mapping targetMapping
		if err := rows.Scan(&mapping.Col, &mapping.Target); err != nil {
			return nil, fmt.Errorf("scan existing target mapping: %w", err)
		}
		mappings = append(mappings, mapping)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read existing target mappings: %w", err)
	}
	if len(mappings) == 0 {
		return nil, parserPlanError(errors.New("existing ParamParseFileMappingColumn configuration was not found"))
	}
	return mappings, nil
}

func (s *SQLStore) loadTargetConnection(ctx context.Context, productID, targetDBField string) (targetConnection, error) {
	if !identifierPattern.MatchString(targetDBField) {
		return targetConnection{}, parserPlanError(errors.New("TargetDB parameter is not a safe SQL identifier"))
	}
	query := fmt.Sprintf(`SELECT TOP (1)
    [ReconciliationDBMasterHost],
    [ReconciliationDBMasterUser],
    %s([ReconciliationDBMasterPass], [ReconciliationDBMasterHost] + @p1),
    %s
FROM %s
WHERE [ProductId] = @p2`,
		s.qualified(s.config.PasswordDecryptFunction), quoteIdentifier(targetDBField), s.qualified(s.config.ProductTable))
	var connection targetConnection
	err := s.reconDB.QueryRowContext(ctx, query, s.config.EncryptionKey, productID).Scan(
		&connection.Host, &connection.User, &connection.Password, &connection.Database,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return connection, parserPlanError(errors.New("product target database configuration was not found"))
	}
	if err != nil {
		return connection, fmt.Errorf("query product target database configuration: %w", err)
	}
	if connection.Host == "" || connection.Database == "" {
		return connection, parserPlanError(errors.New("product target database configuration is incomplete"))
	}
	return connection, nil
}

func (s *SQLStore) loadReaderProfile(ctx context.Context, db *sql.DB, request Request) (readerProfile, error) {
	query := fmt.Sprintf(`SELECT TOP (1)
    [ID], [FileType], [Delimiter], [HasHeader], [FixedWidthUnit],
    [JSONMode], [JSONRecordPath], [XMLRecordPath], [DateFormat],
    [DateTimeFormat], [Timezone], [TrimSpace], [NullIfEmpty],
    [AllowExtraColumns], [SkipEmptyLine], [MaxRecordBytes],
    [MaxDocumentBytes], [MaxFields], [SpreadsheetSheet], [HTMLTableIndex],
    [RecordTypeIndex], [SectionKeyIndex], [FileHeaderCode], [SectionHeaderCode],
    [DataCode], [SectionFooterCode], [FileFooterCode], [HeaderStartIndex],
    [DataStartIndex], [DuplicateHeaderPolicy]
FROM %s
WHERE [ProductID] = @p1 AND [Task] = @p2 AND [Activity] = @p3 AND [IsActive] = 1
ORDER BY [ConfigVersion] DESC, [ID] DESC`, s.qualified(s.config.ReaderTable))
	var profile readerProfile
	err := db.QueryRowContext(ctx, query, request.ProductID, request.Task, request.Activity).Scan(
		&profile.ID, &profile.FileType, &profile.Delimiter, &profile.HasHeader, &profile.FixedWidthUnit,
		&profile.JSONMode, &profile.JSONRecordPath, &profile.XMLRecordPath, &profile.DateFormat,
		&profile.DateTimeFormat, &profile.Timezone, &profile.TrimSpace, &profile.NullIfEmpty,
		&profile.AllowExtraColumns, &profile.SkipEmptyLine, &profile.MaxRecordBytes,
		&profile.MaxDocumentBytes, &profile.MaxFields, &profile.SpreadsheetSheet, &profile.HTMLTableIndex,
		&profile.RecordTypeIndex, &profile.SectionKeyIndex, &profile.FileHeaderCode, &profile.SectionHeaderCode,
		&profile.DataCode, &profile.SectionFooterCode, &profile.FileFooterCode, &profile.HeaderStartIndex,
		&profile.DataStartIndex, &profile.DuplicateHeaderPolicy,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return profile, parserPlanError(errors.New("active ParamReadFile configuration was not found in target database"))
	}
	if err != nil {
		return profile, fmt.Errorf("query target ParamReadFile configuration: %w", err)
	}
	return profile, nil
}

func (s *SQLStore) loadReaderColumns(ctx context.Context, db *sql.DB, profileID int64) ([]readerColumn, error) {
	query := fmt.Sprintf(`SELECT
    [Col], [SourceName], [SourceSelector], [DataType], [FixedStart],
    [FixedLength], [IsRequired], [DefaultValue], [NormalizeCase], [TransformConfigJSON]
FROM %s
WHERE [ParamReadFileID] = @p1 AND [IsActive] = 1
ORDER BY [Col]`, s.qualified(s.config.ReaderColumnTable))
	rows, err := db.QueryContext(ctx, query, profileID)
	if err != nil {
		return nil, fmt.Errorf("query target ParamReadFileColumn configuration: %w", err)
	}
	defer rows.Close()
	var columns []readerColumn
	for rows.Next() {
		var column readerColumn
		if err := rows.Scan(
			&column.Col, &column.SourceName, &column.SourceSelector, &column.DataType,
			&column.FixedStart, &column.FixedLength, &column.IsRequired,
			&column.DefaultValue, &column.NormalizeCase, &column.TransformConfigJSON,
		); err != nil {
			return nil, fmt.Errorf("scan target reader column: %w", err)
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read target reader columns: %w", err)
	}
	if len(columns) == 0 {
		return nil, parserPlanError(errors.New("active ParamReadFileColumn configuration was not found in target database"))
	}
	return columns, nil
}

func compilePlan(
	profile readerProfile,
	readerColumns []readerColumn,
	targetMappings []targetMapping,
	legacy legacyProfile,
	connection targetConnection,
	storeCfg StoreConfig,
) (config.ParserConfig, config.DBConfig, error) {
	parserConfig := config.ParserConfig{
		FileType:          config.FileType(strings.ToUpper(strings.TrimSpace(profile.FileType))),
		Delimiter:         decodeDelimiter(profile.Delimiter.String),
		HasHeader:         profile.HasHeader,
		FixedWidthUnit:    strings.ToUpper(strings.TrimSpace(profile.FixedWidthUnit)),
		JSONMode:          config.JSONMode(strings.ToUpper(strings.TrimSpace(profile.JSONMode))),
		JSONRecordPath:    strings.TrimSpace(profile.JSONRecordPath.String),
		XMLRecordPath:     strings.TrimSpace(profile.XMLRecordPath.String),
		DateFormat:        profile.DateFormat,
		DateTimeFormat:    profile.DateTimeFormat,
		Timezone:          profile.Timezone,
		TrimSpace:         profile.TrimSpace,
		NullIfEmpty:       profile.NullIfEmpty,
		AllowExtraColumns: profile.AllowExtraColumns,
		SkipEmptyLine:     profile.SkipEmptyLine,
		MaxRecordBytes:    profile.MaxRecordBytes,
		MaxDocumentBytes:  profile.MaxDocumentBytes,
		MaxFields:         profile.MaxFields,
		SpreadsheetSheet:  profile.SpreadsheetSheet,
		HTMLTableIndex:    profile.HTMLTableIndex,
		DefaultValues:     make(map[string]string),
		Sectioned: config.SectionedDelimitedConfig{
			RecordTypeIndex:       profile.RecordTypeIndex,
			SectionKeyIndex:       profile.SectionKeyIndex,
			FileHeaderCode:        profile.FileHeaderCode.String,
			SectionHeaderCode:     profile.SectionHeaderCode.String,
			DataCode:              profile.DataCode.String,
			SectionFooterCode:     profile.SectionFooterCode.String,
			FileFooterCode:        profile.FileFooterCode.String,
			HeaderStartIndex:      profile.HeaderStartIndex,
			DataStartIndex:        profile.DataStartIndex,
			DuplicateHeaderPolicy: config.DuplicateHeaderPolicy(strings.ToUpper(strings.TrimSpace(profile.DuplicateHeaderPolicy))),
		},
	}
	if parserConfig.FileType == config.FileTypeCSV && parserConfig.Delimiter == "" {
		parserConfig.Delimiter = ","
	}
	if parserConfig.FileType == config.FileTypeTSV && parserConfig.Delimiter == "" {
		parserConfig.Delimiter = "\t"
	}

	byCol := make(map[int]readerColumn, len(readerColumns))
	for _, column := range readerColumns {
		if column.Col < 1 {
			return config.ParserConfig{}, config.DBConfig{}, errors.New("reader column Col must be one or greater")
		}
		if _, exists := byCol[column.Col]; exists {
			return config.ParserConfig{}, config.DBConfig{}, fmt.Errorf("reader configuration contains duplicate Col %d", column.Col)
		}
		column.SourceName = strings.TrimSpace(column.SourceName)
		byCol[column.Col] = column
		dataType := config.DataType(strings.ToLower(strings.TrimSpace(column.DataType)))
		parserConfig.Columns = append(parserConfig.Columns, config.ColumnSpec{Name: column.SourceName, Type: dataType})
		selector := strings.TrimSpace(column.SourceSelector.String)
		if selector == "" {
			selector = defaultSourceSelector(parserConfig, column)
		}
		parserConfig.Mappings = append(parserConfig.Mappings, config.FieldMapping{Source: selector, Target: column.SourceName})
		if column.FixedStart.Valid || column.FixedLength.Valid {
			if !column.FixedStart.Valid || !column.FixedLength.Valid {
				return config.ParserConfig{}, config.DBConfig{}, fmt.Errorf("reader Col %d has an incomplete fixed-width range", column.Col)
			}
			parserConfig.FixedWidthFields = append(parserConfig.FixedWidthFields, config.FixedWidthField{
				Name: column.SourceName, Start: int(column.FixedStart.Int64) - 1,
				Length: int(column.FixedLength.Int64), Type: dataType,
			})
		}
		if column.IsRequired {
			parserConfig.RequiredFields = append(parserConfig.RequiredFields, column.SourceName)
		}
		if column.DefaultValue.Valid {
			parserConfig.DefaultValues[column.SourceName] = column.DefaultValue.String
		}
		switch strings.ToUpper(strings.TrimSpace(column.NormalizeCase)) {
		case "UPPER":
			parserConfig.UppercaseFields = append(parserConfig.UppercaseFields, column.SourceName)
		case "LOWER":
			parserConfig.LowercaseFields = append(parserConfig.LowercaseFields, column.SourceName)
		}
		if column.TransformConfigJSON.Valid && strings.TrimSpace(column.TransformConfigJSON.String) != "" {
			var transforms []config.TransformRule
			decoder := json.NewDecoder(strings.NewReader(column.TransformConfigJSON.String))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&transforms); err != nil {
				return config.ParserConfig{}, config.DBConfig{}, fmt.Errorf("reader Col %d has invalid TransformConfigJSON: %w", column.Col, err)
			}
			var trailing any
			if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
				return config.ParserConfig{}, config.DBConfig{}, fmt.Errorf("reader Col %d has invalid trailing TransformConfigJSON data", column.Col)
			}
			for index := range transforms {
				if transforms[index].Field != "" && transforms[index].Field != column.SourceName {
					return config.ParserConfig{}, config.DBConfig{}, fmt.Errorf("reader Col %d transform references another field", column.Col)
				}
				transforms[index].Field = column.SourceName
				transforms[index].Operation = config.TransformOperation(strings.ToUpper(strings.TrimSpace(string(transforms[index].Operation))))
			}
			parserConfig.Transforms = append(parserConfig.Transforms, transforms...)
		}
	}

	schema, table, err := splitTargetTable(legacy.TargetTable)
	if err != nil {
		return config.ParserConfig{}, config.DBConfig{}, err
	}
	dbConfig := config.DBConfig{
		Driver: "sqlserver", Host: connection.Host, Port: storeCfg.TargetPort,
		User: connection.User, Password: connection.Password, Name: connection.Database,
		Schema: schema, Table: table, BatchSize: storeCfg.BatchSize,
		BatchMaxBytes: storeCfg.BatchMaxBytes, SSLMode: storeCfg.SSLMode,
		ConnectTimeout: storeCfg.ConnectTimeout,
	}
	targets := make(map[string]struct{}, len(targetMappings)+2)
	for _, mapping := range targetMappings {
		column, exists := byCol[mapping.Col]
		if !exists {
			return config.ParserConfig{}, config.DBConfig{}, fmt.Errorf("target mapping Col %d has no active reader column", mapping.Col)
		}
		target := strings.TrimSpace(mapping.Target)
		if target == "" {
			return config.ParserConfig{}, config.DBConfig{}, fmt.Errorf("target mapping Col %d has an empty target field", mapping.Col)
		}
		if _, duplicate := targets[target]; duplicate {
			return config.ParserConfig{}, config.DBConfig{}, fmt.Errorf("target mapping contains duplicate field %s", target)
		}
		targets[target] = struct{}{}
		dbConfig.Columns = append(dbConfig.Columns, target)
		dbConfig.ColumnMappings = append(dbConfig.ColumnMappings, config.FieldMapping{Source: column.SourceName, Target: target})
	}
	appendMetadataMapping := func(target, canonical string) error {
		target = strings.TrimSpace(target)
		if target == "" {
			return nil
		}
		if _, duplicate := targets[target]; duplicate {
			return fmt.Errorf("metadata target column %s duplicates an existing mapping", target)
		}
		targets[target] = struct{}{}
		dbConfig.Columns = append(dbConfig.Columns, target)
		dbConfig.ColumnMappings = append(dbConfig.ColumnMappings, config.FieldMapping{Source: canonical, Target: target})
		return nil
	}
	if err := appendMetadataMapping(legacy.FieldFilename, metadataFileName); err != nil {
		return config.ParserConfig{}, config.DBConfig{}, err
	}
	if err := appendMetadataMapping(legacy.FieldFiledate, metadataFileDate); err != nil {
		return config.ParserConfig{}, config.DBConfig{}, err
	}
	return parserConfig, dbConfig, nil
}

func defaultSourceSelector(parserConfig config.ParserConfig, column readerColumn) string {
	switch parserConfig.FileType {
	case config.FileTypeDelimited, config.FileTypeCSV, config.FileTypeTSV,
		config.FileTypeHTML, config.FileTypeHTM, config.FileTypeXLS, config.FileTypeXLSX:
		if parserConfig.HasHeader {
			return column.SourceName
		}
		return strconv.Itoa(column.Col - 1)
	case config.FileTypeRaw, config.FileTypeText:
		return "raw"
	default:
		return column.SourceName
	}
}

func splitTargetTable(value string) (string, string, error) {
	parts := strings.Split(strings.TrimSpace(value), ".")
	switch len(parts) {
	case 1:
		if !identifierPattern.MatchString(parts[0]) {
			return "", "", errors.New("TargetTable must be a safe SQL identifier")
		}
		return defaultSchema, parts[0], nil
	case 2:
		if !identifierPattern.MatchString(parts[0]) || !identifierPattern.MatchString(parts[1]) {
			return "", "", errors.New("TargetTable must use safe schema.table identifiers")
		}
		return parts[0], parts[1], nil
	default:
		return "", "", errors.New("TargetTable must be table or schema.table")
	}
}

func validateTargetColumns(ctx context.Context, db *sql.DB, cfg config.DBConfig) error {
	rows, err := db.QueryContext(ctx, `SELECT c.[name]
FROM sys.columns c
INNER JOIN sys.tables t ON t.[object_id] = c.[object_id]
INNER JOIN sys.schemas s ON s.[schema_id] = t.[schema_id]
WHERE s.[name] = @p1 AND t.[name] = @p2`, cfg.Schema, cfg.Table)
	if err != nil {
		return fmt.Errorf("inspect target table columns: %w", err)
	}
	defer rows.Close()
	existing := make(map[string]struct{})
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("scan target table column: %w", err)
		}
		existing[strings.ToLower(name)] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read target table columns: %w", err)
	}
	if len(existing) == 0 {
		return parserPlanError(errors.New("configured target table was not found"))
	}
	for _, column := range cfg.Columns {
		if _, found := existing[strings.ToLower(column)]; !found {
			return parserPlanError(fmt.Errorf("configured target column %s was not found", column))
		}
	}
	return nil
}

func parserPlanError(err error) error {
	if err == nil {
		return nil
	}
	var jobErr *JobError
	if errors.As(err, &jobErr) {
		return jobErr
	}
	return &JobError{Kind: ErrorConfig, Message: "dynamic parser configuration is invalid", Cause: err}
}

func classifyStoreLoadError(err error) error {
	if err == nil {
		return nil
	}
	var jobErr *JobError
	if errors.As(err, &jobErr) {
		return jobErr
	}
	return &JobError{Kind: ErrorDatabase, Message: "unable to load dynamic parser database configuration", Cause: err}
}

func openSQLServer(ctx context.Context, cfg StoreConfig, database string, port int, user, password, host string) (*sql.DB, error) {
	hostPort := net.JoinHostPort(host, strconv.Itoa(port))
	connectionURL := &url.URL{Scheme: "sqlserver", Host: hostPort}
	if user != "" {
		connectionURL.User = url.UserPassword(user, password)
	}
	query := connectionURL.Query()
	query.Set("database", database)
	seconds := int64((cfg.ConnectTimeout + time.Second - 1) / time.Second)
	query.Set("connection timeout", strconv.FormatInt(seconds, 10))
	switch strings.ToLower(strings.TrimSpace(cfg.SSLMode)) {
	case "disable", "disabled", "false":
		query.Set("encrypt", "disable")
	case "require", "required", "true":
		query.Set("encrypt", "true")
	case "skip-verify", "skip_verify":
		query.Set("encrypt", "true")
		query.Set("TrustServerCertificate", "true")
	}
	connectionURL.RawQuery = query.Encode()
	db, err := sql.Open("sqlserver", connectionURL.String())
	if err != nil {
		return nil, errors.New("open SQL Server connection failed")
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(5 * time.Minute)
	pingCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, errors.New("ping SQL Server connection failed")
	}
	return db, nil
}

func (s *SQLStore) qualified(name string) string {
	return quoteIdentifier(s.config.Schema) + "." + quoteIdentifier(name)
}

func quoteIdentifier(value string) string { return "[" + value + "]" }

func decodeDelimiter(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case `\T`, "TAB":
		return "\t"
	case `\N`:
		return "\n"
	case `\R`:
		return "\r"
	default:
		return value
	}
}
