package preview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"parser-engine/internal/config"
)

const testBearerToken = "0123456789abcdef0123456789abcdef"

const atmBersamaInput = `RH|2|20260702
SH|ATMB-000|RPT_DATE|TR_DATE|TR_TIME|ACQ_ID|ISS_ID|BNF_ID|BNF_MBR_ID|FWD_ID|TRML_ID|TRML_CD|CARD_NUMBER|FROM_ACCT_NBR|TRAN_CODE|REV|DB|CR|AMOUNT|XRATE|DB_RC|CR_RC|ISS_TRACE_NBR|BNF_TRACE_NBR|REF_NBR|RECEIPT_NBR|TO_ACCT_NBR|CUST_REF_NBR|ACQ_FEE|BNF_FEE|SW_FEE
SB|ATMB-000|20260702|260701|110103|2|126||||161929||504986400000000068||0110||D||100000||00||||161929030019|19|||6510|0|991
SF|ATMB-000|20260702|4|4|600000
SH|ATMB-020|RPT_DATE|TR_DATE|TR_TIME|ACQ_ID|ISS_ID|BNF_ID|BNF_MBR_ID|FWD_ID|TRML_ID|TRML_CD|CARD_NUMBER|FROM_ACCT_NBR|TRAN_CODE|REV|DB|CR|AMOUNT|XRATE|DB_RC|CR_RC|ISS_TRACE_NBR|BNF_TRACE_NBR|REF_NBR|RECEIPT_NBR|TO_ACCT_NBR|CUST_REF_NBR|ACQ_FEE|BNF_FEE|SW_FEE
SB|ATMB-020|20260702|260701|102203|13|13|2|||86566857|6014|5893859990001354|701075331|4020|||C|10000|||76||013941|000000308996|65250|88880999753|0865260701308996|0|0|0
SF|ATMB-020|20260702|1|1|10000
SH|ATMB-030|RPT_DATE|TR_DATE|TR_TIME|ACQ_ID|ISS_ID|BNF_ID|BNF_MBR_ID|FWD_ID|TRML_ID|TRML_CD|CARD_NUMBER|FROM_ACCT_NBR|TRAN_CODE|REV|DB|CR|AMOUNT|XRATE|DB_RC|CR_RC|ISS_TRACE_NBR|BNF_TRACE_NBR|REF_NBR|RECEIPT_NBR|TO_ACCT_NBR|CUST_REF_NBR|ACQ_FEE|BNF_FEE|SW_FEE
SB|ATMB-030|20260702|260701|104841|2|126||||161929||504986400000000068||0110||D||100000||00||||161929000015|15|||0|0|0
SF|ATMB-030|20260702|6|3|350000
SH|ATMB-510|RPT_DATE|BANK_ID|INQ_TRX|INQ_FEE_ACQ|INQ_FEE_SW|WD_TRX|WD_FEE_ACQ|WD_FEE_SW|WD_AMOUNT|TRF_TRX_BNF|TRF_FEE_BNF|TRF_FEE_SW|TRF_AMOUNT_BNF|TRF_TRX_ACQ|TRF_FEE_ACQ|PDCL_INQ_TRX|PDCL_INQ_FEE_ACQ|PDCL_INQ_FEE_SW|PDCL_WD_TRX|PDCL_WD_FEE_ACQ|PDCL_WD_FEE_SW|PDCL_TRF_TRX_ACQ|PDCL_TRF_FEE_ACQ|PDCL_TRF_FEE_SW|TRF_TRX_BNF|TRF_FEE_BNF|TRF_FEE_SW|TRF_AMOUNT_BNF|TRF_TRX_ACQ|TRF_FEE_ACQ|TOTAL_TRX|TOTAL_FEE|TOTAL_FEE_SW|TOTAL_TRX_AMOUNT|CLAIM_TRX|CLAIM_FEE|CLAIM_AMOUNT|TOTAL_GROSS
SB|ATMB-510|20260702|126|0|0|0|4|26040|3960|600000|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|0|4|26040|3960|60000|0|0|0|626030
SF|ATMB-510|20260702|1|4|626040
RF|2|20260702
`

type fakeObjectSource struct {
	mu      sync.Mutex
	objects map[string][]byte
	errors  map[string]error
	opens   [][2]string
	closed  map[string]int
}

