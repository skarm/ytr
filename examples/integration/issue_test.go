package integration_test

import (
	"context"
	"errors"
	"testing"

	integration "example.com/ytr-product"
)

type unavailable struct{ err error }

func TestProductPortCanBeReplacedWithoutSDKTypes(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("application offline")
	var client integration.IssueClient = unavailable{sentinel}
	issue, err := client.GetIssue(t.Context(), "SDK-1")
	if !errors.Is(err, sentinel) || issue != (integration.Issue{}) {
		t.Fatal(issue, err)
	}
}

func (c unavailable) GetIssue(context.Context, string) (integration.Issue, error) {
	return integration.Issue{}, c.err
}
