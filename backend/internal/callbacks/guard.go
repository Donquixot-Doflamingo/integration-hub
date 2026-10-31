package callbacks

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// SSRF guard (DESIGN §12.1, HUB-119). Every callback goes through Guard.Do. The guard resolves
// the host itself and dials the vetted IP literal, so the address that is checked is the
// address that is connected to: a DNS answer that changes between a registration-time check
// and the delivery (DNS rebinding) cannot reach a private address. The dialer's ControlContext
// re-checks the socket address just before connect as a second line of defence. Redirects are
// never followed, proxies from the environment are ignored, and one delivery has a 5 s budget.

// DefaultTimeout is the total budget of one callback attempt (DESIGN §12.1).
const DefaultTimeout = 5 * time.Second

// IP classes the guard refuses; also the bounded label values of callback_ssrf_blocked_total.
const (
	ClassUnspecified   = "unspecified"
	ClassLoopback      = "loopback"
	ClassPrivate       = "private"
	ClassCGNAT         = "cgnat"
	ClassLinkLocal     = "link_local"
	ClassMulticast     = "multicast"
	ClassDocumentation = "documentation"
	ClassTranslation   = "translation"
	ClassReserved      = "reserved"
	// ClassPolicy is not an IP class: the URL itself was refused (scheme, host allowlist, form).
	ClassPolicy = "policy"
)

type blockedRange struct {
	prefix netip.Prefix
	class  string
}

// blockedRanges is checked in order; the first match names the class. IPv4-mapped IPv6
// addresses (::ffff:a.b.c.d) are unmapped before the check, so they match the IPv4 rows.
var blockedRanges = func() []blockedRange {
	rows := []struct{ cidr, class string }{
		{"0.0.0.0/8", ClassUnspecified},
		{"10.0.0.0/8", ClassPrivate},
		{"100.64.0.0/10", ClassCGNAT},
		{"127.0.0.0/8", ClassLoopback},
		{"169.254.0.0/16", ClassLinkLocal}, // includes the cloud metadata address 169.254.169.254
		{"172.16.0.0/12", ClassPrivate},
		{"192.0.0.0/24", ClassReserved},
		{"192.0.2.0/24", ClassDocumentation},
		{"192.88.99.0/24", ClassTranslation}, // 6to4 relay anycast
		{"192.168.0.0/16", ClassPrivate},
		{"198.18.0.0/15", ClassReserved}, // benchmarking
		{"198.51.100.0/24", ClassDocumentation},
		{"203.0.113.0/24", ClassDocumentation},
		{"224.0.0.0/4", ClassMulticast},
		{"240.0.0.0/4", ClassReserved}, // includes 255.255.255.255
		{"::/128", ClassUnspecified},
		{"::1/128", ClassLoopback},
		{"::/96", ClassReserved},           // deprecated IPv4-compatible
		{"::ffff:0:0:0/96", ClassReserved}, // IPv4-translated (RFC 2765)
		{"64:ff9b::/96", ClassTranslation}, // NAT64: would reach the embedded IPv4 address
		{"64:ff9b:1::/48", ClassTranslation},
		{"100::/64", ClassReserved}, // discard
		{"2001::/32", ClassTranslation},
		{"2001:db8::/32", ClassDocumentation},
		{"2001::/23", ClassReserved},
		{"2002::/16", ClassTranslation}, // 6to4
		{"fc00::/7", ClassPrivate},      // unique local
		{"fe80::/10", ClassLinkLocal},
		{"fec0::/10", ClassReserved}, // deprecated site-local
		{"ff00::/8", ClassMulticast},
	}
	out := make([]blockedRange, len(rows))
	for i, r := range rows {
		out[i] = blockedRange{netip.MustParsePrefix(r.cidr), r.class}
	}
	return out
}()

var globalUnicast6 = netip.MustParsePrefix("2000::/3")

