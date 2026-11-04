package ytr_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/skarm/ytr"
	"github.com/skarm/ytr/internal/apidoc"
)

type benchmarkIssue struct {
	ID      string `json:"id"`
	Key     string `json:"key"`
	Summary string `json:"summary"`
}

type benchmarkJSONFixture struct {
	client  *ytr.Client
	request *http.Request
	calls   int
}

type streamingBody struct {
	reader io.Reader
	reads  int
	bytes  int
	closes int
}

type dataAndErrorReader struct {
	body []byte
	err  error
	sent bool
}

type unexpectedEOFReader struct{ sent bool }

func TestReadErrorsKeepCauseAndCloseBody(t *testing.T) {
	sentinel := errors.New("broken stream")
	for _, code := range []int{200, 500} {
		reader := &trackedReader{Reader: io.MultiReader(strings.NewReader(`{"part":`), failingReader{sentinel})}
		c := client(t, func(*http.Request) (*http.Response, error) { v := reply(code, ""); v.Body = reader; return v, nil })
		response, err := c.GetCurrentUser(context.Background())
		if !errors.Is(err, sentinel) || !reader.closed || string(response.Body) != `{"part":` {
			t.Fatalf("status=%d: response=%v err=%v closed=%v", code, response, err, reader.closed)
		}
	}
}

func TestCompleteDocumentationInventory(t *testing.T) {
	catalog, err := apidoc.Load("api/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	var sources apidoc.Sources
	if err = apidoc.ReadJSON("api/sources.json", &sources); err != nil {
		t.Fatal(err)
	}
	index, err := os.ReadFile("api/index.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err = apidoc.Check(catalog, sources, string(index)); err != nil {
		t.Fatal(err)
	}
	if len(sources.Pages) != len(apidoc.IndexURLs(string(index))) || len(catalog.Operations) == 0 {
		t.Fatalf("review inventory changes explicitly: %d pages, %d operations", len(sources.Pages), len(catalog.Operations))
	}
	published := ytr.Operations()
	if len(published) != len(catalog.Operations) {
		t.Fatal("public catalog incomplete")
	}
	published[0].Path = "mutated"
	if ytr.Operations()[0].Path == "mutated" {
		t.Fatal("public catalog exposes shared mutable state")
	}
}

// This dispatches EVERY exported endpoint method as a consumer. Expected HTTP
// routes come from independently captured Russian documentation signatures,
// not the normalized path template used by the generator.
func TestEveryDocumentedOperationOnWire(t *testing.T) {
	var sources apidoc.Sources
	if err := apidoc.ReadJSON("api/sources.json", &sources); err != nil {
		t.Fatal(err)
	}
	signatures := map[string]string{}
	for _, page := range sources.Pages {
		for i, sig := range page.Signatures {
			if len(page.Operations) == len(page.Signatures) {
				signatures[page.Operations[i]] = sig
			}
		}
	}
	pathParameterPattern := regexp.MustCompile(`\{([^}]+)\}`)
	for _, op := range ytr.Operations() {
		t.Run(string(op.Name), func(t *testing.T) {
			signature, ok := signatures[string(op.Name)]
			if !ok {
				t.Fatalf("missing independent signature: %s", op.Name)
			}
			expected := strings.SplitN(signature, " ", 2)
			values := map[string]string{"тип_сущности": "goal"}
			route := pathParameterPattern.ReplaceAllStringFunc(expected[1], func(s string) string {
				if value := values[s[1:len(s)-1]]; value != "" {
					return value
				}
				return "fixture"
			})
			called := false
			c := client(t, func(r *http.Request) (*http.Response, error) {
				called = true
				if r.Method != expected[0] || r.URL.Path != route || r.URL.Query().Get("probe") != "yes" {
					t.Errorf("%s %s; want %s %s", r.Method, r.URL, expected[0], route)
				}
				if op.Request == "json" {
					body, _ := io.ReadAll(r.Body)
					if string(body) != `{"sentinel":"preserved"}` {
						t.Errorf("body=%s", body)
					}
				}
				if op.Request == "multipart" {
					if err := r.ParseMultipartForm(1 << 20); err != nil {
						t.Fatal(err)
					}
					file, _, err := r.FormFile("file")
					if err != nil {
						t.Fatal(err)
					}
					defer file.Close()
					body, _ := io.ReadAll(file)
					if string(body) != "payload" {
						t.Error(string(body))
					}
				}
				if op.Response == "stream" {
					return reply(200, "binary\x00body"), nil
				}
				return reply(200, `{"result":"decoded"}`), nil
			})
			method := reflect.ValueOf(c).MethodByName(string(op.Name))
			if !method.IsValid() {
				t.Fatal("public method missing")
			}
			args := []reflect.Value{reflect.ValueOf(context.Background())}
			for _, name := range apidoc.PathParameters(op.Path) {
				if name == "entityType" {
					args = append(args, reflect.ValueOf(ytr.Goal))
				} else {
					args = append(args, reflect.ValueOf("fixture"))
				}
			}
			switch op.Request {
			case "json":
				args = append(args, reflect.ValueOf(ytr.Fields{"sentinel": "preserved"}))
			case "multipart":
				args = append(args, reflect.ValueOf(ytr.File{Name: "data.txt", Reader: strings.NewReader("payload")}))
			}
			args = append(args, reflect.ValueOf([]ytr.CallOption{ytr.Query("probe", "yes")}))
			result := method.CallSlice(args)
			if !result[1].IsNil() {
				t.Fatal(result[1].Interface())
			}
			if !called {
				t.Fatal("method did not perform HTTP operation")
			}
			if download, ok := result[0].Interface().(*ytr.Download); ok {
				defer download.Body.Close()
				body, _ := io.ReadAll(download.Body)
				if string(body) != "binary\x00body" {
					t.Error(string(body))
				}
			} else {
				var decoded map[string]string
				if err := result[0].Interface().(*ytr.Response).Decode(&decoded); err != nil {
					t.Fatal(err)
				}
				if decoded["result"] != "decoded" {
					t.Error(decoded)
				}
			}
		})
	}
}

func TestJSONRawMessageDoesNotDoubleEncode(t *testing.T) {
	c := client(t, func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != `["x",null,0,false]` {
			t.Error(string(body))
		}
		return reply(200, "7"), nil
	})
	response, err := c.BulkUpdateEntities(context.Background(), ytr.Goal, json.RawMessage(`["x",null,0,false]`))
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err = response.Decode(&count); err != nil || count != 7 {
		t.Fatal(count, err)
	}
}

