package ytr

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
)

// File describes one multipart file. The caller owns Reader and any Close it
// requires. FieldName defaults to "file", matching the official Python client.
type File struct {
	Name      string
	Reader    io.Reader
	FieldName string
}

// Upload executes a named multipart operation. Reader ownership stays with the
// caller. Named upload methods use this same entry point.
func (c *Client) Upload(ctx context.Context, name OperationName, args []string, file File, options ...CallOption) (*Response, error) {
	operation, err := LookupOperation(name)
	if err != nil {
		return nil, err
	}

	return c.upload(ctx, operation, args, file, options...)
}

// UploadInto uploads a multipart file and decodes its JSON response directly
// into out. The caller retains ownership of file.Reader.
func (c *Client) UploadInto(ctx context.Context, name OperationName, args []string, file File, out any, options ...CallOption) (*ResponseMetadata, error) {
	operation, err := LookupOperation(name)
	if err != nil {
		return nil, err
	}

	if err = validateDecodeTarget(out); err != nil {
		return nil, err
	}

	req, err := c.uploadRequest(ctx, operation, args, file, options...)
	if err != nil {
		return nil, err
	}

	raw, err := c.send(req)
	if err != nil {
		return nil, err
	}

	return c.readInto(raw, out)
}

// Download executes a named streaming operation. The caller must close Body.
// Named download methods use this same entry point.
func (c *Client) Download(ctx context.Context, name OperationName, args []string, options ...CallOption) (*Download, error) {
	operation, err := LookupOperation(name)
	if err != nil {
		return nil, err
	}

	return c.download(ctx, operation, args, options...)
}

func (c *Client) upload(ctx context.Context, operation Operation, args []string, file File, options ...CallOption) (*Response, error) {
	req, err := c.uploadRequest(ctx, operation, args, file, options...)
	if err != nil {
		return nil, err
	}

	raw, err := c.send(req)
	if err != nil {
		return nil, err
	}

	return c.readResponse(raw)
}

func (c *Client) uploadRequest(ctx context.Context, operation Operation, args []string, file File, options ...CallOption) (*http.Request, error) {
	if operation.Request != "multipart" || operation.Response != "json" {
		return nil, fmt.Errorf("%w: %s cannot use Upload", ErrOperationKind, operation.Name)
	}

	path, err := renderPath(operation.Path, args)
	if err != nil {
		return nil, err
	}

	opts, err := optionsFor(options)
	if err != nil {
		return nil, err
	}

	body, contentType, err := multipartBody(file)
	if err != nil {
		return nil, err
	}

	return c.newRequest(ctx, operation.Method, path, body, contentType, opts)
}

func (c *Client) download(ctx context.Context, operation Operation, args []string, options ...CallOption) (*Download, error) {
	if operation.Response != "stream" {
		return nil, fmt.Errorf("%w: %s cannot use Download", ErrOperationKind, operation.Name)
	}

	path, err := renderPath(operation.Path, args)
	if err != nil {
		return nil, err
	}

	opts, err := optionsFor(options)
	if err != nil {
		return nil, err
	}

	req, err := c.newRequest(ctx, operation.Method, path, nil, "", opts)
	if err != nil {
		return nil, err
	}

	if _, supplied := opts.headers["Accept"]; !supplied {
		req.Header.Set("Accept", "*/*")
	}

	response, err := c.send(req)
	if err != nil {
		return nil, err
	}

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, err = c.readResponse(response)
		return nil, err
	}

	return &Download{StatusCode: response.StatusCode, Header: response.Header.Clone(), Body: response.Body}, nil
}

func multipartBody(file File) (io.Reader, string, error) {
	if file.Reader == nil || file.Name == "" || strings.ContainsAny(file.Name, "\r\n\x00") {
		return nil, "", errors.New("ytr: file name and reader are required")
	}

	field := file.FieldName
	if field == "" {
		field = "file"
	}

	if !validHeaderValue(field) {
		return nil, "", errors.New("ytr: invalid multipart field name")
	}

	var buffer bytes.Buffer

	writer := multipart.NewWriter(&buffer)
	if _, err := writer.CreateFormFile(field, file.Name); err != nil {
		return nil, "", err
	}

	prefix := append([]byte(nil), buffer.Bytes()...)
	buffer.Reset()

	if err := writer.Close(); err != nil {
		return nil, "", err
	}

	return io.MultiReader(bytes.NewReader(prefix), file.Reader, bytes.NewReader(buffer.Bytes())), writer.FormDataContentType(), nil
}
