package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/skarm/ytr"
)

func (r runner) writeJSON(response *ytr.Response) error {
	if len(bytes.Trim(response.Body, " \t\r\n")) == 0 {
		return nil
	}

	var compact bytes.Buffer
	if err := json.Compact(&compact, response.Body); err != nil {
		return err
	}

	_, err := fmt.Fprintln(r.stdout, compact.String())

	return err
}

func (r runner) upload(ctx context.Context, client *ytr.Client, op ytr.Operation, args []string, s settings) error {
	file, closeInput, err := r.openUploadInput(s)
	if err != nil {
		return err
	}

	options := append([]ytr.CallOption{ytr.Query("filename", file.Name)}, s.options()...)

	response, callErr := client.Upload(ctx, op.Name, args, file, options...)
	if err = errors.Join(callErr, closeInput()); err != nil {
		return err
	}

	return r.writeJSON(response)
}

func (r runner) openUploadInput(s settings) (ytr.File, func() error, error) {
	file := ytr.File{Name: s.filename, FieldName: s.field}
	if s.file == "-" {
		file.Reader = r.stdin
		return file, func() error { return nil }, nil
	}

	input, err := os.Open(s.file)
	if err != nil {
		return file, nil, err
	}

	file.Reader = input
	if file.Name == "" {
		file.Name = filepath.Base(s.file)
	}

	return file, input.Close, nil
}

func (r runner) download(ctx context.Context, client *ytr.Client, op ytr.Operation, args []string, s settings) error {
	if s.all {
		return fmt.Errorf("%w: download cannot use -all", ytr.ErrOperationKind)
	}

	response, err := client.Download(ctx, op.Name, args, s.options()...)
	if err != nil {
		return err
	}

	_, copyErr := io.Copy(r.stdout, response.Body)

	return errors.Join(copyErr, response.Body.Close())
}

func readJSON(value string, stdin io.Reader) (any, error) {
	if value == "" {
		return nil, nil
	}

	body, err := readJSONInput(value, stdin)
	if err != nil {
		return nil, err
	}

	if !json.Valid(body) {
		return nil, usage("-data must contain one valid JSON value")
	}

	return json.RawMessage(body), nil
}

func readJSONInput(value string, stdin io.Reader) ([]byte, error) {
	if value == "-" {
		return io.ReadAll(stdin)
	}

	if after, ok := strings.CutPrefix(value, "@"); ok {
		return os.ReadFile(after) // #nosec G304 -- The local CLI caller explicitly chooses the input file.
	}

	return []byte(value), nil
}
