package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"parser-engine/internal/config"
	inputfiles "parser-engine/internal/input"
	"parser-engine/internal/model"
	"parser-engine/internal/parser"
	"parser-engine/internal/repository"
	"parser-engine/internal/securefile"
)

type Runner struct {
	config     config.JobConfig
	processor  *parser.Engine
	repository repository.TransactionalRepository
	errorSink  ErrorSink
	discover   func(config.InputConfig) ([]string, error)
	openFile   func(string) (io.ReadCloser, error)
	now        func() time.Time
}

func NewRunner(cfg config.JobConfig, repo repository.TransactionalRepository, sink ErrorSink) (*Runner, error) {
	if err := config.Validate(cfg); err != nil {
		return nil, err
	}
	if repo == nil {
		return nil, errors.New("repository is required")
	}
	if sink == nil {
		return nil, errors.New("error sink is required")
	}
	processor, err := parser.NewEngine(cfg.Parser)
	if err != nil {
		return nil, fmt.Errorf("build parser pipeline: %w", err)
	}
	return &Runner{
		config:     cfg,
		processor:  processor,
		repository: repo,
		errorSink:  sink,
		discover:   inputfiles.Discover,
		openFile: func(path string) (io.ReadCloser, error) {
			return securefile.OpenRead(path)
		},
		now: time.Now,
	}, nil
}

func (r *Runner) Run(ctx context.Context) (Summary, error) {
	summary := Summary{Status: "RUNNING", StartedAt: r.now()}
	finish := func(status string) {
		summary.Status = status
		summary.FinishedAt = r.now()
		summary.FilesProcessed = len(summary.Files)
	}

	files, err := discoverInputs(r.config.Input, r.config.Error.OutputPath, r.discover)
	if err != nil {
		finish("FAILED")
		return summary, err
	}

	for _, fileName := range files {
		if err := ctx.Err(); err != nil {
			finish("FAILED")
			return summary, err
		}

		fileSummary, processErr := r.processFile(ctx, fileName)
		summary.Files = append(summary.Files, fileSummary)
		summary.TotalRecords += fileSummary.TotalRecords
		summary.InsertedRecords += fileSummary.InsertedRecords
		summary.RejectedRecords += fileSummary.RejectedRecords
		summary.Batches += fileSummary.Batches
		if processErr != nil {
			finish("FAILED")
			return summary, fmt.Errorf("process file %q: %w", fileName, processErr)
		}
	}

	status := "SUCCESS"
	if summary.RejectedRecords > 0 {
		status = "PARTIAL"
	}
	finish(status)
	return summary, nil
}

// DiscoverInputs performs the same deterministic, self-ingestion-safe input
// preflight used by Runner. The command calls it before opening a database so
// a missing path or empty glob fails without external database activity.
func DiscoverInputs(cfg config.JobConfig) ([]string, error) {
	return discoverInputs(cfg.Input, cfg.Error.OutputPath, inputfiles.Discover)
}

func discoverInputs(inputCfg config.InputConfig, errorOutputPath string, discover func(config.InputConfig) ([]string, error)) ([]string, error) {
	files, err := discover(inputCfg)
	if err != nil {
		return nil, err
	}
	return excludeErrorOutput(files, errorOutputPath)
}

