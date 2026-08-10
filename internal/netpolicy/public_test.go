package netpolicy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests never touch public DNS or leave the machine: every resolver and
// every low-level dial is a fake, and the one real socket (the TLS test) goes
// to an httptest listener on loopback.

func TestPublicAddr(t *testing.T) {
	tests := map[string]bool{
		// The open internet, v4 and v6, including the boundaries of the RFC
		// 6598 shared range.
		"93.184.216.34":        true,
		"8.8.8.8":              true,
		"203.0.113.7":          true,
		"100.63.255.255":       true, // one below shared address space
		"100.128.0.0":          true, // one past shared address space
		"2606:4700:4700::1111": true,
		"::ffff:8.8.8.8":       true, // v4-mapped, still public

		// Loopback.
		"127.0.0.1":        false,
		"127.250.1.2":      false,
		"::1":              false,
		"::ffff:127.0.0.1": false,

		// Private: RFC 1918 and RFC 4193, mapped or not.
		"10.0.0.1":        false,
		"172.16.0.1":      false,
		"172.31.255.255":  false,
		"192.168.1.1":     false,
		"fc00::1":         false,
		"fd12:3456::1":    false,
		"::ffff:10.1.2.3": false,

		// Unspecified.
		"0.0.0.0": false,
		"::":      false,

		// Link-local unicast — including the cloud metadata address — and
		// link-local multicast.
		"169.254.169.254": false,
		"fe80::1":         false,
		"ff02::1":         false,

		// Interface-local and every other multicast scope.
		"ff01::1":         false,
		"224.0.0.1":       false,
		"239.255.255.255": false,
		"ff0e::1":         false,

		// RFC 6598 shared address space.
		"100.64.0.1":      false,
		"100.127.255.255": false,
	}
	for literal, want := range tests {
		if got := PublicAddr(netip.MustParseAddr(literal)); got != want {
			t.Errorf("PublicAddr(%s) = %v, want %v", literal, got, want)
		}
	}
	if PublicAddr(netip.Addr{}) {
		t.Error("PublicAddr(zero value) = true, want the invalid address refused")
	}
}

