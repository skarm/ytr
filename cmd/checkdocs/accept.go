package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/skarm/ytr/internal/apidoc"
)

func acceptReview(ctx context.Context, directory string) error {
	return acceptReviewWithVerifier(ctx, directory, verifyCandidate)
}

func acceptReviewWithVerifier(ctx context.Context, directory string, verify func(context.Context, string) error) error {
	names := []string{"api/catalog.json", "api/models.json", "api/sources.json", "api/index.txt", "api/documentation.json", "typed/testdata/requests.json", "methods_gen.go", "typed/models_gen.go", "typed/client_gen.go"}

	expected, err := readAcceptanceFiles(names)
	if err != nil {
		return err
	}

	var review apidoc.Review
	if err := apidoc.ReadJSON(filepath.Join(directory, "report.json"), &review); err != nil {
		return err
	}

	var candidate, baseline apidoc.Documents
	if err := apidoc.ReadJSON(filepath.Join(directory, "candidate.json"), &candidate); err != nil {
		return err
	}

	if err := apidoc.ReadJSON("api/documentation.json", &baseline); err != nil {
		return err
	}

	if review.Version != 1 || review.BaseDigest != apidoc.DocumentsDigest(baseline) || candidate.Revision == "" || candidate.Revision != review.Revision {
		return errors.New("checkdocs: stale or inconsistent review candidate; create a fresh report")
	}

	reviewFiles, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer func() { _ = reviewFiles.Close() }()

	index, err := reviewFiles.ReadFile("index.txt")
	if err != nil {
		return err
	}

	if review.CandidateDigest != apidoc.DocumentsDigest(candidate) || review.IndexDigest != fmt.Sprintf("%x", sha256.Sum256(index)) {
		return errors.New("checkdocs: report/candidate/index belong to different captures; create a fresh report")
	}

	catalog, err := apidoc.Load("api/catalog.json")
	if err != nil {
		return err
	}

	models, err := apidoc.LoadModels("api/models.json", catalog)
	if err != nil {
		return err
	}

	var sources apidoc.Sources
	if err := apidoc.ReadJSON("api/sources.json", &sources); err != nil {
		return err
	}

	updated, err := acceptedSources(catalog, sources, candidate, string(index))
	if err != nil {
		return err
	}

	catalog.Revision, catalog.VerifiedOn = candidate.Revision, time.Now().Format(time.DateOnly)
	if err := apidoc.Check(catalog, updated, string(index)); err != nil {
		return err
	}

	if err := apidoc.CheckDocuments(candidate, catalog, updated); err != nil {
		return err
	}

	if err := apidoc.CheckModelChanges(baseline, candidate, models, updated); err != nil {
		return err
	}

	stage, err := os.MkdirTemp("", "ytr-accept-*")
	if err != nil {
		return err
	}

	defer func() { _ = os.RemoveAll(stage) }()

	if err := copyCheckSources(stage); err != nil {
		return err
	}

	inputs, err := hashVerificationInputs(stage)
	if err != nil {
		return err
	}

	if err := apidoc.WriteJSON(filepath.Join(stage, "api/catalog.json"), catalog); err != nil {
		return err
	}

	if err := apidoc.WriteJSON(filepath.Join(stage, "api/sources.json"), updated); err != nil {
		return err
	}

	if err := apidoc.WriteJSON(filepath.Join(stage, "api/documentation.json"), candidate); err != nil {
		return err
	}

	if err := apidoc.WriteArtifact(filepath.Join(stage, "api/index.txt"), index); err != nil {
		return err
	}

	if err := writeRevisionFile("api/models.json", filepath.Join(stage, "api/models.json"), candidate.Revision); err != nil {
		return err
	}

	if err := writeRevisionFile("typed/testdata/requests.json", filepath.Join(stage, "typed/testdata/requests.json"), candidate.Revision); err != nil {
		return err
	}

	if err := verifyAndInstallCandidate(ctx, stage, names, expected, inputs, verify); err != nil {
		return err
	}

	fmt.Printf("Accepted Tracker documentation %s; isolated generation, tests and vet passed\n", candidate.Revision)

	return nil
}

func verifyAndInstallCandidate(ctx context.Context, stage string, names []string, expected map[string][]byte, inputs map[string][32]byte, verify func(context.Context, string) error) error {
	if err := verify(ctx, stage); err != nil {
		return err
	}

	if err := checkUnchangedSources(inputs); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	return installAcceptedFiles(stage, names, expected, apidoc.WriteArtifact)
}

