package rule

type Repository interface {
	GetActiveRuleVersion(ruleID string) (*RuleVersion, error)
}
