package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"parser-engine/internal/config"
	"parser-engine/internal/model"
	"parser-engine/internal/repository"
)

func TestRunnerPartialPersistsErrorsAndCommitsValidRecords(t *testing.T) {
	path := writeInput(t, "001|20260811|bca|150000\n002|20260811|bri|not-a-number\n003|20260811|bni|200000\n")
	cfg := delimitedJobConfig(path, config.ErrorModePartial, 1)
	repo := &fakeRepository{}
	sink := &MemoryErrorSink{}
	runner, err := NewRunner(cfg, repo, sink)
	if err != nil {
		t.Fatal(err)
	}

	summary, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if summary.Status != "PARTIAL" || summary.TotalRecords != 3 || summary.InsertedRecords != 2 || summary.RejectedRecords != 1 {
		t.Fatalf("unexpected summary: %#v", summary)
	}
	if got := repo.committedRecords(); len(got) != 2 {
		t.Fatalf("committed %d records, want 2", len(got))
	} else {
		if got[0].Fields["bank"] != "BCA" || got[1].Fields["bank"] != "BNI" {
			t.Fatalf("uppercase transformation was not applied: %#v", got)
		}
		if amount, ok := got[0].Fields["amount"].(json.Number); !ok || amount.String() != "150000" {
			t.Fatalf("amount did not remain an exact decimal: %T(%v)", got[0].Fields["amount"], got[0].Fields["amount"])
		}
	}
	if len(sink.Errors) != 1 || sink.Errors[0].Field != "amount" || sink.Errors[0].Code != "TYPE_CONVERSION" {
		t.Fatalf("unexpected structured errors: %#v", sink.Errors)
	}
	if tx := repo.lastTransaction(); tx == nil || !tx.committed || tx.rolledBack || !reflect.DeepEqual(tx.batchSizes, []int{1, 1}) {
		t.Fatalf("unexpected transaction state: %#v", tx)
	}
}

func TestRunnerStrictRollsBackAlreadyFlushedBatches(t *testing.T) {
	path := writeInput(t, "001|20260811|bca|150000\n002|20260811|bri|invalid\n003|20260811|bni|200000\n")
	cfg := delimitedJobConfig(path, config.ErrorModeStrict, 1)
	repo := &fakeRepository{}
	sink := &MemoryErrorSink{}
	runner, err := NewRunner(cfg, repo, sink)
	if err != nil {
		t.Fatal(err)
	}

	summary, err := runner.Run(context.Background())
	if err == nil {
		t.Fatal("expected STRICT failure")
	}
	if summary.Status != "FAILED" || summary.InsertedRecords != 0 || summary.RejectedRecords != 1 {
		t.Fatalf("unexpected summary: %#v", summary)
	}
	if got := repo.committedRecords(); len(got) != 0 {
		t.Fatalf("STRICT left %d visible records", len(got))
	}
	if tx := repo.lastTransaction(); tx == nil || tx.committed || !tx.rolledBack || !reflect.DeepEqual(tx.batchSizes, []int{1}) {
		t.Fatalf("unexpected transaction state: %#v", tx)
	}
	if len(sink.Errors) != 1 || sink.Errors[0].RecordNumber != 2 {
		t.Fatalf("unexpected error sink: %#v", sink.Errors)
	}
}

func TestRunnerFlushesExactBatchBoundariesAndFinalBatch(t *testing.T) {
	path := writeInput(t, "001|20260811|bca|1\n002|20260811|bca|2\n003|20260811|bca|3\n004|20260811|bca|4\n005|20260811|bca|5\n")
	cfg := delimitedJobConfig(path, config.ErrorModeStrict, 2)
	repo := &fakeRepository{}
	runner, err := NewRunner(cfg, repo, &MemoryErrorSink{})
	if err != nil {
		t.Fatal(err)
	}
	summary, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if summary.InsertedRecords != 5 || summary.Batches != 3 {
		t.Fatalf("unexpected summary: %#v", summary)
	}
	if got := repo.lastTransaction().batchSizes; !reflect.DeepEqual(got, []int{2, 2, 1}) {
		t.Fatalf("batch sizes = %#v, want [2 2 1]", got)
	}
}

