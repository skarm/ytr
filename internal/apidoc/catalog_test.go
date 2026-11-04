package apidoc_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skarm/ytr/internal/apidoc"
)

func TestCheckInventoryAndMissingEvidence(t *testing.T) {
	c, s, index := fixture()
	if err := apidoc.Check(c, s, index); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*apidoc.Catalog, *apidoc.Sources){
		"missing page":        func(_ *apidoc.Catalog, s *apidoc.Sources) { s.Pages = nil },
		"duplicate page":      func(_ *apidoc.Catalog, s *apidoc.Sources) { s.Pages = append(s.Pages, s.Pages[0]) },
		"unexpected source":   func(_ *apidoc.Catalog, s *apidoc.Sources) { s.Pages[0].URL = "https://other.test" },
		"missing checksum":    func(_ *apidoc.Catalog, s *apidoc.Sources) { s.Pages[0].SHA256 = "" },
		"missing path":        func(_ *apidoc.Catalog, s *apidoc.Sources) { s.Pages[0].Path = "" },
		"unclassified":        func(_ *apidoc.Catalog, s *apidoc.Sources) { s.Pages[0].Operations = nil },
		"unknown operation":   func(_ *apidoc.Catalog, s *apidoc.Sources) { s.Pages[0].Operations = []string{"Unknown"} },
		"wrong signature":     func(_ *apidoc.Catalog, s *apidoc.Sources) { s.Pages[0].Signatures = []string{"POST /v3/issues/{id}"} },
		"uncovered operation": func(_ *apidoc.Catalog, s *apidoc.Sources) { s.Pages[0].Signatures = nil },
		"invalid catalog":     func(c *apidoc.Catalog, _ *apidoc.Sources) { c.Revision = "" },
	} {
		t.Run(name, func(t *testing.T) {
			c, s, index := fixture()
			change(&c, &s)
			if err := apidoc.Check(c, s, index); err == nil {
				t.Fatal("bad inventory accepted")
			}
		})
	}
}

func TestCatalogValidation(t *testing.T) {
	for name, change := range map[string]func(*apidoc.Catalog){
		"empty revision":   func(c *apidoc.Catalog) { c.Revision = "" },
		"empty date":       func(c *apidoc.Catalog) { c.VerifiedOn = "" },
		"empty operations": func(c *apidoc.Catalog) { c.Operations = nil },
		"duplicate":        func(c *apidoc.Catalog) { c.Operations = append(c.Operations, c.Operations[0]) },
		"duplicate route": func(c *apidoc.Catalog) {
			op := c.Operations[0]
			op.Name = "OtherName"
			c.Operations = append(c.Operations, op)
		},
		"bad name":        func(c *apidoc.Catalog) { c.Operations[0].Name = "not Go" },
		"bad source":      func(c *apidoc.Catalog) { c.Operations[0].Source = "outside" },
		"bad path":        func(c *apidoc.Catalog) { c.Operations[0].Path = "/other/x" },
		"bad method":      func(c *apidoc.Catalog) { c.Operations[0].Method = "OPTIONS" },
		"compound method": func(c *apidoc.Catalog) { c.Operations[0].Method = "GET POST" },
		"bad request":     func(c *apidoc.Catalog) { c.Operations[0].Request = "guess" },
		"bad response":    func(c *apidoc.Catalog) { c.Operations[0].Response = "guess" },
	} {
		t.Run(name, func(t *testing.T) {
			c, _, _ := fixture()
			change(&c)
			if err := apidoc.Validate(c); err == nil {
				t.Fatal("bad catalog accepted")
			}
		})
	}
	c, _, _ := fixture()
	c.Operations[0].Path = "/v2/issues/{id}"
	if err := apidoc.Validate(c); err != nil {
		t.Fatal(err)
	}
}

