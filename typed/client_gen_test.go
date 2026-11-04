package typed_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/skarm/ytr"
	"github.com/skarm/ytr/internal/apidoc"
	"github.com/skarm/ytr/typed"
)

type typedBenchmarkWireIssue struct {
	Key         string      `json:"key"`
	ID          string      `json:"id"`
	Summary     string      `json:"summary"`
	CustomFloat json.Number `json:"customfloat"`
}

type typedBenchmarkFixture struct {
	raw     *ytr.Client
	typed   *typed.Client
	request *http.Request
	calls   int
}

type requestFixtures struct {
	Revision   string               `json:"revision"`
	Operations []requestFixture     `json:"operations"`
	Bodies     []requestBodyFixture `json:"bodies"`
}

type requestFixture struct {
	Name        string                 `json:"name"`
	Method      string                 `json:"method"`
	Path        map[string]string      `json:"path"`
	EscapedPath string                 `json:"escapedPath"`
	Encoding    string                 `json:"encoding"`
	Query       []requestQueryFixture  `json:"query"`
	Headers     []requestHeaderFixture `json:"headers"`
	Body        json.RawMessage        `json:"body"`
}

type requestQueryFixture struct {
	Field    string          `json:"field"`
	Name     string          `json:"name"`
	Value    json.RawMessage `json:"value"`
	Wire     string          `json:"wire"`
	Optional bool            `json:"optional"`
}

type requestHeaderFixture struct {
	Field string `json:"field"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

type requestBodyFixture struct {
	Name      string          `json:"name"`
	Operation string          `json:"operation"`
	Body      json.RawMessage `json:"body"`
}

type requestTransport func(*http.Request) (*http.Response, error)

type requestFileReader struct {
	*strings.Reader
	closed bool
}

type responseFixtures struct {
	Bodies     map[string]json.RawMessage `json:"bodies"`
	KnownPaths map[string][]string        `json:"known_paths"`
	Operations []responseFixture          `json:"operations"`
}

type responseFixture struct {
	Name       string `json:"name"`
	Source     string `json:"source"`
	Status     int    `json:"status"`
	Shape      string `json:"shape"`
	Body       string `json:"body"`
	KnownPaths string `json:"known_paths"`
}

type responseTransport func(*http.Request) (*http.Response, error)

type responseBody struct {
	io.Reader
	reads  int
	closes int
}

type failingResponseReader struct{ err error }

func TestExtraFieldsPreserveValuesAndRejectDocumentedCollision(t *testing.T) {
	t.Parallel()
	for _, collision := range []bool{false, true} {
		name := "custom fields"
		if collision {
			name = "documented field collision"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			config := coreConfig(func(request *http.Request) (*http.Response, error) {
				calls++
				data, err := io.ReadAll(request.Body)
				request.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]json.RawMessage
				if err = json.Unmarshal(data, &fields); err != nil {
					t.Fatal(err)
				}
				want := map[string]json.RawMessage{"summary": json.RawMessage(`"typed summary"`), "queue": json.RawMessage(`"SDK"`), "customNumber": json.RawMessage(`9007199254740993`), "flag": json.RawMessage(`false`), "empty": json.RawMessage(`[]`)}
				if !reflect.DeepEqual(fields, want) {
					t.Fatalf("request fields: %s", data)
				}
				return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"key":"SDK-1","id":9007199254740993}`))}, nil
			})
			client, err := typed.New(config)
			if err != nil {
				t.Fatal(err)
			}
			extra := ytr.Fields{"customNumber": json.Number("9007199254740993"), "flag": false, "empty": []string{}}
			if collision {
				extra["summary"] = "extra summary"
			}
			result, err := client.CreateIssue(context.Background(), typed.CreateIssueRequest{Body: typed.CreateIssueRequestBody{Summary: "typed summary", Queue: typed.Ref("SDK")}, ExtraFields: extra})
			if collision {
				if err == nil || result != nil || calls != 0 {
					t.Fatalf("duplicate field: result=%+v, error=%v, calls=%d", result, err, calls)
				}
				return
			}
			if err != nil || result == nil {
				t.Fatalf("custom fields: result=%+v, error=%v", result, err)
			}
			if calls != 1 || result.StatusCode != http.StatusCreated || result.Data.Key != "SDK-1" || result.Data.ID.Number == nil || *result.Data.ID.Number != json.Number("9007199254740993") {
				t.Fatalf("custom field response: %+v, calls=%d", result, calls)
			}
		})
	}
}