// ClassifyIP returns the refused class of ip, or "" for a public unicast address. Anything
// IPv6 outside 2000::/3 (global unicast) is reserved.
func ClassifyIP(ip netip.Addr) string {
	if !ip.IsValid() {
		return ClassReserved
	}
	ip = ip.WithZone("").Unmap()
	for _, r := range blockedRanges {
		if r.prefix.Contains(ip) {
			return r.class
		}
	}
	if ip.Is6() && !globalUnicast6.Contains(ip) {
		return ClassReserved
	}
	return ""
}

// ErrBlocked is matched (errors.Is) by every refusal of the guard.
var ErrBlocked = errors.New("callbacks: destination refused by the SSRF guard")

// BlockedError says why a destination was refused. It never carries the secret or the body.
type BlockedError struct {
	Host   string
	Class  string // one of the Class* constants
	Detail string
}

func (e *BlockedError) Error() string {
	return fmt.Sprintf("callbacks: destination %q refused by the SSRF guard (%s): %s", e.Host, e.Class, e.Detail)
}

// Unwrap lets errors.Is(err, ErrBlocked) match.
func (e *BlockedError) Unwrap() error { return ErrBlocked }

// Resolver resolves host names to addresses; *net.Resolver implements it.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// GuardOptions configure a Guard. Zero values take the documented defaults.
type GuardOptions struct {
	// Timeout is the total budget of one request (dial, TLS, headers, body). Default 5 s.
	Timeout time.Duration
	// AllowedHosts, when not empty, restricts callbacks to these hosts: an exact host name, or
	// "*.example.com" for any subdomain of example.com. Empty means any public host.
	AllowedHosts []string
	// AllowLoopback is the dev/test allowance for local test receivers: loopback addresses
	// (127.0.0.0/8, ::1) are permitted, and plain http is permitted when every address the host
	// resolves to is loopback. Every other private range stays refused. Production wiring never
	// sets it; the dev registrar (config/registrar/reg_orders_cb.yaml, http://127.0.0.1:8097)
	// needs it.
	AllowLoopback bool
	// Resolver resolves host names. Default net.DefaultResolver.
	Resolver Resolver

	// Test seams: dial replaces the network dial of a vetted address; tlsConfig the TLS config.
	dial      func(ctx context.Context, network, addr string) (net.Conn, error)
	tlsConfig *tls.Config
}

// Guard sends HTTP requests to tenant endpoints only after the SSRF checks pass.
type Guard struct {
	opts   GuardOptions
	client *http.Client
}

// NewGuard builds a guard with its own transport (no proxy, no redirects).
func NewGuard(opts GuardOptions) *Guard {
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	if opts.Resolver == nil {
		opts.Resolver = net.DefaultResolver
	}
	g := &Guard{opts: opts}
	tr := &http.Transport{
		Proxy:                  nil, // never via HTTP(S)_PROXY: the proxy would dial unchecked
		DialContext:            g.dialContext,
		TLSClientConfig:        opts.tlsConfig,
		TLSHandshakeTimeout:    opts.Timeout,
		ResponseHeaderTimeout:  opts.Timeout,
		MaxIdleConns:           64,
		MaxIdleConnsPerHost:    4,
		IdleConnTimeout:        90 * time.Second,
		MaxResponseHeaderBytes: 64 << 10,
		ForceAttemptHTTP2:      true,
	}
	g.client = &http.Client{
		Transport: tr,
		Timeout:   opts.Timeout,
		// A redirect is returned as the response (counted as a failed attempt), never followed.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return g
}

// Close drops idle connections.
func (g *Guard) Close() { g.client.CloseIdleConnections() }

type plainHTTPKey struct{}

// CheckURL validates raw the way a registration does (admin API, HUB-201): form, scheme, host
// allowlist, and every address the host resolves to now. Delivery re-checks at dial time, so a
// pass here is not trusted later.
func (g *Guard) CheckURL(ctx context.Context, raw string) error {
	u, err := g.checkForm(raw)
	if err != nil {
		return err
	}
	_, err = g.vet(ctx, u.Hostname(), u.Scheme == "http")
	return err
}

// Do sends req through the guard. The URL form, scheme and allowlist are checked here; the
// addresses are checked in the dialer for the IP actually dialed. Redirect responses are
// returned unfollowed. The caller closes the response body.
func (g *Guard) Do(req *http.Request) (*http.Response, error) {
	u, err := g.checkForm(req.URL.String())
	if err != nil {
		return nil, err
	}
	if u.Scheme == "http" {
		req = req.WithContext(context.WithValue(req.Context(), plainHTTPKey{}, true))
	}
	return g.client.Do(req)
}

func (g *Guard) checkForm(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return nil, &BlockedError{Host: "", Class: ClassPolicy, Detail: "not an absolute URL"}
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case u.User != nil:
		return nil, &BlockedError{Host: host, Class: ClassPolicy, Detail: "credentials in the URL are not allowed"}
	case u.Scheme == "https":
	case u.Scheme == "http" && g.opts.AllowLoopback:
		// Allowed only towards loopback; the dialer enforces it per address.
	default:
		return nil, &BlockedError{Host: host, Class: ClassPolicy, Detail: "scheme must be https"}
	}
	if !g.hostAllowed(host) {
		return nil, &BlockedError{Host: host, Class: ClassPolicy, Detail: "host is not on the callback allowlist"}
	}
	return u, nil
}

