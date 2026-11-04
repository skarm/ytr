package apidoc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

var documentationRevisionPattern = regexp.MustCompile(`revision=(r[0-9]+)`)

// DocumentationChange describes source changes and their concrete SDK owners.
type DocumentationChange struct {
	Source     string         `json:"source"`
	Kind       string         `json:"kind"`
	Before     *DocumentField `json:"before,omitempty"`
	After      *DocumentField `json:"after,omitempty"`
	Targets    []ModelTarget  `json:"targets,omitempty"`
	Operations []string       `json:"operations,omitempty"`
	Actions    []string       `json:"actions"`
	Edits      []ModelEdit    `json:"suggested_edits,omitempty"`
}

// ModelEdit is a conservative scalar-type suggestion, never an automatic source update.
type ModelEdit struct {
	Pointer string `json:"pointer"`
	Model   string `json:"model"`
	Field   string `json:"field"`
	Before  string `json:"before"`
	After   string `json:"after"`
}

// Review is a reproducible change report; candidates never overwrite reviewed sources.
type Review struct {
	Version         int                   `json:"version"`
	BaseDigest      string                `json:"base_digest"`
	CandidateDigest string                `json:"candidate_digest"`
	IndexDigest     string                `json:"index_digest"`
	Revision        string                `json:"revision"`
	Changes         []DocumentationChange `json:"changes"`
	Disagreements   []EvidenceProblem     `json:"table_model_disagreements,omitempty"`
}

type documentFieldKey struct{ section, name, occurrence string }

// ReviewOnline captures the current index and pages and computes an actionable diff.
func ReviewOnline(ctx context.Context, catalog Catalog, baseline Documents, models Models, fetch Fetcher) (Review, Documents, []byte, error) {
	index, err := fetch(ctx, "https://yandex.ru/support/tracker/ru/llms.txt")
	if err != nil {
		return Review{}, Documents{}, nil, err
	}

	urls := IndexURLs(string(index))
	if len(urls) == 0 {
		return Review{}, Documents{}, nil, errors.New("live documentation index has no API pages")
	}

	inventory := Sources{}

	for address := range urls {
		name := strings.TrimPrefix(address, "https://yandex.ru/support/tracker/ru/")
		inventory.Pages = append(inventory.Pages, Page{Path: name, URL: address})
	}

	sort.Slice(inventory.Pages, func(i, j int) bool { return inventory.Pages[i].Path < inventory.Pages[j].Path })

	candidate, err := CaptureDocuments(ctx, inventory, fetch)
	if err != nil {
		return Review{}, Documents{}, index, err
	}

	candidate.Revision = catalog.Revision
	if match := documentationRevisionPattern.FindSubmatch(index); match != nil {
		candidate.Revision = string(match[1])
	}

	review := CompareDocuments(baseline, candidate, models)

	return review, candidate, index, nil
}

// CompareDocuments maps changed rows to JSON pointers in api/models.json.
func CompareDocuments(before, after Documents, models Models) Review {
	review := Review{Version: 1, BaseDigest: DocumentsDigest(before), Revision: after.Revision}

	oldPages, newPages := map[string]Document{}, map[string]Document{}
	for _, page := range before.Pages {
		oldPages[page.Path] = page
	}

	for _, page := range after.Pages {
		newPages[page.Path] = page
	}

	var paths []string
	for name := range oldPages {
		paths = append(paths, name)
	}

	for name := range newPages {
		if _, exists := oldPages[name]; !exists {
			paths = append(paths, name)
		}
	}

	sort.Strings(paths)

	for _, name := range paths {
		old, oldExists := oldPages[name]

		current, newExists := newPages[name]
		if !oldExists || !newExists {
			kind := "page_added"
			if !newExists {
				kind = "page_removed"
			}

			review.Changes = append(review.Changes, DocumentationChange{Source: name, Kind: kind})
			current.Path = name
			review.Changes = append(review.Changes, compareDocumentFields(old, current)...)

			continue
		}

		start := len(review.Changes)
		review.Changes = append(review.Changes, compareDocumentFields(old, current)...)

		// Source line movement does not change example semantics.
		if !sameExamples(old.Examples, current.Examples) || !slices.Equal(old.Signatures, current.Signatures) {
			review.Changes = append(review.Changes, DocumentationChange{Source: name, Kind: "examples_or_routes_changed"})
		}

		if old.Digest != current.Digest && len(review.Changes) == start {
			review.Changes = append(review.Changes, DocumentationChange{Source: name, Kind: "prose_changed"})
		}
	}

	if len(review.Changes) == 0 {
		return review
	}

	targets := ModelTargets(models)
	for i, change := range review.Changes {
		review.Changes[i] = decorateChange(change, targets, models)
	}

	review.Disagreements = checkModelEvidence(changedDocumentFields(after, review.Changes), models, targets)

	return review
}

