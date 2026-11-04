package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skarm/ytr"
)

type memoryTransport func(*http.Request) (*http.Response, error)

type closeReader struct {
	io.Reader
	close func()
}

type errorWriter struct{ err error }

func TestEveryCatalogOperationCanBeInvokedByTheCLI(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(file, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, op := range ytr.Operations() {
		t.Run(string(op.Name), func(t *testing.T) {
			var args []string
			if op.Request == "json" {
				args = append(args, "-data", `{"fixture":true}`)
			}
			if op.Request == "multipart" {
				args = append(args, "-file", file)
			}
			args = append(args, string(op.Name))
			path := op.Path
			for _, name := range op.PathParameters() {
				value := "fixture"
				if name == "entityType" {
					value = "goal"
				}
				args = append(args, value)
				path = strings.ReplaceAll(path, "{"+name+"}", value)
			}
			calls := 0
			r, out := command(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != op.Method || req.URL.Path != path {
					t.Error(req.Method, req.URL, path)
				}
				if op.Response == "stream" {
					return response(200, "binary"), nil
				}
				return response(200, `{"fixture":true}`), nil
			})
			if err := r.run(context.Background(), args); err != nil {
				t.Fatal(err)
			}
			want := "{\"fixture\":true}\n"
			if op.Response == "stream" {
				want = "binary"
			}
			if calls != 1 || out.String() != want {
				t.Fatal(calls, out.String())
			}
		})
	}
}

func TestCLIJSONOperationUsesPackageAndPreservesQuery(t *testing.T) {
	r, out := command(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		if req.Method != "POST" || req.URL.Path != "/v3/issues" || string(body) != `{"summary":"CLI","queue":"SDK"}` || req.URL.Query().Get("notify") != "false" || req.Header.Get("Authorization") != "OAuth secret" {
			t.Error(req.Method, req.URL, string(body), req.Header)
		}
		return response(201, `{ "key": "SDK-1" }`), nil
	})
	err := r.run(context.Background(), []string{"-data", `{"summary":"CLI","queue":"SDK"}`, "-query", "notify=false", "CreateIssue"})
	if err != nil || out.String() != `{"key":"SDK-1"}`+"\n" {
		t.Fatal(out.String(), err)
	}
}

func TestCLILocalDiscoveryDoesNotReadCredentials(t *testing.T) {
	r, out := command(nil)
	r.getenv = func(string) string { t.Fatal("local discovery requested credentials"); return "" }
	if err := r.run(context.Background(), []string{"operations"}); err != nil {
		t.Fatal(err)
	}
	var operations []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(out.Bytes(), &operations); err != nil || len(operations) != len(ytr.Operations()) {
		t.Fatal(len(operations), err)
	}
	out.Reset()
	if err := r.run(context.Background(), []string{"help", "GetIssue"}); err != nil || !strings.Contains(out.String(), "issueID") {
		t.Fatal(out.String(), err)
	}
}

func TestCLIStdinJSONAndMultipart(t *testing.T) {
	r, out := command(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/v3/attachments" || req.URL.Query().Get("filename") != "stdin.txt" {
			t.Error(req.URL)
		}
		reader, err := req.MultipartReader()
		if err != nil {
			t.Fatal(err)
		}
		part, err := reader.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(part)
		if part.FormName() != "file" || part.FileName() != "stdin.txt" || string(body) != "binary\x00body" {
			t.Error(part.FormName(), part.FileName(), string(body))
		}
		return response(201, `{"id":"1"}`), nil
	})
	r.stdin = strings.NewReader("binary\x00body")
	if err := r.run(context.Background(), []string{"-file", "-", "-filename", "stdin.txt", "UploadTemporaryAttachment"}); err != nil {
		t.Fatal(err)
	}
	if out.String() != `{"id":"1"}`+"\n" {
		t.Fatal(out.String())
	}
	r, _ = command(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		if string(body) != `{"queue":"SDK"}` {
			t.Error(string(body))
		}
		return response(200, "[]"), nil
	})
	r.stdin = strings.NewReader(`{"queue":"SDK"}`)
	if err := r.run(context.Background(), []string{"-data", "-", "SearchIssues"}); err != nil {
		t.Fatal(err)
	}
}

func TestCLIDownloadStreamsToOutput(t *testing.T) {
	closed := false
	r, out := command(func(req *http.Request) (*http.Response, error) {
		v := response(200, "")
		v.Body = closeReader{Reader: strings.NewReader("binary\x00data"), close: func() { closed = true }}
		return v, nil
	})
	err := r.run(context.Background(), []string{"DownloadIssueAttachment", "SDK-1", "2", "file.txt"})
	if err != nil || out.String() != "binary\x00data" || !closed {
		t.Fatal(out.String(), closed, err)
	}
}

