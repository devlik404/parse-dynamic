package model

import (
	"encoding/json"
	"math"
	"testing"
)

func TestSizeEstimatesGrowWithCanonicalPayload(t *testing.T) {
	small := Record{File: "x", Fields: map[string]any{"payload": "a"}}
	large := Record{File: "x", Fields: map[string]any{"payload": "aaaaaaaa", "nested": []any{json.Number("12.50")}}}
	if EstimateRecordBytes(large) <= EstimateRecordBytes(small) {
		t.Fatalf("large estimate %d <= small estimate %d", EstimateRecordBytes(large), EstimateRecordBytes(small))
	}
	if EstimateFieldsPayloadBytes(large.Fields) <= EstimateFieldsPayloadBytes(small.Fields) {
		t.Fatal("canonical payload estimate did not grow")
	}
}

func TestSizeEstimateSaturatesOnExcessiveDepth(t *testing.T) {
	var value any = "leaf"
	for index := 0; index < maxSizeWalkDepth+2; index++ {
		value = []any{value}
	}
	if got := EstimateValuePayloadBytes(value); got != math.MaxInt64 {
		t.Fatalf("deep estimate = %d, want saturation", got)
	}
}
