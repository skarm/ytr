package ytr

import (
	"bytes"
	"encoding"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sync"
)

// Type decisions are immutable; the shared cache contains Go type metadata only.
var directDecodeTypes sync.Map //nolint:gochecknoglobals // Concurrency-safe cache of immutable Go type decisions.

type responseReadTracker struct {
	reader io.Reader
	err    error
}

// Decode converts an operation result directly to the application's model:
//
//	issue, err := ytr.Decode[Issue](client.GetIssue(ctx, "SDK-1"))
//
// It preserves the original call error and returns the zero value on failure.
// Use Response.Decode directly when HTTP status or headers are also needed.
func Decode[T any](response *Response, callErr error) (T, error) {
	var value T
	if callErr != nil {
		return value, callErr
	}

	if response == nil {
		return value, ErrInvalidResponse
	}

	if err := response.Decode(&value); err != nil {
		var zero T
		return zero, err
	}

	return value, nil
}

func (r *responseReadTracker) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}

	n, err := r.reader.Read(p)
	r.err = err

	return n, err
}

func validateDecodeTarget(out any) error {
	value := reflect.ValueOf(out)
	if !value.IsValid() || value.Kind() != reflect.Pointer || value.IsNil() {
		return &json.InvalidUnmarshalError{Type: reflect.TypeOf(out)}
	}

	return nil
}

func decodeJSONBytes(body []byte, out any) error {
	if canDecodeDirectly(reflect.TypeOf(out)) {
		err := json.Unmarshal(body, out)

		if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
			return err
		}
	}

	return decodeJSONReader(bytes.NewReader(body), out)
}

func decodeJSONReader(reader io.Reader, out any) error {
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()

	if err := decoder.Decode(out); err != nil {
		return err
	}

	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ErrInvalidResponse
	}

	return nil
}

func decodeLimitedJSON(reader io.Reader, limit int64, out any) error {
	tracked := responseReadTracker{reader: reader}
	limited := io.LimitedReader{R: &tracked, N: limit + 1}
	decodeErr := decodeJSONReader(&limited, out)

	_, readErr := io.Copy(io.Discard, &limited)
	if tracked.err != nil && tracked.err != io.EOF { //nolint:errorlint // io.Reader reserves exact io.EOF; a wrapped EOF remains a read failure.
		readErr = tracked.err
	}

	if limited.N == 0 {
		return errors.Join(ErrResponseTooLarge, readErr)
	}

	if readErr != nil {
		return readErr
	}

	return responseDecodeError(decodeErr)
}

func responseDecodeError(err error) error {
	var syntaxError *json.SyntaxError
	if errors.As(err, &syntaxError) || errors.Is(err, io.ErrUnexpectedEOF) {
		return errors.Join(ErrInvalidResponse, err)
	}

	return err
}

func canDecodeDirectly(t reflect.Type) bool {
	if t == nil {
		return false
	}

	if cached, ok := directDecodeTypes.Load(t); ok {
		return cached == true
	}

	direct := !needsNumberDecoder(t, make(map[reflect.Type]bool))
	directDecodeTypes.Store(t, direct)

	return direct
}

func needsNumberDecoder(t reflect.Type, seen map[reflect.Type]bool) bool {
	if seen[t] {
		return false
	}

	seen[t] = true
	if t.Kind() == reflect.Interface || hasCustomUnmarshaler(t) {
		return true
	}

	switch t.Kind() {
	case reflect.Pointer, reflect.Array, reflect.Slice:
		return needsNumberDecoder(t.Elem(), seen)
	case reflect.Map:
		return needsNumberDecoder(t.Key(), seen) || needsNumberDecoder(t.Elem(), seen)
	case reflect.Struct:
		return structNeedsNumberDecoder(t, seen)
	default:
		return false
	}
}

func structNeedsNumberDecoder(t reflect.Type, seen map[reflect.Type]bool) bool {
	for field := range t.Fields() {
		if needsNumberDecoder(field.Type, seen) {
			return true
		}
	}

	return false
}

func hasCustomUnmarshaler(t reflect.Type) bool {
	jsonType := reflect.TypeFor[json.Unmarshaler]()

	textType := reflect.TypeFor[encoding.TextUnmarshaler]()
	if t.Implements(jsonType) || t.Implements(textType) {
		return true
	}

	if t.Kind() == reflect.Pointer {
		return false
	}

	pointer := reflect.PointerTo(t)

	return pointer.Implements(jsonType) || pointer.Implements(textType)
}
