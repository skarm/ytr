package apidoc

import (
	"errors"
	"fmt"
	"strings"
)

// CheckDocumentRoutes requires every captured route to be an SDK operation or
// an explicitly reviewed example/reference bound to the current page digest.
func CheckDocumentRoutes(document Document, page Page) error {
	classified := make(map[string]bool)
	for _, signature := range page.Signatures {
		classified[RouteShape(signature)] = true
	}

	captured := make(map[string]bool)
	for _, signature := range document.Signatures {
		captured[RouteShape(signature)] = true
	}

	for _, decision := range page.ExcludedSignatures {
		key := RouteShape(decision.Signature)
		if !captured[key] || classified[key] || decision.Digest != document.Digest || strings.TrimSpace(decision.Reason) == "" {
			return fmt.Errorf("%s: stale, duplicate or unjustified route decision for %s", document.Path, decision.Signature)
		}

		classified[key] = true
	}

	for _, signature := range document.Signatures {
		if !classified[RouteShape(signature)] {
			return fmt.Errorf("%s: unclassified documented route %s; add its SDK operation or a justified excluded_signatures entry bound to digest %s", document.Path, signature, document.Digest)
		}
	}

	return nil
}

// CheckModelChanges recomputes discrepancies using the edited models. A report
// generated before those edits is never treated as evidence that they were made.
func CheckModelChanges(before, after Documents, models Models, sources Sources) error {
	if err := CheckModelSections(after, models); err != nil {
		return err
	}

	review := CompareDocuments(before, after, models)

	pages := make(map[string]Page, len(sources.Pages))
	for _, page := range sources.Pages {
		pages[page.Path] = page
	}

	var problems []error

	for _, problem := range review.Disagreements {
		field := DocumentField{Section: problem.Section, Digest: problem.Digest}
		if !hasFieldDecision(pages[problem.Source], field, &problem.Target) {
			problems = append(problems, fmt.Errorf("%s:%d: unresolved type disagreement at api/models.json#%s (%s.%s: %s; documented %s); fix the model or record a field_decisions entry for row %s with the exact model/type and a reason", problem.Source, problem.Line, problem.Target.Pointer, problem.Target.Model, problem.Target.Field, problem.Target.Type, problem.Type, problem.Digest))
		}
	}

	for _, change := range review.Changes {
		field := change.After
		if field == nil {
			if err := checkRemovedField(change, pages[change.Source]); err != nil {
				problems = append(problems, err)
			}

			continue
		}

		unmapped := len(change.Targets) == 0

		unknownType := len(documentKinds(field.Type)) == 0 && (change.Before == nil || change.Before.Type != field.Type)
		if (unmapped || unknownType || field.Note != "") && !hasContextDecision(pages[change.Source], *field, change.Targets) {
			problems = append(problems, fmt.Errorf("%s:%d: unresolved source context for field %s in %s; add a precise model/section binding or a justified field_decisions entry for row %s", change.Source, field.Line, field.Name, field.Section, field.Digest))
		}
	}

	return errors.Join(problems...)
}

// CheckModelSections ensures explicit operation scopes still exist in the source.
func CheckModelSections(documents Documents, models Models) error {
	sections := map[string][]string{}

	for _, document := range documents.Pages {
		for _, field := range document.Fields {
			sections[document.Path] = append(sections[document.Path], field.Section)
		}

		for _, example := range document.Examples {
			sections[document.Path] = append(sections[document.Path], example.Section)
		}
	}

	for _, operation := range models.Operations {
		for _, anchor := range operation.Sections {
			found := false

			for _, section := range sections[operation.Source] {
				if sectionMatches(section, []string{anchor}) {
					found = true
					break
				}
			}

			if !found {
				return fmt.Errorf("%s: operation %s has no source section {#%s}; update its sections binding", operation.Source, operation.Name, anchor)
			}
		}
	}

	return nil
}

func checkRemovedField(change DocumentationChange, page Page) error {
	if change.Kind != "field_removed" || change.Before == nil {
		return nil
	}

	var problems []error

	for _, target := range change.Targets {
		if !hasFieldDecision(page, *change.Before, &target) {
			problems = append(problems, fmt.Errorf("%s:%d: removed field %s still exists at api/models.json#%s (%s.%s); remove the stale binding or record a justified field_decisions entry for row %s and current type %s", change.Source, change.Before.Line, change.Before.Name, target.Pointer, target.Model, target.Field, change.Before.Digest, target.Type))
		}
	}

	return errors.Join(problems...)
}

func hasContextDecision(page Page, field DocumentField, targets []ModelTarget) bool {
	if hasFieldDecision(page, field, nil) {
		return true
	}

	for _, target := range targets {
		if hasFieldDecision(page, field, &target) {
			return true
		}
	}

	return false
}

func hasFieldDecision(page Page, field DocumentField, target *ModelTarget) bool {
	for _, decision := range page.FieldDecisions {
		if decision.Digest != field.Digest || decision.Section != field.Section || strings.TrimSpace(decision.Reason) == "" {
			continue
		}

		if target == nil {
			if decision.Model == "" && decision.Field == "" && decision.Type == "" {
				return true
			}

			continue
		}

		if decision.Model == target.Model && decision.Field == target.Field && decision.Type == target.Type {
			return true
		}
	}

	return false
}
