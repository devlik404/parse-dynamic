package parser

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unsafe"

	"parser-engine/internal/config"
	"parser-engine/internal/model"
)

const atmBersamaSectionedSample = `RH|2|20260702
SH|ATMB-000|RPT_DATE|TR_DATE|TR_TIME|ACQ_ID|ISS_ID|BNF_ID|BNF_MBR_ID|FWD_ID|TRML_ID|TRML_CD|CARD_NUMBER|FROM_ACCT_NBR|TRAN_CODE|REV|DB|CR|AMOUNT|XRATE|DB_RC|CR_RC|ISS_TRACE_NBR|BNF_TRACE_NBR|REF_NBR|RECEIPT_NBR|TO_ACCT_NBR|CUST_REF_NBR|ACQ_FEE|BNF_FEE|SW_FEE
SB|ATMB-000|20260702|260701|110103|2|126||||161929||504986400000000068||0110||D||100000||00||||161929030019|19|||6510|0|991
SF|ATMB-000|20260702|4|4|600000
SH|ATMB-020|RPT_DATE|TR_DATE|TR_TIME|ACQ_ID|ISS_ID|BNF_ID|BNF_MBR_ID|FWD_ID|TRML_ID|TRML_CD|CARD_NUMBER|FROM_ACCT_NBR|TRAN_CODE|REV|DB|CR|AMOUNT|XRATE|DB_RC|CR_RC|ISS_TRACE_NBR|BNF_TRACE_NBR|REF_NBR|RECEIPT_NBR|TO_ACCT_NBR|CUST_REF_NBR|ACQ_FEE|BNF_FEE|SW_FEE
SB|ATMB-020|20260702|260701|102203|13|13|2|||86566857|6014|5893859990001354|701075331|4020|||C|10000|||76||013941|000000308996|65250|88880999753|0865260701308996|0|0|0
SF|ATMB-020|20260702|1|1|10000
SH|ATMB-030|RPT_DATE|TR_DATE|TR_TIME|ACQ_ID|ISS_ID|BNF_ID|BNF_MBR_ID|FWD_ID|TRML_ID|TRML_CD|CARD_NUMBER|FROM_ACCT_NBR|TRAN_CODE|REV|DB|CR|AMOUNT|XRATE|DB_RC|CR_RC|ISS_TRACE_NBR|BNF_TRACE_NBR|REF_NBR|RECEIPT_NBR|TO_ACCT_NBR|CUST_REF_NBR|ACQ_FEE|BNF_FEE|SW_FEE
SB|ATMB-030|20260702|260701|104841|2|126||||161929||504986400000000068||0110||D||100000||00||||161929000015|15|||0|0|0
SF|ATMB-030|20260702|6|3|350000
SH|ATMB-510|RPT_DATE|BANK_ID|INQ_TRX|INQ_FEE_ACQ|INQ_FEE_SW|WD_TRX|WD_FEE_ACQ|WD_FEE_SW|WD_AMOUNT|TRF_TRX_BNF|TRF_FEE_BNF|TRF_FEE_SW|TRF_AMOUNT_BNF|TRF_TRX_ACQ|TRF_FEE_ACQ|PDCL_INQ_TRX|PDCL_INQ_FEE_ACQ|PDCL_INQ_FEE_SW|PDCL_WD_TRX|PDCL_WD_FEE_ACQ|PDCL_WD_FEE_SW|PDCL_TRF_TRX_ACQ|PDCL_TRF_FEE_ACQ|PDCL_TRF_FEE_SW|TRF_TRX_BNF|TRF_FEE_BNF|TRF_FEE_SW|TRF_AMOUNT_BNF|TRF_TRX_ACQ|TRF_FEE_ACQ|TOTAL_TRX|TOTAL_FEE|TOTAL_FEE_SW|TOTAL_TRX_AMOUNT|CLAIM_TRX|CLAIM_FEE|CLAIM_AMOUNT|TOTAL_GROSS
SB|ATMB-510|20260702|126|0|0|0|4|26040|3960|600000|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|4|26040|3960|60000|0|0|0|626030
SF|ATMB-510|20260702|1|4|626040
RF|2|20260702
`

func sectionedTestConfig() config.ParserConfig {
	return config.ParserConfig{
		FileType:       config.FileTypeSectionedDelimited,
		Delimiter:      "|",
		MaxRecordBytes: 1024 * 1024,
		MaxFields:      100,
		Sectioned: config.SectionedDelimitedConfig{
			RecordTypeIndex:       0,
			SectionKeyIndex:       1,
			FileHeaderCode:        "RH",
			SectionHeaderCode:     "SH",
			DataCode:              "SB",
			SectionFooterCode:     "SF",
			FileFooterCode:        "RF",
			HeaderStartIndex:      2,
			DataStartIndex:        2,
			DuplicateHeaderPolicy: config.DuplicateHeaderSuffixIndex,
		},
	}
}

