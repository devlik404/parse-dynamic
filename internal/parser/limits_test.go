package parser

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"parser-engine/internal/config"
	"parser-engine/internal/model"
)

func TestCSVHardLimitResynchronizesQuotedMultilineRecord(t *testing.T) {
	const limit = 24
	input := "1,\"" + strings.Repeat("x", 40) + "\nwith \"\"quote\"\"\"\r\n2,ok\r\n"
	cfg := config.ParserConfig{
		FileType: config.FileTypeCSV, MaxRecordBytes: limit,
		Columns: []config.ColumnSpec{{Name: "id"}, {Name: "note"}},
	}
	records, err := collectSources(t, cfg, input)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(records) != 2 || records[0].Error == nil || records[0].Error.Code != "RECORD_TOO_LARGE" {
		t.Fatalf("records = %#v", records)
	}
	if records[0].Raw != oversizedRawMarker || records[0].Error.RawValue != oversizedRawMarker {
		t.Fatalf("oversized raw values = %#v / %#v", records[0].Raw, records[0].Error.RawValue)
	}
	if records[1].Values["id"] != "2" || records[1].Values["note"] != "ok" || records[1].LineNumber != 3 {
		t.Fatalf("resynchronized record = %#v", records[1])
	}

	framer := newCSVRecordFramer(strings.NewReader(input), ',', limit)
	first, err := framer.next()
	if err != nil || !first.tooLarge || first.peakCap > limit || cap(first.data) > limit {
		t.Fatalf("bounded frame = %#v, err=%v, cap=%d", first, err, cap(first.data))
	}
}

func TestCSVHardLimitOversizedHeaderIsFatal(t *testing.T) {
	cfg := config.ParserConfig{FileType: config.FileTypeCSV, HasHeader: true, MaxRecordBytes: 8}
	records, err := collectSources(t, cfg, strings.Repeat("h", 20)+",b\n1,2\n")
	if err == nil || !strings.Contains(err.Error(), "header") || len(records) != 0 {
		t.Fatalf("records/error = %#v/%v", records, err)
	}
}

func TestCSVOversizedMalformedQuoteRemainsFatal(t *testing.T) {
	cfg := config.ParserConfig{FileType: config.FileTypeCSV, MaxRecordBytes: 8}
	for _, input := range []string{
		"1,\"" + strings.Repeat("x", 100),
		"\"ok\"x" + strings.Repeat("y", 100) + "\n",
	} {
		records, err := collectSources(t, cfg, input)
		if err == nil || len(records) != 0 || !strings.Contains(err.Error(), "malformed quoting") {
			t.Fatalf("records/error = %#v/%v", records, err)
		}
	}
}

func TestCSVFramerBOMAndMultibyteDelimiterQuotedMultiline(t *testing.T) {
	cfg := config.ParserConfig{
		FileType: config.FileTypeCSV, Delimiter: "§", MaxRecordBytes: 128,
		Columns: []config.ColumnSpec{{Name: "id"}, {Name: "note"}},
	}
	records, err := collectSources(t, cfg, "\ufeff1§\"hello\nworld\"\n")
	if err != nil || len(records) != 1 || records[0].Values["note"] != "hello\nworld" {
		t.Fatalf("records/error = %#v/%v", records, err)
	}
}

func TestJSONHardLimitArrayAutoSingleAndPath(t *testing.T) {
	tests := []struct {
		name  string
		cfg   config.ParserConfig
		input string
		want  int
	}{
		{
			name:  "array escaped brackets",
			cfg:   config.ParserConfig{FileType: config.FileTypeJSON, JSONMode: config.JSONModeArray, MaxRecordBytes: 24},
			input: `[{"v":"` + strings.Repeat("x", 30) + `\"}]still-string"},{"id":2}]`,
			want:  2,
		},
		{
			name:  "auto next value",
			cfg:   config.ParserConfig{FileType: config.FileTypeJSON, JSONMode: config.JSONModeAuto, MaxRecordBytes: 12},
			input: `"` + strings.Repeat("x", 30) + `" {"id":2}`,
			want:  2,
		},
		{
			name:  "single",
			cfg:   config.ParserConfig{FileType: config.FileTypeJSON, JSONMode: config.JSONModeSingle, MaxRecordBytes: 12},
			input: `{"v":"` + strings.Repeat("x", 30) + `"}`,
			want:  1,
		},
		{
			name:  "path skips hostile metadata",
			cfg:   config.ParserConfig{FileType: config.FileTypeJSON, JSONMode: config.JSONModeArray, JSONRecordPath: "payload.records", MaxRecordBytes: 24},
			input: `{"metadata":"` + strings.Repeat("m", 10000) + `","payload":{"records":[{"v":"` + strings.Repeat("x", 30) + `"},{"id":2}]}}`,
			want:  2,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			records, err := collectSources(t, test.cfg, test.input)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if len(records) != test.want || records[0].Error == nil || records[0].Error.Code != "RECORD_TOO_LARGE" {
				t.Fatalf("records = %#v", records)
			}
			if records[0].Error.RawValue != oversizedRawMarker {
				t.Fatalf("raw value = %#v", records[0].Error.RawValue)
			}
			if test.want > 1 && records[1].Values["id"] == nil {
				t.Fatalf("valid record after oversized value = %#v", records[1])
			}
		})
	}
}

