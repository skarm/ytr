package ytr_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skarm/ytr"
)

type transportFunc func(*http.Request) (*http.Response, error)

func TestConfigurationValidation(t *testing.T) {
	cases := map[string]func(*ytr.Config){
		"no source":            func(c *ytr.Config) { c.TokenSource = nil },
		"no org":               func(c *ytr.Config) { c.Organization.ID = "" },
		"blank org":            func(c *ytr.Config) { c.Organization.ID = " " },
		"org header injection": func(c *ytr.Config) { c.Organization.ID = "x\r\nsecret: value" },
		"org kind":             func(c *ytr.Config) { c.Organization.Kind = "both" },
		"base parse":           func(c *ytr.Config) { c.BaseURL = "://" },
		"base protocol":        func(c *ytr.Config) { c.BaseURL = "file:///tmp/x" },
		"base missing host":    func(c *ytr.Config) { c.BaseURL = "https:///" },
		"base hostless port":   func(c *ytr.Config) { c.BaseURL = "http://:8080" },
		"base empty IP":        func(c *ytr.Config) { c.BaseURL = "https://[]" },
		"base empty IP port":   func(c *ytr.Config) { c.BaseURL = "https://[]:443" },
		"base credentials":     func(c *ytr.Config) { c.BaseURL = "https://user:pass@example.com" },
		"base query":           func(c *ytr.Config) { c.BaseURL = "https://example.com?q=x" },
		"base fragment":        func(c *ytr.Config) { c.BaseURL = "https://example.com#fragment" },
		"base prefix":          func(c *ytr.Config) { c.BaseURL = "https://example.com/prefix" },
		"language":             func(c *ytr.Config) { c.Language = "en\n" },
		"negative limit":       func(c *ytr.Config) { c.MaxResponseBytes = -1 },
		"overflow limit":       func(c *ytr.Config) { c.MaxResponseBytes = 1<<63 - 1 },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := validConfig()
			edit(&cfg)
			if c, err := ytr.New(cfg); err == nil || c != nil {
				t.Fatal(c, err)
			}
		})
	}
}

func TestRequestValidationFailsBeforeHTTP(t *testing.T) {
	calls := 0
	c := client(t, func(*http.Request) (*http.Response, error) { calls++; return reply(200, `{}`), nil })
	for _, id := range []string{"", ".", ".."} {
		if _, err := c.GetIssue(context.Background(), id); err == nil {
			t.Fatal(id)
		}
	}
	if _, err := c.GetEntity(context.Background(), ytr.EntityType("user"), "x"); err == nil {
		t.Fatal("invalid entity type")
	}
	for _, opt := range []ytr.CallOption{nil, ytr.Header("", "value"), ytr.Header("bad name", "x"), ytr.Header("Foo", "\r\nx"), ytr.Header("Authorization", "x"), ytr.Header("X-Org-ID", "x"), ytr.Header("X-Cloud-Org-ID", "x"), ytr.Header("Content-Type", "x"), ytr.Header("Host", "x"), ytr.Header("Content-Length", "5"), ytr.Header("Transfer-Encoding", "chunked")} {
		if _, err := c.GetCurrentUser(context.Background(), opt); err == nil {
			t.Fatal("invalid option accepted")
		}
	}
	if _, err := c.CreateIssue(context.Background(), make(chan int)); err == nil {
		t.Fatal("unencodable body")
	}
	if calls != 0 {
		t.Fatal(calls)
	}
}

func TestTokenValidationAndProviderErrors(t *testing.T) {
	sentinel := errors.New("cannot refresh")
	for _, test := range []struct {
		source ytr.TokenSource
		cause  error
	}{
		{ytr.OAuthToken(""), nil},
		{ytr.IAMToken("a b"), nil},
		{ytr.OAuthToken("x\x00"), nil},
		{ytr.TokenSourceFunc(func(context.Context) (ytr.Token, error) {
			return ytr.Token{Scheme: "Basic", Value: "private"}, nil
		}), nil},
		{ytr.TokenSourceFunc(func(context.Context) (ytr.Token, error) { return ytr.Token{}, sentinel }), sentinel},
	} {
		cfg := validConfig()
		cfg.TokenSource = test.source
		cfg.HTTPClient = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
			t.Error("invalid auth reached HTTP")
			return reply(200, `{}`), nil
		})}
		c, err := ytr.New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = c.GetCurrentUser(context.Background()); err == nil {
			t.Fatal("invalid token accepted")
		}
		if test.cause != nil && !errors.Is(err, test.cause) {
			t.Fatalf("token source error identity lost: %v", err)
		}
	}
}

