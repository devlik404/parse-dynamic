package parser

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

/*
====================================================
STRING SPLITTER
====================================================
*/

// splitByDelimiter
// ----------------
// Split line berdasarkan delimiter rule.
// Delimiter bisa:
// - "|" "," ";" "\t"
func splitByDelimiter(line, delimiter string) []string {
	// delimiter kosong sudah dicegah di engine
	raw := strings.Split(line, delimiter)

	out := make([]string, 0, len(raw))
	for _, v := range raw {
		out = append(out, strings.TrimSpace(v))
	}
	return out
}

/*
====================================================
VALUE CASTING
====================================================
*/

// castValue
// ---------
// Convert raw string ke tipe data rule
//
// Supported DataType:
// - string
// - integer
// - decimal
// - date
func castValue(raw string, dataType string) (interface{}, error) {
	val := strings.TrimSpace(raw)

	switch strings.ToLower(dataType) {

	case "string":
		return val, nil

	case "integer":
		if val == "" {
			return 0, nil
		}
		i, err := strconv.Atoi(cleanNumber(val))
		if err != nil {
			return nil, err
		}
		return i, nil

	case "decimal":
		if val == "" {
			return float64(0), nil
		}
		f, err := strconv.ParseFloat(cleanNumber(val), 64)
		if err != nil {
			return nil, err
		}
		return f, nil

	case "date":
		// auto-detect common formats
		return parseDate(val)

	default:
		return nil, errors.New("unsupported data type: " + dataType)
	}
}

/*
====================================================
HELPERS
====================================================
*/

// cleanNumber
// -----------
// Remove thousand separator & spaces
// contoh: "-295,000.00" → "-295000.00"
func cleanNumber(s string) string {
	s = strings.ReplaceAll(s, ",", "")
	s = strings.ReplaceAll(s, " ", "")
	return s
}

// parseDate
// ---------
// Auto-detect common date formats
func parseDate(s string) (time.Time, error) {
	formats := []string{
		"02/01/06",   // 24/12/25
		"02/01/2006", // 24/12/2025
		"20060102",   // 20250619
		"2006-01-02",
	}

	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}

	return time.Time{}, errors.New("unsupported date format: " + s)
}
