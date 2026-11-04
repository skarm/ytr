package ytr_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"testing"

	"github.com/skarm/ytr"
)

func TestPOSTPaginationKeepsMethodBodyAndFollowsSchemeLessLink(t *testing.T) {
	calls := 0
	c := client(t, func(r *http.Request) (*http.Response, error) {
		calls++
		body, _ := io.ReadAll(r.Body)
		if r.Method != "POST" || string(body) != `{"queue":"SDK"}` {
			t.Errorf("request changed: %s %s", r.Method, body)
		}
		result := reply(200, `[{"key":"SDK-1"}]`)
		if calls == 1 {
			result.Header.Set("Link", `<api.tracker.yandex.net/v3/issues/_search?id=cursor&perPage=10>; title="a,b"; rel="next last"`)
		} else if r.URL.Query().Get("id") != "cursor" {
			t.Error(r.URL)
		}
		return result, nil
	})
	var pages []string
	err := c.Paginate(context.Background(), ytr.OpSearchIssues, nil, ytr.Fields{"queue": "SDK"}, func(r *ytr.Response) error { pages = append(pages, string(r.Body)); return nil }, ytr.Query("perPage", "10"))
	if err != nil || calls != 2 || len(pages) != 2 {
		t.Fatalf("%v calls=%d pages=%v", err, calls, pages)
	}
}

func TestNumberedPaginationAndStop(t *testing.T) {
	var pages []string
	c := client(t, func(r *http.Request) (*http.Response, error) {
		pages = append(pages, r.URL.Query().Get("page"))
		v := reply(200, `[]`)
		v.Header.Set("X-Total-Pages", "3")
		return v, nil
	})
	err := c.Paginate(context.Background(), ytr.OpListQueues, nil, nil, func(*ytr.Response) error { return nil })
	if err != nil || !reflect.DeepEqual(pages, []string{"", "2", "3"}) {
		t.Fatalf("%v %v", err, pages)
	}
	pages = nil
	err = c.Paginate(context.Background(), ytr.OpListQueues, nil, nil, func(*ytr.Response) error { return ytr.ErrStopPagination })
	if err != nil || len(pages) != 1 {
		t.Fatal(err, pages)
	}
}

func TestPaginationFollowsCaseInsensitiveNextRelation(t *testing.T) {
	calls, visits := 0, 0
	client := client(t, func(request *http.Request) (*http.Response, error) {
		calls++
		response := reply(200, `[]`)
		if calls == 1 {
			response.Header.Set("Link", `</v3/queues?page=2>; rel="NeXt LaSt"`)
		} else if request.URL.Query().Get("page") != "2" {
			t.Errorf("wrong next page: %s", request.URL)
		}
		return response, nil
	})
	err := client.Paginate(t.Context(), ytr.OpListQueues, nil, nil, func(*ytr.Response) error {
		visits++
		return nil
	})
	if err != nil || calls != 2 || visits != 2 {
		t.Fatalf("valid next relation ended traversal early: calls=%d visits=%d error=%v", calls, visits, err)
	}
}

func TestPaginationRejectsUnsafeLinksAndCycles(t *testing.T) {
	for _, link := range []string{"https://evil.example/v3/queues?page=2", "http://api.tracker.yandex.net/v3/queues?page=2", "https://user@api.tracker.yandex.net/v3/queues?page=2", "/v3/users?page=2", "/v3/queues#fragment", "/v3/queues"} {
		t.Run(link, func(t *testing.T) {
			calls := 0
			c := client(t, func(*http.Request) (*http.Response, error) {
				calls++
				v := reply(200, `[]`)
				v.Header.Set("Link", "<"+link+">; rel=next")
				return v, nil
			})
			err := c.Paginate(context.Background(), ytr.OpListQueues, nil, nil, func(*ytr.Response) error { return nil })
			if !errors.Is(err, ytr.ErrUnsafeLink) && !errors.Is(err, ytr.ErrPaginationCycle) {
				t.Fatalf("%v", err)
			}
			if calls != 1 {
				t.Fatalf("unsafe follow: calls=%d", calls)
			}
		})
	}
}