func TestRequiredWidgetFieldsKeepFalseZeroAndEmptyText(t *testing.T) {
	body := typed.CreateDashboardWidgetRequestBody{
		CreateDashboardWidgetBurndownChart: &typed.CreateDashboardWidgetBurndownChart{
			Type: "burndown-chart", Description: "", SprintID: json.Number("0"), WithHolidays: false,
		},
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	want := decodeRequestJSON(t, []byte(`{"type":"burndown-chart","description":"","sprintId":0,"withHolidays":false}`))
	if got := decodeRequestJSON(t, data); !reflect.DeepEqual(got, want) {
		t.Fatalf("required zero-value widget fields omitted or changed: %s", data)
	}
}

func TestAllDocumentedTypedRequestWireContracts(t *testing.T) {
	fixtures := loadRequestFixtures(t)
	catalog, err := apidoc.Load("../api/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	if fixtures.Revision != catalog.Revision || len(fixtures.Operations) != len(ytr.Operations()) {
		t.Fatalf("independent documentation inventory: revision=%q operations=%d", fixtures.Revision, len(fixtures.Operations))
	}
	seen := make(map[string]bool, len(fixtures.Operations))
	for _, fixture := range fixtures.Operations {
		if seen[fixture.Name] {
			t.Fatalf("duplicate independent operation fixture %q", fixture.Name)
		}
		seen[fixture.Name] = true
		t.Run(fixture.Name, func(t *testing.T) {
			runRequestFixture(t, fixture, fixture.Body, false, false, nil)
			if hasOptionalRequestQuery(fixture) {
				t.Run("optional-query-omitted", func(t *testing.T) {
					runRequestFixture(t, fixture, fixture.Body, true, false, nil)
				})
			}
			if len(fixture.Headers) > 0 {
				t.Run("version-header", func(t *testing.T) {
					runRequestFixture(t, fixture, fixture.Body, false, true, nil)
				})
			}
		})
	}
}

func TestDocumentedTypedRequestBodiesPreserveWireShape(t *testing.T) {
	fixtures := loadRequestFixtures(t)
	operations := make(map[string]requestFixture, len(fixtures.Operations))
	for _, fixture := range fixtures.Operations {
		operations[fixture.Name] = fixture
	}
	for _, body := range fixtures.Bodies {
		t.Run(body.Name, func(t *testing.T) {
			fixture, ok := operations[body.Operation]
			if !ok {
				t.Fatalf("body references unknown operation %q", body.Operation)
			}
			runRequestFixture(t, fixture, body.Body, true, false, nil)
		})
	}
}

func TestTypedRequestCustomFieldsPreserveLargeNumbersAndNull(t *testing.T) {
	fixtures := loadRequestFixtures(t)
	for _, fixture := range fixtures.Operations {
		if fixture.Name != "CreateIssue" {
			continue
		}
		body := json.RawMessage(`{"summary":"Custom fields","queue":"TEST"}`)
		extra := ytr.Fields{"customNumber": json.Number("9007199254740993"), "customObject": ytr.Fields{"enabled": false, "value": nil}}
		runRequestFixture(t, fixture, body, true, false, extra)
		return
	}
	t.Fatal("CreateIssue documentation fixture is missing")
}

func TestTypedRequestOptionsOverridePerCallAndKeepSharedClientUnchanged(t *testing.T) {
	transportError := errors.New("options fixture completed")
	calls := 0
	raw, err := ytr.New(ytr.Config{
		Organization: ytr.Organization{Kind: ytr.Organization360, ID: "org"},
		TokenSource:  ytr.OAuthToken("token"),
		HTTPClient: &http.Client{Transport: requestTransport(func(request *http.Request) (*http.Response, error) {
			calls++
			wantQuery := url.Values{"fields": {"summary"}, "expand": {"attachments"}}
			wantHeader := ""
			if calls == 1 {
				wantQuery["fields"] = []string{"description"}
				wantQuery["createdAt"] = []string{"from:2026-01-01", "to:2026-02-01"}
				wantHeader = `"7"`
			}
			if request.Method != http.MethodGet || request.URL.EscapedPath() != "/v3/issues/SDK%2F1" || !reflect.DeepEqual(request.URL.Query(), wantQuery) || request.Header.Get("If-Match") != wantHeader {
				t.Errorf("call %d: request=%s %s headers=%v; want query=%v If-Match=%q", calls, request.Method, request.URL, request.Header, wantQuery, wantHeader)
			}
			return nil, transportError
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := typed.FromClient(raw)
	if err != nil {
		t.Fatal(err)
	}
	if client.Raw() != raw {
		t.Fatal("FromClient did not retain the existing transport client")
	}
	request := typed.GetIssueRequest{IssueID: "SDK/1", Fields: []string{"summary"}, Expand: typed.Ptr("attachments")}
	if _, err = client.GetIssue(t.Context(), request, ytr.Query("fields", "description"), ytr.Query("createdAt", "from:2026-01-01", "to:2026-02-01"), ytr.Header("If-Match", `"7"`)); !errors.Is(err, transportError) {
		t.Fatal(err)
	}
	if _, err = client.GetIssue(t.Context(), request); !errors.Is(err, transportError) {
		t.Fatal(err)
	}
	if calls != 2 || !reflect.DeepEqual(request.Fields, []string{"summary"}) {
		t.Fatalf("calls=%d request fields=%v; per-call options mutated shared request state", calls, request.Fields)
	}
}

func TestTypedRequestExtensionsCannotReplaceDocumentedFields(t *testing.T) {
	calls := 0
	client, err := typed.New(ytr.Config{
		Organization: ytr.Organization{Kind: ytr.Organization360, ID: "org"},
		TokenSource:  ytr.OAuthToken("token"),
		HTTPClient: &http.Client{Transport: requestTransport(func(*http.Request) (*http.Response, error) {
			calls++
			return nil, errors.New("unexpected HTTP request")
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CreateIssue(t.Context(), typed.CreateIssueRequest{
		Body:        typed.CreateIssueRequestBody{Summary: "typed", Queue: typed.Ref("TEST")},
		ExtraFields: ytr.Fields{"summary": "extension"},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicates a documented field") || calls != 0 {
		t.Fatalf("documented-field collision: calls=%d error=%v; want rejection before HTTP", calls, err)
	}
}

func TestTypedExtensionsMergeWithZeroOrOneDocumentedField(t *testing.T) {
	fixtures := loadRequestFixtures(t)
	var operation requestFixture
	for _, fixture := range fixtures.Operations {
		if fixture.Name == "UpdateIssue" {
			operation = fixture
			break
		}
	}
	if operation.Name == "" {
		t.Fatal("UpdateIssue documentation fixture is missing")
	}
	for _, fixture := range []struct {
		name string
		body string
	}{
		{"custom-only", `{}`},
		{"one-documented-field", `{"summary":"one"}`},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			runRequestFixture(t, operation, json.RawMessage(fixture.body), true, false, ytr.Fields{"customField": json.Number("9007199254740993")})
		})
	}
}

func TestTypedDocumentedResponseFixtures(t *testing.T) {
	fixtures := loadResponseFixtures(t)
	checkedPaths := make(map[string]bool)
	for _, fixture := range fixtures.Operations {
		t.Run(fixture.Name, func(t *testing.T) {
			body := fixtures.Bodies[fixture.Body]
			if fixture.Shape == "binary" {
				body = []byte{0, 0xff, 0x89, 'T', 'r', 'a', 'c', 'k', 'e', 'r', '\n'}
			}
			reader := &responseBody{Reader: bytes.NewReader(body)}
			client, calls := fixtureClient(t, fixture.Status, reader)
			result, err := invokeResponseMethod(t, client, fixture.Name)
			if err != nil {
				t.Fatalf("response from %s: %v", fixture.Source, err)
			}
			if *calls != 1 {
				t.Fatalf("HTTP calls = %d; want 1", *calls)
			}
			if fixture.Shape == "binary" {
				assertResponseDownload(t, result, reader, body)
				return
			}
			data := responseData(t, result, fixture.Status)
			if reader.closes != 1 {
				t.Errorf("response body closes = %d; want 1", reader.closes)
			}
			key := data.Type().String() + "#" + fixture.KnownPaths
			if !checkedPaths[key] {
				for _, path := range fixtures.KnownPaths[fixture.KnownPaths] {
					if !responseTypeHasPath(data.Type(), responsePath(path), 64) {
						t.Errorf("documented JSON field %s absent from %s (%s)", path, data.Type(), fixture.Source)
					}
				}
				checkedPaths[key] = true
			}
			if fixture.Body == "" {
				if !data.IsZero() {
					t.Errorf("bodyless response returned nonzero Data: %#v", data.Interface())
				}
				return
			}
			assertResponseValues(t, body, data)
		})
	}
}

func TestTypedResponsesClearDataAfterTrailingJSON(t *testing.T) {
	fixtures := loadResponseFixtures(t)
	for _, fixture := range fixtures.Operations {
		if fixture.Shape == "binary" || fixture.Shape == "empty" {
			continue
		}
		t.Run(fixture.Name, func(t *testing.T) {
			body := append([]byte(nil), fixtures.Bodies[fixture.Body]...)
			if len(body) == 0 {
				body = []byte(`{"populated":true}`)
			}
			body = append(body, []byte("\n{}")...)
			reader := &responseBody{Reader: bytes.NewReader(body)}
			client, calls := fixtureClient(t, fixture.Status, reader)
			result, err := invokeResponseMethod(t, client, fixture.Name)
			if err == nil {
				t.Fatal("multiple JSON values unexpectedly accepted")
			}
			if data := responseData(t, result, fixture.Status); !data.IsZero() {
				t.Errorf("failed decode returned partial Data: %#v", data.Interface())
			}
			if reader.closes != 1 || *calls != 1 {
				t.Errorf("body closes = %d, HTTP calls = %d; want 1, 1", reader.closes, *calls)
			}
		})
	}
}

func TestTypedResponseEmptyBodies(t *testing.T) {
	fixtures := loadResponseFixtures(t)
	for _, fixture := range fixtures.Operations {
		if fixture.Shape == "binary" {
			continue
		}
		t.Run(fixture.Name, func(t *testing.T) {
			reader := &responseBody{Reader: strings.NewReader("")}
			client, calls := fixtureClient(t, fixture.Status, reader)
			result, err := invokeResponseMethod(t, client, fixture.Name)
			allowEmpty := fixture.Shape == "empty" || fixture.Shape == "unspecified"
			if (err == nil) != allowEmpty {
				t.Errorf("empty response error = %v; allow empty = %v", err, allowEmpty)
			}
			if data := responseData(t, result, fixture.Status); !data.IsZero() {
				t.Errorf("empty body returned nonzero Data: %#v", data.Interface())
			}
			if reader.closes != 1 || *calls != 1 {
				t.Errorf("body closes = %d, HTTP calls = %d; want 1, 1", reader.closes, *calls)
			}
		})
	}
}

func TestTypedAPIErrorHasMetadataAndZeroData(t *testing.T) {
	fixtures := loadResponseFixtures(t)
	for _, fixture := range fixtures.Operations {
		t.Run(fixture.Name, func(t *testing.T) {
			reader := &responseBody{Reader: strings.NewReader(`{"errorMessages":["invalid fixture"],"errors":{"summary":"required"}}`)}
			client, calls := fixtureClient(t, http.StatusUnprocessableEntity, reader)
			result, err := invokeResponseMethod(t, client, fixture.Name)
			var apiError *ytr.APIError
			if !errors.As(err, &apiError) || apiError.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("error = %v; want APIError with status 422", err)
			}
			if fixture.Shape == "binary" {
				if !result.IsNil() {
					t.Errorf("failed download returned a stream: %v", result.Interface())
				}
			} else if data := responseData(t, result, http.StatusUnprocessableEntity); !data.IsZero() {
				t.Errorf("API error returned nonzero Data: %#v", data.Interface())
			}
			if reader.closes != 1 || *calls != 1 {
				t.Errorf("body closes = %d, HTTP calls = %d; want 1, 1", reader.closes, *calls)
			}
		})
	}
}

func TestTypedBodylessResponsesPreserveWrappedEOF(t *testing.T) {
	fixtures := loadResponseFixtures(t)
	for _, fixture := range fixtures.Operations {
		if fixture.Shape != "empty" && fixture.Shape != "unspecified" {
			continue
		}
		for _, status := range []int{fixture.Status, http.StatusUnprocessableEntity} {
			t.Run(fmt.Sprintf("%s/%d", fixture.Name, status), func(t *testing.T) {
				readFailure := fmt.Errorf("truncated fixture stream: %w", io.EOF)
				reader := &responseBody{Reader: failingResponseReader{err: readFailure}}
				client, calls := fixtureClient(t, status, reader)
				result, err := invokeResponseMethod(t, client, fixture.Name)
				if !errors.Is(err, readFailure) {
					t.Errorf("error = %v; want wrapped EOF read failure", err)
				}
				if status == http.StatusUnprocessableEntity {
					var apiError *ytr.APIError
					if !errors.As(err, &apiError) || apiError.StatusCode != status {
						t.Errorf("HTTP error with wrapped EOF was lost: %v", err)
					}
				}
				if data := responseData(t, result, status); !data.IsZero() {
					t.Errorf("failed stream returned nonzero Data: %#v", data.Interface())
				}
				if reader.closes != 1 || *calls != 1 {
					t.Errorf("body closes = %d, HTTP calls = %d; want 1, 1", reader.closes, *calls)
				}
			})
		}
	}
}

func TestTypedResponsesPreserveDynamicDictionaryKeys(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{"GetIssue", `{"key":"SDK-1","customField":{"integer":9007199254740993,"fraction":1.25,"flag":false,"null":null,"array":["value",2]}}`},
		{"GetComponent", `{"id":9007199254740993,"name":"Component with exact numeric ID"}`},
		{"GetQueue", `{"key":"CUSTOM","workflows":{"custom-workflow":[{"self":"workflow-url","id":"1","key":"task","display":"Task"}]}}`},
		{"ListQueueWorkflows", `{"custom-workflow":[{"self":"workflow-url","id":"1","key":"task","display":"Task"}]}`},
		{"ListQueueFields", `[{"id":"field","optionsProvider":{"type":"QueueFixedListOptionsProvider","values":{"CUSTOM":["one",2]}}}]`},
		{"ListTriggerLogs", `[{"request":{"headers":{"X-Custom":"request-value"}},"response":{"headers":{"X-Custom-Response":"response-value"}}}]`},
		{"ListBulkOperationIssues", `[{"issue":{"key":"SDK-1"},"error":{"errors":{"customField":"invalid"},"errorMessages":["failure"]}}]`},
		{"GetAutoactionLog", `[{"id":0,"issueReference":{"display":{"template":"Issue"}},"status":{"value":"success","display":{"arbitrary":9007199254740993}}}]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := &responseBody{Reader: strings.NewReader(test.body)}
			client, _ := fixtureClient(t, http.StatusOK, reader)
			result, err := invokeResponseMethod(t, client, test.name)
			if err != nil {
				t.Fatal(err)
			}
			assertResponseValues(t, []byte(test.body), responseData(t, result, http.StatusOK))
		})
	}
}

func TestTypedResponseUnionsRejectInvalidShapes(t *testing.T) {
	for _, test := range []struct {
		name   string
		method string
		body   string
	}{
		{"ambiguous project object", "GetIssue", `{"project":{"unexpected":true}}`},
		{"project number", "GetIssue", `{"project":123}`},
		{"issue ID boolean", "GetIssue", `{"id":true}`},
		{"comment ID boolean", "AddIssueComment", `{"id":true}`},
		{"entity access number", "GetEntity", `{"fields":{"teamAccess":12}}`},
		{"list object", "ListQueueFields", `{"id":"field"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := &responseBody{Reader: strings.NewReader(test.body)}
			client, calls := fixtureClient(t, http.StatusOK, reader)
			result, err := invokeResponseMethod(t, client, test.method)
			if err == nil {
				t.Fatal("invalid response shape unexpectedly accepted")
			}
			if data := responseData(t, result, http.StatusOK); !data.IsZero() {
				t.Errorf("invalid shape returned partial Data: %#v", data.Interface())
			}
			if reader.closes != 1 || *calls != 1 {
				t.Errorf("body closes = %d, HTTP calls = %d; want 1, 1", reader.closes, *calls)
			}
		})
	}
}

func TestTypedResponseSharedTransportFailure(t *testing.T) {
	if client, err := typed.FromClient(nil); client != nil || err == nil {
		t.Fatalf("nil raw client: client=%v, error=%v", client, err)
	}
	for _, name := range []string{"GetIssue", "UploadIssueAttachment", "ClearIssueScroll", "DownloadIssueAttachment"} {
		t.Run(name, func(t *testing.T) {
			failure := errors.New("fixture transport unavailable")
			calls := 0
			raw, err := ytr.New(ytr.Config{
				Organization: ytr.Organization{Kind: ytr.Organization360, ID: "fixture-org"},
				TokenSource:  ytr.OAuthToken("fixture-token"),
				HTTPClient: &http.Client{Transport: responseTransport(func(*http.Request) (*http.Response, error) {
					calls++
					return nil, failure
				})},
			})
			if err != nil {
				t.Fatal(err)
			}
			client, err := typed.FromClient(raw)
			if err != nil {
				t.Fatal(err)
			}
			result, err := invokeResponseMethod(t, client, name)
			if !errors.Is(err, failure) || !result.IsNil() || calls != 1 {
				t.Errorf("result=%v, error=%v, calls=%d; want nil, transport failure, 1", result.Interface(), err, calls)
			}
		})
	}
}

// Both paths decode the complete public Issue DTO, including custom fields.
// Allocated bytes are cumulative B/op, not peak memory. No server or network
// latency is included; the local RoundTripper supplies identical response bytes.
func BenchmarkTypedIssueResponses(b *testing.B) {
	for _, operation := range []struct {
		name   string
		search bool
	}{
		{"GetIssue", false},
		{"SearchIssues", true},
	} {
		b.Run(operation.name, func(b *testing.B) {
			for _, size := range []struct {
				name  string
				bytes int
			}{
				{"128B", 128},
				{"64KiB", 64 << 10},
				{"1MiB", 1 << 20},
			} {
				data, expected := typedBenchmarkIssues(b, size.bytes, operation.search)
				b.Run(size.name, func(b *testing.B) {
					for _, knownLength := range []bool{true, false} {
						lengthName := "UnknownLength"
						if knownLength {
							lengthName = "KnownLength"
						}
						b.Run(lengthName, func(b *testing.B) {
							for _, stream := range []bool{true, false} {
								mode := "RawBufferedDecode"
								if stream {
									mode = "TypedStream"
								}
								b.Run(mode, func(b *testing.B) {
									fixture := newTypedBenchmarkFixture(b, data, knownLength)
									b.SetBytes(int64(len(data)))
									b.ReportAllocs()
									if operation.search {
										benchmarkTypedIssueSearch(b, fixture, expected, stream)
									} else {
										benchmarkTypedIssueGet(b, fixture, expected[0], stream)
									}
								})
							}
						})
					}
				})
			}
		})
	}
}

func benchmarkTypedIssueGet(b *testing.B, fixture *typedBenchmarkFixture, expected typedBenchmarkWireIssue, stream bool) {
	b.Helper()
	ctx := b.Context()
	request := typed.GetIssueRequest{IssueID: "SDK-1", Fields: []string{"key", "id", "summary", "customfloat"}}
	option := ytr.Query("fields", "key,id,summary,customfloat")
	var result *typed.Result[typed.Issue]
	for b.Loop() {
		if stream {
			var err error
			result, err = fixture.typed.GetIssue(ctx, request)
			if err != nil {
				b.Fatal(err)
			}
		} else {
			response, err := fixture.raw.GetIssue(ctx, request.IssueID, option)
			if err != nil {
				b.Fatal(err)
			}
			var issue typed.Issue
			if err := response.Decode(&issue); err != nil {
				b.Fatal(err)
			}
			result = &typed.Result[typed.Issue]{StatusCode: response.StatusCode, Header: response.Header, Data: issue}
		}
	}
	fixture.check(b, http.MethodGet, "/v3/issues/SDK-1")
	checkTypedBenchmarkMetadata(b, result.ResponseMetadata)
	checkTypedBenchmarkIssue(b, result.Data, expected)
	runtime.KeepAlive(result)
}

func benchmarkTypedIssueSearch(b *testing.B, fixture *typedBenchmarkFixture, expected []typedBenchmarkWireIssue, stream bool) {
	b.Helper()
	ctx := b.Context()
	request := typed.SearchIssuesRequest{Fields: []string{"key", "id", "summary", "customfloat"}, Body: typed.SearchIssuesRequestBody{Query: typed.Value("Queue: SDK")}}
	body := ytr.Fields{"query": "Queue: SDK"}
	option := ytr.Query("fields", "key,id,summary,customfloat")
	var result *typed.Result[[]*typed.Issue]
	for b.Loop() {
		if stream {
			var err error
			result, err = fixture.typed.SearchIssues(ctx, request)
			if err != nil {
				b.Fatal(err)
			}
		} else {
			response, err := fixture.raw.SearchIssues(ctx, body, option)
			if err != nil {
				b.Fatal(err)
			}
			var issues []*typed.Issue
			if err := response.Decode(&issues); err != nil {
				b.Fatal(err)
			}
			result = &typed.Result[[]*typed.Issue]{StatusCode: response.StatusCode, Header: response.Header, Data: issues}
		}
	}
	fixture.check(b, http.MethodPost, "/v3/issues/_search")
	checkTypedBenchmarkMetadata(b, result.ResponseMetadata)
	if len(result.Data) != len(expected) {
		b.Fatalf("issues = %d; want %d", len(result.Data), len(expected))
	}
	for i := range expected {
		if result.Data[i] == nil {
			b.Fatalf("issue %d is nil", i)
		}
		checkTypedBenchmarkIssue(b, *result.Data[i], expected[i])
	}
	runtime.KeepAlive(result)
}

func newTypedBenchmarkFixture(b *testing.B, data []byte, knownLength bool) *typedBenchmarkFixture {
	b.Helper()
	fixture := new(typedBenchmarkFixture)
	raw, err := ytr.New(ytr.Config{
		Organization: ytr.Organization{Kind: ytr.Organization360, ID: "benchmark"},
		TokenSource:  ytr.OAuthToken("fixture-token"),
		BaseURL:      "https://benchmark.invalid",
		HTTPClient: &http.Client{Transport: responseTransport(func(request *http.Request) (*http.Response, error) {
			if request.Body != nil {
				if err := request.Body.Close(); err != nil {
					return nil, err
				}
			}
			fixture.request = request
			fixture.calls++
			length := int64(-1)
			if knownLength {
				length = int64(len(data))
			}
			return &http.Response{
				StatusCode:    http.StatusOK,
				Header:        http.Header{"X-Request-Id": {"typed-benchmark"}},
				ContentLength: length,
				Body:          io.NopCloser(bytes.NewReader(data)),
			}, nil
		})},
	})
	if err != nil {
		b.Fatal(err)
	}
	client, err := typed.FromClient(raw)
	if err != nil {
		b.Fatal(err)
	}
	fixture.raw, fixture.typed = raw, client
	return fixture
}

func (fixture *typedBenchmarkFixture) check(b *testing.B, method, path string) {
	b.Helper()
	if fixture.calls != b.N || fixture.request == nil {
		b.Fatalf("HTTP requests = %d; want %d", fixture.calls, b.N)
	}
	if fixture.request.Method != method || fixture.request.URL.EscapedPath() != path || fixture.request.URL.Query().Get("fields") != "key,id,summary,customfloat" {
		b.Fatalf("request: %s %s", fixture.request.Method, fixture.request.URL)
	}
}

func typedBenchmarkIssues(b *testing.B, size int, search bool) ([]byte, []typedBenchmarkWireIssue) {
	b.Helper()
	count := 1
	if search {
		count = size / 128
	}
	expected := make([]typedBenchmarkWireIssue, count)
	for i := range expected {
		id := strconv.Itoa(i + 1)
		expected[i] = typedBenchmarkWireIssue{Key: "SDK-" + id, ID: id, Summary: "Issue " + id + ": inspect complete DTO", CustomFloat: json.Number(id + ".125")}
		encoded, err := json.Marshal(expected[i])
		if err != nil {
			b.Fatal(err)
		}
		recordSize := size
		if search {
			recordSize = 127
			if i == 0 {
				recordSize = 126
			}
		}
		if len(encoded) > recordSize {
			b.Fatalf("record %d exceeds fixture size: %d > %d", i, len(encoded), recordSize)
		}
		expected[i].Summary += strings.Repeat("x", recordSize-len(encoded))
	}
	var value any = expected[0]
	if search {
		value = expected
	}
	data, err := json.Marshal(value)
	if err != nil {
		b.Fatal(err)
	}
	if len(data) != size {
		b.Fatalf("response fixture = %d bytes; want %d", len(data), size)
	}
	return data, expected
}

func checkTypedBenchmarkIssue(b *testing.B, got typed.Issue, expected typedBenchmarkWireIssue) {
	b.Helper()
	if got.ID.String == nil || *got.ID.String != expected.ID || got.ID.Number != nil || got.Key != expected.Key || got.Summary != expected.Summary {
		b.Fatalf("decoded issue fields differ for %s", expected.Key)
	}
	if len(got.AdditionalFields) != 1 || string(got.AdditionalFields["customfloat"]) != string(expected.CustomFloat) {
		b.Fatalf("customfloat for %s = %s; want %s", expected.Key, got.AdditionalFields["customfloat"], expected.CustomFloat)
	}
}

func checkTypedBenchmarkMetadata(b *testing.B, metadata ytr.ResponseMetadata) {
	b.Helper()
	if metadata.StatusCode != http.StatusOK || metadata.Header.Get("X-Request-ID") != "typed-benchmark" {
		b.Fatalf("metadata differs: %+v", metadata)
	}
}

func (f requestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func (r *requestFileReader) Close() error {
	r.closed = true
	return nil
}

func runRequestFixture(t *testing.T, fixture requestFixture, body json.RawMessage, omitOptional, useHeaders bool, extra ytr.Fields) {
	t.Helper()
	transportError := errors.New("request contract transport completed")
	calls := 0
	wantQuery := make(url.Values)
	client, err := typed.New(ytr.Config{
		Organization: ytr.Organization{Kind: ytr.Organization360, ID: "contract-org"},
		TokenSource:  ytr.OAuthToken("contract-token"),
		Language:     "en",
		HTTPClient: &http.Client{Transport: requestTransport(func(request *http.Request) (*http.Response, error) {
			calls++
			assertTypedRequest(t, request, fixture, wantQuery, body, extra, useHeaders)
			return nil, transportError
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	method := reflect.ValueOf(client).MethodByName(fixture.Name)
	if !method.IsValid() {
		t.Fatalf("documented method %s is missing", fixture.Name)
	}
	methodType := method.Type()
	if methodType.NumIn() != 3 || !methodType.IsVariadic() || methodType.In(1).Name() != fixture.Name+"Request" || methodType.In(1).Kind() != reflect.Struct {
		t.Fatalf("%s must accept its concrete named request, got %s", fixture.Name, methodType)
	}
	request := reflect.New(methodType.In(1)).Elem()
	for field, value := range fixture.Path {
		setRequestField(t, request, field, json.RawMessage(strconv.Quote(value)))
	}
	for _, query := range fixture.Query {
		if (omitOptional && query.Optional) || (useHeaders && query.Name == "version") {
			continue
		}
		setRequestField(t, request, query.Field, query.Value)
		wantQuery.Add(query.Name, query.Wire)
	}
	if useHeaders {
		for _, header := range fixture.Headers {
			setRequestField(t, request, header.Field, json.RawMessage(strconv.Quote(header.Value)))
		}
	}
	if fixture.Encoding == "json" {
		field := request.FieldByName("Body")
		if !field.IsValid() {
			t.Fatal("JSON request has no Body field")
		}
		if err := json.Unmarshal(body, field.Addr().Interface()); err != nil {
			t.Fatalf("documented request body does not decode into %s: %v\n%s", field.Type(), err, body)
		}
	}
	if len(extra) > 0 {
		field := request.FieldByName("ExtraFields")
		if !field.IsValid() || field.Type() != reflect.TypeFor[ytr.Fields]() {
			t.Fatal("request has no typed ExtraFields extension")
		}
		field.Set(reflect.ValueOf(extra))
	}
	reader := &requestFileReader{Reader: strings.NewReader("file fixture\nЯ\x00end")}
	if fixture.Encoding == "multipart" {
		request.FieldByName("File").Set(reflect.ValueOf(ytr.File{Name: "wire Я.txt", FieldName: "fixture-file", Reader: reader}))
	}
	results := method.Call([]reflect.Value{reflect.ValueOf(t.Context()), request})
	if len(results) != 2 {
		t.Fatalf("method returned %d values, want result and error", len(results))
	}
	gotError, _ := results[1].Interface().(error)
	if !errors.Is(gotError, transportError) || calls != 1 {
		t.Errorf("dispatches=%d error=%v; want exactly one dispatch and preserved transport error", calls, gotError)
	}
	if reader.closed {
		t.Error("upload closed the caller-owned file reader")
	}
}

func assertTypedRequest(t *testing.T, request *http.Request, fixture requestFixture, wantQuery url.Values, body json.RawMessage, extra ytr.Fields, useHeaders bool) {
	t.Helper()
	if request.Method != fixture.Method || request.URL.EscapedPath() != fixture.EscapedPath {
		t.Errorf("dispatch=%s %s; want %s %s", request.Method, request.URL.EscapedPath(), fixture.Method, fixture.EscapedPath)
	}
	if got := request.URL.Query(); !reflect.DeepEqual(got, wantQuery) {
		t.Errorf("query=%v; want %v", got, wantQuery)
	}
	for name, want := range map[string]string{"Authorization": "OAuth contract-token", "X-Org-ID": "contract-org", "Accept-Language": "en"} {
		if got := request.Header.Get(name); got != want {
			t.Errorf("header %s=%q; want %q", name, got, want)
		}
	}
	if request.Header.Get("X-Cloud-Org-Id") != "" {
		t.Error("360 request also sent a cloud organization header")
	}
	for _, header := range fixture.Headers {
		want := ""
		if useHeaders {
			want = header.Value
		}
		if got := request.Header.Get(header.Name); got != want {
			t.Errorf("header %s=%q; want %q", header.Name, got, want)
		}
	}
	if fixture.Encoding == "multipart" {
		assertTypedMultipart(t, request)
		return
	}
	var got []byte
	if request.Body != nil {
		defer request.Body.Close()
		var err error
		got, err = io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
	}
	if fixture.Encoding != "json" {
		if len(got) != 0 {
			t.Errorf("bodyless operation sent %q", got)
		}
		return
	}
	if request.Header.Get("Content-Type") != "application/json" {
		t.Errorf("JSON content type=%q", request.Header.Get("Content-Type"))
	}
	want := decodeRequestJSON(t, body)
	if len(extra) > 0 {
		fields, ok := want.(map[string]any)
		if !ok {
			t.Fatal("extension fixture requires an object body")
		}
		for name, value := range extra {
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			fields[name] = decodeRequestJSON(t, encoded)
		}
	}
	if value := decodeRequestJSON(t, got); !reflect.DeepEqual(value, want) {
		t.Errorf("body changed documented fields or JSON kinds:\ngot  %s\nwant %s", got, mustRequestJSON(t, want))
	}
}

func assertTypedMultipart(t *testing.T, request *http.Request) {
	t.Helper()
	mediaType, parameters, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || parameters["boundary"] == "" {
		t.Fatalf("invalid multipart content type %q: %v", request.Header.Get("Content-Type"), err)
	}
	defer request.Body.Close()
	reader := multipart.NewReader(request.Body, parameters["boundary"])
	part, err := reader.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(part)
	part.Close()
	if err != nil {
		t.Fatal(err)
	}
	if part.FormName() != "fixture-file" || part.FileName() != "wire Я.txt" || string(data) != "file fixture\nЯ\x00end" {
		t.Errorf("multipart part=%q filename=%q bytes=%q", part.FormName(), part.FileName(), data)
	}
	if _, err = reader.NextPart(); !errors.Is(err, io.EOF) {
		t.Errorf("unexpected additional multipart part: %v", err)
	}
}

func setRequestField(t *testing.T, request reflect.Value, name string, data json.RawMessage) {
	t.Helper()
	field := reflect.Value{}
	for i := range request.NumField() {
		if normalizedRequestField(request.Type().Field(i).Name) == normalizedRequestField(name) {
			field = request.Field(i)
			break
		}
	}
	if !field.IsValid() {
		t.Fatalf("%s lacks documented path/query/header field %q", request.Type(), name)
	}
	if field.Kind() == reflect.Slice && field.Type().Elem().Kind() == reflect.String && len(data) > 0 && data[0] == '"' {
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
		field.Set(reflect.ValueOf(strings.Split(value, ",")).Convert(field.Type()))
		return
	}
	if err := json.Unmarshal(data, field.Addr().Interface()); err != nil {
		t.Fatalf("cannot set %s.%s from %s: %v", request.Type(), name, data, err)
	}
}

func normalizedRequestField(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, value)
}

func hasOptionalRequestQuery(fixture requestFixture) bool {
	for _, query := range fixture.Query {
		if query.Optional {
			return true
		}
	}
	return false
}

func loadRequestFixtures(t *testing.T) requestFixtures {
	t.Helper()
	data, err := os.ReadFile("testdata/requests.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures requestFixtures
	if err = json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	return fixtures
}

func decodeRequestJSON(t *testing.T, data []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("invalid JSON %s: %v", data, err)
	}
	return value
}

func mustRequestJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func (r failingResponseReader) Read([]byte) (int, error) { return 0, r.err }

func (f responseTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func (b *responseBody) Read(data []byte) (int, error) {
	b.reads++
	return b.Reader.Read(data)
}

func (b *responseBody) Close() error {
	b.closes++
	return nil
}

func loadResponseFixtures(t *testing.T) responseFixtures {
	t.Helper()
	data, err := os.ReadFile("testdata/responses.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures responseFixtures
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures.Operations) != len(ytr.Operations()) {
		t.Fatalf("independent operation fixture count = %d; want %d", len(fixtures.Operations), len(ytr.Operations()))
	}
	seen := make(map[string]bool)
	for _, operation := range fixtures.Operations {
		if seen[operation.Name] || operation.Source == "" || operation.Status == 0 {
			t.Fatalf("invalid independent response fixture: %+v", operation)
		}
		seen[operation.Name] = true
		if _, ok := fixtures.KnownPaths[operation.KnownPaths]; !ok {
			t.Fatalf("missing known field paths for %s", operation.Name)
		}
		if operation.Body != "" && !json.Valid(fixtures.Bodies[operation.Body]) {
			t.Fatalf("missing or invalid response body fixture for %s", operation.Name)
		}
	}
	return fixtures
}

func fixtureClient(t *testing.T, status int, body io.ReadCloser) (*typed.Client, *int) {
	t.Helper()
	calls := 0
	client, err := typed.New(ytr.Config{
		Organization: ytr.Organization{Kind: ytr.Organization360, ID: "fixture-org"},
		TokenSource:  ytr.OAuthToken("fixture-token"),
		HTTPClient: &http.Client{Transport: responseTransport(func(request *http.Request) (*http.Response, error) {
			calls++
			if request.Body != nil {
				if _, err := io.Copy(io.Discard, request.Body); err != nil {
					return nil, err
				}
				if err := request.Body.Close(); err != nil {
					return nil, err
				}
			}
			return &http.Response{StatusCode: status, Header: http.Header{"X-Request-Id": {"response-fixture"}}, Body: body}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	return client, &calls
}

func invokeResponseMethod(t *testing.T, client *typed.Client, name string) (reflect.Value, error) {
	t.Helper()
	method := reflect.ValueOf(client).MethodByName(name)
	if !method.IsValid() || method.Type().NumIn() != 3 || method.Type().NumOut() != 2 || !method.Type().IsVariadic() {
		t.Fatalf("missing typed method with (context, request, ...options) signature: %s", name)
	}
	request := reflect.New(method.Type().In(1)).Elem()
	if request.Kind() != reflect.Struct {
		t.Fatalf("%s request is %s; want a DTO struct", name, request.Type())
	}
	for _, field := range request.Fields() {
		if field.Kind() == reflect.String {
			field.SetString("fixture")
		}
		if field.Type() == reflect.TypeFor[ytr.EntityType]() {
			field.Set(reflect.ValueOf(ytr.Project))
		}
		if field.Type() == reflect.TypeFor[ytr.File]() {
			field.Set(reflect.ValueOf(ytr.File{Name: "fixture.txt", Reader: strings.NewReader("fixture upload")}))
		}
	}
	values := method.Call([]reflect.Value{reflect.ValueOf(context.Background()), request})
	if !values[1].IsNil() {
		return values[0], values[1].Interface().(error)
	}
	return values[0], nil
}

func responseData(t *testing.T, result reflect.Value, status int) reflect.Value {
	t.Helper()
	if result.Kind() != reflect.Pointer || result.IsNil() {
		t.Fatal("response metadata was lost: result is nil")
	}
	value := result.Elem()
	if got := value.FieldByName("StatusCode").Int(); got != int64(status) {
		t.Errorf("response status = %d; want %d", got, status)
	}
	if header := value.FieldByName("Header").Interface().(http.Header); header.Get("X-Request-ID") != "response-fixture" {
		t.Errorf("response headers = %v; request ID missing", header)
	}
	data := value.FieldByName("Data")
	if !data.IsValid() {
		t.Fatal("typed result has no Data")
	}
	return data
}

func assertResponseDownload(t *testing.T, result reflect.Value, body *responseBody, want []byte) {
	t.Helper()
	download := result.Interface().(*ytr.Download)
	if download == nil {
		t.Fatal("download is nil")
	}
	if download.StatusCode != http.StatusOK || download.Header.Get("X-Request-ID") != "response-fixture" {
		t.Errorf("download metadata: %+v", download)
	}
	if body.reads != 0 || body.closes != 0 {
		t.Errorf("stream was consumed before return: reads=%d, closes=%d", body.reads, body.closes)
	}
	got, readErr := io.ReadAll(download.Body)
	closeErr := download.Body.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(got, want) || body.closes != 1 {
		t.Errorf("download bytes=%x, read=%v, close=%v, closes=%d; want %x", got, readErr, closeErr, body.closes, want)
	}
}

func responsePath(path string) []string {
	if path == "" {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i := range parts {
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(parts[i], "~1", "/"), "~0", "~")
	}
	return parts
}

func responseTypeHasPath(model reflect.Type, path []string, depth int) bool {
	if depth == 0 {
		return false
	}
	for model.Kind() == reflect.Pointer {
		model = model.Elem()
	}
	if len(path) == 0 {
		return true
	}
	switch model.Kind() {
	case reflect.Array, reflect.Slice, reflect.Map:
		return path[0] == "*" && responseTypeHasPath(model.Elem(), path[1:], depth-1)
	case reflect.Struct:
		for field := range model.Fields() {
			if field.PkgPath != "" {
				continue
			}
			tag, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if tag == path[0] {
				return responseTypeHasPath(field.Type, path[1:], depth-1)
			}
			if field.Anonymous && tag == "" && responseTypeHasPath(field.Type, path, depth-1) {
				return true
			}
		}
		if responseUnionType(model) {
			for field := range model.Fields() {
				if responseTypeHasPath(field.Type, path, depth-1) {
					return true
				}
			}
		}
	}
	return false
}

func responseUnionType(model reflect.Type) bool {
	if model.NumField() == 0 || !model.Implements(reflect.TypeFor[json.Marshaler]()) {
		return false
	}
	for field := range model.Fields() {
		if field.PkgPath != "" || field.Type.Kind() != reflect.Pointer || field.Tag.Get("json") != "-" {
			return false
		}
	}
	return true
}

func assertResponseValues(t *testing.T, original []byte, data reflect.Value) {
	t.Helper()
	encoded, err := json.Marshal(data.Interface())
	if err != nil {
		t.Fatalf("marshal decoded response: %v", err)
	}
	want := responseJSON(t, original)
	got := responseJSON(t, encoded)
	compareResponseJSON(t, data.Type(), "", want, got)
}

func responseJSON(t *testing.T, data []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var result any
	if err := decoder.Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func compareResponseJSON(t *testing.T, model reflect.Type, path string, want, got any) {
	t.Helper()
	switch expected := want.(type) {
	case map[string]any:
		actual, ok := got.(map[string]any)
		if !ok {
			t.Errorf("%s: want object, got %#v", path, got)
			return
		}
		for key, value := range expected {
			next := path + "/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
			actualValue, exists := actual[key]
			if !exists {
				// Response DTOs may omit empty/null values when encoded. The
				// independently known field must still exist in the public type.
				if responseJSONZero(value) && responseTypeHasPath(model, responseWildcardPath(next), 64) {
					continue
				}
				t.Errorf("%s: documented fixture value %#v lost during typed decode", next, value)
				continue
			}
			compareResponseJSON(t, model, next, value, actualValue)
		}
	case []any:
		actual, ok := got.([]any)
		if !ok || len(actual) != len(expected) {
			t.Errorf("%s: want array of length %d, got %#v", path, len(expected), got)
			return
		}
		for i := range expected {
			compareResponseJSON(t, model, fmt.Sprintf("%s/%d", path, i), expected[i], actual[i])
		}
	case json.Number:
		actual, ok := got.(json.Number)
		left, leftOK := new(big.Rat).SetString(string(expected))
		right, rightOK := new(big.Rat).SetString(string(actual))
		if !ok || !leftOK || !rightOK || left.Cmp(right) != 0 {
			t.Errorf("%s: number changed from %s to %#v", path, expected, got)
		}
	default:
		if !reflect.DeepEqual(want, got) {
			t.Errorf("%s: value changed from %#v to %#v", path, want, got)
		}
	}
}

func responseJSONZero(value any) bool {
	switch value := value.(type) {
	case nil:
		return true
	case string:
		return value == ""
	case bool:
		return !value
	case json.Number:
		number, ok := new(big.Rat).SetString(string(value))
		return ok && number.Sign() == 0
	case []any:
		return len(value) == 0
	case map[string]any:
		return len(value) == 0
	default:
		return false
	}
}

func responseWildcardPath(path string) []string {
	parts := responsePath(path)
	for i, part := range parts {
		if _, ok := new(big.Int).SetString(part, 10); ok {
			parts[i] = "*"
		}
	}
	return parts
}

func TestOpenObjectPreservesRepeatedMemberSemantics(t *testing.T) {
	var value typed.Issue
	input := `{"summary":"keep","summary":null,"parent":{"key":"SDK-1"},"parent":{"display":"parent"},"tags":["first"],"tags":["last"],"custom":{"first":true},"custom":{"last":false}}`
	if err := json.Unmarshal([]byte(input), &value); err != nil {
		t.Fatal(err)
	}
	if value.Summary != "keep" || value.Parent == nil || value.Parent.Key != "SDK-1" || value.Parent.Display != "parent" || !reflect.DeepEqual(value.Tags, []string{"last"}) || string(value.AdditionalFields["custom"]) != `{"last":false}` {
		t.Fatalf("repeated members changed: %+v", value)
	}
}

func TestOpenObjectFailureKeepsPreviousValue(t *testing.T) {
	parent := &typed.IssueParent{Key: "old"}
	value := typed.Issue{Summary: "original", Parent: parent, AdditionalFields: map[string]json.RawMessage{"old": json.RawMessage(`false`)}}
	for _, input := range []string{`{"summary":"changed","parent":{"key":"new"},"tags":42}`, `{"summary":"changed","custom":`, `{"summary":"changed"} {}`} {
		if err := json.Unmarshal([]byte(input), &value); err == nil {
			t.Fatalf("invalid input accepted: %s", input)
		}
		if value.Summary != "original" || value.Parent != parent || parent.Key != "old" || string(value.AdditionalFields["old"]) != "false" || len(value.AdditionalFields) != 1 {
			t.Fatalf("failed decode changed previous value: %+v", value)
		}
	}
}

// The client is shared by all workers; the transport has no mutable counters or
// artificial latency. This measures SDK CPU/allocations, not network throughput.
func BenchmarkTypedIssueParallel(b *testing.B) {
	for _, test := range []struct {
		name   string
		size   int
		search bool
	}{{"Get128B", 128, false}, {"Search64KiB", 64 << 10, true}, {"Search1MiB", 1 << 20, true}} {
		b.Run(test.name, func(b *testing.B) {
			data, expected := typedBenchmarkIssues(b, test.size, test.search)
			client, err := typed.New(ytr.Config{Organization: ytr.Organization{Kind: ytr.Organization360, ID: "fixture"}, TokenSource: ytr.OAuthToken("fixture-token"), BaseURL: "https://benchmark.invalid", HTTPClient: &http.Client{Transport: responseTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"X-Request-Id": {"parallel"}}, ContentLength: int64(len(data)), Body: io.NopCloser(bytes.NewReader(data))}, nil
			})}})
			if err != nil {
				b.Fatal(err)
			}
			ctx := context.Background()
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				var last *typed.Issue
				for pb.Next() {
					if test.search {
						result, err := client.SearchIssues(ctx, typed.SearchIssuesRequest{Body: typed.SearchIssuesRequestBody{Query: typed.Value("Queue: SDK")}})
						if err != nil {
							b.Error(err)
							return
						}
						if len(result.Data) != len(expected) {
							b.Error("lost search items")
							return
						}
						last = result.Data[0]
					} else {
						result, err := client.GetIssue(ctx, typed.GetIssueRequest{IssueID: "SDK-1"})
						if err != nil {
							b.Error(err)
							return
						}
						last = &result.Data
					}
				}
				if last != nil && (last.Key != expected[0].Key || last.ID.String == nil || *last.ID.String != expected[0].ID || last.Summary != expected[0].Summary || string(last.AdditionalFields["customfloat"]) != string(expected[0].CustomFloat)) {
					b.Error("parallel decode changed values")
				}
				runtime.KeepAlive(last)
			})
		})
	}
}
