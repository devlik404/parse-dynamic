package sqlrepo

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"parser-engine/internal/model"
)

const maxValueNormalizationDepth = 8

// PostgreSQL NUMERIC stores at most 131072 digits before the decimal point
// and 16383 digits after it. Keeping these limits here also prevents pgx from
// attempting enormous powers of ten for hostile scientific exponents.
const (
	maxPostgresNumericIntegerDigits = 131_072
	maxPostgresNumericScale         = 16_383
	maxPostgresNumericDigits        = maxPostgresNumericIntegerDigits + maxPostgresNumericScale
)

var (
	errInvalidPostgresNumeric  = errors.New("invalid PostgreSQL numeric value")
	errPostgresExponentRange   = errors.New("PostgreSQL numeric exponent is out of range")
	errPostgresNumericCapacity = errors.New("PostgreSQL numeric value exceeds supported precision or scale")
)

func normalizeSQLValue(value any) (any, error) {
	return normalizeSQLValueDepth(value, 0)
}

// PostgreSQL's extended protocol does not encode a Go string directly as a
// NUMERIC parameter. Use pgx's exact Numeric value for json.Number while the
// other drivers safely bind its validated decimal text.
func normalizeSQLValueForDialect(value any, dialect dialectName) (any, error) {
	if number, ok := value.(json.Number); ok && dialect == dialectPostgres {
		return exactPostgresNumeric(number.String())
	}
	if uuid, ok := value.(model.UUID); ok {
		if dialect != dialectPostgres {
			return uuid.String(), nil
		}
		var native pgtype.UUID
		if err := native.Scan(uuid.String()); err != nil {
			return nil, fmt.Errorf("invalid PostgreSQL UUID value: %w", err)
		}
		return native, nil
	}
	return normalizeSQLValue(value)
}

func exactPostgresNumeric(value string) (pgtype.Numeric, error) {
	lexical, err := preflightPostgresNumeric(value)
	if err != nil {
		return pgtype.Numeric{}, err
	}
	if lexical.zero {
		return pgtype.Numeric{Int: new(big.Int), Exp: 0, Valid: true}, nil
	}

	// The lexical preflight above bounds the coefficient to PostgreSQL's total
	// capacity before big.Int sees it. It also lets us discard arbitrarily many
	// insignificant leading zeroes without changing the decimal exponent.
	coefficient := lexical.coefficient(value)
	integer := new(big.Int)
	if _, ok := integer.SetString(coefficient, 10); !ok {
		return pgtype.Numeric{}, errInvalidPostgresNumeric
	}
	if lexical.negative {
		integer.Neg(integer)
	}
	return pgtype.Numeric{Int: integer, Exp: lexical.exponent, Valid: true}, nil
}

// postgresNumericLexical is deliberately only indexes and counts. Keeping the
// preflight allocation-free means a hostile mantissa is rejected before either
// a coefficient copy or big.Int.SetString can scale with attacker input.
type postgresNumericLexical struct {
	mantissaEnd       int
	decimalIndex      int
	firstNonZero      int
	significantDigits int
	exponent          int32
	negative          bool
	zero              bool
}

func preflightPostgresNumeric(value string) (postgresNumericLexical, error) {
	lexical := postgresNumericLexical{decimalIndex: -1, firstNonZero: -1}
	if value == "" {
		return postgresNumericLexical{}, errInvalidPostgresNumeric
	}

	position := 0
	if value[position] == '-' {
		lexical.negative = true
		position++
		if position == len(value) {
			return postgresNumericLexical{}, errInvalidPostgresNumeric
		}
	}
	mantissaStart := position

	// JSON number grammar permits either a single zero or a non-zero digit
	// followed by digits. In particular, leading integer zeroes are invalid.
	switch {
	case value[position] == '0':
		position++
		if position < len(value) && isASCIIDigit(value[position]) {
			return postgresNumericLexical{}, errInvalidPostgresNumeric
		}
	case value[position] >= '1' && value[position] <= '9':
		for position < len(value) && isASCIIDigit(value[position]) {
			position++
		}
	default:
		return postgresNumericLexical{}, errInvalidPostgresNumeric
	}

	fractionDigits := 0
	if position < len(value) && value[position] == '.' {
		lexical.decimalIndex = position
		position++
		fractionStart := position
		for position < len(value) && isASCIIDigit(value[position]) {
			position++
		}
		if position == fractionStart {
			return postgresNumericLexical{}, errInvalidPostgresNumeric
		}
		fractionDigits = position - fractionStart
	}
	lexical.mantissaEnd = position

	sourceExponent := int64(0)
	if position < len(value) && (value[position] == 'e' || value[position] == 'E') {
		position++
		exponentStart := position
		if position < len(value) && (value[position] == '+' || value[position] == '-') {
			position++
		}
		exponentDigits := position
		for position < len(value) && isASCIIDigit(value[position]) {
			position++
		}
		if position == exponentDigits || position != len(value) {
			return postgresNumericLexical{}, errInvalidPostgresNumeric
		}
		parsed, err := strconv.ParseInt(value[exponentStart:position], 10, 64)
		if err != nil {
			return postgresNumericLexical{}, errPostgresExponentRange
		}
		sourceExponent = parsed
	} else if position != len(value) {
		return postgresNumericLexical{}, errInvalidPostgresNumeric
	}

	lexical.firstNonZero, lexical.significantDigits = scanPostgresMantissa(value, mantissaStart, lexical.mantissaEnd, lexical.decimalIndex)
	if lexical.firstNonZero < 0 {
		lexical.zero = true
		return lexical, nil
	}
	if lexical.significantDigits > maxPostgresNumericDigits {
		return postgresNumericLexical{}, errPostgresNumericCapacity
	}

	// fractionDigits is an int derived from len(value), so conversion to int64
	// is safe on every Go architecture. Guard subtraction explicitly because a
	// valid int64 source exponent may otherwise underflow when the fraction is
	// long. No addition involving attacker-controlled counts is needed.
	fractionDigits64 := int64(fractionDigits)
	const minInt64 = -1 << 63
	if sourceExponent < minInt64+fractionDigits64 {
		return postgresNumericLexical{}, errPostgresNumericCapacity
	}
	numericExponent := sourceExponent - fractionDigits64
	if numericExponent < -maxPostgresNumericScale {
		return postgresNumericLexical{}, errPostgresNumericCapacity
	}
	maximumExponent := int64(maxPostgresNumericIntegerDigits - lexical.significantDigits)
	if numericExponent > maximumExponent {
		return postgresNumericLexical{}, errPostgresNumericCapacity
	}
	lexical.exponent = int32(numericExponent)
	return lexical, nil
}

