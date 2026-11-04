package apidoc_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/skarm/ytr/internal/apidoc"
)

type memoryTransport func(*http.Request) (*http.Response, error)

type errorReader struct{}

func TestOnlineAuditDetectsDocumentationDrift(t *testing.T) {
	_, sources, index := fixture()
	body := []byte("documented endpoint")
	sources.Pages[0].ContentSHA256 = fmt.Sprintf("%x", sha256.Sum256(body))
	fetch := func(_ context.Context, url string) ([]byte, error) {
		if strings.Contains(url, "llms.txt") {
			return []byte(index), nil
		}
		return body, nil
	}
	if err := reviewSourceChanges(context.Background(), sources, fetch); err != nil {
		t.Fatal(err)
	}
	sources.Pages[0].ContentSHA256 = strings.Repeat("0", 64)
	if err := reviewSourceChanges(context.Background(), sources, fetch); err == nil || !strings.Contains(err.Error(), "get-issue") {
		t.Fatal(err)
	}
}

func TestOnlineIndexMayOmitRevisionLinks(t *testing.T) {
	_, sources, index := fixture()
	body := []byte("same document")
	sources.Pages[0].ContentSHA256 = fmt.Sprintf("%x", sha256.Sum256(body))
	index = strings.ReplaceAll(index, "?revision=r1", "")
	err := reviewSourceChanges(context.Background(), sources, func(_ context.Context, url string) ([]byte, error) {
		if strings.Contains(url, "llms.txt") {
			return []byte(index), nil
		}
		return body, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestHTTPFetcherReportsTransportStatusReadAndSizeErrors(t *testing.T) {
	for _, tc := range []struct {
		status    int
		body      io.Reader
		transport bool
		want      string
	}{
		{200, strings.NewReader("data"), false, "data"},
		{404, strings.NewReader("missing"), false, ""},
		{200, strings.NewReader(strings.Repeat("x", (4<<20)+1)), false, ""},
		{200, errorReader{}, false, ""},
		{200, strings.NewReader(""), true, ""},
	} {
		client := &http.Client{Transport: memoryTransport(func(r *http.Request) (*http.Response, error) {
			if r.URL.String() != "https://fixture.test/doc.md" || r.Header.Get("Authorization") != "" {
				t.Error(r.URL, r.Header)
			}
			if tc.transport {
				return nil, errors.New("transport")
			}
			return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(tc.body)}, nil
		})}
		got, err := apidoc.HTTPFetcher(client)(context.Background(), "https://fixture.test/doc.md")
		if tc.want != "" {
			if err != nil || string(got) != tc.want {
				t.Fatal(string(got), err)
			}
		} else if err == nil {
			t.Fatal("failure accepted")
		}
	}
	if _, err := apidoc.HTTPFetcher(&http.Client{})(context.Background(), "://"); err == nil {
		t.Fatal("bad URL accepted")
	}
}

func TestOnlineAuditRejectsIndexChangesAndNetworkFailures(t *testing.T) {
	_, sources, index := fixture()
	for _, fetch := range []apidoc.Fetcher{
		func(context.Context, string) ([]byte, error) { return nil, errors.New("network") },
		func(context.Context, string) ([]byte, error) { return []byte("empty index"), nil },
		func(_ context.Context, url string) ([]byte, error) {
			if strings.Contains(url, "llms.txt") {
				return []byte(index), nil
			}
			return nil, errors.New("page unavailable")
		},
	} {
		if err := reviewSourceChanges(context.Background(), sources, fetch); err == nil {
			t.Fatal("drift/failure accepted")
		}
	}
}

func TestContentDigestIgnoresRendererRevisionOnly(t *testing.T) {
	if apidoc.ContentDigest([]byte("link.md?revision=r1#section")) != apidoc.ContentDigest([]byte("link.md?revision=r2#section")) {
		t.Fatal("renderer revision affects content digest")
	}
	if apidoc.ContentDigest([]byte("GET /v3/issues")) == apidoc.ContentDigest([]byte("POST /v3/issues")) {
		t.Fatal("API change hidden by normalization")
	}
}

func TestOnlineRequestsCurrentUnpinnedPage(t *testing.T) {
	_, sources, index := fixture()
	err := reviewSourceChanges(context.Background(), sources, func(_ context.Context, url string) ([]byte, error) {
		if strings.Contains(url, "llms.txt") {
			return []byte(index), nil
		}
		if url != "https://yandex.ru/support/tracker/ru/api/issues/get-issue.md" {
			t.Errorf("unexpected URL: %s", url)
		}
		return nil, errors.New("deliberately unavailable")
	})
	if err == nil {
		t.Fatal("unavailable page accepted")
	}
}

func TestDocumentationLimitAcceptsExactlyFourMiB(t *testing.T) {
	c := &http.Client{Transport: memoryTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", 4<<20)))}, nil
	})}
	body, err := apidoc.HTTPFetcher(c)(context.Background(), "https://fixture.test")
	if err != nil || len(body) != 4<<20 {
		t.Fatal(len(body), err)
	}
}

func (f memoryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("read error") }

func reviewSourceChanges(ctx context.Context, sources apidoc.Sources, fetch apidoc.Fetcher) error {
	catalog, _, _ := fixture()
	baseline := apidoc.Documents{Version: 1, Revision: catalog.Revision}
	for _, page := range sources.Pages {
		baseline.Pages = append(baseline.Pages, apidoc.Document{Path: page.Path, Digest: page.ContentSHA256})
	}
	review, _, _, err := apidoc.ReviewOnline(ctx, catalog, baseline, apidoc.Models{}, fetch)
	if err != nil {
		return err
	}
	if len(review.Changes) > 0 {
		return fmt.Errorf("documentation changed: %s", review.Changes[0].Source)
	}
	return nil
}