func TestCLIRejectsInvalidInputsBeforeHTTP(t *testing.T) {
	for _, args := range [][]string{{"unknown"}, {"GetIssue"}, {"GetCurrentUser", "extra"}, {"-data", "{}", "GetCurrentUser"}, {"-file", "x", "CreateIssue"}, {"-data", "{", "CreateIssue"}, {"-query", "missing-equals", "GetCurrentUser"}, {"-header", "missing-colon", "GetCurrentUser"}, {"-timeout", "0", "GetCurrentUser"}, {"-all", "CreateIssue"}, {"-file", "-", "UploadTemporaryAttachment"}, {"-file", "-", "-filename", "x", "-data", "{}", "UploadTemporaryAttachment"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			r, _ := command(func(*http.Request) (*http.Response, error) {
				t.Error("invalid input reached HTTP")
				return response(200, "{}"), nil
			})
			if err := r.run(context.Background(), args); exitCode(err) != 2 {
				t.Fatal(err)
			}
		})
	}
}

func TestCLIReportsInvalidRequestValuesAsUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"-base-url", "http://:8080", "GetCurrentUser"},
		{"-base-url", "https://[]", "GetCurrentUser"},
		{"-base-url", "https://[]:443", "GetCurrentUser"},
		{"-header", "bad name: value", "GetCurrentUser"},
		{"-header", "X-Note: value\nother", "GetCurrentUser"},
		{"-header", "Authorization: value", "GetCurrentUser"},
		{"-header", "X-Org-ID: value", "GetCurrentUser"},
		{"-header", "X-Cloud-Org-ID: value", "GetCurrentUser"},
		{"-header", "Host: fixture.test", "GetCurrentUser"},
		{"-header", "Content-Type: application/json", "GetCurrentUser"},
		{"-header", "Content-Length: 1", "GetCurrentUser"},
		{"-header", "Transfer-Encoding: chunked", "GetCurrentUser"},
		{"GetIssue", "."},
		{"GetIssue", ".."},
		{"GetIssue", ""},
		{"GetEntity", "user", "1"},
		{"-file", "-", "-filename", "bad\nname", "UploadTemporaryAttachment"},
		{"-file", "-", "-filename", "file.txt", "-field", "bad\nfield", "UploadTemporaryAttachment"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			r, _ := command(func(*http.Request) (*http.Response, error) {
				t.Error("invalid request reached HTTP")
				return response(200, `{}`), nil
			})
			if err := r.run(t.Context(), args); exitCode(err) != 2 {
				t.Fatalf("invalid request exit = %d, want 2: %v", exitCode(err), err)
			}
		})
	}
}

func TestCLIHeaderErrorsDoNotEchoValues(t *testing.T) {
	const secret = "private-header-value"
	for _, test := range []struct{ name, header string }{
		{"managed", "Authorization: " + secret},
		{"invalid value", "X-Note: " + secret + "\nother"},
		{"invalid name", "bad name: " + secret},
		{"missing colon", secret},
	} {
		t.Run(test.name, func(t *testing.T) {
			r, _ := command(func(*http.Request) (*http.Response, error) {
				t.Error("invalid header reached HTTP")
				return response(200, `{}`), nil
			})
			diagnostics := new(bytes.Buffer)
			r.stderr = diagnostics
			err := r.run(t.Context(), []string{"-header", test.header, "GetCurrentUser"})
			if exitCode(err) != 2 || strings.Contains(err.Error(), secret) || strings.Contains(diagnostics.String(), secret) {
				t.Fatalf("header value leaked or usage error misclassified: %v; diagnostics: %s", err, diagnostics)
			}
		})
	}
}

