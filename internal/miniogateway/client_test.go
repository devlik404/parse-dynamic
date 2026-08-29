package miniogateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenStreamsExactEscapedGatewayURL(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		wantURI := "/gateway%20root/rsp-reopsc/daily%20reports/2026%2008/ATM%20report%20%231%25.txt"
		if got := r.URL.RequestURI(); got != wantURI {
			t.Errorf("RequestURI = %q, want %q", got, wantURI)
		}
		if got := r.Header.Get("Accept"); got != "application/octet-stream" {
			t.Errorf("Accept = %q, want application/octet-stream", got)
		}
		_, _ = io.WriteString(w, "streamed payload")
	})
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		response := recorder.Result()
		response.Request = req
		return response, nil
	})

	client, err := New(Config{
		BaseURL:    "http://minio-gateway.test/gateway%20root/",
		BucketName: "rsp-reopsc",
		Timeout:    time.Second,
		Transport:  transport,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	body, err := client.Open(context.Background(), "daily reports/2026 08", "ATM report #1%.txt")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer body.Close()
	payload, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if got, want := string(payload), "streamed payload"; got != want {
		t.Fatalf("payload = %q, want %q", got, want)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("request calls = %d, want one direct GET", got)
	}
}

func TestNewUsesDefaultTimeoutAndTransportSeam(t *testing.T) {
	t.Parallel()

	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("ok")),
			Request:    req,
		}, nil
	})
	client, err := New(Config{
		BaseURL:    "http://minio-gateway.internal:8080",
		BucketName: "rsp-reopsc",
		Transport:  transport,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if client.httpClient.Timeout != defaultHTTPTimeout {
		t.Fatalf("timeout = %v, want %v", client.httpClient.Timeout, defaultHTTPTimeout)
	}
	if client.httpClient.Transport == nil {
		t.Fatal("custom transport was not installed")
	}
}

func TestConfigValidationIsStrictAndDoesNotReflectValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  Config
	}{
		{name: "missing", cfg: Config{}},
		{name: "unsupported scheme", cfg: Config{BaseURL: "ftp://private-host/x", BucketName: "bucket"}},
		{name: "relative URL", cfg: Config{BaseURL: "private-host:8080", BucketName: "bucket"}},
		{name: "userinfo", cfg: Config{BaseURL: "http://private-user:private-pass@host", BucketName: "bucket"}},
		{name: "query", cfg: Config{BaseURL: "http://host?token=private-token", BucketName: "bucket"}},
		{name: "fragment", cfg: Config{BaseURL: "http://host#private-fragment", BucketName: "bucket"}},
		{name: "base traversal", cfg: Config{BaseURL: "http://host/api/../private", BucketName: "bucket"}},
		{name: "bucket separator", cfg: Config{BaseURL: "http://host", BucketName: "private/bucket"}},
		{name: "negative timeout", cfg: Config{BaseURL: "http://host", BucketName: "bucket", Timeout: -time.Second}},
		{name: "excessive timeout", cfg: Config{BaseURL: "http://host", BucketName: "bucket", Timeout: maximumHTTPTimeout + time.Second}},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := test.cfg.Validate()
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("Validate() error = %v, want ErrInvalidConfig", err)
			}
			message := err.Error()
			for _, sensitive := range []string{"private-host", "private-user", "private-pass", "private-token", "private-fragment", "private/bucket"} {
				if strings.Contains(message, sensitive) {
					t.Fatalf("validation error leaked %q: %q", sensitive, message)
				}
			}
		})
	}
}

func TestValidateReferenceRejectsTraversalControlsAndSeparators(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		minioPath string
		fileName  string
	}{
		{name: "empty path", minioPath: "", fileName: "file.txt"},
		{name: "absolute path", minioPath: "/private", fileName: "file.txt"},
		{name: "trailing slash", minioPath: "input/", fileName: "file.txt"},
		{name: "empty segment", minioPath: "input//private", fileName: "file.txt"},
		{name: "dot segment", minioPath: "input/./private", fileName: "file.txt"},
		{name: "parent segment", minioPath: "input/../private", fileName: "file.txt"},
		{name: "encoded parent", minioPath: "input/%2e%2e/private", fileName: "file.txt"},
		{name: "double encoded parent", minioPath: "input/%252e%252e/private", fileName: "file.txt"},
		{name: "backslash path", minioPath: `input\private`, fileName: "file.txt"},
		{name: "control path", minioPath: "input\nprivate", fileName: "file.txt"},
		{name: "empty file", minioPath: "input", fileName: ""},
		{name: "file slash", minioPath: "input", fileName: "private/file.txt"},
		{name: "encoded file slash", minioPath: "input", fileName: "private%2ffile.txt"},
		{name: "parent file", minioPath: "input", fileName: ".."},
		{name: "control file", minioPath: "input", fileName: "private\r.txt"},
		{name: "invalid UTF-8", minioPath: "input", fileName: string([]byte{0xff, '.', 't', 'x', 't'})},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateReference(test.minioPath, test.fileName)
			if !errors.Is(err, ErrInvalidReference) {
				t.Fatalf("ValidateReference() error = %v, want ErrInvalidReference", err)
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatalf("reference error reflected rejected value: %q", err)
			}
		})
	}
}

