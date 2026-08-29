// Package preview exposes a bounded, read-only parser preview for files that
// are streamed from the same MinIO gateway used by the production job. Parser
// configuration is supplied and validated at process startup; it is never
// selected by an HTTP caller or downloaded from an object store.
package preview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"parser-engine/internal/config"
	"parser-engine/internal/miniogateway"
	"parser-engine/internal/model"
	"parser-engine/internal/parser"
)

const (
	Route       = "/parse-file-dynamic"
	HealthRoute = "/healthz"

	defaultPreviewLimit       = 20
	defaultMaxPreviewLimit    = 100
	hardMaxPreviewLimit       = 1000
	defaultMaxRequestBytes    = int64(16 * 1024)
	hardMaxRequestBytes       = int64(1024 * 1024)
	defaultMaxResponseBytes   = int64(2 * 1024 * 1024)
	hardMaxResponseBytes      = int64(8 * 1024 * 1024)
	defaultMaxInputBytes      = int64(64 * 1024 * 1024)
	hardMaxInputBytes         = int64(2 * 1024 * 1024 * 1024)
	defaultMaxValueBytes      = 1024
	hardMaxValueBytes         = 4096
	defaultRequestTimeout     = 30 * time.Second
	hardMaxRequestTimeout     = 10 * time.Minute
	defaultMaxConcurrency     = 4
	hardMaxConcurrency        = 256
	minBearerTokenBytes       = 32
	maxBearerTokenBytes       = 4096
	maxPreviewFields          = 100
	maxPreviewCollectionSize  = 100
	maxPreviewDepth           = 6
	maxPreviewJSONBytes       = 256 * 1024
	maxPreviewResponseRefSize = 256
	progressByteInterval      = int64(1024 * 1024)
	progressTimeInterval      = 5 * time.Second
)

// ObjectSource is the narrow interface implemented by miniogateway.Client.
// The path and file name remain separate to preserve the reference job's
// existing storage contract.
type ObjectSource interface {
	Open(ctx context.Context, minioPath, fileName string) (io.ReadCloser, error)
}

// MetadataObjectSource is implemented by gateways that support the same
// HEAD/GET-with-offset contract as cashrecon-sch-parse-file-qris-tap. Keeping
// it optional preserves compatibility with simple ObjectSource adapters.
type MetadataObjectSource interface {
	ObjectSource
	StatObject(ctx context.Context, minioPath, fileName string) (miniogateway.ObjectMetadata, error)
	OpenObject(ctx context.Context, minioPath, fileName string, offset int64) (io.ReadCloser, error)
}

// Options contains server-owned policy. The request can choose neither parser
// configuration nor MinIO connection details.
type Options struct {
	// AllowedPathPrefixes optionally restricts final_minio_path to one or more
	// directory-like prefixes. An empty slice applies no extra prefix policy;
	// traversal and malformed paths are rejected independently.
	AllowedPathPrefixes []string
	// If RequireBearerAuth is true, BearerToken is mandatory. A non-empty token
	// always enables authentication, even when RequireBearerAuth is false.
	RequireBearerAuth bool
	BearerToken       string

	DefaultLimit          int
	MaxLimit              int
	MaxInputBytes         int64
	MaxRequestBytes       int64
	MaxResponseBytes      int64
	MaxValueBytes         int
	RequestTimeout        time.Duration
	MaxConcurrentRequests int

	// Logger receives fixed-schema operational events. Request values, object
	// paths, filenames, payloads, raw errors, and credentials are never logged.
	// A nil logger disables preview logs, which keeps library callers quiet.
	Logger *slog.Logger
}

// Request mirrors the scheduler payload used by cashrecon-sch-parse-file-atm-
// bersama. Reference metadata is deliberately accepted but does not influence
// parsing. RawMessage allows existing schedulers to send either scalar or null
// metadata without expanding the preview service's trust boundary.
type Request struct {
	FinalMinioPath string          `json:"final_minio_path"`
	FinalFileName  string          `json:"final_file_name"`
	Limit          int             `json:"limit,omitempty"`
	LANum          json.RawMessage `json:"la_num,omitempty"`
	ProductID      json.RawMessage `json:"product_id,omitempty"`
	Task           json.RawMessage `json:"task,omitempty"`
	Activity       json.RawMessage `json:"activity,omitempty"`
	FileDate       json.RawMessage `json:"file_date,omitempty"`
}