func TestPublicOperationInvocationUsesTheSameContract(t *testing.T) {
	c := client(t, func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != "PATCH" || r.URL.EscapedPath() != "/v3/issues/SDK%2F1" || string(body) != `{"summary":"Updated"}` || r.URL.Query().Get("notify") != "false" {
			t.Error(r.Method, r.URL, string(body))
		}
		return reply(200, `{"key":"SDK-1","summary":"Updated"}`), nil
	})
	type issue struct {
		Key     string `json:"key"`
		Summary string `json:"summary"`
	}
	got, err := ytr.Decode[issue](c.Call(context.Background(), ytr.OpUpdateIssue, []string{"SDK/1"}, ytr.Fields{"summary": "Updated"}, ytr.Query("notify", "false")))
	if err != nil || got.Key != "SDK-1" || got.Summary != "Updated" {
		t.Fatal(got, err)
	}
	op, err := ytr.LookupOperation(ytr.OpUpdateIssue)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(op.PathParameters(), []string{"issueID"}) {
		t.Fatal(op.PathParameters())
	}
	op.Path = "changed"
	again, _ := ytr.LookupOperation(ytr.OpUpdateIssue)
	if again.Path == "changed" {
		t.Fatal("catalog is mutable")
	}
}

func TestPublicDispatchRejectsWrongKindsBeforeHTTP(t *testing.T) {
	c := client(t, func(*http.Request) (*http.Response, error) {
		t.Error("invalid operation reached HTTP")
		return reply(200, `{}`), nil
	})
	for _, name := range []ytr.OperationName{ytr.OpUploadTemporaryAttachment, ytr.OpDownloadIssueThumbnail} {
		if _, err := c.Call(context.Background(), name, nil, nil); !errors.Is(err, ytr.ErrOperationKind) {
			t.Fatal(err)
		}
	}
	if _, err := c.Call(context.Background(), ytr.OpGetCurrentUser, nil, ytr.Fields{}); !errors.Is(err, ytr.ErrOperationKind) {
		t.Fatal(err)
	}
	if _, err := c.Upload(context.Background(), ytr.OpCreateIssue, nil, ytr.File{Name: "x", Reader: strings.NewReader("")}); !errors.Is(err, ytr.ErrOperationKind) {
		t.Fatal(err)
	}
	if _, err := c.Download(context.Background(), ytr.OpGetIssue, []string{"SDK-1"}); !errors.Is(err, ytr.ErrOperationKind) {
		t.Fatal(err)
	}
	if _, err := c.Call(context.Background(), "unknown", nil, nil); !errors.Is(err, ytr.ErrUnknownOperation) {
		t.Fatal(err)
	}
}

func TestPathParameterDiscoveryDoesNotTreatUnmatchedBracesAsParameters(t *testing.T) {
	op := ytr.Operation{Path: "/v3/{/}/{id}"}
	if got := op.PathParameters(); !reflect.DeepEqual(got, []string{"id"}) {
		t.Fatal(got)
	}
}

func TestCallIntoPreservesWireDataNumbersAndMetadata(t *testing.T) {
	body := &streamingBody{reader: strings.NewReader(`{"key":"SDK-1","values":{"number":9007199254740993,"nested":[{"n":9223372036854775807}]}}`)}
	headers := http.Header{"X-Request-Id": {"request-1"}}
	calls := 0
	c := client(t, func(r *http.Request) (*http.Response, error) {
		calls++
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if r.Method != "PATCH" || r.URL.EscapedPath() != "/v3/issues/SDK%2F1" || r.URL.Query().Get("notify") != "false" || string(data) != `{"flag":false,"nullable":null}` {
			t.Errorf("request: %s %s %s", r.Method, r.URL, data)
		}
		if r.Header.Get("Authorization") != "OAuth private-token" || r.Header.Get("X-Org-Id") != "42" {
			t.Errorf("credentials: %v", r.Header)
		}
		return &http.Response{StatusCode: 200, Header: headers, Body: body}, nil
	})
	var out struct {
		Key    string         `json:"key"`
		Values map[string]any `json:"values"`
	}
	meta, err := c.CallInto(context.Background(), ytr.OpUpdateIssue, []string{"SDK/1"}, ytr.Fields{"flag": false, "nullable": nil}, &out, ytr.Query("notify", "false"))
	if err != nil {
		t.Fatal(err)
	}
	if meta == nil || meta.StatusCode != 200 || meta.Header.Get("X-Request-ID") != "request-1" || out.Key != "SDK-1" || out.Values["number"] != json.Number("9007199254740993") {
		t.Fatalf("meta=%+v out=%+v", meta, out)
	}
	nested, ok := out.Values["nested"].([]any)
	if !ok || len(nested) != 1 {
		t.Fatalf("nested=%#v", out.Values["nested"])
	}
	item, ok := nested[0].(map[string]any)
	if !ok || item["n"] != json.Number("9223372036854775807") {
		t.Fatalf("nested item=%#v", nested[0])
	}
	headers.Set("X-Request-ID", "changed")
	if meta.Header.Get("X-Request-ID") != "request-1" || calls != 1 || body.closes != 1 {
		t.Fatalf("metadata alias or ownership failure: header=%v calls=%d closes=%d", meta.Header, calls, body.closes)
	}
}

