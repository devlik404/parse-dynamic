package sqlrepo

import (
	"fmt"
	"strings"

	"parser-engine/internal/config"
	"parser-engine/internal/model"
)

const defaultBatchSize = 500

type insertColumn struct {
	database  string
	canonical string
	quoted    string
}

type insertBuilder struct {
	dialect        dialect
	qualifiedTable string
	columns        []insertColumn
}

func newInsertBuilder(cfg config.DBConfig, d dialect) (insertBuilder, error) {
	if strings.TrimSpace(cfg.Table) == "" {
		return insertBuilder{}, fmt.Errorf("DB_TABLE is required")
	}
	quotedTable, err := d.quoteIdentifier(cfg.Table)
	if err != nil {
		return insertBuilder{}, fmt.Errorf("invalid DB_TABLE: %w", err)
	}
	qualifiedTable := quotedTable
	if cfg.Schema != "" {
		quotedSchema, err := d.quoteIdentifier(cfg.Schema)
		if err != nil {
			return insertBuilder{}, fmt.Errorf("invalid DB_SCHEMA: %w", err)
		}
		qualifiedTable = quotedSchema + "." + quotedTable
	}
	if len(cfg.Columns) == 0 {
		return insertBuilder{}, fmt.Errorf("DB_COLUMNS must contain at least one column")
	}
	if len(cfg.Columns) > d.maxParameters {
		return insertBuilder{}, fmt.Errorf("DB_COLUMNS has %d columns, exceeding the %s parameter limit of %d", len(cfg.Columns), d.name, d.maxParameters)
	}

	columnSet := make(map[string]struct{}, len(cfg.Columns))
	canonicalByDatabase := make(map[string]string, len(cfg.ColumnMappings))
	canonicalSet := make(map[string]struct{}, len(cfg.ColumnMappings))
	for index, mapping := range cfg.ColumnMappings {
		canonical := strings.TrimSpace(mapping.Source)
		database := strings.TrimSpace(mapping.Target)
		if canonical == "" || database == "" {
			return insertBuilder{}, fmt.Errorf("DB_COLUMN_MAPPING entry %d must be canonical_field:database_column", index+1)
		}
		if _, exists := canonicalSet[canonical]; exists {
			return insertBuilder{}, fmt.Errorf("DB_COLUMN_MAPPING contains duplicate canonical field %q", canonical)
		}
		if _, exists := canonicalByDatabase[database]; exists {
			return insertBuilder{}, fmt.Errorf("DB_COLUMN_MAPPING contains duplicate database column %q", database)
		}
		canonicalSet[canonical] = struct{}{}
		canonicalByDatabase[database] = canonical
	}

	columns := make([]insertColumn, 0, len(cfg.Columns))
	for _, rawColumn := range cfg.Columns {
		column := strings.TrimSpace(rawColumn)
		if column == "" {
			return insertBuilder{}, fmt.Errorf("DB_COLUMNS cannot contain an empty column")
		}
		if _, exists := columnSet[column]; exists {
			return insertBuilder{}, fmt.Errorf("DB_COLUMNS contains duplicate column %q", column)
		}
		quoted, err := d.quoteIdentifier(column)
		if err != nil {
			return insertBuilder{}, fmt.Errorf("invalid DB_COLUMNS entry: %w", err)
		}
		columnSet[column] = struct{}{}
		canonical := canonicalByDatabase[column]
		if canonical == "" {
			canonical = column
		}
		columns = append(columns, insertColumn{
			database:  column,
			canonical: canonical,
			quoted:    quoted,
		})
	}
	for database := range canonicalByDatabase {
		if _, exists := columnSet[database]; !exists {
			return insertBuilder{}, fmt.Errorf("DB_COLUMN_MAPPING targets %q, which is absent from DB_COLUMNS", database)
		}
	}

	return insertBuilder{
		dialect:        d,
		qualifiedTable: qualifiedTable,
		columns:        columns,
	}, nil
}

func (b insertBuilder) build(records []model.Record) (string, []any, error) {
	if len(records) == 0 {
		return "", nil, nil
	}
	parameterCount := len(records) * len(b.columns)
	if parameterCount > b.dialect.maxParameters {
		return "", nil, fmt.Errorf("insert requires %d parameters, exceeding the %s limit of %d", parameterCount, b.dialect.name, b.dialect.maxParameters)
	}
	if b.dialect.maxRowsPerStatement > 0 && len(records) > b.dialect.maxRowsPerStatement {
		return "", nil, fmt.Errorf("insert requires %d rows, exceeding the %s VALUES limit of %d", len(records), b.dialect.name, b.dialect.maxRowsPerStatement)
	}

	var query strings.Builder
	query.Grow(64 + parameterCount*4)
	query.WriteString("INSERT INTO ")
	query.WriteString(b.qualifiedTable)
	query.WriteString(" (")
	for index, column := range b.columns {
		if index > 0 {
			query.WriteByte(',')
		}
		query.WriteString(column.quoted)
	}
	query.WriteString(") VALUES ")

	args := make([]any, 0, parameterCount)
	parameterPosition := 1
	for recordIndex, record := range records {
		if recordIndex > 0 {
			query.WriteByte(',')
		}
		query.WriteByte('(')
		for columnIndex, column := range b.columns {
			if columnIndex > 0 {
				query.WriteByte(',')
			}
			value, exists := record.Fields[column.canonical]
			if !exists {
				return "", nil, fmt.Errorf("record %d is missing canonical field %q for database column %q", record.RecordNumber, column.canonical, column.database)
			}
			normalized, err := normalizeSQLValueForDialect(value, b.dialect.name)
			if err != nil {
				return "", nil, fmt.Errorf("record %d field %q: %w", record.RecordNumber, column.canonical, err)
			}
			query.WriteString(b.dialect.placeholder(parameterPosition))
			parameterPosition++
			args = append(args, normalized)
		}
		query.WriteByte(')')
	}

	return query.String(), args, nil
}

func (b insertBuilder) rowsPerStatement(batchSize int) int {
	rows := normalizedBatchSize(batchSize)
	byParameterLimit := b.dialect.maxParameters / len(b.columns)
	if rows > byParameterLimit {
		rows = byParameterLimit
	}
	if b.dialect.maxRowsPerStatement > 0 && rows > b.dialect.maxRowsPerStatement {
		rows = b.dialect.maxRowsPerStatement
	}
	return rows
}

func normalizedBatchSize(batchSize int) int {
	if batchSize <= 0 {
		return defaultBatchSize
	}
	return batchSize
}
