package ytr_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
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

// Both response paths must enforce the byte limit independently of the
// Content-Length hint, and preserve the same JSON values and large numbers.
func FuzzJSONResponseBoundaries(f *testing.F) {
	for _, seed := range []string{
		`{"id":9007199254740993}`, `[{"id":9223372036854775807}]`,
		`null`, `0`, `""`, `{}`, `[]`, `{} {}`, `{"id":`, " \r\n\t", "",
	} {
		f.Add([]byte(seed), uint8(64))
	}
	f.Fuzz(func(t *testing.T, data []byte, bound uint8) {
		if len(data) > 4096 {
			t.Skip("keep response inputs bounded")
		}
		limit := int64(bound) + 1
		var want any
		valid := json.Valid(data)
		if valid {
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.UseNumber()
			if err := decoder.Decode(&want); err != nil {
				t.Fatal(err)
			}
		}
		for _, mode := range []string{"buffered", "known length", "unknown length"} {
			stream := &streamingBody{reader: bytes.NewReader(data)}
			c := limitedStreamingClient(t, limit, func(*http.Request) (*http.Response, error) {
				length := int64(len(data))
				if mode == "unknown length" {
					length = -1
				}
				return &http.Response{StatusCode: http.StatusOK, ContentLength: length, Body: stream}, nil
			})
			var got any
			var err error
			if mode == "buffered" {
				var response *ytr.Response
				response, err = c.GetCurrentUser(t.Context())
				if err == nil {
					err = response.Decode(&got)
				}
			} else {
				_, err = c.CallInto(t.Context(), ytr.OpGetCurrentUser, nil, nil, &got)
			}
			switch {
			case int64(len(data)) > limit:
				if !errors.Is(err, ytr.ErrResponseTooLarge) {
					t.Fatalf("%s: oversized response accepted: %v", mode, err)
				}
			case len(bytes.Trim(data, " \r\n\t")) == 0:
				if !errors.Is(err, io.EOF) {
					t.Fatalf("%s: empty response: %v", mode, err)
				}
			case !valid:
				if !errors.Is(err, ytr.ErrInvalidResponse) {
					t.Fatalf("%s: invalid response accepted: %v", mode, err)
				}
			default:
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("%s: decoded %#v, want %#v: %v", mode, got, want, err)
				}
			}
			if stream.closes != 1 || int64(stream.bytes) > limit+1 {
				t.Fatalf("%s: closes=%d bytes=%d limit=%d", mode, stream.closes, stream.bytes, limit)
			}
		}
	})
}