func TestIntoValidationPreventsHTTP(t *testing.T) {
	calls := 0
	c := client(t, func(*http.Request) (*http.Response, error) {
		calls++
		return reply(200, `{}`), nil
	})
	var nilTarget *struct{}
	var out struct{}
	for _, tc := range []struct {
		name string
		out  any
	}{{"nil", nil}, {"nil pointer", nilTarget}, {"value", struct{}{}}} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.CallInto(context.Background(), ytr.OpGetCurrentUser, nil, nil, tc.out)
			if _, ok := errors.AsType[*json.InvalidUnmarshalError](err); !ok {
				t.Fatalf("output=%T error=%v; want InvalidUnmarshalError", tc.out, err)
			}
		})
	}
	for _, tc := range []struct {
		name ytr.OperationName
		body any
		want error
	}{
		{"unknown", nil, ytr.ErrUnknownOperation},
		{ytr.OpUploadTemporaryAttachment, nil, ytr.ErrOperationKind},
		{ytr.OpDownloadIssueThumbnail, nil, ytr.ErrOperationKind},
		{ytr.OpGetCurrentUser, ytr.Fields{}, ytr.ErrOperationKind},
	} {
		if _, err := c.CallInto(context.Background(), tc.name, nil, tc.body, &out); !errors.Is(err, tc.want) {
			t.Fatalf("operation=%s error=%v; want %v", tc.name, err, tc.want)
		}
	}
	file := ytr.File{Name: "input.bin", Reader: strings.NewReader("payload")}
	if _, err := c.UploadInto(context.Background(), ytr.OpCreateIssue, nil, file, &out); !errors.Is(err, ytr.ErrOperationKind) {
		t.Fatal(err)
	}
	if _, err := c.UploadInto(context.Background(), "unknown", nil, file, &out); !errors.Is(err, ytr.ErrUnknownOperation) {
		t.Fatal(err)
	}
	if _, err := c.UploadInto(context.Background(), ytr.OpUploadTemporaryAttachment, nil, file, nilTarget); err == nil {
		t.Fatal("nil upload target accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.CallInto(ctx, ytr.OpGetCurrentUser, nil, nil, &out); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := c.CallInto(context.Background(), ytr.OpGetIssue, nil, nil, &out); err == nil {
		t.Fatal("missing path argument accepted")
	}
	if _, err := c.CallInto(context.Background(), ytr.OpGetCurrentUser, nil, nil, &out, nil); err == nil {
		t.Fatal("nil option accepted")
	}
	if _, err := c.CallInto(context.Background(), ytr.OpCreateIssue, nil, make(chan int), &out); err == nil {
		t.Fatal("unencodable request accepted")
	}
	if calls != 0 {
		t.Fatalf("%d invalid inputs reached HTTP", calls)
	}
}

func TestCallIntoResponseBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   string
		length int64
		want   error
		value  any
	}{
		{"exact limit unknown length", "1234", -1, nil, json.Number("1234")},
		{"exact limit known length", "1234", 4, nil, json.Number("1234")},
		{"exact limit with whitespace", "0   ", -1, nil, json.Number("0")},
		{"overflow", "12345", -1, ytr.ErrResponseTooLarge, nil},
		{"trailing whitespace overflow", "0    ", -1, ytr.ErrResponseTooLarge, nil},
		{"short declared length cannot hide overflow", "12345", 2, ytr.ErrResponseTooLarge, nil},
		{"overflow is not read without bound", "0                 ", -1, ytr.ErrResponseTooLarge, nil},
		{"large declared length is advisory", "1234", 1 << 50, nil, json.Number("1234")},
		{"multiple JSON values", "0 1", -1, ytr.ErrInvalidResponse, nil},
		{"trailing invalid data", "0 x", -1, ytr.ErrInvalidResponse, nil},
		{"invalid first value", "{", -1, ytr.ErrInvalidResponse, nil},
		{"empty", "", 0, io.EOF, nil},
		{"whitespace only", "   ", -1, io.EOF, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := &streamingBody{reader: strings.NewReader(tc.body)}
			c := limitedStreamingClient(t, 4, func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, ContentLength: tc.length, Body: stream}, nil
			})
			var out any
			meta, err := c.CallInto(context.Background(), ytr.OpGetCurrentUser, nil, nil, &out)
			if !errors.Is(err, tc.want) || meta == nil || meta.StatusCode != 200 || stream.closes != 1 {
				t.Fatalf("meta=%+v error=%v; want %v; closes=%d", meta, err, tc.want, stream.closes)
			}
			if tc.want == nil && out != tc.value {
				t.Fatalf("decoded %#v; want %#v", out, tc.value)
			}
			if stream.bytes > 5 {
				t.Fatalf("read %d bytes with a four-byte limit; want at most limit+1", stream.bytes)
			}
		})
	}
}

