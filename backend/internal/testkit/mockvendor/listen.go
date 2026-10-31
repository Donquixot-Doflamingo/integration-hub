package mockvendor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

// Instance is a running mock: the vendor surface (control API included, as in the contract)
// on one listener, and the control API alone on a second listener that stays up while the
// vendor listener refuses connections (down.mode=refuse).
type Instance struct {
	Server     *Server
	VendorURL  string // http://host:port of the vendor surface
	ControlURL string // http://host:port of the dedicated control listener

	log        *slog.Logger
	vendorAddr string // concrete address, reused when the listener is reopened

	mu       sync.Mutex
	vendor   *http.Server // nil while refusing
	control  *http.Server
	wg       sync.WaitGroup
	closed   bool
	serveErr chan error
}

// Listen starts s on vendorAddr and controlAddr (use "127.0.0.1:0" in tests).
func Listen(ctx context.Context, s *Server, vendorAddr, controlAddr string) (*Instance, error) {
	lc := net.ListenConfig{}
	vln, err := lc.Listen(ctx, "tcp", vendorAddr)
	if err != nil {
		return nil, fmt.Errorf("listen vendor %s: %w", vendorAddr, err)
	}
	cln, err := lc.Listen(ctx, "tcp", controlAddr)
	if err != nil {
		_ = vln.Close()
		return nil, fmt.Errorf("listen control %s: %w", controlAddr, err)
	}
	i := &Instance{
		Server: s, log: s.log, vendorAddr: vln.Addr().String(),
		VendorURL: "http://" + vln.Addr().String(), ControlURL: "http://" + cln.Addr().String(),
		serveErr: make(chan error, 4),
	}
	i.control = i.serve(cln, s.ControlHandler())
	i.vendor = i.serve(vln, s.Handler())

	s.mu.Lock()
	s.onRefuse = i.setRefused
	s.mu.Unlock()
	return i, nil
}

func (i *Instance) serve(ln net.Listener, h http.Handler) *http.Server {
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, ErrorLog: slog.NewLogLogger(i.log.Handler(), slog.LevelDebug)}
	i.wg.Go(func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			i.serveErr <- err
		}
	})
	return srv
}

// Err reports the first unexpected serve error, if any (the standalone binary exits on it).
func (i *Instance) Err() <-chan error { return i.serveErr }

// setRefused closes the vendor listener (clients get connection refused) or reopens it on the
// same address. It is called with Server.mu held, so it must not call back into the Server.
func (i *Instance) setRefused(refuse bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return
	}
	switch {
	case refuse && i.vendor != nil:
		_ = i.vendor.Close() // also drops open connections, like a dead host
		i.vendor = nil
		i.log.Info("vendor listener closed (down.mode=refuse)") //nolint:sloglint // called under the server lock; no request context
	case !refuse && i.vendor == nil:
		ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", i.vendorAddr)
		if err != nil {
			i.serveErr <- fmt.Errorf("reopen vendor listener %s: %w", i.vendorAddr, err)
			return
		}
		i.vendor = i.serve(ln, i.Server.Handler())
		i.log.Info("vendor listener reopened") //nolint:sloglint // as above
	}
}

// Close stops both listeners and releases hung requests. It shuts down gracefully until ctx
// ends, then closes the remaining connections (e.g. ones a client dialed but never used,
// which http.Server.Shutdown would otherwise wait 5 s for). Only a failure to close is an error.
func (i *Instance) Close(ctx context.Context) error {
	i.Server.Close()
	i.mu.Lock()
	i.closed = true
	servers := []*http.Server{i.control, i.vendor}
	i.mu.Unlock()
	var errs []error
	for _, srv := range servers {
		if srv == nil {
			continue
		}
		if err := srv.Shutdown(ctx); err != nil {
			if cerr := srv.Close(); cerr != nil {
				errs = append(errs, cerr)
			}
		}
	}
	i.wg.Wait()
	return errors.Join(errs...)
}