func scanPostgresMantissa(value string, start, end, decimalIndex int) (firstNonZero, significantDigits int) {
	firstNonZero = -1
	for index := start; index < end; index++ {
		if index == decimalIndex {
			continue
		}
		if firstNonZero < 0 {
			if value[index] == '0' {
				continue
			}
			firstNonZero = index
		}
		significantDigits++
	}
	return firstNonZero, significantDigits
}

func (p postgresNumericLexical) coefficient(value string) string {
	if p.decimalIndex < p.firstNonZero {
		return value[p.firstNonZero:p.mantissaEnd]
	}
	coefficient := make([]byte, 0, p.significantDigits)
	coefficient = append(coefficient, value[p.firstNonZero:p.decimalIndex]...)
	coefficient = append(coefficient, value[p.decimalIndex+1:p.mantissaEnd]...)
	return string(coefficient)
}

func isASCIIDigit(value byte) bool {
	return value >= '0' && value <= '9'
}

func normalizeSQLValueDepth(value any, depth int) (any, error) {
	if value == nil {
		return nil, nil
	}
	if depth > maxValueNormalizationDepth {
		return nil, fmt.Errorf("SQL value normalization exceeded maximum depth")
	}

	switch typed := value.(type) {
	case model.Date:
		return typed.Time, nil
	case time.Time:
		return typed, nil
	case json.Number:
		if err := validateJSONNumber(typed.String()); err != nil {
			return nil, err
		}
		// MySQL and SQL Server coerce validated bound text into their configured
		// exact decimal column. PostgreSQL is handled above with pgtype.Numeric.
		return typed.String(), nil
	case json.RawMessage:
		if !json.Valid(typed) {
			return nil, fmt.Errorf("invalid JSON value")
		}
		return string(typed), nil
	case []byte:
		return typed, nil
	}

	if valuer, ok := value.(driver.Valuer); ok {
		normalized, err := valuer.Value()
		if err != nil {
			return nil, fmt.Errorf("resolve SQL driver value: %w", err)
		}
		return normalizeSQLValueDepth(normalized, depth+1)
	}

	reflected := reflect.ValueOf(value)
	dereferenced := false
	for reflected.Kind() == reflect.Pointer || reflected.Kind() == reflect.Interface {
		if reflected.IsNil() {
			return nil, nil
		}
		reflected = reflected.Elem()
		dereferenced = true
	}
	if dereferenced && reflected.CanInterface() {
		return normalizeSQLValueDepth(reflected.Interface(), depth+1)
	}

	switch reflected.Kind() {
	case reflect.Map, reflect.Slice, reflect.Array:
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("encode JSON SQL value: %w", err)
		}
		return string(encoded), nil
	default:
		return value, nil
	}
}

func validateJSONNumber(number string) error {
	if number == "" || strings.TrimSpace(number) != number {
		return fmt.Errorf("invalid JSON number %q", number)
	}
	decoder := json.NewDecoder(strings.NewReader(number))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return fmt.Errorf("invalid JSON number %q", number)
	}
	if _, ok := decoded.(json.Number); !ok {
		return fmt.Errorf("invalid JSON number %q", number)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("invalid JSON number %q", number)
	}
	return nil
}
