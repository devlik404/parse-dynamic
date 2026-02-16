package dto

import (
	"time"
)

/*
ParseResult
-----------
DTO untuk hasil parsing satu record.
Digunakan oleh:
- UseCase
- Interface Layer (HTTP / CLI)
- Emitter (DB / MQ / File)
*/

type ParseResult struct {
	LineNumber int                    `json:"line_number"`
	RecordCode string                 `json:"record_code"`
	Values     map[string]interface{} `json:"values"`
	ParsedAt   time.Time              `json:"parsed_at"`
	Status     ParseStatus            `json:"status"`
	Error      *ParseErrorDTO         `json:"error,omitempty"`
}

/*
ParseStatus
-----------
Status parsing per baris
*/

type ParseStatus string

const (
	ParseStatusSuccess ParseStatus = "SUCCESS"
	ParseStatusFailed  ParseStatus = "FAILED"
)

/*
ParseErrorDTO
-------------
DTO error parsing (bukan domain error)
*/

type ParseErrorDTO struct {
	Message string `json:"message"`
}

/*
SummaryResult
-------------
Opsional: ringkasan parsing satu file
*/

type SummaryResult struct {
	TotalLines   int       `json:"total_lines"`
	SuccessCount int       `json:"success_count"`
	ErrorCount   int       `json:"error_count"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at"`
}
