package callbacks

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type resolverFunc func(ctx context.Context, network, host string) ([]netip.Addr, error)

func (f resolverFunc) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return f(ctx, network, host)
}

func addrs(ss ...string) []netip.Addr {
	out := make([]netip.Addr, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParseAddr(s)
	}
	return out
}

// staticResolver answers every lookup of host with the given addresses.
func staticResolver(answers map[string][]netip.Addr) Resolver {
	return resolverFunc(func(_ context.Context, _, host string) ([]netip.Addr, error) {
		if a, ok := answers[host]; ok {
			return a, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	})
}

func TestClassifyIP(t *testing.T) {
	for _, tc := range []struct{ ip, class string }{
		{"8.8.8.8", ""},
		{"1.1.1.1", ""},
		{"2606:4700:4700::1111", ""},
		{"0.0.0.0", ClassUnspecified},
		{"0.1.2.3", ClassUnspecified},
		{"10.0.0.1", ClassPrivate},
		{"10.255.255.255", ClassPrivate},
		{"172.16.0.1", ClassPrivate},
		{"172.31.255.255", ClassPrivate},
		{"172.32.0.1", ""},
		{"192.168.1.1", ClassPrivate},
		{"127.0.0.1", ClassLoopback},
		{"127.255.0.9", ClassLoopback},
		{"169.254.169.254", ClassLinkLocal},
		{"100.64.0.1", ClassCGNAT},
		{"100.127.255.255", ClassCGNAT},
		{"100.128.0.1", ""},
		{"224.0.0.1", ClassMulticast},
		{"239.255.255.250", ClassMulticast},
		{"255.255.255.255", ClassReserved},
		{"240.0.0.1", ClassReserved},
		{"192.0.2.10", ClassDocumentation},
		{"198.18.0.1", ClassReserved},
		{"::", ClassUnspecified},
		{"::1", ClassLoopback},
		{"::ffff:127.0.0.1", ClassLoopback},
		{"::ffff:10.1.2.3", ClassPrivate},
		{"::ffff:169.254.169.254", ClassLinkLocal},
		{"::ffff:8.8.8.8", ""},
		{"::10.0.0.1", ClassReserved},
		{"64:ff9b::a00:1", ClassTranslation},
		{"2002:a00:1::1", ClassTranslation},
		{"2001:0:4136:e378::1", ClassTranslation},
		{"2001:db8::1", ClassDocumentation},
		{"fc00::1", ClassPrivate},
		{"fd12:3456::1", ClassPrivate},
		{"fe80::1", ClassLinkLocal},
		{"fe80::1%en0", ClassLinkLocal},
		{"fec0::1", ClassReserved},
		{"ff02::1", ClassMulticast},
		{"100::1", ClassReserved},
		{"4000::1", ClassReserved},
	} {
		if got := ClassifyIP(netip.MustParseAddr(tc.ip)); got != tc.class {
			t.Errorf("ClassifyIP(%s) = %q, want %q", tc.ip, got, tc.class)
		}
	}
	if got := ClassifyIP(netip.Addr{}); got != ClassReserved {
		t.Errorf("ClassifyIP(invalid) = %q", got)
	}
}

// fakeNet routes every vetted dial to one test server and records the dialed addresses.
type fakeNet struct {
	mu     sync.Mutex
	dialed []string
	target string
}

func (f *fakeNet) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	f.mu.Lock()
	f.dialed = append(f.dialed, addr)
	f.mu.Unlock()
	var d net.Dialer
	return d.DialContext(ctx, network, f.target)
}

func (f *fakeNet) addrs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.dialed...)
}

// tlsReceiver is an https test server whose certificate is valid for example.com; hits counts
// requests per path.
func tlsReceiver(t *testing.T, h http.HandlerFunc) (*httptest.Server, *fakeNet, *tls.Config) {
	t.Helper()
	ts := httptest.NewTLSServer(h)
	t.Cleanup(ts.Close)
	return ts, &fakeNet{target: ts.Listener.Addr().String()}, ts.Client().Transport.(*http.Transport).TLSClientConfig
}

func newTestGuard(t *testing.T, opts GuardOptions) *Guard {
	t.Helper()
	g := NewGuard(opts)
	t.Cleanup(g.Close)
	return g
}

