package model

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

type SourceRecord struct {
	File         string
	RecordNumber int64
	LineNumber   int64
	Values       map[string]any
	Raw          any
	Error        *RecordError
}

type Record struct {
	File         string         `json:"file"`
	RecordNumber int64          `json:"record_number"`
	LineNumber   int64          `json:"line_number,omitempty"`
	Fields       map[string]any `json:"fields"`
}

type RecordError struct {
	File         string `json:"file"`
	RecordNumber int64  `json:"record_number"`
	LineNumber   int64  `json:"line_number,omitempty"`
	Field        string `json:"field,omitempty"`
	RawValue     any    `json:"raw_value"`
	Code         string `json:"code"`
	Phase        string `json:"phase"`
	Message      string `json:"message"`
}

// UUID distinguishes validated UUID fields from ordinary strings so SQL
// adapters can bind them using a dialect-native exact type.
type UUID string

func (u UUID) String() string { return string(u) }

func (e *RecordError) Error() string {
	if e == nil {
		return ""
	}
	position := fmt.Sprintf("record %d", e.RecordNumber)
	if e.LineNumber > 0 {
		position += fmt.Sprintf(" line %d", e.LineNumber)
	}
	if e.Field != "" {
		position += " field " + e.Field
	}
	return fmt.Sprintf("%s: %s: %s", position, e.Code, e.Message)
}

// Date keeps date-only semantics in JSON while remaining a native SQL value.
type Date struct {
	time.Time
}

func NewDate(value time.Time) Date {
	year, month, day := value.Date()
	return Date{Time: time.Date(year, month, day, 0, 0, 0, 0, value.Location())}
}

func (d Date) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.Format("2006-01-02"))
}

func (d Date) Value() (driver.Value, error) {
	return d.Time, nil
}
