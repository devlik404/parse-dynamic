// Package memory provides a generic in-memory adapter for legacy rule tests
// and compatibility wiring. It intentionally contains no seeded business rule.
package memory

import (
	"errors"

	"parser-engine/internal/domain/rule"
)

type RuleRepository struct {
	rules map[string]*rule.RuleVersion
}

func NewRuleRepository(rules ...*rule.RuleVersion) *RuleRepository {
	repository := &RuleRepository{rules: make(map[string]*rule.RuleVersion, len(rules))}
	for _, version := range rules {
		if version != nil && version.ID != "" {
			repository.rules[version.ID] = version
		}
	}
	return repository
}

func (r *RuleRepository) GetActiveRuleVersion(ruleID string) (*rule.RuleVersion, error) {
	if r == nil {
		return nil, errors.New("rule repository is nil")
	}
	version, ok := r.rules[ruleID]
	if !ok {
		return nil, errors.New("rule not found: " + ruleID)
	}
	return version, nil
}