func verifyCandidate(ctx context.Context, stage string) error {
	var documents apidoc.Documents
	if err := apidoc.ReadJSON(filepath.Join(stage, "api/documentation.json"), &documents); err != nil {
		return err
	}

	var sources apidoc.Sources
	if err := apidoc.ReadJSON(filepath.Join(stage, "api/sources.json"), &sources); err != nil {
		return err
	}

	fetch := apidoc.HTTPFetcher(&http.Client{Timeout: 30 * time.Second})

	replayed, err := apidoc.CaptureDocuments(ctx, sources, func(ctx context.Context, address string) ([]byte, error) {
		return fetch(ctx, address+"?revision="+documents.Revision)
	})
	if err != nil {
		return fmt.Errorf("cannot verify candidate against pinned official sources: %w", err)
	}

	replayed.Revision = documents.Revision
	if err := apidoc.VerifyCapturedDocuments(documents, replayed); err != nil {
		return err
	}

	for _, args := range [][]string{{"generate", "./..."}, {"test", "./..."}, {"vet", "./..."}, {"-C", "examples/integration", "test", "./..."}} {
		command := exec.CommandContext(ctx, "go", args...) // #nosec G204 -- Executable and arguments come solely from the fixed internal verification list above.
		command.Dir = stage

		command.Env = append(os.Environ(), "GOWORK=off")
		if output, err := command.CombinedOutput(); err != nil {
			return fmt.Errorf("candidate rejected by go %s: %w\n%s", strings.Join(args, " "), err, output)
		}
	}

	return nil
}

func acceptedSources(catalog apidoc.Catalog, sources apidoc.Sources, documents apidoc.Documents, index string) (apidoc.Sources, error) {
	known := map[string]apidoc.Page{}
	for _, page := range sources.Pages {
		known[page.Path] = page
	}

	result := apidoc.Sources{IndexURL: "https://yandex.ru/support/tracker/ru/llms.txt?revision=" + documents.Revision}
	indexed := apidoc.IndexURLs(index)

	for _, document := range documents.Pages {
		page := known[document.Path]
		page.Path = document.Path

		address := "https://yandex.ru/support/tracker/ru/" + document.Path
		if !indexed[address] {
			return result, fmt.Errorf("candidate page %s is absent from its index", document.Path)
		}

		page.URL = address + "?revision=" + documents.Revision
		page.SHA256 = document.SHA256
		page.ContentSHA256 = document.Digest

		var names, signatures []string

		for _, operation := range catalog.Operations {
			if operation.Source != document.Path {
				continue
			}

			names = append(names, operation.Name)
			for _, signature := range document.Signatures {
				if apidoc.RouteShape(signature) == operation.Method+" "+apidoc.RouteShape(operation.Path) && !slices.Contains(signatures, signature) {
					signatures = append(signatures, signature)
				}
			}
		}

		if len(names) > 0 {
			page.Operations, page.Signatures = names, signatures
		}

		if err := apidoc.CheckDocumentRoutes(document, page); err != nil {
			return result, err
		}

		result.Pages = append(result.Pages, page)
	}

	return result, nil
}

func writeRevisionFile(source, target, revision string) error {
	var value map[string]json.RawMessage
	if err := apidoc.ReadJSON(source, &value); err != nil {
		return err
	}

	data, err := json.Marshal(revision)
	if err != nil {
		return err
	}

	if bytes.Equal(value["revision"], data) {
		original, err := os.ReadFile(source) // #nosec G304 -- Source is selected from the fixed local registry list in acceptReviewWithVerifier.
		if err != nil {
			return err
		}

		return apidoc.WriteArtifact(target, original)
	}

	value["revision"] = data

	return apidoc.WriteJSON(target, value)
}

func copyCheckSources(destination string) error {
	source, err := os.OpenRoot(".")
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()

	target, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}

	defer func() { _ = target.Close() }()

	return visitCheckSources(source, func(name string) error { return copyCheckFile(source, target, name) })
}

func visitCheckSources(source *os.Root, visit func(string) error) error {
	if err := visitRootCheckFiles(source, visit); err != nil {
		return err
	}

	for _, directory := range []string{"api", "typed", "internal", "cmd", "examples"} {
		if err := visitCheckTree(source, directory, visit); err != nil {
			return err
		}
	}

	return nil
}

