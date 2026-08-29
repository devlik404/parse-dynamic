package preview_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"parser-engine/internal/config"
	"parser-engine/internal/miniogateway"
	"parser-engine/internal/preview"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

const atmBersamaGatewaySample = `RH|2|20260702
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

func TestMinIOGatewayToPreviewHandlerSectionedDelimitedEndToEnd(t *testing.T) {
	const (
		bucketName = "cashrecon"
		minioPath  = "rsp/cashrecon-sch-parse-file-atm-bersama/20260702"
		fileName   = "atm-bersama.txt"
		objectPath = "/cashrecon/rsp/cashrecon-sch-parse-file-atm-bersama/20260702/atm-bersama.txt"
		baseURL    = "https://minio-gateway.test"
		objectURL  = baseURL + objectPath
	)

	type gatewayRequest struct {
		method     string
		url        string
		requestURI string
		accept     string
		rangeValue string
	}
	var requests []gatewayRequest
	gatewayTransport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, gatewayRequest{
			method:     request.Method,
			url:        request.URL.String(),
			requestURI: request.URL.RequestURI(),
			accept:     request.Header.Get("Accept"),
			rangeValue: request.Header.Get("Range"),
		})
		if request.Method == http.MethodHead {
			header := make(http.Header)
			header.Set("ETag", `"atm-bersama-v1"`)
			header.Set("Content-Length", strconv.Itoa(len(atmBersamaGatewaySample)))
			header.Set("x-amz-version-id", "version-1")
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     header,
				Body:       http.NoBody,
				Request:    request,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/octet-stream"}},
			Body:       io.NopCloser(strings.NewReader(atmBersamaGatewaySample)),
			Request:    request,
		}, nil
	})

	gatewayClient, err := miniogateway.New(miniogateway.Config{
		BaseURL:    baseURL,
		BucketName: bucketName,
		Timeout:    5 * time.Second,
		Transport:  gatewayTransport,
	})
	if err != nil {
		t.Fatalf("miniogateway.New() error = %v", err)
	}

	parserConfig := config.ParserConfig{
		FileType:  config.FileTypeSectionedDelimited,
		Delimiter: "|",
		Columns: []config.ColumnSpec{
			{Name: "section_key", Type: config.TypeString},
			{Name: "payload", Type: config.TypeJSON},
		},
		Mappings: []config.FieldMapping{
			{Source: "section_key", Target: "section_key"},
			{Source: "payload", Target: "payload"},
		},
		DateFormat:     "20060102",
		DateTimeFormat: time.RFC3339,
		Timezone:       "UTC",
		MaxRecordBytes: 1024 * 1024,
		MaxFields:      100,
		Sectioned: config.SectionedDelimitedConfig{
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
		},
	}
	handler, err := preview.NewHandler(gatewayClient, parserConfig, preview.Options{})
	if err != nil {
		t.Fatalf("preview.NewHandler() error = %v", err)
	}
	requestBody := `{
		"la_num": 987654,
		"product_id": "ATM_BERSAMA",
		"task": "PARSE_FILE",
		"activity": "RECONCILIATION",
		"file_date": "20260702",
		"final_minio_path": "` + minioPath + `",
		"final_file_name": "` + fileName + `",
		"limit": 10
	}`
	request := httptest.NewRequest(http.MethodPost, preview.Route, strings.NewReader(requestBody))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	response := recorder.Result()
	defer response.Body.Close()
	responseBytes, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response error = %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.StatusCode, responseBytes)
	}

	var result preview.Response
	if err := json.Unmarshal(responseBytes, &result); err != nil {
		t.Fatalf("decode response error = %v; body = %s", err, responseBytes)
	}
	if result.Status != "SUCCESS" || result.Summary.Records != 4 || len(result.Records) != 4 {
		t.Fatalf("response summary = %#v, record count = %d", result.Summary, len(result.Records))
	}
	if result.Summary.Errors != 0 || result.FinalMinioPath != minioPath || result.FinalFileName != fileName {
		t.Fatalf("response metadata/summary = %#v", result)
	}

	firstPayload, ok := result.Records[0].Fields["payload"].(map[string]any)
	if !ok {
		t.Fatalf("first dynamic payload type = %T", result.Records[0].Fields["payload"])
	}
	if firstPayload["CARD_NUMBER"] != "504986400000000068" || firstPayload["AMOUNT"] != "100000" {
		t.Fatalf("first dynamic payload = %#v", firstPayload)
	}
	lastPayload, ok := result.Records[3].Fields["payload"].(map[string]any)
	if !ok {
		t.Fatalf("last dynamic payload type = %T", result.Records[3].Fields["payload"])
	}
	if lastPayload["TRF_TRX_BNF"] != "0" || lastPayload["TRF_TRX_BNF__2"] != "0" {
		t.Fatalf("duplicate dynamic header values = %#v / %#v", lastPayload["TRF_TRX_BNF"], lastPayload["TRF_TRX_BNF__2"])
	}
	if lastPayload["TOTAL_GROSS"] != "626030" {
		t.Fatalf("last TOTAL_GROSS = %#v", lastPayload["TOTAL_GROSS"])
	}

	if len(requests) != 3 {
		t.Fatalf("gateway request count = %d, want HEAD/GET/HEAD: %#v", len(requests), requests)
	}
	for index, method := range []string{http.MethodHead, http.MethodGet, http.MethodHead} {
		if requests[index].method != method || requests[index].url != objectURL || requests[index].requestURI != objectPath {
			t.Fatalf("gateway request[%d] = %#v, want %s %s", index, requests[index], method, objectURL)
		}
	}
	if requests[1].accept != "application/octet-stream" || requests[1].rangeValue != "" {
		t.Fatalf("gateway GET headers = %#v", requests[1])
	}
}
