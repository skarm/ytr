package typed_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/skarm/ytr/typed"
)

func TestStringOrNumberWireVariants(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		value typed.StringOrNumber
		want  string
	}{
		{"empty", typed.StringOrNumber{}, `null`},
		{"empty string", typed.Text(""), `""`},
		{"string ID", typed.Text("9007199254740993"), `"9007199254740993"`},
		{"zero", typed.Number(json.Number("0")), `0`},
		{"large integer", typed.Number(json.Number("9007199254740993")), `9007199254740993`},
		{"negative zero", typed.Number(json.Number("-0")), `-0`},
		{"decimal exponent", typed.Number(json.Number("1.2300e+40")), `1.2300e+40`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			data, err := json.Marshal(test.value)
			if err != nil || string(data) != test.want {
				t.Fatalf("wire: %s, error: %v; want %s", data, err, test.want)
			}
			value := typed.Text("previous")
			if err = json.Unmarshal(data, &value); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(value, test.value) {
				t.Fatalf("selected variant: %+v; want %+v", value, test.value)
			}
		})
	}
}

func TestStringOrNumberRejectsInvalidWireWithoutMutation(t *testing.T) {
	t.Parallel()
	for _, input := range []string{`true`, `[]`, `{}`, `"unterminated`, `1 2`, `1e`, ``} {
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			value := typed.Number(json.Number("9007199254740993"))
			if err := value.UnmarshalJSON([]byte(input)); err == nil {
				t.Fatal("invalid union value was accepted")
			}
			if value.String != nil || value.Number == nil || *value.Number != json.Number("9007199254740993") {
				t.Fatalf("failed decode changed selected variant: %+v", value)
			}
		})
	}
	for _, value := range []typed.StringOrNumber{
		{String: typed.Ptr("text"), Number: typed.Ptr(json.Number("1"))},
		{Number: typed.Ptr(json.Number("not-a-number"))},
	} {
		if data, err := json.Marshal(value); err == nil {
			t.Fatalf("invalid union marshalled as %s", data)
		}
	}
}

