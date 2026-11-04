package apidoc

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
)

var revisionQueryPattern = regexp.MustCompile(`[?&]revision=r[0-9]+`)

// Fetcher retrieves a public documentation page.
type Fetcher func(context.Context, string) ([]byte, error)

// HTTPFetcher returns a Fetcher that reads public documents with a 4 MiB per-document bound.
func HTTPFetcher(client *http.Client) Fetcher {
	return func(ctx context.Context, url string) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, err
		}

		response, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer func() { _ = response.Body.Close() }()

		if response.StatusCode != 200 {
			return nil, fmt.Errorf("documentation returned HTTP %d", response.StatusCode)
		}

		body, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
		if err != nil {
			return nil, err
		}

		if len(body) > 4<<20 {
			return nil, errors.New("documentation exceeds 4 MiB")
		}

		return body, nil
	}
}

// ContentDigest ignores only the documentation renderer's revision query suffix.
// Pinned and live pages have identical text but different link presentation.
// The source inventory also retains the original, unnormalized SHA-256.
func ContentDigest(body []byte) string {
	canonical := revisionQueryPattern.ReplaceAll(body, nil)
	return fmt.Sprintf("%x", sha256.Sum256(canonical))
}
