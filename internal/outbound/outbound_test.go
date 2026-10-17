package outbound

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestIsPublicAddresses(t *testing.T) {
	cases := []struct {
		addr   string
		public bool
	}{
		{"8.8.8.8", true},
		{"1.1.1.1", true},
		{"2606:4700:4700::1111", true},
		{"127.0.0.1", false},
		{"10.0.0.1", false},
		{"172.16.0.1", false},
		{"192.168.1.1", false},
		{"169.254.169.254", false},
		{"fd00::1", false},
		{"fe80::1", false},
		{"::1", false},
		{"0.0.0.0", false},
		{"224.0.0.1", false},
		{"240.0.0.1", false},
		{"100.64.0.1", false},
		{"192.0.2.1", false},
		{"::ffff:8.8.8.8", true},
	}
	for _, tc := range cases {
		if got := isPublic(netip.MustParseAddr(tc.addr)); got != tc.public {
			t.Errorf("isPublic(%s) = %v, want %v", tc.addr, got, tc.public)
		}
	}
}

func TestHardBlockedWithPrivateAllowed(t *testing.T) {
	cases := []struct {
		addr    string
		blocked bool
	}{
		{"127.0.0.1", false},
		{"10.0.0.1", false},
		{"192.168.1.1", false},
		{"100.64.0.1", false},
		{"8.8.8.8", false},
		{"169.254.169.254", true},
		{"fe80::1", true},
		{"224.0.0.1", true},
		{"0.0.0.0", true},
		{"240.0.0.1", true},
	}
	for _, tc := range cases {
		if got := hardBlocked(netip.MustParseAddr(tc.addr)); got != tc.blocked {
			t.Errorf("hardBlocked(%s) = %v, want %v", tc.addr, got, tc.blocked)
		}
	}
}

func TestPublicProfileBlocksPrivateTargets(t *testing.T) {
	client := NewClient(Options{Timeout: 2 * time.Second})
	targets := []string{
		"http://127.0.0.1:1/",
		"http://10.10.10.10/",
		"http://192.168.1.10/",
		"http://169.254.169.254/latest/meta-data/",
		"http://[::1]/",
		"http://[fd00::1]/",
	}
	for _, target := range targets {
		resp, err := client.Get(target)
		if resp != nil {
			resp.Body.Close()
		}
		if err == nil || !strings.Contains(err.Error(), "blocked address") {
			t.Fatalf("GET %s 应被地址策略拒绝，err=%v", target, err)
		}
	}
}

func TestAllowPrivateProfile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	client := NewClient(Options{Timeout: 2 * time.Second, AllowPrivate: true})
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("AllowPrivate 应允许回环: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Fatalf("响应体 = %q", string(body))
	}

	// 即便允许私网，云元数据地址仍然拒绝。
	if _, err := client.Get("http://169.254.169.254/latest/meta-data/"); err == nil ||
		!strings.Contains(err.Error(), "blocked address") {
		t.Fatalf("链路本地地址应始终被拒绝，err=%v", err)
	}
}

func TestAllowHostsException(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()
	parsed, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	allowed := NewClient(Options{Timeout: 2 * time.Second, AllowHosts: []string{parsed.Hostname()}})
	resp, err := allowed.Get(srv.URL)
	if err != nil {
		t.Fatalf("AllowHosts 例外应允许该主机: %v", err)
	}
	resp.Body.Close()

	strict := NewClient(Options{Timeout: 2 * time.Second})
	if _, err := strict.Get(srv.URL); err == nil || !strings.Contains(err.Error(), "blocked address") {
		t.Fatalf("未列名主机仍应拒绝，err=%v", err)
	}
}

func TestProxyProfileKeepsValidation(t *testing.T) {
	var sawHost string
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawHost = r.URL.Host
		_, _ = io.WriteString(w, "proxied")
	}))
	defer proxySrv.Close()
	proxyURL, err := url.Parse(proxySrv.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxyFunc := func(*http.Request) (*url.URL, error) { return proxyURL, nil }

	client := NewClient(Options{Timeout: 3 * time.Second, ProxyFunc: proxyFunc})
	resp, err := client.Get("http://upstream.invalid/v1/images/primary/p/i")
	if err != nil {
		t.Fatalf("代理模式应放行本机无法解析的主机名: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "proxied" || sawHost != "upstream.invalid" {
		t.Fatalf("代理未收到目标请求: body=%q host=%q", string(body), sawHost)
	}

	// 私网字面量即使走代理也必须拒绝。
	if _, err := client.Get("http://10.9.8.7/x"); err == nil ||
		!strings.Contains(err.Error(), "blocked address") {
		t.Fatalf("代理模式下私网地址仍应拒绝，err=%v", err)
	}
}

func TestRedirectPolicy(t *testing.T) {
	loop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	}))
	defer loop.Close()

	client := NewClient(Options{Timeout: 2 * time.Second, AllowPrivate: true, MaxRedirects: 2})
	if _, err := client.Get(loop.URL + "/loop"); err == nil ||
		!strings.Contains(err.Error(), "stopped after 2 redirects") {
		t.Fatalf("重定向应在 2 跳后停止，err=%v", err)
	}

	toFile := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "file:///etc/passwd")
		w.WriteHeader(http.StatusFound)
	}))
	defer toFile.Close()

	if _, err := client.Get(toFile.URL + "/redirect"); err == nil ||
		!strings.Contains(err.Error(), "unsupported scheme") {
		t.Fatalf("重定向到非 http(s) 应拒绝，err=%v", err)
	}

	// 重定向后的每一跳仍受同一套地址策略约束。
	toPrivate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "http://10.5.5.5/secret")
		w.WriteHeader(http.StatusFound)
	}))
	defer toPrivate.Close()

	hostOnly := NewClient(Options{Timeout: 2 * time.Second, AllowHosts: []string{"127.0.0.1"}})
	if _, err := hostOnly.Get(toPrivate.URL + "/redirect"); err == nil ||
		!strings.Contains(err.Error(), "blocked address") {
		t.Fatalf("重定向到未授权私网地址应拒绝，err=%v", err)
	}
}

func TestResponseHeaderBudget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Pad", strings.Repeat("a", 4096))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewClient(Options{
		Timeout:                2 * time.Second,
		AllowPrivate:           true,
		MaxResponseHeaderBytes: 1024,
	})
	if _, err := client.Get(srv.URL); err == nil || !strings.Contains(err.Error(), "response headers") {
		t.Fatalf("超限响应头应被拒绝，err=%v", err)
	}
}

func TestUnsupportedScheme(t *testing.T) {
	client := NewClient(Options{Timeout: time.Second})
	if _, err := client.Get("file:///etc/passwd"); err == nil ||
		!strings.Contains(err.Error(), "unsupported scheme") {
		t.Fatalf("非 http(s) 协议应拒绝，err=%v", err)
	}
}

func TestNormalizeHost(t *testing.T) {
	cases := map[string]string{
		"MetaTube.Example.COM":      "metatube.example.com",
		"metatube.example.com:8080": "metatube.example.com",
		"http://metatube:8080/":     "metatube",
		"[::1]:9000":                "::1",
		" example.com. ":            "example.com",
	}
	for input, want := range cases {
		if got := normalizeHost(input); got != want {
			t.Errorf("normalizeHost(%q) = %q, want %q", input, got, want)
		}
	}
}
