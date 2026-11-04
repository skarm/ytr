package typed

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

func decodeOpenObject(data []byte, field func(string, *json.Decoder) (bool, error)) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()

	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}

	if token == nil {
		return nil, nil
	}

	if token != json.Delim('{') {
		return nil, errors.New("ytr/typed: expected object")
	}

	extra, err := decodeObjectFields(decoder, field)
	if err != nil {
		return nil, err
	}

	if _, err := decoder.Token(); err != nil {
		return nil, err
	}

	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("ytr/typed: trailing JSON")
	}

	return extra, nil
}

func decodeObjectFields(decoder *json.Decoder, field func(string, *json.Decoder) (bool, error)) (map[string]json.RawMessage, error) {
	var extra map[string]json.RawMessage

	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}

		key, ok := token.(string)
		if !ok {
			return nil, errors.New("ytr/typed: invalid object key")
		}

		handled, err := field(key, decoder)
		if err != nil {
			return nil, err
		}

		if handled {
			continue
		}

		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}

		if extra == nil {
			extra = make(map[string]json.RawMessage)
		}

		extra[key] = value
	}

	return extra, nil
}

// Decode a field-sized temporary so the complete open DTO stays on the stack.
// Starting with the current value preserves encoding/json's duplicate-member
// semantics for null scalars, maps, slices and partially specified pointers.
func decodeOpenField[T any](decoder *json.Decoder, value T) (T, error) {
	err := decoder.Decode(&value)
	return value, err
}
