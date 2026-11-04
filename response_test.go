package ytr_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/skarm/ytr"
)

func TestDecodeAndRetryAfter(t *testing.T) {
	for _, body := range []string{`{} {}`, `[`, ``} {
		var out any
		err := (&ytr.Response{Body: []byte(body)}).Decode(&out)
		if err == nil {
			t.Fatal(body)
		}
	}
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		header   string
		duration time.Duration
		valid    bool
	}{{"5", 5 * time.Second, true}, {now.Add(time.Minute).Format(http.TimeFormat), time.Minute, true}, {now.Add(-time.Minute).Format(http.TimeFormat), 0, true}, {"-1", 0, false}, {"", 0, false}, {"garbage", 0, false}} {
		e := &ytr.APIError{Header: http.Header{"Retry-After": {tc.header}}}
		d, ok := e.RetryAfter(now)
		if d != tc.duration || ok != tc.valid {
			t.Fatal(tc, d, ok)
		}
	}
}
