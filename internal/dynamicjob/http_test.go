package dynamicjob

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type executorFunc func(context.Context, Request) (Result, error)

func (f executorFunc) Execute(ctx context.Context, request Request) (Result, error) {
	return f(ctx, request)
}

const schedulerRequestJSON = `{
  "final_file_name":"input.txt",
  "final_minio_path":"PRODUCT/202608",
  "file_date":"2026-08-29",
  "la_num":42,
  "product_id":"PRODUCT",
  "task":"PARSE_FILE",
  "activity":"RECON"
}`

func performRequest(handler http.Handler, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, Route, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestHandlerReturnsSchedulerCompatibleSuccess(t *testing.T) {
	var received Request
	handler, err := NewHandler(executorFunc(func(_ context.Context, request Request) (Result, error) {
		received = request
		return Result{TotalData: 1250}, nil
	}), HandlerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	recorder := performRequest(handler, schedulerRequestJSON)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status/body = %d/%s", recorder.Code, recorder.Body.String())
	}
	if got, want := strings.TrimSpace(recorder.Body.String()), `{"status":true,"message":"","total_data":1250}`; got != want {
		t.Fatalf("body = %s, want %s", got, want)
	}
	if received.ProductID != "PRODUCT" || received.LANum != 42 {
		t.Fatalf("request = %#v", received)
	}
}

func TestHandlerMapsFileNotFoundForScheduler(t *testing.T) {
	handler, _ := NewHandler(executorFunc(func(context.Context, Request) (Result, error) {
		return Result{}, &JobError{Kind: ErrorFileNotFound, Message: "input file was not found in MinIO"}
	}), HandlerOptions{})
	recorder := performRequest(handler, schedulerRequestJSON)
	if recorder.Code != http.StatusNotFound || !strings.Contains(recorder.Body.String(), `"status":false`) {
		t.Fatalf("status/body = %d/%s", recorder.Code, recorder.Body.String())
	}
}

func TestHandlerRejectsUnknownSchedulerFields(t *testing.T) {
	handler, _ := NewHandler(executorFunc(func(context.Context, Request) (Result, error) {
		return Result{}, errors.New("must not run")
	}), HandlerOptions{})
	body := strings.TrimSuffix(strings.TrimSpace(schedulerRequestJSON), "}") + `,"target_table":"unsafe"}`
	recorder := performRequest(handler, body)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status/body = %d/%s", recorder.Code, recorder.Body.String())
	}
}

func TestHealthDoesNotExecuteBusinessFlow(t *testing.T) {
	handler, _ := NewHandler(executorFunc(func(context.Context, Request) (Result, error) {
		return Result{}, errors.New("must not run")
	}), HandlerOptions{})
	request := httptest.NewRequest(http.MethodGet, HealthRoute, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	response := recorder.Result()
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || string(body) != "{\"status\":\"UP\"}\n" {
		t.Fatalf("status/body = %d/%s", response.StatusCode, body)
	}
}

func TestHandlerRejectsShortBearerToken(t *testing.T) {
	_, err := NewHandler(executorFunc(func(context.Context, Request) (Result, error) {
		return Result{}, nil
	}), HandlerOptions{BearerToken: "short-token"})
	if err == nil || !strings.Contains(err.Error(), "at least 32 bytes") {
		t.Fatalf("NewHandler() error = %v", err)
	}
}
