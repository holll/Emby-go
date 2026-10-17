// Package outbound 统一服务端出站 HTTP 请求的信任边界（BE-09）。
//
// 所有「由配置或上游数据驱动」的请求都经过同一套策略：
//   - 只允许 http/https；
//   - 直连时先解析目标、校验地址，再按已校验的 IP 拨号（防 DNS rebinding）；
//   - 默认只允许公网地址；需要访问内网/回环的调用方显式放开（见 Options.AllowPrivate），
//     或按主机名逐个例外（Options.AllowHosts）；
//   - 链路本地（含云元数据 169.254.169.254）、组播、未指定地址、保留网段一律拒绝；
//   - 重定向最多 N 跳，每一跳仍走同一套协议/地址策略；
//   - 响应头有上限，拨号、TLS 握手、响应头各有独立预算。
//
// 代理说明：HTTP(S)_PROXY 生效时连接由代理建立，本机无法校验代理实际连到的地址，
// 因此代理模式下对「本机解析失败的主机名」放行，交由代理处理；字面量 IP 和本机可
// 解析的主机仍会在发请求前校验。该残余风险随 D04 一并评审。
package outbound

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// 默认预算。调用方可按场景覆盖，例如长连接播放不设总超时。
const (
	DefaultMaxRedirects           = 3
	DefaultMaxResponseHeaderBytes = 512 << 10
	DefaultDialTimeout            = 10 * time.Second
	DefaultTLSHandshakeTimeout    = 10 * time.Second
	DefaultResponseHeaderTimeout  = 15 * time.Second
	DefaultIdleConnTimeout        = 90 * time.Second
	DefaultMaxConnsPerHost        = 16
	DefaultMaxIdleConnsPerHost    = 8
	DefaultMaxIdleConns           = 32
)

// Options 描述一类出站调用的信任边界与预算。
type Options struct {
	// Timeout 是整个请求（含读完响应体）的超时；<=0 表示不设总超时，由 context 控制。
	Timeout time.Duration
	// AllowPrivate 放开回环与私网地址（局域网播放源、本机翻译服务等）。
	// 即便如此，链路本地/组播/保留网段仍拒绝。
	AllowPrivate bool
	// AllowHosts 逐个放开私网访问的主机名（只比较主机名，忽略端口）。
	AllowHosts []string
	// ProxyFunc 决定是否走代理；nil 时用 http.ProxyFromEnvironment。
	ProxyFunc func(*http.Request) (*url.URL, error)

	MaxRedirects           int
	MaxResponseHeaderBytes int64
	DialTimeout            time.Duration
	TLSHandshakeTimeout    time.Duration
	ResponseHeaderTimeout  time.Duration
	IdleConnTimeout        time.Duration
	MaxConnsPerHost        int
	MaxIdleConnsPerHost    int
	MaxIdleConns           int
}

func (o Options) withDefaults() Options {
	if o.ProxyFunc == nil {
		o.ProxyFunc = http.ProxyFromEnvironment
	}
	if o.MaxRedirects <= 0 {
		o.MaxRedirects = DefaultMaxRedirects
	}
	if o.MaxResponseHeaderBytes <= 0 {
		o.MaxResponseHeaderBytes = DefaultMaxResponseHeaderBytes
	}
	if o.DialTimeout <= 0 {
		o.DialTimeout = DefaultDialTimeout
	}
	if o.TLSHandshakeTimeout <= 0 {
		o.TLSHandshakeTimeout = DefaultTLSHandshakeTimeout
	}
	if o.ResponseHeaderTimeout <= 0 {
		o.ResponseHeaderTimeout = DefaultResponseHeaderTimeout
	}
	if o.IdleConnTimeout <= 0 {
		o.IdleConnTimeout = DefaultIdleConnTimeout
	}
	if o.MaxConnsPerHost <= 0 {
		o.MaxConnsPerHost = DefaultMaxConnsPerHost
	}
	if o.MaxIdleConnsPerHost <= 0 {
		o.MaxIdleConnsPerHost = DefaultMaxIdleConnsPerHost
	}
	if o.MaxIdleConns <= 0 {
		o.MaxIdleConns = DefaultMaxIdleConns
	}
	return o
}

