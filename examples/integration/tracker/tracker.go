// Package tracker adapts the public typed SDK to the product's issue port.
package tracker

import (
	"context"

	integration "example.com/ytr-product"

	"github.com/skarm/ytr/typed"
)

// Adapter translates between the product's model and the SDK's wire types.
type Adapter struct{ Client *typed.Client }

var _ integration.IssueClient = Adapter{}

// GetIssue retrieves only the fields used by the product.
func (t Adapter) GetIssue(ctx context.Context, key string) (integration.Issue, error) {
	result, err := t.Client.GetIssue(ctx, typed.GetIssueRequest{IssueID: key, Fields: []string{"key", "summary"}})
	if err != nil {
		return integration.Issue{}, err
	}

	return integration.Issue{Key: result.Data.Key, Summary: result.Data.Summary}, nil
}
