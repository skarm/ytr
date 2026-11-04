package apidoc

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Models struct {
	Version    int              `json:"version"`
	Revision   string           `json:"revision"`
	Types      []ModelType      `json:"types"`
	Operations []ModelOperation `json:"operations"`
}

type ModelType struct {
	Name, Kind, Source, Target, Discriminator, OpenReason string
	Open                                                  bool
	Fields                                                []ModelField
	Variants                                              []ModelVariant
}

type ModelField struct {
	Name, JSON, Type, Source string
	Optional, OmitEmpty      bool
	Enum                     []string
}

type ModelVariant struct {
	Name, Type, JSONKind string
	Tags, Keys           []string
}

type ModelOperation struct {
	Name, Request, Response, Encoding, Body, Source string
	Sections                                        []string
	Stream, Extras, AllowEmpty                      bool
	Paths                                           []ModelPath
	Queries                                         []ModelQuery
	Headers                                         []ModelHeader
}

type (
	ModelPath struct{ Name, Type string }

	ModelQuery struct {
		Field, Key, Type, Prefix string
		Optional, CSV            bool
	}
)

type ModelHeader struct{ Field, Key string }

func LoadModels(path string, catalog Catalog) (Models, error) {
	var models Models
	if err := ReadJSON(path, &models); err != nil {
		return models, err
	}

	return models, ValidateModels(models, catalog)
}

func ValidateModels(models Models, catalog Catalog) error {
	if models.Version != 1 {
		return fmt.Errorf("unsupported model version %d", models.Version)
	}

	if models.Revision != catalog.Revision {
		return fmt.Errorf("model revision %q does not match catalog %q", models.Revision, catalog.Revision)
	}

	types, err := validateModelTypes(models.Types)
	if err != nil {
		return err
	}

	if err := validateModelOperations(models.Operations, catalog.Operations); err != nil {
		return err
	}

	for _, operation := range models.Operations {
		if err := validateOperationBindings(operation, types); err != nil {
			return fmt.Errorf("model operation %s: %w", operation.Name, err)
		}
	}

	return nil
}

func validateModelOperations(operations []ModelOperation, catalog []Operation) error {
	byName := make(map[string]ModelOperation, len(operations))
	bySource := make(map[string]int)

	for _, op := range operations {
		if _, exists := byName[op.Name]; exists {
			return fmt.Errorf("duplicate model operation %s", op.Name)
		}

		byName[op.Name] = op
		bySource[op.Source]++
	}

	if len(byName) != len(catalog) {
		return fmt.Errorf("model coverage: %d operations, want %d", len(byName), len(catalog))
	}

	for _, op := range catalog {
		model, exists := byName[op.Name]
		if !exists {
			return fmt.Errorf("missing model operation %s", op.Name)
		}

		if bySource[model.Source] > 1 && len(model.Sections) == 0 {
			return fmt.Errorf("model operation %s: shared source page requires explicit sections", model.Name)
		}

		if err := validateModelOperation(model, op); err != nil {
			return fmt.Errorf("model operation %s: %w", op.Name, err)
		}
	}

	return nil
}

func validateModelOperation(model ModelOperation, op Operation) error {
	seen := map[string]bool{}
	for _, section := range model.Sections {
		if section == "" || strings.ContainsAny(section, "{}#/") || strings.IndexFunc(section, unicode.IsSpace) >= 0 || seen[section] {
			return fmt.Errorf("invalid or duplicate source section %q", section)
		}

		seen[section] = true
	}

	if model.Encoding != op.Request || model.Stream != (op.Response == "stream") {
		return fmt.Errorf("encoding does not match catalog (%s request, %s response)", op.Request, op.Response)
	}

	if model.Source != op.Source {
		return fmt.Errorf("source %q does not match catalog %q", model.Source, op.Source)
	}

	parameters := PathParameters(op.Path)
	if len(model.Paths) != len(parameters) {
		return fmt.Errorf("path parameters: got %d, want %d", len(model.Paths), len(parameters))
	}

	for i, name := range parameters {
		first, size := utf8.DecodeRuneInString(name)
		field := string(unicode.ToUpper(first)) + name[size:]

		fieldType := "string"
		if name == "entityType" {
			fieldType = "ytr.EntityType"
		}

		if model.Paths[i].Name != field || model.Paths[i].Type != fieldType {
			return fmt.Errorf("path parameter %d: want %s %s", i+1, field, fieldType)
		}
	}

	return nil
}
