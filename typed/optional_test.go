package typed_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/skarm/ytr/typed"
)

type optionalWire struct {
	Notify  typed.Optional[bool]     `json:"notify,omitzero"`
	Count   typed.Optional[int]      `json:"count,omitzero"`
	Summary typed.Optional[string]   `json:"summary,omitzero"`
	Tags    typed.Optional[[]string] `json:"tags,omitzero"`
}

type optionalRequestFields struct {
	Absent typed.Optional[string]   `json:"absent,omitzero"`
	Text   typed.Optional[string]   `json:"text,omitzero"`
	Flag   typed.Optional[bool]     `json:"flag,omitzero"`
	Count  typed.Optional[int64]    `json:"count,omitzero"`
	Items  typed.Optional[[]string] `json:"items,omitzero"`
	Clear  typed.Optional[string]   `json:"clear,omitzero"`
}

func TestOptionalStateTransitions(t *testing.T) {
	t.Parallel()
	var value typed.Optional[string]
	checkOptional(t, value, "", false, false, false)
	value.Set("")
	checkOptional(t, value, "", true, true, false)
	value.Set("before")
	checkOptional(t, value, "before", true, true, false)
	value.SetNull()
	checkOptional(t, value, "", false, true, true)
	value.Set("after")
	checkOptional(t, value, "after", true, true, false)
	value.Unset()
	checkOptional(t, value, "", false, false, false)
	checkOptional(t, typed.Value(false), false, true, true, false)
	checkOptional(t, typed.Value(0), 0, true, true, false)
	checkOptional(t, typed.Null[int](), 0, false, true, true)
}