func TestOpenRejectsInvalidReferenceWithoutNetworkCall(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	client, err := New(Config{
		BaseURL:    "http://minio-gateway.internal:8080",
		BucketName: "rsp-reopsc",
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return nil, errors.New("must not be called")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := client.Open(context.Background(), "input/../secret", "file.txt"); !errors.Is(err, ErrInvalidReference) {
		t.Fatalf("Open() error = %v, want ErrInvalidReference", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("transport calls = %d, want 0", got)
	}
}

func TestOpenReturnsCallerOwnedSuccessBody(t *testing.T) {
	t.Parallel()

	body := &trackingBody{Reader: strings.NewReader("payload")}
	client := mustClientWithTransport(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       body,
			Request:    req,
		}, nil
	}))

	stream, err := client.Open(context.Background(), "input", "file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if body.closed {
		t.Fatal("Open() closed a successful caller-owned body")
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if !body.closed {
		t.Fatal("Close() did not close the response body")
	}
}

func TestStatObjectReadsStableIdentityWithHEAD(t *testing.T) {
	t.Parallel()

	body := &trackingBody{Reader: strings.NewReader("")}
	client := mustClientWithTransport(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodHead {
			t.Errorf("method = %s, want HEAD", req.Method)
		}
		if got, want := req.URL.RequestURI(), "/rsp-reopsc/input/file.txt"; got != want {
			t.Errorf("RequestURI = %q, want %q", got, want)
		}
		header := make(http.Header)
		header.Set("ETag", `"etag-123"`)
		header.Set("Content-Length", "42")
		header.Set("x-amz-version-id", "version-7")
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: body, Request: req}, nil
	}))

	metadata, err := client.StatObject(context.Background(), "input", "file.txt")
	if err != nil {
		t.Fatalf("StatObject() error = %v", err)
	}
	if want := (ObjectMetadata{ETag: "etag-123", VersionID: "version-7", Size: 42}); metadata != want {
		t.Fatalf("metadata = %#v, want %#v", metadata, want)
	}
	if !body.closed {
		t.Fatal("StatObject() did not close the HEAD response body")
	}
}

func TestStatObjectRejectsMissingOrInvalidIdentity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		etag   string
		length string
	}{
		{name: "missing etag", length: "1"},
		{name: "missing length", etag: "etag"},
		{name: "invalid length", etag: "etag", length: "NaN"},
		{name: "negative length", etag: "etag", length: "-1"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client := mustClientWithTransport(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
				header := make(http.Header)
				header.Set("ETag", test.etag)
				header.Set("Content-Length", test.length)
				return &http.Response{StatusCode: http.StatusOK, Header: header, Body: http.NoBody, Request: req}, nil
			}))
			_, err := client.StatObject(context.Background(), "input", "file.txt")
			if !errors.Is(err, ErrInvalidMetadata) {
				t.Fatalf("StatObject() error = %v, want ErrInvalidMetadata", err)
			}
		})
	}
}

func TestOpenObjectSendsRangeAndRequiresPartialContent(t *testing.T) {
	t.Parallel()

	client := mustClientWithTransport(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("Range"); got != "bytes=128-" {
			t.Errorf("Range = %q, want bytes=128-", got)
		}
		return &http.Response{
			StatusCode: http.StatusPartialContent,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("resumed")),
			Request:    req,
		}, nil
	}))
	body, err := client.OpenObject(context.Background(), "input", "file.txt", 128)
	if err != nil {
		t.Fatalf("OpenObject() error = %v", err)
	}
	defer body.Close()
	payload, err := io.ReadAll(body)
	if err != nil || string(payload) != "resumed" {
		t.Fatalf("payload = %q, error = %v", payload, err)
	}
}

func TestOpenObjectRejectsIgnoredRangeAndNegativeOffset(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	body := &trackingBody{Reader: strings.NewReader("full object")}
	client := mustClientWithTransport(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body, Request: req}, nil
	}))
	stream, err := client.OpenObject(context.Background(), "input", "file.txt", 12)
	if stream != nil || !errors.Is(err, ErrRangeNotSupported) {
		t.Fatalf("OpenObject() stream = %v, error = %v", stream, err)
	}
	if !body.closed {
		t.Fatal("ignored Range response body was not closed")
	}
	if _, err := client.OpenObject(context.Background(), "input", "file.txt", -1); !errors.Is(err, ErrInvalidReference) {
		t.Fatalf("negative offset error = %v, want ErrInvalidReference", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("transport calls = %d, want 1", got)
	}
}