// DocumentsDigest identifies the reviewed baseline and prevents accepting a stale report.
func DocumentsDigest(documents Documents) string {
	digest := sha256.New()
	if err := jsonv2.MarshalWrite(digest, documents, json.DefaultOptionsV1()); err != nil {
		// Keep the existing digest for a value that cannot be encoded.
		return fmt.Sprintf("%x", sha256.Sum256(nil))
	}

	return hex.EncodeToString(digest.Sum(nil))
}

// WriteReview saves a text report, structured report, and candidate source facts.
func WriteReview(directory string, review Review, candidate Documents, index []byte) error {
	review.CandidateDigest = DocumentsDigest(candidate)
	review.IndexDigest = fmt.Sprintf("%x", sha256.Sum256(index))

	if err := os.MkdirAll(directory, 0o750); err != nil {
		return err
	}

	if err := WriteJSON(filepath.Join(directory, "report.json"), review); err != nil {
		return err
	}

	if err := WriteJSON(filepath.Join(directory, "candidate.json"), candidate); err != nil {
		return err
	}

	if err := WriteArtifact(filepath.Join(directory, "index.txt"), index); err != nil {
		return err
	}

	var text strings.Builder
	fmt.Fprintf(&text, "Tracker documentation %s: %d changes\n", review.Revision, len(review.Changes))

	for _, change := range review.Changes {
		fmt.Fprintf(&text, "\n%s: %s\nhttps://yandex.ru/support/tracker/ru/%s\n", change.Source, change.Kind, change.Source)

		for _, side := range []struct {
			name  string
			field *DocumentField
		}{{"before", change.Before}, {"after", change.After}} {
			if side.field != nil {
				fmt.Fprintf(&text, "  %s line %d: %s [%s], presence=%s\n  section: %s\n", side.name, side.field.Line, side.field.Name, side.field.Type, side.field.Required, side.field.Section)
			}
		}

		for _, target := range change.Targets {
			fmt.Fprintf(&text, "  api/models.json#%s: %s.%s (%s), wire path %s\n", target.Pointer, target.Model, target.Field, target.Type, target.Path)
		}

		for _, edit := range change.Edits {
			fmt.Fprintf(&text, "  suggested edit api/models.json#%s: %q -> %q (%s.%s); preserve presence and check source context\n", edit.Pointer, edit.Before, edit.After, edit.Model, edit.Field)
		}

		fmt.Fprintf(&text, "  operations: %s\n", strings.Join(change.Operations, ", "))

		for _, action := range change.Actions {
			fmt.Fprintf(&text, "  - %s\n", action)
		}
	}

	fmt.Fprintf(&text, "\nTable/model disagreements needing contextual review: %d (details in report.json). These are not automatically applied type changes.\n", len(review.Disagreements))

	return WriteArtifact(filepath.Join(directory, "report.txt"), []byte(text.String()))
}

func changedDocumentFields(documents Documents, changes []DocumentationChange) Documents {
	changed := make(map[[3]string]bool)

	for _, change := range changes {
		if field := change.After; field != nil {
			changed[[3]string{change.Source, field.Section, field.Digest}] = true
		}
	}

	var result Documents

	for _, document := range documents.Pages {
		next := Document{Path: document.Path}
		for _, field := range document.Fields {
			if changed[[3]string{document.Path, field.Section, field.Digest}] {
				next.Fields = append(next.Fields, field)
			}
		}

		if len(next.Fields) > 0 {
			result.Pages = append(result.Pages, next)
		}
	}

	return result
}

func compareDocumentFields(before, after Document) []DocumentationChange {
	if slices.Equal(before.Fields, after.Fields) {
		return nil
	}

	oldFields, newFields := indexDocumentFields(before.Fields), indexDocumentFields(after.Fields)

	var keys []documentFieldKey

	for key, oldIndex := range oldFields {
		if newIndex, exists := newFields[key]; exists {
			previous, next := before.Fields[oldIndex], after.Fields[newIndex]
			if previous.Digest == next.Digest && previous.Type == next.Type && previous.Required == next.Required {
				continue
			}
		}

		keys = append(keys, key)
	}

	for key := range newFields {
		if _, exists := oldFields[key]; !exists {
			keys = append(keys, key)
		}
	}

	if len(keys) == 0 {
		return nil
	}

	// Preserve the old string order, with a deterministic tie-break for collisions.
	sort.Slice(keys, func(i, j int) bool {
		left, right := keys[i], keys[j]

		first, second := left.section+"|"+left.name+"|"+left.occurrence, right.section+"|"+right.name+"|"+right.occurrence
		if first != second {
			return first < second
		}

		if left.section != right.section {
			return left.section < right.section
		}

		return left.name < right.name
	})

	changes := make([]DocumentationChange, 0, len(keys))

	for _, key := range keys {
		oldIndex, existed := oldFields[key]
		newIndex, exists := newFields[key]

		var previous, next DocumentField
		if existed {
			previous = before.Fields[oldIndex]
		}

		if exists {
			next = after.Fields[newIndex]
		}

		change := DocumentationChange{Source: after.Path, Kind: "field_changed"}
		if existed {
			change.Before = &previous
		} else {
			change.Kind = "field_added"
		}

		if exists {
			change.After = &next
		} else {
			change.Kind = "field_removed"
		}

		changes = append(changes, change)
	}

	return changes
}

