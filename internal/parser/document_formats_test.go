package parser

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	xlslib "github.com/extrame/xls"
	"github.com/xuri/excelize/v2"

	"parser-engine/internal/config"
	"parser-engine/internal/model"
)

func collectDocumentSources(t *testing.T, cfg config.ParserConfig, input io.Reader) ([]model.SourceRecord, error) {
	t.Helper()
	decoder, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	var records []model.SourceRecord
	err = decoder.Parse(context.Background(), input, "input.dat", func(record model.SourceRecord) error {
		records = append(records, record)
		return nil
	})
	return records, err
}

func TestHTMLTableRowsHeaderAndTableSelection(t *testing.T) {
	cfg := config.ParserConfig{
		FileType: config.FileTypeHTML, HTMLTableIndex: 1, HasHeader: true,
		MaxRecordBytes: 1024, MaxFields: 10, SkipEmptyLine: true,
		Mappings: []config.FieldMapping{{Source: "id", Target: "id"}, {Source: "name", Target: "name"}},
	}
	input := `<html><table><tr><td>ignored</td></tr></table><table><thead><tr><th>id</th><th>name</th></tr></thead><tbody><tr><td>1</td><td>A &amp; <b>B</b></td></tr><tr><td>2</td><td>C</td></tr></tbody></table></html>`
	records, err := collectDocumentSources(t, cfg, strings.NewReader(input))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(records) != 2 || records[0].Values["id"] != "1" || records[0].Values["name"] != "A & B" {
		t.Fatalf("records = %#v", records)
	}
	if records[0].Values["_sheet"] != "table[1]" || records[0].LineNumber != 2 {
		t.Fatalf("HTML metadata = %#v", records[0])
	}
}

func TestHTMLMissingTableAndTextAlias(t *testing.T) {
	htmlCfg := config.ParserConfig{FileType: config.FileTypeHTM, HTMLTableIndex: 2, MaxRecordBytes: 1024, MaxFields: 10}
	if _, err := collectDocumentSources(t, htmlCfg, strings.NewReader(`<table><tr><td>x</td></tr></table>`)); err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("missing table error = %v", err)
	}
	textCfg := config.ParserConfig{FileType: config.FileTypeText, MaxRecordBytes: 1024, SkipEmptyLine: true}
	records, err := collectDocumentSources(t, textCfg, strings.NewReader("hello\n"))
	if err != nil || len(records) != 1 || records[0].Values["raw"] != "hello" {
		t.Fatalf("TEXT records/error = %#v/%v", records, err)
	}
}

func TestXLSXRowsHeaderSheetSelectionAndDocumentLimit(t *testing.T) {
	workbook := excelize.NewFile()
	defer workbook.Close()
	index, err := workbook.NewSheet("Data")
	if err != nil {
		t.Fatal(err)
	}
	workbook.SetActiveSheet(index)
	if err := workbook.SetSheetRow("Data", "A1", &[]any{"id", "name"}); err != nil {
		t.Fatal(err)
	}
	if err := workbook.SetSheetRow("Data", "A2", &[]any{"1", "Alice"}); err != nil {
		t.Fatal(err)
	}
	if err := workbook.SetSheetRow("Data", "A3", &[]any{"2"}); err != nil {
		t.Fatal(err)
	}
	var input bytes.Buffer
	if err := workbook.Write(&input); err != nil {
		t.Fatal(err)
	}
	cfg := config.ParserConfig{
		FileType: config.FileTypeXLSX, SpreadsheetSheet: "Data", HasHeader: true,
		MaxDocumentBytes: 1024 * 1024, MaxRecordBytes: 1024, MaxFields: 10, SkipEmptyLine: true,
		Columns:  []config.ColumnSpec{{Name: "id"}, {Name: "name"}},
		Mappings: []config.FieldMapping{{Source: "id", Target: "id"}, {Source: "name", Target: "name"}},
	}
	records, err := collectDocumentSources(t, cfg, bytes.NewReader(input.Bytes()))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(records) != 2 || records[0].Values["name"] != "Alice" || records[1].Values["name"] != "" {
		t.Fatalf("records = %#v", records)
	}

	cfg.MaxDocumentBytes = input.Len() - 1
	if _, err := collectDocumentSources(t, cfg, bytes.NewReader(input.Bytes())); err == nil || !strings.Contains(err.Error(), "PARSER_MAX_DOCUMENT_BYTES") {
		t.Fatalf("document limit error = %v", err)
	}
}

func TestPDFExtractsOneRecordPerPage(t *testing.T) {
	input := minimalTextPDF("Hello PDF")
	cfg := config.ParserConfig{
		FileType: config.FileTypePDF, MaxDocumentBytes: len(input) + 1024,
		MaxRecordBytes: 1024, MaxFields: 10,
	}
	records, err := collectDocumentSources(t, cfg, bytes.NewReader(input))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(records) != 1 || records[0].Values["page"] != int64(1) || !strings.Contains(records[0].Values["text"].(string), "Hello PDF") {
		t.Fatalf("records = %#v", records)
	}
}

func TestXLSDecoderReadsLegacyWorkbookFixture(t *testing.T) {
	function := runtime.FuncForPC(reflect.ValueOf(xlslib.OpenReader).Pointer())
	if function == nil {
		t.Fatal("cannot locate XLS decoder dependency")
	}
	source, _ := function.FileLine(function.Entry())
	fixture := filepath.Join(filepath.Dir(source), "Table.xls")
	data, err := io.ReadAll(mustOpen(t, fixture))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.ParserConfig{
		FileType: config.FileTypeXLS, SpreadsheetSheet: "0",
		MaxDocumentBytes: len(data) + 1024, MaxRecordBytes: 1024 * 1024, MaxFields: 1000, SkipEmptyLine: true,
		Columns:           []config.ColumnSpec{{Name: "first"}},
		Mappings:          []config.FieldMapping{{Source: "0", Target: "first"}},
		AllowExtraColumns: true,
	}
	records, err := collectDocumentSources(t, cfg, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(records) == 0 {
		t.Fatal("XLS decoder yielded no records")
	}
}

func mustOpen(t *testing.T, path string) io.ReadCloser {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Skipf("XLS dependency fixture is unavailable: %v", err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func minimalTextPDF(text string) []byte {
	text = strings.NewReplacer("\\", "\\\\", "(", "\\(", ")", "\\)").Replace(text)
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	stream := "BT /F1 12 Tf 72 720 Td (" + text + ") Tj ET"
	objects = append(objects, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	var output bytes.Buffer
	output.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for index, object := range objects {
		offsets[index+1] = output.Len()
		fmt.Fprintf(&output, "%d 0 obj\n%s\nendobj\n", index+1, object)
	}
	xref := output.Len()
	fmt.Fprintf(&output, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for index := 1; index < len(offsets); index++ {
		fmt.Fprintf(&output, "%010d 00000 n \n", offsets[index])
	}
	fmt.Fprintf(&output, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return output.Bytes()
}