func TestJSONResponseWhitespaceMatchesDecoder(t *testing.T) {
	for _, test := range []struct {
		name, body string
		empty      bool
	}{
		{"JSON whitespace", " \t\r\n", true},
		{"vertical tab", "\v", false},
		{"form feed", "\f", false},
		{"next line", "\u0085", false},
		{"non-breaking space", "\u00a0", false},
		{"em space", "\u2003", false},
		{"line separator", "\u2028", false},
		{"mixed whitespace", " \u00a0\t", false},
	} {
		for _, mode := range []string{"buffered", "into known length", "into unknown length"} {
			t.Run(test.name+"/"+mode, func(t *testing.T) {
				stream := &trackedReader{Reader: strings.NewReader(test.body)}
				c := client(t, func(*http.Request) (*http.Response, error) {
					length := int64(len(test.body))
					if mode == "into unknown length" {
						length = -1
					}
					return &http.Response{StatusCode: 200, ContentLength: length, Body: stream}, nil
				})
				var err error
				if mode == "buffered" {
					_, err = c.GetCurrentUser(t.Context())
				} else {
					var out int
					_, err = c.CallInto(t.Context(), ytr.OpGetCurrentUser, nil, nil, &out)
				}
				want := ytr.ErrInvalidResponse
				if test.empty {
					want = io.EOF
					if mode == "buffered" {
						want = nil
					}
				}
				if !errors.Is(err, want) || !stream.closed {
					t.Fatalf("response %q: error=%v want=%v closed=%v", test.body, err, want, stream.closed)
				}
			})
		}
	}
}

func TestBufferedResponseUsesContentLengthOnlyAsAHint(t *testing.T) {
	for _, length := range []int64{-1, 2, 4, 1 << 50} {
		stream := &streamingBody{reader: strings.NewReader("1234")}
		c := limitedStreamingClient(t, 4, func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, ContentLength: length, Body: stream}, nil
		})
		response, err := c.GetCurrentUser(context.Background())
		if err != nil || response == nil || string(response.Body) != "1234" || stream.closes != 1 {
			t.Fatalf("content length=%d response=%+v error=%v closes=%d", length, response, err, stream.closes)
		}
	}
}

func TestCallIntoKeepsTruncatedHTTPErrorData(t *testing.T) {
	stream := &streamingBody{reader: strings.NewReader("error response exceeds limit")}
	c := limitedStreamingClient(t, 3, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 500, ContentLength: -1, Body: stream}, nil
	})
	out := struct{ Value string }{Value: "unchanged"}
	meta, err := c.CallInto(context.Background(), ytr.OpGetCurrentUser, nil, nil, &out)
	var api *ytr.APIError
	if !errors.As(err, &api) || !errors.Is(err, ytr.ErrResponseTooLarge) {
		t.Fatalf("missing HTTP/limit error: %v", err)
	}
	if meta == nil || meta.StatusCode != 500 || api.StatusCode != 500 || !api.Truncated || string(api.Body) != "err" || out.Value != "unchanged" || stream.bytes > 4 || stream.closes != 1 {
		t.Fatalf("meta=%+v API=%+v out=%+v stream=%+v", meta, api, out, stream)
	}
}

func TestCallIntoKeepsReadFailureAndSizeFailure(t *testing.T) {
	for _, sentinel := range []error{errors.New("reader failed with final bytes"), fmt.Errorf("wrapped EOF failure: %w", io.EOF)} {
		for _, payload := range []string{"1234", "12345"} {
			t.Run(sentinel.Error()+"/"+payload, func(t *testing.T) {
				stream := &streamingBody{reader: &dataAndErrorReader{body: []byte(payload), err: sentinel}}
				c := limitedStreamingClient(t, 4, func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, ContentLength: -1, Body: stream}, nil
				})
				var out any
				_, err := c.CallInto(context.Background(), ytr.OpGetCurrentUser, nil, nil, &out)
				if !errors.Is(err, sentinel) || stream.closes != 1 {
					t.Fatalf("error=%v closes=%d", err, stream.closes)
				}
				if errors.Is(err, ytr.ErrResponseTooLarge) != (len(payload) > 4) {
					t.Fatalf("wrong size classification for %q: %v", payload, err)
				}
			})
		}
	}
}

func TestCallIntoPreservesHTTPErrorAndStopsRedirects(t *testing.T) {
	sentinel := errors.New("failed after complete error JSON")
	body := `{"errorMessages":["limit"],"errors":{"field":["bad"]}}`
	for _, status := range []int{302, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			stream := &streamingBody{reader: &dataAndErrorReader{body: []byte(body), err: sentinel}}
			calls := 0
			c := client(t, func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Host != "api.tracker.yandex.net" {
					t.Errorf("redirect leaked credentials to %s", r.URL.Host)
				}
				return &http.Response{StatusCode: status, Body: stream, Header: http.Header{
					"Retry-After": {"5"}, "Location": {"https://other.test/v3/myself"},
				}}, nil
			})
			out := struct{ Value string }{Value: "untouched"}
			meta, err := c.CallInto(context.Background(), ytr.OpGetCurrentUser, nil, nil, &out)
			var api *ytr.APIError
			if !errors.As(err, &api) || !errors.Is(err, sentinel) {
				t.Fatalf("missing HTTP/read error: %v", err)
			}
			if meta == nil || meta.StatusCode != status || meta.Header.Get("Retry-After") != "5" ||
				api.StatusCode != status || string(api.Body) != body || len(api.ErrorMessages) != 1 ||
				api.ErrorMessages[0] != "limit" || string(api.Errors["field"]) != `["bad"]` ||
				out.Value != "untouched" || calls != 1 || stream.closes != 1 {
				t.Fatalf("meta=%+v API=%+v out=%+v calls=%d closes=%d", meta, api, out, calls, stream.closes)
			}
		})
	}
}

func TestCallIntoPropagatesTransportAndCancellation(t *testing.T) {
	sentinel := errors.New("transport unavailable")
	c := client(t, func(*http.Request) (*http.Response, error) { return nil, sentinel })
	var out struct{ Value string }
	if meta, err := c.CallInto(context.Background(), ytr.OpGetCurrentUser, nil, nil, &out); meta != nil || !errors.Is(err, sentinel) {
		t.Fatalf("meta=%+v error=%v", meta, err)
	}

	stream := &streamingBody{reader: io.MultiReader(strings.NewReader(`{"value":`), failingReader{context.Canceled})}
	c = client(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: stream}, nil
	})
	if _, err := c.CallInto(context.Background(), ytr.OpGetCurrentUser, nil, nil, &out); !errors.Is(err, context.Canceled) || stream.closes != 1 {
		t.Fatalf("error=%v closes=%d", err, stream.closes)
	}
}

