package apidoc_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/skarm/ytr/internal/apidoc"
)

func TestExtractDocumentRetainsIndependentContractFacts(t *testing.T) {
	body := []byte("## Формат запроса {#query}\n{% cut \"Параметры тела запроса\" %}\n**Обязательные параметры**\n#|\n|| Параметр | Описание | Тип данных ||\n|| `flag` | First line\nsecond `a|b` line | Логический ||\n|#\n{% endcut %}\n## Формат ответа {#answer}\nкодом `201 Created`\n```json\n{\"flag\":false,\"id\":9007199254740993}\n```\nПараметр | Описание | Тип данных\n---|---|---\n[id](#id) | ID | Число\n")
	document, err := apidoc.ExtractDocument("api/example.md", body)
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Fields) != 2 || document.Fields[0].Name != "flag" || document.Fields[0].Type != "Логический" || document.Fields[0].Required != "required" || document.Fields[1].Name != "id" {
		t.Fatalf("fields: %+v", document.Fields)
	}
	if len(document.Examples) != 1 || document.Examples[0].Status != 201 || string(document.Examples[0].Value) != `{"flag":false,"id":9007199254740993}` {
		t.Fatalf("examples: %+v", document.Examples)
	}
	if !strings.Contains(document.Fields[0].Section, "Параметры тела запроса") {
		t.Fatal(document.Fields[0].Section)
	}
}