func TestLanguageHeadersAndCallerClientArePreserved(t *testing.T) {
	redirect := func(*http.Request, []*http.Request) error { return nil }
	original := &http.Client{Timeout: time.Second, CheckRedirect: redirect, Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Accept-Language") != "en" || r.Header.Get("If-Match") != `"7"` || r.Header.Get("User-Agent") != "ytr-go" {
			t.Error(r.Header)
		}
		if r.URL.String() != "https://fixture.test/v3/sprints/1/_start" {
			t.Error(r.URL)
		}
		return reply(204, ""), nil
	})}
	cfg := validConfig()
	cfg.HTTPClient = original
	cfg.Language = "en"
	cfg.BaseURL = "https://fixture.test/"
	c, err := ytr.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Organization.ID = "mutated"
	if _, err = c.StartSprint(context.Background(), "1", ytr.Header("If-Match", `"7"`)); err != nil {
		t.Fatal(err)
	}
	if original.Timeout != time.Second || reflect.ValueOf(original.CheckRedirect).Pointer() != reflect.ValueOf(redirect).Pointer() {
		t.Fatal("input client changed")
	}
}

func TestPerCallHeadersOverrideClientDefaults(t *testing.T) {
	for _, test := range []struct {
		name    string
		options []ytr.CallOption
		want    [3]string
	}{
		{"defaults", nil, [3]string{"ytr-go", "application/json", "ru"}},
		{"custom", []ytr.CallOption{ytr.Header("user-agent", "product/1"), ytr.Header("accept", "application/vnd.example+json"), ytr.Header("accept-language", "en")}, [3]string{"product/1", "application/vnd.example+json", "en"}},
		{"explicit empty", []ytr.CallOption{ytr.Header("User-Agent", ""), ytr.Header("Accept", ""), ytr.Header("Accept-Language", "")}, [3]string{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := validConfig()
			config.Language = "ru"
			calls := 0
			config.HTTPClient = &http.Client{Transport: transportFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				got := [3]string{request.Header.Get("User-Agent"), request.Header.Get("Accept"), request.Header.Get("Accept-Language")}
				if got != test.want {
					t.Errorf("per-call headers lost: got %q, want %q", got, test.want)
				}
				return reply(200, `{}`), nil
			})}
			client, err := ytr.New(config)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if _, err := client.GetCurrentUser(t.Context(), test.options...); err != nil {
					t.Fatal(err)
				}
			}
			if calls != 2 || config.Language != "ru" {
				t.Fatal("per-call options changed the configuration or skipped a request")
			}
		})
	}
}

func TestRedirectNeverSendsCredentialsToAnotherServer(t *testing.T) {
	leaked := false
	source := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "fixture.test" {
			leaked = true
			return reply(200, `{}`), nil
		}
		v := reply(302, "")
		v.Header.Set("Location", "https://other.test/v3/myself")
		return v, nil
	})}
	cfg := validConfig()
	cfg.BaseURL = "https://fixture.test"
	cfg.HTTPClient = source
	c, err := ytr.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.GetCurrentUser(context.Background())
	var api *ytr.APIError
	if !errors.As(err, &api) || api.StatusCode != 302 || leaked {
		t.Fatal(err, leaked)
	}
}

