package typed

import (
	"bytes"
	"encoding/json"
	"errors"
)

// Update accepts a direct value or the documented set operator.
// Use Optional around Update to distinguish omitted fields and explicit null.
type Update[T any] struct {
	Value *T          `json:"-"`
	Set   Optional[T] `json:"set,omitzero"`
}

// OneOrMany accepts one item or an array without changing its JSON shape.
type OneOrMany[T any] struct {
	One  *T
	Many *[]T
}

// Replacement replaces one existing collection item with another.
type Replacement[T any] struct {
	Target      T `json:"target"`
	Replacement T `json:"replacement"`
}

// CollectionUpdate accepts a direct array or set/add/remove/replace operators.
type CollectionUpdate[T any] struct {
	Value   *[]T                                `json:"-"`
	Set     Optional[[]T]                       `json:"set,omitzero"`
	Add     Optional[OneOrMany[T]]              `json:"add,omitzero"`
	Remove  Optional[OneOrMany[T]]              `json:"remove,omitzero"`
	Replace Optional[OneOrMany[Replacement[T]]] `json:"replace,omitzero"`
}

// Replace supplies a direct field value.
func Replace[T any](value T) Update[T] { return Update[T]{Value: &value} }

// Set supplies the explicit set operator.
func Set[T any](value T) Update[T] { return Update[T]{Set: Value(value)} }

// One supplies one item to a collection operator.
func One[T any](value T) OneOrMany[T] { return OneOrMany[T]{One: &value} }

// Many supplies an array of items to a collection operator.
func Many[T any](values []T) OneOrMany[T] { return OneOrMany[T]{Many: &values} }

// Collection supplies a replacement array, including an explicit empty array.
func Collection[T any](values []T) CollectionUpdate[T] { return CollectionUpdate[T]{Value: &values} }

// MarshalJSON writes a direct value or a set object, rejecting conflicting arms.
func (u Update[T]) MarshalJSON() ([]byte, error) {
	if u.Value != nil {
		if u.Set.IsSet() {
			return nil, errors.New("ytr/typed: conflicting update variants")
		}

		return json.Marshal(u.Value)
	}

	if !u.Set.IsSet() {
		return []byte("null"), nil
	}

	type operations Update[T]

	return json.Marshal(operations(u))
}

// UnmarshalJSON replaces the update only on successful decoding.
func (u *Update[T]) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		*u = Update[T]{}
		return nil
	}

	var object map[string]json.RawMessage
	if firstByte(data) == '{' {
		if err := json.Unmarshal(data, &object); err != nil {
			return err
		}

		if value, exists := object["set"]; exists {
			var next Optional[T]
			if err := decodeValueJSON(value, &next); err != nil {
				return err
			}

			*u = Update[T]{Set: next}

			return nil
		}
	}

	var next T
	if err := decodeValueJSON(data, &next); err != nil {
		return err
	}

	*u = Replace(next)

	return nil
}

// MarshalJSON preserves the one-item or array representation.
func (u OneOrMany[T]) MarshalJSON() ([]byte, error) {
	return marshalUnion("", []unionChoice{{Value: u.One}, {Value: u.Many}})
}

// UnmarshalJSON replaces the selected arm only after successful decoding.
func (u *OneOrMany[T]) UnmarshalJSON(data []byte) error {
	var (
		next   OneOrMany[T]
		target any = &next.One
	)
	if firstByte(data) == '[' {
		target = &next.Many
	}

	if err := decodeValueJSON(data, target); err != nil {
		return err
	}

	*u = next

	return nil
}

// MarshalJSON rejects simultaneous direct-array and operator forms.
func (u CollectionUpdate[T]) MarshalJSON() ([]byte, error) {
	if u.Value != nil {
		if u.Set.IsSet() || u.Add.IsSet() || u.Remove.IsSet() || u.Replace.IsSet() {
			return nil, errors.New("ytr/typed: conflicting collection update variants")
		}

		return json.Marshal(u.Value)
	}

	type operations CollectionUpdate[T]

	return json.Marshal(operations(u))
}

// UnmarshalJSON preserves direct arrays and operator objects.
func (u *CollectionUpdate[T]) UnmarshalJSON(data []byte) error {
	if firstByte(data) == '[' {
		var next []T
		if err := decodeValueJSON(data, &next); err != nil {
			return err
		}

		*u = Collection(next)

		return nil
	}

	type operations CollectionUpdate[T]

	var next operations
	if err := decodeValueJSON(data, &next); err != nil {
		return err
	}

	*u = CollectionUpdate[T](next)

	return nil
}

func firstByte(data []byte) byte {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return 0
	}

	return data[0]
}
