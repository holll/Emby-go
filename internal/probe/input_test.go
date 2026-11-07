package probe

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"emby-go/internal/outbound"
)

func TestDownloadInputBlocksPrivateAndEnvironmentProxy(t *testing.T) {
	// ProxyFromEnvironment caches process-wide state. Run the differential
	// control in a fresh test process so earlier tests and -count cannot mask it.
	if os.Getenv("EMBY_GO_TEST_INPUT_PROXY_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDownloadInputBlocksPrivateAndEnvironmentProxy$", "-test.count=1")
		cmd.Env = append(os.Environ(), "EMBY_GO_TEST_INPUT_PROXY_CHILD=1", "GORACE=atexit_sleep_ms=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated proxy test: %v\n%s", err, out)
		}
		return
	}
	var protectedHits, proxyHits atomic.Int64
	protected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		protectedHits.Add(1)
		_, _ = io.WriteString(w, "private content")
	}))
	defer protected.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyHits.Add(1)
		_, _ = io.WriteString(w, "proxied content")
	}))
	defer proxy.Close()
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		t.Setenv(key, proxy.URL)
	}
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")

	// Check the actual fixed transport's reason as well as the public error;
	// a mere connection failure is not proof of address-policy enforcement.
	req, err := http.NewRequest(http.MethodGet, protected.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := inputHTTPClient.Do(req); err == nil || !strings.Contains(err.Error(), "blocked address") {
		if resp != nil {
			resp.Body.Close()
		}
		t.Fatalf("private transport policy: %v", err)
	}
	for _, target := range []string{
		protected.URL + "/secret?token=credential-canary",
		"http://10.0.0.1/secret?token=credential-canary",
		"http://169.254.169.254/latest/meta-data/credential-canary",
		"http://[::1]/credential-canary",
		"file:///credential-canary",
		"http://user:credential-canary@upstream.invalid/secret?token=credential-canary",
		"://credential-canary",
	} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		data, err := downloadInput(ctx, target)
		cancel()
		if err == nil || data != nil || strings.Contains(err.Error(), "credential-canary") {
			t.Fatalf("untrusted input exposed/accepted: data=%d err=%v", len(data), err)
		}
	}
	if protectedHits.Load() != 0 || proxyHits.Load() != 0 {
		t.Fatalf("protected=%d proxy=%d", protectedHits.Load(), proxyHits.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if data, err := downloadInput(ctx, protected.URL); data != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request: data=%d err=%v", len(data), err)
	}
	// A non-loopback, unresolved hostname must reach the configured proxy with
	// the ordinary outbound profile. The fixed probe profile above must not.
	controlCtx, controlCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer controlCancel()
	controlReq, err := http.NewRequestWithContext(controlCtx, http.MethodGet, "http://upstream.invalid/control", nil)
	if err != nil {
		t.Fatal(err)
	}
	control := outbound.NewClient(outbound.Options{})
	resp, err := control.Do(controlReq)
	if err != nil {
		t.Fatalf("environment proxy positive control: %v", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil || string(data) != "proxied content" || proxyHits.Load() != 1 || protectedHits.Load() != 0 {
		t.Fatalf("proxy control: body=%q err=%v proxy=%d protected=%d", data, err, proxyHits.Load(), protectedHits.Load())
	}
}

type countedInput struct {
	remaining int64
	read      int64
}

func (r *countedInput) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := int(min(int64(len(p)), r.remaining))
	clear(p[:n])
	r.remaining -= int64(n)
	r.read += int64(n)
	return n, nil
}

type failingInput struct{ err error }

func (r failingInput) Read([]byte) (int, error) { return 0, r.err }

func TestReadInputExactAndOverflow(t *testing.T) {
	if maxInputSize != 64<<20 {
		t.Fatal("finite input budget changed")
	}
	for _, tc := range []struct {
		name         string
		length, body int64
		want         error
		read         int64
	}{
		{"exact-known", maxInputSize, maxInputSize, nil, maxInputSize},
		{"exact-chunked", -1, maxInputSize, nil, maxInputSize},
		{"over-known", maxInputSize + 1, maxInputSize + 1, errInputTooLarge, 0},
		{"over-chunked", -1, maxInputSize + 2, errInputTooLarge, maxInputSize + 1},
		{"over-lying-length", 3, maxInputSize + 2, errInputTooLarge, maxInputSize + 1},
		{"short-known", 5, 3, errInputRead, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := &countedInput{remaining: tc.body}
			resp := &http.Response{StatusCode: http.StatusOK, ContentLength: tc.length, Body: io.NopCloser(reader)}
			data, err := readInput(context.Background(), resp)
			if !errors.Is(err, tc.want) || reader.read != tc.read {
				t.Fatalf("error=%v read=%d; want error=%v read=%d", err, reader.read, tc.want, tc.read)
			}
			if tc.want != nil && data != nil {
				t.Fatal("failed input returned a usable prefix")
			}
			if tc.want == nil && int64(len(data)) != tc.body {
				t.Fatalf("complete input length=%d want=%d", len(data), tc.body)
			}
		})
	}
	resp := &http.Response{
		StatusCode: http.StatusOK, ContentLength: -1,
		Body: io.NopCloser(io.MultiReader(strings.NewReader("prefix"), failingInput{errors.New("credential-canary")})),
	}
	if data, err := readInput(context.Background(), resp); data != nil || !errors.Is(err, errInputRead) || strings.Contains(err.Error(), "credential-canary") {
		t.Fatalf("read failure: data=%d err=%v", len(data), err)
	}
}

func TestReadInputActualHTTPResponses(t *testing.T) {
	payload := []byte("complete finite input")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write(payload)
		case "/chunked":
			w.(http.Flusher).Flush()
			_, _ = w.Write(payload)
		case "/partial":
			w.Header().Set("Content-Range", "bytes 0-2/1024")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("abc"))
		case "/partial-200":
			w.Header().Set("Content-Range", "bytes 0-2/1024")
			_, _ = w.Write([]byte("abc"))
		case "/partial-empty":
			w.Header().Set("Content-Range", "")
			_, _ = w.Write([]byte("abc"))
		case "/partial-duplicate":
			w.Header().Add("Content-Range", "")
			w.Header().Add("Content-Range", "bytes 0-2/1024")
			_, _ = w.Write([]byte("abc"))
		case "/truncated":
			w.Header().Set("Content-Length", "1024")
			_, _ = w.Write([]byte("abc"))
		case "/cancel":
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		default:
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("credential-canary"))
		}
	}))
	defer server.Close()
	// This client intentionally reaches a local fixture to test HTTP response
	// framing/body failures, not the production public-address policy above.
	for _, path := range []string{"/ok", "/chunked", "/partial", "/partial-200", "/partial-empty", "/partial-duplicate", "/truncated", "/status", "/cancel"} {
		t.Run(path, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if path == "/cancel" {
				cancel()
			}
			data, err := readInput(ctx, resp)
			if path == "/ok" || path == "/chunked" {
				if err != nil || !bytes.Equal(data, payload) {
					t.Fatalf("finite response: data=%q err=%v", data, err)
				}
				return
			}
			if err == nil || data != nil || strings.Contains(err.Error(), "credential-canary") {
				t.Fatalf("invalid response: data=%d err=%v", len(data), err)
			}
			if path == "/cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
		})
	}
}
