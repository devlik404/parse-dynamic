package service

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"parser-engine/internal/model"
)

func TestJSONErrorSinkWritesStructuredJSONLAndTruncatesRaw(t *testing.T) {
	path := filepath.Join(t.TempDir(), "errors.jsonl")
	sink, err := NewJSONErrorSink(path)
	if err != nil {
		t.Fatal(err)
	}
	want := model.RecordError{File: "input.txt", RecordNumber: 2, Field: "configured_field", RawValue: strings.Repeat("x", 5000), Code: "INVALID_TYPE", Phase: "transformation", Message: "invalid integer"}
	if err := sink.Write(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var got model.RecordError
	if err := json.NewDecoder(bufio.NewReader(file)).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Code != want.Code || got.Field != want.Field {
		t.Fatalf("decoded error = %#v", got)
	}
	raw, ok := got.RawValue.(string)
	if !ok || len(raw) >= 5000 || !strings.HasSuffix(raw, "...[truncated]") {
		t.Fatalf("raw value was not safely truncated: %T len=%d", got.RawValue, len(raw))
	}
}
