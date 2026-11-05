package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skarm/ytr/internal/apidoc"
)

func TestAcceptReviewDoesNotTouchSourcesWhenVerificationFails(t *testing.T) {
	directory := acceptanceFixture(t)
	previous := map[string][]byte{}
	for _, name := range []string{"api/catalog.json", "api/models.json", "api/sources.json", "api/documentation.json", "methods_gen.go", "typed/models_gen.go", "typed/client_gen.go"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		previous[name] = data
	}
	sentinel := errors.New("candidate compile failed")
	calls := 0
	err := acceptReviewWithVerifier(t.Context(), directory, func(_ context.Context, stage string) error {
		calls++
		if _, err := os.Stat(filepath.Join(stage, "api/documentation.json")); err != nil {
			t.Fatal(err)
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) || calls != 1 {
		t.Fatalf("candidate gate: %v, calls=%d", err, calls)
	}
	for name, want := range previous {
		got, err := os.ReadFile(name)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s changed on rejection: %v", name, err)
		}
	}
}

func TestAcceptReviewRejectsStaleBaselineBeforeVerification(t *testing.T) {
	directory := acceptanceFixture(t)
	var review apidoc.Review
	if err := apidoc.ReadJSON(filepath.Join(directory, "report.json"), &review); err != nil {
		t.Fatal(err)
	}
	review.BaseDigest = "stale"
	if err := apidoc.WriteJSON(filepath.Join(directory, "report.json"), review); err != nil {
		t.Fatal(err)
	}
	err := acceptReviewWithVerifier(t.Context(), directory, func(context.Context, string) error { t.Fatal("stale candidate reached verifier"); return nil })
	if err == nil {
		t.Fatal("stale review accepted")
	}
}

func TestAcceptReviewPreservesConcurrentSourceEdits(t *testing.T) {
	directory := acceptanceFixture(t)
	changed := []byte("developer changed this file while verification was running")
	err := acceptReviewWithVerifier(t.Context(), directory, func(context.Context, string) error { return os.WriteFile("api/models.json", changed, 0o600) })
	if err == nil {
		t.Fatal("concurrent edit was overwritten")
	}
	got, readErr := os.ReadFile("api/models.json")
	if readErr != nil || !bytes.Equal(got, changed) {
		t.Fatal(string(got), readErr)
	}
}

func TestAcceptReviewRejectsMixedCaptureFiles(t *testing.T) {
	directory := acceptanceFixture(t)
	if err := os.WriteFile(filepath.Join(directory, "index.txt"), []byte("different capture"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := acceptReviewWithVerifier(t.Context(), directory, func(context.Context, string) error { t.Fatal("mixed artifacts reached verifier"); return nil })
	if err == nil {
		t.Fatal("mixed report/candidate/index accepted")
	}
}

func TestCheckdocsRejectsIncompatibleWorkflowFlags(t *testing.T) {
	for _, args := range []struct {
		online                   bool
		report, snapshot, accept string
	}{{false, "report", "", ""}, {true, "", "snapshot", ""}, {true, "report", "", "accept"}, {false, "", "snapshot", "accept"}} {
		if err := execute(args.online, args.report, args.snapshot, args.accept); err == nil {
			t.Fatal("incompatible flags accepted", args)
		}
	}
}

func acceptanceFixture(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	t.Chdir(root)
	if err := copyCheckSources(destination); err != nil {
		t.Fatal(err)
	}
	t.Chdir(destination)
	var documents apidoc.Documents
	if err := apidoc.ReadJSON("api/documentation.json", &documents); err != nil {
		t.Fatal(err)
	}
	catalog, err := apidoc.Load("api/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	models, err := apidoc.LoadModels("api/models.json", catalog)
	if err != nil {
		t.Fatal(err)
	}
	index, err := os.ReadFile("api/index.txt")
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(destination, "review")
	if err := apidoc.WriteReview(directory, apidoc.CompareDocuments(documents, documents, models), documents, index); err != nil {
		t.Fatal(err)
	}
	return directory
}

func TestAcceptRejectsNewUnclassifiedDocumentedRoute(t *testing.T) {
	directory, _ := changedSourceReview(t, "api/issues/get-issue.md", func(document *apidoc.Document) {
		document.Signatures = append(document.Signatures, "POST /v3/issues/_new_operation")
	})
	err := acceptReviewWithVerifier(t.Context(), directory, func(context.Context, string) error {
		t.Fatal("unclassified route reached the verifier")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "unclassified documented route POST /v3/issues/_new_operation") {
		t.Fatalf("unknown source route accepted or rejected for the wrong reason: %v", err)
	}
}

func TestAcceptRejectsUnresolvedDocumentedTypeChange(t *testing.T) {
	directory, review := changedSourceReview(t, "api/issues/create-issue.md", func(document *apidoc.Document) {
		for i := range document.Fields {
			field := &document.Fields[i]
			if field.Name == "unique" && strings.Contains(field.Section, "#query") {
				field.Type, field.Digest = "Логический", strings.Repeat("c", 64)
				return
			}
		}
		t.Fatal("missing independent source field")
	})
	if len(review.Disagreements) != 1 || review.Disagreements[0].Target.Field != "Unique" {
		t.Fatalf("lost precise type disagreement: %+v", review.Disagreements)
	}
	err := acceptReviewWithVerifier(t.Context(), directory, func(context.Context, string) error {
		t.Fatal("unresolved scalar type reached the verifier")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "unresolved type disagreement") || !strings.Contains(err.Error(), "CreateIssueRequestBody.Unique") {
		t.Fatalf("stale type accepted or rejected for the wrong reason: %v", err)
	}
}

func TestAcceptUnchangedClassifiedCandidateReachesVerifier(t *testing.T) {
	directory := acceptanceFixture(t)
	verified := false
	err := acceptReviewWithVerifier(t.Context(), directory, func(context.Context, string) error {
		verified = true
		return nil
	})
	if err != nil || !verified {
		t.Fatalf("valid classified candidate rejected: %v", err)
	}
}

func TestAcceptedSourcesKeepsNewlyImplementedRoute(t *testing.T) {
	document := apidoc.Document{Path: "api/items.md", Digest: strings.Repeat("a", 64), SHA256: strings.Repeat("b", 64), Signatures: []string{"GET /v3/items", "POST /v3/items"}}
	catalog := apidoc.Catalog{Revision: "r2", VerifiedOn: "2026-10-31", Operations: []apidoc.Operation{
		{Name: "GetItems", Method: "GET", Path: "/v3/items", Request: "none", Response: "json", Source: document.Path},
		{Name: "CreateItem", Method: "POST", Path: "/v3/items", Request: "json", Response: "json", Source: document.Path},
	}}
	documents := apidoc.Documents{Version: 1, Revision: "r2", Pages: []apidoc.Document{document}}
	index := "- [Items](https://yandex.ru/support/tracker/ru/api/items.md)\n"
	sources, err := acceptedSources(catalog, apidoc.Sources{}, documents, index)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources.Pages) != 1 || len(sources.Pages[0].Signatures) != 2 {
		t.Fatalf("new implemented route lost: %+v", sources)
	}
	if err := apidoc.Check(catalog, sources, index); err != nil {
		t.Fatal(err)
	}
	if err := apidoc.CheckDocuments(documents, catalog, sources); err != nil {
		t.Fatal(err)
	}
}

func changedSourceReview(t *testing.T, path string, edit func(*apidoc.Document)) (string, apidoc.Review) {
	t.Helper()
	directory := acceptanceFixture(t)
	var before, after apidoc.Documents
	for _, target := range []*apidoc.Documents{&before, &after} {
		if err := apidoc.ReadJSON("api/documentation.json", target); err != nil {
			t.Fatal(err)
		}
	}
	for i := range after.Pages {
		if after.Pages[i].Path == path {
			edit(&after.Pages[i])
			after.Pages[i].Digest = strings.Repeat("b", 64)
		}
	}
	var sources apidoc.Sources
	if err := apidoc.ReadJSON("api/sources.json", &sources); err != nil {
		t.Fatal(err)
	}
	// Existing examples are explicitly re-reviewed for the simulated new page.
	// The new route is deliberately left unclassified.
	for i := range sources.Pages {
		if sources.Pages[i].Path == path {
			for j := range sources.Pages[i].ExcludedSignatures {
				sources.Pages[i].ExcludedSignatures[j].Digest = strings.Repeat("b", 64)
			}
		}
	}
	if err := apidoc.WriteJSON("api/sources.json", sources); err != nil {
		t.Fatal(err)
	}
	catalog, err := apidoc.Load("api/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	models, err := apidoc.LoadModels("api/models.json", catalog)
	if err != nil {
		t.Fatal(err)
	}
	index, err := os.ReadFile("api/index.txt")
	if err != nil {
		t.Fatal(err)
	}
	review := apidoc.CompareDocuments(before, after, models)
	if err := apidoc.WriteReview(directory, review, after, index); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, "candidate.json")); err != nil {
		t.Fatal(err)
	}
	return directory, review
}

func TestInstallRollbackKeepsConcurrentUserEdit(t *testing.T) {
	t.Chdir(t.TempDir())
	stage := t.TempDir()
	expected := map[string][]byte{"first.go": []byte("original first"), "second.go": []byte("original second")}
	for name, data := range expected {
		if err := os.WriteFile(name, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stage, name), []byte("candidate "+name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	userEdit := []byte("concurrent user edit")
	failure := errors.New("injected second-file write failure")
	writes := 0
	err := installAcceptedFiles(stage, []string{"first.go", "second.go"}, expected, func(name string, data []byte) error {
		writes++
		if writes == 2 {
			if err := os.WriteFile("first.go", userEdit, 0o600); err != nil {
				t.Fatal(err)
			}
			return failure
		}
		return apidoc.WriteArtifact(name, data)
	})
	if !errors.Is(err, failure) {
		t.Fatalf("write failure lost: %v", err)
	}
	current, err := os.ReadFile("first.go")
	if err != nil || !bytes.Equal(current, userEdit) {
		t.Fatalf("rollback overwrote concurrent user edit: %s (%v)", current, err)
	}
}

func TestInstallRollbackRestoresUnchangedFiles(t *testing.T) {
	t.Chdir(t.TempDir())
	stage := t.TempDir()
	expected := map[string][]byte{"first.go": []byte("original first"), "second.go": []byte("original second")}
	for name, data := range expected {
		if err := os.WriteFile(name, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stage, name), []byte("candidate "+name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	failure := errors.New("injected second-file write failure")
	writes := 0
	err := installAcceptedFiles(stage, []string{"first.go", "second.go"}, expected, func(name string, data []byte) error {
		writes++
		if writes == 2 {
			return failure
		}
		return apidoc.WriteArtifact(name, data)
	})
	if !errors.Is(err, failure) {
		t.Fatalf("write failure lost: %v", err)
	}
	for name, original := range expected {
		current, err := os.ReadFile(name)
		if err != nil || !bytes.Equal(current, original) {
			t.Fatalf("%s not restored: %s (%v)", name, current, err)
		}
	}
}

func TestInstallDetectsChangesBetweenFiles(t *testing.T) {
	for _, change := range []string{"edit", "remove"} {
		t.Run(change, func(t *testing.T) {
			t.Chdir(t.TempDir())
			stage := t.TempDir()
			expected := map[string][]byte{"first.go": []byte("original first"), "second.go": []byte("original second")}
			for name, data := range expected {
				if err := os.WriteFile(name, data, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(stage, name), []byte("candidate "+name), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			userEdit := []byte("concurrent second-file edit")
			err := installAcceptedFiles(stage, []string{"first.go", "second.go"}, expected, func(name string, data []byte) error {
				if name == "first.go" && bytes.Equal(data, []byte("candidate first.go")) {
					var err error
					if change == "edit" {
						err = os.WriteFile("second.go", userEdit, 0o600)
					} else {
						err = os.Remove("second.go")
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				return apidoc.WriteArtifact(name, data)
			})
			if err == nil {
				t.Error("accepted a file changed after the initial source check")
			}
			first, readErr := os.ReadFile("first.go")
			if readErr != nil || !bytes.Equal(first, expected["first.go"]) {
				t.Errorf("first file was not rolled back: %s (%v)", first, readErr)
			}
			second, readErr := os.ReadFile("second.go")
			if change == "edit" {
				if readErr != nil || !bytes.Equal(second, userEdit) {
					t.Errorf("concurrent edit was overwritten: %s (%v)", second, readErr)
				}
			} else if !errors.Is(readErr, os.ErrNotExist) || !errors.Is(err, os.ErrNotExist) {
				t.Errorf("concurrent removal was lost: data=%s read=%v install=%v", second, readErr, err)
			}
		})
	}
}

func TestAcceptRejectsChangesToOtherVerificationInputs(t *testing.T) {
	for _, test := range []struct {
		name  string
		value []byte
	}{
		{"typed/testdata/responses.json", []byte(`{}`)},
		{"typed/core.go", []byte("package typed\n// concurrent user change\n")},
		{"new_input.go", []byte("package ytr\nconst Unverified = true\n")},
		{"examples/integration/go.sum", []byte("checksum metadata edited\n")},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := acceptanceFixture(t)
			previous, readErr := os.ReadFile(test.name)
			if readErr != nil && !os.IsNotExist(readErr) {
				t.Fatal(readErr)
			}
			called := false
			err := acceptReviewWithVerifier(t.Context(), directory, func(_ context.Context, stage string) error {
				called = true
				staged, stageErr := os.ReadFile(filepath.Join(stage, test.name))
				if readErr == nil && (stageErr != nil || !bytes.Equal(staged, previous)) {
					t.Fatalf("verification did not use original inputs: %v", stageErr)
				}
				if readErr != nil && !os.IsNotExist(stageErr) {
					t.Fatalf("unexpected staged file: %v", stageErr)
				}
				return os.WriteFile(test.name, test.value, 0o600)
			})
			if !called || err == nil || !strings.Contains(err.Error(), "source") {
				t.Fatalf("stale verification accepted: %v", err)
			}
			current, err := os.ReadFile(test.name)
			if err != nil || !bytes.Equal(current, test.value) {
				t.Fatalf("concurrent user edit not preserved: %v", err)
			}
		})
	}
}

func TestAcceptCanceledBeforeCommitKeepsSources(t *testing.T) {
	directory := acceptanceFixture(t)
	previous, err := os.ReadFile("api/sources.json")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err = acceptReviewWithVerifier(ctx, directory, func(context.Context, string) error { cancel(); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled candidate committed: %v", err)
	}
	current, err := os.ReadFile("api/sources.json")
	if err != nil || !bytes.Equal(current, previous) {
		t.Fatalf("sources changed after cancellation: %v", err)
	}
}

func TestVerifyCandidateChecksPinnedFactsBeforeRunningGo(t *testing.T) {
	stage := verificationFixture(t, "GET https://api.tracker.yandex.net/v3/items\n")
	stubPinnedDocumentation(t, "GET https://api.tracker.yandex.net/v3/other\n")
	if err := verifyCandidate(t.Context(), stage); err == nil || !strings.Contains(err.Error(), "differ from the pinned official source") {
		t.Fatalf("changed official facts accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stage, "generated.txt")); !os.IsNotExist(err) {
		t.Fatalf("generation ran before source validation: %v", err)
	}
}

func TestVerifyCandidateRunsIsolatedGoChecks(t *testing.T) {
	body := "GET https://api.tracker.yandex.net/v3/items\n"
	stage := verificationFixture(t, body)
	stubPinnedDocumentation(t, body)
	if err := verifyCandidate(t.Context(), stage); err != nil {
		t.Fatal(err)
	}
	generated, err := os.ReadFile(filepath.Join(stage, "generated.txt"))
	if err != nil || string(generated) != "generated" {
		t.Fatalf("generation was skipped: %s (%v)", generated, err)
	}
	if _, err := os.Stat(filepath.Join(stage, "examples/integration/checked.txt")); err != nil {
		t.Fatalf("nested-module tests were skipped: %v", err)
	}
	// Real failing Go tests must prevent installation and preserve their diagnostics.
	if err := os.WriteFile(filepath.Join(stage, "candidate_test.go"), []byte("package candidate\nimport \"testing\"\nfunc TestFailure(t *testing.T) { t.Fatal(\"candidate regression\") }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyCandidate(t.Context(), stage); err == nil || !strings.Contains(err.Error(), "candidate rejected by go test ./...") || !strings.Contains(err.Error(), "candidate regression") {
		t.Fatalf("failing candidate checks accepted or diagnostics lost: %v", err)
	}
}

func verificationFixture(t *testing.T, body string) string {
	t.Helper()
	stage := t.TempDir()
	for _, directory := range []string{"api", "generator", "examples/integration"} {
		if err := os.MkdirAll(filepath.Join(stage, directory), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	document, err := apidoc.ExtractDocument("api/items.md", []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	document.SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
	if err := apidoc.WriteJSON(filepath.Join(stage, "api/documentation.json"), apidoc.Documents{Version: 1, Revision: "r1", Pages: []apidoc.Document{document}}); err != nil {
		t.Fatal(err)
	}
	if err := apidoc.WriteJSON(filepath.Join(stage, "api/sources.json"), apidoc.Sources{Pages: []apidoc.Page{{Path: "api/items.md", URL: "https://yandex.ru/support/tracker/ru/api/items.md?revision=r1"}}}); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"go.mod":                               "module example.com/candidate\n\ngo 1.27.0\n",
		"candidate.go":                         "package candidate\n//go:generate go run ./generator\n",
		"generator/main.go":                    "package main\nimport \"os\"\nfunc main() { if err := os.WriteFile(\"generated.txt\", []byte(\"generated\"), 0600); err != nil { panic(err) } }\n",
		"candidate_test.go":                    "package candidate\nimport (\"os\"; \"testing\")\nfunc TestGenerated(t *testing.T) { if b, err := os.ReadFile(\"generated.txt\"); err != nil || string(b) != \"generated\" { t.Fatal(\"generation missing\", err) } }\n",
		"examples/integration/go.mod":          "module example.com/integration\n\ngo 1.27.0\n",
		"examples/integration/fixture_test.go": "package integration\nimport (\"os\"; \"testing\")\nfunc TestGenerated(t *testing.T) { if b, err := os.ReadFile(\"../../generated.txt\"); err != nil || string(b) != \"generated\" { t.Fatal(\"generation missing\", err) }; if err := os.WriteFile(\"checked.txt\", []byte(\"checked\"), 0600); err != nil { t.Fatal(err) } }\n",
	} {
		if err := os.WriteFile(filepath.Join(stage, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return stage
}

func stubPinnedDocumentation(t *testing.T, body string) {
	t.Helper()
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = localTransport(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://yandex.ru/support/tracker/ru/api/items.md?revision=r1" {
			t.Errorf("source replay did not pin its revision: %s", request.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
}
