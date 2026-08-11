// Package miniogateway provides read-only, streaming access to the HTTP
// object gateway used by cashrecon-sch-parse-file-atm-bersama.
package miniogateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	defaultHTTPTimeout = 5 * time.Minute
	maximumHTTPTimeout = 24 * time.Hour

	maxBaseURLBytes  = 2048
	maxBucketBytes   = 255
	maxObjectPathLen = 2048
	maxFileNameLen   = 1024

	// Error response payloads are never returned to callers. Reading a small,
	// bounded prefix lets net/http reuse connections for ordinary short error
	// responses without allowing an upstream to make error handling unbounded.
	maxErrorDrainBytes = 4 << 10
)

var (
	// ErrInvalidConfig identifies an invalid HTTP gateway configuration.
	ErrInvalidConfig = errors.New("invalid MinIO gateway configuration")
	// ErrInvalidReference identifies an unsafe object path or file name.
	ErrInvalidReference = errors.New("invalid MinIO object reference")
	// ErrRequestFailed identifies a failure before a usable HTTP response was
	// received. The public error text intentionally excludes the target URL.
	ErrRequestFailed = errors.New("MinIO gateway request failed")
	// ErrUnexpectedStatus identifies any non-2xx gateway response.
	ErrUnexpectedStatus = errors.New("MinIO gateway returned an unexpected status")
	// ErrObjectNotFound additionally identifies an HTTP 404 response.
	ErrObjectNotFound = errors.New("MinIO object not found")
)

// Config describes the HTTP object gateway used by the existing cashrecon
// job. BaseURL may include a fixed path prefix, but must not contain userinfo,
// a query, or a fragment. A zero Timeout uses a safe default.
type Config struct {
	BaseURL    string
	BucketName string
	Timeout    time.Duration

	// Transport is optional and primarily provides a deterministic integration
	// seam. Production callers normally leave it nil.
	Transport http.RoundTripper
}

// ConfigError lists only field names and invariant failures. It never copies
// configuration values into an error that could cross an HTTP boundary.
type ConfigError struct {
	Problems []string
}

func (e *ConfigError) Error() string {
	if e == nil || len(e.Problems) == 0 {
		return ErrInvalidConfig.Error()
	}
	return ErrInvalidConfig.Error() + ":\n - " + strings.Join(e.Problems, "\n - ")
}

func (e *ConfigError) Unwrap() error { return ErrInvalidConfig }

// ReferenceError intentionally omits the rejected value.
type ReferenceError struct {
	Field   string
	Problem string
}

func (e *ReferenceError) Error() string {
	if e == nil {
		return ErrInvalidReference.Error()
	}
	return fmt.Sprintf("%s: %s %s", ErrInvalidReference, e.Field, e.Problem)
}

func (e *ReferenceError) Unwrap() error { return ErrInvalidReference }

// StatusError exposes only the numeric status. In particular, an upstream
// response body (which may contain payload data or implementation details) is
// never reflected in this error.
type StatusError struct {
	StatusCode int
}

func (e *StatusError) Error() string {
	if e == nil {
		return ErrUnexpectedStatus.Error()
	}
	return fmt.Sprintf("%s: HTTP %d", ErrUnexpectedStatus, e.StatusCode)
}

func (e *StatusError) Is(target error) bool {
	if target == ErrUnexpectedStatus {
		return true
	}
	return e != nil && e.StatusCode == http.StatusNotFound && target == ErrObjectNotFound
}

// RequestError retains a cause for errors.Is/errors.As without including the
// cause's possibly sensitive URL in Error().
type RequestError struct {
	cause error
}

func (e *RequestError) Error() string { return ErrRequestFailed.Error() }

func (e *RequestError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *RequestError) Is(target error) bool { return target == ErrRequestFailed }

// Client is a read-only client for the cashrecon HTTP object gateway.
type Client struct {
	baseURL    url.URL
	bucketName string
	httpClient *http.Client
}

