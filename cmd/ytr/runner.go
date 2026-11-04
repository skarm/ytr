package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/skarm/ytr"
)

type runner struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	getenv         func(string) string
	httpClient     *http.Client
}

func (r runner) run(ctx context.Context, args []string) (err error) {
	s, args, err := parseFlags(args, r.stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}

	if err != nil {
		return &usageError{err}
	}

	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	stopStreams := context.AfterFunc(ctx, func() {
		closeCLIStream(r.stdin)
		closeCLIStream(r.stdout)
	})
	defer func() {
		if ctx.Err() == nil {
			stopStreams()
		}
	}()
	defer func() {
		if cause := ctx.Err(); cause != nil && !errors.Is(err, cause) {
			err = errors.Join(err, cause)
		}
	}()

	// Closing an OS file does not interrupt every blocked system call. Keep
	// main responsive so cancellation can terminate this single-command process.
	done := make(chan error, 1)
	go func() { done <- r.runCommand(ctx, args, s) }()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r runner) runCommand(ctx context.Context, args []string, s settings) error {
	if len(args) == 0 {
		return writeUsage(r.stdout)
	}

	if args[0] == "help" {
		return r.help(args[1:])
	}

	if args[0] == "operations" {
		return r.operations(args[1:])
	}

	return r.invoke(ctx, args, s)
}

func (r runner) invoke(ctx context.Context, args []string, s settings) error {
	op, err := ytr.LookupOperation(ytr.OperationName(args[0]))
	if err != nil {
		return err
	}

	parameters := op.PathParameters()
	if len(args)-1 != len(parameters) {
		return usage(fmt.Sprintf("%s needs path arguments: %s", op.Name, strings.Join(parameters, ", ")))
	}

	for index, name := range parameters {
		value := args[index+1]
		if value == "" || value == "." || value == ".." {
			return usage(fmt.Sprintf("invalid %s path argument", name))
		}

		if name == "entityType" && value != string(ytr.Project) && value != string(ytr.Portfolio) && value != string(ytr.Goal) {
			return usage("entity type must be project, portfolio or goal")
		}
	}

	if err = s.validate(op); err != nil {
		return err
	}

	if err = ctx.Err(); err != nil {
		return err
	}

	client, err := r.newClient(s)
	if err != nil {
		return err
	}

	return r.execute(ctx, client, op, args[1:], s)
}

func (r runner) execute(ctx context.Context, client *ytr.Client, op ytr.Operation, args []string, s settings) error {
	if op.Response == "stream" {
		return r.download(ctx, client, op, args, s)
	}

	if op.Request == "multipart" {
		return r.upload(ctx, client, op, args, s)
	}

	body, err := readJSON(s.data, r.stdin)
	if err != nil {
		return err
	}

	if s.all {
		return client.Paginate(ctx, op.Name, args, body, r.writeJSON, s.options()...)
	}

	response, err := client.Call(ctx, op.Name, args, body, s.options()...)
	if err != nil {
		return err
	}

	return r.writeJSON(response)
}

func (r runner) operations(args []string) error {
	if len(args) != 0 {
		return usage("operations does not accept arguments")
	}

	encoder := json.NewEncoder(r.stdout)
	encoder.SetIndent("", "  ")

	return encoder.Encode(ytr.Operations())
}

func (r runner) help(args []string) error {
	if len(args) == 0 {
		return writeUsage(r.stdout)
	}

	if len(args) != 1 {
		return usage("help accepts one operation name")
	}

	op, err := ytr.LookupOperation(ytr.OperationName(args[0]))
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(r.stdout, "%s\n%s %s\nPath arguments: %s\nRequest: %s; response: %s\n%s\n", op.Name, op.Method, op.Path, strings.Join(op.PathParameters(), ", "), op.Request, op.Response, op.Documentation)

	return err
}

func closeCLIStream(stream any) {
	if closer, ok := stream.(io.Closer); ok {
		_ = closer.Close()
	}
}

func writeUsage(out io.Writer) error {
	_, err := fmt.Fprintln(out, `Usage: ytr [flags] Operation [path arguments]
       ytr operations
       ytr help [Operation]

Flags precede the operation. Use -h to list flags.
Credentials: YTR_TOKEN, YTR_ORG_ID, YTR_ORG_TYPE=360|cloud,
YTR_AUTH_SCHEME=OAuth|Bearer (defaults: 360 and OAuth).
JSON goes to stdout; downloads stream binary bytes to stdout.
-all emits one compact JSON page per line. Exit codes: 0 success, 2 usage, 1 failure.`)

	return err
}
