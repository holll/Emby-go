package outbound

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDirectHTTPRedirectBoundary(t *testing.T) {
	// The origin alone has a loopback exception so the real HTTP/TLS fixture
	// can be reached. This is not a successful public-profile connection test.
	for _, secure := range []bool{false, true} {
		name := "http"
		if secure {
			name = "https"
		}
		t.Run(name, func(t *testing.T) {
			var protectedHits, protectedConnections, originHits atomic.Int32
			protectedHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				protectedHits.Add(1)
				_, _ = io.WriteString(w, "protected")
			})
			protected := httptest.NewUnstartedServer(protectedHandler)
			protectedTLS := httptest.NewUnstartedServer(protectedHandler)
			for _, srv := range []*httptest.Server{protected, protectedTLS} {
				srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
					if state == http.StateNew {
						protectedConnections.Add(1)
					}
				}
			}
			protected.Start()
			protectedTLS.StartTLS()
			defer protected.Close()
			defer protectedTLS.Close()
			protectedURL, err := url.Parse(protected.URL)
			if err != nil {
				t.Fatal(err)
			}
			// localhost resolves to loopback but is deliberately not excepted.
			protectedURL.Host = "localhost:" + protectedURL.Port()
			protectedTLSURL, err := url.Parse(protectedTLS.URL)
			if err != nil {
				t.Fatal(err)
			}
			protectedTLSURL.Host = "localhost:" + protectedTLSURL.Port()
			targets := map[string]string{
				"/loopback":     protectedURL.String(),
				"/tls-loopback": protectedTLSURL.String(),
				"/private":      "http://10.5.5.5/secret",
				"/metadata":     "http://169.254.169.254/latest/meta-data/",
				"/ipv6":         "http://[::1]/secret",
				"/file":         "file:///etc/passwd",
			}
			origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				originHits.Add(1)
				if path, ok := strings.CutPrefix(r.URL.Path, "/hop"); ok {
					http.Redirect(w, r, path, http.StatusTemporaryRedirect)
					return
				}
				switch r.URL.Path {
				case "/start":
					http.Redirect(w, r, "/done", http.StatusTemporaryRedirect)
				case "/done":
					_, _ = io.WriteString(w, "complete")
				case "/loop":
					http.Redirect(w, r, "/loop", http.StatusFound)
				default:
					w.Header().Set("Location", targets[r.URL.Path])
					w.WriteHeader(http.StatusFound)
				}
			}))
			if secure {
				origin.StartTLS()
			} else {
				origin.Start()
			}
			defer origin.Close()
			client := NewClient(Options{
				Timeout: 3 * time.Second, AllowHosts: []string{"127.0.0.1"},
				ProxyFunc: func(*http.Request) (*url.URL, error) { return nil, nil },
			})
			defer client.CloseIdleConnections()
			if secure {
				roots := x509.NewCertPool()
				roots.AddCert(origin.Certificate())
				client.Transport.(*routingTransport).direct.TLSClientConfig = &tls.Config{RootCAs: roots}
			}
			resp, err := client.Get(origin.URL + "/start")
			if err != nil {
				t.Fatal(err)
			}
			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr != nil || resp.StatusCode != http.StatusOK || string(body) != "complete" || originHits.Load() != 2 {
				t.Fatalf("relative redirect status=%d body=%q hits=%d err=%v", resp.StatusCode, body, originHits.Load(), readErr)
			}
			for path := range targets {
				for _, prefix := range []string{"", "/hop"} {
					t.Run(prefix+path, func(t *testing.T) {
						before := originHits.Load()
						resp, err := client.Get(origin.URL + prefix + path)
						if resp != nil {
							resp.Body.Close()
						}
						want := "blocked address"
						if path == "/file" {
							want = "unsupported scheme"
						}
						wantHops := int32(1)
						if prefix != "" {
							wantHops = 2
						}
						if err == nil || !strings.Contains(err.Error(), want) || originHits.Load() != before+wantHops || protectedHits.Load() != 0 || protectedConnections.Load() != 0 {
							t.Fatalf("redirect err=%v origin delta=%d want=%d protected hits=%d connections=%d", err, originHits.Load()-before, wantHops, protectedHits.Load(), protectedConnections.Load())
						}
					})
				}
			}
			before := originHits.Load()
			resp, err = client.Get(origin.URL + "/loop")
			if resp != nil {
				resp.Body.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "stopped after 3 redirects") || originHits.Load() != before+3 || protectedHits.Load() != 0 {
				t.Fatalf("redirect budget err=%v origin delta=%d protected hits=%d", err, originHits.Load()-before, protectedHits.Load())
			}
		})
	}
}

func TestDirectTLSCertificateBoundary(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "verified")
	}))
	defer srv.Close()
	// Both names are fixture-only exceptions, not production probe policy.
	client := NewClient(Options{
		Timeout: 3 * time.Second, AllowHosts: []string{"127.0.0.1", "localhost"},
		ProxyFunc: func(*http.Request) (*url.URL, error) { return nil, nil },
	})
	defer client.CloseIdleConnections()
	resp, err := client.Get(srv.URL)
	if resp != nil {
		resp.Body.Close()
	}
	var unknown x509.UnknownAuthorityError
	if !errors.As(err, &unknown) || hits.Load() != 0 {
		t.Fatalf("untrusted certificate err=%v application hits=%d", err, hits.Load())
	}
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	client.Transport.(*routingTransport).direct.TLSClientConfig = &tls.Config{RootCAs: roots}
	resp, err = client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil || string(body) != "verified" || hits.Load() != 1 {
		t.Fatalf("trusted certificate body=%q hits=%d err=%v", body, hits.Load(), readErr)
	}
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	u.Host = "localhost:" + u.Port()
	resp, err = client.Get(u.String())
	if resp != nil {
		resp.Body.Close()
	}
	var hostname x509.HostnameError
	if !errors.As(err, &hostname) || hits.Load() != 1 {
		t.Fatalf("wrong hostname err=%v application hits=%d", err, hits.Load())
	}
}