type PreviewRecord struct {
	RecordNumber int64          `json:"record_number"`
	LineNumber   int64          `json:"line_number,omitempty"`
	Fields       map[string]any `json:"fields"`
}

type PreviewError struct {
	RecordNumber int64  `json:"record_number"`
	LineNumber   int64  `json:"line_number,omitempty"`
	Field        string `json:"field,omitempty"`
	Code         string `json:"code"`
	Phase        string `json:"phase"`
	Message      string `json:"message"`
	RawValue     any    `json:"raw_value,omitempty"`
}

type Summary struct {
	RequestedLimit int  `json:"requested_limit"`
	Results        int  `json:"results"`
	Records        int  `json:"records"`
	Errors         int  `json:"errors"`
	Truncated      bool `json:"truncated"`
}

type Response struct {
	Status         string          `json:"status"`
	FinalMinioPath string          `json:"final_minio_path"`
	FinalFileName  string          `json:"final_file_name"`
	Records        []PreviewRecord `json:"records"`
	Errors         []PreviewError  `json:"errors"`
	Summary        Summary         `json:"summary"`
	Error          *APIError       `json:"error,omitempty"`
}

type Service struct {
	source ObjectSource
	engine *parser.Engine
	opts   Options
	logger *slog.Logger
}

// NewService validates both the parser and all resource policy once at
// startup. The resulting engine is immutable and safe to use concurrently.
func NewService(source ObjectSource, parserConfig config.ParserConfig, opts Options) (*Service, error) {
	if source == nil {
		return nil, fmt.Errorf("preview object source is required")
	}
	if err := config.ValidateParser(parserConfig); err != nil {
		return nil, fmt.Errorf("invalid preview parser configuration: %w", err)
	}
	engine, err := parser.NewEngine(cloneParserConfig(parserConfig))
	if err != nil {
		return nil, fmt.Errorf("preview parser cannot be initialized: %w", err)
	}
	opts, err = normalizeOptions(opts)
	if err != nil {
		return nil, err
	}
	// Authentication material is consumed by NewHandler and must not remain in
	// the long-lived service policy.
	opts.BearerToken = ""
	return &Service{source: source, engine: engine, opts: opts, logger: opts.Logger}, nil
}

func cloneParserConfig(input config.ParserConfig) config.ParserConfig {
	result := input
	result.Columns = append([]config.ColumnSpec(nil), input.Columns...)
	result.Mappings = append([]config.FieldMapping(nil), input.Mappings...)
	result.FixedWidthFields = append([]config.FixedWidthField(nil), input.FixedWidthFields...)
	result.UppercaseFields = append([]string(nil), input.UppercaseFields...)
	result.LowercaseFields = append([]string(nil), input.LowercaseFields...)
	result.Transforms = append([]config.TransformRule(nil), input.Transforms...)
	result.RequiredFields = append([]string(nil), input.RequiredFields...)
	if input.DefaultValues != nil {
		result.DefaultValues = make(map[string]string, len(input.DefaultValues))
		for key, value := range input.DefaultValues {
			result.DefaultValues[key] = value
		}
	}
	return result
}