func collectSectioned(t *testing.T, cfg config.ParserConfig, input string) ([]model.SourceRecord, error) {
	t.Helper()
	decoder, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	var records []model.SourceRecord
	err = decoder.Parse(context.Background(), strings.NewReader(input), "atm.dat", func(record model.SourceRecord) error {
		records = append(records, record)
		return nil
	})
	return records, err
}

func TestSectionedDelimitedParsesATMShapeWithDynamicSchemas(t *testing.T) {
	records, err := collectSectioned(t, sectionedTestConfig(), atmBersamaSectionedSample)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(records) != 4 {
		t.Fatalf("got %d data records, want 4", len(records))
	}
	if records[0].LineNumber != 3 || records[3].LineNumber != 12 {
		t.Fatalf("data line positions = %d..%d, want 3..12", records[0].LineNumber, records[3].LineNumber)
	}
	if got := records[0].Values["section_key"]; got != "ATMB-000" {
		t.Fatalf("first section key = %#v", got)
	}
	if got := records[0].Values["AMOUNT"]; got != "100000" {
		t.Fatalf("first flattened AMOUNT = %#v", got)
	}
	firstPayload, ok := records[0].Values["payload"].(map[string]any)
	if !ok || firstPayload["CARD_NUMBER"] != "504986400000000068" {
		t.Fatalf("first payload = %#v", records[0].Values["payload"])
	}
	lastPayload, ok := records[3].Values["payload"].(map[string]any)
	if !ok {
		t.Fatalf("last payload type = %T", records[3].Values["payload"])
	}
	if _, exists := lastPayload["TRF_TRX_BNF__2"]; !exists {
		t.Fatalf("duplicate dynamic name was not suffixed: %#v", lastPayload)
	}
	if got := lastPayload["TOTAL_GROSS"]; got != "626030" {
		t.Fatalf("last TOTAL_GROSS = %#v", got)
	}
}

func TestSectionedDelimitedDataBeforeHeaderAndCountMismatchAreRecoverable(t *testing.T) {
	input := "RH|1\n" +
		"SB|A|orphan\n" +
		"SH|A|ONE|TWO\n" +
		"SB|A|only-one\n" +
		"SB|A|one|two\n" +
		"SF|A\n" +
		"RF|1\n"
	records, err := collectSectioned(t, sectionedTestConfig(), input)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("got %d records, want 3", len(records))
	}
	if records[0].Error == nil || records[0].Error.Code != "SECTION_HEADER_MISSING" {
		t.Fatalf("missing-header error = %#v", records[0].Error)
	}
	if records[1].Error == nil || records[1].Error.Code != "COLUMN_COUNT_MISMATCH" {
		t.Fatalf("count error = %#v", records[1].Error)
	}
	if records[2].Error != nil || records[2].Values["TWO"] != "two" {
		t.Fatalf("resynchronized record = %#v", records[2])
	}
}

func TestSectionedDelimitedDuplicateHeaderPolicies(t *testing.T) {
	input := "RH|1\nSH|A|VALUE|VALUE\nSB|A|first|second\nSF|A\nRF|1\n"
	errorCfg := sectionedTestConfig()
	errorCfg.Sectioned.DuplicateHeaderPolicy = config.DuplicateHeaderError
	records, err := collectSectioned(t, errorCfg, input)
	if err == nil || !strings.Contains(err.Error(), "duplicate dynamic column") || len(records) != 0 {
		t.Fatalf("ERROR policy records/error = %#v/%v", records, err)
	}

	suffixCfg := sectionedTestConfig()
	records, err = collectSectioned(t, suffixCfg, input)
	if err != nil || len(records) != 1 {
		t.Fatalf("SUFFIX_INDEX records/error = %#v/%v", records, err)
	}
	payload := records[0].Values["payload"].(map[string]any)
	if payload["VALUE"] != "first" || payload["VALUE__2"] != "second" || records[0].Values["VALUE__2"] != "second" {
		t.Fatalf("suffixed values = %#v", records[0].Values)
	}
}

func TestNormalizeSectionHeadersSuffixesInLinearPass(t *testing.T) {
	headers := make([]string, 100_000)
	for index := range headers {
		headers[index] = "VALUE"
	}
	result, err := normalizeSectionHeaders(headers, config.DuplicateHeaderSuffixIndex)
	if err != nil {
		t.Fatal(err)
	}
	if result[0] != "VALUE" || result[1] != "VALUE__2" || result[len(result)-1] != "VALUE__100000" {
		t.Fatalf("unexpected suffix boundaries: %q, %q, %q", result[0], result[1], result[len(result)-1])
	}

	result, err = normalizeSectionHeaders([]string{"A", "A__2", "A"}, config.DuplicateHeaderSuffixIndex)
	if err != nil || strings.Join(result, ",") != "A,A__2,A__3" {
		t.Fatalf("collision result/error = %#v/%v", result, err)
	}
}

func TestNormalizeSectionHeadersRejectsReservedSourceNames(t *testing.T) {
	for _, name := range []string{"record_type", "section_key", "payload", "0", "12"} {
		if _, err := normalizeSectionHeaders([]string{name}, config.DuplicateHeaderSuffixIndex); err == nil {
			t.Fatalf("header %q was accepted", name)
		}
	}
}