func TestRunnerFlushesWhenRetainedByteBudgetIsReached(t *testing.T) {
	path := writeInput(t, "001|20260811|bca|1\n002|20260811|bca|2\n003|20260811|bca|3\n")
	cfg := delimitedJobConfig(path, config.ErrorModeStrict, 100)
	// Every canonical record is larger than this deliberately tiny budget.
	// The runner must retain and flush at most one record at a time.
	cfg.DB.BatchMaxBytes = 1
	repo := &fakeRepository{}
	runner, err := NewRunner(cfg, repo, &MemoryErrorSink{})
	if err != nil {
		t.Fatal(err)
	}
	summary, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if summary.InsertedRecords != 3 || summary.Batches != 3 {
		t.Fatalf("unexpected summary: %#v", summary)
	}
	if got := repo.lastTransaction().batchSizes; !reflect.DeepEqual(got, []int{1, 1, 1}) {
		t.Fatalf("byte-limited batch sizes = %#v, want [1 1 1]", got)
	}
}

func TestRunnerErrorLimitRollsBackWithoutDuplicateSyntheticError(t *testing.T) {
	path := writeInput(t, "001|20260811|bca|invalid\n002|20260811|bri|also-invalid\n")
	cfg := delimitedJobConfig(path, config.ErrorModePartial, 2)
	cfg.Error.MaxCount = 1
	repo := &fakeRepository{}
	sink := &MemoryErrorSink{}
	runner, err := NewRunner(cfg, repo, sink)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := runner.Run(context.Background())
	if err == nil || summary.Status != "FAILED" {
		t.Fatalf("summary/error = %#v, %v", summary, err)
	}
	if len(sink.Errors) != 1 || sink.Errors[0].Code != "TYPE_CONVERSION" {
		t.Fatalf("error sink = %#v", sink.Errors)
	}
	if got := repo.committedRecords(); len(got) != 0 {
		t.Fatalf("error threshold committed %d records", len(got))
	}
}

func TestRunnerDatabaseFailureIsFatalAndRollsBack(t *testing.T) {
	path := writeInput(t, "001|20260811|bca|150000\n")
	cfg := delimitedJobConfig(path, config.ErrorModePartial, 1)
	const sensitive = "customer-secret-123"
	repo := &fakeRepository{insertErr: errors.New("duplicate value " + sensitive)}
	sink := &MemoryErrorSink{}
	runner, err := NewRunner(cfg, repo, sink)
	if err != nil {
		t.Fatal(err)
	}

	summary, err := runner.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "insert batch") {
		t.Fatalf("Run() error = %v", err)
	}
	if summary.Status != "FAILED" || summary.InsertedRecords != 0 || summary.Batches != 0 {
		t.Fatalf("unexpected summary: %#v", summary)
	}
	if tx := repo.lastTransaction(); tx == nil || tx.committed || !tx.rolledBack {
		t.Fatalf("unexpected transaction state: %#v", tx)
	}
	if len(sink.Errors) != 1 || sink.Errors[0].Code != "FATAL_PROCESSING_ERROR" {
		t.Fatalf("fatal error sink = %#v", sink.Errors)
	}
	if sink.Errors[0].RecordNumber != 1 || strings.Contains(sink.Errors[0].Message, sensitive) || strings.Contains(err.Error(), sensitive) {
		t.Fatalf("database failure leaked data or used the wrong record: %#v / %v", sink.Errors[0], err)
	}
}

func TestRunnerPreflushFailureReportsLastRecordInFailedBatch(t *testing.T) {
	path := writeInput(t, "001|20260811|bca|1\n002|20260811|bca|2\n")
	cfg := delimitedJobConfig(path, config.ErrorModePartial, 100)
	firstEstimate := model.EstimateRecordBytes(model.Record{
		File: path, RecordNumber: 1,
		Fields: map[string]any{"id": "001", "date": model.NewDate(time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)), "bank": "BCA", "amount": json.Number("1")},
	})
	cfg.DB.BatchMaxBytes = int(firstEstimate + 1)
	repo := &fakeRepository{insertErr: errors.New("driver detail")}
	sink := &MemoryErrorSink{}
	runner, err := NewRunner(cfg, repo, sink)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background()); err == nil {
		t.Fatal("expected preflush failure")
	}
	if len(sink.Errors) != 1 || sink.Errors[0].RecordNumber != 1 {
		t.Fatalf("fatal sink = %#v, want failed batch ending at record 1", sink.Errors)
	}
}

