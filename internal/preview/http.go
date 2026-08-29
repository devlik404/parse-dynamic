package preview

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

	"parser-engine/internal/config"
)

type Handler struct {
	service          *Service
	maxRequestBytes  int64
	requestTimeout   time.Duration
	concurrencySlots chan struct{}
	authEnabled      bool
	bearerDigest     [sha256.Size]byte
	logger           *slog.Logger
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorResponse struct {
	Status string   `json:"status"`
	Error  APIError `json:"error"`
}

// NewHandler returns a router containing POST /parse-file-dynamic and GET
// /healthz. Parser configuration is fixed at construction time.
func NewHandler(source ObjectSource, parserConfig config.ParserConfig, opts Options) (http.Handler, error) {
	if opts.RequireBearerAuth && opts.BearerToken == "" {
		return nil, errors.New("preview BearerToken is required when RequireBearerAuth is enabled")
	}
	if opts.BearerToken != "" {
		if err := validateBearerToken(opts.BearerToken); err != nil {
			return nil, err
		}
	}
	authEnabled := opts.BearerToken != ""
	bearerDigest := sha256.Sum256([]byte(opts.BearerToken))
	service, err := NewService(source, parserConfig, opts)
	if err != nil {
		return nil, err
	}
	handler := &Handler{
		service:          service,
		maxRequestBytes:  service.opts.MaxRequestBytes,
		requestTimeout:   service.opts.RequestTimeout,
		concurrencySlots: make(chan struct{}, service.opts.MaxConcurrentRequests),
		authEnabled:      authEnabled,
		bearerDigest:     bearerDigest,
		logger:           opts.Logger,
	}
	mux := http.NewServeMux()
	mux.HandleFunc(Route, handler.preview)
	mux.HandleFunc(HealthRoute, handler.health)
	return handler.withAccessLogging(mux), nil
}

type statusResponseWriter struct {
	http.ResponseWriter
	status int
}

func (writer *statusResponseWriter) WriteHeader(status int) {
	if writer.status != 0 {
		return
	}
	writer.status = status
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *statusResponseWriter) Write(data []byte) (int, error) {
	if writer.status == 0 {
		writer.WriteHeader(http.StatusOK)
	}
	return writer.ResponseWriter.Write(data)
}

func (writer *statusResponseWriter) Unwrap() http.ResponseWriter { return writer.ResponseWriter }

func (h *Handler) withAccessLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == HealthRoute || h.logger == nil {
			next.ServeHTTP(w, request)
			return
		}
		startedAt := time.Now()
		requestID := newRequestID()
		logger := h.logger.With("request_id", requestID)
		logger.InfoContext(request.Context(), "request_started", "method", request.Method, "route", Route)
		w.Header().Set("X-Request-ID", requestID)
		statusWriter := &statusResponseWriter{ResponseWriter: w}
		request = request.WithContext(withRequestLogger(request.Context(), logger))
		defer func() {
			status := statusWriter.status
			if status == 0 {
				status = http.StatusOK
			}
			logger.InfoContext(request.Context(), "request_completed",
				"method", request.Method,
				"route", Route,
				"http_status", status,
				"duration_ms", elapsedMilliseconds(startedAt),
			)
		}()
		next.ServeHTTP(statusWriter, request)
	})
}

func (h *Handler) health(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Status: "ERROR", Error: APIError{Code: "METHOD_NOT_ALLOWED", Message: "method not allowed"}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "UP"})
}

func (h *Handler) preview(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Status: "ERROR", Error: APIError{Code: "METHOD_NOT_ALLOWED", Message: "method not allowed"}})
		return
	}
	if !h.authorized(request.Header.Get("Authorization")) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="parser-preview"`)
		writeJSON(w, http.StatusUnauthorized, errorResponse{Status: "ERROR", Error: APIError{Code: "UNAUTHORIZED", Message: "valid bearer authentication is required"}})
		return
	}
	select {
	case h.concurrencySlots <- struct{}{}:
		defer func() { <-h.concurrencySlots }()
	default:
		w.Header().Set("Retry-After", "1")
		writeJSON(w, http.StatusTooManyRequests, errorResponse{Status: "ERROR", Error: APIError{Code: "TOO_MANY_REQUESTS", Message: "preview concurrency limit reached"}})
		return
	}

	ctx, cancel := context.WithTimeout(request.Context(), h.requestTimeout)
	defer cancel()
	request = request.WithContext(ctx)

	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		writeJSON(w, http.StatusUnsupportedMediaType, errorResponse{Status: "ERROR", Error: APIError{Code: "UNSUPPORTED_MEDIA_TYPE", Message: "Content-Type must be application/json"}})
		return
	}

	request.Body = http.MaxBytesReader(w, request.Body, h.maxRequestBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var body Request
	if err := decoder.Decode(&body); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errorResponse{Status: "ERROR", Error: APIError{Code: "REQUEST_TOO_LARGE", Message: "request body exceeds the configured byte limit"}})
			return
		}
		writeJSON(w, http.StatusBadRequest, errorResponse{Status: "ERROR", Error: APIError{Code: "INVALID_JSON", Message: "request body must be one valid JSON object with known fields"}})
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errorResponse{Status: "ERROR", Error: APIError{Code: "REQUEST_TOO_LARGE", Message: "request body exceeds the configured byte limit"}})
			return
		}
		writeJSON(w, http.StatusBadRequest, errorResponse{Status: "ERROR", Error: APIError{Code: "INVALID_JSON", Message: "request body must contain exactly one JSON object"}})
		return
	}

	response, err := h.service.Preview(request.Context(), body)
	if err == nil {
		writeJSON(w, http.StatusOK, response)
		return
	}
	var typed *serviceError
	if !errors.As(err, &typed) {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Status: "ERROR", Error: APIError{Code: "INTERNAL_ERROR", Message: "preview failed"}})
		return
	}
	status := http.StatusInternalServerError
	switch typed.kind {
	case errorInvalidRequest:
		status = http.StatusBadRequest
	case errorParse:
		status = http.StatusUnprocessableEntity
	case errorFileObject:
		status = http.StatusBadGateway
	case errorObjectChanged:
		status = http.StatusConflict
	case errorInputTooLarge:
		status = http.StatusRequestEntityTooLarge
	case errorCanceled:
		status = http.StatusServiceUnavailable
	case errorTimeout:
		status = http.StatusGatewayTimeout
	}
	if typed.kind == errorParse && response.Summary.Results > 0 {
		w.Header().Set("X-Preview-Error-Code", string(typed.kind))
		response.Error = &APIError{Code: string(typed.kind), Message: typed.message}
		fitResponse(&response, h.service.opts.MaxResponseBytes)
		writeJSON(w, status, response)
		return
	}
	writeJSON(w, status, errorResponse{Status: "ERROR", Error: APIError{Code: string(typed.kind), Message: sanitizeText(typed.message, 512)}})
}

func (h *Handler) authorized(header string) bool {
	if !h.authEnabled {
		return true
	}
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" || strings.ContainsAny(token, " \t\r\n") {
		return false
	}
	digest := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(digest[:], h.bearerDigest[:]) == 1
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
