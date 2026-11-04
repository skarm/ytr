package apidoc_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/skarm/ytr/internal/apidoc"
)

func TestModelValidationRejectsSemanticCorruption(t *testing.T) {
	catalog, err := apidoc.Load("../../api/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	var original apidoc.Models
	if err := apidoc.ReadJSON("../../api/models.json", &original); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		edit func(*apidoc.Models)
	}{
		{"missing shared-page sections", func(m *apidoc.Models) {
			for i := range m.Operations {
				if m.Operations[i].Name == "ListAutoactionLogs" {
					m.Operations[i].Sections = nil
					return
				}
			}
		}},
		{"duplicate source section", func(m *apidoc.Models) { m.Operations[0].Sections = []string{"query", "query"} }},
		{"invalid source section", func(m *apidoc.Models) { m.Operations[0].Sections = []string{"bad anchor"} }},
		{"reserved function name", func(m *apidoc.Models) { m.Types[0].Name = "Value" }},
		{"invalid JSON tag", func(m *apidoc.Models) {
			for i := range m.Types {
				if m.Types[i].Name == "Issue" {
					m.Types[i].Fields[0].JSON = "bad\tname"
					return
				}
			}
		}},
		{"noncanonical Optional", func(m *apidoc.Models) { m.Types[0].Fields[0].Type = "Optional [string]" }},
		{"alias loses custom codec", func(m *apidoc.Models) {
			m.Types = append(m.Types, apidoc.ModelType{Name: "AliasOfNumber", Kind: "alias", Target: "json.Number"})
		}},
		{"unknown kind", func(m *apidoc.Models) { m.Types[0].Kind = "anything" }},
		{"duplicate type", func(m *apidoc.Models) { m.Types = append(m.Types, m.Types[0]) }},
		{"reserved type", func(m *apidoc.Models) { m.Types[0].Name = "Optional" }},
		{"unknown field type", func(m *apidoc.Models) { m.Types[0].Fields[0].Type = "UnknownValue" }},
		{"invalid Go expression", func(m *apidoc.Models) { m.Types[0].Fields[0].Type = "func()" }},
		{"unsupported map key", func(m *apidoc.Models) { m.Types[0].Fields[0].Type = "map[int64]string" }},
		{"unsupported generic", func(m *apidoc.Models) { m.Types[0].Fields[0].Type = "Unknown[string]" }},
		{"duplicate field", func(m *apidoc.Models) { m.Types[0].Fields = append(m.Types[0].Fields, m.Types[0].Fields[0]) }},
		{"unknown request", func(m *apidoc.Models) { m.Operations[0].Request = "MissingRequest" }},
		{"unknown response", func(m *apidoc.Models) { m.Operations[0].Response = "MissingResponse" }},
		{"missing path binding", func(m *apidoc.Models) { m.Operations[1].Paths[0].Name = "WrongField" }},
		{"wrong query type", func(m *apidoc.Models) { m.Operations[0].Queries[0].Type = "int64" }},
		{"wrong CSV", func(m *apidoc.Models) { m.Operations[0].Queries[0].CSV = true }},
		{"missing body", func(m *apidoc.Models) { m.Operations[0].Body = "" }},
		{"unbound request field", func(m *apidoc.Models) {
			m.Types[0].Fields = append(m.Types[0].Fields, apidoc.ModelField{Name: "Ignored", Type: "string"})
		}},
		{"recursive value", func(m *apidoc.Models) {
			m.Types = append(m.Types, apidoc.ModelType{Name: "Recursive", Kind: "struct", Fields: []apidoc.ModelField{{Name: "Child", JSON: "child", Type: "Optional[Recursive]", Optional: true}}})
		}},
		{"unknown union kind", func(m *apidoc.Models) {
			for i := range m.Types {
				if m.Types[i].Kind == "union" {
					m.Types[i].Variants[0].JSONKind = "documented-object"
					return
				}
			}
		}},
		{"wrong union wire type", func(m *apidoc.Models) {
			for i := range m.Types {
				if m.Types[i].Name == "StringOrNumber" {
					m.Types[i].Variants[0].JSONKind = "boolean"
					return
				}
			}
		}},
		{"lost presence", func(m *apidoc.Models) {
			for i := range m.Types {
				if m.Types[i].Name == "UpdateIssueRequestBody" {
					m.Types[i].Fields[0].Optional = false
					return
				}
			}
		}},
		{"duplicate JSON key", func(m *apidoc.Models) {
			for i := range m.Types {
				if m.Types[i].Name == "Issue" {
					m.Types[i].Fields[1].JSON = m.Types[i].Fields[0].JSON
					return
				}
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var models apidoc.Models
			if err := json.Unmarshal(data, &models); err != nil {
				t.Fatal(err)
			}
			test.edit(&models)
			if err := apidoc.ValidateModels(models, catalog); err == nil {
				t.Fatal("semantic corruption accepted")
			}
		})
	}
}

func TestModelTypesCannotReplaceClientConstructors(t *testing.T) {
	catalog, err := apidoc.Load("../../api/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	original, err := apidoc.LoadModels("../../api/models.json", catalog)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"New", "FromClient", "FutureModel"} {
		t.Run(name, func(t *testing.T) {
			models := original
			models.Types = append(append([]apidoc.ModelType(nil), original.Types...), apidoc.ModelType{Name: name, Kind: "alias", Target: "string"})
			err := apidoc.ValidateModels(models, catalog)
			if name == "FutureModel" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "reserved") {
				t.Fatalf("constructor collision was not rejected: %v", err)
			}
		})
	}
}

