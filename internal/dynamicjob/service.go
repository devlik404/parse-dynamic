// Package dynamicjob implements the scheduler-triggered dynamic parsing flow.
// Reader configuration is loaded per request from the resolved target MSSQL
// database; target table/column mappings continue to come from the existing
// ParamParseFile tables.
package dynamicjob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"parser-engine/internal/config"
	"parser-engine/internal/miniogateway"
	"parser-engine/internal/model"
	"parser-engine/internal/parser"
	"parser-engine/internal/repository"
)

const (
	Route       = "/parse-file-dynamic"
	HealthRoute = "/healthz"

	metadataFileName = "__dynamic_file_name"
	metadataFileDate = "__dynamic_file_date"
)

type Request struct {
	FinalFileName  string `json:"final_file_name"`
	FinalMinioPath string `json:"final_minio_path"`
	FileDate       string `json:"file_date"`
	LANum          int64  `json:"la_num"`
	ProductID      string `json:"product_id"`
	Task           string `json:"task"`
	Activity       string `json:"activity"`
}

type Result struct {
	TotalData int64
}

type Response struct {
	Status    bool   `json:"status"`
	Message   string `json:"message"`
	TotalData int64  `json:"total_data"`
}

type ErrorKind string

const (
	ErrorInvalidRequest ErrorKind = "INVALID_REQUEST"
	ErrorConfig         ErrorKind = "PARSER_CONFIG_ERROR"
	ErrorFileNotFound   ErrorKind = "FILE_NOT_FOUND"
	ErrorFileObject     ErrorKind = "FILE_OBJECT_ERROR"
	ErrorInputTooLarge  ErrorKind = "INPUT_TOO_LARGE"
	ErrorParse          ErrorKind = "PARSE_ERROR"
	ErrorDatabase       ErrorKind = "DATABASE_ERROR"
	ErrorObjectChanged  ErrorKind = "OBJECT_CHANGED"
	ErrorCanceled       ErrorKind = "REQUEST_CANCELED"
	ErrorTimeout        ErrorKind = "REQUEST_TIMEOUT"
)

type JobError struct {
	Kind    ErrorKind
	Message string
	Cause   error
}

func (e *JobError) Error() string {
	if e == nil || e.Message == "" {
		return "dynamic parse job failed"
	}
	return e.Message
}