func (g *Guard) hostAllowed(host string) bool {
	if len(g.opts.AllowedHosts) == 0 {
		return true
	}
	for _, a := range g.opts.AllowedHosts {
		a = strings.ToLower(a)
		if suffix, ok := strings.CutPrefix(a, "*."); ok {
			if strings.HasSuffix(host, "."+suffix) {
				return true
			}
		} else if host == a {
			return true
		}
	}
	return false
}

// refused returns the refused class of ip for this guard, or "".
func (g *Guard) refused(ip netip.Addr, plainHTTP bool) string {
	class := ClassifyIP(ip)
	if class == ClassLoopback && g.opts.AllowLoopback {
		class = ""
	}
	if class == "" && plainHTTP && ClassifyIP(ip) != ClassLoopback {
		return ClassPolicy // http is only for loopback test receivers
	}
	return class
}

// vet resolves host and returns its addresses only if every one of them is allowed: one bad
// address in the answer refuses the whole host, so round-robin answers cannot smuggle one in.
func (g *Guard) vet(ctx context.Context, host string, plainHTTP bool) ([]netip.Addr, error) {
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{ip}
	} else {
		ips, err = g.opts.Resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("callbacks: resolve %s: %w", host, err)
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("callbacks: resolve %s: no addresses", host)
	}
	for _, ip := range ips {
		if class := g.refused(ip, plainHTTP); class != "" {
			detail := "resolves to a " + class + " address"
			if class == ClassPolicy {
				detail = "plain http is only allowed to loopback test receivers"
			}
			return nil, &BlockedError{Host: host, Class: class, Detail: detail}
		}
	}
	return ips, nil
}

func (g *Guard) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("callbacks: dial %s: %w", addr, err)
	}
	plainHTTP, _ := ctx.Value(plainHTTPKey{}).(bool)
	ips, err := g.vet(ctx, host, plainHTTP)
	if err != nil {
		return nil, err
	}
	dial := g.opts.dial
	if dial == nil {
		d := &net.Dialer{Timeout: g.opts.Timeout, ControlContext: g.control}
		dial = d.DialContext
	}
	var errs []error
	for _, ip := range ips {
		conn, err := dial(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}

// control re-checks the socket address right before connect(2): whatever produced the address,
// a refused one never gets a connection.
func (g *Guard) control(ctx context.Context, _, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return &BlockedError{Host: address, Class: ClassPolicy, Detail: "dial address is not an IP literal"}
	}
	plainHTTP, _ := ctx.Value(plainHTTPKey{}).(bool)
	if class := g.refused(ap.Addr(), plainHTTP); class != "" {
		return &BlockedError{Host: address, Class: class, Detail: "dial address refused"}
	}
	return nil
}