func TestJSONHardLimitUsesRawBytesAndClipsCapacity(t *testing.T) {
	const limit = 32
	input := `{"a":` + strings.Repeat(" ", 40) + `1}`
	scanner, err := newJSONLexScanner(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	frame, err := scanner.readBoundedValue(limit, 0)
	if err != nil || !frame.tooLarge || frame.peakCap > limit || cap(frame.data) > limit {
		t.Fatalf("frame/error/cap = %#v/%v/%d", frame, err, cap(frame.data))
	}
}

func TestJSONNestingLimitFailsWithoutPanic(t *testing.T) {
	input := strings.Repeat("[", maxJSONNestingDepth+2) + "0" + strings.Repeat("]", maxJSONNestingDepth+2)
	cfg := config.ParserConfig{FileType: config.FileTypeJSON, JSONMode: config.JSONModeSingle, MaxRecordBytes: len(input) + 1}
	if _, err := collectSources(t, cfg, input); err == nil || !strings.Contains(err.Error(), "nesting") {
		t.Fatalf("nesting error = %v", err)
	}
}

func TestXMLHardLimitResynchronizesNamespaceCDATACommentsAndPI(t *testing.T) {
	const limit = 96
	input := `<?xml version="1.0"?>` +
		`<ns:root xmlns:ns="urn:test">` +
		`<ns:item id="1" note="a>b"><!-- comment --><![CDATA[` + strings.Repeat("x", 180) + `]]><?work ok?></ns:item>` +
		`<ns:item id="2"/>` +
		`</ns:root>`
	cfg := config.ParserConfig{FileType: config.FileTypeXML, XMLRecordPath: "/root/item", MaxRecordBytes: limit}
	records, err := collectSources(t, cfg, input)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(records) != 2 || records[0].Error == nil || records[0].Error.Code != "RECORD_TOO_LARGE" {
		t.Fatalf("records = %#v", records)
	}
	if records[0].Error.RawValue != oversizedRawMarker || records[1].Values["@id"] != "2" {
		t.Fatalf("oversized/valid records = %#v / %#v", records[0], records[1])
	}

	framer, err := newXMLRecordFramer(strings.NewReader(input), []string{"root", "item"}, limit)
	if err != nil {
		t.Fatal(err)
	}
	first, err := framer.next()
	if err != nil || !first.tooLarge || first.peakCap > limit || cap(first.data) > limit {
		t.Fatalf("bounded XML frame = %#v, err=%v, cap=%d", first, err, cap(first.data))
	}
}

func TestXMLHardLimitHugeTargetAttributeAndNextRecord(t *testing.T) {
	cfg := config.ParserConfig{FileType: config.FileTypeXML, XMLRecordPath: "/root/item", MaxRecordBytes: 32}
	input := `<root><item note="` + strings.Repeat("x", 80) + `"/><item id="2"/></root>`
	records, err := collectSources(t, cfg, input)
	if err != nil || len(records) != 2 || records[0].Error == nil || records[0].Error.Code != "RECORD_TOO_LARGE" || records[1].Values["@id"] != "2" {
		t.Fatalf("records/error = %#v/%v", records, err)
	}
}

func TestXMLRejectsDoctypeAndInvalidSkippedText(t *testing.T) {
	cfg := config.ParserConfig{FileType: config.FileTypeXML, XMLRecordPath: "/root/item", MaxRecordBytes: 256}
	tests := []string{
		`<!DOCTYPE root><root><item/></root>`,
		"<root><bad>\x00</bad><item/></root>",
		`<root><bad>&#0;</bad><item/></root>`,
		`<root><bad>&#xD800;</bad><item/></root>`,
		`<root><bad>&#X41;</bad><item/></root>`,
		`<root><!-- invalid--comment --><item/></root>`,
		"<root><bad>\xff</bad><item/></root>",
	}
	for _, input := range tests {
		if _, err := collectSources(t, cfg, input); err == nil {
			t.Fatalf("malformed XML accepted: %q", input)
		}
	}
}

func TestXMLCDATAPlacementAndOverlappingTerminator(t *testing.T) {
	cfg := config.ParserConfig{FileType: config.FileTypeXML, XMLRecordPath: "/root/item", MaxRecordBytes: 128}
	for _, input := range []string{
		"<![CDATA[x]]><root><item/></root>",
		"<root><item/></root><![CDATA[x]]>",
	} {
		if _, err := collectSources(t, cfg, input); err == nil {
			t.Fatalf("CDATA outside root accepted: %q", input)
		}
	}
	records, err := collectSources(t, cfg, "<root><item><![CDATA[]]]></item></root>")
	if err != nil || len(records) != 1 || records[0].Values["$"] != "]" {
		t.Fatalf("overlapping CDATA terminator records/error = %#v/%v", records, err)
	}
}

func TestXMLRootRecordPathStillEnforcesSingleRoot(t *testing.T) {
	cfg := config.ParserConfig{FileType: config.FileTypeXML, XMLRecordPath: "/item", MaxRecordBytes: 64}
	records, err := collectSources(t, cfg, `<item id="1"/><item id="2"/>`)
	if err == nil || len(records) != 1 || !strings.Contains(err.Error(), "multiple root") {
		t.Fatalf("records/error = %#v/%v", records, err)
	}
}

func TestXMLQNameStackAggregateIsBounded(t *testing.T) {
	nameA := "a" + strings.Repeat("x", 79)
	nameB := "b" + strings.Repeat("y", 79)
	input := "<" + nameA + "><" + nameB + "></" + nameB + "></" + nameA + ">"
	cfg := config.ParserConfig{FileType: config.FileTypeXML, XMLRecordPath: "/missing", MaxRecordBytes: 128}
	_, err := collectSources(t, cfg, input)
	if err == nil || !strings.Contains(err.Error(), "stack exceeds") {
		t.Fatalf("stack bound error = %v", err)
	}
}

func TestXMLMalformedOutsideRecordRemainsFatal(t *testing.T) {
	cfg := config.ParserConfig{FileType: config.FileTypeXML, XMLRecordPath: "/root/item", MaxRecordBytes: 64}
	_, err := collectSources(t, cfg, `<root><item id="1"/><broken attr></broken></root>`)
	if err == nil {
		t.Fatal("malformed non-record markup did not fail")
	}
}

func TestHardLimitDrainHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader := &cancelAfterReader{data: []byte("1,\"" + strings.Repeat("x", 10000) + "\"\n"), cancel: cancel, after: 64}
	decoder, err := New(config.ParserConfig{FileType: config.FileTypeCSV, MaxRecordBytes: 16})
	if err != nil {
		t.Fatal(err)
	}
	err = decoder.Parse(ctx, reader, "x.csv", func(model.SourceRecord) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
}

func TestHardLimitBoundsUnderlyingReadAhead(t *testing.T) {
	const limit = 64
	tests := []struct {
		cfg   config.ParserConfig
		input string
	}{
		{config.ParserConfig{FileType: config.FileTypeCSV, MaxRecordBytes: limit}, strings.Repeat("x", 200) + "\n"},
		{config.ParserConfig{FileType: config.FileTypeJSON, JSONMode: config.JSONModeSingle, MaxRecordBytes: limit}, `{"x":"` + strings.Repeat("x", 200) + `"}`},
		{config.ParserConfig{FileType: config.FileTypeXML, XMLRecordPath: "/root/item", MaxRecordBytes: limit}, `<root><item>` + strings.Repeat("x", 200) + `</item></root>`},
	}
	for _, test := range tests {
		reader := &requestTrackingReader{reader: strings.NewReader(test.input)}
		decoder, err := New(test.cfg)
		if err != nil {
			t.Fatal(err)
		}
		err = decoder.Parse(context.Background(), reader, "input", func(model.SourceRecord) error { return nil })
		if err != nil {
			t.Fatalf("Parse() error = %v", err)
		}
		if reader.maxRequest > limit {
			t.Fatalf("underlying read requested %d bytes, cap is %d", reader.maxRequest, limit)
		}
	}
}

type cancelAfterReader struct {
	data   []byte
	offset int
	after  int
	cancel context.CancelFunc
}

type requestTrackingReader struct {
	reader     io.Reader
	maxRequest int
}

func (r *requestTrackingReader) Read(buffer []byte) (int, error) {
	if len(buffer) > r.maxRequest {
		r.maxRequest = len(buffer)
	}
	return r.reader.Read(buffer)
}

func (r *cancelAfterReader) Read(buffer []byte) (int, error) {
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	if r.offset >= r.after {
		r.cancel()
	}
	// Keep reads small so contextReader observes cancellation during drain.
	if len(buffer) > 16 {
		buffer = buffer[:16]
	}
	count := copy(buffer, r.data[r.offset:])
	r.offset += count
	return count, nil
}
