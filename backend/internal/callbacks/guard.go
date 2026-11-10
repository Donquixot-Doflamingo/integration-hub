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
	// TLS 1.2+ with verification (DESIGN §16.5), explicit rather than Go's default client floor;
	// the test seam may add roots but can never lower the floor.
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if opts.tlsConfig != nil {
		tlsCfg = opts.tlsConfig.Clone()
		tlsCfg.MinVersion = max(tlsCfg.MinVersion, tls.VersionTLS12)
	}
	tr := &http.Transport{
		Proxy:                  nil, // never via HTTP(S)_PROXY: the proxy would dial unchecked
		DialContext:            g.dialContext,
		TLSClientConfig:        tlsCfg,
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

// otherFamilyGrace is how long, after one address family answered, the other one is still
// waited for so that its addresses are vetted too. A family that stays silent (a resolver that
// never answers AAAA) costs at most this much, not the whole budget.
const otherFamilyGrace = 150 * time.Millisecond

// familyAnswer is the outcome of one ip4 or ip6 lookup.
type familyAnswer struct {
	ips []netip.Addr
	err error
}

// resolution is a host being resolved: the ip4 and ip6 lookups run in parallel and the first
// family that answers is used; the other is a fallback.
type resolution struct {
	g         *Guard
	host      string
	plainHTTP bool
	cancel    context.CancelFunc
	answers   chan familyAnswer
	pending   int // lookups not yet received
}

func (r *resolution) close() { r.cancel() }

// vetFamily returns the addresses of one answer after vetting every one of them.
func (r *resolution) vetFamily(ips []netip.Addr) ([]netip.Addr, error) {
	if err := r.g.vetAll(r.host, ips, r.plainHTTP); err != nil {
		return nil, err
	}
	return ips, nil
}

// start launches the two lookups. The returned resolution must be closed.
func (g *Guard) start(ctx context.Context, host string, plainHTTP bool) *resolution {
	lctx, cancel := context.WithCancel(ctx)
	r := &resolution{g: g, host: host, plainHTTP: plainHTTP, cancel: cancel, answers: make(chan familyAnswer, 2), pending: 2}
	for _, network := range []string{"ip4", "ip6"} {
		go func() {
			ips, err := g.opts.Resolver.LookupNetIP(lctx, network, host)
			// Keep each lookup to its own family: a resolver that answers both for either network
			// would otherwise duplicate addresses.
			var kept []netip.Addr
			for _, ip := range ips {
				if ip.Is4() == (network == "ip4") {
					kept = append(kept, ip)
				}
			}
			r.answers <- familyAnswer{ips: kept, err: err}
		}()
	}
	return r
}

// next waits for the next lookup that produced addresses. ok=false: the remaining lookups ended
// without addresses (err is the first failure), wait fired, or ctx ended.
func (r *resolution) next(ctx context.Context, wait <-chan time.Time) (ips []netip.Addr, ok bool, err error) {
	for r.pending > 0 {
		select {
		case a := <-r.answers:
			r.pending--
			if a.err == nil && len(a.ips) > 0 {
				return a.ips, true, nil
			}
			if err == nil {
				err = a.err
			}
		case <-wait:
			return nil, false, nil
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}
	return nil, false, err
}

// vetAll refuses the host when any of ips is refused: one bad address in an answer refuses the
// whole host, so round-robin answers cannot smuggle one in.
func (g *Guard) vetAll(host string, ips []netip.Addr, plainHTTP bool) error {
	for _, ip := range ips {
		if class := g.refused(ip, plainHTTP); class != "" {
			detail := "resolves to a " + class + " address"
			if class == ClassPolicy {
				detail = "plain http is only allowed to loopback test receivers"
			}
			return &BlockedError{Host: host, Class: class, Detail: detail}
		}
	}
	return nil
}

// resolve returns the vetted addresses of host from the first family that answers, plus the
// other family's addresses when they arrive within the grace period; a family still pending is
// left to the caller as a fallback (r != nil, close it). A refused address in any answer that
// arrived refuses the host.
func (g *Guard) resolve(ctx context.Context, host string, plainHTTP bool) (ips []netip.Addr, r *resolution, err error) {
	if ip, perr := netip.ParseAddr(host); perr == nil {
		if err := g.vetAll(host, []netip.Addr{ip}, plainHTTP); err != nil {
			return nil, nil, err
		}
		return []netip.Addr{ip}, nil, nil
	}
	r = g.start(ctx, host, plainHTTP)
	first, ok, err := r.next(ctx, nil)
	if !ok {
		r.close()
		if err == nil {
			err = errors.New("no addresses")
		}
		return nil, nil, fmt.Errorf("callbacks: resolve %s: %w", host, err)
	}
	if ips, err = r.vetFamily(first); err != nil {
		r.close()
		return nil, nil, err
	}
	if r.pending > 0 {
		grace := time.NewTimer(otherFamilyGrace)
		defer grace.Stop()
		if more, ok, _ := r.next(ctx, grace.C); ok {
			extra, err := r.vetFamily(more)
			if err != nil {
				r.close()
				return nil, nil, err
			}
			ips = append(ips, extra...)
		}
	}
	return ips, r, nil
}

// vet resolves host and returns its addresses, all vetted (CheckURL).
func (g *Guard) vet(ctx context.Context, host string, plainHTTP bool) ([]netip.Addr, error) {
	ips, r, err := g.resolve(ctx, host, plainHTTP)
	if r != nil {
		r.close()
	}
	return ips, err
}

// dialTimeoutError reports a dial that ran out of time as a timeout, so the attempt is classed
// "timeout" and not "connect", even when an earlier address was refused outright.
type dialTimeoutError struct{ err error }

func (e *dialTimeoutError) Error() string   { return e.err.Error() }
func (e *dialTimeoutError) Unwrap() error   { return e.err }
func (e *dialTimeoutError) Timeout() bool   { return true }
func (e *dialTimeoutError) Temporary() bool { return true }

func (g *Guard) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("callbacks: dial %s: %w", addr, err)
	}
	plainHTTP, _ := ctx.Value(plainHTTPKey{}).(bool)
	queue, r, err := g.resolve(ctx, host, plainHTTP)
	if err != nil {
		return nil, err
	}
	if r != nil {
		defer r.close()
	}
	dial := g.opts.dial
	if dial == nil {
		d := &net.Dialer{Timeout: g.opts.Timeout, ControlContext: g.control}
		dial = d.DialContext
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(g.opts.Timeout)
	}
	var errs []error
	timedOut := false
	for i := 0; ; i++ {
		if i >= len(queue) {
			// Out of vetted addresses: wait for the family that had not answered yet.
			if r == nil || r.pending == 0 {
				break
			}
			more, ok, _ := r.next(ctx, nil)
			if !ok {
				break
			}
			extra, err := r.vetFamily(more)
			if err != nil {
				return nil, err
			}
			queue = append(queue, extra...)
			i--
			continue
		}
		// An equal share of what is left for each address still to try, and one more for the
		// family that has not answered: one black-holed address cannot use all of the budget.
		left := len(queue) - i
		if r != nil && r.pending > 0 {
			left++
		}
		dctx, cancel := context.WithTimeout(ctx, max(time.Until(deadline), time.Millisecond)/time.Duration(left))
		conn, err := dial(dctx, network, net.JoinHostPort(queue[i].String(), port))
		cancel()
		if err == nil {
			return conn, nil
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			timedOut = true
		}
		errs = append(errs, err)
		if ctx.Err() != nil {
			break
		}
	}
	joined := errors.Join(errs...)
	if joined == nil {
		joined = errors.New("callbacks: dial: no address to try")
	}
	if timedOut {
		return nil, &dialTimeoutError{err: joined}
	}
	return nil, joined
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
