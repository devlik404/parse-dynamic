package sqlserver

import (
	"database/sql"

	"parser-engine/internal/domain/rule"
)

type RuleRepository struct {
	DB *sql.DB
}

func NewRuleRepository(db *sql.DB) *RuleRepository {
	return &RuleRepository{DB: db}
}

func (r *RuleRepository) GetActiveRuleVersion(ruleCode string) (*rule.RuleVersion, error) {
	// 1️⃣ Ambil rule version aktif
	var ruleVersionID int64
	err := r.DB.QueryRow(`
		SELECT rule_version_id
		FROM rule_version
		WHERE rule_code = @p1 AND is_active = 1
	`, ruleCode).Scan(&ruleVersionID)
	if err != nil {
		return nil, err
	}

	// 2️⃣ Ambil sections
	rows, err := r.DB.Query(`
		SELECT section_id, section_name, start_pattern, end_pattern,
		       record_type, delimiter, ignore_flag
		FROM rule_section
		WHERE rule_version_id = @p1
		ORDER BY section_id
	`, ruleVersionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sections []rule.SectionRule

	for rows.Next() {
		var (
			sectionID  int64
			sec        rule.SectionRule
			recordType string
			delimiter  sql.NullString
			ignore     bool
		)

		err := rows.Scan(
			&sectionID,
			&sec.Name,
			&sec.StartPattern,
			&sec.EndPattern,
			&recordType,
			&delimiter,
			&ignore,
		)
		if err != nil {
			return nil, err
		}

		sec.Record.Type = recordType
		if delimiter.Valid {
			sec.Record.Delimiter = delimiter.String
		}
		sec.Record.Ignore = ignore

		// 3️⃣ Ambil fields per section
		fRows, err := r.DB.Query(`
			SELECT field_name, field_index, start_pos, field_length, data_type
			FROM rule_field
			WHERE section_id = @p1
			ORDER BY field_index, start_pos
		`, sectionID)
		if err != nil {
			return nil, err
		}

		for fRows.Next() {
			var f rule.FieldRule
			fRows.Scan(
				&f.Name,
				&f.Index,
				&f.Start,
				&f.Length,
				&f.DataType,
			)
			sec.Record.Fields = append(sec.Record.Fields, f)
		}
		fRows.Close()

		sections = append(sections, sec)
	}

	return &rule.RuleVersion{
		ID:       ruleCode,
		Sections: sections,
	}, nil
}
