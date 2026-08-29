package dynamicjob

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"
)

type Executor interface {
	Execute(ctx context.Context, request Request) (Result, error)
}

type HandlerOptions struct {
	MaxRequestBytes       int64
	RequestTimeout        time.Duration
	MaxConcurrentRequests int
	RequireBearerAuth     bool
	BearerToken           string
	Logger                *slog.Logger
}

type Handler struct {
	executor    Executor
	opts        HandlerOptions
	slots       chan struct{}
	authEnabled bool
	tokenDigest [sha256.Size]byte
}

func NewHandler(executor Executor, opts HandlerOptions) (http.Handler, error) {
	if executor == nil {
		return nil, errors.New("dynamic job executor is required")
	}
	if opts.MaxRequestBytes <= 0 {
		opts.MaxRequestBytes = 16 * 1024
	}
	if opts.RequestTimeout <= 0 {
		opts.RequestTimeout = 10 * time.Minute
	}
	if opts.MaxConcurrentRequests <= 0 {
		opts.MaxConcurrentRequests = 4
	}
	if opts.RequireBearerAuth && opts.BearerToken == "" {
		return nil, errors.New("scheduler bearer token is required when authentication is enabled")
	}
	if opts.BearerToken != "" && len(opts.BearerToken) < 32 {
		return nil, errors.New("scheduler bearer token must contain at least 32 bytes")
	}
	authEnabled := opts.RequireBearerAuth || opts.BearerToken != ""
	handler := &Handler{
		executor: executor, opts: opts,
		slots:       make(chan struct{}, opts.MaxConcurrentRequests),
		authEnabled: authEnabled, tokenDigest: sha256.Sum256([]byte(opts.BearerToken)),
	}
	mux := http.NewServeMux()
	mux.HandleFunc(Route, handler.execute)
	mux.HandleFunc(HealthRoute, handler.health)
	return mux, nil
}

func (h *Handler) health(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeResponse(w, http.StatusMethodNotAllowed, Response{Status: false, Message: "method not allowed"})
		return
	}
	writeResponse(w, http.StatusOK, map[string]string{"status": "UP"})
}

func (h *Handler) execute(w http.ResponseWriter, request *http.Request) {
	startedAt := time.Now()
	if request.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeResponse(w, http.StatusMethodNotAllowed, Response{Status: false, Message: "method not allowed"})
		return
	}
	if h.authEnabled && !h.authorized(request.Header.Get("Authorization")) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="dynamic-parser"`)
		writeResponse(w, http.StatusUnauthorized, Response{Status: false, Message: "authentication required"})
		return
	}
	contentType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		writeResponse(w, http.StatusUnsupportedMediaType, Response{Status: false, Message: "Content-Type must be application/json"})
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		writeResponse(w, http.StatusTooManyRequests, Response{Status: false, Message: "too many dynamic parse requests"})
		return
	}

	request.Body = http.MaxBytesReader(w, request.Body, h.opts.MaxRequestBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var body Request
	if err := decoder.Decode(&body); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeResponse(w, http.StatusRequestEntityTooLarge, Response{Status: false, Message: "request body exceeds the configured byte limit"})
			return
		}
		writeResponse(w, http.StatusBadRequest, Response{Status: false, Message: "request body must be one valid JSON object"})
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeResponse(w, http.StatusBadRequest, Response{Status: false, Message: "request body must contain exactly one JSON object"})
		return
	}

	ctx, cancel := context.WithTimeout(request.Context(), h.opts.RequestTimeout)
	defer cancel()
	result, executeErr := h.executor.Execute(ctx, body)
	if executeErr == nil {
		h.log(ctx, slog.LevelInfo, "dynamic_parse_completed", body, time.Since(startedAt), "", result.TotalData)
		writeResponse(w, http.StatusOK, Response{Status: true, Message: "", TotalData: result.TotalData})
		return
	}
	var jobErr *JobError
	if !errors.As(executeErr, &jobErr) {
		jobErr = &JobError{Kind: ErrorDatabase, Message: "dynamic parse job failed", Cause: executeErr}
	}
	h.log(ctx, slog.LevelError, "dynamic_parse_failed", body, time.Since(startedAt), jobErr.Kind, 0)
	writeResponse(w, statusFor(jobErr.Kind), Response{Status: false, Message: jobErr.Message})
}

func (h *Handler) authorized(header string) bool {
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" || strings.ContainsAny(token, " \t\r\n") {
		return false
	}
	digest := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(digest[:], h.tokenDigest[:]) == 1
}

func (h *Handler) log(ctx context.Context, level slog.Level, event string, request Request, duration time.Duration, kind ErrorKind, total int64) {
	if h.opts.Logger == nil {
		return
	}
	attrs := []any{
		"event", event,
		"la_num", request.LANum,
		"product_id", request.ProductID,
		"task", request.Task,
		"activity", request.Activity,
		"duration_ms", duration.Milliseconds(),
		"total_data", total,
	}
	if kind != "" {
		attrs = append(attrs, "error_code", string(kind))
	}
	h.opts.Logger.Log(ctx, level, event, attrs...)
}

func statusFor(kind ErrorKind) int {
	switch kind {
	case ErrorInvalidRequest:
		return http.StatusBadRequest
	case ErrorFileNotFound:
		return http.StatusNotFound
	case ErrorInputTooLarge:
		return http.StatusRequestEntityTooLarge
	case ErrorParse, ErrorConfig:
		return http.StatusUnprocessableEntity
	case ErrorObjectChanged:
		return http.StatusConflict
	case ErrorFileObject:
		return http.StatusBadGateway
	case ErrorTimeout:
		return http.StatusGatewayTimeout
	case ErrorCanceled:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

func writeResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