func normalizeOptions(opts Options) (Options, error) {
	if opts.RequireBearerAuth && opts.BearerToken == "" {
		return Options{}, fmt.Errorf("preview BearerToken is required when RequireBearerAuth is enabled")
	}
	if opts.BearerToken != "" {
		if err := validateBearerToken(opts.BearerToken); err != nil {
			return Options{}, err
		}
	}

	var err error
	opts.AllowedPathPrefixes, err = validatePathPrefixes(opts.AllowedPathPrefixes)
	if err != nil {
		return Options{}, err
	}
	if opts.DefaultLimit == 0 {
		opts.DefaultLimit = defaultPreviewLimit
	}
	if opts.MaxLimit == 0 {
		opts.MaxLimit = defaultMaxPreviewLimit
	}
	if opts.MaxRequestBytes == 0 {
		opts.MaxRequestBytes = defaultMaxRequestBytes
	}
	if opts.MaxInputBytes == 0 {
		opts.MaxInputBytes = defaultMaxInputBytes
	}
	if opts.MaxResponseBytes == 0 {
		opts.MaxResponseBytes = defaultMaxResponseBytes
	}
	if opts.MaxValueBytes == 0 {
		opts.MaxValueBytes = defaultMaxValueBytes
	}
	if opts.RequestTimeout == 0 {
		opts.RequestTimeout = defaultRequestTimeout
	}
	if opts.MaxConcurrentRequests == 0 {
		opts.MaxConcurrentRequests = defaultMaxConcurrency
	}

	var problems []string
	if opts.MaxLimit <= 0 || opts.MaxLimit > hardMaxPreviewLimit {
		problems = append(problems, fmt.Sprintf("MaxLimit must be between 1 and %d", hardMaxPreviewLimit))
	}
	if opts.DefaultLimit <= 0 || opts.DefaultLimit > opts.MaxLimit {
		problems = append(problems, "DefaultLimit must be between 1 and MaxLimit")
	}
	if opts.MaxRequestBytes <= 0 || opts.MaxRequestBytes > hardMaxRequestBytes {
		problems = append(problems, fmt.Sprintf("MaxRequestBytes must be between 1 and %d", hardMaxRequestBytes))
	}
	if opts.MaxInputBytes <= 0 || opts.MaxInputBytes > hardMaxInputBytes {
		problems = append(problems, fmt.Sprintf("MaxInputBytes must be between 1 and %d", hardMaxInputBytes))
	}
	if opts.MaxResponseBytes < 4096 || opts.MaxResponseBytes > hardMaxResponseBytes {
		problems = append(problems, fmt.Sprintf("MaxResponseBytes must be between 4096 and %d", hardMaxResponseBytes))
	}
	if opts.MaxValueBytes <= 0 || opts.MaxValueBytes > hardMaxValueBytes {
		problems = append(problems, fmt.Sprintf("MaxValueBytes must be between 1 and %d", hardMaxValueBytes))
	}
	if opts.RequestTimeout <= 0 || opts.RequestTimeout > hardMaxRequestTimeout {
		problems = append(problems, fmt.Sprintf("RequestTimeout must be between 1ns and %s", hardMaxRequestTimeout))
	}
	if opts.MaxConcurrentRequests <= 0 || opts.MaxConcurrentRequests > hardMaxConcurrency {
		problems = append(problems, fmt.Sprintf("MaxConcurrentRequests must be between 1 and %d", hardMaxConcurrency))
	}
	if len(problems) > 0 {
		return Options{}, fmt.Errorf("invalid preview options: %s", strings.Join(problems, "; "))
	}
	return opts, nil
}

type errorKind string

const (
	errorInvalidRequest errorKind = "INVALID_REQUEST"
	errorFileObject     errorKind = "FILE_OBJECT_ERROR"
	errorObjectChanged  errorKind = "OBJECT_CHANGED"
	errorInputTooLarge  errorKind = "INPUT_TOO_LARGE"
	errorParse          errorKind = "PARSE_ERROR"
	errorCanceled       errorKind = "REQUEST_CANCELED"
	errorTimeout        errorKind = "REQUEST_TIMEOUT"
)

type serviceError struct {
	kind    errorKind
	message string
	cause   error
}

func (e *serviceError) Error() string {
	if e == nil {
		return "preview failed"
	}
	return e.message
}

func (e *serviceError) Unwrap() error { return e.cause }

var errPreviewComplete = errors.New("preview result limit reached")
var errInputByteLimit = errors.New("input object exceeds the configured byte limit")