func (s *fakeObjectSource) Open(ctx context.Context, minioPath, fileName string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opens = append(s.opens, [2]string{minioPath, fileName})
	key := minioPath + "\x00" + fileName
	if err := s.errors[key]; err != nil {
		return nil, err
	}
	value, exists := s.objects[key]
	if !exists {
		return nil, errors.New("not found")
	}
	return &trackingReadCloser{
		Reader: bytes.NewReader(append([]byte(nil), value...)),
		close: func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.closed == nil {
				s.closed = make(map[string]int)
			}
			s.closed[key]++
		},
	}, nil
}

type trackingReadCloser struct {
	*bytes.Reader
	close  func()
	closed bool
}

func (r *trackingReadCloser) Close() error {
	if r.closed {
		return errors.New("closed twice")
	}
	r.closed = true
	r.close()
	return nil
}

type objectSourceFunc func(context.Context, string, string) (io.ReadCloser, error)

func (f objectSourceFunc) Open(ctx context.Context, minioPath, fileName string) (io.ReadCloser, error) {
	return f(ctx, minioPath, fileName)
}

func validBaseConfig() config.ParserConfig {
	return config.ParserConfig{
		FileType:       config.FileTypeRaw,
		Columns:        []config.ColumnSpec{{Name: "body", Type: config.TypeString}},
		Mappings:       []config.FieldMapping{{Source: "raw", Target: "body"}},
		DateFormat:     "20060102",
		DateTimeFormat: time.RFC3339,
		Timezone:       "UTC",
		MaxRecordBytes: 1024 * 1024,
		MaxFields:      100,
	}
}

func atmBersamaConfig() config.ParserConfig {
	cfg := validBaseConfig()
	cfg.FileType = config.FileTypeSectionedDelimited
	cfg.Delimiter = "|"
	cfg.Columns = []config.ColumnSpec{
		{Name: "section_key", Type: config.TypeString},
		{Name: "payload", Type: config.TypeJSON},
	}
	cfg.Mappings = []config.FieldMapping{
		{Source: "section_key", Target: "section_key"},
		{Source: "payload", Target: "payload"},
	}
	cfg.Sectioned = config.SectionedDelimitedConfig{
		RecordTypeIndex:       0,
		SectionKeyIndex:       1,
		FileHeaderCode:        "RH",
		SectionHeaderCode:     "SH",
		DataCode:              "SB",
		SectionFooterCode:     "SF",
		FileFooterCode:        "RF",
		HeaderStartIndex:      2,
		DataStartIndex:        2,
		DuplicateHeaderPolicy: config.DuplicateHeaderSuffixIndex,
	}
	return cfg
}

func newTestHandler(t *testing.T, source ObjectSource, cfg config.ParserConfig, mutate func(*Options)) http.Handler {
	t.Helper()
	opts := Options{
		AllowedPathPrefixes: []string{"rsp/atm-bersama"},
		DefaultLimit:        20,
		MaxLimit:            100,
	}
	if mutate != nil {
		mutate(&opts)
	}
	handler, err := NewHandler(source, cfg, opts)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return handler
}

