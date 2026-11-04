package ytr

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"time"
)

var (
	// ErrResponseTooLarge indicates that a buffered response exceeded MaxResponseBytes.
	ErrResponseTooLarge = errors.New("ytr: response exceeds configured size limit")
	// ErrInvalidResponse indicates missing or invalid JSON response data.
	ErrInvalidResponse = errors.New("ytr: invalid JSON response")
	// ErrPaginationCycle indicates that a page traversal encountered the same page again.
	ErrPaginationCycle = errors.New("ytr: pagination cycle")
	// ErrUnsafeLink indicates that a next-page link leaves the requested origin or resource.
	ErrUnsafeLink = errors.New("ytr: pagination link leaves the requested resource")
	// ErrStopPagination can be returned by a visitor to finish successfully.
	ErrStopPagination = errors.New("ytr: stop pagination")
)

// Fields is an open JSON object. A nil value encodes JSON null; false, zero,
// empty strings and empty slices are transmitted without omitting them.
type Fields map[string]any

// Response owns a fully buffered response body. No Close call is needed.
type Response struct {
	StatusCode int
	Header     http.Header
	Body       json.RawMessage
}

// ResponseMetadata contains HTTP status and headers after decoding directly
// into a model. The response body has been consumed and closed.
type ResponseMetadata struct {
	StatusCode int
	Header     http.Header
}

// Download owns an unbuffered response stream. The caller must close Body,
// including when it decides not to consume the complete file.
type Download struct {
	StatusCode int
	Header     http.Header
	Body       io.ReadCloser
}

// APIError retains structured details without including server-controlled
// content or credentials in Error(). Body may be truncated at MaxResponseBytes.
type APIError struct {
	StatusCode    int
	Header        http.Header
	Body          json.RawMessage
	ErrorMessages []string
	Errors        map[string]json.RawMessage
	Truncated     bool
	Cause         error
}

// Decode decodes into the application's model. JSON numbers decoded into
// interface values use json.Number, preserving IDs larger than 2^53. An empty
// response (for example 204) returns io.EOF.
func (r *Response) Decode(out any) error {
	return decodeJSONBytes(r.Body, out)
}

// Error reports the HTTP status without including response content or credentials.
func (e *APIError) Error() string { return fmt.Sprintf("ytr: Tracker returned HTTP %d", e.StatusCode) }

// Unwrap preserves a failure while reading the error response body.
func (e *APIError) Unwrap() error { return e.Cause }

// RetryAfter parses either form of the HTTP Retry-After header. No request is
// automatically retried. now is explicit to make scheduling deterministic.
func (e *APIError) RetryAfter(now time.Time) (time.Duration, bool) {
	raw := e.Header.Get("Retry-After")
	if seconds, err := strconv.ParseUint(raw, 10, 32); err == nil {
		return time.Duration(seconds) * time.Second, true
	}

	date, err := http.ParseTime(raw)
	if err != nil {
		return 0, false
	}

	return max(time.Duration(0), date.Sub(now)), true
}

func (c *Client) readResponse(raw *http.Response) (*Response, error) {
	defer raw.Body.Close() //nolint:errcheck // Close releases the stream; read errors are returned separately.
	return c.bufferedResponse(raw)
}

func (c *Client) readInto(raw *http.Response, out any) (*ResponseMetadata, error) {
	defer raw.Body.Close() //nolint:errcheck // Close releases the stream; read errors are returned separately.

	if raw.StatusCode < 200 || raw.StatusCode >= 300 {
		response, err := c.bufferedResponse(raw)
		return &ResponseMetadata{StatusCode: response.StatusCode, Header: response.Header}, err
	}

	meta := &ResponseMetadata{StatusCode: raw.StatusCode, Header: raw.Header.Clone()}

	return meta, c.decodeInto(raw, out)
}

func (c *Client) bufferedResponse(raw *http.Response) (*Response, error) {
	body, readErr := readLimited(raw.Body, c.responseLimit, raw.ContentLength)

	response := &Response{StatusCode: raw.StatusCode, Header: raw.Header.Clone(), Body: body}
	if raw.StatusCode < 200 || raw.StatusCode >= 300 {
		return response, apiError(response, readErr)
	}

	if readErr != nil {
		return response, readErr
	}

	if len(bytes.Trim(body, " \t\r\n")) > 0 && !json.Valid(body) {
		return response, ErrInvalidResponse
	}

	return response, nil
}

func (c *Client) decodeInto(raw *http.Response, out any) error {
	if raw.ContentLength <= 0 || raw.ContentLength > c.responseLimit || raw.ContentLength > defaultResponseLimit || !canDecodeDirectly(reflect.TypeOf(out)) {
		return decodeLimitedJSON(raw.Body, c.responseLimit, out)
	}

	body, err := readLimited(raw.Body, c.responseLimit, raw.ContentLength)
	if err != nil {
		return err
	}

	return responseDecodeError(decodeJSONBytes(body, out))
}

func apiError(response *Response, cause error) *APIError {
	result := &APIError{StatusCode: response.StatusCode, Header: response.Header.Clone(), Body: append(json.RawMessage(nil), response.Body...), Truncated: errors.Is(cause, ErrResponseTooLarge), Cause: cause}

	var details struct {
		ErrorMessages []string                   `json:"errorMessages"`
		Errors        map[string]json.RawMessage `json:"errors"`
	}
	if json.Unmarshal(response.Body, &details) == nil {
		result.ErrorMessages = details.ErrorMessages
		result.Errors = details.Errors
	}

	return result
}

func readLimited(reader io.Reader, limit, contentLength int64) ([]byte, error) {
	limited := io.LimitReader(reader, limit+1)

	body, err := readWithHint(limited, contentLength, limit)
	if int64(len(body)) > limit {
		return body[:limit], errors.Join(ErrResponseTooLarge, err)
	}

	return body, err
}

func readWithHint(reader io.Reader, length, limit int64) ([]byte, error) {
	if length <= 0 || length > limit || length > defaultResponseLimit {
		return io.ReadAll(reader)
	}

	var body bytes.Buffer
	body.Grow(int(length) + bytes.MinRead)
	_, err := body.ReadFrom(reader)

	return body.Bytes(), err
}
