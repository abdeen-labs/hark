// Package netpolicy is Hark's single opinion about which network destinations
// a caller-supplied URL may point at.
//
// Two enforcement points share it, and sharing is the point. Request
// validation uses the static half — [PublicHTTPSURL], [PublicHost],
// [PublicAddr] — to refuse URLs whose host is unroutable by definition: a
// loopback or RFC 1918 literal, `localhost`, a `.local` name. A DNS name
// passes that check unresolved, because resolving it at validation time would
// be a request in itself and the answer could change before it matters. The
// callback worker's [Dialer] is the dynamic half: it applies the same
// classification to every address the name actually resolves to, at the
// moment a socket is opened, which is the only moment the answer is known to
// be the one that gets used. Keeping both halves in one package is what stops
// the deny lists drifting apart.
package netpolicy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// PublicAddr reports whether addr is an address the open internet can route
// to. It refuses loopback, private (RFC 1918 and RFC 4193), unspecified,
// link-local unicast and multicast, interface-local multicast, all other
// multicast, and RFC 6598 shared address space, for IPv4 and IPv6 alike.
// IPv4-mapped IPv6 addresses are classified as the IPv4 address they carry,
// so `::ffff:127.0.0.1` is as refused as `127.0.0.1` is.
func PublicAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	return addr.IsValid() && !addr.IsLoopback() && !addr.IsPrivate() &&
		!addr.IsUnspecified() && !addr.IsLinkLocalUnicast() &&
		!addr.IsLinkLocalMulticast() && !addr.IsInterfaceLocalMulticast() &&
		!addr.IsMulticast() && !sharedAddressSpace(addr)
}

// sharedAddressSpace covers RFC 6598 carrier-grade NAT (100.64.0.0/10), which
// the standard library does not classify but which is as unreachable from the
// internet as RFC 1918 is.
func sharedAddressSpace(addr netip.Addr) bool {
	if !addr.Is4() {
		return false
	}
	b := addr.As4()
	return b[0] == 100 && b[1] >= 64 && b[1] <= 127
}

// PublicHost reports whether a URL host is one the open internet can reach.
//
// The check is name-based as well as address-based: it refuses the names that
// are unroutable by definition and the IP literals [PublicAddr] refuses, and
// lets every other DNS name through for later resolution. A name's actual
// addresses are the [Dialer]'s to judge, at connection time.
func PublicHost(host string) bool {
	host = strings.ToLower(strings.Trim(host, "[]"))
	switch {
	case host == "", host == "localhost",
		strings.HasSuffix(host, ".localhost"), strings.HasSuffix(host, ".local"):
		return false
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return true // a name; the dial-time policy holds the rest
	}
	return PublicAddr(addr)
}

// PublicHTTPSURL reports whether raw parses as an HTTPS URL whose host passes
// [PublicHost]. It is the rule request validation applies to every URL that
// something other than the caller will dereference.
func PublicHTTPSURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && PublicHost(u.Hostname())
}

// The sentinel errors deliberately carry no host, address or port: everything
// the [Dialer] returns ends up in the callback worker's log and in the stored
// last-error column, and the destination is the caller's URL.
var (
	// ErrNotPublic refuses a destination that is not publicly routable: a
	// refused literal, a local name, or a DNS answer with even one non-public
	// candidate in it.
	ErrNotPublic = errors.New("the destination is not a public address")
	// ErrResolve refuses a destination whose name could not be resolved to
	// any address at all.
	ErrResolve = errors.New("the destination could not be resolved")
)

// defaultDialer mirrors the timeouts net/http's DefaultTransport dials with.
var defaultDialer = &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}

// A Dialer opens TCP connections only to public addresses. It is what the
// callback client installs as its transport's DialContext, and it exists
// because static URL validation cannot resolve DNS: a hostname that passed
// validation can point anywhere by delivery time, so the deny list has to be
// applied to the exact addresses the name resolves to, here, where the socket
// is opened.
//
// The zero value is the production dialer. The two function fields exist so
// tests can make resolution and connection deterministic; production leaves
// them nil.
type Dialer struct {
	// Lookup resolves a hostname to all of its A/AAAA candidates. Nil uses
	// net.DefaultResolver.
	Lookup func(ctx context.Context, host string) ([]netip.Addr, error)
	// Dial opens one connection to an already-approved ip:port literal. Nil
	// uses a plain net.Dialer. It is never handed a hostname.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
}

// DialContext dials address — a host:port, as an http.Transport supplies it —
// after resolving the host once and approving every address in the answer.
//
// The socket is always opened to an IP literal taken from that single lookup,
// via net.JoinHostPort, never by handing the hostname back to a dialer that
// would resolve it a second time: a second resolution is a second chance for
// the answer to change, which is exactly the rebinding window this type
// exists to close. A mixed answer — public and non-public candidates together
// — fails the whole destination rather than dialing its public part, because
// a name pointing both at the internet and into someone's network is a
// rebinding signal, not a destination with options.
func (d *Dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return nil, errors.New("netpolicy: only TCP destinations are dialed")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		// The malformed address derives from the stored URL, so it is not
		// echoed back.
		return nil, errors.New("netpolicy: the destination address is malformed")
	}
	if !PublicHost(host) {
		return nil, ErrNotPublic
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		// A literal, and PublicHost above already approved it. Nothing to
		// resolve; dial it as it stands.
		return d.connect(ctx, network, addr, port)
	}

	addrs, err := d.lookup(ctx, host)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// A resolver error names the host it looked up; only the class of
		// failure may travel.
		return nil, ErrResolve
	}
	if len(addrs) == 0 {
		return nil, ErrResolve
	}
	// Every candidate must pass, not merely the one that ends up dialed.
	for _, addr := range addrs {
		if !PublicAddr(addr) {
			return nil, ErrNotPublic
		}
	}

	// Candidates are tried in resolver order, each dialed as the literal it
	// was approved as.
	var lastErr error
	for _, addr := range addrs {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		conn, err := d.connect(ctx, network, addr, port)
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// connect opens one socket to an approved literal, with the failure redacted.
func (d *Dialer) connect(ctx context.Context, network string, addr netip.Addr, port string) (net.Conn, error) {
	dial := d.Dial
	if dial == nil {
		dial = defaultDialer.DialContext
	}
	conn, err := dial(ctx, network, net.JoinHostPort(addr.Unmap().String(), port))
	if err != nil {
		return nil, redactDialError(err)
	}
	return conn, nil
}

// lookup resolves host through the injected resolver, or the system one.
func (d *Dialer) lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	if d.Lookup != nil {
		return d.Lookup(ctx, host)
	}
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// redactDialError keeps the class of a connection failure — refused, timed
// out, unreachable — and drops the address it names, which is derived from
// the callback URL and has no business in a log line or a stored error.
func redactDialError(err error) error {
	var op *net.OpError
	if errors.As(err, &op) && op.Err != nil {
		return fmt.Errorf("netpolicy: dial failed: %w", op.Err)
	}
	return errors.New("netpolicy: dial failed")
}
