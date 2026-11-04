package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/skarm/ytr/internal/apidoc"
)

type localTransport func(*http.Request) (*http.Response, error)

func TestOnlineCommandChecksBothSuccessAndFailure(t *testing.T) {
	t.Chdir(t.TempDir())
	os.Mkdir("api", 0o755)
	doc := []byte("GET https://api.tracker.yandex.net/v3/items\n")
	url := "https://yandex.ru/support/tracker/ru/api/get.md?revision=r1"
	index := "- [Get](" + url + ")\n"
	catalog := apidoc.Catalog{Revision: "r1", VerifiedOn: "2026-10-30", Operations: []apidoc.Operation{{Name: "GetItem", Method: "GET", Path: "/v3/items", Request: "none", Response: "json", Source: "api/get.md"}}}
	sources := apidoc.Sources{Pages: []apidoc.Page{{Path: "api/get.md", URL: url, SHA256: fmt.Sprintf("%x", sha256.Sum256(doc)), ContentSHA256: apidoc.ContentDigest(doc), Operations: []string{"GetItem"}, Signatures: []string{"GET /v3/items"}}}}
	for name, value := range map[string]any{"catalog.json": catalog, "sources.json": sources} {
		data, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join("api", name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile("api/index.txt", []byte(index), 0o600)
	writeExtraInventory(t, catalog, sources, doc)
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = localTransport(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) < time.Second {
			t.Error("documentation request has no usable timeout")
		}
		body := doc
		if strings.Contains(r.URL.String(), "llms.txt") {
			body = []byte(index)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})
	if err := check(true); err != nil {
		t.Fatal(err)
	}
	http.DefaultTransport = localTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") })
	if err := check(true); err == nil {
		t.Fatal("online error ignored")
	}
}

func TestMainCLIExitStatusAndOfflineDefault(t *testing.T) {
	source, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		dir  string
		code int
	}{{source, 0}, {t.TempDir(), 1}} {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestCLIHelperProcess$")
		cmd.Dir = tc.dir
		cmd.Env = append(os.Environ(), "YTR_CHECKDOCS_HELPER=1")
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

func TestCheckOfflineUsesCompleteInventory(t *testing.T) {
	dir := t.TempDir()
	source, err := filepath.Abs("../../api")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	if err = check(false); err == nil {
		t.Fatal("missing files accepted")
	}
	if err = os.Mkdir("api", 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"catalog.json", "sources.json", "index.txt", "models.json", "documentation.json"} {
		data, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join("api", name), data, 0o600); err != nil {
			t.Fatal(err)
		}
		if name != "documentation.json" {
			if err = check(false); err == nil {
				t.Fatal("incomplete inventory accepted")
			}
		}
	}
	if err = check(false); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile("api/index.txt", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = check(false); err == nil {
		t.Fatal("changed index accepted")
	}
}

func TestCLIHelperProcess(_ *testing.T) {
	if os.Getenv("YTR_CHECKDOCS_HELPER") != "1" {
		return
	}
	os.Args = []string{"checkdocs"}
	flag.CommandLine = flag.NewFlagSet("checkdocs", flag.ExitOnError)
	http.DefaultTransport = localTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("unexpected network access") })
	main()
	os.Exit(0)
}

func (f localTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func writeExtraInventory(t *testing.T, catalog apidoc.Catalog, sources apidoc.Sources, body []byte) {
	t.Helper()
	models := apidoc.Models{Version: 1, Revision: catalog.Revision}
	for _, operation := range catalog.Operations {
		models.Types = append(models.Types, apidoc.ModelType{Name: operation.Name + "Request", Kind: "request"})
		models.Operations = append(models.Operations, apidoc.ModelOperation{Name: operation.Name, Request: operation.Name + "Request", Response: "json.RawMessage", Encoding: operation.Request, Source: operation.Source})
	}
	if err := apidoc.WriteJSON("api/models.json", models); err != nil {
		t.Fatal(err)
	}
	documents := apidoc.Documents{Version: 1, Revision: catalog.Revision}
	for _, page := range sources.Pages {
		document, err := apidoc.ExtractDocument(page.Path, body)
		if err != nil {
			t.Fatal(err)
		}
		document.SHA256 = fmt.Sprintf("%x", sha256.Sum256(body))
		documents.Pages = append(documents.Pages, document)
	}
	if err := apidoc.WriteJSON("api/documentation.json", documents); err != nil {
		t.Fatal(err)
	}
}