// Preview opens exactly one configured file and streams it through the parser.
// It has no database dependency and cannot perform persistence.
func (s *Service) Preview(ctx context.Context, req Request) (Response, error) {
	startedAt := time.Now()
	response := Response{
		Status:  "ERROR",
		Records: make([]PreviewRecord, 0),
		Errors:  make([]PreviewError, 0),
	}
	if ctx == nil {
		return response, &serviceError{kind: errorInvalidRequest, message: "request context is required"}
	}
	if err := ctx.Err(); err != nil {
		return response, contextError(err)
	}

	minioPath := req.FinalMinioPath
	fileName := req.FinalFileName
	if strings.TrimSpace(minioPath) != minioPath || strings.TrimSpace(fileName) != fileName {
		return response, &serviceError{kind: errorInvalidRequest, message: "final_minio_path and final_file_name must not have surrounding whitespace"}
	}
	if err := miniogateway.ValidateReference(minioPath, fileName); err != nil {
		return response, &serviceError{kind: errorInvalidRequest, message: "final_minio_path or final_file_name is invalid", cause: err}
	}
	if !hasAllowedPathPrefix(minioPath, s.opts.AllowedPathPrefixes) {
		return response, &serviceError{kind: errorInvalidRequest, message: "final_minio_path is outside the allowed path prefixes"}
	}
	limit := req.Limit
	if limit == 0 {
		limit = s.opts.DefaultLimit
	}
	if limit < 1 || limit > s.opts.MaxLimit {
		return response, &serviceError{kind: errorInvalidRequest, message: fmt.Sprintf("limit must be between 1 and %d", s.opts.MaxLimit)}
	}

	response.FinalMinioPath = sanitizeText(minioPath, maxPreviewResponseRefSize)
	response.FinalFileName = sanitizeText(fileName, maxPreviewResponseRefSize)
	response.Summary.RequestedLimit = limit

	logger := requestLogger(ctx, s.logger)
	var initialMetadata *miniogateway.ObjectMetadata
	metadataSource, supportsMetadata := s.source.(MetadataObjectSource)
	if supportsMetadata {
		statStartedAt := time.Now()
		logger.InfoContext(ctx, "minio_stat_started")
		metadata, statErr := metadataSource.StatObject(ctx, minioPath, fileName)
		if statErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				logger.WarnContext(ctx, "minio_stat_failed", "error_code", string(contextError(ctxErr).kind), "duration_ms", elapsedMilliseconds(statStartedAt))
				return response, contextError(ctxErr)
			}
			logger.WarnContext(ctx, "minio_stat_failed", "error_code", string(errorFileObject), "duration_ms", elapsedMilliseconds(statStartedAt))
			return response, &serviceError{kind: errorFileObject, message: "unable to read input file metadata from MinIO", cause: statErr}
		}
		if metadata.Size > s.opts.MaxInputBytes {
			logger.WarnContext(ctx, "minio_stat_failed", "error_code", string(errorInputTooLarge), "duration_ms", elapsedMilliseconds(statStartedAt))
			return response, &serviceError{kind: errorInputTooLarge, message: "input file exceeds the configured byte limit", cause: errInputByteLimit}
		}
		initialMetadata = &metadata
		logger.InfoContext(ctx, "minio_metadata_loaded", "object_size", metadata.Size, "duration_ms", elapsedMilliseconds(statStartedAt))
	}

	logger.InfoContext(ctx, "minio_open_started")
	openStartedAt := time.Now()
	var fileReader io.ReadCloser
	var err error
	if supportsMetadata {
		fileReader, err = metadataSource.OpenObject(ctx, minioPath, fileName, 0)
	} else {
		fileReader, err = s.source.Open(ctx, minioPath, fileName)
	}
	if err != nil {
		if fileReader != nil {
			_ = fileReader.Close()
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			logger.WarnContext(ctx, "minio_open_failed", "error_code", string(contextError(ctxErr).kind), "duration_ms", elapsedMilliseconds(openStartedAt))
			return response, contextError(ctxErr)
		}
		logger.WarnContext(ctx, "minio_open_failed", "error_code", string(errorFileObject), "duration_ms", elapsedMilliseconds(openStartedAt))
		return response, &serviceError{kind: errorFileObject, message: "unable to open input file from MinIO", cause: err}
	}
	if fileReader == nil {
		logger.WarnContext(ctx, "minio_open_failed", "error_code", string(errorFileObject), "duration_ms", elapsedMilliseconds(openStartedAt))
		return response, &serviceError{kind: errorFileObject, message: "unable to open input file from MinIO"}
	}
	logger.InfoContext(ctx, "minio_stream_opened", "duration_ms", elapsedMilliseconds(openStartedAt))
	limitedReader := &inputBoundedReadCloser{ReadCloser: fileReader, remaining: s.opts.MaxInputBytes}
	trackedReader := &errorTrackingReadCloser{ReadCloser: limitedReader}
	progress := &progressReadCloser{
		ReadCloser: trackedReader,
		logger:     logger,
		startedAt:  startedAt,
		lastAt:     startedAt,
		ctx:        ctx,
		nextBytes:  progressByteInterval,
		results:    &response.Summary.Results,
		records:    &response.Summary.Records,
		errors:     &response.Summary.Errors,
	}

	usedBytes, _ := encodedSize(response)
	usedBytes += 256
	logger.InfoContext(ctx, "parsing_started", "limit", limit)
	processErr := s.engine.Process(ctx, progress, fileName, func(result parser.Result) error {
		if response.Summary.Results >= limit {
			response.Summary.Truncated = true
			return errPreviewComplete
		}
		if result.Record != nil {
			item := PreviewRecord{
				RecordNumber: result.Record.RecordNumber,
				LineNumber:   result.Record.LineNumber,
				Fields:       sanitizeFields(result.Record.Fields, s.opts.MaxValueBytes),
			}
			size, err := encodedSize(item)
			if err != nil || usedBytes+size+1 > s.opts.MaxResponseBytes {
				response.Summary.Truncated = true
				return errPreviewComplete
			}
			response.Records = append(response.Records, item)
			response.Summary.Records++
			response.Summary.Results++
			usedBytes += size + 1
			return nil
		}
		if result.Error != nil {
			item := sanitizeRecordError(*result.Error, s.opts.MaxValueBytes)
			size, err := encodedSize(item)
			if err != nil || usedBytes+size+1 > s.opts.MaxResponseBytes {
				response.Summary.Truncated = true
				return errPreviewComplete
			}
			response.Errors = append(response.Errors, item)
			response.Summary.Errors++
			response.Summary.Results++
			usedBytes += size + 1
		}
		return nil
	})
	closeErr := progress.Close()
	if processErr != nil && !errors.Is(processErr, errPreviewComplete) {
		if ctxErr := ctx.Err(); ctxErr != nil {
			logParsingFinished(logger, ctx, "parsing_failed", response.Summary, progress.bytesRead, startedAt, contextError(ctxErr).kind)
			return response, contextError(ctxErr)
		}
		if errors.Is(trackedReader.readErr, errInputByteLimit) || errors.Is(processErr, errInputByteLimit) {
			logParsingFinished(logger, ctx, "parsing_failed", response.Summary, progress.bytesRead, startedAt, errorInputTooLarge)
			return response, &serviceError{kind: errorInputTooLarge, message: "input file exceeds the configured byte limit", cause: errInputByteLimit}
		}
		if trackedReader.readErr != nil {
			logParsingFinished(logger, ctx, "parsing_failed", response.Summary, progress.bytesRead, startedAt, errorFileObject)
			return response, &serviceError{kind: errorFileObject, message: "unable to read input file from MinIO", cause: trackedReader.readErr}
		}
		response.Status = "ERROR"
		if response.Summary.Results > 0 {
			response.Status = "PARTIAL"
		}
		fitResponse(&response, s.opts.MaxResponseBytes)
		logParsingFinished(logger, ctx, "parsing_failed", response.Summary, progress.bytesRead, startedAt, errorParse)
		return response, &serviceError{kind: errorParse, message: "input file could not be parsed safely", cause: processErr}
	}
	if closeErr != nil {
		logParsingFinished(logger, ctx, "parsing_failed", response.Summary, progress.bytesRead, startedAt, errorFileObject)
		return response, &serviceError{kind: errorFileObject, message: "unable to close input file from MinIO", cause: closeErr}
	}
	if initialMetadata != nil && progress.bytesRead > initialMetadata.Size {
		logger.WarnContext(ctx, "minio_verify_failed", "error_code", string(errorObjectChanged))
		return response, &serviceError{kind: errorObjectChanged, message: "MinIO object changed while it was being read"}
	}
	if initialMetadata != nil && trackedReader.reachedEOF {
		if progress.bytesRead != initialMetadata.Size {
			logger.WarnContext(ctx, "minio_verify_failed", "error_code", string(errorObjectChanged))
			return response, &serviceError{kind: errorObjectChanged, message: "MinIO object changed while it was being read"}
		}
		verifyStartedAt := time.Now()
		logger.InfoContext(ctx, "minio_verify_started")
		finalMetadata, statErr := metadataSource.StatObject(ctx, minioPath, fileName)
		if statErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				logger.WarnContext(ctx, "minio_verify_failed", "error_code", string(contextError(ctxErr).kind), "duration_ms", elapsedMilliseconds(verifyStartedAt))
				return response, contextError(ctxErr)
			}
			logger.WarnContext(ctx, "minio_verify_failed", "error_code", string(errorFileObject), "duration_ms", elapsedMilliseconds(verifyStartedAt))
			return response, &serviceError{kind: errorFileObject, message: "unable to verify input file metadata from MinIO", cause: statErr}
		}
		if !initialMetadata.SameObject(finalMetadata) {
			logger.WarnContext(ctx, "minio_verify_failed", "error_code", string(errorObjectChanged), "duration_ms", elapsedMilliseconds(verifyStartedAt))
			return response, &serviceError{kind: errorObjectChanged, message: "MinIO object changed while it was being read"}
		}
		logger.InfoContext(ctx, "minio_object_verified", "duration_ms", elapsedMilliseconds(verifyStartedAt))
	}

	response.Status = "SUCCESS"
	if response.Summary.Errors > 0 || response.Summary.Truncated {
		response.Status = "PARTIAL"
	}
	fitResponse(&response, s.opts.MaxResponseBytes)
	logParsingFinished(logger, ctx, "parsing_completed", response.Summary, progress.bytesRead, startedAt, "")
	return response, nil
}

