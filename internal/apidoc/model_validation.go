package apidoc

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strings"
	"unicode"
)

// ModelJSONKinds returns the possible wire kinds of a declared model expression.
// It is used by documentation checks as well as union validation.
func ModelJSONKinds(value string, types map[string]ModelType) []string {
	return modelJSONKinds(value, types, make(map[string]bool))
}

func modelJSONKinds(value string, types map[string]ModelType, seen map[string]bool) []string {
	if seen[value] {
		return nil
	}

	seen[value] = true
	defer delete(seen, value)

	if strings.HasPrefix(value, "*") {
		return append(modelJSONKinds(value[1:], types, seen), "null")
	}

	if strings.HasPrefix(value, "[]") {
		return []string{"array"}
	}

	if strings.HasPrefix(value, "map[") || value == "struct{}" || value == "ytr.Fields" {
		return []string{"object"}
	}

	for _, wrapper := range []string{"Optional", "Update", "CollectionUpdate", "OneOrMany", "Replacement"} {
		if inner, ok := strings.CutPrefix(value, wrapper+"["); ok {
			kinds := modelJSONKinds(strings.TrimSuffix(inner, "]"), types, seen)

			switch wrapper {
			case "Optional":
				return append(kinds, "null")
			case "Update":
				return append(kinds, "object", "null")
			case "Replacement":
				return []string{"object"}
			case "CollectionUpdate":
				return []string{"array", "object", "null"}
			default:
				return append(kinds, "array", "null")
			}
		}
	}

	switch value {
	case "string", "ytr.EntityType":
		return []string{"string"}
	case "bool":
		return []string{"boolean"}
	case "int64", "json.Number":
		return []string{"number"}
	case "json.RawMessage":
		return []string{"any"}
	}

	model, exists := types[value]
	if !exists {
		return nil
	}

	if model.Kind == "alias" {
		return modelJSONKinds(model.Target, types, seen)
	}

	if model.Kind != "union" {
		return []string{"object"}
	}

	var result []string
	for _, variant := range model.Variants {
		result = append(result, modelJSONKinds(variant.Type, types, seen)...)
	}

	return result
}

func validateModelTypes(types []ModelType) (map[string]ModelType, error) {
	byName := make(map[string]ModelType, len(types))

	var problems []error

	for _, model := range types {
		if !exportedIdentifier(model.Name) || slices.Contains([]string{"Client", "Result", "New", "FromClient", "Optional", "Update", "CollectionUpdate", "Replacement", "OneOrMany", "Ptr", "Ref", "IDRef", "Number", "Text", "Value", "Null", "Replace", "Set", "One", "Many", "Collection"}, model.Name) {
			problems = append(problems, fmt.Errorf("type %q: invalid or reserved Go name", model.Name))
		}

		if _, exists := byName[model.Name]; exists {
			problems = append(problems, fmt.Errorf("duplicate type %s", model.Name))
		}

		byName[model.Name] = model
	}

	for _, model := range types {
		if err := validateModelType(model, byName); err != nil {
			problems = append(problems, fmt.Errorf("type %s: %w", model.Name, err))
		}
	}

	if err := validateValueCycles(byName); err != nil {
		problems = append(problems, err)
	}

	return byName, errors.Join(problems...)
}

func validateValueCycles(types map[string]ModelType) error {
	state := map[string]int{}

	var (
		visit      func(string) error
		expression func(ast.Expr) error
	)

	expression = func(node ast.Expr) error {
		switch value := node.(type) {
		case *ast.Ident:
			if _, exists := types[value.Name]; exists {
				return visit(value.Name)
			}
		case *ast.IndexExpr:
			if name, ok := value.X.(*ast.Ident); ok && slices.Contains([]string{"Optional", "Update", "Replacement"}, name.Name) {
				return expression(value.Index)
			}
		}

		return nil
	}
	visit = func(name string) error {
		if state[name] == 1 {
			return fmt.Errorf("type %s: recursive value cycle requires pointer/slice/map indirection", name)
		}

		if state[name] == 2 {
			return nil
		}

		state[name] = 1
		model := types[name]

		var values []string
		if model.Kind == "alias" {
			values = append(values, model.Target)
		}

		for _, field := range model.Fields {
			values = append(values, field.Type)
		}

		for _, value := range values {
			node, err := parser.ParseExpr(value)
			if err == nil {
				if err := expression(node); err != nil {
					return err
				}
			}
		}

		state[name] = 2

		return nil
	}

	names := make([]string, 0, len(types))
	for name := range types {
		names = append(names, name)
	}

	slices.Sort(names)

	for _, name := range names {
		if err := visit(name); err != nil {
			return err
		}
	}

	return nil
}

