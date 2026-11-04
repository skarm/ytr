package tracker_test

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	integration "example.com/ytr-product"
	"example.com/ytr-product/tracker"

	"github.com/skarm/ytr"
	"github.com/skarm/ytr/typed"
)

type transport func(*http.Request) (*http.Response, error)

func TestIndependentProductImportsAndCallsPublicSDK(t *testing.T) {
	t.Parallel()
	client, err := typed.New(ytr.Config{Organization: ytr.Organization{Kind: ytr.Organization360, ID: "fixture"}, TokenSource: ytr.OAuthToken("fixture-token"), HTTPClient: &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.Path != "/v3/issues/SDK-1" || r.URL.Query().Get("fields") != "key,summary" || r.Header.Get("Authorization") != "OAuth fixture-token" {
			t.Error(r.Method, r.URL, r.Header)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"key":"SDK-1","summary":"Product model","customField":false}`))}, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	var port integration.IssueClient = tracker.Adapter{Client: client}
	issue, err := port.GetIssue(t.Context(), "SDK-1")
	if err != nil || issue.Key != "SDK-1" || issue.Summary != "Product model" {
		t.Fatal(issue, err)
	}
}

func TestAdapterPreservesSDKErrorAndReturnsZeroIssue(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("transport unavailable")
	client, err := typed.New(ytr.Config{Organization: ytr.Organization{Kind: ytr.Organization360, ID: "fixture"}, TokenSource: ytr.OAuthToken("fixture-token"), HTTPClient: &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
		return nil, sentinel
	})}})
	if err != nil {
		t.Fatal(err)
	}
	issue, err := (tracker.Adapter{Client: client}).GetIssue(t.Context(), "SDK-1")
	if !errors.Is(err, sentinel) || issue != (integration.Issue{}) {
		t.Fatal(issue, err)
	}
}

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
