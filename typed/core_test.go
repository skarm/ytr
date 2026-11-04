package typed_test

import (
	"net/http"
	"testing"

	"github.com/skarm/ytr"
	"github.com/skarm/ytr/typed"
)

type coreTransportFunc func(*http.Request) (*http.Response, error)

func TestClientConstructorsValidateAndShareWithoutIO(t *testing.T) {
	t.Parallel()
	if client, err := typed.FromClient(nil); err == nil || client != nil {
		t.Fatalf("FromClient(nil): %v, %v", client, err)
	}
	if client, err := typed.New(ytr.Config{}); err == nil || client != nil {
		t.Fatalf("New(empty config): %v, %v", client, err)
	}
	calls := 0
	config := coreConfig(func(*http.Request) (*http.Response, error) {
		calls++
		t.Error("constructor made an HTTP request")
		return nil, nil
	})
	created, err := typed.New(config)
	if err != nil || created == nil || created.Raw() == nil {
		t.Fatalf("New: %v, %v", created, err)
	}
	raw, err := ytr.New(config)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := typed.FromClient(raw)
	if err != nil || shared == nil || shared.Raw() != raw || calls != 0 {
		t.Fatalf("FromClient: %v, %v, calls=%d", shared, err, calls)
	}
}

func (fn coreTransportFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func coreConfig(fn coreTransportFunc) ytr.Config {
	return ytr.Config{Organization: ytr.Organization{Kind: ytr.Organization360, ID: "fixture"}, TokenSource: ytr.OAuthToken("fixture-token"), BaseURL: "https://fixture.invalid", HTTPClient: &http.Client{Transport: fn}}
}
