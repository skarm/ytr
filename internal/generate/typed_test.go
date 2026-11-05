package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"text/template"

	"github.com/skarm/ytr/internal/apidoc"
)

func TestRenderModelsPreservesOptionalPresence(t *testing.T) {
	t.Parallel()
	models := apidoc.Models{Types: []apidoc.ModelType{{Name: "Body", Kind: "struct", Fields: []apidoc.ModelField{{Name: "Notify", JSON: "notify", Type: "Optional[bool]", Optional: true}}}}}
	data, err := renderTypedFile("models.go", modelTemplate, models)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Optional[bool] `json:\"notify,omitzero\"`") {
		t.Fatalf("lost presence: %s", data)
	}
}

func TestGenerateRejectsInvalidInputWithoutReplacingOutput(t *testing.T) {
	for _, test := range []struct {
		name       string
		edit       func(*apidoc.Models)
		inputPath  string
		inputData  string
		removePath string
		errorText  string
		wantCause  error
	}{
		{name: "missing models", removePath: "api/models.json", wantCause: fs.ErrNotExist},
		{name: "invalid model JSON", inputPath: "api/models.json", inputData: "{", wantCause: io.ErrUnexpectedEOF},
		{name: "missing version", edit: func(models *apidoc.Models) { models.Version = 0 }, errorText: "version"},
		{name: "unsupported version", edit: func(models *apidoc.Models) { models.Version = 2 }, errorText: "version"},
		{name: "missing revision", edit: func(models *apidoc.Models) { models.Revision = "" }, errorText: "revision"},
		{name: "different revision", edit: func(models *apidoc.Models) { models.Revision = "another-revision" }, errorText: "revision"},
		{name: "duplicate operation", edit: func(models *apidoc.Models) { models.Operations = append(models.Operations, models.Operations[0]) }, errorText: "operation"},
		{name: "missing operation", edit: func(models *apidoc.Models) { models.Operations = models.Operations[:1] }, errorText: "operation"},
		{name: "unknown extra operation", edit: func(models *apidoc.Models) {
			models.Operations = append(models.Operations, apidoc.ModelOperation{Name: "UnknownOperation"})
		}, errorText: "operation"},
		{name: "unknown replacement operation", edit: func(models *apidoc.Models) { models.Operations[1].Name = "UnknownOperation" }, errorText: "operation"},
		{name: "missing catalog", removePath: "api/catalog.json", wantCause: fs.ErrNotExist},
		{name: "invalid catalog JSON", inputPath: "api/catalog.json", inputData: "{", wantCause: io.ErrUnexpectedEOF},
		{name: "invalid model Go syntax", edit: func(models *apidoc.Models) { models.Types[0].Name = "Invalid-Type" }, errorText: "type"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			catalog := writeTypedTestCatalog(t)
			writeTestModels(t, catalog)
			if test.edit != nil {
				var models apidoc.Models
				if err := json.Unmarshal(readTypedTestFile(t, "api/models.json"), &models); err != nil {
					t.Fatal(err)
				}
				test.edit(&models)
				writeTypedTestJSON(t, "api/models.json", models)
			}
			if test.inputPath != "" {
				if err := os.WriteFile(test.inputPath, []byte(test.inputData), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if test.removePath != "" {
				if err := os.Remove(test.removePath); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Mkdir("typed", 0o750); err != nil {
				t.Fatal(err)
			}
			previous := make(map[string][]byte)
			for _, path := range []string{"methods_gen.go", "typed/models_gen.go", "typed/client_gen.go"} {
				previous[path] = []byte("// existing " + path + "\npackage typed\n")
				if err := os.WriteFile(path, previous[path], 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err := generate()
			if err == nil {
				t.Fatal("invalid input accepted")
			}
			if test.errorText != "" && !strings.Contains(err.Error(), test.errorText) {
				t.Fatalf("error %v does not identify %q", err, test.errorText)
			}
			if test.wantCause != nil && !errors.Is(err, test.wantCause) {
				t.Fatalf("input error: %v; want cause %v", err, test.wantCause)
			}
			for path, want := range previous {
				if got := readTypedTestFile(t, path); !bytes.Equal(got, want) {
					t.Errorf("%s changed on failure: %s", path, got)
				}
			}
		})
	}
}

func TestGenerateTypedCreatesDeterministicGoFiles(t *testing.T) {
	t.Chdir(t.TempDir())
	catalog := writeTypedTestCatalog(t)
	writeTestModels(t, catalog)
	var models apidoc.Models
	if err := json.Unmarshal(readTypedTestFile(t, "api/models.json"), &models); err != nil {
		t.Fatal(err)
	}
	models.Types[0].Fields = []apidoc.ModelField{
		{Name: "EntityType", Type: "ytr.EntityType"},
		{Name: "EntityID", Type: "string"},
		{Name: "Notify", Type: "*bool"},
		{Name: "Force", Type: "bool"},
		{Name: "Page", Type: "*int64"},
		{Name: "Count", Type: "int64"},
		{Name: "Fields", Type: "[]string"},
		{Name: "Since", Type: "*string"},
		{Name: "Until", Type: "string"},
	}
	models.Operations[0].Paths = []apidoc.ModelPath{{Name: "EntityType", Type: "ytr.EntityType"}, {Name: "EntityID", Type: "string"}}
	models.Operations[0].Queries = []apidoc.ModelQuery{
		{Field: "Notify", Key: "notify", Type: "bool", Optional: true},
		{Field: "Force", Key: "force", Type: "bool"},
		{Field: "Page", Key: "page", Type: "int64", Optional: true},
		{Field: "Count", Key: "count", Type: "int64"},
		{Field: "Fields", Key: "fields", Type: "[]string", CSV: true},
		{Field: "Since", Key: "createdAt", Type: "string", Prefix: "from:", Optional: true},
		{Field: "Until", Key: "createdAt", Type: "string", Prefix: "to:"},
	}
	writeTypedTestJSON(t, "api/models.json", models)
	if err := generate(); err != nil {
		t.Fatal(err)
	}
	previous := make(map[string][]byte)
	for _, path := range []string{"typed/models_gen.go", "typed/client_gen.go"} {
		data := readTypedTestFile(t, path)
		parsed, err := parser.ParseFile(token.NewFileSet(), path, data, parser.AllErrors)
		if err != nil || parsed.Name.Name != "typed" {
			t.Fatalf("invalid generated Go file %s: %v", path, err)
		}
		for _, op := range catalog.Operations {
			name := op.Name
			if strings.HasSuffix(path, "models_gen.go") {
				name += "Request"
			}
			found := parsed.Scope.Lookup(name) != nil
			for _, declaration := range parsed.Decls {
				method, ok := declaration.(*ast.FuncDecl)
				if ok && method.Name.Name == name && method.Recv != nil {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s missing expected declaration %s", path, name)
			}
		}
		previous[path] = data
	}
	if err := generate(); err != nil {
		t.Fatal(err)
	}
	for path, want := range previous {
		if got := readTypedTestFile(t, path); !bytes.Equal(got, want) {
			t.Errorf("%s generation is not deterministic", path)
		}
	}
}

func TestGenerateReportsBlockedOutputPaths(t *testing.T) {
	for _, path := range []string{"typed", "methods_gen.go", "typed/models_gen.go", "typed/client_gen.go"} {
		t.Run(path, func(t *testing.T) {
			t.Chdir(t.TempDir())
			catalog := writeTypedTestCatalog(t)
			writeTestModels(t, catalog)
			blocker := path
			if path != "typed" {
				if err := os.MkdirAll(path, 0o750); err != nil {
					t.Fatal(err)
				}
				blocker = filepath.Join(path, "keep.txt")
			}
			if err := os.WriteFile(blocker, []byte("keep existing output"), 0o600); err != nil {
				t.Fatal(err)
			}
			previous := make(map[string][]byte)
			for _, output := range []string{"methods_gen.go", "typed/models_gen.go", "typed/client_gen.go"} {
				if output == path || path == "typed" {
					continue
				}
				if err := os.MkdirAll(filepath.Dir(output), 0o750); err != nil {
					t.Fatal(err)
				}
				previous[output] = []byte("// previous " + output + "\n")
				if err := os.WriteFile(output, previous[output], 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err := generate()
			var pathError *os.PathError
			if !errors.As(err, &pathError) || pathError.Path != path {
				t.Fatalf("blocked output %s: %T %v", path, err, err)
			}
			if got := string(readTypedTestFile(t, blocker)); got != "keep existing output" {
				t.Fatalf("blocker changed: %q", got)
			}
			for output, want := range previous {
				if got := readTypedTestFile(t, output); !bytes.Equal(got, want) {
					t.Errorf("%s changed before a known blocked output was rejected", output)
				}
			}
		})
	}
}

func TestRenderTypedFileReturnsNoOutputOnTemplateAndFormatErrors(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		source     string
		wantExec   bool
		wantFormat bool
	}{
		{name: "template syntax", source: "{{"},
		{name: "template execution", source: "package typed\nvar Value = {{.MissingField}}\n", wantExec: true},
		{name: "Go syntax", source: "package typed\nfunc (", wantFormat: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := "output.go"
			data, err := renderTypedFile(path, test.source, apidoc.Models{Version: 1})
			if err == nil {
				t.Fatal("invalid source accepted")
			}
			var executionError template.ExecError
			if test.wantExec && !errors.As(err, &executionError) {
				t.Fatalf("execution error: %T %v", err, err)
			}
			if test.wantFormat && !strings.Contains(err.Error(), "format "+path) {
				t.Fatalf("format error lost target path: %v", err)
			}
			if data != nil {
				t.Fatalf("failed render returned output: %s", data)
			}
		})
	}
}

func TestWriteGeneratedFileReplacesCompleteSource(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "generated.go")
	if err := os.WriteFile(path, []byte("old source"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := []byte("package generated\n")
	if err := writeGeneratedFile(path, want); err != nil {
		t.Fatal(err)
	}
	if got := readTypedTestFile(t, path); !bytes.Equal(got, want) {
		t.Fatalf("wrong source: %s", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o644 {
		t.Fatalf("generated source permissions: %o", info.Mode().Perm())
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary output not removed: %v, %v", entries, err)
	}
}

func TestWriteGeneratedFileCleansUpAfterRenameFailure(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "blocked.go")
	if err := os.Mkdir(path, 0o750); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(path, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeGeneratedFile(path, []byte("package generated\n")); err == nil {
		t.Fatal("directory output was replaced")
	}
	if got := readTypedTestFile(t, marker); string(got) != "keep" {
		t.Fatalf("blocked output changed: %s", got)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary output not removed after failure: %v, %v", entries, err)
	}
}

func writeTestModels(t *testing.T, catalog apidoc.Catalog) {
	t.Helper()
	models := apidoc.Models{Version: 1, Revision: catalog.Revision}
	for _, op := range catalog.Operations {
		request := apidoc.ModelType{Name: op.Name + "Request", Kind: "request", Source: op.Source}
		binding := apidoc.ModelOperation{Name: op.Name, Request: request.Name, Response: "json.RawMessage", Encoding: op.Request, Stream: op.Response == "stream", Source: op.Source}
		for _, name := range apidoc.PathParameters(op.Path) {
			field := apidoc.ModelPath{Name: strings.ToUpper(name[:1]) + name[1:], Type: "string"}
			if name == "entityType" {
				field.Type = "ytr.EntityType"
			}
			request.Fields = append(request.Fields, apidoc.ModelField{Name: field.Name, Type: field.Type})
			binding.Paths = append(binding.Paths, field)
		}
		if op.Response == "stream" {
			binding.Response = ""
		}
		if op.Request == "json" {
			binding.Body = "json.RawMessage"
			request.Fields = append(request.Fields, apidoc.ModelField{Name: "Body", Type: "json.RawMessage"})
		}
		if op.Request == "multipart" {
			request.Fields = append(request.Fields, apidoc.ModelField{Name: "File", Type: "ytr.File"})
		}
		models.Types = append(models.Types, request)
		models.Operations = append(models.Operations, binding)
	}
	data, err := json.Marshal(models)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("api/models.json", data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeTypedTestCatalog(t *testing.T) apidoc.Catalog {
	t.Helper()
	if err := os.Mkdir("api", 0o750); err != nil {
		t.Fatal(err)
	}
	catalog := apidoc.Catalog{Revision: "typed-fixture-revision", VerifiedOn: "2026-10-30", Operations: []apidoc.Operation{
		{Name: "GetEntity", Method: "GET", Path: "/v3/entities/{entityType}/{entityID}", Request: "none", Response: "json", Source: "api/get.md"},
		{Name: "CreateIssue", Method: "POST", Path: "/v3/issues", Request: "json", Response: "json", Source: "api/create.md"},
	}}
	writeTypedTestJSON(t, "api/catalog.json", catalog)
	return catalog
}

func writeTypedTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readTypedTestFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
