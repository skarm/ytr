package typed_test

import (
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
