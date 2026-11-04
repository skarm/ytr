package typed_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"testing"

	"github.com/skarm/ytr"
	"github.com/skarm/ytr/typed"
)

func TestTypedUpdateRequestLiteralWireContract(t *testing.T) {
	want := []byte(`{"summary":"","description":null,"possibleSpam":{"set":false},"storyPoints":0,"tags":{"add":"new","remove":["old","obsolete"],"replace":[{"target":"before","replacement":"after"}]}}`)
	transportError := errors.New("literal update fixture completed")
	calls := 0
	client, err := typed.New(ytr.Config{
		Organization: ytr.Organization{Kind: ytr.Organization360, ID: "org"},
		TokenSource:  ytr.OAuthToken("token"),
		HTTPClient: &http.Client{Transport: requestTransport(func(request *http.Request) (*http.Response, error) {
			calls++
			defer request.Body.Close()
			body, readError := io.ReadAll(request.Body)
			if readError != nil {
				t.Fatal(readError)
			}
			if request.Method != http.MethodPatch || request.URL.EscapedPath() != "/v3/issues/SDK%2F1" || request.URL.Query().Get("version") != "4" {
				t.Errorf("literal update dispatch=%s %s", request.Method, request.URL)
			}
			if !reflect.DeepEqual(decodeRequestJSON(t, body), decodeRequestJSON(t, want)) {
				t.Errorf("literal update body=%s; want %s", body, want)
			}
			return nil, transportError
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.UpdateIssue(t.Context(), typed.UpdateIssueRequest{
		IssueID: "SDK/1", Version: typed.Ptr(int64(4)),
		Body: typed.UpdateIssueRequestBody{
			Summary:      typed.Value(typed.Replace("")),
			Description:  typed.Null[typed.Update[string]](),
			PossibleSpam: typed.Value(typed.Set(false)),
			StoryPoints:  typed.Value(typed.Replace(json.Number("0"))),
			Tags: typed.Value(typed.CollectionUpdate[string]{
				Add:     typed.Value(typed.One("new")),
				Remove:  typed.Value(typed.Many([]string{"old", "obsolete"})),
				Replace: typed.Value(typed.Many([]typed.Replacement[string]{{Target: "before", Replacement: "after"}})),
			}),
		},
	})
	if !errors.Is(err, transportError) || calls != 1 {
		t.Fatalf("literal request: calls=%d error=%v; want one dispatch and preserved transport error", calls, err)
	}
}

func TestGenericUpdateConstructorsKeepExactJSONKinds(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"direct-empty-string", typed.Replace(""), `""`},
		{"set-false", typed.Set(false), `{"set":false}`},
		{"direct-large-number", typed.Replace(json.Number("9007199254740993")), `9007199254740993`},
		{"set-large-number", typed.Set(json.Number("9007199254740993")), `{"set":9007199254740993}`},
		{"empty-collection", typed.Collection([]string{}), `[]`},
		{"singleton", typed.One("first"), `"first"`},
		{"many", typed.Many([]string{"first", "second"}), `["first","second"]`},
	}
	for _, fixture := range cases {
		t.Run(fixture.name, func(t *testing.T) {
			data, err := json.Marshal(fixture.value)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(decodeRequestJSON(t, data), decodeRequestJSON(t, []byte(fixture.want))) {
				t.Fatalf("constructor JSON=%s; want %s", data, fixture.want)
			}
		})
	}
}

func TestCollectionUpdateLiteralPreservesAllOperatorsAndEmptySet(t *testing.T) {
	value := typed.CollectionUpdate[string]{
		Set:    typed.Value([]string{}),
		Add:    typed.Value(typed.Many([]string{"first", "second"})),
		Remove: typed.Value(typed.One("obsolete")),
		Replace: typed.Value(typed.Many([]typed.Replacement[string]{
			{Target: "before", Replacement: "after"},
			{Target: "old", Replacement: "new"},
		})),
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte(`{"set":[],"add":["first","second"],"remove":"obsolete","replace":[{"target":"before","replacement":"after"},{"target":"old","replacement":"new"}]}`)
	if !reflect.DeepEqual(decodeRequestJSON(t, data), decodeRequestJSON(t, want)) {
		t.Fatalf("collection operators changed JSON kinds or lost an operator: %s", data)
	}
}

func TestGenericUpdatesRejectDirectValueCombinedWithOperators(t *testing.T) {
	for _, fixture := range []struct {
		name  string
		value any
	}{
		{"scalar-set", typed.Update[string]{Value: typed.Ptr("direct"), Set: typed.Value("operator")}},
		{"collection-set", typed.CollectionUpdate[string]{Value: typed.Ptr([]string{"direct"}), Set: typed.Value([]string{})}},
		{"collection-add", typed.CollectionUpdate[string]{Value: typed.Ptr([]string{"direct"}), Add: typed.Value(typed.One("operator"))}},
		{"collection-remove", typed.CollectionUpdate[string]{Value: typed.Ptr([]string{"direct"}), Remove: typed.Value(typed.One("operator"))}},
		{"collection-replace", typed.CollectionUpdate[string]{Value: typed.Ptr([]string{"direct"}), Replace: typed.Value(typed.Many([]typed.Replacement[string]{{Target: "before", Replacement: "after"}}))}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			if data, err := json.Marshal(fixture.value); err == nil {
				t.Errorf("conflicting update variants accepted: %s", data)
			}
		})
	}
}

func TestGenericUpdateDecodingReplacesPriorVariantAtomically(t *testing.T) {
	value := typed.Set("previous")
	if err := json.Unmarshal([]byte(`"direct"`), &value); err != nil {
		t.Fatal(err)
	}
	if got := string(mustRequestJSON(t, value)); got != `"direct"` {
		t.Fatalf("old set operator survived direct assignment: %s", got)
	}
	if err := json.Unmarshal([]byte(`42`), &value); err == nil {
		t.Fatal("number accepted for string update")
	}
	if got := string(mustRequestJSON(t, value)); got != `"direct"` {
		t.Fatalf("invalid JSON changed the prior value: %s", got)
	}
	collection := typed.Collection([]string{"previous"})
	if err := json.Unmarshal([]byte(`{"add":"new","remove":["old"]}`), &collection); err != nil {
		t.Fatal(err)
	}
	want := []byte(`{"add":"new","remove":["old"]}`)
	if !reflect.DeepEqual(decodeRequestJSON(t, mustRequestJSON(t, collection)), decodeRequestJSON(t, want)) {
		t.Fatal("collection retained its previous direct-assignment arm")
	}
}

func TestGenericUpdateDecodersRejectEmptyInputWithoutChangingValue(t *testing.T) {
	scalar := typed.Replace("retained")
	collection := typed.Collection([]string{"retained"})
	oneOrMany := typed.One("retained")
	for _, fixture := range []struct {
		name  string
		value interface {
			json.Marshaler
			json.Unmarshaler
		}
		want string
	}{
		{"scalar", &scalar, `"retained"`},
		{"collection", &collection, `["retained"]`},
		{"one-or-many", &oneOrMany, `"retained"`},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			for _, data := range [][]byte{nil, {}, []byte(" \t\n")} {
				if err := fixture.value.UnmarshalJSON(data); err == nil {
					t.Errorf("empty JSON accepted: %q", data)
				}
				if got := string(mustRequestJSON(t, fixture.value)); got != fixture.want {
					t.Errorf("failed empty-input decode changed prior value: %s; want %s", got, fixture.want)
				}
			}
		})
	}
}