func (e *JobError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

type ObjectSource interface {
	StatObject(ctx context.Context, minioPath, fileName string) (miniogateway.ObjectMetadata, error)
	OpenObject(ctx context.Context, minioPath, fileName string, offset int64) (io.ReadCloser, error)
}

type Plan struct {
	Parser          config.ParserConfig
	Repository      repository.TransactionalRepository
	BatchSize       int
	BatchMaxBytes   int
	IncludeFileName bool
	IncludeFileDate bool
	configurationID int64
}

func (p *Plan) close() error {
	if p == nil || p.Repository == nil {
		return nil
	}
	return p.Repository.Close()
}

type PlanLoader interface {
	Load(ctx context.Context, request Request) (*Plan, error)
}

type Options struct {
	MaxInputBytes        int64
	MaxRecordBytes       int
	MaxDocumentBytes     int
	MaxFields            int
	DefaultBatchSize     int
	DefaultBatchMaxBytes int
	AllowedPathPrefixes  []string
}

type Service struct {
	source ObjectSource
	plans  PlanLoader
	opts   Options
}

func NewService(source ObjectSource, plans PlanLoader, opts Options) (*Service, error) {
	if source == nil {
		return nil, errors.New("dynamic job object source is required")
	}
	if plans == nil {
		return nil, errors.New("dynamic job plan loader is required")
	}
	if opts.MaxInputBytes <= 0 {
		opts.MaxInputBytes = 64 * 1024 * 1024
	}
	if opts.MaxRecordBytes <= 0 {
		opts.MaxRecordBytes = 8 * 1024 * 1024
	}
	if opts.MaxDocumentBytes <= 0 {
		opts.MaxDocumentBytes = 512 * 1024 * 1024
	}
	if opts.MaxFields <= 0 {
		opts.MaxFields = 100_000
	}
	if opts.DefaultBatchSize <= 0 {
		opts.DefaultBatchSize = 500
	}
	if opts.DefaultBatchMaxBytes <= 0 {
		opts.DefaultBatchMaxBytes = 64 * 1024 * 1024
	}
	return &Service{source: source, plans: plans, opts: opts}, nil
}

func (s *Service) Execute(ctx context.Context, request Request) (result Result, returnErr error) {
	if err := validateRequest(ctx, request, s.opts.AllowedPathPrefixes); err != nil {
		return result, err
	}

	plan, err := s.plans.Load(ctx, request)
	if err != nil {
		return result, classifyContext(err, ErrorConfig, "unable to load dynamic parser configuration")
	}
	if plan == nil || plan.Repository == nil {
		return result, &JobError{Kind: ErrorConfig, Message: "dynamic parser configuration is incomplete"}
	}
	defer func() {
		if closeErr := plan.close(); closeErr != nil && returnErr == nil {
			returnErr = &JobError{Kind: ErrorDatabase, Message: "unable to close target database connection", Cause: closeErr}
		}
	}()

	if err := s.validatePlan(plan); err != nil {
		return result, &JobError{Kind: ErrorConfig, Message: "dynamic parser configuration is invalid", Cause: err}
	}
	engine, err := parser.NewEngine(plan.Parser)
	if err != nil {
		return result, &JobError{Kind: ErrorConfig, Message: "dynamic parser configuration is invalid", Cause: err}
	}

	initial, err := s.source.StatObject(ctx, request.FinalMinioPath, request.FinalFileName)
	if err != nil {
		return result, classifyObjectError(ctx, err, "unable to read input file metadata from MinIO")
	}
	if initial.Size > s.opts.MaxInputBytes {
		return result, &JobError{Kind: ErrorInputTooLarge, Message: "input file exceeds the configured byte limit"}
	}

	stream, err := s.source.OpenObject(ctx, request.FinalMinioPath, request.FinalFileName, 0)
	if err != nil {
		return result, classifyObjectError(ctx, err, "unable to open input file from MinIO")
	}
	if stream == nil {
		return result, &JobError{Kind: ErrorFileObject, Message: "unable to open input file from MinIO"}
	}
	reader := &boundedObjectReader{ReadCloser: stream, remaining: s.opts.MaxInputBytes}
	defer reader.Close()

	tx, err := plan.Repository.BeginFile(ctx, request.FinalFileName)
	if err != nil {
		return result, classifyContext(err, ErrorDatabase, "unable to begin target database transaction")
	}
	transactionDone := false
	defer func() {
		if !transactionDone {
			_ = tx.Rollback()
		}
	}()

	batchSize := plan.BatchSize
	if batchSize <= 0 {
		batchSize = s.opts.DefaultBatchSize
	}
	batchMaxBytes := plan.BatchMaxBytes
	if batchMaxBytes <= 0 {
		batchMaxBytes = s.opts.DefaultBatchMaxBytes
	}
	batch := make([]model.Record, 0, batchSize)
	batchBytes := int64(0)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := tx.Insert(ctx, batch); err != nil {
			return &JobError{Kind: ErrorDatabase, Message: "unable to insert parsed records into target table", Cause: err}
		}
		clear(batch)
		batch = batch[:0]
		batchBytes = 0
		return nil
	}

	fileDate, err := time.Parse("2006-01-02", request.FileDate)
	if err != nil {
		return result, &JobError{Kind: ErrorInvalidRequest, Message: "file_date must use YYYY-MM-DD", Cause: err}
	}

	processErr := engine.Process(ctx, reader, request.FinalFileName, func(outcome parser.Result) error {
		if outcome.Error != nil {
			return &JobError{Kind: ErrorParse, Message: "input file contains a rejected record", Cause: outcome.Error}
		}
		if outcome.Record == nil {
			return &JobError{Kind: ErrorParse, Message: "parser returned an empty record"}
		}
		if plan.IncludeFileName {
			outcome.Record.Fields[metadataFileName] = request.FinalFileName
		}
		if plan.IncludeFileDate {
			outcome.Record.Fields[metadataFileDate] = fileDate
		}
		recordBytes := int64(model.EstimateRecordBytes(*outcome.Record))
		if len(batch) > 0 && (recordBytes >= int64(batchMaxBytes) || batchBytes > int64(batchMaxBytes)-recordBytes) {
			if err := flush(); err != nil {
				return err
			}
		}
		batch = append(batch, *outcome.Record)
		batchBytes += recordBytes
		result.TotalData++
		if len(batch) >= batchSize || batchBytes >= int64(batchMaxBytes) {
			return flush()
		}
		return nil
	})
	if processErr != nil {
		if errors.Is(processErr, errObjectTooLarge) {
			return Result{}, &JobError{Kind: ErrorInputTooLarge, Message: "input file exceeds the configured byte limit", Cause: processErr}
		}
		var jobErr *JobError
		if errors.As(processErr, &jobErr) {
			return Result{}, jobErr
		}
		return Result{}, classifyContext(processErr, ErrorParse, "input file could not be parsed")
	}
	if err := flush(); err != nil {
		return Result{}, err
	}
	if closeErr := reader.Close(); closeErr != nil {
		return Result{}, &JobError{Kind: ErrorFileObject, Message: "unable to close input file from MinIO", Cause: closeErr}
	}
	if !reader.reachedEOF || reader.bytesRead != initial.Size {
		return Result{}, &JobError{Kind: ErrorObjectChanged, Message: "MinIO object changed while it was being read"}
	}
	finalMetadata, err := s.source.StatObject(ctx, request.FinalMinioPath, request.FinalFileName)
	if err != nil {
		return Result{}, classifyObjectError(ctx, err, "unable to verify input file metadata from MinIO")
	}
	if !initial.SameObject(finalMetadata) {
		return Result{}, &JobError{Kind: ErrorObjectChanged, Message: "MinIO object changed while it was being read"}
	}
	if result.TotalData == 0 {
		return Result{}, &JobError{Kind: ErrorParse, Message: "input file did not produce any records"}
	}
	if err := tx.Commit(); err != nil {
		transactionDone = true
		return Result{}, &JobError{Kind: ErrorDatabase, Message: "unable to commit parsed records", Cause: err}
	}
	transactionDone = true
	return result, nil
}