func TestModelTargetsKeepDistinctSectionLists(t *testing.T) {
	catalog := apidoc.Catalog{Revision: "r1", VerifiedOn: "2026-11-04", Operations: []apidoc.Operation{
		{Name: "CreateOne", Method: "POST", Path: "/v3/one", Request: "json", Response: "json", Source: "api/shared.md"},
		{Name: "CreateTwo", Method: "POST", Path: "/v3/two", Request: "json", Response: "json", Source: "api/shared.md"},
	}}
	models := apidoc.Models{Version: 1, Revision: "r1", Types: []apidoc.ModelType{
		{Name: "Body", Kind: "struct", Fields: []apidoc.ModelField{{Name: "Value", JSON: "value", Type: "string"}}},
		{Name: "CreateRequest", Kind: "request", Fields: []apidoc.ModelField{{Name: "Body", Type: "Body"}}},
	}}
	for i, sections := range [][]string{{"one,two"}, {"one", "two"}} {
		models.Operations = append(models.Operations, apidoc.ModelOperation{Name: catalog.Operations[i].Name, Request: "CreateRequest", Body: "Body", Response: "struct{}", AllowEmpty: true, Encoding: "json", Source: "api/shared.md", Sections: sections})
	}
	if err := apidoc.ValidateModels(models, catalog); err != nil {
		t.Fatal(err)
	}
	targets := apidoc.ModelTargets(models)
	if len(targets) != 2 || len(targets[0].Operations) != 1 || targets[0].Operations[0] != "CreateOne" || len(targets[1].Operations) != 1 || targets[1].Operations[0] != "CreateTwo" {
		t.Fatalf("different source scopes were merged: %+v", targets)
	}
}

func FuzzModelValidationDoesNotPanic(f *testing.F) {
	for _, value := range []string{"", "[]", "Optional[", "map[string]Unknown", "struct{}", "*Value", "Optional[Value]", "documented-object"} {
		f.Add(value)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if len(value) > 4096 {
			t.Skip()
		}
		catalog := apidoc.Catalog{Revision: "r1", VerifiedOn: "2026-10-31", Operations: []apidoc.Operation{{Name: "GetItem", Method: "GET", Path: "/v3/items", Request: "none", Response: "json", Source: "api/get.md"}}}
		models := apidoc.Models{Version: 1, Revision: "r1", Types: []apidoc.ModelType{{Name: "GetItemRequest", Kind: "request"}, {Name: "PayloadValue", Kind: "struct", Fields: []apidoc.ModelField{{Name: "Data", JSON: "data", Type: value, Optional: strings.HasPrefix(value, "Optional[")}}}}, Operations: []apidoc.ModelOperation{{Name: "GetItem", Request: "GetItemRequest", Response: "PayloadValue", Encoding: "none", Source: "api/get.md"}}}
		_ = apidoc.ValidateModels(models, catalog)
	})
}