type progressReadCloser struct {
	io.ReadCloser
	logger    *slog.Logger
	startedAt time.Time
	lastAt    time.Time
	ctx       context.Context
	nextBytes int64
	bytesRead int64
	results   *int
	records   *int
	errors    *int
}

func (r *progressReadCloser) Read(buffer []byte) (int, error) {
	count, err := r.ReadCloser.Read(buffer)
	r.bytesRead += int64(count)
	now := time.Now()
	if r.bytesRead >= r.nextBytes || now.Sub(r.lastAt) >= progressTimeInterval {
		r.logger.InfoContext(r.ctx, "parsing_progress",
			"bytes_read", r.bytesRead,
			"results", counterValue(r.results),
			"records", counterValue(r.records),
			"parse_errors", counterValue(r.errors),
			"duration_ms", elapsedMilliseconds(r.startedAt),
		)
		r.lastAt = now
		for r.nextBytes <= r.bytesRead {
			r.nextBytes += progressByteInterval
		}
	}
	return count, err
}

func counterValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func logParsingFinished(logger *slog.Logger, ctx context.Context, event string, summary Summary, bytesRead int64, startedAt time.Time, code errorKind) {
	attrs := []any{
		"bytes_read", bytesRead,
		"results", summary.Results,
		"records", summary.Records,
		"parse_errors", summary.Errors,
		"truncated", summary.Truncated,
		"duration_ms", elapsedMilliseconds(startedAt),
	}
	if code != "" {
		attrs = append(attrs, "error_code", string(code))
		logger.WarnContext(ctx, event, attrs...)
		return
	}
	logger.InfoContext(ctx, event, attrs...)
}