func TestCallIntoAndDecodeCanShareAClientConcurrently(t *testing.T) {
	var calls atomic.Int64
	c := client(t, func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return reply(200, `{"value":9007199254740993}`), nil
	})
	raw := &ytr.Response{Body: []byte(`{"value":7,"next":{"value":9}}`)}
	var wg sync.WaitGroup
	for range 24 {
		wg.Go(func() {
			var dynamic struct {
				Value any `json:"value"`
			}
			if _, err := c.CallInto(context.Background(), ytr.OpGetCurrentUser, nil, nil, &dynamic); err != nil || dynamic.Value != json.Number("9007199254740993") {
				t.Errorf("out=%+v error=%v", dynamic, err)
			}
			var typed recursiveDecodedValue
			if err := raw.Decode(&typed); err != nil || typed.Value != 7 || typed.Next == nil || typed.Next.Value != 9 {
				t.Errorf("typed=%+v error=%v", typed, err)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 24 {
		t.Fatalf("HTTP calls=%d; want 24", calls.Load())
	}
}

func TestCallIntoResponseLengthsPreserveCustomSyntaxErrorsOnce(t *testing.T) {
	for _, known := range []bool{true, false} {
		name := "unknown length"
		if known {
			name = "known length"
		}
		t.Run(name, func(t *testing.T) {
			syntax := &json.SyntaxError{Offset: 31}
			jsonTarget := callbackJSON{Value: "before", Err: syntax}
			err := callIntoDocument(t, `"decoded"`, known, &jsonTarget)
			if !errors.Is(err, syntax) || !errors.Is(err, ytr.ErrInvalidResponse) || jsonTarget.Calls != 1 || jsonTarget.Value != "before" {
				t.Fatalf("JSON target=%+v error=%v", jsonTarget, err)
			}
			textTarget := callbackText{Value: "before", Err: syntax}
			err = callIntoDocument(t, `"decoded"`, known, &textTarget)
			if !errors.Is(err, syntax) || !errors.Is(err, ytr.ErrInvalidResponse) || textTarget.Calls != 1 || textTarget.Value != "before" {
				t.Fatalf("text target=%+v error=%v", textTarget, err)
			}
			var nested struct {
				Item syntaxCallbackJSON `json:"item"`
			}
			err = callIntoDocument(t, `{"item":"decoded"}`, known, &nested)
			var nestedSyntax *json.SyntaxError
			if !errors.As(err, &nestedSyntax) || nestedSyntax.Offset != 17 || !errors.Is(err, ytr.ErrInvalidResponse) || nested.Item.Calls != 1 {
				t.Fatalf("nested target=%+v error=%v", nested, err)
			}
		})
	}
}

func TestCallIntoResponseLengthsPreserveTrailingCallbackMutation(t *testing.T) {
	for _, known := range []bool{true, false} {
		for _, suffix := range []string{" {}", " false", " broken"} {
			name := "unknown length"
			if known {
				name = "known length"
			}
			t.Run(name+suffix, func(t *testing.T) {
				jsonTarget := callbackJSON{Value: "before"}
				err := callIntoDocument(t, `"decoded"`+suffix, known, &jsonTarget)
				if !errors.Is(err, ytr.ErrInvalidResponse) || jsonTarget.Calls != 1 || jsonTarget.Value != "decoded" {
					t.Fatalf("JSON target=%+v error=%v", jsonTarget, err)
				}
				textTarget := callbackText{Value: "before"}
				err = callIntoDocument(t, `"decoded"`+suffix, known, &textTarget)
				if !errors.Is(err, ytr.ErrInvalidResponse) || textTarget.Calls != 1 || textTarget.Value != "decoded" {
					t.Fatalf("text target=%+v error=%v", textTarget, err)
				}
				var nested struct {
					Item callbackJSON `json:"item"`
				}
				err = callIntoDocument(t, `{"item":"decoded"}`+suffix, known, &nested)
				if !errors.Is(err, ytr.ErrInvalidResponse) || nested.Item.Calls != 1 || nested.Item.Value != "decoded" {
					t.Fatalf("nested target=%+v error=%v", nested, err)
				}
			})
		}
	}
}

func TestCallIntoResponseLengthsPreserveConcreteErrorMutation(t *testing.T) {
	for _, known := range []bool{true, false} {
		for _, test := range []struct {
			name        string
			body        string
			wantValue   int
			wantName    string
			wantInvalid bool
			wantTypeErr bool
		}{
			{"trailing object", `{"value":7,"name":"new"} {}`, 7, "new", true, false},
			{"trailing invalid bytes", `{"value":7,"name":"new"} broken`, 7, "new", true, false},
			{"malformed first object", `{"value":7,"name":`, 2, "before", true, false},
			{"type error after valid field", `{"name":"new","value":"wrong"}`, 2, "new", false, true},
		} {
			name := "unknown length/"
			if known {
				name = "known length/"
			}
			t.Run(name+test.name, func(t *testing.T) {
				target := struct {
					Value int    `json:"value"`
					Name  string `json:"name"`
				}{Value: 2, Name: "before"}
				err := callIntoDocument(t, test.body, known, &target)
				var typeError *json.UnmarshalTypeError
				if err == nil || errors.Is(err, ytr.ErrInvalidResponse) != test.wantInvalid || errors.As(err, &typeError) != test.wantTypeErr || target.Value != test.wantValue || target.Name != test.wantName {
					t.Fatalf("target=%+v error=%v; want value=%d name=%q invalid=%v typeError=%v", target, err, test.wantValue, test.wantName, test.wantInvalid, test.wantTypeErr)
				}
			})
		}
		name := "unknown length/malformed callback document"
		if known {
			name = "known length/malformed callback document"
		}
		t.Run(name, func(t *testing.T) {
			target := struct {
				Value int          `json:"value"`
				Item  callbackJSON `json:"item"`
			}{Value: 2, Item: callbackJSON{Value: "before"}}
			err := callIntoDocument(t, `{"value":7,"item":"decoded",`, known, &target)
			if !errors.Is(err, ytr.ErrInvalidResponse) || target.Value != 2 || target.Item.Calls != 0 || target.Item.Value != "before" {
				t.Fatalf("malformed first document changed target=%+v error=%v", target, err)
			}
		})
	}
}

func TestCallIntoResponseLengthsRespectCustomNumberSemantics(t *testing.T) {
	for _, known := range []bool{true, false} {
		for _, ownUseNumber := range []bool{true, false} {
			name := "unknown length/"
			if known {
				name = "known length/"
			}
			if ownUseNumber {
				name += "custom UseNumber"
			} else {
				name += "custom float64"
			}
			t.Run(name, func(t *testing.T) {
				target := ownedDynamicJSON{UseNumber: ownUseNumber}
				err := callIntoDocument(t, `{"number":9007199254740993,"nested":[9223372036854775807]}`, known, &target)
				var number, nested any = json.Number("9007199254740993"), json.Number("9223372036854775807")
				if !ownUseNumber {
					number, nested = float64(9007199254740993), float64(9223372036854775807)
				}
				want := map[string]any{"number": number, "nested": []any{nested}}
				if err != nil || target.Calls != 1 || !reflect.DeepEqual(target.Value, want) {
					t.Fatalf("custom target=%+v error=%v; want %#v", target, err, want)
				}
			})
		}
	}
}

func TestCallIntoResponseLengthsPreserveNativeNumbersBesideCustomCodec(t *testing.T) {
	for _, known := range []bool{true, false} {
		name := "unknown length"
		if known {
			name = "known length"
		}
		t.Run(name, func(t *testing.T) {
			var target struct {
				Custom ownedDynamicJSON `json:"custom"`
				Native any              `json:"native"`
			}
			err := callIntoDocument(t, `{"custom":{"id":9007199254740993},"native":{"id":9007199254740993,"nested":[{"id":9223372036854775807}]}}`, known, &target)
			wantCustom := map[string]any{"id": float64(9007199254740993)}
			wantNative := map[string]any{"id": json.Number("9007199254740993"), "nested": []any{map[string]any{"id": json.Number("9223372036854775807")}}}
			if err != nil || target.Custom.Calls != 1 || !reflect.DeepEqual(target.Custom.Value, wantCustom) || !reflect.DeepEqual(target.Native, wantNative) {
				t.Fatalf("mixed target=%+v error=%v; want native=%#v", target, err, wantNative)
			}
		})
	}
}

func TestCallAndCallIntoRespectExactSuccessStatusBoundaries(t *testing.T) {
	for _, mode := range []string{"Call", "CallInto"} {
		for _, test := range []struct {
			status  int
			success bool
		}{{199, false}, {200, true}, {299, true}, {300, false}} {
			t.Run(mode+"/"+strconv.Itoa(test.status), func(t *testing.T) {
				document := `{"value":"decoded","errorMessages":["outside success range"],"errors":{"field":"invalid"}}`
				stream := &streamingBody{reader: strings.NewReader(document)}
				sourceHeader := http.Header{"X-Request-Id": {"status-boundary"}}
				calls := 0
				c := client(t, func(*http.Request) (*http.Response, error) {
					calls++
					return &http.Response{StatusCode: test.status, Header: sourceHeader, ContentLength: int64(len(document)), Body: stream}, nil
				})
				target := struct {
					Value string `json:"value"`
				}{Value: "before"}
				var err error
				var gotStatus int
				var gotHeader http.Header
				if mode == "Call" {
					var response *ytr.Response
					response, err = c.Call(context.Background(), ytr.OpGetCurrentUser, nil, nil)
					if response == nil || string(response.Body) != document {
						t.Fatalf("raw response=%+v error=%v", response, err)
					}
					gotStatus, gotHeader = response.StatusCode, response.Header
					if test.success {
						if decodeErr := response.Decode(&target); decodeErr != nil {
							t.Fatal(decodeErr)
						}
					}
				} else {
					var metadata *ytr.ResponseMetadata
					metadata, err = c.CallInto(context.Background(), ytr.OpGetCurrentUser, nil, nil, &target)
					if metadata == nil {
						t.Fatalf("missing metadata: %v", err)
					}
					gotStatus, gotHeader = metadata.StatusCode, metadata.Header
				}
				if gotStatus != test.status || gotHeader.Get("X-Request-ID") != "status-boundary" || calls != 1 || stream.closes != 1 {
					t.Fatalf("status=%d header=%v requests=%d closes=%d", gotStatus, gotHeader, calls, stream.closes)
				}
				var api *ytr.APIError
				if test.success {
					if err != nil || target.Value != "decoded" {
						t.Fatalf("success target=%+v error=%v", target, err)
					}
				} else {
					if !errors.As(err, &api) {
						t.Fatalf("status %d error=%T %v; want APIError", test.status, err, err)
					}
					if api.StatusCode != test.status || string(api.Body) != document || api.Header.Get("X-Request-ID") != "status-boundary" || api.Truncated || api.Cause != nil || !reflect.DeepEqual(api.ErrorMessages, []string{"outside success range"}) || string(api.Errors["field"]) != `"invalid"` || target.Value != "before" {
						t.Fatalf("HTTP failure API=%+v target=%+v", api, target)
					}
				}
				sourceHeader.Set("X-Request-ID", "changed")
				if gotHeader.Get("X-Request-ID") != "status-boundary" || (api != nil && api.Header.Get("X-Request-ID") != "status-boundary") {
					t.Fatalf("headers alias transport state: response=%v API=%+v", gotHeader, api)
				}
			})
		}
	}
}

// ContentLength is untrusted. A small bounded response must not reserve memory
// for a multi-megabyte hint, and a huge configured limit must not overflow Grow.
func TestResponseLimitsBoundUntrustedLengthAllocations(t *testing.T) {
	const document = `{"id":"fixture"}`
	largestSize := int64(^uint64(0) >> 1)
	for _, test := range []struct {
		name        string
		limit, hint int64
	}{
		{"hint exceeds response limit", 128, 8 << 20},
		{"huge permitted response", largestSize - 1, largestSize - 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			stream := &streamingBody{reader: strings.NewReader(document)}
			c, err := ytr.New(ytr.Config{Organization: ytr.Organization{Kind: ytr.Organization360, ID: "fixture"}, TokenSource: ytr.OAuthToken("fixture"), MaxResponseBytes: test.limit, HTTPClient: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: make(http.Header), ContentLength: test.hint, Body: stream}, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			// This serial test has no concurrent requests; the generous 256 KiB budget
			// distinguishes bounded bookkeeping from trusting the 8 MiB length header.
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			response, err := c.GetCurrentUser(t.Context())
			runtime.ReadMemStats(&after)
			if err != nil || response == nil || string(response.Body) != document || stream.closes != 1 {
				t.Fatalf("response=%+v error=%v closes=%d", response, err, stream.closes)
			}
			if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 256<<10 {
				t.Fatalf("%d allocated bytes for %d-byte response with untrusted ContentLength=%d", allocated, len(document), test.hint)
			}
		})
	}
}

func TestExactStatusAndJSONBoundaries(t *testing.T) {
	for _, code := range []int{199, 300} {
		c := client(t, func(*http.Request) (*http.Response, error) { return reply(code, ""), nil })
		if _, err := c.GetCurrentUser(context.Background()); err == nil {
			t.Fatal(code)
		}
		if _, err := c.DownloadIssueThumbnail(context.Background(), "Q-1", "2"); err == nil {
			t.Fatal(code)
		}
	}
	c := client(t, func(*http.Request) (*http.Response, error) { return reply(200, "!"), nil })
	if _, err := c.GetCurrentUser(context.Background()); !errors.Is(err, ytr.ErrInvalidResponse) {
		t.Fatal(err)
	}
}

func TestSizeBoundHoldsWhenReaderAlsoReturnsError(t *testing.T) {
	cfg := validConfig()
	cfg.MaxResponseBytes = 4
	cfg.HTTPClient = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		v := reply(200, "")
		v.Body = io.NopCloser(&unexpectedEOFReader{})
		return v, nil
	})}
	c, err := ytr.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	response, err := c.GetCurrentUser(context.Background())
	if !errors.Is(err, ytr.ErrResponseTooLarge) || !errors.Is(err, io.ErrUnexpectedEOF) || string(response.Body) != "1234" {
		t.Fatalf("body=%s err=%v", response.Body, err)
	}
}

