package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skarm/ytr/internal/apidoc"
)

func TestGenerateValidatesAllInputsBeforeWriting(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := os.ReadFile(filepath.Join(root, "api/catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	input, err := os.ReadFile(filepath.Join(root, "api/models.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		edit func(*apidoc.Models)
	}{
		{"revision", func(m *apidoc.Models) { m.Revision = "invalid" }},
		{"request encoding", func(m *apidoc.Models) { m.Operations[0].Encoding = "multipart" }},
		{"response encoding", func(m *apidoc.Models) { m.Operations[0].Stream = true }},
		{"source", func(m *apidoc.Models) { m.Operations[0].Source = "api/other.md" }},
		{"missing path", func(m *apidoc.Models) { m.Operations[1].Paths = nil }},
		{"path order", func(m *apidoc.Models) {
			for i := range m.Operations {
				paths := m.Operations[i].Paths
				if len(paths) > 1 {
					paths[0], paths[1] = paths[1], paths[0]
					return
				}
			}
		}},
		{"path type", func(m *apidoc.Models) { m.Operations[1].Paths[0].Type = "ytr.EntityType" }},
		{"model syntax", func(m *apidoc.Models) { m.Types[0].Name = "Invalid-Type" }},
		{"client syntax", func(m *apidoc.Models) { m.Operations[0].Response = "Invalid-Type" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			if err := os.MkdirAll("api", 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile("api/catalog.json", catalog, 0o600); err != nil {
				t.Fatal(err)
			}
			var models apidoc.Models
			if err := json.Unmarshal(input, &models); err != nil {
				t.Fatal(err)
			}
			test.edit(&models)
			writeTypedTestJSON(t, "api/models.json", models)
			if err := os.MkdirAll("typed", 0o750); err != nil {
				t.Fatal(err)
			}
			previous := map[string][]byte{}
			for _, path := range []string{"methods_gen.go", "typed/models_gen.go", "typed/client_gen.go"} {
				previous[path] = []byte("// existing " + path + "\n")
				if err := os.WriteFile(path, previous[path], 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := generate(); err == nil {
				t.Error("inconsistent or invalid input accepted")
			}
			for path, want := range previous {
				if got := readTypedTestFile(t, path); !bytes.Equal(got, want) {
					t.Errorf("%s changed after input failure", path)
				}
			}
		})
	}
}

func TestGenerateRejectsUnknownModelFields(t *testing.T) {
	t.Chdir(t.TempDir())
	catalog := writeTypedTestCatalog(t)
	writeTestModels(t, catalog)
	path := "api/models.json"
	data := readTypedTestFile(t, path)
	data = append([]byte(`{"unexpected": true,`), data[1:]...)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := generate(); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown schema field: %v", err)
	}
	if _, err := os.Stat("methods_gen.go"); !os.IsNotExist(err) {
		t.Fatalf("output was created for invalid input: %v", err)
	}
}

func TestGeneratorRejectsUnusableMethodAndPathIdentifiers(t *testing.T) {
	for _, test := range []struct{ name, path, request, response string }{
		{"Call", "/v3/items", "", ""},
		{"Raw", "/v3/items", "", ""},
		{"GetItems", "/v3/items/{ctx}", "", ""},
		{"GetItems", "/v3/items/{options}", "", ""},
		{"GetItems", "/v3/items/{id}/copies/{id}", "", ""},
		{"GetItems", "/v3/items/{missing", "", ""},
		{"GetItems", "/v3/items/prefix-{id}", "", ""},
		{"GetItems", "/v3/items/{c}", "", ""},
		{"GetItems", "/v3/items/{operations}", "", ""},
		{"GetItems", "/v3/items/{string}", "", ""},
		{"GetItems", "/v3/items/{nil}", "", ""},
		{"GetItems", "/v3/items?fixed=true", "", ""},
		{"GetItems", "/v3/items#fragment", "", ""},
		{"DownloadItems", "/v3/items", "json", "stream"},
	} {
		t.Run(test.name+test.path, func(t *testing.T) {
			t.Chdir(t.TempDir())
			if err := os.MkdirAll("api", 0o750); err != nil {
				t.Fatal(err)
			}
			request, response := test.request, test.response
			if request == "" {
				request = "none"
			}
			if response == "" {
				response = "json"
			}
			catalog := apidoc.Catalog{Revision: "r1", VerifiedOn: "2026-10-31", Operations: []apidoc.Operation{{Name: test.name, Method: "GET", Path: test.path, Request: request, Response: response, Source: "api/items.md"}}}
			writeTypedTestJSON(t, "api/catalog.json", catalog)
			writeTestModels(t, catalog)
			if err := os.MkdirAll("typed", 0o750); err != nil {
				t.Fatal(err)
			}
			names := []string{"methods_gen.go", "typed/models_gen.go", "typed/client_gen.go"}
			for _, name := range names {
				if err := os.WriteFile(name, []byte("existing source\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := generate(); err == nil {
				t.Error("unusable catalog accepted")
			}
			for _, name := range names {
				if !bytes.Equal(readTypedTestFile(t, name), []byte("existing source\n")) {
					t.Errorf("%s overwritten for an unusable catalog", name)
				}
			}
		})
	}
}