func TestOptionalMarshalPresence(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		value optionalWire
		want  string
	}{
		{"absent", optionalWire{}, `{}`},
		{"explicit zero values", optionalWire{Notify: typed.Value(false), Count: typed.Value(0), Summary: typed.Value(""), Tags: typed.Value([]string{})}, `{"notify":false,"count":0,"summary":"","tags":[]}`},
		{"explicit null", optionalWire{Notify: typed.Null[bool](), Count: typed.Null[int](), Summary: typed.Null[string](), Tags: typed.Null[[]string]()}, `{"notify":null,"count":null,"summary":null,"tags":null}`},
		{"values", optionalWire{Notify: typed.Value(true), Count: typed.Value(7), Summary: typed.Value("summary"), Tags: typed.Value([]string{"one", "two"})}, `{"notify":true,"count":7,"summary":"summary","tags":["one","two"]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			data, err := json.Marshal(test.value)
			if err != nil || string(data) != test.want {
				t.Fatalf("wire: %s, error: %v; want %s", data, err, test.want)
			}
		})
	}
}

func TestOptionalUnmarshalReplacesCompositeAndNullState(t *testing.T) {
	t.Parallel()
	value := typed.Value(map[string]int{"old": 9, "shared": 5})
	if err := json.Unmarshal([]byte(`{"new":3}`), &value); err != nil {
		t.Fatal(err)
	}
	got, ok := value.Get()
	if !ok || !reflect.DeepEqual(got, map[string]int{"new": 3}) || value.IsNull() || value.IsZero() {
		t.Fatalf("decoded map: %v, present=%v, null=%v, zero=%v", got, ok, value.IsNull(), value.IsZero())
	}
	if err := json.Unmarshal([]byte(`null`), &value); err != nil {
		t.Fatal(err)
	}
	got, ok = value.Get()
	if got != nil || ok || !value.IsSet() || !value.IsNull() || value.IsZero() {
		t.Fatalf("null map: %v, present=%v, set=%v, null=%v", got, ok, value.IsSet(), value.IsNull())
	}
	if err := json.Unmarshal([]byte(`{}`), &value); err != nil {
		t.Fatal(err)
	}
	got, ok = value.Get()
	if !ok || got == nil || len(got) != 0 || value.IsNull() {
		t.Fatalf("empty map after null: %v, present=%v, null=%v", got, ok, value.IsNull())
	}
	tags := typed.Value([]string{"old", "retained"})
	if err := json.Unmarshal([]byte(`[]`), &tags); err != nil {
		t.Fatal(err)
	}
	gotTags, tagsOK := tags.Get()
	if !tagsOK || gotTags == nil || len(gotTags) != 0 {
		t.Fatalf("empty tags: %#v, present=%v", gotTags, tagsOK)
	}
	flag := typed.Null[bool]()
	if err := json.Unmarshal([]byte(`false`), &flag); err != nil {
		t.Fatal(err)
	}
	checkOptional(t, flag, false, true, true, false)
	if err := flag.UnmarshalJSON([]byte(" \n null 	")); err != nil {
		t.Fatal(err)
	}
	checkOptional(t, flag, false, false, true, true)
}

func TestOptionalUnmarshalFailurePreservesValueAndAlias(t *testing.T) {
	t.Parallel()
	for _, input := range []string{`{"new":1,"bad":"text"}`, `true`, `{"new":`, `null false`} {
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			original := map[string]int{"old": 9}
			value := typed.Value(original)
			if err := json.Unmarshal([]byte(input), &value); err == nil {
				t.Fatal("invalid map value was accepted")
			}
			got, ok := value.Get()
			if !ok || !value.IsSet() || value.IsNull() || value.IsZero() || !reflect.DeepEqual(got, map[string]int{"old": 9}) || !reflect.DeepEqual(original, map[string]int{"old": 9}) {
				t.Fatalf("failed decode changed state: value=%v alias=%v, present=%v", got, original, ok)
			}
		})
	}
	value := typed.Null[int]()
	if err := json.Unmarshal([]byte(`"wrong type"`), &value); err == nil {
		t.Fatal("invalid number was accepted")
	}
	checkOptional(t, value, 0, false, true, true)
	var unset typed.Optional[string]
	if err := json.Unmarshal([]byte(`false`), &unset); err == nil {
		t.Fatal("invalid unset string was accepted")
	}
	checkOptional(t, unset, "", false, false, false)
}

func TestOptionalRequestValuesPreserveOmissionNullAndZero(t *testing.T) {
	fields := optionalRequestFields{
		Text: typed.Value(""), Flag: typed.Value(false), Count: typed.Value(int64(0)),
		Items: typed.Value([]string{}), Clear: typed.Null[string](),
	}
	data, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	want := decodeRequestJSON(t, []byte(`{"text":"","flag":false,"count":0,"items":[],"clear":null}`))
	if got := decodeRequestJSON(t, data); !reflect.DeepEqual(got, want) {
		t.Fatalf("optional field encoding=%s; want empty string, false, zero, empty array and null with absent omitted", data)
	}
	if value, ok := fields.Flag.Get(); !ok || value || !fields.Flag.IsSet() || fields.Flag.IsNull() {
		t.Error("explicit false lost its supplied-value state")
	}
	if value, ok := fields.Items.Get(); !ok || value == nil || len(value) != 0 {
		t.Error("explicit empty array lost its supplied-value state")
	}
	if _, ok := fields.Clear.Get(); ok || !fields.Clear.IsSet() || !fields.Clear.IsNull() {
		t.Error("explicit null lost its clear-field state")
	}
	if fields.Absent.IsSet() || fields.Absent.IsNull() {
		t.Error("omitted field became present")
	}
}

func TestOptionalRequestDecodePreservesValueOnFailureAndCanUnsetNull(t *testing.T) {
	field := typed.Value("retained")
	if err := json.Unmarshal([]byte(`42`), &field); err == nil {
		t.Fatal("numeric JSON accepted for optional string")
	}
	if value, ok := field.Get(); !ok || value != "retained" || field.IsNull() {
		t.Fatal("failed decoding corrupted the previously supplied value")
	}
	if err := json.Unmarshal([]byte(" \n null \t"), &field); err != nil {
		t.Fatal(err)
	}
	if _, ok := field.Get(); ok || !field.IsSet() || !field.IsNull() {
		t.Fatal("decoding null did not replace the prior value")
	}
	field.Unset()
	if _, ok := field.Get(); ok || field.IsSet() || field.IsNull() || !field.IsZero() {
		t.Fatal("Unset did not restore omission state")
	}
	field.Set("reused")
	if value, ok := field.Get(); !ok || value != "reused" || field.IsNull() || field.IsZero() {
		t.Fatal("Set after Unset did not restore supplied-value state")
	}
	field.SetNull()
	if !field.IsNull() || !field.IsSet() || field.IsZero() {
		t.Fatal("SetNull did not preserve explicit presence")
	}
}

func checkOptional[T comparable](t *testing.T, value typed.Optional[T], want T, wantValue, wantSet, wantNull bool) {
	t.Helper()
	got, ok := value.Get()
	if got != want || ok != wantValue || value.IsSet() != wantSet || value.IsNull() != wantNull || value.IsZero() == wantSet {
		t.Fatalf("optional: value=%v ok=%v set=%v null=%v zero=%v; want %v %v %v %v", got, ok, value.IsSet(), value.IsNull(), value.IsZero(), want, wantValue, wantSet, wantNull)
	}
}

func TestGenericCodecsKeepDynamicNumbersExact(t *testing.T) {
	for _, test := range []struct {
		name, input string
		value       any
	}{
		{"optional", `{"id":9007199254740993}`, new(typed.Optional[map[string]any])},
		{"direct update", `{"id":9007199254740993}`, new(typed.Update[map[string]any])},
		{"set update", `{"set":{"id":9007199254740993}}`, new(typed.Update[map[string]any])},
		{"one", `{"id":9007199254740993}`, new(typed.OneOrMany[map[string]any])},
		{"many", `[{"id":9007199254740993}]`, new(typed.OneOrMany[map[string]any])},
		{"collection", `[{"id":9007199254740993}]`, new(typed.CollectionUpdate[map[string]any])},
		{"add", `{"add":{"id":9007199254740993}}`, new(typed.CollectionUpdate[map[string]any])},
		{"replace", `{"replace":{"target":{"id":9007199254740993},"replacement":{"id":9007199254740995}}}`, new(typed.CollectionUpdate[map[string]any])},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := json.Unmarshal([]byte(test.input), test.value); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(test.value)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(decodeRequestJSON(t, []byte(test.input)), decodeRequestJSON(t, encoded)) {
				t.Fatalf("numeric value changed: %s -> %s", test.input, encoded)
			}
		})
	}
}