func elapsedMilliseconds(startedAt time.Time) int64 {
	value := time.Since(startedAt).Milliseconds()
	if value < 0 {
		return 0
	}
	return value
}

type errorTrackingReadCloser struct {
	io.ReadCloser
	readErr    error
	reachedEOF bool
}

func (r *errorTrackingReadCloser) Read(buffer []byte) (int, error) {
	count, err := r.ReadCloser.Read(buffer)
	if errors.Is(err, io.EOF) {
		r.reachedEOF = true
	}
	if err != nil && !errors.Is(err, io.EOF) && r.readErr == nil {
		r.readErr = err
	}
	return count, err
}

type inputBoundedReadCloser struct {
	io.ReadCloser
	remaining int64
}

func (r *inputBoundedReadCloser) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	if r.remaining == 0 {
		var probe [1]byte
		count, err := r.ReadCloser.Read(probe[:])
		if count > 0 {
			return 0, errInputByteLimit
		}
		return 0, err
	}
	if int64(len(buffer)) > r.remaining {
		buffer = buffer[:r.remaining]
	}
	count, err := r.ReadCloser.Read(buffer)
	r.remaining -= int64(count)
	return count, err
}

func contextError(err error) *serviceError {
	if errors.Is(err, context.DeadlineExceeded) {
		return &serviceError{kind: errorTimeout, message: "request timed out", cause: err}
	}
	return &serviceError{kind: errorCanceled, message: "request canceled", cause: err}
}

