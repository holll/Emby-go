package outbound

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// The child owns net.DefaultResolver. No resolver change escapes to other tests,
// including parallel tests added later. All DNS traffic goes to a local UDP
// fixture; Control rejects every data socket before connect, even on failure.
func TestOutboundDialDNSBoundary(t *testing.T) {
	const childEnv = "EMBY_GO_TEST_DIAL_DNS_BOUNDARY"
	mode := os.Getenv(childEnv)
	if mode == "" {
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"loopback", "private", "metadata", "public-pinned"} {
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, binary, "-test.run=^TestOutboundDialDNSBoundary$", "-test.v")
				cmd.Env = append(os.Environ(), childEnv+"="+name)
				output, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("isolated DNS fixture: %v\n%s", err, output)
				}
				t.Logf("%s", output)
			})
		}
		return
	}

	var final []netip.Addr
	switch mode {
	case "loopback":
		final = []netip.Addr{netip.MustParseAddr("127.0.0.1")}
	case "private":
		final = []netip.Addr{netip.MustParseAddr("10.0.0.1")}
	case "metadata":
		final = []netip.Addr{netip.MustParseAddr("169.254.169.254")}
	case "public-pinned":
		final = []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("8.8.8.8")}
	default:
		t.Fatalf("unknown child mode %q", mode)
	}

	dns, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var lookups atomic.Int64
	done := make(chan struct{})
	failures := make(chan error, 1)
	go func() {
		defer close(done)
		buffer := make([]byte, 2048)
		for {
			n, peer, err := dns.ReadFrom(buffer)
			if err != nil {
				if !errors.Is(err, net.ErrClosed) {
					failures <- err
				}
				return
			}
			var query dnsmessage.Message
			if err := query.Unpack(buffer[:n]); err != nil {
				failures <- err
				return
			}
			response := dnsmessage.Message{
				Header: dnsmessage.Header{
					ID: query.ID, Response: true, Authoritative: true,
					RecursionDesired: query.RecursionDesired, RecursionAvailable: true,
				},
				Questions: query.Questions,
			}
			for _, question := range query.Questions {
				if question.Name.String() != "dial-boundary.test." {
					failures <- fmt.Errorf("unexpected DNS name %s", question.Name.String())
					return
				}
				if question.Type != dnsmessage.TypeA {
					continue // AAAA receives an authoritative empty response.
				}
				var addresses []netip.Addr
				switch lookups.Add(1) {
				case 1:
					addresses = []netip.Addr{netip.MustParseAddr("9.9.9.9")}
				case 2:
					addresses = final
				default:
					// A hostname accidentally handed to net.Dialer would resolve
					// again, now to private space, rather than the pinned public IP.
					addresses = []netip.Addr{netip.MustParseAddr("127.0.0.1")}
				}
				for _, address := range addresses {
					response.Answers = append(response.Answers, dnsmessage.Resource{
						Header: dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 0},
						Body:   &dnsmessage.AResource{A: address.As4()},
					})
				}
			}
			packet, err := response.Pack()
			if err != nil {
				failures <- err
				return
			}
			if _, err := dns.WriteTo(packet, peer); err != nil {
				failures <- err
				return
			}
		}
	}()
	t.Cleanup(func() {
		dns.Close()
		<-done
		select {
		case err := <-failures:
			t.Errorf("local DNS fixture: %v", err)
		default:
		}
	})
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "udp4", dns.LocalAddr().String())
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	u, err := url.Parse("http://dial-boundary.test.:18080/media")
	if err != nil {
		t.Fatal(err)
	}
	p := policy{}
	if err := p.validateURL(ctx, u, true); err != nil {
		t.Fatalf("initial public answer rejected: %v", err)
	}
	if lookups.Load() != 1 {
		t.Fatalf("initial A lookups=%d want=1", lookups.Load())
	}
	stopped := errors.New("test stopped socket before connect")
	var mu sync.Mutex
	var attempted []string
	dialer := &net.Dialer{
		Control: func(network, address string, _ syscall.RawConn) error {
			mu.Lock()
			attempted = append(attempted, network+" "+address)
			mu.Unlock()
			return stopped
		},
	}
	conn, err := p.dialContext(dialer)(ctx, "tcp", u.Host)
	if conn != nil {
		conn.Close()
		t.Fatal("fixture unexpectedly connected")
	}
	mu.Lock()
	got := append([]string(nil), attempted...)
	mu.Unlock()
	if lookups.Load() != 2 {
		t.Fatalf("A lookups=%d want=2 (URL validation + final policy resolution only)", lookups.Load())
	}
	if mode != "public-pinned" {
		if err == nil || !strings.Contains(err.Error(), "blocked address "+final[0].String()) || len(got) != 0 {
			t.Fatalf("rebound address reached socket boundary: error=%v attempts=%v", err, got)
		}
		return
	}
	if !errors.Is(err, stopped) || len(got) != len(final) {
		t.Fatalf("public addresses not attempted at Control: error=%v attempts=%v", err, got)
	}
	want := map[string]bool{}
	for _, address := range final {
		want["tcp4 "+net.JoinHostPort(address.String(), "18080")] = true
	}
	for _, address := range got {
		if !want[address] {
			t.Fatalf("socket did not use a verified literal IP: %q; attempts=%v", address, got)
		}
		delete(want, address)
	}
	if len(want) != 0 {
		t.Fatalf("missing public literal IP attempts: %v", want)
	}
	// This is a socket-Control boundary test, not a public connection, HTTP,
	// TLS, redirect, proxy, or whole-probe isolation acceptance test.
}