// NewClient 按策略构造 *http.Client。返回的客户端可安全并发使用。
func NewClient(opts Options) *http.Client {
	opts = opts.withDefaults()
	p := policy{allowPrivate: opts.AllowPrivate, allowHosts: normalizeHosts(opts.AllowHosts)}
	dialer := &net.Dialer{Timeout: opts.DialTimeout, KeepAlive: 30 * time.Second}

	newTransport := func(guarded bool) *http.Transport {
		transport := &http.Transport{
			ForceAttemptHTTP2:      true,
			DialContext:            dialer.DialContext,
			MaxIdleConns:           opts.MaxIdleConns,
			MaxIdleConnsPerHost:    opts.MaxIdleConnsPerHost,
			MaxConnsPerHost:        opts.MaxConnsPerHost,
			IdleConnTimeout:        opts.IdleConnTimeout,
			TLSHandshakeTimeout:    opts.TLSHandshakeTimeout,
			ResponseHeaderTimeout:  opts.ResponseHeaderTimeout,
			ExpectContinueTimeout:  time.Second,
			MaxResponseHeaderBytes: opts.MaxResponseHeaderBytes,
		}
		if guarded {
			// 直连：拨号前按目标主机策略校验并钉住解析出的 IP。
			transport.DialContext = p.dialContext(dialer)
			transport.Proxy = nil
		} else {
			// 代理：连接由代理建立，本地只校验请求目标的可判定部分。
			transport.Proxy = opts.ProxyFunc
		}
		return transport
	}

	return &http.Client{
		Transport: &routingTransport{
			policy:    p,
			proxyFunc: opts.ProxyFunc,
			direct:    newTransport(true),
			proxied:   newTransport(false),
		},
		Timeout:       opts.Timeout,
		CheckRedirect: redirectGuard(opts.MaxRedirects),
	}
}

// routingTransport 逐请求决定走直连还是代理，并在发出前校验目标。
type routingTransport struct {
	policy    policy
	proxyFunc func(*http.Request) (*url.URL, error)
	direct    *http.Transport
	proxied   *http.Transport
}

func (t *routingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil {
		return nil, errors.New("outbound: request has no URL")
	}
	proxyURL, err := t.proxyFunc(req)
	if err != nil {
		return nil, fmt.Errorf("outbound: proxy lookup failed: %w", err)
	}
	if proxyURL != nil {
		// 代理模式下解析失败不算错误（正是用代理的场景），但能解析时仍校验。
		if err := t.policy.validateURL(req.Context(), req.URL, false); err != nil {
			return nil, err
		}
		return t.proxied.RoundTrip(req)
	}
	if err := t.policy.validateURL(req.Context(), req.URL, true); err != nil {
		return nil, err
	}
	return t.direct.RoundTrip(req)
}

func redirectGuard(max int) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= max {
			return fmt.Errorf("outbound: stopped after %d redirects", max)
		}
		if req.URL == nil {
			return errors.New("outbound: redirect without URL")
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return fmt.Errorf("outbound: redirect to unsupported scheme %q", req.URL.Scheme)
		}
		return nil
	}
}

type policy struct {
	allowPrivate bool
	allowHosts   map[string]struct{}
}

func (p policy) allowPrivateFor(host string) bool {
	if p.allowPrivate {
		return true
	}
	_, ok := p.allowHosts[normalizeHost(host)]
	return ok
}

