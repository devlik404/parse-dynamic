package parser

import (
	"errors"
	"regexp"

	"parser-engine/internal/domain/rule"
)

/*
====================================================
ENGINE
====================================================

Tanggung jawab Engine:
- Mencocokkan baris dengan SectionRule
- Menentukan apakah baris di-parse atau di-ignore
- Memanggil parser sesuai RecordRule (delimited / fixed_width)

Engine:
- TIDAK tahu DB
- TIDAK tahu file format spesifik
- TIDAK tahu bisnis
*/

type Engine struct {
	rule *rule.RuleVersion
	fsm  *sectionFSM
}

// NewEngine
// ---------
// Membuat parser engine dengan RuleVersion (immutable snapshot)
func NewEngine(ruleVersion *rule.RuleVersion) (*Engine, error) {
	if ruleVersion == nil {
		return nil, errors.New("ruleVersion is nil")
	}
	if len(ruleVersion.Sections) == 0 {
		return nil, errors.New("ruleVersion has no sections")
	}

	return &Engine{
		rule: ruleVersion,
		fsm:  newSectionFSM(ruleVersion.Sections),
	}, nil
}

// ParseLine
// ---------
// Memproses satu baris text.
// Return:
// - parsed record (map)
// - ok = true jika baris valid & diparse
// - error jika parsing gagal
func (e *Engine) ParseLine(line string) (map[string]interface{}, bool, error) {
	if line == "" {
		return nil, false, nil
	}

	section := e.fsm.match(line)
	if section == nil {
		// baris tidak termasuk section manapun → ignore
		return nil, false, nil
	}

	// Section di-ignore (header / footer / separator)
	if section.Record.Ignore {
		return nil, false, nil
	}

	switch section.Record.Type {
	case "delimited":
		return parseDelimited(line, section.Record)

	case "fixed_width":
		return parseFixedWidth(line, section.Record)

	default:
		return nil, false, errors.New("unsupported record type: " + section.Record.Type)
	}
}

//
// ====================================================
// SECTION FSM (PRIVATE)
// ====================================================
//

type sectionFSM struct {
	sections []compiledSection
}

type compiledSection struct {
	rule       rule.SectionRule
	startRegex *regexp.Regexp
	endRegex   *regexp.Regexp
}

func newSectionFSM(sections []rule.SectionRule) *sectionFSM {
	var compiled []compiledSection

	for _, s := range sections {
		c := compiledSection{
			rule: s,
		}

		if s.StartPattern != "" {
			c.startRegex = regexp.MustCompile(s.StartPattern)
		}
		if s.EndPattern != "" {
			c.endRegex = regexp.MustCompile(s.EndPattern)
		}

		compiled = append(compiled, c)
	}

	return &sectionFSM{
		sections: compiled,
	}
}

// match
// -----
// Cari section yang cocok untuk baris ini.
// Priority: urutan section di rule.
func (f *sectionFSM) match(line string) *rule.SectionRule {
	for _, s := range f.sections {
		if s.startRegex != nil && s.startRegex.MatchString(line) {
			return &s.rule
		}
	}
	return nil
}

//
// ====================================================
// RECORD PARSER (PRIVATE)
// ====================================================
//

func parseDelimited(line string, r rule.RecordRule) (map[string]interface{}, bool, error) {
	if r.Delimiter == "" {
		return nil, false, errors.New("delimiter is empty")
	}

	fields := splitByDelimiter(line, r.Delimiter)
	result := make(map[string]interface{})

	for _, f := range r.Fields {
		if f.Index < 0 || f.Index >= len(fields) {
			continue
		}

		val, err := castValue(fields[f.Index], f.DataType)
		if err != nil {
			return nil, false, err
		}

		result[f.Name] = val
	}

	return result, true, nil
}

func parseFixedWidth(line string, r rule.RecordRule) (map[string]interface{}, bool, error) {
	result := make(map[string]interface{})

	for _, f := range r.Fields {
		if f.Start < 0 || f.Length <= 0 {
			continue
		}

		end := f.Start + f.Length
		if end > len(line) {
			continue
		}

		raw := line[f.Start:end]
		val, err := castValue(raw, f.DataType)
		if err != nil {
			return nil, false, err
		}

		result[f.Name] = val
	}

	return result, true, nil
}
