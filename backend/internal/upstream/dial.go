package upstream

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"time"
)

// resolvingDialer is the default ContextDialer. net.Dialer resolves A and AAAA together and waits
// for both, so one resolver that never answers AAAA (common behind home and office DNS) holds
// every new connection until the resolver's own 5 s timeout, far past a 500 ms to 10 s connect
// timeout: the Hub answered UPSTREAM_TIMEOUT for a vendor curl reached in 40 ms (HUB-446). This
// dialer asks for IPv4 and IPv6 separately and dials the first family that answers, trying the
// other one when that fails.
type resolvingDialer struct {
	base   *net.Dialer
	lookup func(ctx context.Context, network, host string) ([]netip.Addr, error) // nil: net.DefaultResolver
}

func (d *resolvingDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || network != "tcp" {
		return d.base.DialContext(ctx, network, addr)
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return d.base.DialContext(ctx, network, addr)
	}
	lookup := d.lookup
	if lookup == nil {
		lookup = net.DefaultResolver.LookupNetIP
	}
	type answer struct {
		ips []netip.Addr
		err error
	}
	rctx, cancel := context.WithCancel(ctx)
	defer cancel() // stops a lookup that is still waiting when a connection is up
	ch := make(chan answer, 2)
	for _, family := range []string{"ip4", "ip6"} {
		go func() {
			ips, err := lookup(rctx, family, host)
			ch <- answer{ips, err}
		}()
	}
	var lastErr, timeoutErr error // timeoutErr: a lookup or dial that timed out wins, so the failure is reported as a timeout
	note := func(err error) {
		lastErr = err
		var ne net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
			timeoutErr = err
		}
	}
	for range 2 {
		select {
		case a := <-ch:
			if a.err != nil || len(a.ips) == 0 {
				if a.err != nil {
					note(a.err)
				}
				continue
			}
			for i, ip := range a.ips {
				// Each address gets an equal share of the time left (as net.Dialer does), so a
				// dead first address cannot use the whole connect timeout.
				dctx, dcancel := ctx, context.CancelFunc(func() {})
				if dl, ok := ctx.Deadline(); ok {
					dctx, dcancel = context.WithTimeout(ctx, time.Until(dl)/time.Duration(len(a.ips)-i))
				}
				conn, err := d.base.DialContext(dctx, "tcp", net.JoinHostPort(ip.String(), port))
				dcancel()
				if err == nil {
					return conn, nil
				}
				note(err)
				if ctx.Err() != nil {
					return nil, timeoutOr(timeoutErr, err)
				}
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if lastErr == nil {
		lastErr = &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return nil, timeoutOr(timeoutErr, lastErr)
}

func timeoutOr(timeoutErr, err error) error {
	if timeoutErr != nil {
		return timeoutErr
	}
	return err
}
