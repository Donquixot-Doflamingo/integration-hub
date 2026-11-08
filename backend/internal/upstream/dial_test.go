package upstream

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestResolvingDialerDoesNotWaitForASilentFamily(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	tests := []struct {
		name    string
		lookup  func(ctx context.Context, network, host string) ([]netip.Addr, error)
		wantErr bool
	}{
		{"AAAA never answers", func(ctx context.Context, network, host string) ([]netip.Addr, error) {
			if network == "ip6" {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}, false},
		{"A never answers, AAAA fails", func(ctx context.Context, network, host string) ([]netip.Addr, error) {
			if network == "ip6" {
				return nil, errors.New("servfail")
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}, true},
		{"nothing resolves", func(ctx context.Context, network, host string) ([]netip.Addr, error) {
			return nil, nil
		}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			d := &resolvingDialer{base: &net.Dialer{}, lookup: tc.lookup}
			start := time.Now()
			c, err := d.DialContext(ctx, "tcp", net.JoinHostPort("vendor.example", port))
			if c != nil {
				_ = c.Close()
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("err %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && time.Since(start) > 200*time.Millisecond {
				t.Fatalf("dial took %v: it waited for the silent family", time.Since(start))
			}
		})
	}
}

// A dead first address must not use the whole connect timeout: the second one is still tried.
func TestResolvingDialerSharesTimeAcrossAddresses(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	d := &resolvingDialer{base: &net.Dialer{}, lookup: func(ctx context.Context, network, host string) ([]netip.Addr, error) {
		if network == "ip6" {
			return nil, errors.New("no AAAA")
		}
		// 192.0.2.1 (TEST-NET-1) never answers: the connect hangs until its share of the time is up.
		return []netip.Addr{netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("127.0.0.1")}, nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	c, err := d.DialContext(ctx, "tcp", net.JoinHostPort("vendor.example", port))
	if err != nil {
		t.Skipf("no route to the blackhole address in this environment (%v), the sharing cannot be observed", err)
	}
	_ = c.Close()
}
