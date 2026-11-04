package ytr_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"runtime"
	"strings"
	"testing"

	"github.com/skarm/ytr"
)

type trackedReader struct {
	io.Reader
	closed bool
	reads  int
}

type failingReader struct{ err error }

type benchmarkDownloadBody struct {
	reader bytes.Reader
	read   *int
	closed *int
}

func TestMultipartStreamsAndPreservesFilename(t *testing.T) {
	file := &trackedReader{Reader: strings.NewReader("data\x00Привет")}
	c := client(t, func(r *http.Request) (*http.Response, error) {
		if file.reads != 0 {
			t.Error("file buffered before transport")
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		part, header, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		defer part.Close()
		data, _ := io.ReadAll(part)
		if header.Filename != `a"b Привет.txt` || string(data) != "data\x00Привет" {
			t.Error(header.Filename, string(data))
		}
		return reply(201, `{"id":"3"}`), nil
	})
	_, err := c.UploadTemporaryAttachment(context.Background(), ytr.File{Name: `a"b Привет.txt`, Reader: file})
	if err != nil {
		t.Fatal(err)
	}
	if file.closed {
		t.Fatal("caller-owned reader closed")
	}
}

func TestUploadReadFailureAndInvalidFiles(t *testing.T) {
	sentinel := errors.New("read failed")
	c := client(t, func(r *http.Request) (*http.Response, error) { _, err := io.ReadAll(r.Body); return nil, err })
	if _, err := c.UploadTemporaryAttachment(context.Background(), ytr.File{Name: "x", Reader: failingReader{sentinel}}); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	for _, file := range []ytr.File{{Name: "x"}, {Reader: strings.NewReader("")}, {Name: "a\nb", Reader: strings.NewReader("")}, {Name: "x", Reader: strings.NewReader(""), FieldName: "bad\n"}} {
		if _, err := c.UploadTemporaryAttachment(context.Background(), file); err == nil {
			t.Fatal("invalid file accepted")
		}
	}
}

func TestDownloadIsUnbufferedAndCallerCloses(t *testing.T) {
	stream := &trackedReader{Reader: strings.NewReader("content")}
	c := client(t, func(r *http.Request) (*http.Response, error) { v := reply(200, ""); v.Body = stream; return v, nil })
	download, err := c.DownloadIssueAttachment(context.Background(), "Q-1", "5", "файл.txt")
	if err != nil {
		t.Fatal(err)
	}
	if stream.reads != 0 || stream.closed {
		t.Fatal("stream consumed eagerly")
	}
	data, err := io.ReadAll(download.Body)
	if err != nil || string(data) != "content" {
		t.Fatal(string(data), err)
	}
	if err = download.Body.Close(); err != nil || !stream.closed {
		t.Fatal(err)
	}
}

func TestDownloadPreservesExplicitAcceptHeader(t *testing.T) {
	for _, test := range []struct {
		name    string
		options []ytr.CallOption
		want    string
	}{
		{"default", nil, "*/*"},
		{"custom", []ytr.CallOption{ytr.Header("accept", "application/pdf")}, "application/pdf"},
		{"empty", []ytr.CallOption{ytr.Header("Accept", "")}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			c := client(t, func(request *http.Request) (*http.Response, error) {
				calls++
				if got := request.Header.Get("Accept"); got != test.want {
					t.Errorf("Accept = %q, want %q", got, test.want)
				}
				return reply(200, "content"), nil
			})
			for _, call := range []func() (*ytr.Download, error){
				func() (*ytr.Download, error) {
					return c.DownloadIssueAttachment(t.Context(), "SDK-1", "1", "report.pdf", test.options...)
				},
				func() (*ytr.Download, error) {
					return c.Download(t.Context(), ytr.OpDownloadIssueAttachment, []string{"SDK-1", "1", "report.pdf"}, test.options...)
				},
			} {
				download, err := call()
				if err != nil {
					t.Fatal(err)
				}
				if err := download.Body.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if calls != 2 {
				t.Fatalf("requests = %d, want 2", calls)
			}
		})
	}
}

func TestResponsesAlwaysCloseAndRespectExactLimit(t *testing.T) {
	for _, tc := range []struct {
		code int
		body string
		want error
	}{{200, `1234`, nil}, {200, `12345`, ytr.ErrResponseTooLarge}, {200, `oops`, ytr.ErrInvalidResponse}, {204, "", nil}, {302, "redirect", nil}, {404, `{"errorMessages":["missing"]}`, nil}} {
		t.Run(tc.body, func(t *testing.T) {
			stream := &trackedReader{Reader: strings.NewReader(tc.body)}
			c, err := ytr.New(ytr.Config{Organization: ytr.Organization{Kind: ytr.Organization360, ID: "1"}, TokenSource: ytr.OAuthToken("t"), MaxResponseBytes: 4, HTTPClient: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { v := reply(tc.code, ""); v.Body = stream; return v, nil })}})
			if err != nil {
				t.Fatal(err)
			}
			response, err := c.GetCurrentUser(context.Background())
			if tc.code >= 300 {
				var api *ytr.APIError
				if !errors.As(err, &api) || api.StatusCode != tc.code {
					t.Fatal(err)
				}
			} else if !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
			if !stream.closed || len(response.Body) > 4 {
				t.Fatal("body leaked or exceeded bound")
			}
		})
	}
}

