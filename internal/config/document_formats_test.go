package config

import "testing"

func TestLoadParserDocumentFormats(t *testing.T) {
	tests := []struct {
		name     string
		fileType string
		extra    map[string]string
	}{
		{name: "HTML", fileType: "html", extra: map[string]string{"PARSER_HTML_TABLE_INDEX": "2", "PARSER_HAS_HEADER": "true"}},
		{name: "HTM", fileType: "htm", extra: map[string]string{"PARSER_HTML_TABLE_INDEX": "0"}},
		{name: "PDF", fileType: "pdf", extra: map[string]string{}},
		{name: "XLS", fileType: "xls", extra: map[string]string{"PARSER_SPREADSHEET_SHEET": "Transactions", "PARSER_HAS_HEADER": "true"}},
		{name: "XLSX", fileType: "xlsx", extra: map[string]string{"PARSER_SPREADSHEET_SHEET": "1", "PARSER_HAS_HEADER": "true"}},
		{name: "TEXT", fileType: "text", extra: map[string]string{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := map[string]string{
				"PARSER_FILE_TYPE":          test.fileType,
				"PARSER_COLUMNS":            "value",
				"PARSER_TYPES":              "string",
				"PARSER_MAPPING":            "0:value",
				"PARSER_MAX_DOCUMENT_BYTES": "10485760",
			}
			if test.name == "PDF" {
				env["PARSER_MAPPING"] = "text:value"
			}
			if test.name == "TEXT" {
				env["PARSER_MAPPING"] = "raw:value"
			}
			for key, value := range test.extra {
				env[key] = value
			}
			cfg, err := LoadParser(mapLookup(env))
			if err != nil {
				t.Fatalf("LoadParser() error = %v", err)
			}
			if string(cfg.FileType) != test.name || cfg.MaxDocumentBytes != 10*1024*1024 {
				t.Fatalf("config = %+v", cfg)
			}
		})
	}
}
