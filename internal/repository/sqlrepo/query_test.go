package sqlrepo

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"parser-engine/internal/config"
	"parser-engine/internal/model"
)

func TestInsertBuilderUsesStableDBColumnOrderAndCanonicalMapping(t *testing.T) {
	d, err := resolveDialect("postgres")
	if err != nil {
		t.Fatal(err)
	}
	builder, err := newInsertBuilder(config.DBConfig{
		Schema:  "ingest",
		Table:   "orders",
		Columns: []string{"external_id", "order_date", "payload", "note"},
		ColumnMappings: []config.FieldMapping{
			{Source: "id", Target: "external_id"},
			{Source: "date", Target: "order_date"},
		},
	}, d)
	if err != nil {
		t.Fatal(err)
	}

	date := model.NewDate(time.Date(2026, time.August, 11, 19, 30, 0, 0, time.FixedZone("WIB", 7*60*60)))
	records := []model.Record{
		{
			RecordNumber: 4,
			Fields: map[string]any{
				"payload": map[string]any{"ok": true, "count": json.Number("12.50")},
				"id":      int64(9),
				"note":    nil,
				"date":    date,
			},
		},
		{
			RecordNumber: 5,
			Fields: map[string]any{
				"date":    date,
				"note":    "second",
				"id":      int64(10),
				"payload": []any{"a", 2},
			},
		},
	}

	query, args, err := builder.build(records)
	if err != nil {
		t.Fatal(err)
	}
	wantQuery := `INSERT INTO "ingest"."orders" ("external_id","order_date","payload","note") VALUES ($1,$2,$3,$4),($5,$6,$7,$8)`
	if query != wantQuery {
		t.Fatalf("query\n got: %s\nwant: %s", query, wantQuery)
	}
	wantArgs := []any{
		int64(9), date.Time, `{"count":12.50,"ok":true}`, nil,
		int64(10), date.Time, `["a",2]`, "second",
	}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args\n got: %#v\nwant: %#v", args, wantArgs)
	}
}

func TestInsertBuilderQuotesAndUsesPlaceholdersForEachDialect(t *testing.T) {
	tests := []struct {
		name   string
		driver string
		want   string
	}{
		{
			name:   "postgres",
			driver: "pgx",
			want:   `INSERT INTO "data"."event" ("a","b") VALUES ($1,$2)`,
		},
		{
			name:   "mysql",
			driver: "mysql",
			want:   "INSERT INTO `data`.`event` (`a`,`b`) VALUES (?,?)",
		},
		{
			name:   "sql server",
			driver: "sqlserver",
			want:   "INSERT INTO [data].[event] ([a],[b]) VALUES (@p1,@p2)",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			d, err := resolveDialect(test.driver)
			if err != nil {
				t.Fatal(err)
			}
			builder, err := newInsertBuilder(config.DBConfig{
				Schema: "data", Table: "event", Columns: []string{"a", "b"},
			}, d)
			if err != nil {
				t.Fatal(err)
			}
			query, _, err := builder.build([]model.Record{{RecordNumber: 1, Fields: map[string]any{"a": 1, "b": 2}}})
			if err != nil {
				t.Fatal(err)
			}
			if query != test.want {
				t.Fatalf("query = %q, want %q", query, test.want)
			}
		})
	}
}