func TestFailedDownloadClosesBody(t *testing.T) {
	stream := &trackedReader{Reader: strings.NewReader(`{"errorMessages":["missing"]}`)}
	c := client(t, func(*http.Request) (*http.Response, error) { v := reply(404, ""); v.Body = stream; return v, nil })
	download, err := c.DownloadIssueThumbnail(context.Background(), "Q-1", "2")
	var api *ytr.APIError
	if download != nil || !errors.As(err, &api) || api.StatusCode != 404 || !stream.closed {
		t.Fatal(download, err, stream.closed)
	}
}

func TestPublicFileCallsValidateBeforeTransport(t *testing.T) {
	c := client(t, func(*http.Request) (*http.Response, error) {
		t.Error("invalid request reached HTTP")
		return reply(200, "{}"), nil
	})
	file := ytr.File{Name: "file.txt", Reader: strings.NewReader("data")}
	if _, err := c.Upload(context.Background(), "unknown", nil, file); !errors.Is(err, ytr.ErrUnknownOperation) {
		t.Fatal(err)
	}
	if _, err := c.Download(context.Background(), "unknown", nil); !errors.Is(err, ytr.ErrUnknownOperation) {
		t.Fatal(err)
	}
	if _, err := c.Upload(context.Background(), ytr.OpUploadIssueAttachment, nil, file); err == nil {
		t.Fatal("missing issue ID accepted")
	}
	if _, err := c.Download(context.Background(), ytr.OpDownloadIssueThumbnail, nil); err == nil {
		t.Fatal("missing IDs accepted")
	}
	if _, err := c.Upload(context.Background(), ytr.OpUploadTemporaryAttachment, nil, file, nil); err == nil {
		t.Fatal("nil option accepted")
	}
	if _, err := c.Download(context.Background(), ytr.OpDownloadIssueThumbnail, []string{"SDK-1", "1"}, nil); err == nil {
		t.Fatal("nil option accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Upload(ctx, ytr.OpUploadTemporaryAttachment, nil, file); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := c.Download(ctx, ytr.OpDownloadIssueThumbnail, []string{"SDK-1", "1"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestPublicFileCallsPreserveTransportErrors(t *testing.T) {
	sentinel := errors.New("network unavailable")
	c := client(t, func(*http.Request) (*http.Response, error) { return nil, sentinel })
	if _, err := c.Upload(context.Background(), ytr.OpUploadTemporaryAttachment, nil, ytr.File{Name: "file.txt", Reader: strings.NewReader("data")}); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if _, err := c.Download(context.Background(), ytr.OpDownloadIssueThumbnail, []string{"SDK-1", "1"}); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}

func TestUploadIntoStreamsInputAndClosesOnlyResponse(t *testing.T) {
	input := &streamingBody{reader: strings.NewReader("binary\x00payload")}
	output := &streamingBody{reader: strings.NewReader(`{"id":"upload-1"}`)}
	c := client(t, func(req *http.Request) (*http.Response, error) {
		if input.reads != 0 {
			t.Error("upload input was read before transport")
		}
		if req.Method != "POST" || req.URL.Path != "/v3/attachments" || req.URL.Query().Get("filename") != "explicit.bin" {
			t.Errorf("request: %s %s", req.Method, req.URL)
		}
		multipart, err := req.MultipartReader()
		if err != nil {
			t.Fatal(err)
		}
		part, err := multipart.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(part)
		if err != nil {
			t.Fatal(err)
		}
		if part.FormName() != "payload" || part.FileName() != "source.bin" || string(data) != "binary\x00payload" {
			t.Errorf("multipart=%s %s %q", part.FormName(), part.FileName(), data)
		}
		return &http.Response{StatusCode: 201, Body: output}, nil
	})
	var out struct {
		ID string `json:"id"`
	}
	meta, err := c.UploadInto(context.Background(), ytr.OpUploadTemporaryAttachment, nil, ytr.File{
		Name: "source.bin", FieldName: "payload", Reader: input,
	}, &out, ytr.Query("filename", "explicit.bin"))
	if err != nil || meta == nil || meta.StatusCode != 201 || out.ID != "upload-1" || input.closes != 0 || input.reads == 0 || output.closes != 1 {
		t.Fatalf("meta=%+v out=%+v error=%v input=%+v output=%+v", meta, out, err, input, output)
	}
}

func TestMultipartValidationPreventsHTTP(t *testing.T) {
	calls := 0
	c := client(t, func(*http.Request) (*http.Response, error) { calls++; return reply(200, `{}`), nil })
	for _, file := range []ytr.File{{Reader: strings.NewReader("")}, {Name: "x"}, {Name: "x\n", Reader: strings.NewReader("")}} {
		if _, err := c.UploadTemporaryAttachment(context.Background(), file); err == nil {
			t.Fatal(file.Name)
		}
	}
	if calls != 0 {
		t.Fatal("invalid upload reached HTTP", calls)
	}
}

// BenchmarkDownloadStart measures request creation and closing an unread response. It
// excludes file transfer and does not report transfer throughput.
func BenchmarkDownloadStart(b *testing.B) {
	for _, size := range []struct {
		name  string
		bytes int
	}{
		{"1KiB", 1 << 10},
		{"8MiB", 8 << 20},
	} {
		b.Run(size.name, func(b *testing.B) {
			data := bytes.Repeat([]byte("x"), size.bytes)
			var read, closed, calls int
			c := newBenchmarkClient(b, func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{
					StatusCode:    http.StatusOK,
					Header:        make(http.Header),
					ContentLength: int64(len(data)),
					Body:          &benchmarkDownloadBody{reader: *bytes.NewReader(data), read: &read, closed: &closed},
				}, nil
			})
			ctx := b.Context()
			args := []string{"SDK-1", "1", "attachment.bin"}
			b.ReportAllocs()
			for b.Loop() {
				download, err := c.Download(ctx, ytr.OpDownloadIssueAttachment, args)
				if err != nil {
					b.Fatal(err)
				}
				if err = download.Body.Close(); err != nil {
					b.Fatal(err)
				}
			}
			if read != 0 || closed != b.N || calls != b.N {
				b.Fatalf("bytes read=%d, closed=%d, requests=%d; want 0, %d, %d", read, closed, calls, b.N, b.N)
			}
			runtime.KeepAlive(data)
		})
	}
}

func (r *trackedReader) Read(p []byte) (int, error) { r.reads++; return r.Reader.Read(p) }

func (r *trackedReader) Close() error { r.closed = true; return nil }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func (body *benchmarkDownloadBody) Read(p []byte) (int, error) {
	n, err := body.reader.Read(p)
	*body.read += n
	return n, err
}

func (body *benchmarkDownloadBody) Close() error {
	*body.closed += 1
	return nil
}