func validateBearerToken(value string) error {
	if len(value) < minBearerTokenBytes || len(value) > maxBearerTokenBytes {
		return fmt.Errorf("preview BearerToken must contain between %d and %d bytes", minBearerTokenBytes, maxBearerTokenBytes)
	}
	for index := 0; index < len(value); index++ {
		if value[index] < 0x21 || value[index] > 0x7e {
			return fmt.Errorf("preview BearerToken must contain only visible ASCII characters without spaces")
		}
	}
	return nil
}

func validatePathPrefixes(prefixes []string) ([]string, error) {
	result := make([]string, 0, len(prefixes))
	seen := make(map[string]struct{}, len(prefixes))
	for _, prefix := range prefixes {
		if strings.TrimSpace(prefix) != prefix {
			return nil, fmt.Errorf("preview AllowedPathPrefixes entries must not have surrounding whitespace")
		}
		if err := miniogateway.ValidateReference(prefix, "validation"); err != nil {
			return nil, fmt.Errorf("preview AllowedPathPrefixes contains an invalid path: %w", err)
		}
		if _, duplicate := seen[prefix]; duplicate {
			continue
		}
		seen[prefix] = struct{}{}
		result = append(result, prefix)
	}
	return result, nil
}

func hasAllowedPathPrefix(value string, prefixes []string) bool {
	if len(prefixes) == 0 {
		return true
	}
	for _, prefix := range prefixes {
		if value == prefix || strings.HasPrefix(value, strings.TrimSuffix(prefix, "/")+"/") {
			return true
		}
	}
	return false
}

func encodedSize(value any) (int64, error) {
	encoded, err := json.Marshal(value)
	return int64(len(encoded)), err
}

func fitResponse(response *Response, maximum int64) {
	if response == nil {
		return
	}
	for {
		size, err := encodedSize(response)
		if err == nil && size+1 <= maximum {
			return
		}
		response.Summary.Truncated = true
		response.Status = "PARTIAL"
		switch {
		case len(response.Errors) > 0:
			response.Errors = response.Errors[:len(response.Errors)-1]
			response.Summary.Errors--
			response.Summary.Results--
		case len(response.Records) > 0:
			response.Records = response.Records[:len(response.Records)-1]
			response.Summary.Records--
			response.Summary.Results--
		default:
			response.FinalMinioPath = sanitizeText(response.FinalMinioPath, 128)
			response.FinalFileName = sanitizeText(response.FinalFileName, 128)
			if response.Error != nil {
				response.Error.Code = sanitizeText(response.Error.Code, 64)
				response.Error.Message = sanitizeText(response.Error.Message, 256)
			}
			size, err := encodedSize(response)
			if err == nil && size+1 <= maximum {
				return
			}
			response.FinalMinioPath = "[truncated]"
			response.FinalFileName = "[truncated]"
			if response.Error != nil {
				response.Error = &APIError{Code: sanitizeText(response.Error.Code, 32), Message: "response truncated"}
			}
			return
		}
	}
}