func TestInsertBuilderRejectsUnsafeIdentifiersAndInvalidMappings(t *testing.T) {
	d, err := resolveDialect("postgres")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		cfg  config.DBConfig
		want string
	}{
		{
			name: "unsafe table",
			cfg:  config.DBConfig{Table: `events; DROP TABLE events`, Columns: []string{"id"}},
			want: "unsafe SQL identifier",
		},
		{
			name: "unsafe column",
			cfg:  config.DBConfig{Table: "events", Columns: []string{`id) VALUES (1);--`}},
			want: "unsafe SQL identifier",
		},
		{
			name: "mapping target absent",
			cfg: config.DBConfig{
				Table: "events", Columns: []string{"id"},
				ColumnMappings: []config.FieldMapping{{Source: "event_id", Target: "other"}},
			},
			want: "absent from DB_COLUMNS",
		},
		{
			name: "duplicate database column",
			cfg:  config.DBConfig{Table: "events", Columns: []string{"id", "id"}},
			want: "duplicate column",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := newInsertBuilder(test.cfg, d)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestInsertBuilderRejectsMissingCanonicalField(t *testing.T) {
	d, err := resolveDialect("mysql")
	if err != nil {
		t.Fatal(err)
	}
	builder, err := newInsertBuilder(config.DBConfig{
		Table: "events", Columns: []string{"event_id"},
		ColumnMappings: []config.FieldMapping{{Source: "id", Target: "event_id"}},
	}, d)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = builder.build([]model.Record{{RecordNumber: 42, Fields: map[string]any{}}})
	if err == nil || !strings.Contains(err.Error(), `record 42 is missing canonical field "id"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInsertSplitsSQLServerBatchAtParameterLimit(t *testing.T) {
	d, err := resolveDialect("sqlserver")
	if err != nil {
		t.Fatal(err)
	}
	builder, err := newInsertBuilder(config.DBConfig{
		Table: "events", Columns: []string{"a", "b", "c"},
	}, d)
	if err != nil {
		t.Fatal(err)
	}
	repository := &SQLRepository{dialect: d, builder: builder, batchSize: 5_000}
	records := make([]model.Record, 701)
	for index := range records {
		records[index] = model.Record{
			RecordNumber: int64(index + 1),
			Fields:       map[string]any{"a": index, "b": index + 1, "c": index + 2},
		}
	}
	executor := &capturingExecutor{}
	if err := repository.insert(context.Background(), executor, records); err != nil {
		t.Fatal(err)
	}
	if len(executor.calls) != 2 {
		t.Fatalf("ExecContext calls = %d, want 2", len(executor.calls))
	}
	if got := len(executor.calls[0].args); got != 2_100 {
		t.Fatalf("first argument count = %d, want 2100", got)
	}
	if got := len(executor.calls[1].args); got != 3 {
		t.Fatalf("second argument count = %d, want 3", got)
	}
	if !strings.Contains(executor.calls[1].query, "VALUES (@p1,@p2,@p3)") {
		t.Fatalf("placeholder numbering did not reset for next statement: %s", executor.calls[1].query)
	}
}

func TestInsertStopsAfterExecutorError(t *testing.T) {
	d, err := resolveDialect("mysql")
	if err != nil {
		t.Fatal(err)
	}
	builder, err := newInsertBuilder(config.DBConfig{Table: "events", Columns: []string{"id"}}, d)
	if err != nil {
		t.Fatal(err)
	}
	repository := &SQLRepository{dialect: d, builder: builder, batchSize: 1}
	executor := &capturingExecutor{failAtCall: 2}
	err = repository.insert(context.Background(), executor, []model.Record{
		{RecordNumber: 1, Fields: map[string]any{"id": 1}},
		{RecordNumber: 2, Fields: map[string]any{"id": 2}},
		{RecordNumber: 3, Fields: map[string]any{"id": 3}},
	})
	if err == nil || !strings.Contains(err.Error(), "starting at record index 1") {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(executor.calls) != 2 {
		t.Fatalf("ExecContext calls = %d, want 2", len(executor.calls))
	}
}

type capturedCall struct {
	query string
	args  []any
}

type capturingExecutor struct {
	calls      []capturedCall
	failAtCall int
}

func (e *capturingExecutor) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	e.calls = append(e.calls, capturedCall{query: query, args: append([]any(nil), args...)})
	if e.failAtCall > 0 && len(e.calls) == e.failAtCall {
		return nil, errors.New("injected execution error")
	}
	return staticResult(1), nil
}

type staticResult int64

func (r staticResult) LastInsertId() (int64, error) { return int64(r), nil }
func (r staticResult) RowsAffected() (int64, error) { return int64(r), nil }