func TestPublicHost(t *testing.T) {
	tests := map[string]bool{
		"example.com":            true,
		"cdn.example.co.uk":      true,
		"8.8.8.8":                true,
		"2606:4700:4700::1111":   true,
		"[2606:4700:4700::1111]": true,

		"":              false,
		"localhost":     false,
		"LOCALHOST":     false,
		"a.localhost":   false,
		"printer.local": false,
		"127.0.0.1":     false,
		"[::1]":         false,
		"192.168.0.10":  false,
		"fe80::1%en0":   false, // a zoned literal is still a literal
	}
	for host, want := range tests {
		if got := PublicHost(host); got != want {
			t.Errorf("PublicHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestPublicHTTPSURL(t *testing.T) {
	tests := map[string]bool{
		"https://example.com/hook":      true,
		"https://example.com:8443/hook": true,
		"https://8.8.8.8/hook":          true,

		"http://example.com/hook":       false,
		"ftp://example.com/hook":        false,
		"https://127.0.0.1/hook":        false,
		"https://[::1]/hook":            false,
		"https://user:pw@10.0.0.1/hook": false,
		"https://":                      false,
		"not a url":                     false,
		"":                              false,
	}
	for raw, want := range tests {
		if got := PublicHTTPSURL(raw); got != want {
			t.Errorf("PublicHTTPSURL(%q) = %v, want %v", raw, got, want)
		}
	}
}

// addrs parses literals for a fake resolver answer.
func addrs(literals ...string) []netip.Addr {
	out := make([]netip.Addr, 0, len(literals))
	for _, l := range literals {
		out = append(out, netip.MustParseAddr(l))
	}
	return out
}

// fakeConn is a connection a fake dial can hand back without any socket.
func fakeConn(t *testing.T) net.Conn {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	return client
}

// TestDialerDialsTheApprovedLiteral covers the straight-through case: one
// lookup, an all-public answer, and a socket opened to the first candidate as
// the literal it was approved as — never to the hostname.
func TestDialerDialsTheApprovedLiteral(t *testing.T) {
	var lookups int
	var dialed []string
	d := &Dialer{
		Lookup: func(_ context.Context, host string) ([]netip.Addr, error) {
			lookups++
			if host != "hooks.example.com" {
				t.Errorf("resolved %q, want hooks.example.com", host)
			}
			return addrs("203.0.113.5", "2001:db8::7"), nil
		},
		Dial: func(_ context.Context, network, address string) (net.Conn, error) {
			dialed = append(dialed, network+" "+address)
			return fakeConn(t), nil
		},
	}

	conn, err := d.DialContext(t.Context(), "tcp", "hooks.example.com:443")
	if err != nil {
		t.Fatalf("DialContext failed: %v", err)
	}
	_ = conn.Close()
	if lookups != 1 {
		t.Errorf("the hostname was resolved %d times, want once", lookups)
	}
	if len(dialed) != 1 || dialed[0] != "tcp 203.0.113.5:443" {
		t.Errorf("dialed %v, want exactly the first approved literal", dialed)
	}
}

// TestDialerTriesCandidatesInResolverOrder pins the fallback: when the first
// approved literal refuses, the next is tried, still from the same answer.
func TestDialerTriesCandidatesInResolverOrder(t *testing.T) {
	var dialed []string
	d := &Dialer{
		Lookup: func(context.Context, string) ([]netip.Addr, error) {
			return addrs("203.0.113.5", "2001:db8::7"), nil
		},
		Dial: func(_ context.Context, _, address string) (net.Conn, error) {
			dialed = append(dialed, address)
			if len(dialed) == 1 {
				return nil, &net.OpError{Op: "dial", Net: "tcp",
					Err: errors.New("connect: connection refused")}
			}
			return fakeConn(t), nil
		},
	}

	conn, err := d.DialContext(t.Context(), "tcp", "hooks.example.com:443")
	if err != nil {
		t.Fatalf("DialContext failed: %v", err)
	}
	_ = conn.Close()
	want := []string{"203.0.113.5:443", "[2001:db8::7]:443"}
	if len(dialed) != 2 || dialed[0] != want[0] || dialed[1] != want[1] {
		t.Errorf("dialed %v, want %v in resolver order", dialed, want)
	}
}

// TestDialerRejectsAnswersBeforeDialing is the heart of the policy: an answer
// containing any non-public candidate fails the whole destination, before the
// low-level dial is ever reached. Selecting the public candidate out of a
// mixed answer would be playing along with a rebinding.
func TestDialerRejectsAnswersBeforeDialing(t *testing.T) {
	tests := map[string][]netip.Addr{
		"private only":             addrs("10.0.0.5"),
		"loopback only":            addrs("127.0.0.1"),
		"link-local only":          addrs("fe80::1"),
		"metadata address":         addrs("169.254.169.254"),
		"mixed public and private": addrs("203.0.113.5", "192.168.1.9"),
		"mixed public and mapped":  addrs("203.0.113.5", "::ffff:10.0.0.9"),
		"public then loopback":     addrs("8.8.8.8", "127.0.0.1"),
	}
	for name, answer := range tests {
		t.Run(name, func(t *testing.T) {
			d := &Dialer{
				Lookup: func(context.Context, string) ([]netip.Addr, error) {
					return answer, nil
				},
				Dial: func(_ context.Context, _, address string) (net.Conn, error) {
					t.Errorf("a socket to %s was opened for a refused answer", address)
					return nil, errors.New("unreachable")
				},
			}
			if _, err := d.DialContext(t.Context(), "tcp", "hooks.example.com:443"); !errors.Is(err, ErrNotPublic) {
				t.Errorf("err = %v, want ErrNotPublic", err)
			}
		})
	}
}

// TestDialerResolvesExactlyOnce is the rebinding pin: a resolver that answers
// public once and private ever after must never be asked twice, and the socket
// must go to a literal from the one answer that was checked.
func TestDialerResolvesExactlyOnce(t *testing.T) {
	var lookups int
	var dialed []string
	d := &Dialer{
		Lookup: func(context.Context, string) ([]netip.Addr, error) {
			lookups++
			if lookups == 1 {
				return addrs("203.0.113.5"), nil
			}
			return addrs("10.0.0.5"), nil // the rebound answer nobody may see
		},
		Dial: func(_ context.Context, _, address string) (net.Conn, error) {
			dialed = append(dialed, address)
			return fakeConn(t), nil
		},
	}

	conn, err := d.DialContext(t.Context(), "tcp", "rebind.example.com:443")
	if err != nil {
		t.Fatalf("DialContext failed: %v", err)
	}
	_ = conn.Close()
	if lookups != 1 {
		t.Errorf("the destination was resolved %d times, want exactly once", lookups)
	}
	if len(dialed) != 1 || dialed[0] != "203.0.113.5:443" {
		t.Errorf("dialed %v, want the literal approved from the single lookup", dialed)
	}
}

// TestDialerFailsClosed sweeps the edges: bad input, empty or failed
// resolution, refused literals and names — none may reach a lookup or a dial
// they should not, and every error is a refusal rather than a pass.
func TestDialerFailsClosed(t *testing.T) {
	tests := map[string]struct {
		network     string
		address     string
		answer      []netip.Addr
		lookupErr   error
		wantErr     error // nil means any error
		wantLookups int
	}{
		"empty answer":      {network: "tcp", address: "hooks.example.com:443", answer: []netip.Addr{}, wantErr: ErrResolve, wantLookups: 1},
		"resolver failure":  {network: "tcp", address: "hooks.example.com:443", lookupErr: &net.DNSError{Err: "no such host", Name: "hooks.example.com"}, wantErr: ErrResolve, wantLookups: 1},
		"malformed address": {network: "tcp", address: "no-port"},
		"not tcp":           {network: "udp", address: "8.8.8.8:53"},
		"loopback literal":  {network: "tcp", address: "127.0.0.1:443", wantErr: ErrNotPublic},
		"v6 loopback":       {network: "tcp", address: "[::1]:443", wantErr: ErrNotPublic},
		"mapped loopback":   {network: "tcp", address: "[::ffff:127.0.0.1]:443", wantErr: ErrNotPublic},
		"private literal":   {network: "tcp", address: "192.168.1.1:443", wantErr: ErrNotPublic},
		"localhost name":    {network: "tcp", address: "localhost:443", wantErr: ErrNotPublic},
		"mdns name":         {network: "tcp", address: "printer.local:443", wantErr: ErrNotPublic},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var lookups, dials int
			d := &Dialer{
				Lookup: func(context.Context, string) ([]netip.Addr, error) {
					lookups++
					return tc.answer, tc.lookupErr
				},
				Dial: func(context.Context, string, string) (net.Conn, error) {
					dials++
					return nil, errors.New("unreachable")
				},
			}
			_, err := d.DialContext(t.Context(), tc.network, tc.address)
			if err == nil {
				t.Fatal("the destination was dialed, want a refusal")
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
			if lookups != tc.wantLookups {
				t.Errorf("lookups = %d, want %d", lookups, tc.wantLookups)
			}
			if dials != 0 {
				t.Errorf("dials = %d, want none", dials)
			}
		})
	}
}

// TestDialerHonorsCancellation: a context canceled between candidates stops
// the walk, and one canceled up front never opens anything.
func TestDialerHonorsCancellation(t *testing.T) {
	t.Run("between candidates", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		var dials int
		d := &Dialer{
			Lookup: func(context.Context, string) ([]netip.Addr, error) {
				return addrs("203.0.113.5", "203.0.113.6"), nil
			},
			Dial: func(context.Context, string, string) (net.Conn, error) {
				dials++
				cancel()
				return nil, &net.OpError{Op: "dial", Err: errors.New("connection reset")}
			},
		}
		_, err := d.DialContext(ctx, "tcp", "hooks.example.com:443")
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
		if dials != 1 {
			t.Errorf("dialed %d candidates after cancellation, want 1", dials)
		}
	})

	t.Run("before dialing", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		d := &Dialer{
			Lookup: func(context.Context, string) ([]netip.Addr, error) {
				return addrs("203.0.113.5"), nil
			},
			Dial: func(context.Context, string, string) (net.Conn, error) {
				t.Error("a canceled context opened a socket")
				return nil, errors.New("unreachable")
			},
		}
		if _, err := d.DialContext(ctx, "tcp", "hooks.example.com:443"); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})
}

