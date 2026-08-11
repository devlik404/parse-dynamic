package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"parser-engine/internal/config"
	"parser-engine/internal/model"
	"parser-engine/internal/repository"
)

func TestRunConfiguredReportsCloseFailureAfterCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input.txt")
	if err := os.WriteFile(path, []byte("payload\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	closeErr := errors.New("close failed")
	repo := &testRepository{closeErr: closeErr}
	sink := &testErrorSink{}
	var output bytes.Buffer

	err := runConfigured(context.Background(), &output, rawTestConfig(path), repo, sink)
	if !errors.Is(err, closeErr) {
		t.Fatalf("runConfigured() error = %v", err)
	}
	var summary struct {
		Status          string `json:"status"`
		InsertedRecords int64  `json:"inserted_records"`
	}
	if decodeErr := json.Unmarshal(output.Bytes(), &summary); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if summary.Status != "COMPLETED_WITH_CLOSE_ERROR" || summary.InsertedRecords != 1 || !repo.tx.committed || !sink.closed {
		t.Fatalf("summary/repository/sink = %#v / %#v / %#v", summary, repo, sink)
	}
}

func rawTestConfig(path string) config.JobConfig {
	return config.JobConfig{
		Input: config.InputConfig{Path: path, FilePattern: "*", SkipEmptyLine: true},
		Parser: config.ParserConfig{
			FileType:       config.FileTypeRaw,
			Columns:        []config.ColumnSpec{{Name: "payload", Type: config.TypeString}},
			Mappings:       []config.FieldMapping{{Source: "raw", Target: "payload"}},
			DateFormat:     "2006-01-02",
			DateTimeFormat: time.RFC3339,
			Timezone:       "UTC",
			FixedWidthUnit: "BYTE",
			JSONMode:       config.JSONModeAuto,
			MaxRecordBytes: 1024,
			MaxFields:      10_000,
			SkipEmptyLine:  true,
			RequiredFields: []string{"payload"},
			DefaultValues:  map[string]string{},
		},
		DB: config.DBConfig{
			Driver:         "postgres",
			DSN:            "test-only",
			Table:          "records",
			Columns:        []string{"payload"},
			ColumnMappings: []config.FieldMapping{{Source: "payload", Target: "payload"}},
			BatchSize:      1,
			BatchMaxBytes:  64 * 1024 * 1024,
			ConnectTimeout: time.Second,
		},
		Error: config.ErrorConfig{Mode: config.ErrorModeStrict},
	}
}

type testRepository struct {
	tx       *testTransaction
	closeErr error
}

func (r *testRepository) BeginFile(context.Context, string) (repository.FileTransaction, error) {
	r.tx = &testTransaction{}
	return r.tx, nil
}

func (r *testRepository) Close() error { return r.closeErr }

type testTransaction struct {
	records    []model.Record
	committed  bool
	rolledBack bool
}

func (t *testTransaction) Insert(_ context.Context, records []model.Record) error {
	t.records = append(t.records, records...)
	return nil
}

func (t *testTransaction) Commit() error {
	t.committed = true
	return nil
}

func (t *testTransaction) Rollback() error {
	t.rolledBack = true
	return nil
}

type testErrorSink struct {
	closed bool
}

func (s *testErrorSink) Write(context.Context, model.RecordError) error { return nil }
func (s *testErrorSink) Close() error {
	s.closed = true
	return nil
}
