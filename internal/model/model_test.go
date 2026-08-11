package model

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDatePreservesDateOnlyJSONAndSQLTime(t *testing.T) {
	date := NewDate(time.Date(2026, time.August, 11, 18, 30, 0, 0, time.FixedZone("WIB", 7*60*60)))
	encoded, err := json.Marshal(date)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `"2026-08-11"` {
		t.Fatalf("MarshalJSON() = %s", encoded)
	}
	value, err := date.Value()
	if err != nil {
		t.Fatal(err)
	}
	got := value.(time.Time)
	if got.Hour() != 0 || got.Day() != 11 || got.Location().String() != "WIB" {
		t.Fatalf("Value() = %v", got)
	}
}

func TestRecordErrorIncludesStablePositionAndCode(t *testing.T) {
	err := (&RecordError{RecordNumber: 4, LineNumber: 7, Field: "configured", Code: "INVALID_TYPE", Message: "bad integer"}).Error()
	want := "record 4 line 7 field configured: INVALID_TYPE: bad integer"
	if err != want {
		t.Fatalf("Error() = %q, want %q", err, want)
	}
}
