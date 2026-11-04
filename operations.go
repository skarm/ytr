package ytr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const (
	Project   EntityType = "project"
	Portfolio EntityType = "portfolio"
	Goal      EntityType = "goal"
)

var (
	// ErrUnknownOperation indicates that an operation name is absent from the catalog.
	ErrUnknownOperation = errors.New("ytr: unknown operation")
	// ErrOperationKind indicates that the selected executor cannot handle an operation.
	ErrOperationKind = errors.New("ytr: incompatible operation kind")
)

// EntityType selects project, portfolio, or goal resources in the entities API.
type EntityType string

// OperationName identifies one documented HTTP operation in the catalog.
// The Op constants are accepted by LookupOperation and Client.Call.
type OperationName string

// Operation describes one documented HTTP operation. Request is none, json or
// multipart; Response is json or stream. Documentation links its source page.
type Operation struct {
	Name          OperationName `json:"name"`
	Method        string        `json:"method"`
	Path          string        `json:"path"`
	Request       string        `json:"request"`
	Response      string        `json:"response"`
	Documentation string        `json:"documentation"`
}

// Operations returns a copy of the complete published operation catalog.
func Operations() []Operation { return append([]Operation(nil), operations...) }

// LookupOperation finds an operation for dynamic adapters such as CLIs.
// The returned value does not expose mutable client state.
func LookupOperation(name OperationName) (Operation, error) {
	if index, ok := operationIndex[name]; ok {
		return operations[index], nil
	}

	return Operation{}, fmt.Errorf("%w: %q", ErrUnknownOperation, name)
}

// PathParameters returns the names of positional path arguments, in order.
// The returned slice belongs to the caller.
func (o Operation) PathParameters() []string {
	var names []string

	for part := range strings.SplitSeq(o.Path, "/") {
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			names = append(names, part[1:len(part)-1])
		}
	}

	return names
}

// Call executes a named JSON or bodyless operation. Path arguments follow
// Operation.PathParameters order. Use Upload for multipart and Download for
// streaming responses. Generated, named methods use this same entry point.
func (c *Client) Call(ctx context.Context, name OperationName, args []string, body any, options ...CallOption) (*Response, error) {
	operation, err := LookupOperation(name)
	if err != nil {
		return nil, err
	}

	return c.call(ctx, operation, args, body, options...)
}

// CallInto decodes a JSON operation directly into out without retaining its raw
// body. out must be a non-nil pointer. It preserves JSON numbers in interface
// values and returns HTTP metadata separately. An empty body returns io.EOF.
func (c *Client) CallInto(ctx context.Context, name OperationName, args []string, body, out any, options ...CallOption) (*ResponseMetadata, error) {
	operation, err := LookupOperation(name)
	if err != nil {
		return nil, err
	}

	if err = validateDecodeTarget(out); err != nil {
		return nil, err
	}

	req, err := c.callRequest(ctx, operation, args, body, options...)
	if err != nil {
		return nil, err
	}

	raw, err := c.send(req)
	if err != nil {
		return nil, err
	}

	return c.readInto(raw, out)
}

func (c *Client) call(ctx context.Context, operation Operation, args []string, body any, options ...CallOption) (*Response, error) {
	req, err := c.callRequest(ctx, operation, args, body, options...)
	if err != nil {
		return nil, err
	}

	raw, err := c.send(req)
	if err != nil {
		return nil, err
	}

	return c.readResponse(raw)
}

func (c *Client) callRequest(ctx context.Context, operation Operation, args []string, body any, options ...CallOption) (*http.Request, error) {
	if operation.Response != "json" || operation.Request == "multipart" {
		return nil, fmt.Errorf("%w: %s cannot use Call", ErrOperationKind, operation.Name)
	}

	if operation.Request == "none" && body != nil {
		return nil, fmt.Errorf("%w: %s does not accept a request body", ErrOperationKind, operation.Name)
	}

	path, err := renderPath(operation.Path, args)
	if err != nil {
		return nil, err
	}

	opts, err := optionsFor(options)
	if err != nil {
		return nil, err
	}

	data, err := encodeJSON(body)
	if err != nil {
		return nil, err
	}

	return c.newRequest(ctx, operation.Method, path, bytes.NewReader(data), "application/json", opts)
}

func (c *Client) jsonRequest(ctx context.Context, method, path string, data []byte, options callOptions) (*Response, error) {
	req, err := c.newRequest(ctx, method, path, bytes.NewReader(data), "application/json", options)
	if err != nil {
		return nil, err
	}

	response, err := c.send(req)
	if err != nil {
		return nil, err
	}

	return c.readResponse(response)
}

func renderPath(template string, args []string) (string, error) {
	parts := strings.Split(template, "/")
	index := 0

	for i, part := range parts {
		if !strings.HasPrefix(part, "{") {
			continue
		}

		if index >= len(args) {
			return "", errors.New("ytr: missing path argument")
		}

		if err := validatePathArgument(part, args[index]); err != nil {
			return "", err
		}

		parts[i] = url.PathEscape(args[index])
		index++
	}

	if index != len(args) {
		return "", errors.New("ytr: too many path arguments")
	}

	return strings.Join(parts, "/"), nil
}

func validatePathArgument(name, value string) error {
	if value == "" || value == "." || value == ".." {
		return fmt.Errorf("ytr: invalid %s path argument", name)
	}

	if name == "{entityType}" && value != string(Project) && value != string(Portfolio) && value != string(Goal) {
		return errors.New("ytr: entity type must be project, portfolio or goal")
	}

	return nil
}

func encodeJSON(body any) ([]byte, error) {
	if body == nil {
		return nil, nil
	}

	result, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("ytr: encode JSON: %w", err)
	}

	return result, nil
}