// validateURL 校验协议与目标地址。strict 为 true 时解析失败即拒绝（直连）；
// 代理模式下 strict=false，解析失败交给代理。
func (p policy) validateURL(ctx context.Context, u *url.URL, strict bool) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("outbound: unsupported scheme %q", u.Scheme)
	}
	host := u.Hostname()
	if strings.TrimSpace(host) == "" {
		return errors.New("outbound: request URL has no host")
	}
	allowPrivate := p.allowPrivateFor(host)
	addrs, err := resolveAddrs(ctx, host)
	if err != nil {
		if strict {
			return fmt.Errorf("outbound: resolve %s: %w", host, err)
		}
		return nil
	}
	if len(addrs) == 0 {
		if strict {
			return fmt.Errorf("outbound: %s resolved to no address", host)
		}
		return nil
	}
	for _, addr := range addrs {
		if allowPrivate {
			if hardBlocked(addr) {
				return blockedError(host, addr)
			}
			continue
		}
		if !isPublic(addr) {
			return blockedError(host, addr)
		}
	}
	return nil
}

// dialContext 在拨号前校验目标主机并只连接已放行的 IP，避免校验后再次解析。
func (p policy) dialContext(dialer *net.Dialer) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("outbound: invalid dial address %q: %w", addr, err)
		}
		allowPrivate := p.allowPrivateFor(host)
		addrs, err := resolveAddrs(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("outbound: resolve %s: %w", host, err)
		}
		var lastErr error
		for _, ip := range addrs {
			if allowPrivate {
				if hardBlocked(ip) {
					lastErr = blockedError(host, ip)
					continue
				}
			} else if !isPublic(ip) {
				lastErr = blockedError(host, ip)
				continue
			}
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("outbound: %s resolved to no address", host)
		}
		return nil, lastErr
	}
}

func resolveAddrs(ctx context.Context, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip.Unmap()}, nil
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	out := addrs[:0]
	for _, addr := range addrs {
		out = append(out, addr.Unmap())
	}
	return out, nil
}

func blockedError(host string, addr netip.Addr) error {
	return fmt.Errorf("outbound: %s resolved to blocked address %s", host, addr)
}

var (
	// alwaysBlocked 在任何模式下都拒绝：不可路由/文档/保留/协议转换网段。
	alwaysBlocked = mustPrefixes(
		"0.0.0.0/8",
		"192.0.0.0/24",
		"192.0.2.0/24",
		"198.18.0.0/15",
		"198.51.100.0/24",
		"203.0.113.0/24",
		"240.0.0.0/4",
		"2001:db8::/32",
		"2002::/16",
		"64:ff9b::/96",
	)
	// sharedSpace 运营商级 NAT（100.64.0.0/10）：AllowPrivate 时按私网对待。
	sharedSpace = netip.MustParsePrefix("100.64.0.0/10")
)

func mustPrefixes(values ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		out = append(out, netip.MustParsePrefix(value))
	}
	return out
}

func inPrefixes(prefixes []netip.Prefix, addr netip.Addr) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// isPublic 报告地址是否可视为公网目标。
func isPublic(addr netip.Addr) bool {
	addr = addr.Unmap()
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return false
	}
	if sharedSpace.Contains(addr) {
		return false
	}
	return !inPrefixes(alwaysBlocked, addr)
}

// hardBlocked 报告地址在「允许私网」模式下也必须拒绝的部分。
func hardBlocked(addr netip.Addr) bool {
	addr = addr.Unmap()
	if addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() || addr.IsUnspecified() {
		return true
	}
	return inPrefixes(alwaysBlocked, addr)
}

func normalizeHosts(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		if host := normalizeHost(value); host != "" {
			out[host] = struct{}{}
		}
	}
	return out
}

// normalizeHost 统一成小写、无端口、无方括号、无尾点的主机名。
func normalizeHost(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return ""
	}
	if strings.Contains(value, "://") {
		if u, err := url.Parse(value); err == nil && u.Hostname() != "" {
			value = u.Hostname()
		}
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	value = strings.Trim(value, "[]")
	return strings.TrimSuffix(value, ".")
}
