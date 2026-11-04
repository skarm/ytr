package typed

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
)

type unionChoice struct {
	Value any
	Kind  string
	Tags  []string
	Keys  []string
}

func marshalUnion(discriminator string, choices []unionChoice) ([]byte, error) {
	var selected *unionChoice

	for i := range choices {
		if reflect.ValueOf(choices[i].Value).IsNil() {
			continue
		}

		if selected != nil {
			return nil, errors.New("ytr/typed: more than one union variant selected")
		}

		selected = &choices[i]
	}

	if selected == nil {
		return []byte("null"), nil
	}

	data, err := json.Marshal(selected.Value)
	if err != nil {
		return nil, err
	}

	if err := checkDiscriminator(data, discriminator, selected.Tags); err != nil {
		return nil, err
	}

	return data, nil
}

func unmarshalUnion(data []byte, discriminator string, choices []unionChoice) error {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) {
		return nil
	}

	if !json.Valid(data) {
		return errors.New("ytr/typed: invalid union JSON")
	}

	kind := jsonKind(data[0])

	// Generated callers supply a fresh choice slice for this decode.
	// Compact it in place; no separate candidate buffer needs to escape.
	candidates := choices[:0]
	for _, choice := range choices {
		if choice.Kind == kind || choice.Kind == "any" {
			candidates = append(candidates, choice)
		}
	}

	choice, err := selectUnion(data, discriminator, candidates)
	if err != nil {
		return err
	}

	return decodeValueJSON(data, choice.Value)
}

func selectUnion(data []byte, discriminator string, candidates []unionChoice) (unionChoice, error) {
	if len(candidates) == 1 {
		return candidates[0], checkDiscriminator(data, discriminator, candidates[0].Tags)
	}

	if len(candidates) == 0 {
		return unionChoice{}, errors.New("ytr/typed: JSON type does not match union")
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return unionChoice{}, err
	}

	if discriminator != "" {
		return selectTaggedUnion(object, discriminator, candidates)
	}

	return selectObjectUnion(object, candidates)
}

func selectTaggedUnion(object map[string]json.RawMessage, discriminator string, candidates []unionChoice) (unionChoice, error) {
	var tag string
	if err := json.Unmarshal(object[discriminator], &tag); err != nil {
		return unionChoice{}, err
	}

	for _, choice := range candidates {
		if slices.Contains(choice.Tags, tag) {
			return choice, nil
		}
	}

	return unionChoice{}, fmt.Errorf("ytr/typed: unknown %s discriminator %q", discriminator, tag)
}

func selectObjectUnion(object map[string]json.RawMessage, candidates []unionChoice) (unionChoice, error) {
	best, score, ties := -1, -1, 0

	for i, candidate := range candidates {
		found := 0

		for _, key := range candidate.Keys {
			if _, ok := object[key]; ok {
				found++
			}
		}

		if found > score {
			best, score, ties = i, found, 1
		} else if found == score {
			ties++
		}
	}

	if ties != 1 {
		return unionChoice{}, errors.New("ytr/typed: ambiguous object union")
	}

	return candidates[best], nil
}

func checkDiscriminator(data []byte, discriminator string, tags []string) error {
	if discriminator == "" || len(tags) == 0 {
		return nil
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}

	var tag string
	if err := json.Unmarshal(object[discriminator], &tag); err != nil {
		return err
	}

	if !slices.Contains(tags, tag) {
		return fmt.Errorf("ytr/typed: invalid %s discriminator %q", discriminator, tag)
	}

	return nil
}

func jsonKind(first byte) string {
	switch first {
	case '{':
		return "object"
	case '[':
		return "array"
	case '"':
		return "string"
	case 't', 'f':
		return "boolean"
	default:
		return "number"
	}
}