func sameExamples(left, right []DocumentExample) bool {
	if len(left) != len(right) {
		return false
	}

	for i := range left {
		if left[i].Section != right[i].Section || left[i].Status != right[i].Status || !sameExampleJSON(left[i].Value, right[i].Value) {
			return false
		}
	}

	return true
}

func sameExampleJSON(left, right json.RawMessage) bool {
	if bytes.Equal(left, right) {
		return json.Valid(left)
	}

	canonical := func(value json.RawMessage) []byte {
		if !json.Valid(value) {
			return nil
		}

		decoder := json.NewDecoder(bytes.NewReader(value))
		decoder.UseNumber()

		var decoded any
		if decoder.Decode(&decoded) != nil {
			return nil
		}

		data, _ := json.Marshal(decoded)

		return data
	}
	first, second := canonical(left), canonical(right)

	return first != nil && second != nil && bytes.Equal(first, second)
}

func indexDocumentFields(fields []DocumentField) map[documentFieldKey]int {
	result := map[documentFieldKey]int{}
	counts := map[[2]string]int{}

	for index, field := range fields {
		key := [2]string{field.Section, field.Name}
		counts[key]++
		result[documentFieldKey{field.Section, field.Name, strconv.Itoa(counts[key])}] = index
	}

	return result
}

func decorateChange(change DocumentationChange, targets []ModelTarget, models Models) DocumentationChange {
	field := change.After
	if field == nil {
		field = change.Before
	}

	for _, target := range targets {
		if target.Source == change.Source && (field == nil || targetMatchesField(target, change.Source, *field)) {
			change.Targets = append(change.Targets, target)
			for _, name := range target.Operations {
				if !slices.Contains(change.Operations, name) {
					change.Operations = append(change.Operations, name)
				}
			}
		}
	}

	for _, operation := range models.Operations {
		if operation.Source == change.Source && (field == nil || sectionMatches(field.Section, operation.Sections)) && !slices.Contains(change.Operations, operation.Name) {
			change.Operations = append(change.Operations, operation.Name)
		}
	}

	sort.Strings(change.Operations)

	change.Edits = suggestModelEdits(change)

	switch change.Kind {
	case "page_added", "page_removed", "examples_or_routes_changed":
		change.Actions = append(change.Actions, "Сверить HTTP method/path/status/encoding; исправить api/catalog.json и привязки операций api/models.json.")
	case "field_added":
		change.Actions = append(change.Actions, "Добавить документированное поле в DTO или query/header binding; задать тип, Optional/null и источник. Если контекст неоднозначен, проверить указанную секцию источника.")
	case "field_removed":
		change.Actions = append(change.Actions, "Проверить удаление или перенос поля; обновить указанные DTO и их вызовы напрямую.")
	default:
		change.Actions = append(change.Actions, "Сверить изменённый тип, обязательность, допустимые значения и правила сериализации в указанной секции; исправить перечисленные JSON pointers.")
	}

	if len(change.Targets) == 0 {
		change.Actions = append(change.Actions, "Автоматическое сопоставление не найдено: проверить операции страницы и определить владельца поля. Отсутствие сопоставления не означает отсутствие изменений SDK.")
	}

	change.Actions = append(change.Actions, "Обновить независимые fixtures typed/testdata по источнику; выполнить go generate ./... и go test ./...; проверить новые опубликованные примеры.")

	return change
}

func suggestModelEdits(change DocumentationChange) []ModelEdit {
	var edits []ModelEdit

	if change.Before != nil && change.After != nil && change.Before.Type != change.After.Type {
		kinds := documentKinds(change.After.Type)
		if len(kinds) == 1 {
			for _, target := range change.Targets {
				if after := suggestScalarType(target.Type, kinds[0]); after != "" && after != target.Type && strings.HasPrefix(target.Pointer, "/types/") {
					edits = append(edits, ModelEdit{Pointer: target.Pointer, Model: target.Model, Field: target.Field, Before: target.Type, After: after})
				}
			}
		}
	}

	return edits
}

func suggestScalarType(before, kind string) string {
	after := map[string]string{"string": "string", "number": "json.Number", "boolean": "bool"}[kind]
	if after == "" {
		return ""
	}

	inner := before
	for strings.HasPrefix(inner, "Optional[") || strings.HasPrefix(inner, "Update[") {
		_, inner, _ = strings.Cut(inner, "[")
		inner = strings.TrimSuffix(inner, "]")
	}

	inner = strings.TrimPrefix(inner, "*")
	if map[string]string{"string": "string", "bool": "boolean", "int64": "number", "json.Number": "number"}[inner] == kind {
		return before
	}

	if !slices.Contains([]string{"string", "bool", "int64", "json.Number"}, inner) {
		return ""
	}

	return strings.Replace(before, inner, after, 1)
}