func TestReferenceWireVariantsAndHelpers(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		value typed.Reference
		want  string
	}{
		{"empty", typed.Reference{}, `null`},
		{"key helper", typed.Ref("SDK"), `"SDK"`},
		{"empty key", typed.Ref(""), `""`},
		{"large numeric ID", typed.Reference{Number: typed.Ptr(json.Number("9007199254740993"))}, `9007199254740993`},
		{"object", typed.Reference{IDKeyReference: &typed.IDKeyReference{ID: typed.Value(typed.Number(json.Number("9007199254740993"))), Key: typed.Value("SDK"), Name: typed.Value("")}}, `{"id":9007199254740993,"key":"SDK","name":""}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			data, err := json.Marshal(test.value)
			if err != nil || string(data) != test.want {
				t.Fatalf("wire: %s, error: %v; want %s", data, err, test.want)
			}
			value := typed.Reference{Number: typed.Ptr(json.Number("7"))}
			if err = json.Unmarshal(data, &value); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(value, test.value) {
				t.Fatalf("selected variant: %+v; want %+v", value, test.value)
			}
		})
	}
	for _, id := range []string{"", "login", "9007199254740993"} {
		value := typed.IDRef(id)
		want, err := json.Marshal(id)
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(value)
		if err != nil || string(data) != string(want) || value.String == nil || *value.String != id || value.Number != nil || value.IDReference != nil {
			t.Fatalf("IDRef(%q): %s, value=%+v, error=%v", id, data, value, err)
		}
	}
	pointer := typed.Ptr(false)
	if pointer == nil || *pointer {
		t.Fatalf("Ptr(false): %v", pointer)
	}
}

func TestReferenceObjectReplacementClearsMissingFields(t *testing.T) {
	t.Parallel()
	value := typed.Reference{IDKeyReference: &typed.IDKeyReference{ID: typed.Value(typed.Text("old-id")), Key: typed.Value("old"), Name: typed.Value("old-name")}}
	if err := json.Unmarshal([]byte(`{"key":"new"}`), &value); err != nil {
		t.Fatal(err)
	}
	if value.String != nil || value.Number != nil || value.IDKeyReference == nil {
		t.Fatalf("selected variant: %+v", value)
	}
	data, err := json.Marshal(value)
	if err != nil || string(data) != `{"key":"new"}` || value.IDKeyReference.ID.IsSet() || value.IDKeyReference.Name.IsSet() {
		t.Fatalf("old object fields survived replacement: %s, error=%v", data, err)
	}
	checkOptional(t, value.IDKeyReference.Key, "new", true, true, false)
}

func TestReferenceRejectsConflictingVariants(t *testing.T) {
	t.Parallel()
	for _, value := range []typed.Reference{
		{String: typed.Ptr("SDK"), Number: typed.Ptr(json.Number("1"))},
		{String: typed.Ptr("SDK"), IDKeyReference: &typed.IDKeyReference{Key: typed.Value("other")}},
		{Number: typed.Ptr(json.Number("1")), IDKeyReference: &typed.IDKeyReference{Key: typed.Value("other")}},
		{String: typed.Ptr("SDK"), Number: typed.Ptr(json.Number("1")), IDKeyReference: &typed.IDKeyReference{Key: typed.Value("other")}},
	} {
		if data, err := json.Marshal(value); err == nil {
			t.Fatalf("conflicting variants marshalled as %s", data)
		}
	}
}

func TestReferenceFailedDecodePreservesExistingObject(t *testing.T) {
	t.Parallel()
	for _, input := range []string{`{"key":"new","id":true}`, `{"key":false}`, `true`, `[]`, `{"key":`, `{ } null`} {
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			original := &typed.IDKeyReference{ID: typed.Value(typed.Text("old-id")), Key: typed.Value("old")}
			value := typed.Reference{IDKeyReference: original}
			if err := value.UnmarshalJSON([]byte(input)); err == nil {
				t.Fatal("invalid reference was accepted")
			}
			data, err := json.Marshal(value)
			if err != nil || string(data) != `{"id":"old-id","key":"old"}` || value.IDKeyReference != original || value.Number != nil || value.String != nil {
				t.Fatalf("failed decode changed state: %s, value=%+v, error=%v", data, value, err)
			}
			checkOptional(t, original.Key, "old", true, true, false)
		})
	}
}

func TestTypedWidgetUnionRejectsMismatchedSelectedDiscriminator(t *testing.T) {
	for _, discriminator := range []string{"", "security", "future-widget"} {
		t.Run("type="+discriminator, func(t *testing.T) {
			body := typed.CreateDashboardWidgetRequestBody{
				CreateDashboardWidgetNote: &typed.CreateDashboardWidgetNote{
					Type: discriminator, Description: "Note", Text: "Text", MarkupType: "plain",
				},
			}
			if data, err := json.Marshal(body); err == nil {
				t.Fatalf("selected note variant accepted incompatible type %q: %s", discriminator, data)
			}
		})
	}
}

func TestTypedProjectUnionRecognizesSingleKeyObject(t *testing.T) {
	for _, body := range []string{`{"id":"project-id"}`, `{"primary":{"id":"project-id"}}`} {
		t.Run(body, func(t *testing.T) {
			var project typed.IssueProject
			if err := json.Unmarshal([]byte(body), &project); err != nil {
				t.Fatalf("single-key project reference was rejected: %v", err)
			}
			if got := mustRequestJSON(t, project); !reflect.DeepEqual(decodeRequestJSON(t, got), decodeRequestJSON(t, []byte(body))) {
				t.Fatalf("project reference changed shape: got %s; want %s", got, body)
			}
		})
	}
}

func TestTaggedUnionRejectsCaseFoldedDiscriminatorWithoutMutation(t *testing.T) {
	for _, input := range []string{
		`{"type":"note","Type":"security","description":"changed"}`,
		`{"Type":"security","type":"note","description":"changed"}`,
		`{"type":"note","TYPE":"note","description":"changed"}`,
		`{"TYPE":"note","type":"note","description":"changed"}`,
		`{"type":"note","\u0054ype":"security","description":"changed"}`,
	} {
		t.Run(input, func(t *testing.T) {
			original := &typed.CreateDashboardWidgetNote{Type: "note", Description: "retained", Text: "text", MarkupType: "plain"}
			value := typed.CreateDashboardWidgetRequestBody{CreateDashboardWidgetNote: original}
			if err := json.Unmarshal([]byte(input), &value); err == nil {
				t.Fatal("case-folded discriminator alias accepted")
			}
			if value.CreateDashboardWidgetNote != original || original.Type != "note" || original.Description != "retained" || value.CreateDashboardWidgetSecurity != nil {
				t.Fatalf("failed decode changed the retained variant: %+v", value)
			}
		})
	}
}

func TestTaggedUnionKeepsExactDiscriminatorLastWinsAndUnknownFields(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{`{"type":"security","type":"note","description":"d","text":"t","markupType":"plain","unknown":false}`, "note"},
		{`{"type":"note","type":"security","description":"d","unknown":false}`, "security"},
	} {
		t.Run(test.want, func(t *testing.T) {
			var value typed.CreateDashboardWidgetRequestBody
			if err := json.Unmarshal([]byte(test.input), &value); err != nil {
				t.Fatal(err)
			}
			data, err := value.MarshalJSON()
			if err != nil {
				t.Fatal(err)
			}
			var object map[string]json.RawMessage
			if err := json.Unmarshal(data, &object); err != nil || string(object["type"]) != `"`+test.want+`"` {
				t.Fatalf("exact repeated discriminator changed: %s, error=%v", data, err)
			}
		})
	}
}

func TestUntaggedUnionKeepsCaseInsensitiveObjectFields(t *testing.T) {
	var value typed.Reference
	if err := json.Unmarshal([]byte(`{"KEY":"SDK","unknown":false}`), &value); err != nil {
		t.Fatal(err)
	}
	if value.IDKeyReference == nil {
		t.Fatal("untagged object variant was not selected")
	}
	if key, present := value.IDKeyReference.Key.Get(); !present || key != "SDK" {
		t.Fatalf("untagged object field changed: key=%q present=%v", key, present)
	}
}

func BenchmarkTaggedWidgetJSON(b *testing.B) {
	data := []byte(`{"type":"note","description":"Example","text":"Text","markupType":"plain"}`)
	b.ReportAllocs()
	var value typed.CreateDashboardWidgetRequestBody
	for b.Loop() {
		if err := json.Unmarshal(data, &value); err != nil {
			b.Fatal(err)
		}
	}
	if value.CreateDashboardWidgetNote == nil || value.CreateDashboardWidgetNote.Type != "note" || value.CreateDashboardWidgetNote.Text != "Text" {
		b.Fatalf("tagged widget changed: %+v", value)
	}
}

// The codecs promise to replace their previous value only after a successful
// decode. Exercise that boundary with populated composite values, so failed
// decoding cannot silently mutate a retained map, slice or pointed-to model.
func FuzzTypedJSONCodecs(f *testing.F) {
	for _, seed := range []string{
		`null`, `{}`, `[]`, `false`, `0`, `""`,
		`{"id":9007199254740993,"key":"SDK"}`,
		`{"set":{"id":9007199254740993}}`,
		`{"add":[{"id":9007199254740993}],"remove":null}`,
		`{"parent":{"key":"SDK-1"},"parent":{"display":"Parent"},"custom":{"flag":false}}`,
		`{"summary":"changed","parent":{"key":"new"},"tags":42}`,
		`{"type":"note","description":"d","text":"t","markupType":"plain"}`,
		`{"type":"note","Type":"security","description":"d"}`,
		`{"Type":"security","type":"note","description":"d"}`,
		`{"type":"note","TYPE":"note"}`,
		`{"type":"note","\u0054ype":"security"}`,
		`{"id":true}`, `{"new":`, `null false`, " \t\n", "",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip("keep individual codec inputs bounded")
		}
		for _, codec := range []interface {
			json.Marshaler
			json.Unmarshaler
		}{
			typed.Ptr(typed.Value(map[string]any{"retained": json.Number("9007199254740993")})),
			typed.Ptr(typed.Set(map[string]any{"retained": true})),
			typed.Ptr(typed.Collection([]map[string]any{{"retained": true}})),
			typed.Ptr(typed.One(map[string]any{"retained": true})),
			typed.Ptr(typed.Reference{IDKeyReference: &typed.IDKeyReference{Key: typed.Value("retained")}}),
			&typed.Issue{Summary: "retained", Parent: &typed.IssueParent{Key: "SDK-1"}, AdditionalFields: map[string]json.RawMessage{"retained": json.RawMessage(`false`)}},
			&typed.CreateDashboardWidgetRequestBody{CreateDashboardWidgetNote: &typed.CreateDashboardWidgetNote{Type: "note", Description: "retained", Text: "text", MarkupType: "plain"}},
		} {
			before, err := codec.MarshalJSON()
			if err != nil {
				t.Fatal(err)
			}
			decodeErr := json.Unmarshal(data, codec)
			after, err := codec.MarshalJSON()
			if err != nil {
				t.Fatalf("%T cannot marshal after decode (error %v): %v", codec, decodeErr, err)
			}
			if !json.Valid(after) {
				t.Fatalf("%T produced invalid JSON: %s", codec, after)
			}
			if decodeErr != nil && !bytes.Equal(before, after) {
				t.Fatalf("%T changed after failed decode: before=%s after=%s error=%v", codec, before, after, decodeErr)
			}
		}
	})
}
