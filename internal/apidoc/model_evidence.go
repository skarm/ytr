package apidoc

import (
	"fmt"
	"go/ast"
	"go/parser"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// ModelTarget locates the exact SDK registry entry affected by a source field.
type ModelTarget struct {
	Pointer    string   `json:"pointer"`
	Model      string   `json:"model"`
	Field      string   `json:"field"`
	JSON       string   `json:"json"`
	Type       string   `json:"type"`
	Source     string   `json:"source"`
	Role       string   `json:"role"`
	Path       string   `json:"path"`
	Operations []string `json:"operations"`
	Sections   []string `json:"sections,omitempty"`
}

// EvidenceProblem identifies a possible wire-kind disagreement with table data.
// Intersecting unions and open JSON remain subject to example/semantic review.
type EvidenceProblem struct {
	Source  string      `json:"source"`
	Line    int         `json:"line"`
	Field   string      `json:"field"`
	Type    string      `json:"documented_type"`
	Target  ModelTarget `json:"target"`
	Section string      `json:"section"`
	Digest  string      `json:"digest"`
}

type modelTargetResolver struct {
	types          map[string]ModelType
	indexes        map[string]int
	targets        []ModelTarget
	identities     map[modelTargetKey]int
	referenceNames map[string][]string
}

type modelTargetKey struct{ pointer, source, role, path, sections string }

type modelTrace struct {
	source, role, prefix, operation string
	owner                           string
	sections                        []string
	stack                           map[string]bool
}

// ModelTargets resolves shared model references back to their consuming operations.
func ModelTargets(models Models) []ModelTarget {
	resolver := modelTargetResolver{types: make(map[string]ModelType, len(models.Types)), indexes: make(map[string]int, len(models.Types)), identities: map[modelTargetKey]int{}, referenceNames: map[string][]string{}}
	capacity := 0

	for index, model := range models.Types {
		resolver.types[model.Name], resolver.indexes[model.Name] = model, index
		capacity += len(model.Fields)
	}

	for _, operation := range models.Operations {
		capacity += len(operation.Queries) + len(operation.Headers)
	}

	if capacity > 0 && len(models.Operations) > 0 {
		resolver.targets = make([]ModelTarget, 0, capacity)
		resolver.identities = make(map[modelTargetKey]int, capacity)
	}

	for index, operation := range models.Operations {
		trace := modelTrace{source: operation.Source, owner: operation.Source, sections: operation.Sections, role: "request", operation: operation.Name, stack: map[string]bool{}}
		resolver.walk(operation.Body, trace)
		trace.role = "response"
		resolver.walk(operation.Response, trace)
		resolver.addParameters(index, operation)
	}

	if len(resolver.targets) == 0 {
		return nil
	}

	return resolver.targets
}

// CheckModelEvidence compares source table wire kinds with independently declared models.
func CheckModelEvidence(documents Documents, models Models) []EvidenceProblem {
	return checkModelEvidence(documents, models, ModelTargets(models))
}

func (r *modelTargetResolver) add(target ModelTarget, operation string) {
	key := modelTargetKey{target.Pointer, target.Source, target.Role, target.Path, modelSectionsKey(target.Sections)}
	if index, exists := r.identities[key]; exists {
		if !slices.Contains(r.targets[index].Operations, operation) {
			r.targets[index].Operations = append(r.targets[index].Operations, operation)
		}

		return
	}

	target.Operations = []string{operation}
	r.identities[key] = len(r.targets)
	r.targets = append(r.targets, target)
}

func (r *modelTargetResolver) walk(expression string, trace modelTrace) {
	for _, name := range r.references(expression) {
		model, exists := r.types[name]

		identity := name + "|" + trace.source + "|" + trace.role
		if !exists || trace.stack[identity] {
			continue
		}

		trace.stack[identity] = true
		r.walkModel(name, model, trace)
		delete(trace.stack, identity)
	}
}

func (r *modelTargetResolver) references(expression string) []string {
	if names, found := r.referenceNames[expression]; found {
		return names
	}

	node, err := parser.ParseExpr(expression)
	if err != nil {
		r.referenceNames[expression] = nil
		return nil
	}

	var names []string

	ast.Inspect(node, func(node ast.Node) bool {
		if _, qualified := node.(*ast.SelectorExpr); qualified {
			return false
		}

		if name, ok := node.(*ast.Ident); ok {
			if _, exists := r.types[name.Name]; exists && !slices.Contains(names, name.Name) {
				names = append(names, name.Name)
			}
		}

		return true
	})

	r.referenceNames[expression] = names

	return names
}

func (r *modelTargetResolver) walkModel(name string, model ModelType, trace modelTrace) {
	if model.Source != "" {
		nextSource := sourcePath(model.Source)
		if nextSource != trace.source {
			trace.prefix = ""
		}

		trace.source = nextSource
	}

	for index, field := range model.Fields {
		next := trace
		if field.Source != "" {
			next.source = sourcePath(field.Source)
		}

		next.prefix += "/" + field.JSON
		if field.JSON != "" {
			var sections []string
			if next.source == trace.owner {
				sections = trace.sections
			}

			r.add(ModelTarget{Pointer: fmt.Sprintf("/types/%d/fields/%d/type", r.indexes[name], index), Model: name, Field: field.Name, JSON: field.JSON, Type: field.Type, Source: next.source, Role: trace.role, Path: next.prefix, Sections: sections}, trace.operation)
		}

		r.walk(field.Type, next)
	}

	for _, variant := range model.Variants {
		r.walk(variant.Type, trace)
	}

	r.walk(model.Target, trace)
}

func (r *modelTargetResolver) addParameters(index int, operation ModelOperation) {
	for queryIndex, query := range operation.Queries {
		valueType := query.Type
		if query.CSV {
			valueType = "string"
		}

		r.add(ModelTarget{Pointer: fmt.Sprintf("/operations/%d/queries/%d/type", index, queryIndex), Model: operation.Request, Field: query.Field, JSON: query.Key, Type: valueType, Source: operation.Source, Role: "query", Sections: operation.Sections}, operation.Name)
	}

	for headerIndex, header := range operation.Headers {
		r.add(ModelTarget{Pointer: fmt.Sprintf("/operations/%d/headers/%d", index, headerIndex), Model: operation.Request, Field: header.Field, JSON: header.Key, Type: "string", Source: operation.Source, Role: "header", Sections: operation.Sections}, operation.Name)
	}
}

func modelSectionsKey(sections []string) string {
	var key strings.Builder

	// Length prefixes keep commas and other separators inside names unambiguous.
	for _, section := range sections {
		key.WriteString(strconv.Itoa(len(section)))
		key.WriteByte(':')
		key.WriteString(section)
	}

	return key.String()
}

func sourcePath(source string) string {
	source = strings.TrimPrefix(source, "https://yandex.ru/support/tracker/ru/")
	source, _, _ = strings.Cut(source, "#")
	source, _, _ = strings.Cut(source, "?")

	return source
}

func checkModelEvidence(documents Documents, models Models, targets []ModelTarget) []EvidenceProblem {
	byField := make(map[[2]string][]int)

	for index, target := range targets {
		key := [2]string{target.Source, target.JSON}
		byField[key] = append(byField[key], index)
	}

	types := map[string]ModelType{}
	for _, model := range models.Types {
		types[model.Name] = model
	}

	var problems []EvidenceProblem

	seen := map[[4]string]bool{}

	for _, document := range documents.Pages {
		for _, field := range document.Fields {
			kinds := documentKinds(field.Type)
			if len(kinds) == 0 {
				continue
			}

			for _, index := range byField[[2]string{document.Path, field.Name}] {
				target := targets[index]
				if !targetMatchesField(target, document.Path, field) {
					continue
				}

				actual := ModelJSONKinds(target.Type, types)
				if kindsOverlap(kinds, actual) {
					continue
				}

				key := [4]string{document.Path, target.Pointer, field.Section, field.Digest}
				if seen[key] {
					continue
				}

				seen[key] = true

				problems = append(problems, EvidenceProblem{Source: document.Path, Line: field.Line, Field: field.Name, Type: field.Type, Target: target, Section: field.Section, Digest: field.Digest})
			}
		}
	}

	return problems
}

func fieldRoleMatches(field DocumentField, role string) bool {
	if strings.Contains(field.Section, "#answer") || strings.Contains(field.Section, "Параметры ответа") {
		return role == "response"
	}

	if strings.Contains(field.Section, "Параметры тела") {
		return role == "request"
	}

	if strings.Contains(field.Section, "Параметры запроса") {
		return role == "query"
	}

	if strings.Contains(field.Section, "Ресурс") {
		return role == "path"
	}

	if strings.Contains(field.Section, "Заголовки") {
		return role == "header"
	}

	if strings.Contains(field.Section, "#query") {
		return role != "response"
	}

	return true
}

func targetMatchesField(target ModelTarget, source string, field DocumentField) bool {
	return target.Source == source && field.Name == target.JSON && sectionMatches(field.Section, target.Sections) && fieldRoleMatches(field, target.Role) && fieldContainerMatches(field, target)
}

func sectionMatches(section string, anchors []string) bool {
	if len(anchors) == 0 {
		return true
	}

	for _, anchor := range anchors {
		if strings.Contains(section, "{#"+anchor+"}") {
			return true
		}
	}

	return false
}

func fieldContainerMatches(field DocumentField, target ModelTarget) bool {
	group := field.Section
	if index := strings.LastIndex(group, " / "); index >= 0 {
		group = group[index+3:]
	}

	if strings.Contains(group, "объект") || strings.Contains(group, "Объект") || strings.Contains(group, "массива") {
		path := strings.Trim(target.Path, "/")

		end := strings.LastIndexByte(path, '/')
		if end < 0 {
			return false
		}

		parent := path[:end]
		if start := strings.LastIndexByte(parent, '/'); start >= 0 {
			parent = parent[start+1:]
		}

		return hasModelContainer(group, parent)
	}

	return strings.Count(target.Path, "/") == 1 || target.Role == "query" || target.Role == "header"
}

// hasModelContainer matches the documentation's Unicode identifiers without
// materializing every word in a section. Numbers may continue an identifier.
func hasModelContainer(group, parent string) bool {
	start := -1

	for index, ch := range group {
		if ch == '_' || unicode.IsLetter(ch) || start >= 0 && unicode.IsNumber(ch) {
			if start < 0 {
				start = index
			}

			continue
		}

		if start >= 0 && group[start:index] == parent {
			return true
		}

		start = -1
	}

	return start >= 0 && group[start:] == parent
}

func documentKinds(value string) []string {
	value = strings.ToLower(value)
	if strings.Contains(value, "массив") || strings.Contains(value, "список") {
		if before, _, ok := strings.Cut(value, "или"); ok && !strings.Contains(before, "массив") && !strings.Contains(before, "список") {
			return append(documentKinds(before), "array")
		}

		return []string{"array"}
	}

	var kinds []string

	for _, pair := range [][2]string{{"строк", "string"}, {"числ", "number"}, {"логическ", "boolean"}, {"булев", "boolean"}, {"объект", "object"}} {
		if strings.Contains(value, pair[0]) {
			kinds = append(kinds, pair[1])
		}
	}

	return kinds
}

func kindsOverlap(left, right []string) bool {
	if slices.Contains(right, "any") {
		return true
	}

	for _, kind := range left {
		if slices.Contains(right, kind) {
			return true
		}
	}

	return false
}