func TestPaginationVisitorAndInputFailures(t *testing.T) {
	sentinel := errors.New("visitor")
	calls := 0
	c := client(t, func(*http.Request) (*http.Response, error) { calls++; return reply(200, `[]`), nil })
	if err := c.Paginate(context.Background(), ytr.OpListQueues, nil, nil, func(*ytr.Response) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	for _, name := range []ytr.OperationName{"unknown", ytr.OpCreateIssue, ytr.OpDownloadIssueThumbnail} {
		if err := c.Paginate(context.Background(), name, nil, nil, func(*ytr.Response) error { return nil }); err == nil {
			t.Fatal(name)
		}
	}
	if err := c.Paginate(context.Background(), ytr.OpListQueues, nil, nil, nil); err == nil {
		t.Fatal("nil visitor accepted")
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}

func TestPaginationRejectsMalformedNextQueryBeforeAnotherRequest(t *testing.T) {
	for _, query := range []string{"page=2&cursor=%zz", "page=2&cursor=%", "page=2&cursor=bad;value"} {
		t.Run(query, func(t *testing.T) {
			calls, visits := 0, 0
			client := client(t, func(*http.Request) (*http.Response, error) {
				calls++
				response := reply(200, `[]`)
				response.Header.Set("Link", "</v3/queues?"+query+">; rel=next")
				return response, nil
			})
			err := client.Paginate(t.Context(), ytr.OpListQueues, nil, nil, func(*ytr.Response) error {
				visits++
				return nil
			}, ytr.Query("perPage", "10"))
			if !errors.Is(err, ytr.ErrUnsafeLink) || calls != 1 || visits != 1 {
				t.Fatalf("malformed cursor was silently dropped: calls=%d visits=%d error=%v", calls, visits, err)
			}
		})
	}
}

func TestNextLinkParsing(t *testing.T) {
	for _, tc := range []struct {
		header, want string
		bad          bool
	}{
		{`</v3/queues?page=1>; rel="prev", </v3/queues?page=3>; rel="next"`, "/v3/queues?page=3", false},
		{`</v3/queues?page=3>; title="comma, and ; semicolon"; rel="next last"`, "/v3/queues?page=3", false},
		{`</v3/queues?page=3>; rel="NEXT last"`, "/v3/queues?page=3", false},
		{`</v3/queues?page=3>; REL="prev NeXt"`, "/v3/queues?page=3", false},
		{`</v3/queues?page=3>; rel=next; anchor="/v3/users"`, "", false},
		{`</v3/queues?page=3>; rel=next; anchor="/v3/users", </v3/queues?page=4>; rel=next`, "/v3/queues?page=4", false},
		{`</v3/queues>; rel=prev`, "", false},
		{`broken`, "", true},
		{`</v3/queues>; rel="next`, "", true},
		{`</a>; rel=next, </b>; rel=next`, "", true},
	} {
		r := &ytr.Response{Header: http.Header{"Link": {tc.header}}}
		got, err := r.NextLink()
		if got != tc.want || (err != nil) != tc.bad {
			t.Errorf("%s => %q %v", tc.header, got, err)
		}
	}
	r := &ytr.Response{Header: make(http.Header)}
	if link, err := r.NextLink(); link != "" || err != nil {
		t.Fatal(link, err)
	}
}

func TestPaginationIgnoresNextLinksWithAnotherContext(t *testing.T) {
	calls := 0
	client := client(t, func(*http.Request) (*http.Response, error) {
		calls++
		response := reply(200, `[]`)
		response.Header.Set("Link", `</v3/queues?page=2>; rel=next; anchor="/v3/users"`)
		return response, nil
	})
	err := client.Paginate(t.Context(), ytr.OpListQueues, nil, nil, func(*ytr.Response) error { return nil })
	if err != nil || calls != 1 {
		t.Fatalf("followed a next link for another context: calls=%d error=%v", calls, err)
	}
}

func TestNextLinkRejectsMalformedTargetWithQuotedParameters(t *testing.T) {
	response := &ytr.Response{Header: http.Header{"Link": {`</v3/queues">; title="a\"b"; rel=next`}}}
	link, err := response.NextLink()
	if err == nil || link != "" {
		t.Fatalf("malformed target accepted: link=%q error=%v", link, err)
	}
}

func TestPaginationParameterBoundaries(t *testing.T) {
	for _, tc := range []struct {
		total, page string
		bad         bool
		calls       int
	}{{"bad", "", true, 1}, {"-1", "", true, 1}, {"0", "", false, 1}, {"1", "1", false, 1}, {"1", "0", true, 1}, {"1", "bad", true, 1}, {"2", "1", false, 2}} {
		calls := 0
		c := client(t, func(*http.Request) (*http.Response, error) {
			calls++
			v := reply(200, `[]`)
			v.Header.Set("X-Total-Pages", tc.total)
			return v, nil
		})
		err := c.Paginate(context.Background(), ytr.OpListQueues, nil, nil, func(*ytr.Response) error { return nil }, ytr.Query("page", tc.page))
		if (err != nil) != tc.bad || calls != tc.calls {
			t.Fatalf("%+v: %v calls=%d", tc, err, calls)
		}
	}
	c := client(t, func(*http.Request) (*http.Response, error) {
		t.Error("invalid arity reached HTTP")
		return reply(200, `[]`), nil
	})
	for _, args := range [][]string{nil, {"one", "extra"}} {
		if err := c.Paginate(context.Background(), ytr.OpGetIssue, args, nil, func(*ytr.Response) error { return nil }); err == nil {
			t.Fatal(args)
		}
	}
}

func TestLinkEscapesAnglesAndShortTargets(t *testing.T) {
	for _, header := range []string{
		`<x>; rel=next`,
		`</v3/queues?q=a,b>; rel=next`,
		`</v3/queues>; title="a\"b,c"; rel=prev, </v3/queues?page=2>; rel=next`,
	} {
		r := &ytr.Response{Header: http.Header{"Link": {header}}}
		next, err := r.NextLink()
		if err != nil || next == "" {
			t.Fatal(header, next, err)
		}
	}
	if _, err := (&ytr.Response{Header: http.Header{"Link": {"broken>"}}}).NextLink(); err == nil {
		t.Fatal("missing opening angle accepted")
	}
}

func TestPaginationRejectsBodyForBodylessOperation(t *testing.T) {
	calls := 0
	client := client(t, func(*http.Request) (*http.Response, error) {
		calls++
		return reply(200, `[]`), nil
	})
	err := client.Paginate(context.Background(), ytr.OpListQueues, nil, ytr.Fields{"unexpected": true}, func(*ytr.Response) error { return nil })
	if !errors.Is(err, ytr.ErrOperationKind) || calls != 0 {
		t.Fatalf("bodyless contract bypassed: error=%v HTTP calls=%d", err, calls)
	}
}
