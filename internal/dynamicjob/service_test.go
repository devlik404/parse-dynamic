package dynamicjob

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"parser-engine/internal/config"
	"parser-engine/internal/miniogateway"
	"parser-engine/internal/model"
	"parser-engine/internal/repository"
)

type fakeSource struct {
	data       []byte
	metadata   []miniogateway.ObjectMetadata
	statCalls  int
	openCalls  int
	lastOffset int64
}

func (s *fakeSource) StatObject(context.Context, string, string) (miniogateway.ObjectMetadata, error) {
	index := s.statCalls
	if index >= len(s.metadata) {
		index = len(s.metadata) - 1
	}
	s.statCalls++
	return s.metadata[index], nil
}

func (s *fakeSource) OpenObject(_ context.Context, _, _ string, offset int64) (io.ReadCloser, error) {
	s.openCalls++
	s.lastOffset = offset
	return io.NopCloser(bytes.NewReader(s.data[offset:])), nil
}

type fakePlanLoader struct{ plan *Plan }

func (l fakePlanLoader) Load(context.Context, Request) (*Plan, error) { return l.plan, nil }

type errorPlanLoader struct{ err error }

func (l errorPlanLoader) Load(context.Context, Request) (*Plan, error) { return nil, l.err }

type fakeRepository struct {
	tx     *fakeTransaction
	closed bool
}

func (r *fakeRepository) BeginFile(context.Context, string) (repository.FileTransaction, error) {
	if r.tx == nil {
		r.tx = &fakeTransaction{}
	}
	return r.tx, nil
}

func (r *fakeRepository) Close() error { r.closed = true; return nil }

type fakeTransaction struct {
	records    []model.Record
	committed  bool
	rolledBack bool
}

func (t *fakeTransaction) Insert(_ context.Context, records []model.Record) error {
	for _, record := range records {
		copyRecord := record
		copyRecord.Fields = make(map[string]any, len(record.Fields))
		for key, value := range record.Fields {
			copyRecord.Fields[key] = value
		}
		t.records = append(t.records, copyRecord)
	}
	return nil
}
func (t *fakeTransaction) Commit() error   { t.committed = true; return nil }
func (t *fakeTransaction) Rollback() error { t.rolledBack = true; return nil }

func rawPlan(repo repository.TransactionalRepository) *Plan {
	return &Plan{
		Parser: config.ParserConfig{
			FileType:   config.FileTypeRaw,
			Columns:    []config.ColumnSpec{{Name: "value", Type: config.TypeString}},
			Mappings:   []config.FieldMapping{{Source: "raw", Target: "value"}},
			DateFormat: "2006-01-02", DateTimeFormat: time.RFC3339, Timezone: "UTC",
			MaxRecordBytes: 1024, MaxDocumentBytes: 1024, MaxFields: 10,
			SkipEmptyLine: true,
		},
		Repository: repo, BatchSize: 1, BatchMaxBytes: 1024,
		IncludeFileName: true, IncludeFileDate: true,
	}
}

func validRequest() Request {
	return Request{
		FinalFileName: "input.txt", FinalMinioPath: "product/202608",
		FileDate: "2026-08-29", LANum: 42, ProductID: "PRODUCT",
		Task: "PARSE_FILE", Activity: "RECON",
	}
}