func BenchmarkLookupOperation(b *testing.B) {
	catalog := ytr.Operations()
	for _, position := range []struct {
		name  string
		index int
	}{
		{"first", 0},
		{"middle", len(catalog) / 2},
		{"last", len(catalog) - 1},
	} {
		want := catalog[position.index]
		b.Run(position.name+"/"+string(want.Name), func(b *testing.B) {
			b.ReportAllocs()
			var got ytr.Operation
			for b.Loop() {
				var err error
				got, err = ytr.LookupOperation(want.Name)
				if err != nil {
					b.Fatal(err)
				}
			}
			if got != want {
				b.Fatalf("operation: %+v; want %+v", got, want)
			}
			runtime.KeepAlive(got)
		})
	}
}

func BenchmarkIssueDispatch(b *testing.B) {
	_, expected := benchmarkIssueJSON(b, 128)
	data, err := json.Marshal(expected[0])
	if err != nil {
		b.Fatal(err)
	}
	for _, named := range []bool{true, false} {
		name := "DynamicCall"
		if named {
			name = "NamedGetIssue"
		}
		b.Run(name, func(b *testing.B) {
			fixture := newBenchmarkJSONFixture(b, data, true)
			args := []string{"SDK/Я 1"}
			ctx := b.Context()
			b.ReportAllocs()
			var response *ytr.Response
			for b.Loop() {
				if named {
					response, err = fixture.client.GetIssue(ctx, args[0])
				} else {
					response, err = fixture.client.Call(ctx, ytr.OpGetIssue, args, nil)
				}
				if err != nil {
					b.Fatal(err)
				}
			}
			fixture.checkRequest(b, http.MethodGet, "/v3/issues/SDK%2F%D0%AF%201")
			if response.StatusCode != http.StatusOK || !bytes.Equal(response.Body, data) {
				b.Fatalf("response: %+v", response)
			}
			runtime.KeepAlive(response)
		})
	}
}

