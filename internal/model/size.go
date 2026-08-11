package model

import (
	"encoding/json"
	"math"
	"time"
)

// Structured decoders accept nesting up to 1000. Keep a little traversal
// headroom so their valid output is not reclassified by downstream budgeting.
const maxSizeWalkDepth = 1024

// EstimateRecordBytes conservatively estimates memory retained by a canonical
// record. It is intentionally approximate: the runner uses it as a safety
// budget, not as a serialized-size contract.
func EstimateRecordBytes(record Record) int64 {
	size := int64(192 + len(record.File))
	return saturatingSizeAdd(size, estimateMapBytes(record.Fields, 0))
}

// EstimateFieldsPayloadBytes estimates the byte payload of canonical fields,
// excluding Go map/interface bookkeeping. Transformation uses this to keep a
// logical record within PARSER_MAX_RECORD_BYTES after expansion.
func EstimateFieldsPayloadBytes(fields map[string]any) int64 {
	return estimateMapPayloadBytes(fields, 0)
}

// EstimateValuePayloadBytes estimates the content bytes retained by a value.
func EstimateValuePayloadBytes(value any) int64 {
	return estimateValuePayloadBytes(value, 0)
}

func estimateMapBytes(fields map[string]any, depth int) int64 {
	if fields == nil {
		return 0
	}
	if depth > maxSizeWalkDepth {
		return math.MaxInt64
	}
	size := int64(128)
	for key, value := range fields {
		size = saturatingSizeAdd(size, int64(64+len(key)))
		size = saturatingSizeAdd(size, estimateValueBytes(value, depth+1))
	}
	return size
}

func estimateValueBytes(value any, depth int) int64 {
	if value == nil {
		return 16
	}
	if depth > maxSizeWalkDepth {
		return math.MaxInt64
	}
	switch typed := value.(type) {
	case string:
		return saturatingSizeAdd(32, saturatingSizeMultiply(int64(len(typed)), 2))
	case []byte:
		return saturatingSizeAdd(32, saturatingSizeMultiply(int64(len(typed)), 2))
	case json.RawMessage:
		return saturatingSizeAdd(32, saturatingSizeMultiply(int64(len(typed)), 2))
	case json.Number:
		return int64(32 + len(typed.String()))
	case UUID:
		return int64(32 + len(typed))
	case Date, time.Time:
		return 64
	case map[string]any:
		return saturatingSizeAdd(estimateMapBytes(typed, depth+1), saturatingSizeMultiply(estimateMapPayloadBytes(typed, depth+1), 6))
	case []any:
		size := int64(48)
		for _, item := range typed {
			size = saturatingSizeAdd(size, 16)
			size = saturatingSizeAdd(size, estimateValueBytes(item, depth+1))
		}
		return saturatingSizeAdd(size, saturatingSizeMultiply(estimateValuePayloadBytes(typed, depth+1), 6))
	case []string:
		size := int64(48)
		for _, item := range typed {
			size = saturatingSizeAdd(size, int64(32+len(item)))
		}
		return size
	default:
		// Parser-produced scalar values are fixed-size here. Unknown custom
		// values remain bounded by a conservative interface allowance.
		return 64
	}
}

func estimateMapPayloadBytes(fields map[string]any, depth int) int64 {
	if fields == nil {
		return 0
	}
	if depth > maxSizeWalkDepth {
		return math.MaxInt64
	}
	var size int64
	for _, value := range fields {
		size = saturatingSizeAdd(size, estimateValuePayloadBytes(value, depth+1))
	}
	return size
}

func estimateValuePayloadBytes(value any, depth int) int64 {
	if value == nil {
		return 0
	}
	if depth > maxSizeWalkDepth {
		return math.MaxInt64
	}
	switch typed := value.(type) {
	case string:
		return int64(len(typed))
	case []byte:
		return int64(len(typed))
	case json.RawMessage:
		return int64(len(typed))
	case json.Number:
		return int64(len(typed.String()))
	case UUID:
		return int64(len(typed))
	case Date:
		return 10
	case time.Time:
		return int64(len(time.RFC3339Nano))
	case map[string]any:
		return estimateMapPayloadBytes(typed, depth+1)
	case []any:
		var size int64
		for _, item := range typed {
			size = saturatingSizeAdd(size, estimateValuePayloadBytes(item, depth+1))
		}
		return size
	case []string:
		var size int64
		for _, item := range typed {
			size = saturatingSizeAdd(size, int64(len(item)))
		}
		return size
	default:
		return 32
	}
}

func saturatingSizeAdd(left, right int64) int64 {
	if left < 0 || right < 0 || left > math.MaxInt64-right {
		return math.MaxInt64
	}
	return left + right
}

func saturatingSizeMultiply(left, right int64) int64 {
	if left < 0 || right < 0 || (left != 0 && right > math.MaxInt64/left) {
		return math.MaxInt64
	}
	return left * right
}
