// Command ytr is a thin command-line adapter over the public ytr package.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"

	"github.com/skarm/ytr"
)

func main() {
	os.Exit(runCLI())
}

func runCLI() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	r := runner{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr, getenv: os.Getenv}
	if err := r.run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitCode(err)
	}

	return 0
}

func (r runner) newClient(s settings) (*ytr.Client, error) {
	kind := ytr.OrganizationKind(r.getenv("YTR_ORG_TYPE"))
	if kind == "" {
		kind = ytr.Organization360
	}

	scheme := ytr.AuthScheme(r.getenv("YTR_AUTH_SCHEME"))
	if scheme == "" {
		scheme = ytr.OAuth
	}

	source, err := tokenSource(scheme, r.getenv("YTR_TOKEN"))
	if err != nil {
		return nil, err
	}

	httpClient := r.httpClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: s.timeout}
	}

	client, err := ytr.New(ytr.Config{Organization: ytr.Organization{Kind: kind, ID: r.getenv("YTR_ORG_ID")}, TokenSource: source, HTTPClient: httpClient, BaseURL: s.baseURL})
	if err != nil {
		return nil, &usageError{err}
	}

	return client, nil
}

func tokenSource(scheme ytr.AuthScheme, value string) (ytr.TokenSource, error) {
	if value == "" {
		return nil, usage("YTR_TOKEN is required")
	}

	if strings.ContainsRune(value, ' ') || !validHeaderText(value) {
		return nil, usage("invalid YTR_TOKEN value")
	}

	switch scheme {
	case ytr.OAuth:
		return ytr.OAuthToken(value), nil
	case ytr.Bearer:
		return ytr.IAMToken(value), nil
	default:
		return nil, usage("YTR_AUTH_SCHEME must be OAuth or Bearer")
	}
}