func TestRunnerFinalFlushAndCommitFailuresArePersisted(t *testing.T) {
	tests := []struct {
		name      string
		batchSize int
		repo      *fakeRepository
		operation string
	}{
		{name: "final flush", batchSize: 2, repo: &fakeRepository{insertErr: errors.New("secret insert detail")}, operation: "insert batch"},
		{name: "commit", batchSize: 1, repo: &fakeRepository{commitErr: errors.New("secret commit detail")}, operation: "commit file transaction"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeInput(t, "001|20260811|bca|150000\n")
			cfg := delimitedJobConfig(path, config.ErrorModePartial, test.batchSize)
			sink := &MemoryErrorSink{}
			runner, err := NewRunner(cfg, test.repo, sink)
			if err != nil {
				t.Fatal(err)
			}
			summary, err := runner.Run(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.operation) || strings.Contains(err.Error(), "secret") {
				t.Fatalf("summary/error = %#v, %v", summary, err)
			}
			if len(sink.Errors) != 1 || sink.Errors[0].RecordNumber != 1 || sink.Errors[0].Code != "FATAL_PROCESSING_ERROR" || strings.Contains(sink.Errors[0].Message, "secret") {
				t.Fatalf("fatal sink = %#v", sink.Errors)
			}
		})
	}
}

func TestRunnerFinalFlushFailureReportsFailedValidBatchNotLaterRejection(t *testing.T) {
	path := writeInput(t, "001|20260811|bca|1\n002|20260811|bri|invalid\n")
	cfg := delimitedJobConfig(path, config.ErrorModePartial, 10)
	repo := &fakeRepository{insertErr: errors.New("driver detail")}
	sink := &MemoryErrorSink{}
	runner, err := NewRunner(cfg, repo, sink)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background()); err == nil {
		t.Fatal("expected final flush failure")
	}
	if len(sink.Errors) != 2 || sink.Errors[0].RecordNumber != 2 || sink.Errors[0].Code != "TYPE_CONVERSION" ||
		sink.Errors[1].RecordNumber != 1 || sink.Errors[1].Code != "FATAL_PROCESSING_ERROR" {
		t.Fatalf("error sink = %#v", sink.Errors)
	}
}