func excludeErrorOutput(files []string, errorOutputPath string) ([]string, error) {
	if errorOutputPath == "" || errorOutputPath == "-" {
		return files, nil
	}
	errorPath, err := filepath.Abs(errorOutputPath)
	if err != nil {
		return nil, fmt.Errorf("resolve ERROR_OUTPUT_PATH %q: %w", errorOutputPath, err)
	}
	errorPath = filepath.Clean(errorPath)
	var errorInfo os.FileInfo
	if info, statErr := os.Lstat(errorPath); statErr == nil && info.Mode().IsRegular() {
		errorInfo = info
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect ERROR_OUTPUT_PATH %q: %w", errorOutputPath, statErr)
	}
	filtered := make([]string, 0, len(files))
	for _, fileName := range files {
		absolute, absErr := filepath.Abs(fileName)
		if absErr != nil {
			return nil, fmt.Errorf("resolve input path %q: %w", fileName, absErr)
		}
		isErrorOutput := filepath.Clean(absolute) == errorPath
		if !isErrorOutput && errorInfo != nil {
			if inputInfo, statErr := os.Lstat(absolute); statErr != nil {
				return nil, fmt.Errorf("inspect input path %q: %w", fileName, statErr)
			} else if inputInfo.Mode().IsRegular() && os.SameFile(inputInfo, errorInfo) {
				isErrorOutput = true
			}
		}
		if isErrorOutput {
			continue
		}
		filtered = append(filtered, fileName)
	}
	if len(filtered) == 0 {
		return nil, fmt.Errorf("all discovered files resolve to ERROR_OUTPUT_PATH %q", errorOutputPath)
	}
	return filtered, nil
}

func (r *Runner) processFile(ctx context.Context, fileName string) (FileSummary, error) {
	result := FileSummary{File: fileName, Status: "RUNNING"}
	file, err := r.openFile(fileName)
	if err != nil {
		result.Status = "FAILED"
		return result, fmt.Errorf("open input: %w", err)
	}
	defer file.Close()

	tx, err := r.repository.BeginFile(ctx, fileName)
	if err != nil {
		result.Status = "FAILED"
		return result, newOperationError("begin file transaction", err)
	}
	transactionDone := false
	defer func() {
		if !transactionDone {
			_ = tx.Rollback()
		}
	}()

	batch := make([]model.Record, 0, r.config.DB.BatchSize)
	batchBytes := int64(0)
	batchMaxBytes := int64(r.config.DB.BatchMaxBytes)
	lastBatchRecordNumber := int64(0)
	validRecords := int64(0)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := tx.Insert(ctx, batch); err != nil {
			return newRecordOperationError("insert batch", err, lastBatchRecordNumber)
		}
		result.Batches++
		// Drop canonical maps before reusing the count-bounded backing array;
		// otherwise stale entries beyond len(batch) would retain the prior
		// batch until every slot was overwritten.
		clear(batch)
		batch = batch[:0]
		batchBytes = 0
		lastBatchRecordNumber = 0
		return nil
	}

	processErr := r.processor.Process(ctx, file, fileName, func(outcome parser.Result) error {
		result.TotalRecords++
		if outcome.Error != nil {
			result.RejectedRecords++
			if err := r.errorSink.Write(ctx, *outcome.Error); err != nil {
				return fmt.Errorf("write rejected record: %w", err)
			}
			if r.config.Error.Mode == config.ErrorModeStrict {
				return &recordAbortError{recordError: outcome.Error}
			}
			if r.config.Error.MaxCount > 0 && result.RejectedRecords >= int64(r.config.Error.MaxCount) {
				return &policyAbortError{count: result.RejectedRecords}
			}
			return nil
		}
		if outcome.Record == nil {
			return errors.New("parser pipeline returned an empty outcome")
		}

		recordBytes := model.EstimateRecordBytes(*outcome.Record)
		// Flush before retaining a record that would cross the configured byte
		// budget. A single larger record is still allowed, but is flushed
		// immediately so the batch never retains two such records.
		if len(batch) > 0 && (recordBytes >= batchMaxBytes || batchBytes > batchMaxBytes-recordBytes) {
			if err := flush(); err != nil {
				return err
			}
		}
		batch = append(batch, *outcome.Record)
		lastBatchRecordNumber = outcome.Record.RecordNumber
		if recordBytes >= batchMaxBytes || batchBytes > batchMaxBytes-recordBytes {
			batchBytes = batchMaxBytes
		} else {
			batchBytes += recordBytes
		}
		validRecords++
		if len(batch) >= r.config.DB.BatchSize || batchBytes >= batchMaxBytes {
			return flush()
		}
		return nil
	})
	if processErr != nil {
		result.Status = "FAILED"
		if !isControlledAbort(processErr) {
			fatalRecordNumber := result.TotalRecords + 1
			var databaseFailure *operationError
			if errors.As(processErr, &databaseFailure) {
				fatalRecordNumber = databaseFailure.recordNumber
				if fatalRecordNumber < 1 {
					fatalRecordNumber = result.TotalRecords
				}
			}
			if sinkErr := r.persistFatalError(ctx, fileName, fatalRecordNumber, processErr); sinkErr != nil {
				processErr = errors.Join(processErr, sinkErr)
			}
		}
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			transactionDone = true
			return result, errors.Join(processErr, newOperationError("rollback file transaction", rollbackErr))
		}
		transactionDone = true
		return result, processErr
	}
	if err := flush(); err != nil {
		result.Status = "FAILED"
		if sinkErr := r.persistFatalError(ctx, fileName, operationRecordNumber(err, result.TotalRecords), err); sinkErr != nil {
			err = errors.Join(err, sinkErr)
		}
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			transactionDone = true
			return result, errors.Join(err, newOperationError("rollback file transaction", rollbackErr))
		}
		transactionDone = true
		return result, err
	}
	if err := tx.Commit(); err != nil {
		transactionDone = true
		result.Status = "FAILED"
		commitErr := newOperationError("commit file transaction", err)
		if sinkErr := r.persistFatalError(ctx, fileName, result.TotalRecords, commitErr); sinkErr != nil {
			return result, errors.Join(commitErr, sinkErr)
		}
		return result, commitErr
	}
	transactionDone = true
	result.InsertedRecords = validRecords
	result.Status = "SUCCESS"
	if result.RejectedRecords > 0 {
		result.Status = "PARTIAL"
	}
	return result, nil
}

