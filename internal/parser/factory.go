package parser

import (
	"fmt"
	"strings"

	"parser-engine/internal/config"
)

// New validates decoder-specific configuration and creates a streaming parser.
func New(cfg config.ParserConfig) (StreamParser, error) {
	switch config.FileType(strings.ToUpper(strings.TrimSpace(string(cfg.FileType)))) {
	case config.FileTypeDelimited:
		if cfg.Delimiter == "" {
			return nil, fmt.Errorf("PARSER_DELIMITER is required for DELIMITED")
		}
		return &delimitedParser{cfg: cfg}, nil
	case config.FileTypeCSV:
		delimiter := ','
		if cfg.Delimiter != "" {
			runes := []rune(cfg.Delimiter)
			if len(runes) != 1 || !validCSVDelimiter(runes[0]) {
				return nil, fmt.Errorf("CSV delimiter must be one valid rune")
			}
			delimiter = runes[0]
		}
		return &csvParser{cfg: cfg, delimiter: delimiter}, nil
	case config.FileTypeTSV:
		return &csvParser{cfg: cfg, delimiter: '\t'}, nil
	case config.FileTypeFixedWidth:
		if len(cfg.FixedWidthFields) == 0 {
			return nil, fmt.Errorf("PARSER_FIXED_WIDTH_FIELDS is required for FIXED_WIDTH")
		}
		if len(cfg.FixedWidthFields) > configuredMaxFields(cfg.MaxFields) {
			return nil, fmt.Errorf("PARSER_FIXED_WIDTH_FIELDS exceeds PARSER_MAX_FIELDS")
		}
		unit := strings.ToUpper(strings.TrimSpace(cfg.FixedWidthUnit))
		if unit == "" {
			unit = "BYTE"
		}
		if unit != "BYTE" && unit != "RUNE" {
			return nil, fmt.Errorf("PARSER_FIXED_WIDTH_UNIT must be BYTE or RUNE")
		}
		for _, field := range cfg.FixedWidthFields {
			if field.Name == "" || field.Start < 0 || field.Length <= 0 {
				return nil, fmt.Errorf("invalid fixed-width field %q: start must be >= 0 and length > 0", field.Name)
			}
		}
		return &fixedWidthParser{cfg: cfg, unit: unit}, nil
	case config.FileTypeJSON:
		mode := config.JSONMode(strings.ToUpper(strings.TrimSpace(string(cfg.JSONMode))))
		if mode == "" {
			mode = config.JSONModeAuto
		}
		switch mode {
		case config.JSONModeAuto, config.JSONModeSingle, config.JSONModeArray, config.JSONModeNDJSON:
			return &jsonParser{cfg: cfg, mode: mode, recordPath: splitPath(cfg.JSONRecordPath)}, nil
		default:
			return nil, fmt.Errorf("unsupported PARSER_JSON_MODE %q", cfg.JSONMode)
		}
	case config.FileTypeXML:
		path := splitPath(cfg.XMLRecordPath)
		if len(path) == 0 {
			return nil, fmt.Errorf("PARSER_XML_RECORD_PATH is required for XML")
		}
		return &xmlParser{cfg: cfg, recordPath: path}, nil
	case config.FileTypeRaw, config.FileTypeText:
		return &rawParser{cfg: cfg}, nil
	case config.FileTypeHTML, config.FileTypeHTM:
		return &htmlParser{cfg: cfg}, nil
	case config.FileTypePDF:
		return &pdfParser{cfg: cfg}, nil
	case config.FileTypeXLS:
		return &xlsParser{cfg: cfg}, nil
	case config.FileTypeXLSX:
		return &xlsxParser{cfg: cfg}, nil
	case config.FileTypeSectionedDelimited:
		return newSectionedDelimitedParser(cfg)
	default:
		return nil, fmt.Errorf("unsupported PARSER_FILE_TYPE %q", cfg.FileType)
	}
}

func splitPath(path string) []string {
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(path, "$")
	path = strings.Trim(path, "./ ")
	if path == "" {
		return nil
	}
	return strings.FieldsFunc(path, func(r rune) bool { return r == '.' || r == '/' })
}