func TestExtractDocumentDoesNotInventMalformedOrInvalidExamples(t *testing.T) {
	for _, source := range []string{"#|\n|| Параметр | Тип ||\n|| value | never closed", "#|\n||"} {
		if _, err := apidoc.ExtractDocument("api/bad.md", []byte(source)); err == nil {
			t.Fatal("unterminated table accepted")
		}
	}
	document, err := apidoc.ExtractDocument("api/notes.md", []byte("Параметр | Описание | Тип данных\n---|---|---\nvalue | description | odd | Строка\n\n```json\n{broken}\n```\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Fields) != 1 || document.Fields[0].Note == "" || document.Fields[0].Type != "Строка" || len(document.Examples) != 0 {
		t.Fatalf("ambiguous source hidden: %+v", document)
	}
}

func TestReviewNamesChangedFieldAndItsRegistryPointer(t *testing.T) {
	parse := func(body string) apidoc.Document {
		d, err := apidoc.ExtractDocument("api/create.md", []byte(body))
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	prefix := "## Формат запроса {#query}\n{% cut \"Параметры тела запроса\" %}\nПараметр | Описание | Тип данных\n---|---|---\n"
	before := apidoc.Documents{Version: 1, Revision: "r1", Pages: []apidoc.Document{parse(prefix + "value | Data | Строка\n")}}
	after := apidoc.Documents{Version: 1, Revision: "r2", Pages: []apidoc.Document{parse(prefix + "value | Data | Число\nadded | New | Логический\n")}}
	models := apidoc.Models{Types: []apidoc.ModelType{{Name: "Body", Kind: "struct", Source: "api/create.md", Fields: []apidoc.ModelField{{Name: "Value", JSON: "value", Type: "string"}}}}, Operations: []apidoc.ModelOperation{{Name: "CreateItem", Body: "Body", Source: "api/create.md"}, {Name: "CopyItem", Body: "Body", Source: "api/create.md"}}}
	review := apidoc.CompareDocuments(before, after, models)
	if len(review.Changes) != 2 || review.BaseDigest != apidoc.DocumentsDigest(before) {
		t.Fatalf("diff: %+v", review)
	}
	var changed *apidoc.DocumentationChange
	for i := range review.Changes {
		if review.Changes[i].Kind == "field_changed" {
			changed = &review.Changes[i]
		}
	}
	if changed == nil || len(changed.Targets) != 1 || changed.Targets[0].Pointer != "/types/0/fields/0/type" || len(changed.Targets[0].Operations) != 2 || changed.Before.Type != "Строка" || changed.After.Type != "Число" {
		t.Fatalf("wrong change ownership: %+v", changed)
	}
	directory := t.TempDir()
	if err := apidoc.WriteReview(directory, review, after, []byte("index")); err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile(filepath.Join(directory, "report.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"api/models.json#/types/0/fields/0/type", "Body.Value", "Строка", "Число", "CreateItem", "CopyItem", "json.Number", "suggested edit"} {
		if !strings.Contains(string(text), expected) {
			t.Fatalf("report omits %s: %s", expected, text)
		}
	}
}

func TestReviewReportsPageChangesAndUnmappedFields(t *testing.T) {
	before := apidoc.Documents{Version: 1, Revision: "r1", Pages: []apidoc.Document{{Path: "api/removed.md"}, {Path: "api/current.md", Digest: "before"}}}
	after := apidoc.Documents{Version: 1, Revision: "r2", Pages: []apidoc.Document{{Path: "api/added.md"}, {Path: "api/current.md", Digest: "after", Fields: []apidoc.DocumentField{{Name: "unknown", Type: "Строка", Digest: "new"}}}}}
	review := apidoc.CompareDocuments(before, after, apidoc.Models{})
	if len(review.Changes) != 3 {
		t.Fatalf("lost source changes: %+v", review)
	}
	for _, change := range review.Changes {
		if len(change.Actions) < 2 {
			t.Fatalf("unmapped change has no update instructions: %+v", change)
		}
	}
}

func TestReviewKeepsFieldsWithDelimiterCharactersDistinct(t *testing.T) {
	first := "## A\n**B|C / D**\nПараметр | Тип данных\n---|---\n`E` | Строка\n"
	second := "## A\n**B**\nПараметр | Тип данных\n---|---\n`C / D|E` | Строка\n"
	before, err := apidoc.ExtractDocument("api/example.md", []byte(first+second))
	if err != nil {
		t.Fatal(err)
	}
	after, err := apidoc.ExtractDocument(before.Path, []byte(second))
	if err != nil {
		t.Fatal(err)
	}
	review := apidoc.CompareDocuments(apidoc.Documents{Pages: []apidoc.Document{before}}, apidoc.Documents{Pages: []apidoc.Document{after}}, apidoc.Models{})
	if len(review.Changes) != 1 || review.Changes[0].Kind != "field_removed" || review.Changes[0].Before.Name != "E" {
		t.Fatalf("distinct section/name pairs collided: %+v", review.Changes)
	}
}

func TestCaptureDocumentsPropagatesFailuresAndCancellation(t *testing.T) {
	sentinel := errors.New("source unavailable")
	sources := apidoc.Sources{Pages: []apidoc.Page{{Path: "api/a.md", URL: "https://fixture.test/a.md"}, {Path: "api/b.md", URL: "https://fixture.test/b.md"}}}
	if _, err := apidoc.CaptureDocuments(t.Context(), sources, func(context.Context, string) ([]byte, error) { return nil, sentinel }); err == nil || !strings.Contains(err.Error(), sentinel.Error()) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := apidoc.CaptureDocuments(ctx, sources, func(ctx context.Context, _ string) ([]byte, error) { return nil, ctx.Err() }); err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatal(err)
	}
}

func TestCaptureDocumentsPreservesEveryFetchFailureAndPartialResult(t *testing.T) {
	first, second := errors.New("first failure"), errors.New("second failure")
	sources := apidoc.Sources{Pages: []apidoc.Page{
		{Path: "api/z.md", URL: "https://fixture.test/z"},
		{Path: "api/a.md", URL: "https://fixture.test/a"},
		{Path: "api/ok.md", URL: "https://fixture.test/ok"},
	}}
	documents, err := apidoc.CaptureDocuments(t.Context(), sources, func(_ context.Context, address string) ([]byte, error) {
		switch address {
		case "https://fixture.test/z":
			return nil, first
		case "https://fixture.test/a":
			return nil, second
		default:
			return []byte("## Response\n"), nil
		}
	})
	if !errors.Is(err, first) || !errors.Is(err, second) {
		t.Fatalf("fetch error identity lost: %v", err)
	}
	if err.Error() != "api/a.md: second failure\napi/z.md: first failure" {
		t.Fatalf("diagnostics are not deterministic: %v", err)
	}
	if len(documents.Pages) != 3 || documents.Pages[2].Path != "api/ok.md" || len(documents.Pages[2].SHA256) != 64 {
		t.Fatalf("successful partial capture lost: %+v", documents)
	}
}

func TestCaptureDocumentsCancellationRemainsDetectable(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := apidoc.CaptureDocuments(ctx, apidoc.Sources{Pages: []apidoc.Page{{Path: "api/a.md"}}}, func(ctx context.Context, _ string) ([]byte, error) {
		return nil, ctx.Err()
	})
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "api/a.md") {
		t.Fatalf("cancellation cannot be identified: %v", err)
	}
}

func TestFailedJSONArtifactLeavesExistingContent(t *testing.T) {
	name := filepath.Join(t.TempDir(), "artifact.json")
	if err := os.WriteFile(name, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := apidoc.WriteJSON(name, func() {}); err == nil {
		t.Fatal("unencodable artifact accepted")
	}
	data, err := os.ReadFile(name)
	if err != nil || string(data) != "existing" {
		t.Fatal(string(data), err)
	}
}

func TestCapturedFactsCannotBeForgedByKeepingSourceHash(t *testing.T) {
	document, err := apidoc.ExtractDocument("api/test.md", []byte("Параметр | Описание | Тип данных\n---|---|---\nvalue | Data | Строка\n"))
	if err != nil {
		t.Fatal(err)
	}
	actual := apidoc.Documents{Version: 1, Revision: "r1", Pages: []apidoc.Document{document}}
	forged := actual
	forged.Pages = append([]apidoc.Document(nil), actual.Pages...)
	forged.Pages[0].Fields = append([]apidoc.DocumentField(nil), document.Fields...)
	forged.Pages[0].Fields[0].Type = "Число"
	if err := apidoc.VerifyCapturedDocuments(forged, actual); err == nil {
		t.Fatal("forged source facts accepted")
	}
	if err := apidoc.VerifyCapturedDocuments(actual, actual); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotBindsDeclaredRoutesToIndependentSourceFacts(t *testing.T) {
	page := apidoc.Page{Path: "api/item.md", ContentSHA256: "canonical", Signatures: []string{"GET /v3/items/{itemID}"}}
	sources := apidoc.Sources{Pages: []apidoc.Page{page}}
	document := apidoc.Document{Path: page.Path, Digest: page.ContentSHA256, SHA256: strings.Repeat("a", 64), Signatures: []string{"GET /v3/items/{id}"}}
	snapshot := apidoc.Documents{Version: 1, Revision: "r1", Pages: []apidoc.Document{document}}
	catalog := apidoc.Catalog{Revision: "r1"}
	if err := apidoc.CheckDocuments(snapshot, catalog, sources); err != nil {
		t.Fatal(err)
	}
	snapshot.Pages[0].Signatures = []string{"POST /v3/items/{id}"}
	if err := apidoc.CheckDocuments(snapshot, catalog, sources); err == nil || !strings.Contains(err.Error(), "absent from official source") {
		t.Fatalf("a route invented in both registries was accepted: %v", err)
	}
}

func TestSnapshotJSONFormattingDoesNotCreateContractChanges(t *testing.T) {
	document, err := apidoc.ExtractDocument("api/example.md", []byte("## Формат ответа {#answer}\n```json\n{\"id\":9007199254740993,\"text\":\"<>&\"}\n```"))
	if err != nil {
		t.Fatal(err)
	}
	captured := apidoc.Documents{Version: 1, Revision: "r1", Pages: []apidoc.Document{document}}
	name := filepath.Join(t.TempDir(), "documents.json")
	if err := apidoc.WriteJSON(name, captured); err != nil {
		t.Fatal(err)
	}
	var stored apidoc.Documents
	if err := apidoc.ReadJSON(name, &stored); err != nil {
		t.Fatal(err)
	}
	if review := apidoc.CompareDocuments(stored, captured, apidoc.Models{}); len(review.Changes) != 0 {
		t.Fatalf("JSON formatting looked like contract drift: %+v", review.Changes)
	}
	if err := apidoc.VerifyCapturedDocuments(stored, captured); err != nil {
		t.Fatal(err)
	}
}

func TestMalformedOuterJSONIsNotReplacedByItsValidTail(t *testing.T) {
	document, err := apidoc.ExtractDocument("api/bad.md", []byte("```json\n{broken\n{}\n```"))
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Examples) != 0 {
		t.Fatal("nested tail mistaken for complete example", document.Examples)
	}
}

func FuzzExtractDocumentDoesNotPanic(f *testing.F) {
	for _, source := range []string{"", "#|\n||", "Параметр | Тип данных\n---|---\na | Строка", "```json\n{}\n```", "|| `a|b` | value ||"} {
		f.Add(source)
	}
	f.Fuzz(func(t *testing.T, source string) {
		if len(source) > 65536 {
			t.Skip()
		}
		_, _ = apidoc.ExtractDocument("api/fuzz.md", []byte(source))
	})
}

func TestDocumentsDigestKeepsCanonicalWireBytes(t *testing.T) {
	for _, test := range []struct {
		name      string
		value     apidoc.Documents
		canonical string
	}{
		{"nil pages", apidoc.Documents{}, `{"version":0,"revision":"","pages":null}`},
		{"empty pages", apidoc.Documents{Version: 1, Revision: "r1", Pages: []apidoc.Document{}}, `{"version":1,"revision":"r1","pages":[]}`},
		{"exact raw JSON and escaping", apidoc.Documents{Version: 1, Revision: "r1", Pages: []apidoc.Document{{Path: "api/x.md", Examples: []apidoc.DocumentExample{{Section: "请求", Line: 1, Value: json.RawMessage(` { "value": "<>&", "number": 9007199254740993 } `)}}}}}, `{"version":1,"revision":"r1","pages":[{"path":"api/x.md","digest":"","sha256":"","examples":[{"section":"请求","line":1,"value":{"value":"\u003c\u003e\u0026","number":9007199254740993}}]}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			want := fmt.Sprintf("%x", sha256.Sum256([]byte(test.canonical)))
			if got := apidoc.DocumentsDigest(test.value); got != want {
				t.Fatalf("source digest contract changed: %s; want %s", got, want)
			}
		})
	}
	invalid := apidoc.Documents{Pages: []apidoc.Document{{Examples: []apidoc.DocumentExample{{Value: json.RawMessage(`{`)}}}}}
	if got := apidoc.DocumentsDigest(invalid); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("encoding error changed digest: %s", got)
	}
}

func BenchmarkDocumentationContracts(b *testing.B) {
	catalog, err := apidoc.Load("../../api/catalog.json")
	if err != nil {
		b.Fatal(err)
	}
	models, err := apidoc.LoadModels("../../api/models.json", catalog)
	if err != nil {
		b.Fatal(err)
	}
	var docs apidoc.Documents
	if err := apidoc.ReadJSON("../../api/documentation.json", &docs); err != nil {
		b.Fatal(err)
	}
	b.Run("ModelTargets", func(b *testing.B) {
		b.ReportAllocs()
		var targets []apidoc.ModelTarget
		for b.Loop() {
			targets = apidoc.ModelTargets(models)
		}
		if len(targets) == 0 {
			b.Fatal("no model targets")
		}
	})
	b.Run("ModelEvidence", func(b *testing.B) {
		b.ReportAllocs()
		var problems []apidoc.EvidenceProblem
		for b.Loop() {
			problems = apidoc.CheckModelEvidence(docs, models)
		}
		if len(problems) == 0 {
			b.Fatal("lost documented/model disagreements")
		}
	})
	for _, changed := range []bool{false, true} {
		name := "UnchangedReview"
		candidate := docs
		if changed {
			name = "ChangedFieldReview"
			candidate.Pages = append([]apidoc.Document(nil), docs.Pages...)
			found := false
			for i, page := range candidate.Pages {
				if page.Path != "api/issues/create-issue.md" {
					continue
				}
				candidate.Pages[i].Fields = append([]apidoc.DocumentField(nil), page.Fields...)
				for j, field := range candidate.Pages[i].Fields {
					if field.Name == "unique" && strings.Contains(field.Section, "#query") {
						candidate.Pages[i].Fields[j].Type = "Логический"
						candidate.Pages[i].Fields[j].Digest = "benchmark-changed-row"
						found = true
						break
					}
				}
			}
			if !found {
				b.Fatal("independent source field unique not found")
			}
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			var review apidoc.Review
			for b.Loop() {
				review = apidoc.CompareDocuments(docs, candidate, models)
			}
			if changed {
				if len(review.Changes) != 1 || len(review.Disagreements) != 1 || review.Disagreements[0].Target.Field != "Unique" {
					b.Fatalf("changed-field report lost: changes=%d disagreements=%+v", len(review.Changes), review.Disagreements)
				}
			} else if len(review.Changes) != 0 || len(review.Disagreements) != 0 {
				b.Fatal("unchanged source produced a diff")
			}
		})
	}
	pageIndex := 0
	for i := range docs.Pages {
		if len(docs.Pages[i].Fields) > len(docs.Pages[pageIndex].Fields) {
			pageIndex = i
		}
	}
	candidate := docs
	candidate.Pages = append([]apidoc.Document(nil), docs.Pages...)
	candidate.Pages[pageIndex].Fields = append([]apidoc.DocumentField(nil), docs.Pages[pageIndex].Fields...)
	for i := range candidate.Pages[pageIndex].Fields {
		candidate.Pages[pageIndex].Fields[i].Digest += "-benchmark-changed"
	}
	b.Run("ChangedPageReview", func(b *testing.B) {
		b.ReportAllocs()
		var review apidoc.Review
		for b.Loop() {
			review = apidoc.CompareDocuments(docs, candidate, models)
		}
		if len(review.Changes) != len(candidate.Pages[pageIndex].Fields) {
			b.Fatalf("changed-page review lost rows: %d, want %d", len(review.Changes), len(candidate.Pages[pageIndex].Fields))
		}
	})
}

// File contents are in the OS cache; this isolates local snapshot loading and
// JSON ownership costs, not cold-disk latency or network documentation fetches.
func BenchmarkDocumentationLoad(b *testing.B) {
	var sources apidoc.Sources
	if err := apidoc.ReadJSON("../../api/sources.json", &sources); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	var documents apidoc.Documents
	for b.Loop() {
		documents = apidoc.Documents{}
		if err := apidoc.ReadJSON("../../api/documentation.json", &documents); err != nil {
			b.Fatal(err)
		}
	}
	if len(documents.Pages) != len(sources.Pages) {
		b.Fatalf("snapshot pages: %d", len(documents.Pages))
	}
}

func TestNewDocumentationPageCannotBypassFieldValidation(t *testing.T) {
	before, after, models := scalarChangeFixture()
	before.Pages = nil
	if err := apidoc.CheckModelChanges(before, after, models, apidoc.Sources{}); err == nil || !strings.Contains(err.Error(), "unresolved type disagreement") {
		t.Fatalf("new page accepted with wrong DTO type: %v", err)
	}
	models.Types[0].Fields[0].Type = "Optional[bool]"
	if err := apidoc.CheckModelChanges(before, after, models, apidoc.Sources{}); err != nil {
		t.Fatalf("fixed new page rejected: %v", err)
	}
}

func TestRemovedSourceFieldCannotLeaveAnUnreviewedRequestField(t *testing.T) {
	before, after, models := scalarChangeFixture()
	after.Pages[0].Fields = nil
	if err := apidoc.CheckModelChanges(before, after, models, apidoc.Sources{}); err == nil || !strings.Contains(err.Error(), "removed field") {
		t.Fatalf("removed source field accepted while the SDK still sends it: %v", err)
	}
	old := before.Pages[0].Fields[0]
	sources := apidoc.Sources{Pages: []apidoc.Page{{Path: after.Pages[0].Path, FieldDecisions: []apidoc.FieldDecision{{Section: old.Section, Digest: old.Digest, Model: "Body", Field: "Value", Type: "Optional[string]", Reason: "Retain the field under a separately reviewed contract."}}}}}
	if err := apidoc.CheckModelChanges(before, after, models, sources); err != nil {
		t.Fatalf("explicit reviewed retention rejected: %v", err)
	}
	sources.Pages[0].FieldDecisions[0].Digest = "stale"
	if err := apidoc.CheckModelChanges(before, after, models, sources); err == nil {
		t.Fatal("stale retention decision accepted")
	}
	models.Types[0].Fields = nil
	if err := apidoc.CheckModelChanges(before, after, models, apidoc.Sources{}); err != nil {
		t.Fatalf("removed DTO field rejected: %v", err)
	}
}

func TestReviewKeepsDuplicateRowOrderAndIndependentValues(t *testing.T) {
	before := apidoc.Document{Path: "api/rows.md"}
	after := apidoc.Document{Path: before.Path}
	for i := 1; i <= 12; i++ {
		field := apidoc.DocumentField{Section: "s", Name: "value", Type: "Строка", Digest: "old-" + strconv.Itoa(i), Line: i}
		before.Fields = append(before.Fields, field)
		field.Digest = "new-" + strconv.Itoa(i)
		field.Line += 100
		after.Fields = append(after.Fields, field)
	}
	for i := range 100 {
		field := apidoc.DocumentField{Section: "s", Name: "unchanged-" + strconv.Itoa(i), Digest: "same", Line: i + 1000}
		before.Fields = append(before.Fields, field)
		field.Line += 1000
		field.Note = "line and note alone do not change the row contract"
		after.Fields = append(after.Fields, field)
	}
	before.Fields = append(before.Fields, apidoc.DocumentField{Section: "s", Name: "gone", Digest: "removed", Line: 500})
	after.Fields = append(after.Fields, apidoc.DocumentField{Section: "s", Name: "added", Digest: "added", Line: 600})

	review := apidoc.CompareDocuments(apidoc.Documents{Pages: []apidoc.Document{before}}, apidoc.Documents{Pages: []apidoc.Document{after}}, apidoc.Models{})
	if len(review.Changes) != 14 || review.Changes[0].Kind != "field_added" || review.Changes[0].After.Name != "added" || review.Changes[1].Kind != "field_removed" || review.Changes[1].Before.Name != "gone" {
		t.Fatalf("added/removed rows or unchanged filtering differ: %+v", review.Changes)
	}
	for i, line := range []int{1, 10, 11, 12, 2, 3, 4, 5, 6, 7, 8, 9} {
		change := review.Changes[i+2]
		if change.Kind != "field_changed" || change.Before.Line != line || change.After.Line != line+100 || change.Before.Digest != "old-"+strconv.Itoa(line) || change.After.Digest != "new-"+strconv.Itoa(line) {
			t.Fatalf("duplicate occurrence %d: %+v", i, change)
		}
	}
	review.Changes[2].Before.Name = "edited report"
	review.Changes[2].After.Name = "edited report"
	if before.Fields[0].Name != "value" || after.Fields[0].Name != "value" || review.Changes[3].Before.Name != "value" {
		t.Fatal("review rows alias their inputs or each other")
	}
}

func TestReviewPreservesJSONExampleEquivalence(t *testing.T) {
	for _, test := range []struct {
		name, before, after string
		changed             bool
	}{
		{"identical exact number", `{"id":9007199254740993}`, `{"id":9007199254740993}`, false},
		{"format and property order", `{"b":2,"a":"<"}`, " { \"a\": \"\\u003c\", \"b\": 2 } ", false},
		{"duplicate last member", `{"a":1,"a":2}`, `{"a":2}`, false},
		{"numeric spelling", `{"a":1}`, `{"a":1.0}`, true},
		{"distinct exact numbers", `{"id":9007199254740993}`, `{"id":9007199254740994}`, true},
		{"null", `null`, `null`, false},
		{"identical malformed value", `{`, `{`, true},
		{"identical empty value", "", "", true},
		{"malformed versus valid", `{`, `{}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := apidoc.Document{Path: "api/example.md", Examples: []apidoc.DocumentExample{{Value: json.RawMessage(test.before)}}}
			after := apidoc.Document{Path: before.Path, Examples: []apidoc.DocumentExample{{Value: json.RawMessage(test.after)}}}
			review := apidoc.CompareDocuments(apidoc.Documents{Pages: []apidoc.Document{before}}, apidoc.Documents{Pages: []apidoc.Document{after}}, apidoc.Models{})
			if test.changed {
				if len(review.Changes) != 1 || review.Changes[0].Kind != "examples_or_routes_changed" {
					t.Fatalf("example difference lost: %+v", review.Changes)
				}
			} else if len(review.Changes) != 0 {
				t.Fatalf("equivalent examples differ: %+v", review.Changes)
			}
		})
	}
}

func TestModelTargetsWithoutBindingsRemainNil(t *testing.T) {
	for _, models := range []apidoc.Models{
		{},
		{Types: []apidoc.ModelType{{Name: "Unused", Fields: []apidoc.ModelField{{Name: "Value", JSON: "value", Type: "string"}}}}},
		{Types: []apidoc.ModelType{{Name: "Unused", Fields: []apidoc.ModelField{{Name: "Value", JSON: "value", Type: "string"}}}}, Operations: []apidoc.ModelOperation{{Name: "Unbound"}}},
	} {
		if targets := apidoc.ModelTargets(models); targets != nil {
			t.Fatalf("empty bindings changed their JSON/null contract: %#v", targets)
		}
	}
}

func TestReviewKeepsOwnersAndActionsForEveryChangeKind(t *testing.T) {
	field := apidoc.DocumentField{Section: "Параметры тела", Name: "value", Type: "Строка", Digest: "old"}
	page := func(path string) apidoc.Document {
		return apidoc.Document{Path: path, Fields: []apidoc.DocumentField{field}, Examples: []apidoc.DocumentExample{{Value: json.RawMessage(`{"value":1}`)}}}
	}
	before := apidoc.Documents{Pages: []apidoc.Document{page("api/a.md"), page("api/c.md")}}
	after := apidoc.Documents{Pages: []apidoc.Document{page("api/a.md"), page("api/b.md")}}
	after.Pages[0].Fields[0].Type = "Логический"
	after.Pages[0].Fields[0].Digest = "new"
	after.Pages[0].Examples[0].Value = json.RawMessage(`{"value":2}`)
	models := apidoc.Models{Types: []apidoc.ModelType{{Name: "Body", Kind: "struct", Fields: []apidoc.ModelField{{Name: "Value", JSON: "value", Type: "string"}}}}}
	for _, path := range []string{"api/a.md", "api/b.md", "api/c.md"} {
		models.Operations = append(models.Operations, apidoc.ModelOperation{Name: path, Source: path, Body: "Body"})
	}
	review := apidoc.CompareDocuments(before, after, models)
	want := []string{"field_changed", "examples_or_routes_changed", "page_added", "field_added", "page_removed", "field_removed"}
	if len(review.Changes) != len(want) {
		t.Fatalf("lost changes: %+v", review.Changes)
	}
	for i, change := range review.Changes {
		if change.Kind != want[i] || len(change.Targets) != 1 || len(change.Operations) != 1 || change.Operations[0] != change.Source || len(change.Actions) == 0 {
			t.Fatalf("change %d lost order or model context: %+v", i, change)
		}
	}
	if len(review.Changes[0].Edits) != 1 || review.Changes[0].Edits[0].After != "bool" || len(review.Disagreements) != 1 {
		t.Fatalf("lost suggested edit or evidence: %+v", review)
	}
}

func TestEvidenceKeepsContainerSectionAndPathSemantics(t *testing.T) {
	for _, test := range []struct {
		name, parent, member, section string
		matches                       bool
	}{
		{"object parent", "parent", "value", "Объект parent", true},
		{"last section only", "parent", "value", "Объект other / Объект parent", true},
		{"empty last section", "parent", "value", "Объект parent / ", false},
		{"unicode parent", "родитель", "value", "объект родитель", true},
		{"array container", "parent", "value", "Элементы массива parent", true},
		{"different parent", "parent", "value", "Объект other", false},
		{"slash in key", "outer/parent", "value", "Объект parent", true},
		{"empty parent segment", "parent/", "value", "Объект parent", false},
		{"leading path slash", "/parent", "value", "Объект parent", true},
		{"trailing path slash", "parent", "value/", "Объект parent", true},
		{"no prefix match", "parent", "value", "Объект parent2", false},
		{"underscore continues identifier", "parent", "value", "Объект _parent", false},
		{"underscore starts identifier", "_parent", "value", "Объект _parent", true},
		{"leading digits are skipped", "parent", "value", "Объект 123parent", true},
		{"number cannot start identifier", "123parent", "value", "Объект 123parent", false},
		{"Unicode number continues identifier", "parentⅧ", "value", "Объект parentⅧ", true},
		{"Unicode number cannot start identifier", "parent", "value", "Объект Ⅷparent", true},
		{"Unicode letters and digits", "父２", "value", "Объект 父２", true},
		{"combining mark splits identifier", "rent", "value", "Объект pa\u0301rent", true},
		{"combining mark is not a letter", "parent", "value", "Объект pa\u0301rent", false},
		{"invalid UTF-8 separates identifiers", "parent", "value", "Объект \xffparent\xfe", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			models := apidoc.Models{Types: []apidoc.ModelType{
				{Name: "Root", Kind: "struct", Fields: []apidoc.ModelField{{Name: "Parent", JSON: test.parent, Type: "Child"}}},
				{Name: "Child", Kind: "struct", Fields: []apidoc.ModelField{{Name: "Value", JSON: test.member, Type: "bool"}}},
			}, Operations: []apidoc.ModelOperation{{Name: "Create", Source: "api/container.md", Body: "Root"}}}
			documents := apidoc.Documents{Pages: []apidoc.Document{{Path: "api/container.md", Fields: []apidoc.DocumentField{{Name: test.member, Type: "Строка", Section: "Параметры тела / " + test.section}}}}}
			problems := apidoc.CheckModelEvidence(documents, models)
			if (len(problems) == 1) != test.matches || len(problems) > 1 {
				t.Fatalf("container match=%v; evidence: %+v", test.matches, problems)
			}
		})
	}
}

func TestChangedFieldKeepsItsOperationSection(t *testing.T) {
	catalog, err := apidoc.Load("../../api/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	models, err := apidoc.LoadModels("../../api/models.json", catalog)
	if err != nil {
		t.Fatal(err)
	}
	var before, after apidoc.Documents
	for _, target := range []*apidoc.Documents{&before, &after} {
		if err := apidoc.ReadJSON("../../api/documentation.json", target); err != nil {
			t.Fatal(err)
		}
	}
	for i := range after.Pages {
		if after.Pages[i].Path != "api/queues/view-autoaction-logs.md" {
			continue
		}
		for j := range after.Pages[i].Fields {
			field := &after.Pages[i].Fields[j]
			if field.Name == "id" && strings.Contains(field.Section, "#all-launches") {
				field.Type, field.Digest = "Логический", "changed"
				break
			}
		}
	}
	review := apidoc.CompareDocuments(before, after, models)
	if len(review.Changes) != 1 || len(review.Changes[0].Targets) != 1 || len(review.Changes[0].Edits) != 1 {
		t.Fatalf("imprecise operation ownership: %+v", review)
	}
	change := review.Changes[0]
	if change.Edits[0].Model != "AutoactionRun" || !slices.Equal(change.Operations, []string{"ListAutoactionLogs"}) {
		t.Fatalf("other operation modified: %+v", change)
	}
	if len(review.Disagreements) != 1 || review.Disagreements[0].Target.Model != "AutoactionRun" {
		t.Fatalf("other operation leaked into disagreements: %+v", review.Disagreements)
	}
}

func TestChangedModelTypeMustBeFixedOrExplicitlyReviewed(t *testing.T) {
	before, after, models := scalarChangeFixture()
	if err := apidoc.CheckModelChanges(before, after, models, apidoc.Sources{}); err == nil || !strings.Contains(err.Error(), "unresolved type disagreement") {
		t.Fatal(err)
	}
	field := after.Pages[0].Fields[0]
	decision := apidoc.FieldDecision{Section: field.Section, Digest: field.Digest, Model: "Body", Field: "Value", Type: "Optional[string]", Reason: "Published example still uses a string; retain it pending an upstream correction."}
	sources := apidoc.Sources{Pages: []apidoc.Page{{Path: after.Pages[0].Path, FieldDecisions: []apidoc.FieldDecision{decision}}}}
	if err := apidoc.CheckModelChanges(before, after, models, sources); err != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(*apidoc.FieldDecision){func(d *apidoc.FieldDecision) { d.Digest = "stale" }, func(d *apidoc.FieldDecision) { d.Type = "string" }, func(d *apidoc.FieldDecision) { d.Model = "OtherBody" }, func(d *apidoc.FieldDecision) { d.Section = "other section" }, func(d *apidoc.FieldDecision) { d.Reason = "  " }} {
		sources.Pages[0].FieldDecisions[0] = decision
		edit(&sources.Pages[0].FieldDecisions[0])
		if err := apidoc.CheckModelChanges(before, after, models, sources); err == nil {
			t.Fatal("stale or unjustified decision accepted")
		}
	}
	models.Types[0].Fields[0].Type = "Optional[bool]"
	if err := apidoc.CheckModelChanges(before, after, models, apidoc.Sources{}); err != nil {
		t.Fatal(err)
	}
}

func TestRoutesRequireCompleteCurrentClassification(t *testing.T) {
	document := apidoc.Document{Path: "api/get.md", Digest: "current", Signatures: []string{"GET /v3/items/{id}", "GET /v3/items/EXAMPLE"}}
	page := apidoc.Page{Signatures: []string{"GET /v3/items/{itemID}"}}
	if err := apidoc.CheckDocumentRoutes(document, page); err == nil || !strings.Contains(err.Error(), "unclassified") {
		t.Fatal(err)
	}
	decision := apidoc.RouteDecision{Signature: "GET /v3/items/EXAMPLE", Digest: "current", Reason: "Concrete request example for GetItem."}
	page.ExcludedSignatures = []apidoc.RouteDecision{decision}
	if err := apidoc.CheckDocumentRoutes(document, page); err != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(*apidoc.RouteDecision){func(d *apidoc.RouteDecision) { d.Digest = "stale" }, func(d *apidoc.RouteDecision) { d.Signature = "POST /v3/other" }, func(d *apidoc.RouteDecision) { d.Reason = "" }} {
		page.ExcludedSignatures[0] = decision
		edit(&page.ExcludedSignatures[0])
		if err := apidoc.CheckDocumentRoutes(document, page); err == nil {
			t.Fatal("stale or unjustified route exclusion accepted")
		}
	}
	page.ExcludedSignatures = []apidoc.RouteDecision{decision, decision}
	if err := apidoc.CheckDocumentRoutes(document, page); err == nil {
		t.Fatal("duplicate route classification accepted")
	}
}

func TestUnmappedOrUnknownChangedFieldsNeedAContextDecision(t *testing.T) {
	before, after, models := scalarChangeFixture()
	for _, name := range []string{"missing", "unknown type", "malformed row"} {
		t.Run(name, func(t *testing.T) {
			localAfter := after
			localAfter.Pages = append([]apidoc.Document(nil), after.Pages...)
			localAfter.Pages[0].Fields = append([]apidoc.DocumentField(nil), after.Pages[0].Fields...)
			field := &localAfter.Pages[0].Fields[0]
			switch name {
			case "missing":
				field.Name = "newField"
			case "unknown type":
				field.Type = "Unknown format"
			case "malformed row":
				field.Type = "Строка"
				field.Note = "bad cell count"
			}
			if err := apidoc.CheckModelChanges(before, localAfter, models, apidoc.Sources{}); err == nil {
				t.Fatal("unresolved context accepted")
			}
			sources := apidoc.Sources{Pages: []apidoc.Page{{Path: localAfter.Pages[0].Path, FieldDecisions: []apidoc.FieldDecision{{Section: field.Section, Digest: field.Digest, Reason: "Reviewed malformed/unspecified source context; no unverified model edit."}}}}}
			if name == "missing" {
				// Renaming the row also removes the known value field. Review
				// that separate retained binding explicitly, rather than letting
				// the unmapped-row decision cover it implicitly.
				old := before.Pages[0].Fields[0]
				sources.Pages[0].FieldDecisions = append(sources.Pages[0].FieldDecisions, apidoc.FieldDecision{Section: old.Section, Digest: old.Digest, Model: "Body", Field: "Value", Type: "Optional[string]", Reason: "Separately reviewed retention of value after the source-table rename."})
			}
			if err := apidoc.CheckModelChanges(before, localAfter, models, sources); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestModelSectionCannotSilentlyStopMatching(t *testing.T) {
	_, after, models := scalarChangeFixture()
	models.Operations[0].Sections = []string{"query"}
	if err := apidoc.CheckModelSections(after, models); err != nil {
		t.Fatal(err)
	}
	models.Operations[0].Sections = []string{"removed-section"}
	if err := apidoc.CheckModelSections(after, models); err == nil {
		t.Fatal("stale section accepted")
	}
}

func scalarChangeFixture() (apidoc.Documents, apidoc.Documents, apidoc.Models) {
	field := apidoc.DocumentField{Section: "Формат запроса {#query} / Параметры тела запроса / ", Name: "value", Type: "Строка", Digest: "before", Line: 7}
	before := apidoc.Documents{Version: 1, Revision: "r1", Pages: []apidoc.Document{{Path: "api/create.md", Fields: []apidoc.DocumentField{field}}}}
	field.Type, field.Digest = "Логический", "after"
	after := apidoc.Documents{Version: 1, Revision: "r2", Pages: []apidoc.Document{{Path: "api/create.md", Fields: []apidoc.DocumentField{field}}}}
	models := apidoc.Models{Types: []apidoc.ModelType{{Name: "Body", Kind: "struct", Source: "api/create.md", Fields: []apidoc.ModelField{{Name: "Value", JSON: "value", Type: "Optional[string]", Optional: true}}}}, Operations: []apidoc.ModelOperation{{Name: "CreateItem", Body: "Body", Source: "api/create.md"}}}
	return before, after, models
}