func visitRootCheckFiles(source *os.Root, visit func(string) error) error {
	entries, err := fs.ReadDir(source.FS(), ".")
	if err != nil {
		return err
	}

	for _, entry := range entries {
		selected := strings.HasSuffix(entry.Name(), ".go") || entry.Name() == "go.mod" || entry.Name() == "go.sum"
		if selected && entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("check source must not be a symlink: %s", entry.Name())
		}

		if entry.Type().IsRegular() && selected {
			if err := visit(entry.Name()); err != nil {
				return err
			}
		}
	}

	return nil
}

func visitCheckTree(source *os.Root, directory string, visit func(string) error) error {
	return fs.WalkDir(source.FS(), directory, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if entry.IsDir() {
			return nil
		}

		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("check source must not be a symlink: %s", name)
		}

		if strings.HasSuffix(name, ".go") || strings.HasSuffix(name, ".json") || filepath.Base(name) == "go.mod" || filepath.Base(name) == "go.sum" || filepath.Base(name) == "index.txt" {
			return visit(name)
		}

		return nil
	})
}

func hashVerificationInputs(directory string) (map[string][32]byte, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()

	hashes := make(map[string][32]byte)
	err = visitCheckSources(root, func(name string) error {
		data, err := root.ReadFile(name)
		if err == nil {
			hashes[name] = sha256.Sum256(data)
		}

		return err
	})

	return hashes, err
}

func checkUnchangedSources(expected map[string][32]byte) error {
	current, err := hashVerificationInputs(".")
	if err != nil {
		return err
	}

	for name, hash := range expected {
		if current[name] != hash {
			return fmt.Errorf("check source changed during candidate verification: %s; no files accepted", name)
		}
	}

	for name := range current {
		if _, existed := expected[name]; !existed {
			return fmt.Errorf("check source added during candidate verification: %s; no files accepted", name)
		}
	}

	return nil
}

func copyCheckFile(source, target *os.Root, name string) error {
	data, err := source.ReadFile(name)
	if err != nil {
		return err
	}

	if err := target.MkdirAll(filepath.Dir(name), 0o750); err != nil {
		return err
	}

	return target.WriteFile(name, data, 0o600)
}

func readAcceptanceFiles(names []string) (map[string][]byte, error) {
	source, err := os.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = source.Close() }()

	previous := map[string][]byte{}

	for _, name := range names {
		info, err := source.Lstat(name)
		if err != nil {
			return nil, err
		}

		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("refusing to replace non-regular source %s", name)
		}

		data, err := source.ReadFile(name)
		if err != nil {
			return nil, err
		}

		previous[name] = data
	}

	return previous, nil
}

func installAcceptedFiles(stage string, names []string, expected map[string][]byte, write func(string, []byte) error) error {
	current, err := readAcceptanceFiles(names)
	if err != nil {
		return err
	}

	for _, name := range names {
		if !bytes.Equal(current[name], expected[name]) {
			return fmt.Errorf("source changed during candidate verification: %s; no files accepted", name)
		}
	}

	staged, err := os.OpenRoot(stage)
	if err != nil {
		return err
	}

	defer func() { _ = staged.Close() }()

	var installed []string

	installedHashes := make(map[string][32]byte)

	for _, name := range names {
		data, err := staged.ReadFile(name)
		if err == nil {
			current, err = readAcceptanceFiles([]string{name})
			if err == nil && !bytes.Equal(current[name], expected[name]) {
				err = fmt.Errorf("source changed during candidate installation: %s", name)
			}
		}

		if err == nil {
			err = write(name, data)
		}

		if err != nil {
			return errors.Join(err, restoreAcceptedFiles(installed, installedHashes, expected, write))
		}

		installed = append(installed, name)
		installedHashes[name] = sha256.Sum256(data)
	}

	return nil
}

func restoreAcceptedFiles(names []string, installed map[string][32]byte, expected map[string][]byte, write func(string, []byte) error) error {
	var failures []error

	for _, name := range names {
		current, err := readAcceptanceFiles([]string{name})
		if err != nil {
			failures = append(failures, fmt.Errorf("cannot safely roll back %s; source retained: %w", name, err))
			continue
		}

		if sha256.Sum256(current[name]) != installed[name] {
			failures = append(failures, fmt.Errorf("source changed after installation: %s; concurrent edit retained during rollback", name))
			continue
		}

		if err := write(name, expected[name]); err != nil {
			failures = append(failures, err)
		}
	}

	return errors.Join(failures...)
}
