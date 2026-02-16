package http

/*
ParseRequest
------------
Payload HTTP untuk menjalankan parsing.
*/

type ParseRequest struct {
	RuleID        string `json:"rule_id"`
	FilePath      string `json:"file_path"`
	MaxErrorCount int    `json:"max_error_count"`
	SkipEmptyLine bool   `json:"skip_empty_line"`
}
