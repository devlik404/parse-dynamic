package preview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProcessLoggingReportsStagesAndNeverLeaksRequestData(t *testing.T) {
	secretPath := "rsp/atm-bersama/private-location"
	secretFile := "card-504986400000000068.raw"
	secretPayload := "PAN-504986400000000068"
	key := secretPath + "\x00" + secretFile
	source := &fakeObjectSource{objects: map[string][]byte{key: []byte(secretPayload + "\n")}}

	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelInfo}))
	handler, err := NewHandler(source, validBaseConfig(), Options{Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	recorder := performJSON(handler, `{"final_minio_path":"`+secretPath+`","final_file_name":"`+secretFile+`"}`, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status/body = %d/%s", recorder.Code, recorder.Body.String())
	}
	requestID := recorder.Header().Get("X-Request-ID")
	if len(requestID) != 32 {
		t.Fatalf("X-Request-ID = %q", requestID)
	}

	events := decodeLogEvents(t, output.Bytes())
	wantOrder := []string{"request_started", "minio_open_started", "minio_stream_opened", "parsing_started", "parsing_completed", "request_completed"}
	if len(events) != len(wantOrder) {
		t.Fatalf("events = %#v", events)
	}
	for index, want := range wantOrder {
		if events[index]["msg"] != want || events[index]["request_id"] != requestID {
			t.Fatalf("event %d = %#v, want %s/%s", index, events[index], want, requestID)
		}
	}
	completed := events[len(events)-2]
	if completed["records"] != float64(1) || completed["bytes_read"] != float64(len(secretPayload)+1) {
		t.Fatalf("parsing_completed = %#v", completed)
	}
	access := events[len(events)-1]
	if access["http_status"] != float64(http.StatusOK) {
		t.Fatalf("request_completed = %#v", access)
	}
	logs := output.String()
	for _, forbidden := range []string{secretPath, secretFile, secretPayload, "504986400000000068"} {
		if strings.Contains(logs, forbidden) {
			t.Fatalf("logs leaked %q: %s", forbidden, logs)
		}
	}
}

func TestProcessLoggingClassifiesTimeoutWithoutLeakingCause(t *testing.T) {
	secret := "upstream-secret-url-and-token"
	source := objectSourceFunc(func(ctx context.Context, _, _ string) (io.ReadCloser, error) {
		<-ctx.Done()
		return nil, errors.New(secret)
	})
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	handler := newTestHandler(t, source, validBaseConfig(), func(opts *Options) {
		opts.Logger = logger
		opts.RequestTimeout = 10 * time.Millisecond
	})
	recorder := performJSON(handler, `{"final_minio_path":"rsp/atm-bersama","final_file_name":"secret.raw"}`, "")
	if recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("status/body = %d/%s", recorder.Code, recorder.Body.String())
	}
	logs := output.String()
	if strings.Contains(logs, secret) || strings.Contains(logs, "secret.raw") {
		t.Fatalf("logs leaked request/cause: %s", logs)
	}
	if !strings.Contains(logs, `"msg":"minio_open_failed"`) || !strings.Contains(logs, `"error_code":"REQUEST_TIMEOUT"`) || !strings.Contains(logs, `"http_status":504`) {
		t.Fatalf("timeout log events missing: %s", logs)
	}
}

func TestHealthDoesNotSpamOperationalLog(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	handler, err := NewHandler(&fakeObjectSource{}, validBaseConfig(), Options{Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, HealthRoute, nil))
	if recorder.Code != http.StatusOK || output.Len() != 0 {
		t.Fatalf("status/log = %d/%q", recorder.Code, output.String())
	}
}

func decodeLogEvents(t *testing.T, input []byte) []map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(input))
	var result []map[string]any
	for {
		var item map[string]any
		if err := decoder.Decode(&item); errors.Is(err, io.EOF) {
			return result
		} else if err != nil {
			t.Fatal(err)
		}
		result = append(result, item)
	}
}