func TestCLIFileInputAndPagination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "body.json")
	if err := os.WriteFile(path, []byte(`{"queue":"SDK"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	r, out := command(func(req *http.Request) (*http.Response, error) {
		calls++
		body, _ := io.ReadAll(req.Body)
		if req.Method != "POST" || string(body) != `{"queue":"SDK"}` {
			t.Error(req.Method, string(body))
		}
		v := response(200, `[{"key":"SDK-1"}]`)
		if calls == 1 {
			v.Header.Set("Link", `</v3/issues/_search?id=next>; rel=next`)
		}
		return v, nil
	})
	if err := r.run(context.Background(), []string{"-all", "-data", "@" + path, "SearchIssues"}); err != nil || calls != 2 || strings.Count(out.String(), "\n") != 2 {
		t.Fatal(out.String(), calls, err)
	}
}

func TestCLIErrorsAndExitCodes(t *testing.T) {
	sentinel := errors.New("output failed")
	r, _ := command(func(*http.Request) (*http.Response, error) { return response(200, "{}"), nil })
	r.stdout = errorWriter{sentinel}
	if err := r.run(context.Background(), []string{"GetCurrentUser"}); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	r, _ = command(func(*http.Request) (*http.Response, error) { return response(403, `{"errorMessages":["denied"]}`), nil })
	if err := r.run(context.Background(), []string{"GetCurrentUser"}); exitCode(err) != 1 || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
	if exitCode(nil) != 0 {
		t.Fatal("nil error exit status")
	}
}

func TestAllCannotBeSilentlyIgnoredForUploads(t *testing.T) {
	r, _ := command(func(*http.Request) (*http.Response, error) {
		t.Error("invalid pagination reached HTTP")
		return response(200, "{}"), nil
	})
	if err := r.run(context.Background(), []string{"-all", "-file", "-", "-filename", "x", "UploadTemporaryAttachment"}); exitCode(err) != 2 {
		t.Fatal(err)
	}
}

func TestRepeatedQueriesHeadersAndCloudCredentials(t *testing.T) {
	r, _ := command(func(req *http.Request) (*http.Response, error) {
		if values := req.URL.Query()["createdAt"]; len(values) != 2 || values[0] != "from:2026-01-01" || values[1] != "to:2026-12-31" {
			t.Error(req.URL)
		}
		if req.Header.Get("If-Match") != `"7"` || req.Header.Get("Authorization") != "Bearer cloud-token" || req.Header.Get("X-Cloud-Org-Id") != "cloud-org" || req.Header.Get("X-Org-Id") != "" {
			t.Error(req.Header)
		}
		return response(200, "[]"), nil
	})
	r.getenv = func(name string) string {
		return map[string]string{"YTR_TOKEN": "cloud-token", "YTR_ORG_ID": "cloud-org", "YTR_ORG_TYPE": "cloud", "YTR_AUTH_SCHEME": "Bearer"}[name]
	}
	if err := r.run(context.Background(), []string{"-query", "createdAt=from:2026-01-01", "-query", "createdAt=to:2026-12-31", "-header", `If-Match: "7"`, "ListWorklogs"}); err != nil {
		t.Fatal(err)
	}
}

func TestFileUploadOverridesNameAndFieldExplicitly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(path, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, _ := command(func(req *http.Request) (*http.Response, error) {
		if req.URL.Query().Get("filename") != "override.txt" {
			t.Error(req.URL)
		}
		parts, err := req.MultipartReader()
		if err != nil {
			t.Fatal(err)
		}
		part, err := parts.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		if part.FormName() != "custom" || part.FileName() != "source.txt" {
			t.Error(part.FormName(), part.FileName())
		}
		body, _ := io.ReadAll(part)
		if string(body) != "content" {
			t.Error(string(body))
		}
		return response(201, "{}"), nil
	})
	if err := r.run(context.Background(), []string{"-file", path, "-field", "custom", "-query", "filename=override.txt", "UploadTemporaryAttachment"}); err != nil {
		t.Fatal(err)
	}
}

func TestCommandErrorsNeverTurnIntoSuccessfulEmptyOutput(t *testing.T) {
	for _, args := range [][]string{{"operations", "extra"}, {"help", "GetIssue", "extra"}, {"help", "Unknown"}, {"-unknown"}, {"-data", "@/missing/ytr-body", "CreateIssue"}, {"-file", "/missing/ytr-file", "UploadTemporaryAttachment"}} {
		r, _ := command(func(*http.Request) (*http.Response, error) {
			t.Error("invalid input reached HTTP")
			return response(200, "{}"), nil
		})
		if err := r.run(context.Background(), args); err == nil {
			t.Fatal(args)
		}
	}
	r, out := command(nil)
	if err := r.run(context.Background(), nil); err != nil || !strings.Contains(out.String(), "Usage:") {
		t.Fatal(err, out.String())
	}
	if err := r.run(context.Background(), []string{"help"}); err != nil {
		t.Fatal(err)
	}
	if err := r.run(context.Background(), []string{"-h"}); err != nil {
		t.Fatal(err)
	}
}

func TestCommandRequiresCredentialsOnlyForRequests(t *testing.T) {
	for _, env := range []map[string]string{
		{},
		{"YTR_TOKEN": "t", "YTR_ORG_ID": "1", "YTR_AUTH_SCHEME": "Basic"},
		{"YTR_TOKEN": "t", "YTR_ORG_ID": "1", "YTR_ORG_TYPE": "unknown"},
		{"YTR_TOKEN": "secret token", "YTR_ORG_ID": "1"},
		{"YTR_TOKEN": "secret\x00", "YTR_ORG_ID": "1"},
		{"YTR_TOKEN": "secret\t", "YTR_ORG_ID": "1"},
		{"YTR_TOKEN": "secret\x7f", "YTR_ORG_ID": "1"},
	} {
		r, _ := command(func(*http.Request) (*http.Response, error) {
			t.Error("invalid config reached HTTP")
			return response(200, "{}"), nil
		})
		r.getenv = func(name string) string { return env[name] }
		if err := r.run(context.Background(), []string{"GetCurrentUser"}); exitCode(err) != 2 {
			t.Fatal(err)
		}
	}
	r, _ := command(func(*http.Request) (*http.Response, error) {
		t.Error("cancelled call reached HTTP")
		return response(200, "{}"), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.run(ctx, []string{"GetCurrentUser"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestTimeoutFlagControlsTheRealHTTPClient(t *testing.T) {
	r, _ := command(nil)
	r.httpClient = nil
	original := http.DefaultTransport
	http.DefaultTransport = memoryTransport(func(req *http.Request) (*http.Response, error) {
		deadline, ok := req.Context().Deadline()
		if !ok || time.Until(deadline) < time.Minute {
			t.Error("-timeout 2m was capped by the SDK's default HTTP timeout")
		}
		return response(200, "{}"), nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	if err := r.run(context.Background(), []string{"-timeout", "2m", "GetCurrentUser"}); err != nil {
		t.Fatal(err)
	}
}

func TestPositiveTimeoutBoundaryAndUnusedFileFlags(t *testing.T) {
	s, _, err := parseFlags([]string{"-timeout", "1ns"}, io.Discard)
	if err != nil || s.timeout != time.Nanosecond {
		t.Fatal(s.timeout, err)
	}
	for _, args := range [][]string{{"-filename", "x", "CreateIssue"}, {"-field", "custom", "GetCurrentUser"}} {
		r, _ := command(func(*http.Request) (*http.Response, error) {
			t.Error("unused file flag reached HTTP")
			return response(200, "{}"), nil
		})
		if err := r.run(context.Background(), args); exitCode(err) != 2 {
			t.Fatal(args, err)
		}
	}
}

func TestZeroJSONAndEmptyResponsesStayDistinct(t *testing.T) {
	for _, tc := range []struct {
		status                int
		body, operation, want string
		wantErr               error
	}{
		{200, "0", "CountIssues", "0\n", nil},
		{204, "", "GetCurrentUser", "", nil},
		{200, " \t\r\n", "GetCurrentUser", "", nil},
		{200, "\u00a0", "GetCurrentUser", "", ytr.ErrInvalidResponse},
		{200, "\v", "GetCurrentUser", "", ytr.ErrInvalidResponse},
	} {
		r, out := command(func(*http.Request) (*http.Response, error) { return response(tc.status, tc.body), nil })
		if err := r.run(context.Background(), []string{tc.operation}); !errors.Is(err, tc.wantErr) || out.String() != tc.want {
			t.Fatalf("status=%d body=%q: output=%q error=%v; want output=%q error=%v", tc.status, tc.body, out.String(), err, tc.want, tc.wantErr)
		}
	}
}

func (f memoryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func (r closeReader) Close() error { r.close(); return nil }

func (w errorWriter) Write([]byte) (int, error) { return 0, w.err }

func response(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func command(fn memoryTransport) (runner, *bytes.Buffer) {
	out, diagnostic := new(bytes.Buffer), new(bytes.Buffer)
	return runner{stdin: strings.NewReader(""), stdout: out, stderr: diagnostic, getenv: func(name string) string {
		return map[string]string{"YTR_TOKEN": "secret", "YTR_ORG_ID": "42", "YTR_ORG_TYPE": "360", "YTR_AUTH_SCHEME": "OAuth"}[name]
	}, httpClient: &http.Client{Transport: fn}}, out
}

type blockedCLIStream struct {
	started, closed chan struct{}
	start, close    sync.Once
	closeErr        error
}

func TestCLICancelUnblocksOwnedStreams(t *testing.T) {
	for _, kind := range []string{"JSON stdin", "multipart stdin", "download stdout", "operations stdout"} {
		t.Run(kind, func(t *testing.T) {
			stream := newBlockedCLIStream()
			stream.closeErr = errors.New("fixture close failure")
			defer stream.Close()
			r, _ := command(func(request *http.Request) (*http.Response, error) {
				if kind == "multipart stdin" {
					_, err := io.Copy(io.Discard, request.Body)
					if err != nil {
						return nil, err
					}
				}
				return response(200, "download"), nil
			})
			args := []string{"-data", "-", "SearchIssues"}
			switch kind {
			case "JSON stdin":
				r.stdin = stream
			case "multipart stdin":
				r.stdin = stream
				args = []string{"-file", "-", "-filename", "x", "UploadTemporaryAttachment"}
			case "download stdout":
				r.stdout = stream
				args = []string{"DownloadIssueAttachment", "SDK-1", "1", "x"}
			case "operations stdout":
				r.stdout = stream
				args = []string{"operations"}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- r.run(ctx, args) }()
			select {
			case <-stream.started:
			case <-time.After(2 * time.Second):
				t.Fatal("stream was not used")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel cause lost: %v", err)
				}
			case <-time.After(time.Second):
				stream.Close()
				<-done
				t.Fatal("cancellation left CLI blocked on owned stream")
			}
		})
	}
}

func TestCLIDeadlineUnblocksJSONStdin(t *testing.T) {
	stream := newBlockedCLIStream()
	defer stream.Close()
	r, _ := command(nil)
	r.stdin = stream
	done := make(chan error, 1)
	go func() {
		done <- r.run(context.Background(), []string{"-timeout", "100ms", "-data", "-", "SearchIssues"})
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("deadline cause lost: %v", err)
		}
	case <-time.After(2 * time.Second):
		stream.Close()
		<-done
		t.Fatal("operation deadline left CLI blocked on stdin")
	}
}

func TestCLISuccessDoesNotCloseInputStream(t *testing.T) {
	stream := newBlockedCLIStream()
	defer stream.Close()
	r, _ := command(func(*http.Request) (*http.Response, error) { return response(200, `{}`), nil })
	r.stdin = stream
	if err := r.run(context.Background(), []string{"GetCurrentUser"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stream.closed:
		t.Fatal("successful operation closed stdin")
	default:
	}
}

func TestCLIDeadlineUnblocksInputFiles(t *testing.T) {
	mkfifo, err := exec.LookPath("mkfifo")
	if err != nil {
		t.Skip("named pipes require the mkfifo utility")
	}
	for _, kind := range []string{"JSON open", "JSON read", "multipart open", "multipart read"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "input.fifo")
			if output, err := exec.CommandContext(t.Context(), mkfifo, path).CombinedOutput(); err != nil {
				t.Fatalf("create FIFO: %v: %s", err, output)
			}
			var writer *os.File
			if kind == "JSON read" || kind == "multipart read" {
				writer, err = os.OpenFile(path, os.O_RDWR, 0)
				if err != nil {
					t.Fatal(err)
				}
			}
			defer func() {
				if writer == nil {
					writer, err = os.OpenFile(path, os.O_RDWR, 0)
					if err != nil {
						t.Error(err)
						return
					}
				}
				_ = writer.Close()
			}()
			r, _ := command(func(request *http.Request) (*http.Response, error) {
				if kind == "multipart open" || kind == "multipart read" {
					_, err := io.Copy(io.Discard, request.Body)
					if err != nil {
						return nil, err
					}
				} else {
					t.Error("incomplete JSON reached HTTP")
				}
				return response(200, `{}`), nil
			})
			args := []string{"-timeout", "100ms", "-data", "@" + path, "SearchIssues"}
			if kind == "multipart open" || kind == "multipart read" {
				args = []string{"-timeout", "100ms", "-file", path, "UploadTemporaryAttachment"}
			}
			done := make(chan error, 1)
			go func() { done <- r.run(context.Background(), args) }()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) || exitCode(err) != 1 {
					t.Fatalf("input deadline error=%v exit=%d; want deadline exceeded and exit 1", err, exitCode(err))
				}
			case <-time.After(2 * time.Second):
				t.Error("operation remained blocked on an input file past its deadline")
				if writer == nil {
					writer, err = os.OpenFile(path, os.O_RDWR, 0)
					if err != nil {
						t.Fatal(err)
					}
				}
				_ = writer.Close()
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Fatal("input file operation did not stop after cleanup")
				}
			}
		})
	}
}

func newBlockedCLIStream() *blockedCLIStream {
	return &blockedCLIStream{started: make(chan struct{}), closed: make(chan struct{})}
}

func (s *blockedCLIStream) Read([]byte) (int, error) {
	s.start.Do(func() { close(s.started) })
	<-s.closed
	return 0, io.ErrClosedPipe
}
func (s *blockedCLIStream) Write([]byte) (int, error) { return s.Read(nil) }
func (s *blockedCLIStream) Close() error              { s.close.Do(func() { close(s.closed) }); return s.closeErr }