// post sends an empty JSON body through g and returns the response status.
func post(t *testing.T, g *Guard, rawURL string) (int, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, rawURL, strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := g.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

func TestGuardRefusesDestinations(t *testing.T) {
	var hits atomic.Int64
	_, fn, tlsCfg := tlsReceiver(t, func(http.ResponseWriter, *http.Request) { hits.Add(1) })
	res := staticResolver(map[string][]netip.Addr{
		"example.com":          addrs("8.8.8.8"),
		"internal.example.com": addrs("10.1.2.3"),
		"meta.example.com":     addrs("169.254.169.254"),
		"mixed.example.com":    addrs("8.8.8.8", "192.168.0.10"),
		"v6local.example.com":  addrs("fd00::1"),
		"mapped.example.com":   addrs("::ffff:127.0.0.1"),
		"cgnat.example.com":    addrs("100.64.1.1"),
		"lo.example.com":       addrs("127.0.0.1"),
	})
	for _, tc := range []struct {
		name, url, class string
		opts             GuardOptions
	}{
		{name: "rfc1918 literal", url: "https://10.0.0.1/cb", class: ClassPrivate},
		{name: "172.16/12 literal", url: "https://172.20.1.1/cb", class: ClassPrivate},
		{name: "192.168/16 literal", url: "https://192.168.1.1/cb", class: ClassPrivate},
		{name: "metadata literal", url: "https://169.254.169.254/latest/meta-data", class: ClassLinkLocal},
		{name: "loopback literal", url: "https://127.0.0.1:8443/cb", class: ClassLoopback},
		{name: "ipv6 loopback", url: "https://[::1]/cb", class: ClassLoopback},
		{name: "ipv4-mapped loopback", url: "https://[::ffff:127.0.0.1]/cb", class: ClassLoopback},
		{name: "ipv6 link-local", url: "https://[fe80::1]/cb", class: ClassLinkLocal},
		{name: "ipv6 ula", url: "https://[fc00::1]/cb", class: ClassPrivate},
		{name: "unspecified", url: "https://0.0.0.0/cb", class: ClassUnspecified},
		{name: "multicast", url: "https://224.0.0.1/cb", class: ClassMulticast},
		{name: "cgnat literal", url: "https://100.64.0.1/cb", class: ClassCGNAT},
		{name: "name to private", url: "https://internal.example.com/cb", class: ClassPrivate},
		{name: "name to metadata", url: "https://meta.example.com/cb", class: ClassLinkLocal},
		{name: "one private address in the answer", url: "https://mixed.example.com/cb", class: ClassPrivate},
		{name: "name to ipv6 ula", url: "https://v6local.example.com/cb", class: ClassPrivate},
		{name: "name to mapped loopback", url: "https://mapped.example.com/cb", class: ClassLoopback},
		{name: "name to cgnat", url: "https://cgnat.example.com/cb", class: ClassCGNAT},
		{name: "plain http", url: "http://example.com/cb", class: ClassPolicy},
		{name: "other scheme", url: "ftp://example.com/cb", class: ClassPolicy},
		{name: "userinfo", url: "https://user:pw@example.com/cb", class: ClassPolicy},
		{name: "not on allowlist", url: "https://example.com/cb", class: ClassPolicy, opts: GuardOptions{AllowedHosts: []string{"hooks.tenant.test"}}},
		{name: "loopback allowance keeps private refused", url: "http://internal.example.com/cb", class: ClassPrivate, opts: GuardOptions{AllowLoopback: true}},
		{name: "loopback allowance: plain http only to loopback", url: "http://example.com/cb", class: ClassPolicy, opts: GuardOptions{AllowLoopback: true}},
		{name: "loopback without allowance", url: "https://lo.example.com/cb", class: ClassLoopback},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.opts
			opts.Resolver, opts.dial, opts.tlsConfig = res, fn.dial, tlsCfg
			g := newTestGuard(t, opts)
			for _, check := range []struct {
				name string
				err  func() error
			}{
				{"CheckURL", func() error { return g.CheckURL(t.Context(), tc.url) }},
				{"Do", func() error { _, err := post(t, g, tc.url); return err }},
			} {
				err := check.err()
				var be *BlockedError
				if !errors.As(err, &be) || !errors.Is(err, ErrBlocked) || be.Class != tc.class {
					t.Fatalf("%s(%s) = %v, want BlockedError class %s", check.name, tc.url, err, tc.class)
				}
			}
		})
	}
	if hits.Load() != 0 || len(fn.addrs()) != 0 {
		t.Fatalf("refused destinations were contacted: hits=%d dials=%v", hits.Load(), fn.addrs())
	}
}

// TestGuardDNSRebinding: the name resolves to a public address when it is registered and to a
// private one when the callback is sent. The dialer resolves and checks for itself, so the
// registration-time answer is never reused and the private address is never dialed.
func TestGuardDNSRebinding(t *testing.T) {
	var hits atomic.Int64
	_, fn, tlsCfg := tlsReceiver(t, func(http.ResponseWriter, *http.Request) { hits.Add(1) })
	for _, private := range []string{"10.0.0.7", "127.0.0.1", "169.254.169.254", "::1", "::ffff:192.168.1.1"} {
		t.Run(private, func(t *testing.T) {
			var calls atomic.Int64
			res := resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
				if calls.Add(1) == 1 {
					return addrs("8.8.8.8"), nil
				}
				return addrs(private), nil
			})
			g := newTestGuard(t, GuardOptions{Resolver: res, dial: fn.dial, tlsConfig: tlsCfg})
			if err := g.CheckURL(t.Context(), "https://example.com/cb"); err != nil {
				t.Fatalf("registration check: %v", err)
			}
			_, err := post(t, g, "https://example.com/cb")
			if !errors.Is(err, ErrBlocked) {
				t.Fatalf("rebound delivery err = %v, want ErrBlocked", err)
			}
			if calls.Load() != 2 {
				t.Fatalf("resolver calls = %d, want 2 (the dialer must resolve again)", calls.Load())
			}
		})
	}
	if hits.Load() != 0 || len(fn.addrs()) != 0 {
		t.Fatalf("rebound address was contacted: hits=%d dials=%v", hits.Load(), fn.addrs())
	}
}