func (s *Service) validatePlan(plan *Plan) error {
	if plan.Parser.MaxRecordBytes > s.opts.MaxRecordBytes {
		return fmt.Errorf("MaxRecordBytes exceeds the server ceiling")
	}
	if plan.Parser.MaxDocumentBytes > s.opts.MaxDocumentBytes {
		return fmt.Errorf("MaxDocumentBytes exceeds the server ceiling")
	}
	if plan.Parser.MaxFields > s.opts.MaxFields {
		return fmt.Errorf("MaxFields exceeds the server ceiling")
	}
	return config.ValidateParser(plan.Parser)
}

func validateRequest(ctx context.Context, request Request, prefixes []string) error {
	if ctx == nil {
		return &JobError{Kind: ErrorInvalidRequest, Message: "request context is required"}
	}
	if err := ctx.Err(); err != nil {
		return contextJobError(err)
	}
	for name, value := range map[string]string{
		"product_id": request.ProductID,
		"task":       request.Task,
		"activity":   request.Activity,
		"file_date":  request.FileDate,
	} {
		if value == "" || strings.TrimSpace(value) != value || len(value) > 256 {
			return &JobError{Kind: ErrorInvalidRequest, Message: name + " is required and must be valid"}
		}
	}
	if request.LANum <= 0 {
		return &JobError{Kind: ErrorInvalidRequest, Message: "la_num must be greater than zero"}
	}
	if err := miniogateway.ValidateReference(request.FinalMinioPath, request.FinalFileName); err != nil {
		return &JobError{Kind: ErrorInvalidRequest, Message: "final_minio_path or final_file_name is invalid", Cause: err}
	}
	if len(prefixes) > 0 {
		allowed := false
		for _, prefix := range prefixes {
			prefix = strings.TrimSuffix(prefix, "/")
			if request.FinalMinioPath == prefix || strings.HasPrefix(request.FinalMinioPath, prefix+"/") {
				allowed = true
				break
			}
		}
		if !allowed {
			return &JobError{Kind: ErrorInvalidRequest, Message: "final_minio_path is outside the allowed path prefixes"}
		}
	}
	if _, err := time.Parse("2006-01-02", request.FileDate); err != nil {
		return &JobError{Kind: ErrorInvalidRequest, Message: "file_date must use YYYY-MM-DD", Cause: err}
	}
	return nil
}

func classifyObjectError(ctx context.Context, err error, message string) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return contextJobError(ctxErr)
	}
	if errors.Is(err, miniogateway.ErrObjectNotFound) {
		return &JobError{Kind: ErrorFileNotFound, Message: "input file was not found in MinIO", Cause: err}
	}
	return &JobError{Kind: ErrorFileObject, Message: message, Cause: err}
}

func classifyContext(err error, fallback ErrorKind, message string) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return contextJobError(err)
	}
	var jobErr *JobError
	if errors.As(err, &jobErr) {
		return jobErr
	}
	return &JobError{Kind: fallback, Message: message, Cause: err}
}

func contextJobError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &JobError{Kind: ErrorTimeout, Message: "dynamic parse request timed out", Cause: err}
	}
	return &JobError{Kind: ErrorCanceled, Message: "dynamic parse request was canceled", Cause: err}
}

var errObjectTooLarge = errors.New("input object exceeds configured byte limit")

type boundedObjectReader struct {
	io.ReadCloser
	remaining  int64
	bytesRead  int64
	reachedEOF bool
	closed     bool
}

func (r *boundedObjectReader) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	if r.remaining < 0 {
		return 0, errObjectTooLarge
	}
	if int64(len(buffer)) > r.remaining+1 {
		buffer = buffer[:r.remaining+1]
	}
	count, err := r.ReadCloser.Read(buffer)
	r.bytesRead += int64(count)
	r.remaining -= int64(count)
	if r.remaining < 0 {
		return count, errObjectTooLarge
	}
	if errors.Is(err, io.EOF) {
		r.reachedEOF = true
	}
	return count, err
}

func (r *boundedObjectReader) Close() error {
	if r == nil || r.closed {
		return nil
	}
	r.closed = true
	return r.ReadCloser.Close()
}