// Validate checks configuration without performing a network request.
func (cfg Config) Validate() error {
	var problems []string
	add := func(problem string) { problems = append(problems, problem) }

	if _, err := parseBaseURL(cfg.BaseURL); err != nil {
		add(err.Error())
	}
	if err := validateBucket(cfg.BucketName); err != nil {
		add(err.Error())
	}
	if cfg.Timeout < 0 {
		add("MINIO_HTTP_TIMEOUT must not be negative")
	} else if cfg.Timeout > maximumHTTPTimeout {
		add("MINIO_HTTP_TIMEOUT must not exceed 24 hours")
	}

	if len(problems) != 0 {
		return &ConfigError{Problems: problems}
	}
	return nil
}

// New creates a client without performing a network request.
func New(cfg Config) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	baseURL, err := parseBaseURL(cfg.BaseURL)
	if err != nil {
		// Validate above guarantees this branch is unreachable, but keep the
		// constructor total if validation and parsing ever evolve separately.
		return nil, &ConfigError{Problems: []string{err.Error()}}
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultHTTPTimeout
	}

	return &Client{
		baseURL:    baseURL,
		bucketName: cfg.BucketName,
		httpClient: &http.Client{
			Timeout:   timeout,
			Transport: cfg.Transport,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// Open streams one object using the same URL contract as the existing job:
// GET ${MINIO_BASE_URL}/${MINIO_BUCKET_NAME}/${minioPath}/${fileName}.
// The caller owns the returned body and must close it. Open deliberately sends
// no separate list/existence request; HTTP 404 is reported as ErrObjectNotFound.
func (c *Client) Open(ctx context.Context, minioPath, fileName string) (io.ReadCloser, error) {
	if c == nil || c.httpClient == nil {
		return nil, &RequestError{cause: errors.New("gateway client is not initialized")}
	}
	if ctx == nil {
		return nil, &ReferenceError{Field: "context", Problem: "must not be nil"}
	}
	if err := ValidateReference(minioPath, fileName); err != nil {
		return nil, err
	}

	objectURL := c.objectURL(minioPath, fileName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, objectURL, nil)
	if err != nil {
		return nil, &RequestError{cause: err}
	}
	req.Header.Set("Accept", "application/octet-stream")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, &RequestError{cause: err}
	}
	if resp == nil || resp.Body == nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, &RequestError{cause: errors.New("gateway returned an invalid response")}
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		discardAndClose(resp.Body)
		return nil, &StatusError{StatusCode: resp.StatusCode}
	}

	return resp.Body, nil
}

// ValidateReference checks the two request-derived URL components without
// reflecting their contents in errors.
func ValidateReference(minioPath, fileName string) error {
	if err := validateObjectPath(minioPath); err != nil {
		return err
	}
	return validateFileName(fileName)
}

func (c *Client) objectURL(minioPath, fileName string) string {
	u := c.baseURL
	escapedPrefix := strings.TrimRight(u.EscapedPath(), "/")
	segments := make([]string, 0, 2+strings.Count(minioPath, "/"))
	segments = append(segments, c.bucketName)
	segments = append(segments, strings.Split(minioPath, "/")...)
	segments = append(segments, fileName)

	decodedSuffix := "/" + strings.Join(segments, "/")
	escapedSegments := make([]string, len(segments))
	for i, segment := range segments {
		escapedSegments[i] = url.PathEscape(segment)
	}
	escapedSuffix := "/" + strings.Join(escapedSegments, "/")

	u.Path = strings.TrimRight(u.Path, "/") + decodedSuffix
	u.RawPath = escapedPrefix + escapedSuffix
	return u.String()
}

func discardAndClose(body io.ReadCloser) {
	_, _ = io.CopyN(io.Discard, body, maxErrorDrainBytes)
	_ = body.Close()
}