func validateModelType(model ModelType, types map[string]ModelType) error {
	switch model.Kind {
	case "alias":
		return validateModelAlias(model, types)
	case "union":
		return validateModelUnion(model, types)
	case "struct", "request":
		if model.Target != "" || len(model.Variants) != 0 || model.Discriminator != "" || (model.Kind == "request" && model.Open) {
			return errors.New("object has alias/union properties")
		}
	default:
		return fmt.Errorf("unknown kind %q", model.Kind)
	}

	names, keys := map[string]bool{}, map[string]bool{}
	for _, field := range model.Fields {
		if !exportedIdentifier(field.Name) || names[field.Name] || (model.Open && slices.Contains([]string{"AdditionalFields", "MarshalJSON", "UnmarshalJSON"}, field.Name)) {
			return fmt.Errorf("field %q: invalid or duplicate Go name", field.Name)
		}

		names[field.Name] = true
		if err := validateTypeExpression(field.Type, types); err != nil {
			return fmt.Errorf("field %s: %w", field.Name, err)
		}

		if model.Kind == "request" {
			if field.JSON != "" || field.Optional || field.OmitEmpty {
				return fmt.Errorf("field %s: request parameters must use operation bindings", field.Name)
			}

			continue
		}

		if !validJSONFieldName(field.JSON) || keys[field.JSON] {
			return fmt.Errorf("field %s: invalid or duplicate JSON name %q", field.Name, field.JSON)
		}

		keys[field.JSON] = true
		if field.Optional != strings.HasPrefix(field.Type, "Optional[") || (field.Optional && field.OmitEmpty) {
			return fmt.Errorf("field %s: optional presence disagrees with type or omitempty", field.Name)
		}
	}

	return nil
}

func validateModelAlias(model ModelType, types map[string]ModelType) error {
	if len(model.Fields) != 0 || len(model.Variants) != 0 || model.Open || model.Discriminator != "" {
		return errors.New("alias has object/union properties")
	}

	if !slices.Contains([]string{"string", "bool", "int64"}, model.Target) && !strings.HasPrefix(model.Target, "[]") && !strings.HasPrefix(model.Target, "map[string]") {
		return errors.New("alias target must be a scalar, slice or string-keyed map; custom codecs require an explicit model")
	}

	return validateTypeExpression(model.Target, types)
}

func validateModelUnion(model ModelType, types map[string]ModelType) error {
	if len(model.Variants) < 2 || len(model.Fields) != 0 || model.Target != "" || model.Open {
		return errors.New("union needs at least two variants and no object/alias properties")
	}

	names, tags := map[string]bool{}, map[string]bool{}
	for _, variant := range model.Variants {
		if !exportedIdentifier(variant.Name) || names[variant.Name] || slices.Contains([]string{"MarshalJSON", "UnmarshalJSON"}, variant.Name) {
			return fmt.Errorf("variant %q: invalid or duplicate name", variant.Name)
		}

		names[variant.Name] = true
		if err := validateModelVariant(model, variant, types); err != nil {
			return fmt.Errorf("variant %s: %w", variant.Name, err)
		}

		for _, tag := range variant.Tags {
			if tags[tag] || tag == "" {
				return fmt.Errorf("variant %s: empty or duplicate discriminator tag %q", variant.Name, tag)
			}

			tags[tag] = true
		}
	}

	return nil
}

func validateModelVariant(model ModelType, variant ModelVariant, types map[string]ModelType) error {
	if err := validateTypeExpression(variant.Type, types); err != nil {
		return err
	}

	if !slices.Contains([]string{"object", "array", "string", "number", "boolean", "any"}, variant.JSONKind) {
		return fmt.Errorf("unsupported JSON kind %q", variant.JSONKind)
	}

	kinds := ModelJSONKinds(variant.Type, types)
	if variant.JSONKind != "any" && !slices.Contains(kinds, variant.JSONKind) && !slices.Contains(kinds, "any") {
		return fmt.Errorf("JSON kind %s disagrees with %s", variant.JSONKind, variant.Type)
	}

	if err := validateVariantDiscriminator(model, variant, types); err != nil {
		return err
	}

	member, exists := types[variant.Type]
	if exists && member.Kind == "struct" {
		fields := map[string]bool{}
		for _, field := range member.Fields {
			fields[field.JSON] = true
		}

		for _, key := range variant.Keys {
			if !fields[key] {
				return fmt.Errorf("unknown distinguishing key %s", key)
			}
		}
	}

	return nil
}

func validateVariantDiscriminator(model ModelType, variant ModelVariant, types map[string]ModelType) error {
	if model.Discriminator == "" {
		return nil
	}

	if variant.JSONKind != "object" || len(variant.Tags) == 0 {
		return errors.New("discriminator requires tagged objects")
	}

	for _, field := range types[variant.Type].Fields {
		if field.JSON == model.Discriminator && slices.Contains(ModelJSONKinds(field.Type, types), "string") {
			return nil
		}
	}

	return fmt.Errorf("missing string discriminator field %s", model.Discriminator)
}

func validateTypeExpression(value string, types map[string]ModelType) error {
	if strings.IndexFunc(value, unicode.IsSpace) >= 0 {
		return fmt.Errorf("type %q must use compact spelling without whitespace", value)
	}

	expression, err := parser.ParseExpr(value)
	if err != nil {
		return fmt.Errorf("invalid type %q: %w", value, err)
	}

	return validateTypeNode(expression, types)
}

