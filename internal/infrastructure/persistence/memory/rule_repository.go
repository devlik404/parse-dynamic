package memory

import (
	"errors"

	"parser-engine/internal/domain/rule"
)

/*
MemoryRuleRepository
--------------------
In-memory implementation dari rule.Repository

Catatan penting:
- Strukturnya MENIRU 1:1 hasil load dari DB ParamDynamic
- Engine tidak tahu ini memory atau SQL
*/

type RuleRepository struct {
	rules map[string]*rule.RuleVersion
}

// NewRuleRepository
// -----------------
// Seed rule contoh (REAL) untuk file QR report fixed-width
func NewRuleRepository() *RuleRepository {
	repo := &RuleRepository{
		rules: make(map[string]*rule.RuleVersion),
	}

	/*
		====================================================
		RULE: QR_REPORT_SENDER
		====================================================
		- HEADER    → di-ignore
		- DATA      → fixed width
		- SUBTOTAL  → di-ignore
		- TOTAL     → di-ignore
	*/

	repo.rules["QR_REPORT_SENDER"] = &rule.RuleVersion{
		ID: "QR_REPORT_SENDER",
		Sections: []rule.SectionRule{

			// ========================
			// HEADER SECTION
			// ========================
			{
				Name:         "HEADER",
				StartPattern: `^(LAPORAN TRANSAKSI|PT\. ARTAJASA|No Report|Layanan|Nama Bank|Kode|Posisi)`,
				Record: rule.RecordRule{
					Ignore: true,
				},
			},

			// ========================
			// COLUMN HEADER (IGNORE)
			// ========================
			{
				Name:         "COLUMN_HEADER",
				StartPattern: `^No\.\s+Trx_Code`,
				Record: rule.RecordRule{
					Ignore: true,
				},
			},

			// ========================
			// DATA SECTION (FIXED WIDTH)
			// ========================
			{
				Name:         "DATA",
				StartPattern: `^[0-9]{6}\s`,
				Record: rule.RecordRule{
					Type: "fixed_width",
					Fields: []rule.FieldRule{
						{
							Name:     "row_no",
							Start:    0,
							Length:   6,
							DataType: "integer",
						},
						{
							Name:     "trx_code",
							Start:    7,
							Length:   6,
							DataType: "string",
						},
						{
							Name:     "trx_date",
							Start:    14,
							Length:   8,
							DataType: "date",
						},
						{
							Name:     "trx_time",
							Start:    24,
							Length:   8,
							DataType: "string",
						},
						{
							Name:     "ref_no",
							Start:    33,
							Length:   12,
							DataType: "string",
						},
						{
							Name:     "trace_no",
							Start:    46,
							Length:   6,
							DataType: "string",
						},
						{
							Name:     "terminal_id",
							Start:    53,
							Length:   8,
							DataType: "string",
						},
						{
							Name:     "merchant_pan",
							Start:    62,
							Length:   19,
							DataType: "string",
						},
						{
							Name:     "amount",
							Start:    104,
							Length:   13,
							DataType: "decimal",
						},
					},
				},
			},

			// ========================
			// SUBTOTAL SECTION (IGNORE)
			// ========================
			{
				Name:         "SUBTOTAL",
				StartPattern: `^SUB TOTAL`,
				Record: rule.RecordRule{
					Ignore: true,
				},
			},

			// ========================
			// TOTAL SECTION (IGNORE)
			// ========================
			{
				Name:         "TOTAL",
				StartPattern: `^TOTAL `,
				Record: rule.RecordRule{
					Ignore: true,
				},
			},

			// ========================
			// END OF PAGE
			// ========================
			{
				Name:         "END",
				StartPattern: `^END OF PAGES`,
				Record: rule.RecordRule{
					Ignore: true,
				},
			},
		},
	}

	return repo
}

// GetActiveRuleVersion
// --------------------
// Implementasi interface domain rule.Repository
func (r *RuleRepository) GetActiveRuleVersion(ruleID string) (*rule.RuleVersion, error) {
	ruleVer, ok := r.rules[ruleID]
	if !ok {
		return nil, errors.New("rule not found: " + ruleID)
	}
	return ruleVer, nil
}