func TestEvidenceKeepsAllBindingsAndSourceOrder(t *testing.T) {
	models := apidoc.Models{Types: []apidoc.ModelType{
		{Name: "First", Kind: "struct", Fields: []apidoc.ModelField{{Name: "Value", JSON: "value", Type: "string"}}},
		{Name: "Second", Kind: "struct", Fields: []apidoc.ModelField{{Name: "Value", JSON: "value", Type: "string"}}},
	}, Operations: []apidoc.ModelOperation{
		{Name: "CreateFirst", Body: "First", Source: "api/shared.md", Sections: []string{"one"}},
		{Name: "CreateSecond", Body: "Second", Source: "api/shared.md", Sections: []string{"two"}},
		{Name: "CopyFirst", Body: "First", Source: "api/shared.md", Sections: []string{"one"}},
	}}
	docs := apidoc.Documents{Pages: []apidoc.Document{{Path: "api/shared.md", Fields: []apidoc.DocumentField{
		{Name: "value", Type: "Логический", Section: "{#two} / Параметры тела", Digest: "second", Line: 10},
		{Name: "value", Type: "Число", Section: "{#one} / Параметры тела", Digest: "first", Line: 20},
		{Name: "value", Type: "Число", Section: "{#one} / Параметры тела", Digest: "first", Line: 30},
	}}}}
	problems := apidoc.CheckModelEvidence(docs, models)
	if len(problems) != 2 || problems[0].Target.Model != "Second" || problems[0].Line != 10 || problems[1].Target.Model != "First" || problems[1].Line != 20 || !reflect.DeepEqual(problems[1].Target.Operations, []string{"CreateFirst", "CopyFirst"}) {
		t.Fatalf("bindings, deduplication or source order changed: %+v", problems)
	}
	before := docs
	after := docs
	after.Pages = append([]apidoc.Document(nil), docs.Pages...)
	after.Pages[0].Fields = append([]apidoc.DocumentField(nil), docs.Pages[0].Fields...)
	after.Pages[0].Fields[1].Type = "Логический"
	after.Pages[0].Fields[1].Digest = "changed"
	report := apidoc.CompareDocuments(before, after, models)
	if len(report.Disagreements) != 1 || report.Disagreements[0].Target.Model != "First" || report.Disagreements[0].Line != 20 {
		t.Fatalf("unchanged rows affected changed-field report: %+v", report.Disagreements)
	}
}

func TestEvidenceKeepsBindingAndSectionBoundaries(t *testing.T) {
	catalog := apidoc.Catalog{Revision: "r1", VerifiedOn: "2026-11-04", Operations: []apidoc.Operation{
		{Name: "GetExample", Method: "GET", Path: "/v3/example", Request: "none", Response: "json", Source: "api/example.md", Summary: "Example"},
	}}
	request := apidoc.ModelType{Name: "Request", Kind: "request"}
	operation := apidoc.ModelOperation{Name: "GetExample", Request: "Request", Response: "struct{}", AllowEmpty: true, Encoding: "none", Source: "api/example.md"}
	for index := range 13 {
		name, header := fmt.Sprintf("Field%d", index), fmt.Sprintf("X-Other-%d", index)
		if index == 1 || index == 12 {
			header = "X-Value"
		}
		request.Fields = append(request.Fields, apidoc.ModelField{Name: name, Type: "*string"})
		operation.Headers = append(operation.Headers, apidoc.ModelHeader{Field: name, Key: header})
	}
	models := apidoc.Models{Version: 1, Revision: "r1", Types: []apidoc.ModelType{request}, Operations: []apidoc.ModelOperation{operation}}
	if err := apidoc.ValidateModels(models, catalog); err != nil {
		t.Fatal(err)
	}
	document, err := apidoc.ExtractDocument("api/example.md", []byte(`## 2Заголовки
| Параметр | Тип |
| --- | --- |
| X-Value | Число |

## Заголовки
| Параметр | Тип |
| --- | --- |
| X-Value | Число |
`))
	if err != nil || len(document.Fields) != 2 {
		t.Fatalf("source extraction: %+v (%v)", document, err)
	}
	if document.Fields[0].Digest != document.Fields[1].Digest {
		t.Fatal("identical source rows should have identical digests")
	}
	problems := apidoc.CheckModelEvidence(apidoc.Documents{Pages: []apidoc.Document{document}}, models)
	want := map[[2]string]bool{
		{"/operations/0/headers/1", "2Заголовки / "}:  true,
		{"/operations/0/headers/12", "2Заголовки / "}: true,
		{"/operations/0/headers/1", "Заголовки / "}:   true,
		{"/operations/0/headers/12", "Заголовки / "}:  true,
	}
	if len(problems) != len(want) {
		t.Fatalf("lost distinct binding/section: got %d problems, want %d: %+v", len(problems), len(want), problems)
	}
	for _, problem := range problems {
		key := [2]string{problem.Target.Pointer, problem.Section}
		if !want[key] {
			t.Fatalf("unexpected or duplicate evidence: %+v", problem)
		}
		delete(want, key)
	}
}