func performJSON(handler http.Handler, body string, authorization string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, Route, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestATMSectionedDelimitedPreviewUsesReferenceMinIOContract(t *testing.T) {
	path := "rsp/atm-bersama/20260702"
	name := "ATM-BERSAMA-20260702.txt"
	key := path + "\x00" + name
	source := &fakeObjectSource{objects: map[string][]byte{key: []byte(atmBersamaInput)}}
	handler := newTestHandler(t, source, atmBersamaConfig(), nil)
	body := `{
		"final_minio_path":"rsp/atm-bersama/20260702",
		"final_file_name":"ATM-BERSAMA-20260702.txt",
		"la_num":"LA-100",
		"product_id":123,
		"task":{"id":"scheduler-task"},
		"activity":null,
		"file_date":"2026-07-02",
		"limit":10
	}`
	recorder := performJSON(handler, body, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != "SUCCESS" || response.Summary.Records != 4 || response.Summary.Errors != 0 {
		t.Fatalf("response = %#v", response)
	}
	if response.FinalMinioPath != path || response.FinalFileName != name {
		t.Fatalf("references = %q/%q", response.FinalMinioPath, response.FinalFileName)
	}
	firstPayload, ok := response.Records[0].Fields["payload"].(map[string]any)
	if !ok || firstPayload["CARD_NUMBER"] != "504986400000000068" || firstPayload["AMOUNT"] != "100000" {
		t.Fatalf("first payload = %#v", response.Records[0].Fields["payload"])
	}
	lastPayload, ok := response.Records[3].Fields["payload"].(map[string]any)
	if !ok || lastPayload["TRF_TRX_BNF__2"] != "0" || lastPayload["TOTAL_GROSS"] != "626030" {
		t.Fatalf("last payload = %#v", response.Records[3].Fields["payload"])
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if len(source.opens) != 1 || source.opens[0] != [2]string{path, name} {
		t.Fatalf("source opens = %#v", source.opens)
	}
	if source.closed[key] != 1 {
		t.Fatalf("close count = %d", source.closed[key])
	}
}

func TestBearerAuthenticationIsOptionalWhenNotConfigured(t *testing.T) {
	key := "rsp/atm-bersama\x00report.txt"
	source := &fakeObjectSource{objects: map[string][]byte{key: []byte("hello\n")}}
	handler := newTestHandler(t, source, validBaseConfig(), nil)
	for _, authorization := range []string{"", "not-a-bearer-value"} {
		recorder := performJSON(handler, `{"final_minio_path":"rsp/atm-bersama","final_file_name":"report.txt"}`, authorization)
		if recorder.Code != http.StatusOK {
			t.Fatalf("authorization %q produced %d/%s", authorization, recorder.Code, recorder.Body.String())
		}
	}
}

func TestNonEmptyBearerTokenAlwaysEnablesAuthentication(t *testing.T) {
	key := "rsp/atm-bersama\x00report.txt"
	source := &fakeObjectSource{objects: map[string][]byte{key: []byte("hello\n")}}
	handler := newTestHandler(t, source, validBaseConfig(), func(opts *Options) {
		opts.BearerToken = testBearerToken
	})
	for name, authorization := range map[string]string{
		"missing": "",
		"wrong":   "Bearer wrong",
	} {
		t.Run(name, func(t *testing.T) {
			recorder := performJSON(handler, `{"final_minio_path":"rsp/atm-bersama","final_file_name":"report.txt"}`, authorization)
			if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), `"code":"UNAUTHORIZED"`) {
				t.Fatalf("status/body = %d/%s", recorder.Code, recorder.Body.String())
			}
		})
	}
	if recorder := performJSON(handler, `{"final_minio_path":"rsp/atm-bersama","final_file_name":"report.txt"}`, "Bearer "+testBearerToken); recorder.Code != http.StatusOK {
		t.Fatalf("valid token produced %d/%s", recorder.Code, recorder.Body.String())
	}
}

func TestRequireBearerAuthenticationRejectsBlankToken(t *testing.T) {
	_, err := NewHandler(&fakeObjectSource{}, validBaseConfig(), Options{RequireBearerAuth: true})
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("NewHandler error = %v", err)
	}
}

func TestPathPolicyAndRequestValidationPreventObjectAccess(t *testing.T) {
	source := &fakeObjectSource{}
	handler := newTestHandler(t, source, validBaseConfig(), nil)
	tests := []string{
		`{}`,
		`{"final_minio_path":"rsp/atm-bersama","final_file_name":""}`,
		`{"final_minio_path":"/rsp/atm-bersama","final_file_name":"report.txt"}`,
		`{"final_minio_path":"rsp/atm-bersama/","final_file_name":"report.txt"}`,
		`{"final_minio_path":"../rsp/atm-bersama","final_file_name":"report.txt"}`,
		`{"final_minio_path":"rsp/atm-bersama/../private","final_file_name":"report.txt"}`,
		`{"final_minio_path":"rsp/atm-bersama/%252e%252e/private","final_file_name":"report.txt"}`,
		`{"final_minio_path":"rsp/private","final_file_name":"report.txt"}`,
		`{"final_minio_path":"rsp/atm-bersama","final_file_name":"../report.txt"}`,
		`{"final_minio_path":"rsp/atm-bersama","final_file_name":"%252e%252e%252freport.txt"}`,
		`{"final_minio_path":"rsp/atm-bersama","final_file_name":"folder/report.txt"}`,
		`{"final_minio_path":"rsp/atm-bersama","final_file_name":"report.txt","unknown":true}`,
		`{"final_minio_path":"rsp/atm-bersama","final_file_name":"report.txt","limit":101}`,
	}
	for _, body := range tests {
		recorder := performJSON(handler, body, "")
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body %s produced %d/%s", body, recorder.Code, recorder.Body.String())
		}
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if len(source.opens) != 0 {
		t.Fatalf("invalid requests opened objects: %#v", source.opens)
	}
}

