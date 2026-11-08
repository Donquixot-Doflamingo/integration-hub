package callbacks

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"testing"
	"time"
)

// familyResolver answers ip4 and ip6 lookups separately. A nil answer for a family blocks until
// the lookup's context ends, like a resolver that never answers that record type.
type familyResolver struct {
	v4, v6 []netip.Addr
	delay4 time.Duration
}

func (f familyResolver) LookupNetIP(ctx context.Context, network, _ string) ([]netip.Addr, error) {
	ans := f.v6
	if network == "ip4" {
		ans = f.v4
		if f.delay4 > 0 {
			select {
			case <-time.After(f.delay4):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	if ans == nil {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return ans, nil
}

// TestGuardResolverThatNeverAnswersOneFamily: the answered family is used at once; a lookup that
// hangs for the other one does not use up the budget (HUB-452).
func TestGuardResolverThatNeverAnswersOneFamily(t *testing.T) {
	for _, tc := range []struct {
		name string
		res  familyResolver
		want string
	}{
		{"AAAA never answers", familyResolver{v4: addrs("8.8.8.8")}, "8.8.8.8:443"},
		{"A never answers", familyResolver{v6: addrs("2606:4700:4700::1111")}, "[2606:4700:4700::1111]:443"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, fn, tlsCfg := tlsReceiver(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
			g := newTestGuard(t, GuardOptions{Resolver: tc.res, dial: fn.dial, tlsConfig: tlsCfg, Timeout: 3 * time.Second})
			start := time.Now()
			code, err := post(t, g, "https://example.com/cb")
			if err != nil || code != http.StatusNoContent {
				t.Fatalf("status %d err %v", code, err)
			}
			if d := time.Since(start); d > time.Second {
				t.Fatalf("took %v with one family unanswered", d)
			}
			if got := fn.addrs(); len(got) != 1 || got[0] != tc.want {
				t.Fatalf("dialed %v, want [%s]", got, tc.want)
			}
		})
	}
}

func TestGuardCheckURLDoesNotWaitForTheSilentFamily(t *testing.T) {
	g := newTestGuard(t, GuardOptions{Resolver: familyResolver{v4: addrs("8.8.8.8")}, Timeout: 3 * time.Second})
	start := time.Now()
	if err := g.CheckURL(t.Context(), "https://example.com/cb"); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("CheckURL took %v", d)
	}
}

// TestGuardDeadFirstAddress: an address that black-holes gets an equal share of the time, not all
// of it, so the next address is still tried and the request succeeds.
func TestGuardDeadFirstAddress(t *testing.T) {
	_, fn, tlsCfg := tlsReceiver(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	var mu sync.Mutex
	var order []string
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		mu.Lock()
		order = append(order, addr)
		mu.Unlock()
		if addr == "[2606:4700:4700::1111]:443" {
			<-ctx.Done() // black hole
			return nil, ctx.Err()
		}
		return fn.dial(ctx, network, addr)
	}
	// AAAA answers first and sorts first; A answers a little later.
	res := familyResolver{v4: addrs("8.8.8.8"), v6: addrs("2606:4700:4700::1111"), delay4: 20 * time.Millisecond}
	g := newTestGuard(t, GuardOptions{Resolver: res, dial: dial, tlsConfig: tlsCfg, Timeout: 2 * time.Second})
	start := time.Now()
	code, err := post(t, g, "https://example.com/cb")
	if err != nil || code != http.StatusNoContent {
		t.Fatalf("status %d err %v", code, err)
	}
	if d := time.Since(start); d > 1900*time.Millisecond {
		t.Fatalf("took %v: the dead address used the whole budget", d)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[1] != "8.8.8.8:443" {
		t.Fatalf("dial order %v", order)
	}
}

// TestGuardFallsBackToTheOtherFamilyLate: the first family's only address is dead and the other
// family answers after the first dial began; it is still used.
func TestGuardFallsBackToTheOtherFamilyLate(t *testing.T) {
	_, fn, tlsCfg := tlsReceiver(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr == "[2606:4700:4700::1111]:443" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return fn.dial(ctx, network, addr)
	}
	res := familyResolver{v4: addrs("8.8.8.8"), v6: addrs("2606:4700:4700::1111"), delay4: 600 * time.Millisecond}
	g := newTestGuard(t, GuardOptions{Resolver: res, dial: dial, tlsConfig: tlsCfg, Timeout: 3 * time.Second})
	code, err := post(t, g, "https://example.com/cb")
	if err != nil || code != http.StatusNoContent {
		t.Fatalf("status %d err %v", code, err)
	}
}

// TestGuardAllDeadAddressesTimeOut: every address dead is a timeout (retry class "timeout"), and
// it ends within the budget.
func TestGuardAllDeadAddressesTimeOut(t *testing.T) {
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	res := familyResolver{v4: addrs("8.8.8.8"), v6: addrs("2606:4700:4700::1111")}
	g := newTestGuard(t, GuardOptions{Resolver: res, dial: dial, Timeout: 400 * time.Millisecond})
	start := time.Now()
	_, err := post(t, g, "https://example.com/cb")
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %v", d)
	}
}

// TestGuardEveryResolvedAddressIsVetted: the host is refused when any address of any family that
// answered is refused (decision kept from HUB-119: a round-robin answer cannot smuggle one in).
func TestGuardEveryResolvedAddressIsVetted(t *testing.T) {
	for _, tc := range []struct {
		name  string
		res   familyResolver
		class string
	}{
		{"all private", familyResolver{v4: addrs("10.0.0.1"), v6: addrs("fd00::1")}, ClassPrivate},
		{"public A, private A", familyResolver{v4: addrs("8.8.8.8", "10.0.0.1"), v6: addrs("2606:4700:4700::1111")}, ClassPrivate},
		{"public A, private AAAA", familyResolver{v4: addrs("8.8.8.8"), v6: addrs("fd00::1")}, ClassPrivate},
		{"private A, silent AAAA", familyResolver{v4: addrs("169.254.169.254")}, ClassLinkLocal},
		{"mapped metadata", familyResolver{v6: addrs("::ffff:169.254.169.254")}, ClassLinkLocal},
		{"NAT64", familyResolver{v6: addrs("64:ff9b::a00:1")}, ClassTranslation},
		{"loopback", familyResolver{v4: addrs("127.0.0.1")}, ClassLoopback},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fn := &fakeNet{target: "127.0.0.1:1"}
			g := newTestGuard(t, GuardOptions{Resolver: tc.res, dial: fn.dial, Timeout: 2 * time.Second})
			_, err := post(t, g, "https://example.com/cb")
			var be *BlockedError
			if !errors.As(err, &be) || be.Class != tc.class {
				t.Fatalf("err = %v, want blocked %s", err, tc.class)
			}
			if len(fn.addrs()) != 0 {
				t.Fatalf("dialed %v", fn.addrs())
			}
			if err := g.CheckURL(t.Context(), "https://example.com/cb"); !errors.As(err, &be) || be.Class != tc.class {
				t.Fatalf("CheckURL err = %v", err)
			}
		})
	}
}