func parseBaseURL(value string) (url.URL, error) {
	fail := func(problem string) (url.URL, error) {
		return url.URL{}, errors.New("MINIO_BASE_URL " + problem)
	}
	if value == "" {
		return fail("is required")
	}
	if len(value) > maxBaseURLBytes {
		return fail("is too long")
	}
	if !utf8.ValidString(value) {
		return fail("must contain valid UTF-8")
	}
	if strings.TrimSpace(value) != value {
		return fail("must not have surrounding whitespace")
	}
	if containsControl(value) {
		return fail("must not contain control characters")
	}
	if strings.Contains(value, "\\") {
		return fail("must not contain backslashes")
	}

	parsed, err := url.Parse(value)
	if err != nil {
		return fail("must be a valid absolute HTTP URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fail("scheme must be http or https")
	}
	if parsed.Opaque != "" || parsed.Host == "" || parsed.Hostname() == "" {
		return fail("must be an absolute URL with a host")
	}
	if parsed.User != nil {
		return fail("must not contain userinfo")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return fail("must not contain a query")
	}
	if parsed.Fragment != "" {
		return fail("must not contain a fragment")
	}
	if containsControl(parsed.Host) || strings.ContainsAny(parsed.Host, " /?#") {
		return fail("host is invalid")
	}
	if err := validateBasePath(parsed.Path); err != nil {
		return fail(err.Error())
	}

	// Canonicalize only trailing separators. Path contents and an encoded base
	// prefix remain intact when object segments are appended.
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = strings.TrimRight(parsed.EscapedPath(), "/")
	return *parsed, nil
}

func validateBasePath(value string) error {
	trimmed := strings.Trim(value, "/")
	if trimmed == "" {
		return nil
	}
	for _, segment := range strings.Split(trimmed, "/") {
		if segment == "" {
			return errors.New("path prefix must not contain empty segments")
		}
		if segment == "." || segment == ".." || hasEncodedTraversal(segment) {
			return errors.New("path prefix must not contain traversal segments")
		}
	}
	return nil
}

func validateBucket(value string) error {
	if value == "" {
		return errors.New("MINIO_BUCKET_NAME is required")
	}
	if len(value) > maxBucketBytes {
		return errors.New("MINIO_BUCKET_NAME is too long")
	}
	if !utf8.ValidString(value) {
		return errors.New("MINIO_BUCKET_NAME must contain valid UTF-8")
	}
	if strings.TrimSpace(value) != value {
		return errors.New("MINIO_BUCKET_NAME must not have surrounding whitespace")
	}
	if containsControl(value) || strings.ContainsAny(value, "/\\") {
		return errors.New("MINIO_BUCKET_NAME must be one safe path segment")
	}
	if value == "." || value == ".." || hasEncodedTraversal(value) {
		return errors.New("MINIO_BUCKET_NAME must not contain traversal")
	}
	return nil
}

func validateObjectPath(value string) error {
	fail := func(problem string) error {
		return &ReferenceError{Field: "minioPath", Problem: problem}
	}
	if value == "" {
		return fail("is required")
	}
	if len(value) > maxObjectPathLen {
		return fail("is too long")
	}
	if !utf8.ValidString(value) {
		return fail("must contain valid UTF-8")
	}
	if strings.TrimSpace(value) != value {
		return fail("must not have surrounding whitespace")
	}
	if containsControl(value) || strings.Contains(value, "\\") {
		return fail("contains an unsafe character")
	}
	if strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") {
		return fail("must be relative and must not end with a separator")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" {
			return fail("must not contain an empty segment")
		}
		if strings.TrimSpace(segment) != segment {
			return fail("segments must not have surrounding whitespace")
		}
		if segment == "." || segment == ".." || hasEncodedTraversal(segment) {
			return fail("must not contain traversal segments")
		}
	}
	return nil
}

func validateFileName(value string) error {
	fail := func(problem string) error {
		return &ReferenceError{Field: "fileName", Problem: problem}
	}
	if value == "" {
		return fail("is required")
	}
	if len(value) > maxFileNameLen {
		return fail("is too long")
	}
	if !utf8.ValidString(value) {
		return fail("must contain valid UTF-8")
	}
	if strings.TrimSpace(value) != value {
		return fail("must not have surrounding whitespace")
	}
	if containsControl(value) || strings.ContainsAny(value, "/\\") {
		return fail("must be one safe path segment")
	}
	if value == "." || value == ".." || hasEncodedTraversal(value) {
		return fail("must not contain traversal")
	}
	return nil
}

func containsControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// hasEncodedTraversal protects deployments where an intermediary performs an
// extra path-unescape. Each successful pass strictly shortens an encoded
// value, so this loop is bounded by the already-small component length.
func hasEncodedTraversal(value string) bool {
	decoded := value
	for range len(value) + 1 {
		next, err := url.PathUnescape(decoded)
		if err != nil || next == decoded {
			return false
		}
		if next == "." || next == ".." || strings.ContainsAny(next, "/\\") || containsControl(next) {
			return true
		}
		decoded = next
	}
	return false
}
