package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/skarm/ytr"
)

type settings struct {
	data, file, filename, field, baseURL string
	timeout                              time.Duration
	all                                  bool
	query                                queryFlags
	headers                              headerFlags
}

type queryFlags struct{ values url.Values }

type headerFlags struct {
	values  []string
	options []ytr.CallOption
}

type usageError struct{ cause error }

func parseFlags(args []string, diagnostics io.Writer) (settings, []string, error) {
	s := settings{query: queryFlags{make(url.Values)}}
	flags := flag.NewFlagSet("ytr", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	flags.StringVar(&s.data, "data", "", "JSON, @file, or - for stdin")
	flags.StringVar(&s.file, "file", "", "multipart file path, or - for stdin")
	flags.StringVar(&s.filename, "filename", "", "multipart filename (required with -file -)")
	flags.StringVar(&s.field, "field", "", "multipart field name (default file)")
	flags.StringVar(&s.baseURL, "base-url", ytr.DefaultBaseURL, "Tracker API origin")
	flags.DurationVar(&s.timeout, "timeout", 30*time.Second, "time limit for the operation or full page traversal")
	flags.BoolVar(&s.all, "all", false, "follow pages; emit one compact JSON page per line")
	flags.Var(&s.query, "query", "query name=value; repeat to send multiple values")
	flags.Var(&s.headers, "header", "HTTP Name: value; repeat for additional headers")

	if err := flags.Parse(args); err != nil {
		return s, nil, err
	}

	// Validate after flag parsing so flag's diagnostics never quote header values.
	for _, raw := range s.headers.values {
		option, err := parseHeaderOption(raw)
		if err != nil {
			return s, nil, err
		}

		s.headers.options = append(s.headers.options, option)
	}

	if s.timeout <= 0 {
		return s, nil, usage("timeout must be positive")
	}

	return s, flags.Args(), nil
}

func (s settings) options() []ytr.CallOption {
	return append([]ytr.CallOption{ytr.WithQuery(s.query.values)}, s.headers.options...)
}

func (s settings) validate(op ytr.Operation) error {
	if op.Request == "multipart" {
		return s.validateUpload()
	}

	if s.file != "" || s.filename != "" || s.field != "" {
		return usage("file options require a multipart operation")
	}

	if op.Request == "none" && s.data != "" {
		return usage("operation does not accept -data")
	}

	return nil
}

func (s settings) validateUpload() error {
	if s.all {
		return usage("multipart operation cannot use -all")
	}

	if s.file == "" || s.data != "" {
		return usage("multipart operation requires -file and cannot use -data")
	}

	if s.file == "-" && s.filename == "" {
		return usage("-filename is required with -file -")
	}

	filename := s.filename
	if filename == "" {
		filename = filepath.Base(s.file)
	}

	if strings.ContainsAny(filename, "\r\n\x00") || !validHeaderText(s.field) {
		return usage("invalid multipart filename or field name")
	}

	return nil
}

func (q queryFlags) String() string { return q.values.Encode() }
func (q *queryFlags) Set(raw string) error {
	name, value, ok := strings.Cut(raw, "=")
	if !ok || name == "" {
		return errors.New("query must be name=value")
	}

	q.values.Add(name, value)

	return nil
}

func (headerFlags) String() string { return "" }
func (h *headerFlags) Set(raw string) error {
	h.values = append(h.values, raw)
	return nil
}

func (e *usageError) Error() string { return e.cause.Error() }
func (e *usageError) Unwrap() error { return e.cause }

func parseHeaderOption(raw string) (ytr.CallOption, error) {
	name, value, ok := strings.Cut(raw, ":")

	name = strings.TrimSpace(name)
	if !ok || name == "" {
		return nil, errors.New("header must be Name: value")
	}

	value = strings.TrimSpace(value)
	if !validHeaderText(value) {
		return nil, errors.New("invalid header value")
	}

	for _, ch := range name {
		if !strings.ContainsRune("!#$%&'*+-.^_`|~0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz", ch) {
			return nil, errors.New("invalid header name")
		}
	}

	switch strings.ToLower(name) {
	case "authorization", "x-org-id", "x-cloud-org-id", "host", "content-type", "content-length", "transfer-encoding":
		return nil, fmt.Errorf("header %s is managed by the client", name)
	}

	return ytr.Header(name, value), nil
}

func validHeaderText(value string) bool {
	for _, ch := range value {
		if ch < 32 || ch == 127 {
			return false
		}
	}

	return true
}

func usage(message string) error { return &usageError{fmt.Errorf("ytr: %s", message)} }
func exitCode(err error) int {
	if err == nil {
		return 0
	}

	var input *usageError
	if errors.As(err, &input) || errors.Is(err, ytr.ErrUnknownOperation) || errors.Is(err, ytr.ErrOperationKind) {
		return 2
	}

	return 1
}
