package ytr

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// CallOption configures one call without changing shared client state.
type (
	CallOption func(*callOptions) error

	callOptions struct {
		query   url.Values
		headers http.Header
	}
)

// Query sets all values of one query parameter, including repeated values.
func Query(name string, values ...string) CallOption {
	return WithQuery(url.Values{name: append([]string(nil), values...)})
}

// WithQuery copies values when the option is created. Applying a later option
// replaces only the keys present in that option. An empty slice removes a key.
func WithQuery(values url.Values) CallOption {
	snapshot := cloneValues(values)

	return func(o *callOptions) error {
		for k, v := range snapshot {
			o.query[k] = append([]string(nil), v...)
		}

		return nil
	}
}

// Header sets a request header, for example If-Match for sprint versions.
// Supplied values, including empty strings, override default request headers.
// Authentication, organization and framing headers are owned by the client.
func Header(name, value string) CallOption {
	return func(o *callOptions) error {
		if !validHeaderName(name) || !validHeaderValue(value) {
			return errors.New("ytr: invalid request header")
		}

		switch strings.ToLower(name) {
		case "authorization", "x-org-id", "x-cloud-org-id", "host", "content-type", "content-length", "transfer-encoding":
			return fmt.Errorf("ytr: header %s is managed by the client", name)
		}

		o.headers.Set(name, value)

		return nil
	}
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}

	for _, ch := range name {
		if !strings.ContainsRune("!#$%&'*+-.^_`|~0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz", ch) {
			return false
		}
	}

	return true
}

func optionsFor(options []CallOption) (callOptions, error) {
	result := callOptions{query: make(url.Values), headers: make(http.Header)}

	for _, option := range options {
		if option == nil {
			return result, errors.New("ytr: nil call option")
		}

		if err := option(&result); err != nil {
			return result, err
		}
	}

	return result, nil
}

func cloneValues(values url.Values) url.Values {
	result := make(url.Values, len(values))
	for k, v := range values {
		result[k] = append([]string(nil), v...)
	}

	return result
}