func TestCreateIssueWireContract(t *testing.T) {
	c := client(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != "POST" || r.URL.String() != "https://api.tracker.yandex.net/v3/issues?notify=false&version=7" {
			t.Errorf("request: %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Authorization") != "OAuth private-token" || r.Header.Get("X-Org-Id") != "42" || r.Header.Get("X-Cloud-Org-Id") != "" {
			t.Errorf("auth headers: %v", r.Header)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Error(r.Header)
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]json.RawMessage
		if err = json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		for key, want := range map[string]string{"summary": `"Привет"`, "assignee": "null", "flag": "false", "count": "0", "tags": "[]", "customField": `{"add":["x"]}`} {
			if string(got[key]) != want {
				t.Errorf("%s=%s; want %s", key, got[key], want)
			}
		}
		resp := reply(201, `{"key":"SDK-1","customNumber":9007199254740993}`)
		resp.Header.Set("X-Request-ID", "fixture")
		return resp, nil
	})
	res, err := c.CreateIssue(context.Background(), ytr.Fields{"summary": "Привет", "assignee": nil, "flag": false, "count": 0, "tags": []string{}, "customField": ytr.Fields{"add": []string{"x"}}}, ytr.Query("notify", "false"), ytr.Query("version", "7"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err = res.Decode(&got); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 201 || res.Header.Get("X-Request-ID") != "fixture" || got["key"] != "SDK-1" || got["customNumber"] != json.Number("9007199254740993") {
		t.Fatalf("response: %+v %+v", res, got)
	}
}

func TestPathEscapingAndRepeatedQuery(t *testing.T) {
	c := client(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.EscapedPath() != "/v3/issues/Q%2F%D0%AF%20%3F%23%25" {
			t.Errorf("path: %s", r.URL.EscapedPath())
		}
		if !reflect.DeepEqual(r.URL.Query()["createdAt"], []string{"from:2026-01-01", "to:2026-12-31"}) {
			t.Error(r.URL.RawQuery)
		}
		return reply(200, `{}`), nil
	})
	q := url.Values{"createdAt": {"from:2026-01-01", "to:2026-12-31"}}
	opt := ytr.WithQuery(q)
	q.Set("createdAt", "mutated")
	if _, err := c.GetIssue(context.Background(), "Q/Я ?#%", opt); err != nil {
		t.Fatal(err)
	}
}

func TestCloudIAMAndTokenRotation(t *testing.T) {
	calls := 0
	c, err := ytr.New(ytr.Config{Organization: ytr.Organization{Kind: ytr.OrganizationCloud, ID: "cloud-id"}, TokenSource: ytr.TokenSourceFunc(func(context.Context) (ytr.Token, error) {
		calls++
		return ytr.Token{Scheme: ytr.Bearer, Value: "token-" + strings.Repeat("x", calls)}, nil
	}), HTTPClient: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("X-Cloud-Org-Id") != "cloud-id" || r.Header.Get("X-Org-Id") != "" || r.Header.Get("Authorization") != "Bearer token-"+strings.Repeat("x", calls) {
			t.Error(r.Header)
		}
		return reply(200, `{}`), nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err = c.GetCurrentUser(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}

func TestAPIErrorAndNoWriteRetry(t *testing.T) {
	calls := 0
	c := client(t, func(*http.Request) (*http.Response, error) {
		calls++
		r := reply(429, `{"errorMessages":["limit"],"errors":{"field":["bad"]}}`)
		r.Header.Set("Retry-After", "3")
		return r, nil
	})
	res, err := c.CreateIssue(context.Background(), ytr.Fields{"summary": "x"})
	var api *ytr.APIError
	if !errors.As(err, &api) || api.StatusCode != 429 || res.StatusCode != 429 || len(api.ErrorMessages) != 1 || string(api.Errors["field"]) != `["bad"]` || api.Header.Get("Retry-After") != "3" || calls != 1 {
		t.Fatalf("%+v, %v, calls %d", api, err, calls)
	}
	if strings.Contains(err.Error(), "private-token") {
		t.Fatal("token in error")
	}
}

func TestCancelledContextAndTransportError(t *testing.T) {
	sentinel := errors.New("offline")
	c := client(t, func(r *http.Request) (*http.Response, error) {
		if r.Context().Err() != nil {
			return nil, r.Context().Err()
		}
		return nil, sentinel
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.GetIssue(ctx, "Q-1"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := c.GetIssue(context.Background(), "Q-1"); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}

func TestConcurrentRequests(t *testing.T) {
	c := client(t, func(r *http.Request) (*http.Response, error) { return reply(200, `{"key":"Q-1"}`), nil })
	option := ytr.WithQuery(url.Values{"expand": {"all"}})
	var wg sync.WaitGroup
	for range 24 {
		wg.Go(func() {
			r, err := c.GetIssue(context.Background(), "Q-1", option)
			if err != nil || string(r.Body) != `{"key":"Q-1"}` {
				t.Errorf("%v %v", r, err)
			}
		})
	}
	wg.Wait()
}

func TestBaseURLCannotContainEmptyQueryDelimiter(t *testing.T) {
	cfg := validConfig()
	cfg.BaseURL = "https://fixture.test?"
	if c, err := ytr.New(cfg); c != nil || err == nil {
		t.Fatal("base URL query would consume the API path")
	}
}

func TestHTTPOriginAndWhitespaceHeader(t *testing.T) {
	cfg := validConfig()
	cfg.BaseURL = "http://fixture.test"
	cfg.HTTPClient = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "http" || r.Header.Get("X-Note") != "a b" {
			t.Error(r.URL, r.Header)
		}
		return reply(200, `{}`), nil
	})}
	c, err := ytr.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.GetCurrentUser(context.Background(), ytr.Header("X-Note", "a b")); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultTimeoutIsAppliedWithoutWaiting(t *testing.T) {
	before := time.Now()
	original := http.DefaultTransport
	http.DefaultTransport = transportFunc(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok || deadline.Before(before.Add(time.Second)) || deadline.After(before.Add(time.Minute)) {
			t.Error("default timeout is not bounded at 30 seconds")
		}
		return reply(200, `{}`), nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	c, err := ytr.New(validConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.GetCurrentUser(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func validConfig() ytr.Config {
	return ytr.Config{Organization: ytr.Organization{Kind: ytr.Organization360, ID: "42"}, TokenSource: ytr.OAuthToken("secret")}
}

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func reply(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func client(t *testing.T, fn transportFunc) *ytr.Client {
	t.Helper()
	c, err := ytr.New(ytr.Config{Organization: ytr.Organization{Kind: ytr.Organization360, ID: "42"}, TokenSource: ytr.OAuthToken("private-token"), HTTPClient: &http.Client{Transport: fn}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