func TestEmptyAllowedPrefixesDoNotAddPathRestriction(t *testing.T) {
	key := "another/location\x00report.txt"
	source := &fakeObjectSource{objects: map[string][]byte{key: []byte("hello\n")}}
	handler := newTestHandler(t, source, validBaseConfig(), func(opts *Options) {
		opts.AllowedPathPrefixes = nil
	})
	recorder := performJSON(handler, `{"final_minio_path":"another/location","final_file_name":"report.txt"}`, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status/body = %d/%s", recorder.Code, recorder.Body.String())
	}
}

func TestPreviewEnforcesRequestInputAndResponseBounds(t *testing.T) {
	t.Run("request", func(t *testing.T) {
		handler := newTestHandler(t, &fakeObjectSource{}, validBaseConfig(), func(opts *Options) {
			opts.MaxRequestBytes = 128
		})
		body := `{"final_minio_path":"rsp/atm-bersama","final_file_name":"` + strings.Repeat("a", 200) + `"}`
		recorder := performJSON(handler, body, "")
		if recorder.Code != http.StatusRequestEntityTooLarge || !strings.Contains(recorder.Body.String(), `"code":"REQUEST_TOO_LARGE"`) {
			t.Fatalf("status/body = %d/%s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("input", func(t *testing.T) {
		key := "rsp/atm-bersama\x00report.txt"
		source := &fakeObjectSource{objects: map[string][]byte{key: []byte("hello world\n")}}
		handler := newTestHandler(t, source, validBaseConfig(), func(opts *Options) {
			opts.MaxInputBytes = 4
		})
		recorder := performJSON(handler, `{"final_minio_path":"rsp/atm-bersama","final_file_name":"report.txt"}`, "")
		if recorder.Code != http.StatusRequestEntityTooLarge || !strings.Contains(recorder.Body.String(), `"code":"INPUT_TOO_LARGE"`) {
			t.Fatalf("status/body = %d/%s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("response", func(t *testing.T) {
		key := "rsp/atm-bersama\x00report.txt"
		input := strings.Repeat("a", 3000) + "\n" + strings.Repeat("b", 3000) + "\n"
		source := &fakeObjectSource{objects: map[string][]byte{key: []byte(input)}}
		handler := newTestHandler(t, source, validBaseConfig(), func(opts *Options) {
			opts.DefaultLimit = 10
			opts.MaxResponseBytes = 4096
			opts.MaxValueBytes = 4096
		})
		recorder := performJSON(handler, `{"final_minio_path":"rsp/atm-bersama","final_file_name":"report.txt"}`, "")
		if recorder.Code != http.StatusOK || recorder.Body.Len() > 4096 {
			t.Fatalf("status/size/body = %d/%d/%s", recorder.Code, recorder.Body.Len(), recorder.Body.String())
		}
		var response Response
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Status != "PARTIAL" || !response.Summary.Truncated {
			t.Fatalf("response = %#v", response)
		}
	})
}

func TestPreviewPropagatesContextAndTimeout(t *testing.T) {
	t.Run("context value", func(t *testing.T) {
		type contextKey string
		const key contextKey = "request-id"
		seen := make(chan string, 1)
		source := objectSourceFunc(func(ctx context.Context, _, _ string) (io.ReadCloser, error) {
			seen <- ctx.Value(key).(string)
			return io.NopCloser(strings.NewReader("hello\n")), nil
		})
		handler := newTestHandler(t, source, validBaseConfig(), nil)
		request := httptest.NewRequest(http.MethodPost, Route, strings.NewReader(`{"final_minio_path":"rsp/atm-bersama","final_file_name":"report.txt"}`))
		request.Header.Set("Content-Type", "application/json")
		request = request.WithContext(context.WithValue(request.Context(), key, "req-123"))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK || <-seen != "req-123" {
			t.Fatalf("status/body = %d/%s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("timeout", func(t *testing.T) {
		source := objectSourceFunc(func(ctx context.Context, _, _ string) (io.ReadCloser, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		})
		handler := newTestHandler(t, source, validBaseConfig(), func(opts *Options) {
			opts.RequestTimeout = 10 * time.Millisecond
		})
		recorder := performJSON(handler, `{"final_minio_path":"rsp/atm-bersama","final_file_name":"report.txt"}`, "")
		if recorder.Code != http.StatusGatewayTimeout || !strings.Contains(recorder.Body.String(), `"code":"REQUEST_TIMEOUT"`) {
			t.Fatalf("status/body = %d/%s", recorder.Code, recorder.Body.String())
		}
	})
}

func TestPreviewEnforcesConcurrencyLimit(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	source := objectSourceFunc(func(ctx context.Context, _, _ string) (io.ReadCloser, error) {
		select {
		case entered <- struct{}{}:
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		default:
		}
		return io.NopCloser(strings.NewReader("hello\n")), nil
	})
	handler := newTestHandler(t, source, validBaseConfig(), func(opts *Options) {
		opts.MaxConcurrentRequests = 1
		opts.RequestTimeout = time.Second
	})
	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstDone <- performJSON(handler, `{"final_minio_path":"rsp/atm-bersama","final_file_name":"report.txt"}`, "")
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first request did not reach object source")
	}
	second := performJSON(handler, `{"final_minio_path":"rsp/atm-bersama","final_file_name":"report.txt"}`, "")
	if second.Code != http.StatusTooManyRequests || !strings.Contains(second.Body.String(), `"code":"TOO_MANY_REQUESTS"`) {
		t.Fatalf("second status/body = %d/%s", second.Code, second.Body.String())
	}
	close(release)
	select {
	case first := <-firstDone:
		if first.Code != http.StatusOK {
			t.Fatalf("first status/body = %d/%s", first.Code, first.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("first request did not finish")
	}
}

func TestSourceFailureIsClassifiedWithoutLeakingCause(t *testing.T) {
	source := objectSourceFunc(func(context.Context, string, string) (io.ReadCloser, error) {
		return nil, errors.New("minio-secret-do-not-leak")
	})
	handler := newTestHandler(t, source, validBaseConfig(), nil)
	recorder := performJSON(handler, `{"final_minio_path":"rsp/atm-bersama","final_file_name":"report.txt"}`, "")
	if recorder.Code != http.StatusBadGateway || !strings.Contains(recorder.Body.String(), `"code":"FILE_OBJECT_ERROR"`) {
		t.Fatalf("status/body = %d/%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "minio-secret") {
		t.Fatalf("source cause leaked: %s", recorder.Body.String())
	}
}

func TestConstructorRejectsInvalidParserAndUnsafeOptions(t *testing.T) {
	invalid := validBaseConfig()
	invalid.FileType = "UNKNOWN"
	if _, err := NewHandler(&fakeObjectSource{}, invalid, Options{}); err == nil {
		t.Fatal("invalid parser config was accepted")
	}
	if _, err := NewHandler(&fakeObjectSource{}, validBaseConfig(), Options{AllowedPathPrefixes: []string{"safe/../private"}}); err == nil {
		t.Fatal("unsafe prefix was accepted")
	}
	if _, err := NewHandler(&fakeObjectSource{}, validBaseConfig(), Options{MaxResponseBytes: 100}); err == nil {
		t.Fatal("unsafe response bound was accepted")
	}
}

func TestHealthAndExactRoute(t *testing.T) {
	handler := newTestHandler(t, &fakeObjectSource{}, validBaseConfig(), nil)
	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, HealthRoute, nil))
	if health.Code != http.StatusOK || health.Body.String() != "{\"status\":\"UP\"}\n" {
		t.Fatalf("health = %d/%q", health.Code, health.Body.String())
	}
	old := performJSON(handler, `{"final_minio_path":"rsp/atm-bersama","final_file_name":"report.txt"}`, "")
	if old.Code == http.StatusNotFound {
		t.Fatal("exact preview route was not registered")
	}
	request := httptest.NewRequest(http.MethodPost, "/rsp/cashrecon-sch-parse-file-atm-bersama", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	legacy := httptest.NewRecorder()
	handler.ServeHTTP(legacy, request)
	if legacy.Code != http.StatusNotFound {
		t.Fatalf("legacy route status = %d", legacy.Code)
	}
}

func TestSanitizersBoundHostileValues(t *testing.T) {
	input := strings.Repeat("\x01", 8*1024*1024)
	got := sanitizeText(input, 32)
	if !utf8.ValidString(got) || !strings.HasSuffix(got, "...[truncated]") || len(got) > 32+len("...[truncated]") {
		t.Fatalf("sanitizeText length/encoding = %d/%t", len(got), utf8.ValidString(got))
	}
	fields := map[string]any{
		"long":   strings.Repeat("x", 100),
		"binary": []byte{0xff, 0xfe},
		"nested": map[string]any{"a": map[string]any{"b": map[string]any{"c": map[string]any{"d": map[string]any{"e": map[string]any{"f": "hidden"}}}}}},
	}
	result := sanitizeFields(fields, 12)
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result["long"].(string), "[truncated]") || result["binary"] != "[binary value omitted]" || strings.Contains(string(encoded), "hidden") {
		t.Fatalf("sanitized fields = %s", encoded)
	}
}
