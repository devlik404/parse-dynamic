package valueobject

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

/*
DataType
--------
Value object untuk casting & validasi tipe data
Dipakai oleh parser engine
*/

type DataType string

const (
	String  DataType = "string"
	Integer DataType = "integer"
	Decimal DataType = "decimal"
	Date    DataType = "date"
)

// Cast mengubah string mentah → tipe data sesuai DataType
func (d DataType) Cast(value string) (interface{}, error) {
	val := strings.TrimSpace(value)

	switch d {
	case String:
		return val, nil

	case Integer:
		return strconv.Atoi(val)

	case Decimal:
		// handle format: -295,000.00
		clean := strings.ReplaceAll(val, ",", "")
		return strconv.ParseFloat(clean, 64)

	case Date:
		// default yyyyMMdd
		layouts := []string{
			"20060102",
			"02/01/06",
			"02/01/2006",
			time.RFC3339,
		}

		for _, layout := range layouts {
			if t, err := time.Parse(layout, val); err == nil {
				return t, nil
			}
		}
		return nil, errors.New("invalid date format")

	default:
		return nil, errors.New("unsupported data type")
	}
}
