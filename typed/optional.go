package typed

import (
	"bytes"
	"encoding/json"
)

// Optional distinguishes omitted, explicit null and an explicitly supplied value.
// Generated request fields use omitzero, so false, zero and empty slices survive.
type Optional[T any] struct {
	value   T
	present bool
	null    bool
}

// Value constructs an explicitly supplied value.
func Value[T any](value T) Optional[T] { return Optional[T]{value: value, present: true} }

// Null constructs an explicit JSON null, for example to clear an update field.
func Null[T any]() Optional[T] { return Optional[T]{present: true, null: true} }

// Get returns a supplied non-null value. Its boolean is false for omitted/null.
func (o Optional[T]) Get() (T, bool) { return o.value, o.present && !o.null }

// IsSet reports whether the field was explicitly supplied.
func (o Optional[T]) IsSet() bool { return o.present }

// IsNull reports an explicit JSON null.
func (o Optional[T]) IsNull() bool { return o.present && o.null }

// IsZero lets encoding/json omit only absent fields.
func (o Optional[T]) IsZero() bool { return !o.present }

// MarshalJSON encodes a supplied value or null. Omission belongs to the parent.
func (o Optional[T]) MarshalJSON() ([]byte, error) {
	if !o.present || o.null {
		return []byte("null"), nil
	}

	return json.Marshal(o.value)
}

// UnmarshalJSON replaces the optional value only after successful decoding.
func (o *Optional[T]) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		o.SetNull()
		return nil
	}

	var next T
	if err := decodeValueJSON(data, &next); err != nil {
		return err
	}

	o.Set(next)

	return nil
}

// Set replaces the field with an explicit non-null value.
func (o *Optional[T]) Set(value T) { *o = Value(value) }

// SetNull replaces the field with explicit null.
func (o *Optional[T]) SetNull() { *o = Null[T]() }

// Unset removes the field from the next request.
func (o *Optional[T]) Unset() { *o = Optional[T]{} }
