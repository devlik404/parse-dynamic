package transformation

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxJSONSizeDepth = 1024

func boundedReplace(value, from, to string, maximum int64) (string, bool) {
	if from == "" || int64(len(value)) > maximum {
		return "", false
	}
	count := int64(strings.Count(value, from))
	resultSize := int64(len(value))
	delta := int64(len(to) - len(from))
	if delta > 0 {
		if count > 0 && (resultSize > maximum || delta > (maximum-resultSize)/count) {
			return "", false
		}
		resultSize += count * delta
	} else {
		resultSize += count * delta
	}
	if resultSize < 0 || resultSize > maximum {
		return "", false
	}
	return strings.ReplaceAll(value, from, to), true
}

func boundedCase(value string, upper bool, maximum int64) (string, bool) {
	var size int64
	for _, current := range value {
		mapped := unicode.ToLower(current)
		if upper {
			mapped = unicode.ToUpper(current)
		}
		width := utf8.RuneLen(mapped)
		if width < 0 || size > maximum-int64(width) {
			return "", false
		}
		size += int64(width)
	}
	if size > maximum {
		return "", false
	}
	mapper := unicode.ToLower
	if upper {
		mapper = unicode.ToUpper
	}
	return strings.Map(mapper, value), true
}

func boundedSubstring(value string, start, length int) (string, bool) {
	if start < 0 || length <= 0 {
		return "", false
	}
	runeCount := utf8.RuneCountInString(value)
	if start > runeCount || length > runeCount-start {
		return "", false
	}
	endRune := start + length // overflow-safe after length <= runeCount-start
	startByte, endByte := len(value), len(value)
	runeIndex := 0
	for byteIndex := range value {
		if runeIndex == start {
			startByte = byteIndex
		}
		if runeIndex == endRune {
			endByte = byteIndex
			break
		}
		runeIndex++
	}
	return strings.Clone(value[startByte:endByte]), true
}

// jsonEncodedSize computes the bytes encoding/json would need for the JSON
// values emitted by the parsers. It lets callers reject expansion before
// json.Marshal allocates its output buffer.
func jsonEncodedSize(value any, depth int) (int64, error) {
	if depth > maxJSONSizeDepth {
		return 0, fmt.Errorf("JSON nesting exceeds safe limit")
	}
	switch typed := value.(type) {
	case nil:
		return 4, nil
	case bool:
		if typed {
			return 4, nil
		}
		return 5, nil
	case string:
		return jsonStringEncodedSize(typed), nil
	case json.Number:
		if !json.Valid([]byte(typed.String())) {
			return 0, fmt.Errorf("invalid JSON number")
		}
		return int64(len(typed.String())), nil
	case json.RawMessage:
		if !json.Valid(typed) {
			return 0, fmt.Errorf("invalid JSON value")
		}
		// RawMessage may be HTML-escaped by encoding/json when nested. Six
		// bytes per input byte is a safe bound without materializing it.
		return saturatingMultiply(int64(len(typed)), 6), nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return 0, fmt.Errorf("invalid JSON float")
		}
		return int64(len(strconv.FormatFloat(typed, 'g', -1, 64))), nil
	case float32:
		if math.IsNaN(float64(typed)) || math.IsInf(float64(typed), 0) {
			return 0, fmt.Errorf("invalid JSON float")
		}
		return int64(len(strconv.FormatFloat(float64(typed), 'g', -1, 32))), nil
	case int:
		return int64(len(strconv.Itoa(typed))), nil
	case int8:
		return int64(len(strconv.FormatInt(int64(typed), 10))), nil
	case int16:
		return int64(len(strconv.FormatInt(int64(typed), 10))), nil
	case int32:
		return int64(len(strconv.FormatInt(int64(typed), 10))), nil
	case int64:
		return int64(len(strconv.FormatInt(typed, 10))), nil
	case uint:
		return int64(len(strconv.FormatUint(uint64(typed), 10))), nil
	case uint8:
		return int64(len(strconv.FormatUint(uint64(typed), 10))), nil
	case uint16:
		return int64(len(strconv.FormatUint(uint64(typed), 10))), nil
	case uint32:
		return int64(len(strconv.FormatUint(uint64(typed), 10))), nil
	case uint64:
		return int64(len(strconv.FormatUint(typed, 10))), nil
	case map[string]any:
		size := int64(2)
		entry := 0
		for key, item := range typed {
			if entry > 0 {
				size = saturatingAdd(size, 1)
			}
			size = saturatingAdd(size, jsonStringEncodedSize(key))
			size = saturatingAdd(size, 1)
			itemSize, err := jsonEncodedSize(item, depth+1)
			if err != nil {
				return 0, err
			}
			size = saturatingAdd(size, itemSize)
			entry++
		}
		return size, nil
	case []any:
		size := int64(2)
		for index, item := range typed {
			if index > 0 {
				size = saturatingAdd(size, 1)
			}
			itemSize, err := jsonEncodedSize(item, depth+1)
			if err != nil {
				return 0, err
			}
			size = saturatingAdd(size, itemSize)
		}
		return size, nil
	default:
		return 0, fmt.Errorf("unsupported JSON value type %T", value)
	}
}

func jsonStringEncodedSize(value string) int64 {
	size := int64(2) // surrounding quotes
	for index := 0; index < len(value); {
		current := value[index]
		if current < utf8.RuneSelf {
			switch current {
			case '\\', '"', '\b', '\f', '\n', '\r', '\t':
				size = saturatingAdd(size, 2)
			case '<', '>', '&':
				size = saturatingAdd(size, 6)
			default:
				if current < 0x20 {
					size = saturatingAdd(size, 6)
				} else {
					size = saturatingAdd(size, 1)
				}
			}
			index++
			continue
		}
		decoded, width := utf8.DecodeRuneInString(value[index:])
		if decoded == utf8.RuneError && width == 1 {
			size = saturatingAdd(size, 6)
			index++
			continue
		}
		if decoded == '\u2028' || decoded == '\u2029' {
			size = saturatingAdd(size, 6)
		} else {
			size = saturatingAdd(size, int64(width))
		}
		index += width
	}
	return size
}

func saturatingAdd(left, right int64) int64 {
	if left < 0 || right < 0 || left > math.MaxInt64-right {
		return math.MaxInt64
	}
	return left + right
}

func saturatingMultiply(left, right int64) int64 {
	if left < 0 || right < 0 || (left != 0 && right > math.MaxInt64/left) {
		return math.MaxInt64
	}
	return left * right
}