func TestGuardDialsTheVettedAddress(t *testing.T) {
	_, fn, tlsCfg := tlsReceiver(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	res := staticResolver(map[string][]netip.Addr{"example.com": addrs("8.8.8.8", "2606:4700:4700::1111")})
	g := newTestGuard(t, GuardOptions{Resolver: res, dial: fn.dial, tlsConfig: tlsCfg})
	status, err := post(t, g, "https://example.com/cb")
	if err != nil || status != http.StatusNoContent {
		t.Fatalf("Do = %v, %v", status, err)
	}
	if got := fn.addrs(); len(got) != 1 || got[0] != "8.8.8.8:443" {
		t.Fatalf("dialed %v, want the vetted IP literal 8.8.8.8:443", got)
	}
}

func TestGuardDoesNotFollowRedirects(t *testing.T) {
	var other atomic.Int64
	_, fn, tlsCfg := tlsReceiver(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/other" {
			other.Add(1)
			return
		}
		http.Redirect(w, r, "https://example.com/other", http.StatusTemporaryRedirect)
	})
	res := staticResolver(map[string][]netip.Addr{"example.com": addrs("8.8.8.8")})
	g := newTestGuard(t, GuardOptions{Resolver: res, dial: fn.dial, tlsConfig: tlsCfg})
	status, err := post(t, g, "https://example.com/cb")
	if err != nil || status != http.StatusTemporaryRedirect {
		t.Fatalf("Do = %v, %v; want the 307 itself", status, err)
	}
	if other.Load() != 0 {
		t.Fatal("redirect was followed")
	}
}

func TestGuardLoopbackAllowance(t *testing.T) {
	var hits atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)

	dev := newTestGuard(t, GuardOptions{AllowLoopback: true})
	if err := dev.CheckURL(t.Context(), ts.URL); err != nil {
		t.Fatalf("dev CheckURL: %v", err)
	}
	status, err := post(t, dev, ts.URL+"/hub/callbacks")
	if err != nil || status != http.StatusOK || hits.Load() != 1 {
		t.Fatalf("dev Do = %v, %v (hits %d)", status, err, hits.Load())
	}

	prod := newTestGuard(t, GuardOptions{})
	if _, err := post(t, prod, ts.URL+"/hub/callbacks"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("production guard reached a loopback receiver: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatal("production guard contacted the loopback receiver")
	}
}

func TestGuardTimeout(t *testing.T) {
	release := make(chan struct{})
	_, fn, tlsCfg := tlsReceiver(t, func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	defer close(release)
	res := staticResolver(map[string][]netip.Addr{"example.com": addrs("8.8.8.8")})
	g := newTestGuard(t, GuardOptions{Resolver: res, dial: fn.dial, tlsConfig: tlsCfg, Timeout: 150 * time.Millisecond})
	start := time.Now()
	_, err := post(t, g, "https://example.com/cb")
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("timeout took %v", d)
	}
	if NewGuard(GuardOptions{}).opts.Timeout != 5*time.Second {
		t.Fatal("default timeout is not 5 s")
	}
}

func TestGuardControlRechecksSocketAddress(t *testing.T) {
	g := newTestGuard(t, GuardOptions{})
	for addr, ok := range map[string]bool{
		"8.8.8.8:443":        true,
		"10.0.0.1:443":       false,
		"[::1]:443":          false,
		"169.254.169.254:80": false,
		"example.com:443":    false,
	} {
		err := g.control(t.Context(), "tcp", addr, nil)
		if (err == nil) != ok {
			t.Errorf("control(%s) = %v, want ok=%v", addr, err, ok)
		}
	}
}

func TestGuardAllowlist(t *testing.T) {
	g := newTestGuard(t, GuardOptions{AllowedHosts: []string{"hooks.orders.test", "*.tenant.test"}})
	for host, ok := range map[string]bool{
		"hooks.orders.test":      true,
		"HOOKS.orders.test":      true,
		"a.tenant.test":          true,
		"deep.a.tenant.test":     true,
		"tenant.test":            false,
		"eviltenant.test":        false,
		"hooks.orders.test.evil": false,
	} {
		if got := g.hostAllowed(strings.ToLower(host)); got != ok {
			t.Errorf("hostAllowed(%s) = %v, want %v", host, got, ok)
		}
	}
}
