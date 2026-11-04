// Package typed provides documented Tracker request and response models on the
// shared ytr transport. Methods decode responses without retaining a raw copy.
package typed

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/skarm/ytr"
)

// Client uses the authentication, limits, cancellation and HTTP transport of ytr.
type Client struct{ raw *ytr.Client }

// Result contains the decoded data and response metadata, including pagination
// headers. On failure Data is zero; metadata remains available when received.
type Result[T any] struct {
	ytr.ResponseMetadata
	Data T
}

// New creates a typed SDK with the same configuration as the low-level client.
func New(config ytr.Config) (*Client, error) {
	raw, err := ytr.New(config)
	if err != nil {
		return nil, err
	}

	return FromClient(raw)
}

// FromClient shares an existing client, including its connection pool.
func FromClient(raw *ytr.Client) (*Client, error) {
	if raw == nil {
		return nil, errors.New("ytr/typed: nil client")
	}

	return &Client{raw: raw}, nil
}

// Raw returns the underlying client for application-specific models or endpoints.
func (c *Client) Raw() *ytr.Client { return c.raw }

// Ptr returns a pointer for an explicitly present query parameter or union arm.
func Ptr[T any](value T) *T { return new(value) }

// Ref selects a queue/type/priority by key instead of numeric ID or object.
func Ref(key string) Reference { return Reference{String: &key} }

// IDRef selects an assignee/follower by login or string ID.
func IDRef(id string) IDInput { return IDInput{String: &id} }

// Number constructs a number arm without floating-point conversion.
func Number(value json.Number) StringOrNumber { return StringOrNumber{Number: &value} }

// Text constructs a string arm while preserving its JSON representation.
func Text(value string) StringOrNumber { return StringOrNumber{String: &value} }

func call[T any](ctx context.Context, raw *ytr.Client, operation ytr.OperationName, args []string, body any, allowEmpty bool, options ...ytr.CallOption) (*Result[T], error) {
	value := new(Result[T])

	meta, err := raw.CallInto(ctx, operation, args, body, &value.Data, options...)
	if allowEmpty && err == io.EOF { //nolint:errorlint // Only an empty successful body is EOF; wrapped read failures must remain errors.
		err = nil
	}

	return result(value, meta, err)
}

func upload[T any](ctx context.Context, raw *ytr.Client, operation ytr.OperationName, args []string, file ytr.File, options ...ytr.CallOption) (*Result[T], error) {
	value := new(Result[T])

	meta, err := raw.UploadInto(ctx, operation, args, file, &value.Data, options...)

	return result(value, meta, err)
}

func result[T any](value *Result[T], meta *ytr.ResponseMetadata, err error) (*Result[T], error) {
	if meta == nil {
		return nil, err
	}

	if err != nil {
		var zero T

		value.Data = zero
	}

	value.ResponseMetadata = *meta

	return value, err
}

// Reuse the transport decoder's number-preserving behavior for dynamic values
// nested inside SDK codecs. Concrete types retain its json.Unmarshal fast path.
func decodeValueJSON(data []byte, out any) error {
	return (&ytr.Response{Body: data}).Decode(out)
}
