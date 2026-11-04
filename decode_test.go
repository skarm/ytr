package ytr_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"runtime"
	"testing"

	"github.com/skarm/ytr"
)

type callbackJSON struct {
	Calls int
	Value string
	Err   error
}

type callbackText struct {
	Calls int
	Value string
	Err   error
}

type syntaxCallbackJSON struct{ Calls int }

type syntaxCallbackText struct{ Calls int }

type valueReceiverSyntaxJSON struct{ Calls *int }

type valueReceiverSyntaxText struct{ Calls *int }

type ownedDynamicJSON struct {
	Calls     int
	UseNumber bool
	Value     any
}

type recursiveDynamicValue struct {
	Next  *recursiveDynamicValue `json:"next"`
	Value any                    `json:"value"`
}

type recursiveDecodedValue struct {
	Value int                    `json:"value"`
	Next  *recursiveDecodedValue `json:"next"`
}

type embeddedDynamicValue struct {
	Value any `json:"value"`
}

func TestTypedDecodePreservesErrorsAndRequiresAResponse(t *testing.T) {
	sentinel := errors.New("application failure")
	got, err := ytr.Decode[struct{ Key string }](&ytr.Response{Body: []byte(`{"Key":"unexpected"}`)}, sentinel)
	if !errors.Is(err, sentinel) || got.Key != "" {
		t.Fatal(got, err)
	}
	if _, err := ytr.Decode[string](nil, nil); !errors.Is(err, ytr.ErrInvalidResponse) {
		t.Fatal(err)
	}
	if _, err := ytr.Decode[string](&ytr.Response{Body: []byte(`{}`)}, nil); err == nil {
		t.Fatal("type mismatch accepted")
	}
}

func TestResponseDecodePreservesCustomCallbacks(t *testing.T) {
	syntax := &json.SyntaxError{Offset: 7}
	sentinel := errors.New("custom decode failed")
	for _, callbackErr := range []error{nil, sentinel, syntax} {
		jsonTarget := callbackJSON{Err: callbackErr}
		err := (&ytr.Response{Body: []byte(`"decoded"`)}).Decode(&jsonTarget)
		if !errors.Is(err, callbackErr) || jsonTarget.Calls != 1 || (callbackErr == nil && jsonTarget.Value != "decoded") {
			t.Fatalf("JSON target=%+v error=%v; want %v", jsonTarget, err, callbackErr)
		}
		textTarget := callbackText{Err: callbackErr}
		err = (&ytr.Response{Body: []byte(`"decoded"`)}).Decode(&textTarget)
		if !errors.Is(err, callbackErr) || textTarget.Calls != 1 || (callbackErr == nil && textTarget.Value != "decoded") {
			t.Fatalf("text target=%+v error=%v; want %v", textTarget, err, callbackErr)
		}
	}
	jsonTarget := callbackJSON{}
	if err := (&ytr.Response{Body: []byte(`"decoded" {}`)}).Decode(&jsonTarget); !errors.Is(err, ytr.ErrInvalidResponse) || jsonTarget.Calls != 1 || jsonTarget.Value != "decoded" {
		t.Fatalf("trailing data changed callback semantics: target=%+v error=%v", jsonTarget, err)
	}
}

func TestResponseDecodeDoesNotRepeatConcreteNestedCustomCallbacks(t *testing.T) {
	var jsonTarget struct {
		Item syntaxCallbackJSON `json:"item"`
	}
	err := (&ytr.Response{Body: []byte(`{"item":"value"}`)}).Decode(&jsonTarget)
	var syntax *json.SyntaxError
	if !errors.As(err, &syntax) || syntax.Offset != 17 || jsonTarget.Item.Calls != 1 {
		t.Fatalf("nested JSON target=%+v error=%v", jsonTarget, err)
	}
	var textTarget struct {
		Item syntaxCallbackText `json:"item"`
	}
	err = (&ytr.Response{Body: []byte(`{"item":"value"}`)}).Decode(&textTarget)
	if !errors.As(err, &syntax) || syntax.Offset != 19 || textTarget.Item.Calls != 1 {
		t.Fatalf("nested text target=%+v error=%v", textTarget, err)
	}
}

func TestResponseDecodePreservesTrailingErrorMutationAndEmptyInput(t *testing.T) {
	for _, suffix := range []string{" {}", " false", " broken"} {
		target := struct {
			Value int `json:"value"`
		}{Value: 2}
		err := (&ytr.Response{Body: []byte(`{"value":7}` + suffix)}).Decode(&target)
		if !errors.Is(err, ytr.ErrInvalidResponse) || target.Value != 7 {
			t.Fatalf("suffix=%q value=%d error=%v", suffix, target.Value, err)
		}
	}
	for _, data := range []string{"", " \r\n\t"} {
		target := struct{ Value int }{Value: 2}
		if err := (&ytr.Response{Body: []byte(data)}).Decode(&target); !errors.Is(err, io.EOF) || target.Value != 2 {
			t.Fatalf("empty body=%q target=%+v error=%v", data, target, err)
		}
	}
	target := struct {
		Value int `json:"value"`
	}{Value: 2}
	if err := (&ytr.Response{Body: []byte(`{"value":`)}).Decode(&target); err == nil || target.Value != 2 {
		t.Fatalf("invalid first value mutated target: value=%d error=%v", target.Value, err)
	}
	var nilTarget *recursiveDecodedValue
	for _, output := range []any{nil, nilTarget, recursiveDecodedValue{}} {
		var invalid *json.InvalidUnmarshalError
		if err := (&ytr.Response{Body: []byte(`{}`)}).Decode(output); !errors.As(err, &invalid) {
			t.Fatalf("output=%T error=%v", output, err)
		}
	}
}

