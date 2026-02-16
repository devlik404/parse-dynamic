package usecase

import (
	"fmt"

	"parser-engine/internal/domain/rule"
)

/*
====================================================
LOAD RULE USE CASE
====================================================

Tanggung jawab:
- Mengambil rule aktif berdasarkan ruleID
- Memvalidasi rule (domain service)
- Mengembalikan RuleVersion siap dipakai engine

Tidak:
- Tidak tahu DB
- Tidak tahu cache
- Tidak tahu parser engine detail
*/

type LoadRuleUseCase struct {
	RuleRepo rule.Repository
	RuleSvc  *rule.Service
}

// NewLoadRuleUseCase
func NewLoadRuleUseCase(
	repo rule.Repository,
	svc *rule.Service,
) *LoadRuleUseCase {
	return &LoadRuleUseCase{
		RuleRepo: repo,
		RuleSvc:  svc,
	}
}

// Execute
// -------
// Load + validate rule berdasarkan ruleID
func (uc *LoadRuleUseCase) Execute(ruleID string) (*rule.RuleVersion, error) {
	if ruleID == "" {
		return nil, fmt.Errorf("ruleID is empty")
	}

	// 1️⃣ Load rule dari repository (DB ParamDynamic)
	ruleVer, err := uc.RuleRepo.GetActiveRuleVersion(ruleID)
	if err != nil {
		return nil, fmt.Errorf("failed to load rule [%s]: %w", ruleID, err)
	}

	// 2️⃣ Validasi rule (domain service)
	if err := uc.RuleSvc.Validate(ruleVer); err != nil {
		return nil, fmt.Errorf("invalid rule [%s]: %w", ruleID, err)
	}

	return ruleVer, nil
}