// TestDialerErrorsAreRedacted holds every failure path to the rule that no
// error names the destination: these strings end up in the worker's log and in
// the stored callback error, and the destination is the caller's URL.
func TestDialerErrorsAreRedacted(t *testing.T) {
	const host = "secret-internal.example.com"
	const ip = "203.0.113.9"

	t.Run("resolver failure", func(t *testing.T) {
		d := &Dialer{
			Lookup: func(context.Context, string) ([]netip.Addr, error) {
				return nil, &net.DNSError{Err: "no such host", Name: host}
			},
		}
		_, err := d.DialContext(t.Context(), "tcp", host+":443")
		if !errors.Is(err, ErrResolve) {
			t.Fatalf("err = %v, want ErrResolve", err)
		}
		if strings.Contains(err.Error(), host) {
			t.Errorf("the error names the host: %v", err)
		}
	})

	t.Run("dial failure keeps the class, drops the address", func(t *testing.T) {
		d := &Dialer{
			Lookup: func(context.Context, string) ([]netip.Addr, error) {
				return addrs(ip), nil
			},
			Dial: func(_ context.Context, _, address string) (net.Conn, error) {
				return nil, &net.OpError{Op: "dial", Net: "tcp",
					Addr: &net.TCPAddr{IP: net.ParseIP(ip), Port: 443},
					Err:  errors.New("connect: connection refused")}
			},
		}
		_, err := d.DialContext(t.Context(), "tcp", host+":443")
		if err == nil {
			t.Fatal("want a dial error")
		}
		if strings.Contains(err.Error(), ip) || strings.Contains(err.Error(), host) {
			t.Errorf("the error names the destination: %v", err)
		}
		if !strings.Contains(err.Error(), "connection refused") {
			t.Errorf("the failure class was lost: %v", err)
		}
	})

	t.Run("opaque dial failure is fully generic", func(t *testing.T) {
		d := &Dialer{
			Lookup: func(context.Context, string) ([]netip.Addr, error) {
				return addrs(ip), nil
			},
			Dial: func(context.Context, string, string) (net.Conn, error) {
				return nil, fmt.Errorf("dial tcp %s:443: boom", ip)
			},
		}
		_, err := d.DialContext(t.Context(), "tcp", host+":443")
		if err == nil {
			t.Fatal("want a dial error")
		}
		if strings.Contains(err.Error(), ip) || strings.Contains(err.Error(), host) {
			t.Errorf("the error names the destination: %v", err)
		}
	})
}