func sanitizeRecordError(value model.RecordError, maximum int) PreviewError {
	return PreviewError{
		RecordNumber: value.RecordNumber,
		LineNumber:   value.LineNumber,
		Field:        sanitizeText(value.Field, 256),
		Code:         sanitizeText(value.Code, 128),
		Phase:        sanitizeText(value.Phase, 128),
		Message:      sanitizeText(value.Message, 512),
		RawValue:     sanitizeValue(value.RawValue, min(maximum, 256), 0),
	}
}

func sanitizeFields(fields map[string]any, maximum int) map[string]any {
	return sanitizeFieldsAtDepth(fields, maximum, 0)
}

func sanitizeValue(value any, maximum, depth int) any {
	if depth >= maxPreviewDepth {
		return "[truncated: maximum nesting depth]"
	}
	switch typed := value.(type) {
	case nil, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, model.Date, time.Time, model.UUID:
		return typed
	case json.Number:
		if len(typed.String()) > maximum {
			return sanitizeText(typed.String(), maximum)
		}
		return typed
	case string:
		return sanitizeText(typed, maximum)
	case []byte:
		if utf8.Valid(typed) {
			return sanitizeText(string(typed), maximum)
		}
		return "[binary value omitted]"
	case json.RawMessage:
		structuredLimit := maximum * maxPreviewFields
		if structuredLimit < 4096 {
			structuredLimit = 4096
		}
		if structuredLimit > maxPreviewJSONBytes {
			structuredLimit = maxPreviewJSONBytes
		}
		if len(typed) > structuredLimit {
			return "[JSON value omitted: exceeds preview limit]"
		}
		decoder := json.NewDecoder(bytes.NewReader(typed))
		decoder.UseNumber()
		var decoded any
		if err := decoder.Decode(&decoded); err == nil {
			var trailing any
			if err := decoder.Decode(&trailing); errors.Is(err, io.EOF) {
				return sanitizeValue(decoded, maximum, depth+1)
			}
		}
		return "[invalid JSON value omitted]"
	case []string:
		count := min(len(typed), maxPreviewCollectionSize)
		result := make([]any, 0, count+1)
		for _, item := range typed[:count] {
			result = append(result, sanitizeText(item, maximum))
		}
		if count < len(typed) {
			result = append(result, "[truncated: additional elements]")
		}
		return result
	case []any:
		count := min(len(typed), maxPreviewCollectionSize)
		result := make([]any, 0, count+1)
		for _, item := range typed[:count] {
			result = append(result, sanitizeValue(item, maximum, depth+1))
		}
		if count < len(typed) {
			result = append(result, "[truncated: additional elements]")
		}
		return result
	case map[string]any:
		return sanitizeFieldsAtDepth(typed, maximum, depth+1)
	default:
		return "[unsupported value omitted]"
	}
}

func sanitizeFieldsAtDepth(fields map[string]any, maximum, depth int) map[string]any {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > maxPreviewFields {
		keys = keys[:maxPreviewFields]
	}
	result := make(map[string]any, len(keys)+1)
	for _, key := range keys {
		result[sanitizeText(key, 256)] = sanitizeValue(fields[key], maximum, depth)
	}
	if len(fields) > len(keys) {
		result["[truncated]"] = "additional fields"
	}
	return result
}

func sanitizeText(value string, maximum int) string {
	if maximum <= 0 {
		return ""
	}
	capacity := min(maximum, len(value))
	var result strings.Builder
	result.Grow(capacity)
	consumed := 0
	for consumed < len(value) {
		r, width := utf8.DecodeRuneInString(value[consumed:])
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			r = utf8.RuneError
		}
		encodedWidth := utf8.RuneLen(r)
		if encodedWidth < 0 {
			r = utf8.RuneError
			encodedWidth = utf8.RuneLen(r)
		}
		if result.Len() > maximum-encodedWidth {
			break
		}
		result.WriteRune(r)
		consumed += width
	}
	if consumed == len(value) {
		return result.String()
	}
	return result.String() + "...[truncated]"
}