func TestOpenClosesAndBoundsUnexpectedStatusBody(t *testing.T) {
	t.Parallel()

	secret := "private-upstream-payload"
	payload := secret + strings.Repeat("x", maxErrorDrainBytes*3)
	body := &trackingBody{Reader: strings.NewReader(payload)}
	client := mustClientWithTransport(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Header:     make(http.Header),
			Body:       body,
			Request:    req,
		}, nil
	}))

	stream, err := client.Open(context.Background(), "input", "file.txt")
	if stream != nil {
		t.Fatal("Open() returned a body for an error response")
	}
	if !errors.Is(err, ErrUnexpectedStatus) {
		t.Fatalf("Open() error = %v, want ErrUnexpectedStatus", err)
	}
	var statusErr *StatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("Open() error = %#v, want HTTP 500 StatusError", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked response payload: %q", err)
	}
	if !body.closed {
		t.Fatal("error response body was not closed")
	}
	if body.bytesRead > maxErrorDrainBytes {
		t.Fatalf("error body bytes read = %d, maximum %d", body.bytesRead, maxErrorDrainBytes)
	}
}

func TestOpenMaps404WithoutSeparateExistenceRequest(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	client := mustClientWithTransport(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("sensitive object details")),
			Request:    req,
		}, nil
	}))

	_, err := client.Open(context.Background(), "input", "missing.txt")
	if !errors.Is(err, ErrObjectNotFound) || !errors.Is(err, ErrUnexpectedStatus) {
		t.Fatalf("Open() error = %v, want not-found and unexpected-status identities", err)
	}
	if strings.Contains(err.Error(), "sensitive") || strings.Contains(err.Error(), "missing.txt") {
		t.Fatalf("error leaked request or response data: %q", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("transport calls = %d, want one direct GET", got)
	}
}

func TestOpenPropagatesContextCancellation(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	client := mustClientWithTransport(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		close(started)
		<-req.Context().Done()
		return nil, req.Context().Err()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := client.Open(ctx, "input", "file.txt")
		result <- err
	}()
	<-started
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Open() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Open() did not stop after context cancellation")
	}
}

func TestOpenEnforcesConfiguredHTTPTimeout(t *testing.T) {
	t.Parallel()

	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	client, err := New(Config{
		BaseURL:    "http://minio-gateway.internal:8080",
		BucketName: "rsp-reopsc",
		Timeout:    10 * time.Millisecond,
		Transport:  transport,
	})
	if err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() {
		_, openErr := client.Open(context.Background(), "input", "file.txt")
		result <- openErr
	}()
	select {
	case openErr := <-result:
		if !errors.Is(openErr, ErrRequestFailed) || !errors.Is(openErr, context.DeadlineExceeded) {
			t.Fatalf("Open() error = %v, want request failure caused by deadline", openErr)
		}
	case <-time.After(time.Second):
		t.Fatal("Open() did not enforce configured HTTP timeout")
	}
}

func TestOpenHidesTransportErrorURLButPreservesCauseIdentity(t *testing.T) {
	t.Parallel()

	cause := errors.New("dial failed at http://private-host/private-object")
	client := mustClientWithTransport(t, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, cause
	}))

	_, err := client.Open(context.Background(), "input", "secret-file.txt")
	if !errors.Is(err, ErrRequestFailed) || !errors.Is(err, cause) {
		t.Fatalf("Open() error = %v, want request failure preserving cause identity", err)
	}
	if strings.Contains(err.Error(), "private-host") || strings.Contains(err.Error(), "secret-file") {
		t.Fatalf("transport error leaked URL details: %q", err)
	}
}

func TestOpenDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()

	var destinationCalls atomic.Int32
	var redirectorCalls atomic.Int32
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "redirect-destination.test" {
			destinationCalls.Add(1)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("unexpected")),
				Request:    req,
			}, nil
		}
		redirectorCalls.Add(1)
		header := make(http.Header)
		header.Set("Location", "http://redirect-destination.test/object")
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader("redirect")),
			Request:    req,
		}, nil
	})

	client, err := New(Config{
		BaseURL:    "http://redirect-source.test",
		BucketName: "rsp-reopsc",
		Transport:  transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Open(context.Background(), "input", "file.txt")
	if !errors.Is(err, ErrUnexpectedStatus) {
		t.Fatalf("Open() error = %v, want redirect status error", err)
	}
	if got := destinationCalls.Load(); got != 0 {
		t.Fatalf("redirect destination calls = %d, want 0", got)
	}
	if got := redirectorCalls.Load(); got != 1 {
		t.Fatalf("redirect source calls = %d, want 1", got)
	}
}

func mustClientWithTransport(t *testing.T, transport http.RoundTripper) *Client {
	t.Helper()
	client, err := New(Config{
		BaseURL:    "http://minio-gateway.internal:8080",
		BucketName: "rsp-reopsc",
		Timeout:    time.Second,
		Transport:  transport,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return client
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type trackingBody struct {
	io.Reader
	bytesRead int
	closed    bool
}

func (b *trackingBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.bytesRead += n
	return n, err
}

func (b *trackingBody) Close() error {
	b.closed = true
	return nil
}
