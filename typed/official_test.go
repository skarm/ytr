package typed_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/skarm/ytr"
	"github.com/skarm/ytr/internal/apidoc"
	"github.com/skarm/ytr/typed"
)

// Expectations come from independently fetched official examples, never models.json.
func TestOfficialResponseExamples(t *testing.T) {
	var documents apidoc.Documents
	if err := apidoc.ReadJSON("../api/documentation.json", &documents); err != nil {
		t.Fatal(err)
	}
	catalog, err := apidoc.Load("../api/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	bySource := map[string][]apidoc.Operation{}
	for _, operation := range catalog.Operations {
		bySource[operation.Source] = append(bySource[operation.Source], operation)
	}
	checked := 0
	for _, document := range documents.Pages {
		operations := bySource[document.Path]
		if len(operations) != 1 || operations[0].Response == "stream" {
			continue
		}
		for _, example := range document.Examples {
			if !strings.Contains(example.Section, "#answer") || example.Status >= 300 {
				continue
			}
			checked++
			t.Run(fmt.Sprintf("%s/line-%d", operations[0].Name, example.Line), func(t *testing.T) {
				status := example.Status
				if status == 0 {
					status = 200
				}
				body := &responseBody{Reader: strings.NewReader(string(example.Value))}
				client, _ := fixtureClient(t, status, body)
				result, err := invokeResponseMethod(t, client, operations[0].Name)
				if err != nil {
					t.Fatalf("%s:%d: %v", document.Path, example.Line, err)
				}
				data := responseData(t, result, status)
				assertResponseValues(t, example.Value, data)
			})
		}
	}
	if checked == 0 {
		t.Fatal("no official response examples exercised")
	}
}

func TestFilterGroupByObjectRoundTrip(t *testing.T) {
	for _, input := range []string{`"status"`, `{"field":"status","custom":false}`} {
		var value typed.CreateFilterRequestBodyGroupBy
		if err := json.Unmarshal([]byte(input), &value); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(value)
		if err != nil || string(data) != input {
			t.Fatalf("round trip: %s, %v", data, err)
		}
	}
}

func TestOfficialRequestExamples(t *testing.T) {
	var documents apidoc.Documents
	if err := apidoc.ReadJSON("../api/documentation.json", &documents); err != nil {
		t.Fatal(err)
	}
	catalog, err := apidoc.Load("../api/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	bySource := map[string][]apidoc.Operation{}
	for _, operation := range catalog.Operations {
		bySource[operation.Source] = append(bySource[operation.Source], operation)
	}
	fixtures := loadRequestFixtures(t)
	byName := map[string]requestFixture{}
	for _, fixture := range fixtures.Operations {
		byName[fixture.Name] = fixture
	}
	checked := 0
	for _, document := range documents.Pages {
		operations := bySource[document.Path]
		if len(operations) != 1 || operations[0].Request != "json" {
			continue
		}
		for _, example := range document.Examples {
			if !strings.Contains(example.Section, "#query") {
				continue
			}
			checked++
			t.Run(fmt.Sprintf("%s/line-%d", operations[0].Name, example.Line), func(t *testing.T) {
				runRequestFixture(t, byName[operations[0].Name], example.Value, false, false, officialRequestExtras(t, operations[0].Name, example.Value))
			})
		}
	}
	if checked == 0 {
		t.Fatal("no official request examples exercised")
	}
}

func officialRequestExtras(t *testing.T, name string, body json.RawMessage) ytr.Fields {
	t.Helper()
	method, exists := reflect.TypeFor[*typed.Client]().MethodByName(name)
	if !exists {
		t.Fatal(name)
	}
	request := method.Type.In(2)
	if _, exists := request.FieldByName("ExtraFields"); !exists {
		return nil
	}
	field, exists := request.FieldByName("Body")
	if !exists || field.Type.Kind() != reflect.Struct {
		return nil
	}
	if _, exists := field.Type.FieldByName("AdditionalFields"); exists {
		return nil
	}
	known := map[string]bool{}
	for member := range field.Type.Fields() {
		key, _, _ := strings.Cut(member.Tag.Get("json"), ",")
		known[key] = true
	}
	if len(known) == 1 && known["-"] {
		return nil
	}
	var members map[string]json.RawMessage
	if json.Unmarshal(body, &members) != nil {
		return nil
	}
	extra := ytr.Fields{}
	for key, value := range members {
		if !known[key] {
			extra[key] = value
		}
	}
	return extra
}