func TestResponseDecodePreservesEmbeddedDynamicFieldsAndRawBodyChanges(t *testing.T) {
	var out struct {
		embeddedDynamicValue
		Items []map[string]any `json:"items"`
	}
	raw := &ytr.Response{Body: []byte(`{"value":9007199254740993,"items":[{"id":9223372036854775807}]}`)}
	if err := raw.Decode(&out); err != nil || out.Value != json.Number("9007199254740993") || len(out.Items) != 1 || out.Items[0]["id"] != json.Number("9223372036854775807") {
		t.Fatalf("dynamic out=%+v error=%v", out, err)
	}
	body := []byte(`{"value":1}`)
	raw = &ytr.Response{Body: body}
	var typed struct {
		Value int `json:"value"`
	}
	if err := raw.Decode(&typed); err != nil || typed.Value != 1 {
		t.Fatalf("initial decode: %+v %v", typed, err)
	}
	body[len(body)-2] = '2'
	if err := raw.Decode(&typed); err != nil || typed.Value != 2 {
		t.Fatalf("modified raw body was ignored: %+v %v", typed, err)
	}
	var nodes []recursiveDecodedValue
	raw.Body = []byte(`[{"value":1,"next":{"value":2}},{"value":3}]`)
	if err := raw.Decode(&nodes); err != nil || len(nodes) != 2 || nodes[0].Next == nil || nodes[0].Next.Value != 2 || nodes[1].Value != 3 {
		t.Fatalf("recursive out=%+v error=%v", nodes, err)
	}
	var values map[string][]any
	raw.Body = []byte(`{"ids":[9007199254740993,{"nested":9223372036854775807}]}`)
	if err := raw.Decode(&values); err != nil {
		t.Fatal(err)
	}
	if len(values["ids"]) != 2 {
		t.Fatalf("missing IDs: %#v", values)
	}
	if values["ids"][0] != json.Number("9007199254740993") || !reflect.DeepEqual(values["ids"][1], map[string]any{"nested": json.Number("9223372036854775807")}) {
		t.Fatalf("map values=%#v", values)
	}
}

func TestResponseDecodePreservesNumbersBehindRecursivePointers(t *testing.T) {
	var value recursiveDynamicValue
	err := (&ytr.Response{Body: []byte(`{"value":9007199254740993,"next":{"value":9223372036854775807}}`)}).Decode(&value)
	if err != nil || value.Value != json.Number("9007199254740993") || value.Next == nil || value.Next.Value != json.Number("9223372036854775807") {
		t.Fatalf("recursive dynamic target=%+v error=%v", value, err)
	}
}

func TestResponseDecodeDoesNotRepeatValueReceiverCustomCallbacks(t *testing.T) {
	jsonCalls, textCalls := 0, 0
	for _, test := range []struct {
		name   string
		out    any
		calls  *int
		offset int64
	}{
		{"JSON", &valueReceiverSyntaxJSON{Calls: &jsonCalls}, &jsonCalls, 41},
		{"text", &valueReceiverSyntaxText{Calls: &textCalls}, &textCalls, 43},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := (&ytr.Response{Body: []byte(`"decoded"`)}).Decode(test.out)
			var syntax *json.SyntaxError
			if !errors.As(err, &syntax) || syntax.Offset != test.offset || *test.calls != 1 {
				t.Fatalf("value receiver %s: calls=%d error=%v", test.name, *test.calls, err)
			}
		})
	}
}

func BenchmarkConcreteJSONDecode(b *testing.B) {
	for _, size := range benchmarkJSONSizes() {
		data, expected := benchmarkIssueJSON(b, size.bytes)
		b.Run(size.name, func(b *testing.B) {
			for _, standard := range []bool{false, true} {
				name := "ResponseDecode"
				if standard {
					name = "JSONUnmarshal"
				}
				b.Run(name, func(b *testing.B) {
					response := &ytr.Response{StatusCode: http.StatusOK, Body: data}
					b.SetBytes(int64(len(data)))
					b.ReportAllocs()
					var got []benchmarkIssue
					for b.Loop() {
						got = nil
						var err error
						if standard {
							err = json.Unmarshal(data, &got)
						} else {
							err = response.Decode(&got)
						}
						if err != nil {
							b.Fatal(err)
						}
					}
					checkBenchmarkIssues(b, got, expected)
					runtime.KeepAlive(got)
				})
			}
		})
	}
}

func (v *callbackJSON) UnmarshalJSON(data []byte) error {
	v.Calls++
	if v.Err != nil {
		return v.Err
	}
	return json.Unmarshal(data, &v.Value)
}

func (v *callbackText) UnmarshalText(data []byte) error {
	v.Calls++
	if v.Err != nil {
		return v.Err
	}
	v.Value = string(data)
	return nil
}

func (v *syntaxCallbackJSON) UnmarshalJSON([]byte) error {
	v.Calls++
	return &json.SyntaxError{Offset: 17}
}

func (v *syntaxCallbackText) UnmarshalText([]byte) error {
	v.Calls++
	return &json.SyntaxError{Offset: 19}
}

func (v valueReceiverSyntaxJSON) UnmarshalJSON([]byte) error {
	*v.Calls += 1
	return &json.SyntaxError{Offset: 41}
}

func (v valueReceiverSyntaxText) UnmarshalText([]byte) error {
	*v.Calls += 1
	return &json.SyntaxError{Offset: 43}
}

func (v *ownedDynamicJSON) UnmarshalJSON(data []byte) error {
	v.Calls++
	decoder := json.NewDecoder(bytes.NewReader(data))
	if v.UseNumber {
		decoder.UseNumber()
	}
	return decoder.Decode(&v.Value)
}