func TestLoadStrictJSONAndParameters(t *testing.T) {
	if names := apidoc.PathParameters("/v3/queues"); names != nil {
		t.Fatalf("path without placeholders returned %v, want nil", names)
	}

	if names := apidoc.PathParameters("/v3/entities/{entityType}/{entityID}"); strings.Join(names, ",") != "entityType,entityID" {
		t.Fatal(names)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.json")
	for _, body := range []string{`{`, `{"unexpected":true}`, `{}`} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := apidoc.Load(path); err == nil {
			t.Fatal(body)
		}
	}
	if _, err := apidoc.Load(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing file accepted")
	}
	if got := apidoc.IndexURLs("unrelated"); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestCatalogRejectsTrailingJSONValue(t *testing.T) {
	c, _, _ := fixture()
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "catalog.json")
	if err = os.WriteFile(path, append(data, []byte(" {}")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = apidoc.Load(path); err == nil {
		t.Fatal("trailing JSON value accepted")
	}
}

func TestNamesCannotBeReusedOnDifferentRoutes(t *testing.T) {
	c, _, _ := fixture()
	other := c.Operations[0]
	other.Path = "/v3/queues/{id}"
	c.Operations = append(c.Operations, other)
	if err := apidoc.Validate(c); err == nil {
		t.Fatal("duplicate name accepted")
	}
}

func TestInventoryRejectsPagePathThatDiffersFromItsURL(t *testing.T) {
	catalog, sources, index := fixture()
	sources.Pages[0].Path = "api/issues/unrelated-page.md"
	if err := apidoc.Check(catalog, sources, index); err == nil {
		t.Fatal("source URL and checksums were attributed to a different page path")
	}
}

func TestInventoryRequiresTheOperationsPrimarySourcePage(t *testing.T) {
	catalog, sources, index := fixture()
	catalog.Operations[0].Source = "api/issues/missing-page.md"
	if err := apidoc.Check(catalog, sources, index); err == nil {
		t.Fatal("operation with an absent primary documentation source was accepted")
	}

	catalog, sources, index = fixture()
	extra := sources.Pages[0]
	extra.Path = "api/issues/supplement.md"
	extra.URL = "https://yandex.ru/support/tracker/ru/" + extra.Path
	sources.Pages = append(sources.Pages, extra)
	index += "- [Supplement](" + extra.URL + ")\n"
	if err := apidoc.Check(catalog, sources, index); err != nil {
		t.Fatalf("supplementary page may still reference the same operation: %v", err)
	}
}

func fixture() (apidoc.Catalog, apidoc.Sources, string) {
	op := apidoc.Operation{Name: "GetIssue", Method: "GET", Path: "/v3/issues/{issueID}", Request: "none", Response: "json", Source: "api/issues/get-issue.md"}
	url := "https://yandex.ru/support/tracker/ru/api/issues/get-issue.md?revision=r1"
	return apidoc.Catalog{Revision: "r1", VerifiedOn: "2026-10-30", Operations: []apidoc.Operation{op}}, apidoc.Sources{Pages: []apidoc.Page{{Path: op.Source, URL: url, SHA256: strings.Repeat("a", 64), ContentSHA256: strings.Repeat("a", 64), Operations: []string{"GetIssue"}, Signatures: []string{"GET /v3/issues/{id_задачи}"}}}}, "- [Get](" + url + ")\n"
}

func TestUnicodePathParameterUsesItsCompleteFirstRune(t *testing.T) {
	catalog := apidoc.Catalog{Revision: "r1", VerifiedOn: "2026-10-31", Operations: []apidoc.Operation{{Name: "GetItem", Method: "GET", Path: "/v3/items/{идентификатор}", Request: "none", Response: "json", Source: "api/items.md"}}}
	if err := apidoc.Validate(catalog); err != nil {
		t.Fatal(err)
	}
	models := apidoc.Models{Version: 1, Revision: "r1", Types: []apidoc.ModelType{{Name: "GetItemRequest", Kind: "request", Fields: []apidoc.ModelField{{Name: "Идентификатор", Type: "string"}}}}, Operations: []apidoc.ModelOperation{{Name: "GetItem", Request: "GetItemRequest", Response: "json.RawMessage", Encoding: "none", Source: "api/items.md", Paths: []apidoc.ModelPath{{Name: "Идентификатор", Type: "string"}}}}}
	if err := apidoc.ValidateModels(models, catalog); err != nil {
		t.Fatalf("valid Unicode identifier corrupted by model validation: %v", err)
	}
}

func TestCatalogDoesNotReserveUnrelatedGoIdentifiers(t *testing.T) {
	for _, test := range []struct{ path, request, response string }{
		{"/v3/items/{body}/{file}/{any}/{error}/{context}", "none", "json"},
		{"/v3/items/{nil}", "none", "stream"},
	} {
		catalog := apidoc.Catalog{Revision: "r1", VerifiedOn: "2026-10-31", Operations: []apidoc.Operation{{Name: "GetItem", Method: "GET", Path: test.path, Request: test.request, Response: test.response, Source: "api/items.md"}}}
		if err := apidoc.Validate(catalog); err != nil {
			t.Fatalf("valid non-conflicting identifiers rejected: %v", err)
		}
	}
}