func validateTypeNode(expression ast.Expr, types map[string]ModelType) error {
	switch node := expression.(type) {
	case *ast.Ident:
		if model, exists := types[node.Name]; exists {
			if model.Kind == "request" {
				return errors.New("request container cannot be used as a JSON value type")
			}

			return nil
		}

		if slices.Contains([]string{"string", "bool", "int64"}, node.Name) {
			return nil
		}

		return fmt.Errorf("unknown type %s", node.Name)
	case *ast.SelectorExpr:
		prefix, ok := node.X.(*ast.Ident)
		if ok && slices.Contains([]string{"json.Number", "json.RawMessage", "ytr.EntityType", "ytr.Fields", "ytr.File"}, prefix.Name+"."+node.Sel.Name) {
			return nil
		}
	case *ast.StarExpr:
		return validateTypeNode(node.X, types)
	case *ast.ArrayType:
		if node.Len == nil {
			return validateTypeNode(node.Elt, types)
		}
	case *ast.MapType:
		key, ok := node.Key.(*ast.Ident)
		if ok && key.Name == "string" {
			return validateTypeNode(node.Value, types)
		}
	case *ast.IndexExpr:
		name, ok := node.X.(*ast.Ident)
		if ok && slices.Contains([]string{"Optional", "Update", "CollectionUpdate", "OneOrMany", "Replacement"}, name.Name) {
			return validateTypeNode(node.Index, types)
		}
	case *ast.StructType:
		if len(node.Fields.List) == 0 {
			return nil
		}
	}

	return errors.New("unsupported type expression")
}

func exportedIdentifier(name string) bool {
	return token.IsIdentifier(name) && ast.IsExported(name)
}

func validJSONFieldName(name string) bool {
	if name == "" || name == "-" {
		return false
	}
	// Match encoding/json's tag-name grammar; rejected names otherwise silently
	// fall back to the Go field name even when the generated file compiles.
	for _, ch := range name {
		if !unicode.IsLetter(ch) && !unicode.IsDigit(ch) && !strings.ContainsRune("!#$%&()*+-./:;<=>?@[]^_{|}~ ", ch) {
			return false
		}
	}

	return true
}

func validateOperationBindings(model ModelOperation, types map[string]ModelType) error {
	request, exists := types[model.Request]
	if !exists || request.Kind != "request" {
		return fmt.Errorf("request %q is not a declared request type", model.Request)
	}

	fields := make(map[string]string, len(request.Fields))
	for _, field := range request.Fields {
		fields[field.Name] = field.Type
	}

	used := map[string]bool{}

	bind := func(name, want string) error {
		if fields[name] != want {
			return fmt.Errorf("binding %s: request field has type %q, want %q", name, fields[name], want)
		}

		used[name] = true

		return nil
	}
	for _, path := range model.Paths {
		if err := bind(path.Name, path.Type); err != nil {
			return err
		}
	}

	if err := bindModelQueries(model.Queries, bind); err != nil {
		return err
	}

	for _, header := range model.Headers {
		if header.Key == "" {
			return errors.New("header binding has no key")
		}

		if err := bind(header.Field, "*string"); err != nil {
			return err
		}
	}

	if err := bindModelBody(model, bind); err != nil {
		return err
	}

	for _, field := range request.Fields {
		if !used[field.Name] {
			return fmt.Errorf("request field %s has no transport binding", field.Name)
		}
	}

	return validateModelResponse(model, types)
}

func validateModelResponse(model ModelOperation, types map[string]ModelType) error {
	if model.Stream {
		if model.Response != "" || model.AllowEmpty {
			return errors.New("stream operation has JSON response settings")
		}

		return nil
	}

	if err := validateTypeExpression(model.Response, types); err != nil {
		return fmt.Errorf("response: %w", err)
	}

	if model.Response == "struct{}" && !model.AllowEmpty {
		return errors.New("bodyless response does not allow an empty body")
	}

	return nil
}

func bindModelQueries(queries []ModelQuery, bind func(string, string) error) error {
	for _, query := range queries {
		if query.Key == "" || !slices.Contains([]string{"string", "bool", "int64", "[]string"}, query.Type) || query.CSV != (query.Type == "[]string") || (query.CSV && query.Optional) {
			return fmt.Errorf("query %s: unsupported key/type/serialization", query.Field)
		}

		want := query.Type
		if query.Optional {
			want = "*" + want
		}

		if err := bind(query.Field, want); err != nil {
			return err
		}
	}

	return nil
}

func bindModelBody(model ModelOperation, bind func(string, string) error) error {
	if model.Encoding == "json" {
		if model.Body == "" {
			return errors.New("JSON operation has no body binding")
		}

		if err := bind("Body", model.Body); err != nil {
			return err
		}
	} else if model.Body != "" || model.Extras {
		return errors.New("non-JSON operation has JSON body/extension bindings")
	}

	if model.Encoding == "multipart" {
		if err := bind("File", "ytr.File"); err != nil {
			return err
		}
	}

	if model.Extras {
		if err := bind("ExtraFields", "ytr.Fields"); err != nil {
			return err
		}
	}

	return nil
}