func TestRunnerCanceledBeforeFileDoesNotBeginTransaction(t *testing.T) {
	path := writeInput(t, "001|20260811|bca|150000\n")
	cfg := delimitedJobConfig(path, config.ErrorModeStrict, 1)
	repo := &fakeRepository{}
	runner, err := NewRunner(cfg, repo, &MemoryErrorSink{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	summary, err := runner.Run(ctx)
	if !errors.Is(err, context.Canceled) || summary.Status != "FAILED" {
		t.Fatalf("summary/error = %#v, %v", summary, err)
	}
	if len(repo.transactions) != 0 {
		t.Fatalf("canceled run began %d transactions", len(repo.transactions))
	}
}

func TestSamePipelineProcessesEveryConfiguredFormat(t *testing.T) {
	baseColumns := []config.ColumnSpec{
		{Name: "id", Type: config.TypeString},
		{Name: "date", Type: config.TypeDate},
		{Name: "bank", Type: config.TypeString},
		{Name: "amount", Type: config.TypeDecimal},
	}
	baseMappings := []config.FieldMapping{{Source: "0", Target: "id"}, {Source: "1", Target: "date"}, {Source: "2", Target: "bank"}, {Source: "3", Target: "amount"}}
	tests := []struct {
		name    string
		content string
		mutate  func(*config.ParserConfig)
	}{
		{name: "delimited", content: "001|20260811|bca|150000\n", mutate: func(p *config.ParserConfig) {
			p.FileType, p.Delimiter, p.Mappings = config.FileTypeDelimited, "|", baseMappings
		}},
		{name: "csv-header", content: "bank,amount,id,date\nbca,150000,001,20260811\n", mutate: func(p *config.ParserConfig) {
			p.FileType, p.Delimiter, p.HasHeader = config.FileTypeCSV, ",", true
			p.Mappings = []config.FieldMapping{{Source: "id", Target: "id"}, {Source: "date", Target: "date"}, {Source: "bank", Target: "bank"}, {Source: "amount", Target: "amount"}}
		}},
		{name: "tsv", content: "001\t20260811\tbca\t150000\n", mutate: func(p *config.ParserConfig) {
			p.FileType, p.Delimiter, p.Mappings = config.FileTypeTSV, "\t", baseMappings
		}},
		{name: "fixed-width", content: "00120260811bca150000\n", mutate: func(p *config.ParserConfig) {
			p.FileType, p.FixedWidthUnit = config.FileTypeFixedWidth, "BYTE"
			p.FixedWidthFields = []config.FixedWidthField{{Name: "id", Start: 0, Length: 3, Type: config.TypeString}, {Name: "date", Start: 3, Length: 8, Type: config.TypeDate}, {Name: "bank", Start: 11, Length: 3, Type: config.TypeString}, {Name: "amount", Start: 14, Length: 6, Type: config.TypeDecimal}}
			p.Mappings = []config.FieldMapping{{Source: "id", Target: "id"}, {Source: "date", Target: "date"}, {Source: "bank", Target: "bank"}, {Source: "amount", Target: "amount"}}
		}},
		{name: "json", content: `{"transaction":{"id":"001","date":"20260811","bank":"bca","amount":150000}}`, mutate: func(p *config.ParserConfig) {
			p.FileType, p.JSONMode = config.FileTypeJSON, config.JSONModeSingle
			p.Mappings = []config.FieldMapping{{Source: "transaction.id", Target: "id"}, {Source: "transaction.date", Target: "date"}, {Source: "transaction.bank", Target: "bank"}, {Source: "transaction.amount", Target: "amount"}}
		}},
		{name: "xml", content: `<transactions><transaction><id>001</id><date>20260811</date><bank>bca</bank><amount>150000</amount></transaction></transactions>`, mutate: func(p *config.ParserConfig) {
			p.FileType, p.XMLRecordPath = config.FileTypeXML, "/transactions/transaction"
			p.Mappings = []config.FieldMapping{{Source: "id", Target: "id"}, {Source: "date", Target: "date"}, {Source: "bank", Target: "bank"}, {Source: "amount", Target: "amount"}}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeInput(t, test.content)
			cfg := baseJobConfig(path, baseColumns, config.ErrorModeStrict, 10)
			test.mutate(&cfg.Parser)
			repo := &fakeRepository{}
			runner, err := NewRunner(cfg, repo, &MemoryErrorSink{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runner.Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			records := repo.committedRecords()
			if len(records) != 1 {
				t.Fatalf("got %d records", len(records))
			}
			fields := records[0].Fields
			if fields["id"] != "001" || fields["bank"] != "BCA" {
				t.Fatalf("unexpected canonical record: %#v", fields)
			}
			if date, ok := fields["date"].(model.Date); !ok || date.Format("2006-01-02") != "2026-08-11" {
				t.Fatalf("unexpected date: %T(%v)", fields["date"], fields["date"])
			}
			if amount, ok := fields["amount"].(json.Number); !ok || amount.String() != "150000" {
				t.Fatalf("unexpected amount: %T(%v)", fields["amount"], fields["amount"])
			}
		})
	}
}

func TestRawFormatEndToEnd(t *testing.T) {
	path := writeInput(t, "alpha\nbeta\n")
	columns := []config.ColumnSpec{{Name: "payload", Type: config.TypeString}}
	cfg := baseJobConfig(path, columns, config.ErrorModeStrict, 10)
	cfg.Parser.FileType = config.FileTypeRaw
	cfg.Parser.Mappings = []config.FieldMapping{{Source: "raw", Target: "payload"}}
	cfg.Parser.UppercaseFields = nil
	repo := &fakeRepository{}
	runner, err := NewRunner(cfg, repo, &MemoryErrorSink{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	records := repo.committedRecords()
	if len(records) != 2 || records[0].Fields["payload"] != "alpha" || records[1].Fields["payload"] != "beta" {
		t.Fatalf("unexpected RAW output: %#v", records)
	}
}

func TestAttachedJSONExampleWorksWithMappingOnlyENV(t *testing.T) {
	path := writeInput(t, `{"transaction":{"id":"TRX001","bank":"BCA","amount":150000}}`)
	values := map[string]string{
		"INPUT_PATH":          path,
		"FILE_PATTERN":        "*",
		"PARSER_FILE_TYPE":    "JSON",
		"PARSER_JSON_MAPPING": "transaction.id:transaction_id,transaction.bank:bank,transaction.amount:amount",
		"DB_DRIVER":           "postgres",
		"DB_DSN":              "test-only",
		"DB_TABLE":            "transactions",
		"DB_COLUMNS":          "transaction_id,bank,amount",
		"DB_BATCH_SIZE":       "2",
		"ERROR_MODE":          "STRICT",
	}
	cfg, err := config.Load(func(key string) (string, bool) {
		value, exists := values[key]
		return value, exists
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range cfg.Parser.Columns {
		if column.Type != config.TypeAny {
			t.Fatalf("inferred type for %q = %q, want pass-through any", column.Name, column.Type)
		}
	}
	repo := &fakeRepository{}
	runner, err := NewRunner(cfg, repo, &MemoryErrorSink{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	records := repo.committedRecords()
	if len(records) != 1 {
		t.Fatalf("got %d records", len(records))
	}
	fields := records[0].Fields
	if fields["transaction_id"] != "TRX001" || fields["bank"] != "BCA" {
		t.Fatalf("unexpected JSON mapping: %#v", fields)
	}
	amount, ok := fields["amount"].(json.Number)
	if !ok || amount.String() != "150000" {
		t.Fatalf("JSON number was not preserved: %T(%v)", fields["amount"], fields["amount"])
	}
}

func TestExcludeErrorOutputPreventsSelfIngestion(t *testing.T) {
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "input.jsonl")
	errorPath := filepath.Join(dir, "errors.jsonl")
	files, err := excludeErrorOutput([]string{inputPath, errorPath}, errorPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(files, []string{inputPath}) {
		t.Fatalf("filtered files = %#v", files)
	}
	if _, err := excludeErrorOutput([]string{errorPath}, errorPath); err == nil {
		t.Fatal("expected error when only discovered file is the error sink")
	}
}

func TestDiscoverInputsFailsBeforeRuntimeForMissingPath(t *testing.T) {
	cfg := baseJobConfig(filepath.Join(t.TempDir(), "missing"), []config.ColumnSpec{{Name: "value", Type: config.TypeString}}, config.ErrorModeStrict, 1)
	if _, err := DiscoverInputs(cfg); err == nil || !strings.Contains(err.Error(), "INPUT_PATH") {
		t.Fatalf("DiscoverInputs() error = %v", err)
	}
}

func TestExcludeErrorOutputDetectsHardLinkAlias(t *testing.T) {
	dir := t.TempDir()
	errorPath := filepath.Join(dir, "errors.jsonl")
	aliasPath := filepath.Join(dir, "input-alias.jsonl")
	realInput := filepath.Join(dir, "input.jsonl")
	if err := os.WriteFile(errorPath, []byte("old error\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(errorPath, aliasPath); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if err := os.WriteFile(realInput, []byte("record\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	files, err := excludeErrorOutput([]string{aliasPath, realInput}, errorPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(files, []string{realInput}) {
		t.Fatalf("filtered files = %#v", files)
	}
}

func writeInput(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "input.dat")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func delimitedJobConfig(path string, mode config.ErrorMode, batchSize int) config.JobConfig {
	columns := []config.ColumnSpec{{Name: "id", Type: config.TypeString}, {Name: "date", Type: config.TypeDate}, {Name: "bank", Type: config.TypeString}, {Name: "amount", Type: config.TypeDecimal}}
	cfg := baseJobConfig(path, columns, mode, batchSize)
	cfg.Parser.FileType = config.FileTypeDelimited
	cfg.Parser.Delimiter = "|"
	cfg.Parser.Mappings = []config.FieldMapping{{Source: "0", Target: "id"}, {Source: "1", Target: "date"}, {Source: "2", Target: "bank"}, {Source: "3", Target: "amount"}}
	return cfg
}

func baseJobConfig(path string, columns []config.ColumnSpec, mode config.ErrorMode, batchSize int) config.JobConfig {
	mappings := make([]config.FieldMapping, 0, len(columns))
	dbColumns := make([]string, 0, len(columns))
	for _, column := range columns {
		mappings = append(mappings, config.FieldMapping{Source: column.Name, Target: column.Name})
		dbColumns = append(dbColumns, column.Name)
	}
	return config.JobConfig{
		Input: config.InputConfig{Path: path, FilePattern: "*", SkipEmptyLine: true},
		Parser: config.ParserConfig{
			Columns:         columns,
			DateFormat:      "20060102",
			DateTimeFormat:  time.RFC3339,
			Timezone:        "UTC",
			TrimSpace:       true,
			UppercaseFields: []string{"bank"},
			RequiredFields:  dbColumns,
			MaxRecordBytes:  1024 * 1024,
			MaxFields:       10_000,
			SkipEmptyLine:   true,
			FixedWidthUnit:  "BYTE",
			JSONMode:        config.JSONModeAuto,
		},
		DB: config.DBConfig{
			Driver:         "postgres",
			DSN:            "test-only",
			Table:          "records",
			Columns:        dbColumns,
			ColumnMappings: mappings,
			BatchSize:      batchSize,
			BatchMaxBytes:  64 * 1024 * 1024,
			ConnectTimeout: time.Second,
		},
		Error: config.ErrorConfig{Mode: mode, OutputPath: filepath.Join(filepath.Dir(path), "errors.jsonl")},
	}
}

type fakeRepository struct {
	transactions []*fakeTransaction
	committed    []model.Record
	insertErr    error
	commitErr    error
}

func (r *fakeRepository) BeginFile(_ context.Context, _ string) (repository.FileTransaction, error) {
	tx := &fakeTransaction{parent: r}
	r.transactions = append(r.transactions, tx)
	return tx, nil
}

func (r *fakeRepository) Close() error { return nil }

func (r *fakeRepository) lastTransaction() *fakeTransaction {
	if len(r.transactions) == 0 {
		return nil
	}
	return r.transactions[len(r.transactions)-1]
}

func (r *fakeRepository) committedRecords() []model.Record {
	return cloneRecords(r.committed)
}

type fakeTransaction struct {
	parent     *fakeRepository
	staged     []model.Record
	batchSizes []int
	committed  bool
	rolledBack bool
}

func (t *fakeTransaction) Insert(_ context.Context, records []model.Record) error {
	if t.committed || t.rolledBack {
		return errors.New("transaction is closed")
	}
	t.batchSizes = append(t.batchSizes, len(records))
	if t.parent.insertErr != nil {
		return t.parent.insertErr
	}
	t.staged = append(t.staged, cloneRecords(records)...)
	return nil
}

func (t *fakeTransaction) Commit() error {
	if t.committed || t.rolledBack {
		return errors.New("transaction is closed")
	}
	if t.parent.commitErr != nil {
		return t.parent.commitErr
	}
	t.parent.committed = append(t.parent.committed, cloneRecords(t.staged)...)
	t.committed = true
	return nil
}

func (t *fakeTransaction) Rollback() error {
	if t.committed {
		return errors.New("transaction already committed")
	}
	t.staged = nil
	t.rolledBack = true
	return nil
}

func cloneRecords(records []model.Record) []model.Record {
	result := make([]model.Record, len(records))
	for index, record := range records {
		fields := make(map[string]any, len(record.Fields))
		for key, value := range record.Fields {
			fields[key] = value
		}
		record.Fields = fields
		result[index] = record
	}
	return result
}
