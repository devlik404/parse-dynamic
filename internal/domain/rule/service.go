package rule

import (
	"errors"
	"fmt"
	"regexp"
)

/*
====================================================
RULE SERVICE (DOMAIN SERVICE)
====================================================

Tanggung jawab:
- Validasi RuleVersion
- Pastikan rule konsisten & aman dipakai engine
- TIDAK tahu DB
- TIDAK tahu parser implementation
*/

type Service struct{}

// NewService
// ----------
// Stateless domain service
func NewService() *Service {
	return &Service{}
}

// Validate
// --------
// Validasi RuleVersion secara menyeluruh
func (s *Service) Validate(rule *RuleVersion) error {
	if rule == nil {
		return errors.New("rule version is nil")
	}

	if rule.ID == "" {
		return errors.New("rule id is empty")
	}

	if len(rule.Sections) == 0 {
		return errors.New("rule has no sections")
	}

	for i, sec := range rule.Sections {
		if sec.Name == "" {
			return fmt.Errorf("section[%d] name is empty", i)
		}

		// ===== Validate regex =====
		if sec.StartPattern == "" {
			return fmt.Errorf("section[%s] start pattern is empty", sec.Name)
		}

		if _, err := regexp.Compile(sec.StartPattern); err != nil {
			return fmt.Errorf("section[%s] invalid start pattern: %v", sec.Name, err)
		}

		if sec.EndPattern != "" {
			if _, err := regexp.Compile(sec.EndPattern); err != nil {
				return fmt.Errorf("section[%s] invalid end pattern: %v", sec.Name, err)
			}
		}

		// ===== Validate record rule =====
		if err := s.validateRecordRule(sec.Name, sec.Record); err != nil {
			return err
		}
	}

	return nil
}

// ================================
// PRIVATE HELPERS
// ================================

func (s *Service) validateRecordRule(sectionName string, r RecordRule) error {
	if r.Ignore {
		// ignore section boleh tanpa detail
		return nil
	}

	switch r.Type {

	case "delimited":
		if r.Delimiter == "" {
			return fmt.Errorf("section[%s] delimiter is empty", sectionName)
		}
		if len(r.Fields) == 0 {
			return fmt.Errorf("section[%s] has no fields", sectionName)
		}
		for i, f := range r.Fields {
			if f.Name == "" {
				return fmt.Errorf("section[%s] field[%d] name is empty", sectionName, i)
			}
			if f.Index < 0 {
				return fmt.Errorf("section[%s] field[%s] invalid index", sectionName, f.Name)
			}
			if f.DataType == "" {
				return fmt.Errorf("section[%s] field[%s] data type is empty", sectionName, f.Name)
			}
		}

	case "fixed_width":
		if len(r.Fields) == 0 {
			return fmt.Errorf("section[%s] has no fields", sectionName)
		}

		used := make(map[int]string)
		for _, f := range r.Fields {
			if f.Name == "" {
				return fmt.Errorf("section[%s] field name is empty", sectionName)
			}
			if f.Start < 0 || f.Length <= 0 {
				return fmt.Errorf(
					"section[%s] field[%s] invalid start/length",
					sectionName, f.Name,
				)
			}

			// overlap detection (simple)
			for i := f.Start; i < f.Start+f.Length; i++ {
				if prev, ok := used[i]; ok {
					return fmt.Errorf(
						"section[%s] field[%s] overlaps with field[%s]",
						sectionName, f.Name, prev,
					)
				}
				used[i] = f.Name
			}

			if f.DataType == "" {
				return fmt.Errorf("section[%s] field[%s] data type is empty", sectionName, f.Name)
			}
		}

	default:
		return fmt.Errorf("section[%s] unsupported record type: %s", sectionName, r.Type)
	}

	return nil
}