func TestServiceExecuteStreamsInsertsVerifiesAndCommits(t *testing.T) {
	data := []byte("one\ntwo\n")
	metadata := miniogateway.ObjectMetadata{ETag: "etag-1", VersionID: "version-1", Size: int64(len(data))}
	source := &fakeSource{data: data, metadata: []miniogateway.ObjectMetadata{metadata, metadata}}
	repo := &fakeRepository{tx: &fakeTransaction{}}
	service, err := NewService(source, fakePlanLoader{plan: rawPlan(repo)}, Options{MaxInputBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Execute(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.TotalData != 2 || len(repo.tx.records) != 2 {
		t.Fatalf("result/records = %#v/%d", result, len(repo.tx.records))
	}
	if !repo.tx.committed || repo.tx.rolledBack || !repo.closed {
		t.Fatalf("transaction/repository state = %#v / closed=%t", repo.tx, repo.closed)
	}
	if source.statCalls != 2 || source.openCalls != 1 || source.lastOffset != 0 {
		t.Fatalf("source calls = stat:%d open:%d offset:%d", source.statCalls, source.openCalls, source.lastOffset)
	}
	if repo.tx.records[0].Fields[metadataFileName] != "input.txt" {
		t.Fatalf("filename metadata = %#v", repo.tx.records[0].Fields[metadataFileName])
	}
	date, ok := repo.tx.records[0].Fields[metadataFileDate].(time.Time)
	if !ok || date.Format("2006-01-02") != "2026-08-29" {
		t.Fatalf("file date metadata = %#v", repo.tx.records[0].Fields[metadataFileDate])
	}
}

func TestServiceExecuteRollsBackWhenObjectChanges(t *testing.T) {
	data := []byte("one\n")
	source := &fakeSource{data: data, metadata: []miniogateway.ObjectMetadata{
		{ETag: "etag-1", Size: int64(len(data))},
		{ETag: "etag-2", Size: int64(len(data))},
	}}
	repo := &fakeRepository{tx: &fakeTransaction{}}
	service, _ := NewService(source, fakePlanLoader{plan: rawPlan(repo)}, Options{MaxInputBytes: 1024})
	_, err := service.Execute(context.Background(), validRequest())
	var jobErr *JobError
	if !errors.As(err, &jobErr) || jobErr.Kind != ErrorObjectChanged {
		t.Fatalf("Execute() error = %v, want OBJECT_CHANGED", err)
	}
	if repo.tx.committed || !repo.tx.rolledBack {
		t.Fatalf("transaction state = %#v", repo.tx)
	}
}

func TestServiceExecuteRejectsHEADSizeBeforeGET(t *testing.T) {
	source := &fakeSource{metadata: []miniogateway.ObjectMetadata{{ETag: "etag", Size: 11}}}
	repo := &fakeRepository{tx: &fakeTransaction{}}
	service, _ := NewService(source, fakePlanLoader{plan: rawPlan(repo)}, Options{MaxInputBytes: 10})
	_, err := service.Execute(context.Background(), validRequest())
	var jobErr *JobError
	if !errors.As(err, &jobErr) || jobErr.Kind != ErrorInputTooLarge {
		t.Fatalf("Execute() error = %v, want INPUT_TOO_LARGE", err)
	}
	if source.openCalls != 0 || repo.tx.committed || repo.tx.rolledBack {
		t.Fatalf("unexpected open/transaction: open=%d tx=%#v", source.openCalls, repo.tx)
	}
}

func TestServiceExecutePreservesDatabasePlanLoadFailure(t *testing.T) {
	source := &fakeSource{}
	service, _ := NewService(source, errorPlanLoader{err: &JobError{
		Kind: ErrorDatabase, Message: "unable to load dynamic parser database configuration",
	}}, Options{MaxInputBytes: 10})
	_, err := service.Execute(context.Background(), validRequest())
	var jobErr *JobError
	if !errors.As(err, &jobErr) || jobErr.Kind != ErrorDatabase {
		t.Fatalf("Execute() error = %v, want DATABASE_ERROR", err)
	}
	if source.statCalls != 0 || source.openCalls != 0 {
		t.Fatalf("source accessed after plan failure: stat=%d open=%d", source.statCalls, source.openCalls)
	}
}

func TestCompilePlanCombinesReaderAndExistingTargetMappings(t *testing.T) {
	profile := readerProfile{
		ID: 7, FileType: "DELIMITED", Delimiter: sql.NullString{String: "|", Valid: true},
		FixedWidthUnit: "BYTE", JSONMode: "AUTO", DateFormat: "2006-01-02",
		DateTimeFormat: time.RFC3339, Timezone: "UTC", SkipEmptyLine: true,
		MaxRecordBytes: 1024, MaxDocumentBytes: 2048, MaxFields: 10,
		SpreadsheetSheet: "0", DuplicateHeaderPolicy: "ERROR",
	}
	columns := []readerColumn{
		{Col: 1, SourceName: "id", DataType: "string", NormalizeCase: "NONE"},
		{Col: 2, SourceName: "amount", DataType: "decimal", NormalizeCase: "NONE"},
	}
	mappings := []targetMapping{{Col: 1, Target: "TransactionID"}, {Col: 2, Target: "Amount"}}
	legacy := legacyProfile{TargetTable: "recon.Transaction", FieldFilename: "FileName", FieldFiledate: "FileDate"}
	connection := targetConnection{Host: "target", Database: "Recon", User: "user", Password: "secret"}
	parserCfg, dbCfg, err := compilePlan(profile, columns, mappings, legacy, connection, normalizeStoreConfig(StoreConfig{}))
	if err != nil {
		t.Fatal(err)
	}
	if err := config.ValidateParser(parserCfg); err != nil {
		t.Fatalf("compiled parser config invalid: %v", err)
	}
	if parserCfg.Mappings[0].Source != "0" || parserCfg.Mappings[1].Source != "1" {
		t.Fatalf("parser mappings = %#v", parserCfg.Mappings)
	}
	if dbCfg.Schema != "recon" || dbCfg.Table != "Transaction" {
		t.Fatalf("target = %s.%s", dbCfg.Schema, dbCfg.Table)
	}
	if len(dbCfg.Columns) != 4 || dbCfg.ColumnMappings[0] != (config.FieldMapping{Source: "id", Target: "TransactionID"}) {
		t.Fatalf("database config = %#v", dbCfg)
	}
}

func TestCompilePlanRejectsTrailingTransformJSON(t *testing.T) {
	profile := readerProfile{
		ID: 7, FileType: "RAW", FixedWidthUnit: "BYTE", JSONMode: "AUTO",
		DateFormat: "2006-01-02", DateTimeFormat: time.RFC3339, Timezone: "UTC",
		MaxRecordBytes: 1024, MaxDocumentBytes: 2048, MaxFields: 10,
		SpreadsheetSheet: "0", DuplicateHeaderPolicy: "ERROR",
	}
	columns := []readerColumn{{
		Col: 1, SourceName: "value", DataType: "string", NormalizeCase: "NONE",
		TransformConfigJSON: sql.NullString{String: `[{"operation":"TRIM"}] {}`, Valid: true},
	}}
	_, _, err := compilePlan(
		profile, columns, []targetMapping{{Col: 1, Target: "Value"}},
		legacyProfile{TargetTable: "Records"}, targetConnection{Database: "Recon"},
		normalizeStoreConfig(StoreConfig{}),
	)
	if err == nil || !strings.Contains(err.Error(), "trailing TransformConfigJSON") {
		t.Fatalf("compilePlan() error = %v", err)
	}
}