func TestSectionedDelimitedPayloadDetachesSelectedValueFromIgnoredBacking(t *testing.T) {
	cfg := sectionedTestConfig()
	cfg.AllowExtraColumns = true
	ignored := strings.Repeat("x", 4096)
	input := "RH|1\nSH|A|VALUE\nSB|A|ok|" + ignored + "\nSF|A\nRF|1\n"
	records, err := collectSectioned(t, cfg, input)
	if err != nil || len(records) != 1 {
		t.Fatalf("records/error = %#v/%v", records, err)
	}
	raw := records[0].Raw.(string)
	selectedFromRaw := strings.Split(raw, "|")[2]
	payload := records[0].Values["payload"].(map[string]any)
	detached := payload["VALUE"].(string)
	if detached != selectedFromRaw || unsafe.StringData(detached) == unsafe.StringData(selectedFromRaw) {
		t.Fatal("payload retained the full physical-line backing string")
	}
}

func TestSectionedDelimitedLimitsRecoverAndResynchronizeDataRecords(t *testing.T) {
	t.Run("max fields", func(t *testing.T) {
		cfg := sectionedTestConfig()
		cfg.MaxFields = 3
		input := "RH|1\nSH|A|COL\nSB|A|one|extra\nSB|A|ok\nSF|A\nRF|1\n"
		records, err := collectSectioned(t, cfg, input)
		if err != nil || len(records) != 2 {
			t.Fatalf("records/error = %#v/%v", records, err)
		}
		if records[0].Error == nil || records[0].Error.Code != "RECORD_TOO_COMPLEX" || records[1].Values["COL"] != "ok" {
			t.Fatalf("records = %#v", records)
		}
	})

	t.Run("max bytes", func(t *testing.T) {
		cfg := sectionedTestConfig()
		cfg.MaxRecordBytes = 12
		input := "RH|1\nSH|A|COL\nSB|A|this-value-is-too-long\nSB|A|ok\nSF|A\nRF|1\n"
		records, err := collectSectioned(t, cfg, input)
		if err != nil || len(records) != 2 {
			t.Fatalf("records/error = %#v/%v", records, err)
		}
		if records[0].Error == nil || records[0].Error.Code != "RECORD_TOO_LARGE" || records[1].Values["COL"] != "ok" {
			t.Fatalf("records = %#v", records)
		}
	})
}

func TestSectionedDelimitedUnsafeSequenceIsFatal(t *testing.T) {
	input := "RH|1\nSH|A|ONE\nSH|B|TWO\nSB|B|value\nSF|B\nRF|1\n"
	records, err := collectSectioned(t, sectionedTestConfig(), input)
	if err == nil || !strings.Contains(err.Error(), "missing its footer") || len(records) != 0 {
		t.Fatalf("records/error = %#v/%v", records, err)
	}
}

func TestSectionedDelimitedRejectsBoundedLimitViolationsAfterFileFooter(t *testing.T) {
	base := "RH|1\nSH|A|COL\nSF|A\nRF|1\n"
	tests := []struct {
		name  string
		edit  func(*config.ParserConfig)
		extra string
	}{
		{
			name:  "oversized data",
			edit:  func(cfg *config.ParserConfig) { cfg.MaxRecordBytes = 12 },
			extra: "SB|A|this-is-too-long\n",
		},
		{
			name:  "too many fields",
			edit:  func(cfg *config.ParserConfig) { cfg.MaxFields = 3 },
			extra: "SB|A|one|two\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := sectionedTestConfig()
			test.edit(&cfg)
			records, err := collectSectioned(t, cfg, base+test.extra)
			if err == nil || !strings.Contains(err.Error(), "after file footer") || len(records) != 0 {
				t.Fatalf("records/error = %#v/%v", records, err)
			}
		})
	}
}

func TestSectionedDelimitedErrorsDoNotEchoSectionKeys(t *testing.T) {
	input := "RH|1\nSH|SECRET_ACTIVE|COL\nSB|SECRET_WRONG|value\nSF|SECRET_WRONG\nRF|1\n"
	records, err := collectSectioned(t, sectionedTestConfig(), input)
	if err == nil || len(records) != 1 || records[0].Error == nil {
		t.Fatalf("records/error = %#v/%v", records, err)
	}
	for _, message := range []string{records[0].Error.Message, err.Error()} {
		if strings.Contains(message, "SECRET_ACTIVE") || strings.Contains(message, "SECRET_WRONG") {
			t.Fatalf("error message exposes section key: %q", message)
		}
	}
	if raw, _ := records[0].Error.RawValue.(string); !strings.Contains(raw, "SECRET_WRONG") {
		t.Fatalf("raw value should remain available in its dedicated field: %#v", records[0].Error.RawValue)
	}
}

func TestSectionedDelimitedHonorsCanceledContext(t *testing.T) {
	decoder, err := New(sectionedTestConfig())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = decoder.Parse(ctx, strings.NewReader(atmBersamaSectionedSample), "atm.dat", func(model.SourceRecord) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Parse() error = %v, want context.Canceled", err)
	}
}
