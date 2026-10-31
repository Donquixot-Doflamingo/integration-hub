package upstream

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// Target is where one attempt goes: an account of a vendor, one endpoint version, and the base
// URL of the account's region. Build it with TargetFor from the request's snapshot.
type Target struct {
	AccountID string
	Region    string
	Vendor    *registry.Vendor
	// EndpointName is the vendor endpoint key ("upload@1"); it labels metrics and spans.
	EndpointName string
	Endpoint     registry.Endpoint
	BaseURL      string
}

// ErrNoTarget means the snapshot cannot produce a target (unknown account, vendor mismatch,
// unknown endpoint or region). With a validated snapshot this is a bug.
var ErrNoTarget = errors.New("upstream: no target")

// TargetFor resolves accountID and endpointRef ("clevertap.upload@1", as transformers name
// their vendor_endpoint) against snap. It reads only the snapshot.
func TargetFor(snap *registry.Snapshot, accountID, endpointRef string) (Target, error) {
	vendorName, epName, ok := strings.Cut(endpointRef, ".")
	if !ok || vendorName == "" || epName == "" {
		return Target{}, fmt.Errorf("%w: endpoint ref %q, want <vendor>.<endpoint>@<version>", ErrNoTarget, endpointRef)
	}
	acct := snap.Accounts[accountID]
	if acct == nil {
		return Target{}, fmt.Errorf("%w: unknown account %q", ErrNoTarget, accountID)
	}
	if acct.Vendor != vendorName {
		return Target{}, fmt.Errorf("%w: account %q belongs to vendor %q, not %q", ErrNoTarget, accountID, acct.Vendor, vendorName)
	}
	v := snap.Vendors[vendorName]
	if v == nil {
		return Target{}, fmt.Errorf("%w: unknown vendor %q", ErrNoTarget, vendorName)
	}
	ep, ok := v.Endpoints[epName]
	if !ok {
		return Target{}, fmt.Errorf("%w: vendor %q has no endpoint %q", ErrNoTarget, vendorName, epName)
	}
	base, ok := v.Regions[acct.Region]
	if !ok {
		return Target{}, fmt.Errorf("%w: vendor %q has no region %q (account %q)", ErrNoTarget, vendorName, acct.Region, accountID)
	}
	return Target{AccountID: accountID, Region: acct.Region, Vendor: v, EndpointName: epName, Endpoint: ep, BaseURL: base}, nil
}

// allowedHosts is the egress allowlist of one vendor: scheme and host:port of every declared
// region. The URL check matches scheme and host:port; the dialer matches host:port.
type allowedHosts []allowedHost

type allowedHost struct{ scheme, hostport string }

func vendorHosts(v *registry.Vendor) allowedHosts {
	hosts := make(allowedHosts, 0, len(v.Regions))
	for _, raw := range v.Regions {
		if u, err := url.Parse(raw); err == nil {
			if hp, err := hostPort(u); err == nil {
				hosts = append(hosts, allowedHost{scheme: strings.ToLower(u.Scheme), hostport: hp})
			}
		}
	}
	return hosts
}

// allowsURL reports whether u's scheme and host:port are declared.
func (a allowedHosts) allowsURL(u *url.URL) (string, bool) {
	hp, err := hostPort(u)
	if err != nil {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	for _, x := range a {
		if x.scheme == scheme && x.hostport == hp {
			return hp, true
		}
	}
	return hp, false
}

// allows reports whether a dial address is declared.
func (a allowedHosts) allows(hostport string) bool {
	h, p, err := net.SplitHostPort(hostport)
	if err != nil {
		return false
	}
	want := net.JoinHostPort(strings.ToLower(h), p)
	for _, x := range a {
		if x.hostport == want {
			return true
		}
	}
	return false
}

// hostPort normalises a URL's authority to lowercase host:port with the scheme's default port.
func hostPort(u *url.URL) (string, error) {
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", errors.New("no host")
	}
	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "https":
			port = "443"
		case "http":
			port = "80"
		default:
			return "", fmt.Errorf("unsupported scheme %q", u.Scheme)
		}
	}
	return net.JoinHostPort(host, port), nil
}

// isLoopback reports whether host is a loopback name or address (plain http is allowed only there).
func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
