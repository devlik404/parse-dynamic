package sqlrepo

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"parser-engine/internal/model"
)

func TestNormalizeSQLValue(t *testing.T) {
	timestamp := time.Date(2026, time.August, 11, 10, 30, 0, 123, time.UTC)
	date := model.NewDate(timestamp)
	text := "hello"
	tests := []struct {
		name  string
		value any
		want  any
	}{
		{name: "nil", value: nil, want: nil},
		{name: "nil pointer", value: (*string)(nil), want: nil},
		{name: "pointer", value: &text, want: "hello"},
		{name: "date", value: date, want: date.Time},
		{name: "timestamp", value: timestamp, want: timestamp},
		{name: "exact JSON number", value: json.Number("1234567890.123456789"), want: "1234567890.123456789"},
		{name: "raw JSON", value: json.RawMessage(`{"b":2,"a":1}`), want: `{"b":2,"a":1}`},
		{name: "JSON object", value: map[string]any{"b": 2, "a": true}, want: `{"a":true,"b":2}`},
		{name: "JSON array", value: []string{"a", "b"}, want: `["a","b"]`},
		{name: "binary", value: []byte{0, 1, 2}, want: []byte{0, 1, 2}},
		{name: "driver valuer", value: testValuer{value: "value"}, want: "value"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeSQLValue(test.value)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("normalizeSQLValue() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestNormalizePostgresDecimalUsesExactPGXNumeric(t *testing.T) {
	got, err := normalizeSQLValueForDialect(json.Number("1234567890.123456789"), dialectPostgres)
	if err != nil {
		t.Fatal(err)
	}
	numeric, ok := got.(pgtype.Numeric)
	if !ok {
		t.Fatalf("normalized decimal = %T, want pgtype.Numeric", got)
	}
	encoded, err := json.Marshal(numeric)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != "1234567890.123456789" {
		t.Fatalf("exact numeric JSON = %s", encoded)
	}
	scientific, err := normalizeSQLValueForDialect(json.Number("1.234567890123456789e2"), dialectPostgres)
	if err != nil {
		t.Fatal(err)
	}
	scientificJSON, err := json.Marshal(scientific.(pgtype.Numeric))
	if err != nil || string(scientificJSON) != "123.4567890123456789" {
		t.Fatalf("exact scientific numeric = %s, %v", scientificJSON, err)
	}
	codecMap := pgtype.NewMap()
	plan := codecMap.PlanEncode(pgtype.NumericOID, pgtype.BinaryFormatCode, numeric)
	if plan == nil {
		t.Fatal("pgx has no binary NUMERIC encode plan")
	}
	if _, err := plan.Encode(numeric, nil); err != nil {
		t.Fatalf("pgx NUMERIC encode: %v", err)
	}

	other, err := normalizeSQLValueForDialect(json.Number("123.45"), dialectMySQL)
	if err != nil || other != "123.45" {
		t.Fatalf("MySQL decimal = %#v, %v", other, err)
	}
}

func TestNormalizePostgresDecimalRejectsHostileExponents(t *testing.T) {
	tests := []string{
		"1e-2147483648",
		"1e2147483647",
		"1e-16384",
		"1e131072",
	}
	for _, value := range tests {
		t.Run(value, func(t *testing.T) {
			if _, err := normalizeSQLValueForDialect(json.Number(value), dialectPostgres); err == nil || !strings.Contains(err.Error(), "precision or scale") {
				t.Fatalf("normalize PostgreSQL NUMERIC %q error = %v", value, err)
			}
		})
	}

	for _, value := range []string{"0e-2147483648", "1e-16383", "1e131071"} {
		t.Run("boundary_"+value, func(t *testing.T) {
			normalized, err := normalizeSQLValueForDialect(json.Number(value), dialectPostgres)
			if err != nil {
				t.Fatal(err)
			}
			numeric := normalized.(pgtype.Numeric)
			codecMap := pgtype.NewMap()
			plan := codecMap.PlanEncode(pgtype.NumericOID, pgtype.BinaryFormatCode, numeric)
			if plan == nil {
				t.Fatal("pgx has no binary NUMERIC encode plan")
			}
			if _, err := plan.Encode(numeric, nil); err != nil {
				t.Fatalf("pgx NUMERIC encode: %v", err)
			}
		})
	}
}

func TestExactPostgresNumericRejectsHostileMantissaBeforeBigInt(t *testing.T) {
	// This exceeds PostgreSQL's combined integer/scale capacity. Calling the
	// lexical helper directly documents and enforces that rejection happens in
	// the allocation-free phase, before coefficient construction or SetString.
	hostileInteger := strings.Repeat("9", 200_000)
	if _, err := preflightPostgresNumeric(hostileInteger); !errors.Is(err, errPostgresNumericCapacity) {
		t.Fatalf("preflight oversized integer error = %v", err)
	}

	// A small significant coefficient hidden behind a huge fractional zero run
	// must also be rejected on scale unless its scientific exponent shifts those
	// irrelevant leading zeroes away.
	zeroRun := strings.Repeat("0", 200_000)
	hostileFraction := "0." + zeroRun + "1"
	if _, err := preflightPostgresNumeric(hostileFraction); !errors.Is(err, errPostgresNumericCapacity) {
		t.Fatalf("preflight oversized scale error = %v", err)
	}

	var gotErr error
	allocations := testing.AllocsPerRun(5, func() {
		_, gotErr = exactPostgresNumeric(hostileInteger)
	})
	if !errors.Is(gotErr, errPostgresNumericCapacity) {
		t.Fatalf("exact oversized integer error = %v", gotErr)
	}
	if allocations > 1 {
		t.Fatalf("hostile rejection allocated %.1f objects per call; big.Int preflight likely regressed", allocations)
	}
}

func TestExactPostgresNumericTrimsIrrelevantLeadingZerosSafely(t *testing.T) {
	zeroRun := strings.Repeat("0", 200_000)
	shifted := "0." + zeroRun + "123e200003"
	lexical, err := preflightPostgresNumeric(shifted)
	if err != nil {
		t.Fatal(err)
	}
	if lexical.zero || lexical.exponent != 0 || lexical.coefficient(shifted) != "123" {
		t.Fatalf("unexpected shifted lexical form: %#v coefficient=%q", lexical, lexical.coefficient(shifted))
	}
	numeric, err := exactPostgresNumeric(shifted)
	if err != nil {
		t.Fatal(err)
	}
	if numeric.Int == nil || numeric.Int.String() != "123" || numeric.Exp != 0 {
		t.Fatalf("shifted numeric = %#v", numeric)
	}

	allZero := "0." + zeroRun
	lexical, err = preflightPostgresNumeric(allZero)
	if err != nil || !lexical.zero {
		t.Fatalf("all-zero preflight = %#v, %v", lexical, err)
	}
	numeric, err = exactPostgresNumeric(allZero)
	if err != nil || numeric.Int == nil || numeric.Int.Sign() != 0 || numeric.Exp != 0 {
		t.Fatalf("all-zero numeric = %#v, %v", numeric, err)
	}
}

func TestPostgresNumericLexicalCapacityBoundaries(t *testing.T) {
	integerBoundary := strings.Repeat("9", maxPostgresNumericIntegerDigits)
	if _, err := preflightPostgresNumeric(integerBoundary); err != nil {
		t.Fatalf("integer boundary rejected: %v", err)
	}
	if _, err := preflightPostgresNumeric(integerBoundary + "9"); !errors.Is(err, errPostgresNumericCapacity) {
		t.Fatalf("integer boundary+1 error = %v", err)
	}

	combinedBoundary := strings.Repeat("9", maxPostgresNumericDigits) + "e-16383"
	if _, err := preflightPostgresNumeric(combinedBoundary); err != nil {
		t.Fatalf("combined precision/scale boundary rejected: %v", err)
	}
	if _, err := preflightPostgresNumeric("9" + combinedBoundary); !errors.Is(err, errPostgresNumericCapacity) {
		t.Fatalf("combined boundary+1 error = %v", err)
	}
}

func TestPostgresNumericPreflightValidatesJSONNumberGrammar(t *testing.T) {
	for _, value := range []string{"", "+1", "01", "-01", ".1", "1.", "1e", "1e+", "NaN", " 1", "1 "} {
		t.Run(value, func(t *testing.T) {
			if _, err := preflightPostgresNumeric(value); !errors.Is(err, errInvalidPostgresNumeric) {
				t.Fatalf("preflight %q error = %v", value, err)
			}
		})
	}
}

func TestNormalizePostgresUUIDUsesNativePGXUUID(t *testing.T) {
	const text = "550e8400-e29b-41d4-a716-446655440000"
	got, err := normalizeSQLValueForDialect(model.UUID(text), dialectPostgres)
	if err != nil {
		t.Fatal(err)
	}
	native, ok := got.(pgtype.UUID)
	if !ok || !native.Valid {
		t.Fatalf("normalized UUID = %T(%v), want valid pgtype.UUID", got, got)
	}
	codecMap := pgtype.NewMap()
	plan := codecMap.PlanEncode(pgtype.UUIDOID, pgtype.BinaryFormatCode, native)
	if plan == nil {
		t.Fatal("pgx has no binary UUID encode plan")
	}
	if _, err := plan.Encode(native, nil); err != nil {
		t.Fatalf("pgx UUID encode: %v", err)
	}
	encoded, err := json.Marshal(native)
	if err != nil || string(encoded) != `"550e8400-e29b-41d4-a716-446655440000"` {
		t.Fatalf("UUID JSON = %s, %v", encoded, err)
	}
	other, err := normalizeSQLValueForDialect(model.UUID(text), dialectMySQL)
	if err != nil || other != text {
		t.Fatalf("MySQL UUID = %#v, %v", other, err)
	}
}

func TestNormalizePostgresJSONScalarsUseValidJSONText(t *testing.T) {
	codecMap := pgtype.NewMap()
	for _, raw := range []json.RawMessage{json.RawMessage(`"hello"`), json.RawMessage(`true`), json.RawMessage(`null`), json.RawMessage(`{"ok":1}`)} {
		t.Run(string(raw), func(t *testing.T) {
			normalized, err := normalizeSQLValueForDialect(raw, dialectPostgres)
			if err != nil {
				t.Fatal(err)
			}
			text, ok := normalized.(string)
			if !ok || text != string(raw) {
				t.Fatalf("normalized JSON = %T(%v)", normalized, normalized)
			}
			plan := codecMap.PlanEncode(pgtype.JSONBOID, pgtype.TextFormatCode, normalized)
			if plan == nil {
				t.Fatal("pgx has no JSONB text encode plan")
			}
			encoded, err := plan.Encode(normalized, nil)
			if err != nil || string(encoded) != string(raw) {
				t.Fatalf("pgx JSONB encode = %s, %v", encoded, err)
			}
		})
	}
}

func TestNormalizeSQLValueRejectsInvalidValues(t *testing.T) {
	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{name: "invalid number", value: json.Number("NaN"), want: "invalid JSON number"},
		{name: "invalid raw JSON", value: json.RawMessage(`{"missing":`), want: "invalid JSON value"},
		{name: "cyclic JSON object", value: cyclic, want: "encode JSON SQL value"},
		{name: "valuer error", value: testValuer{err: errors.New("broken")}, want: "resolve SQL driver value"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizeSQLValue(test.value)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

type testValuer struct {
	value driver.Value
	err   error
}

func (v testValuer) Value() (driver.Value, error) {
	return v.value, v.err
}