// These benchmarks report cumulative allocated bytes per operation (B/op), not
// peak live memory. Every iteration decodes into fresh slice storage.
func BenchmarkIssueSearch(b *testing.B) {
	for _, size := range benchmarkJSONSizes() {
		data, expected := benchmarkIssueJSON(b, size.bytes)
		b.Run(size.name, func(b *testing.B) {
			for _, known := range []bool{true, false} {
				lengthName := "UnknownLength"
				if known {
					lengthName = "KnownLength"
				}
				b.Run(lengthName, func(b *testing.B) {
					for _, stream := range []bool{false, true} {
						mode := "BufferedDecode"
						if stream {
							mode = "CallInto"
						}
						b.Run(mode, func(b *testing.B) {
							fixture := newBenchmarkJSONFixture(b, data, known)
							body := ytr.Fields{"query": "Queue: SDK"}
							ctx := b.Context()
							b.SetBytes(int64(len(data)))
							b.ReportAllocs()
							var got []benchmarkIssue
							for b.Loop() {
								got = nil
								if stream {
									if _, err := fixture.client.CallInto(ctx, ytr.OpSearchIssues, nil, body, &got); err != nil {
										b.Fatal(err)
									}
								} else {
									response, err := fixture.client.SearchIssues(ctx, body)
									if err != nil {
										b.Fatal(err)
									}
									if err = response.Decode(&got); err != nil {
										b.Fatal(err)
									}
								}
							}
							fixture.checkRequest(b, http.MethodPost, "/v3/issues/_search")
							checkBenchmarkIssues(b, got, expected)
							runtime.KeepAlive(got)
						})
					}
				})
			}
		})
	}
}

