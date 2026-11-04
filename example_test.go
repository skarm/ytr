package ytr_test

import (
	"fmt"

	"github.com/skarm/ytr"
)

func ExampleDecode() {
	type Issue struct {
		Key string `json:"key"`
	}
	response := &ytr.Response{Body: []byte(`{"key":"SDK-1"}`)}
	issue, err := ytr.Decode[Issue](response, nil)
	if err != nil {
		panic(err)
	}
	fmt.Println(issue.Key)
	// Output: SDK-1
}

func ExampleLookupOperation() {
	op, err := ytr.LookupOperation(ytr.OpGetIssue)
	if err != nil {
		panic(err)
	}
	fmt.Println(op.Method, op.Path, op.PathParameters())
	// Output: GET /v3/issues/{issueID} [issueID]
}
