package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skarm/ytr/internal/apidoc"
)

func TestCheckedInBindingsMatchGenerator(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	for _, name := range []string{"api/catalog.json", "api/models.json"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(destination, name)
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(destination)
	if err := generate(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"methods_gen.go", "typed/models_gen.go", "typed/client_gen.go"} {
		actual, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		expected, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(actual, expected) {
			t.Fatalf("%s is out of date; run go generate ./...", name)
		}
	}
}

func TestGeneratorCLIExitStatus(t *testing.T) {
	source, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(source, "api/catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	good := t.TempDir()
	os.Mkdir(filepath.Join(good, "api"), 0o755)
	os.WriteFile(filepath.Join(good, "api/catalog.json"), data, 0o600)
	models, err := os.ReadFile(filepath.Join(source, "api/models.json"))
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(good, "api/models.json"), models, 0o600)
	for _, tc := range []struct {
		dir  string
		code int
	}{{good, 0}, {t.TempDir(), 1}} {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestGeneratorCLIHelper$")
		cmd.Dir = tc.dir
		cmd.Env = append(os.Environ(), "YTR_GENERATE_HELPER=1")
		output, err := cmd.CombinedOutput()
		code := 0
		failure := &exec.ExitError{}
		if errors.As(err, &failure) {
			code = failure.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		if code != tc.code {
			t.Fatalf("exit %d want %d: %s", code, tc.code, output)
		}
	}
}

func TestGenerateAllWireShapes(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := generate(); err == nil {
		t.Fatal("missing catalog accepted")
	}
	if err := os.Mkdir("api", 0o755); err != nil {
		t.Fatal(err)
	}
	catalog := apidoc.Catalog{Revision: "r1", VerifiedOn: "2026-10-30", Operations: []apidoc.Operation{
		{Name: "GetEntity", Method: "GET", Path: "/v3/entities/{entityType}/{entityID}", Request: "none", Response: "json", Source: "api/get.md"},
		{Name: "CreateIssue", Method: "POST", Path: "/v3/issues", Request: "json", Response: "json", Source: "api/create.md"},
		{Name: "UploadFile", Method: "POST", Path: "/v3/attachments", Request: "multipart", Response: "json", Source: "api/upload.md"},
		{Name: "DownloadFile", Method: "GET", Path: "/v3/issues/{issueID}/attachments/{fileID}", Request: "none", Response: "stream", Source: "api/download.md"},
	}}
	data, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join("api", "catalog.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestModels(t, catalog)
	if err = generate(); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile("methods_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = parser.ParseFile(token.NewFileSet(), "methods_gen.go", first, parser.AllErrors); err != nil {
		t.Fatal(err)
	}
	for _, signature := range []string{"GetEntity(ctx context.Context, entityType EntityType, entityID string, options ...CallOption)", "CreateIssue(ctx context.Context, body any, options ...CallOption)", "UploadFile(ctx context.Context, file File, options ...CallOption)", "(*Download, error)", "c.call(", "c.upload(", "c.download(", "operationIndex = map[OperationName]int", "operations[0]"} {
		if !strings.Contains(string(first), signature) {
			t.Fatalf("missing %s", signature)
		}
	}
	if err = generate(); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile("methods_gen.go")
	if string(first) != string(second) {
		t.Fatal("generation is not deterministic")
	}
}

func TestGeneratorCLIHelper(_ *testing.T) {
	if os.Getenv("YTR_GENERATE_HELPER") != "1" {
		return
	}
	main()
	os.Exit(0)
}