func newBenchmarkClient(b *testing.B, fn transportFunc) *ytr.Client {
	b.Helper()
	c, err := ytr.New(ytr.Config{
		Organization: ytr.Organization{Kind: ytr.Organization360, ID: "benchmark"},
		TokenSource:  ytr.OAuthToken("fixture-token"),
		BaseURL:      "https://benchmark.invalid",
		HTTPClient:   &http.Client{Transport: fn, Timeout: 0},
	})
	if err != nil {
		b.Fatal(err)
	}
	return c
}

func newBenchmarkJSONFixture(b *testing.B, data []byte, known bool) *benchmarkJSONFixture {
	b.Helper()
	fixture := new(benchmarkJSONFixture)
	fixture.client = newBenchmarkClient(b, func(request *http.Request) (*http.Response, error) {
		if request.Body != nil {
			request.Body.Close()
		}
		fixture.request = request
		fixture.calls++
		length := int64(-1)
		if known {
			length = int64(len(data))
		}
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        make(http.Header),
			ContentLength: length,
			Body:          io.NopCloser(bytes.NewReader(data)),
		}, nil
	})
	return fixture
}

func (fixture *benchmarkJSONFixture) checkRequest(b *testing.B, method, path string) {
	b.Helper()
	if fixture.calls != b.N || fixture.request == nil {
		b.Fatalf("requests=%d; want %d", fixture.calls, b.N)
	}
	if fixture.request.Method != method || fixture.request.URL.EscapedPath() != path {
		b.Fatalf("request: %s %s; want %s %s", fixture.request.Method, fixture.request.URL, method, path)
	}
}

func benchmarkJSONSizes() []struct {
	name  string
	bytes int
} {
	return []struct {
		name  string
		bytes int
	}{
		{"128B", 128},
		{"64KiB", 64 << 10},
		{"1MiB", 1 << 20},
	}
}

func benchmarkIssueJSON(b *testing.B, size int) ([]byte, []benchmarkIssue) {
	b.Helper()
	expected := make([]benchmarkIssue, size/128)
	for i := range expected {
		id := strconv.Itoa(i + 1)
		expected[i] = benchmarkIssue{ID: id, Key: "SDK-" + id, Summary: "Issue " + id + ": inspect request and response"}
		record, err := json.Marshal(expected[i])
		if err != nil {
			b.Fatal(err)
		}
		recordSize := 127
		if i == 0 {
			recordSize = 126
		}
		expected[i].Summary += strings.Repeat("x", recordSize-len(record))
	}
	data, err := json.Marshal(expected)
	if err != nil {
		b.Fatal(err)
	}
	if len(data) != size {
		b.Fatalf("fixture is %d bytes; want %d", len(data), size)
	}
	return data, expected
}

func checkBenchmarkIssues(b *testing.B, got, expected []benchmarkIssue) {
	b.Helper()
	if len(got) != len(expected) {
		b.Fatalf("decoded %d records; want %d", len(got), len(expected))
	}
	for i := range expected {
		if got[i] != expected[i] {
			b.Fatalf("record %d: %+v; want %+v", i, got[i], expected[i])
		}
	}
}

func (r *streamingBody) Read(p []byte) (int, error) {
	r.reads++
	n, err := r.reader.Read(p)
	r.bytes += n
	return n, err
}

func (r *streamingBody) Close() error {
	r.closes++
	return nil
}

func (r *dataAndErrorReader) Read(p []byte) (int, error) {
	if r.sent {
		return 0, io.EOF
	}
	r.sent = true
	return copy(p, r.body), r.err
}

func limitedStreamingClient(t *testing.T, limit int64, fn transportFunc) *ytr.Client {
	t.Helper()
	cfg := validConfig()
	cfg.MaxResponseBytes = limit
	cfg.HTTPClient = &http.Client{Transport: fn}
	c, err := ytr.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func callIntoDocument(t *testing.T, document string, known bool, out any) error {
	t.Helper()
	body := &streamingBody{reader: strings.NewReader(document)}
	calls := 0
	c := client(t, func(*http.Request) (*http.Response, error) {
		calls++
		length := int64(-1)
		if known {
			length = int64(len(document))
		}
		return &http.Response{StatusCode: http.StatusOK, ContentLength: length, Header: make(http.Header), Body: body}, nil
	})
	meta, err := c.CallInto(context.Background(), ytr.OpGetCurrentUser, nil, nil, out)
	if meta == nil || meta.StatusCode != http.StatusOK || calls != 1 || body.closes != 1 {
		t.Fatalf("metadata=%+v requests=%d closes=%d error=%v", meta, calls, body.closes, err)
	}
	return err
}

func (r *unexpectedEOFReader) Read(out []byte) (int, error) {
	if r.sent {
		return 0, io.EOF
	}
	r.sent = true
	n := copy(out, "12345")
	return n, io.ErrUnexpectedEOF
}
