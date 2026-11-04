package ytr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is the Tracker API origin used when Config.BaseURL is empty.
const (
	DefaultBaseURL             = "https://api.tracker.yandex.net"
	defaultResponseLimit int64 = 16 << 20
)

// Config is copied by New. A custom HTTPClient is shallow-copied; its Transport
// remains application-owned. Zero MaxResponseBytes selects 16 MiB. A nil HTTPClient
// selects a 30-second timeout. BaseURL must be an HTTP(S) origin with no path.
type Config struct {
	Organization     Organization
	TokenSource      TokenSource
	HTTPClient       *http.Client
	BaseURL          string
	Language         string
	MaxResponseBytes int64
}

// Client executes Tracker operations with caller-supplied credentials.
// It can be shared by goroutines when its token source and transport also
// support concurrent use.
type Client struct {
	base          *url.URL
	http          *http.Client
	organization  Organization
	orgHeader     string
	tokenSource   TokenSource
	language      string
	responseLimit int64
}

// New validates config and creates a client without making network requests.
func New(config Config) (*Client, error) {
	orgHeader, err := config.Organization.header()
	if err != nil {
		return nil, err
	}

	if config.TokenSource == nil {
		return nil, errors.New("ytr: a token source is required")
	}

	base, err := parseBaseURL(config.BaseURL)
	if err != nil {
		return nil, err
	}

	limit, err := resolveResponseLimit(config.MaxResponseBytes)
	if err != nil {
		return nil, err
	}

	if !validHeaderValue(config.Language) {
		return nil, errors.New("ytr: invalid language header")
	}

	return &Client{base: base, http: newHTTPClient(config.HTTPClient), organization: config.Organization, orgHeader: orgHeader, tokenSource: config.TokenSource, language: config.Language, responseLimit: limit}, nil
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader, contentType string, options callOptions) (*http.Request, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	address := c.base.String() + path
	if query := options.query.Encode(); query != "" {
		address += "?" + query
	}

	req, err := http.NewRequestWithContext(ctx, method, address, body)
	if err != nil {
		return nil, err
	}

	req.Header = options.headers.Clone()
	if _, supplied := req.Header["User-Agent"]; !supplied {
		req.Header.Set("User-Agent", "ytr-go")
	}

	if _, supplied := req.Header["Accept"]; !supplied {
		req.Header.Set("Accept", "application/json")
	}

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	if _, supplied := req.Header["Accept-Language"]; !supplied && c.language != "" {
		req.Header.Set("Accept-Language", c.language)
	}

	if err = c.authenticate(ctx, req); err != nil {
		return nil, err
	}

	return req, nil
}

func (c *Client) authenticate(ctx context.Context, req *http.Request) error {
	token, err := c.tokenSource.Token(ctx)
	if err != nil {
		return fmt.Errorf("ytr: token source: %w", err)
	}

	value, err := authorization(token)
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", value)
	req.Header.Set(c.orgHeader, c.organization.ID)

	return nil
}

func (c *Client) send(req *http.Request) (*http.Response, error) {
	response, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ytr: HTTP transport: %w", err)
	}

	return response, nil
}

func newHTTPClient(original *http.Client) *http.Client {
	result := http.Client{Timeout: 30 * time.Second}
	if original != nil {
		result = *original
	}

	result.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	return &result
}

func parseBaseURL(raw string) (*url.URL, error) {
	if raw == "" {
		raw = DefaultBaseURL
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("ytr: invalid base URL")
	}

	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, errors.New("ytr: base URL must use HTTP or HTTPS")
	}

	if !isOrigin(u) {
		return nil, errors.New("ytr: base URL must be an origin without credentials, query or fragment")
	}

	u.Path = ""
	u.RawPath = ""

	return u, nil
}

func isOrigin(u *url.URL) bool {
	return u.Hostname() != "" && u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && strings.Trim(u.Path, "/") == ""
}

func resolveResponseLimit(limit int64) (int64, error) {
	if limit == 0 {
		return defaultResponseLimit, nil
	}

	if limit < 0 || limit == int64(^uint64(0)>>1) {
		return 0, errors.New("ytr: invalid response size limit")
	}

	return limit, nil
}