// TestTransportVerifiesTheOriginalHostname proves the property the whole
// design leans on: with the policy dialer installed, http.Transport still
// performs SNI and certificate verification against the URL's hostname, even
// though the socket underneath was dialed to an approved IP literal. The
// httptest certificate is valid for example.com, so a request to example.com
// over the vetted socket succeeds, and a request to any other name over the
// very same socket path fails verification.
func TestTransportVerifiesTheOriginalHostname(t *testing.T) {
	var (
		mu  sync.Mutex
		sni []string
	)
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sni = append(sni, r.TLS.ServerName)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	ts.StartTLS()
	defer ts.Close()

	pool := x509.NewCertPool()
	pool.AddCert(ts.Certificate())
	port := ts.Listener.Addr().(*net.TCPAddr).Port

	var dialed []string
	dialer := &Dialer{
		// Any name resolves to one public literal; the socket really goes to
		// the loopback test listener, which is what an approved literal being
		// decoupled from the TLS identity looks like in a test.
		Lookup: func(context.Context, string) ([]netip.Addr, error) {
			return addrs("203.0.113.10"), nil
		},
		Dial: func(_ context.Context, _, address string) (net.Conn, error) {
			mu.Lock()
			dialed = append(dialed, address)
			mu.Unlock()
			return net.Dial("tcp", ts.Listener.Addr().String())
		},
	}

	// Built the way the callback worker builds its production transport, with
	// only the test CA added; verification itself stays on.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = dialer.DialContext
	transport.TLSClientConfig = &tls.Config{RootCAs: pool}
	client := &http.Client{Timeout: 10 * time.Second, Transport: transport}

	resp, err := client.Get(fmt.Sprintf("https://example.com:%d/hook", port))
	if err != nil {
		t.Fatalf("the request over the vetted socket failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}

	mu.Lock()
	if len(dialed) == 0 || !strings.HasPrefix(dialed[0], "203.0.113.10:") {
		t.Errorf("dialed %v, want the approved literal, never the hostname", dialed)
	}
	if len(sni) == 0 || sni[0] != "example.com" {
		t.Errorf("the server saw SNI %v, want the original hostname example.com", sni)
	}
	mu.Unlock()

	// A hostname the certificate does not name must fail the handshake even
	// though the socket connects: hostname verification is alive and bound to
	// the URL, not to the address that was dialed.
	_, err = client.Get(fmt.Sprintf("https://other.test:%d/hook", port))
	var certErr *tls.CertificateVerificationError
	if !errors.As(err, &certErr) {
		t.Errorf("request for other.test = %v, want a certificate verification failure", err)
	}
}
