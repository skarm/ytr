package apidoc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// CaptureDocuments extracts source facts from every page, using the caller's
// fetcher so offline tests never require a network or Tracker credentials.
func CaptureDocuments(ctx context.Context, sources Sources, fetch Fetcher) (Documents, error) {
	result := Documents{Version: 1, Pages: make([]Document, len(sources.Pages))}
	jobs := make(chan int)
	failures := make(chan error, len(sources.Pages))

	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for index := range jobs {
				page := sources.Pages[index]

				body, err := fetch(ctx, stripURLQuery(page.URL))
				if err == nil {
					result.Pages[index], err = ExtractDocument(page.Path, body)
					result.Pages[index].SHA256 = fmt.Sprintf("%x", sha256.Sum256(body))
				}

				if err != nil {
					failures <- fmt.Errorf("%s: %w", page.Path, err)
				}
			}
		})
	}

	for index := range sources.Pages {
		jobs <- index
	}

	close(jobs)
	workers.Wait()
	close(failures)

	var problems []error
	for err := range failures {
		problems = append(problems, err)
	}

	sort.Slice(problems, func(i, j int) bool { return problems[i].Error() < problems[j].Error() })

	return result, errors.Join(problems...)
}

// LocalDocuments reads a developer-selected directory of downloaded Markdown.
func LocalDocuments(ctx context.Context, sources Sources, directory string) (Documents, error) {
	return CaptureDocuments(ctx, sources, func(_ context.Context, address string) ([]byte, error) {
		for _, page := range sources.Pages {
			if stripURLQuery(page.URL) == address {
				if !safeDocumentPath(page.Path) {
					return nil, errors.New("unsafe documentation path")
				}

				return os.ReadFile(filepath.Join(directory, filepath.FromSlash(page.Path))) // #nosec G304 -- Directory is explicitly selected by the local developer; relative inventory paths are validated above.
			}
		}

		return nil, errors.New("unknown documentation URL")
	})
}

func safeDocumentPath(value string) bool {
	return path.Clean(value) == value && strings.HasPrefix(value, "api/") && !strings.Contains(value, `\`)
}

// CheckDocuments binds a source-derived snapshot to the reviewed source hashes.
func CheckDocuments(documents Documents, catalog Catalog, sources Sources) error {
	if documents.Version != 1 || documents.Revision != catalog.Revision || len(documents.Pages) != len(sources.Pages) {
		return errors.New("documentation snapshot version/revision/coverage mismatch")
	}

	byPath := make(map[string]Page, len(sources.Pages))
	for _, page := range sources.Pages {
		byPath[page.Path] = page
	}

	seen := make(map[string]bool)

	for _, document := range documents.Pages {
		if !safeDocumentPath(document.Path) {
			return errors.New("unsafe documentation snapshot path")
		}

		page, exists := byPath[document.Path]
		if !exists || seen[document.Path] || document.Digest != page.ContentSHA256 || len(document.SHA256) != 64 {
			return fmt.Errorf("documentation snapshot %s: source hash or identity mismatch", document.Path)
		}

		seen[document.Path] = true
		if err := checkCapturedSignatures(document, page); err != nil {
			return err
		}

		if err := CheckDocumentRoutes(document, page); err != nil {
			return err
		}

		for _, example := range document.Examples {
			if !json.Valid(example.Value) || example.Line <= 0 {
				return fmt.Errorf("documentation snapshot %s: invalid example", document.Path)
			}
		}
	}

	return nil
}

// VerifyCapturedDocuments rejects edited facts even when their page hash metadata
// was left unchanged. The caller independently fetches and parses pinned sources.
func VerifyCapturedDocuments(expected, actual Documents) error {
	if expected.Version != actual.Version || expected.Revision != actual.Revision || len(expected.Pages) != len(actual.Pages) {
		return errors.New("candidate source capture coverage/revision mismatch")
	}

	pages := map[string]Document{}
	for _, page := range actual.Pages {
		pages[page.Path] = page
	}

	for _, page := range expected.Pages {
		other, exists := pages[page.Path]
		if !exists || !bytes.Equal(documentFactBytes(page), documentFactBytes(other)) {
			return fmt.Errorf("candidate facts for %s differ from the pinned official source; create a fresh report", page.Path)
		}
	}

	return nil
}

// WriteJSON writes a complete local artifact via rename after encoding succeeds.
func WriteJSON(file string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}

	return WriteArtifact(file, append(data, '\n'))
}

// WriteArtifact atomically replaces one developer-selected artifact.
func WriteArtifact(file string, data []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(file), ".ytr-artifact-*")
	if err != nil {
		return err
	}

	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()

	_, writeErr := temporary.Write(data)
	if err := errors.Join(writeErr, temporary.Close()); err != nil {
		return err
	}

	return os.Rename(name, file)
}

func checkCapturedSignatures(document Document, page Page) error {
	captured := make(map[string]bool, len(document.Signatures))
	for _, signature := range document.Signatures {
		captured[RouteShape(signature)] = true
	}

	for _, signature := range page.Signatures {
		if !captured[RouteShape(signature)] {
			return fmt.Errorf("documentation snapshot %s: declared signature %s is absent from official source facts", document.Path, signature)
		}
	}

	return nil
}

func documentFactBytes(document Document) []byte {
	document.SHA256 = ""

	document.Fields = append([]DocumentField(nil), document.Fields...)
	for i := range document.Fields {
		document.Fields[i].Line = 0
	}

	document.Examples = append([]DocumentExample(nil), document.Examples...)
	for i := range document.Examples {
		document.Examples[i].Line = 0
	}

	data, _ := json.Marshal(document)

	return data
}