func (r *Runner) persistFatalError(ctx context.Context, fileName string, recordNumber int64, cause error) error {
	if recordNumber < 1 {
		recordNumber = 1
	}
	recordError := model.RecordError{
		File:         fileName,
		RecordNumber: recordNumber,
		Code:         "FATAL_PROCESSING_ERROR",
		Phase:        "service",
		Message:      cause.Error(),
	}
	if err := r.errorSink.Write(ctx, recordError); err != nil {
		return fmt.Errorf("persist fatal processing error: %w", err)
	}
	return nil
}

// operationError keeps database driver text available to errors.Is/errors.As
// without rendering it into process logs or the durable error sink, where
// duplicate-key and constraint errors may otherwise expose bound record data.
type operationError struct {
	operation    string
	cause        error
	recordNumber int64
}

func newOperationError(operation string, cause error) error {
	return &operationError{operation: operation, cause: cause}
}

func newRecordOperationError(operation string, cause error, recordNumber int64) error {
	return &operationError{operation: operation, cause: cause, recordNumber: recordNumber}
}

func operationRecordNumber(err error, fallback int64) int64 {
	var operation *operationError
	if errors.As(err, &operation) && operation.recordNumber > 0 {
		return operation.recordNumber
	}
	return fallback
}

func (e *operationError) Error() string { return e.operation + " failed" }

func (e *operationError) Unwrap() error { return e.cause }

type recordAbortError struct {
	recordError *model.RecordError
}

func (e *recordAbortError) Error() string { return e.recordError.Error() }

type policyAbortError struct {
	count int64
}

func (e *policyAbortError) Error() string {
	return fmt.Sprintf("ERROR_MAX_COUNT reached after %d rejected records", e.count)
}

func isControlledAbort(err error) bool {
	var target *recordAbortError
	if errors.As(err, &target) {
		return true
	}
	var policyTarget *policyAbortError
	return errors.As(err, &policyTarget)
}
