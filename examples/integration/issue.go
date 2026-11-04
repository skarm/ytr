// Package integration defines the product's issue model and the port consumed
// by its application logic. It has no dependency on the Tracker SDK.
package integration

import "context"

// Issue is the product-owned projection of a Tracker issue.
type Issue struct {
	Key     string `json:"key"`
	Summary string `json:"summary"`
}

// IssueClient is the product port used to retrieve an issue.
type IssueClient interface {
	GetIssue(context.Context, string) (Issue, error)
}
