package service

import "time"

type Summary struct {
	Status          string        `json:"status"`
	FilesProcessed  int           `json:"files_processed"`
	TotalRecords    int64         `json:"total_records"`
	InsertedRecords int64         `json:"inserted_records"`
	RejectedRecords int64         `json:"rejected_records"`
	Batches         int64         `json:"batches"`
	StartedAt       time.Time     `json:"started_at"`
	FinishedAt      time.Time     `json:"finished_at"`
	Files           []FileSummary `json:"files"`
}

type FileSummary struct {
	File            string `json:"file"`
	Status          string `json:"status"`
	TotalRecords    int64  `json:"total_records"`
	InsertedRecords int64  `json:"inserted_records"`
	RejectedRecords int64  `json:"rejected_records"`
	Batches         int64  `json:"batches"`
}
