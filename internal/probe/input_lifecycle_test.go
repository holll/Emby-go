package probe

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// These observers instrument real local HTTP bodies, not the production public
// address profile. This package has no parallel tests; the fixed client is
// replaced only for each synchronous subtest and restored after its worker joins.
type inputBodyObserver struct {
	io.ReadCloser
	bytesRead int
	closes    int
	blocked   chan struct{}
	signaled  bool
}

func (b *inputBodyObserver) Read(p []byte) (int, error) {
	if b.bytesRead > 0 && b.blocked != nil && !b.signaled {
		b.signaled = true
		close(b.blocked)
	}
	n, err := b.ReadCloser.Read(p)
	b.bytesRead += n
	return n, err
}

func (b *inputBodyObserver) Close() error {
	b.closes++
	return b.ReadCloser.Close()
}

type inputTransportObserver struct {
	base           http.RoundTripper
	body           *inputBodyObserver
	blocked        chan struct{}
	acceptEncoding string
}

func (o *inputTransportObserver) RoundTrip(req *http.Request) (*http.Response, error) {
	o.acceptEncoding = req.Header.Get("Accept-Encoding")
	resp, err := o.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	o.body = &inputBodyObserver{ReadCloser: resp.Body, blocked: o.blocked}
	resp.Body = o.body
	return resp, nil
}

func TestDownloadInputClosesBodiesAndCancelsActiveRead(t *testing.T) {
	prefix := []byte("prefix")
	for _, name := range []string{"success", "status", "partial", "empty-range", "too-large", "truncated", "redirect-error", "cancel", "deadline"} {
		t.Run(name, func(t *testing.T) {
			active := name == "cancel" || name == "deadline"
			serverDone := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(serverDone)
				switch name {
				case "success":
					_, _ = w.Write(prefix)
					return
				case "status":
					w.WriteHeader(http.StatusBadGateway)
				case "partial":
					w.Header().Set("Content-Range", "bytes 0-5/1000")
					w.WriteHeader(http.StatusPartialContent)
				case "empty-range":
					w.Header().Set("Content-Range", "")
				case "too-large":
					w.Header().Set("Content-Length", strconv.Itoa(maxInputSize+1))
				case "redirect-error":
					w.Header().Set("Location", "http://upstream.invalid/credential-canary")
					w.WriteHeader(http.StatusFound)
					_, _ = w.Write(prefix)
					return
				case "truncated":
					w.Header().Set("Content-Length", "128")
					_, _ = w.Write(prefix)
					return
				default: // Keep a real body read pending after a received prefix.
					_, _ = w.Write(prefix)
				}
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer server.Close()

			observer := &inputTransportObserver{base: server.Client().Transport}
			if active {
				observer.blocked = make(chan struct{})
			}
			original := inputHTTPClient
			inputHTTPClient = &http.Client{Transport: observer}
			if name == "redirect-error" {
				inputHTTPClient.CheckRedirect = func(*http.Request, []*http.Request) error {
					return errors.New("credential-canary")
				}
			}
			defer func() { inputHTTPClient = original }()

			budget := 3 * time.Second
			if name == "deadline" {
				budget = time.Second
			}
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			var data []byte
			var err error
			done := make(chan struct{})
			go func() {
				data, err = downloadInput(ctx, server.URL)
				close(done)
			}()
			defer func() { cancel(); <-done }()
			if active {
				select {
				case <-observer.blocked:
					if name == "cancel" {
						cancel()
					}
				case <-ctx.Done():
					t.Fatal("request ended before receiving a prefix and starting the next read")
				case <-done:
					t.Fatal("unfinished response unexpectedly completed")
				}
			}
			<-done
			if observer.body == nil || observer.body.closes != 1 {
				t.Fatalf("body was not closed exactly once: %+v", observer.body)
			}
			if observer.acceptEncoding != "identity" {
				t.Fatalf("Accept-Encoding=%q", observer.acceptEncoding)
			}
			if name == "success" {
				if err != nil || !bytes.Equal(data, prefix) {
					t.Fatalf("complete response: data=%q err=%v", data, err)
				}
			} else if err == nil || data != nil {
				t.Fatalf("failed response returned input: data=%q err=%v", data, err)
			}
			if err != nil && strings.Contains(err.Error(), "credential-canary") {
				t.Fatalf("transport diagnostics escaped: %v", err)
			}
			switch name {
			case "redirect-error":
				if !errors.Is(err, errInputRequest) {
					t.Fatalf("redirect request error: %v", err)
				}
			case "cancel":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("active cancellation: %v", err)
				}
			case "deadline":
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("active deadline: %v", err)
				}
			case "too-large":
				if !errors.Is(err, errInputTooLarge) {
					t.Fatalf("known size rejection: %v", err)
				}
			case "truncated":
				if !errors.Is(err, errInputRead) {
					t.Fatalf("truncated response: %v", err)
				}
			}
			wantRead := 0
			if active || name == "success" || name == "truncated" || name == "redirect-error" {
				wantRead = len(prefix)
			}
			if observer.body.bytesRead != wantRead {
				t.Fatalf("body read=%d want=%d", observer.body.bytesRead, wantRead)
			}
			select {
			case <-serverDone:
			case <-time.After(3 * time.Second):
				t.Fatal("response body close did not release the local server handler")
			}
		})
	}
}
