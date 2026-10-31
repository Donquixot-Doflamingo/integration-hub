package roles

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"
)

func testConfig(role Role) Config {
	return Config{
		Role: role, HTTPAddr: "127.0.0.1:0", OpsAddr: "127.0.0.1:0", PGDSN: "postgres://unused",
		RedisAddr: "127.0.0.1:1", ShutdownGrace: 5 * time.Second, ReadyDrain: 50 * time.Millisecond,
	}
}

func TestEveryRoleStartsAndStopsCleanly(t *testing.T) {
	for _, role := range Roles {
		t.Run(string(role), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var mu sync.Mutex
			addrs := map[string]string{}
			started := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				done <- Run(ctx, testConfig(role), slog.New(slog.DiscardHandler), Options{
					OnListen: func(name, addr string) {
						mu.Lock()
						defer mu.Unlock()
						addrs[name] = addr
						if name == "ops" {
							close(started)
						}
					},
				})
			}()
			select {
			case <-started:
			case err := <-done:
				t.Fatalf("Run returned before listening: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("role did not start")
			}

			mu.Lock()
			_, hasHTTP := addrs["http"]
			opsAddr := addrs["ops"]
			mu.Unlock()
			if hasHTTP != role.servesPublicHTTP() {
				t.Errorf("public listener present = %v, want %v", hasHTTP, role.servesPublicHTTP())
			}
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+opsAddr+"/", nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("ops listener not serving: %v", err)
			}
			_ = resp.Body.Close()

			stopAt := time.Now()
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("Run: %v", err)
				}
				if d := time.Since(stopAt); d > 10*time.Second {
					t.Fatalf("shutdown took %s, want < 10s", d)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("role did not stop within 10s")
			}
		})
	}
}

func TestRunFailsWhenPortTaken(t *testing.T) {
	cfg := testConfig(Gateway)
	first := make(chan string, 2)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, cfg, slog.New(slog.DiscardHandler), Options{OnListen: func(name, addr string) {
			if name == "http" {
				first <- addr
			}
		}})
	}()
	taken := <-first
	cfg.HTTPAddr = taken
	if err := Run(t.Context(), cfg, slog.New(slog.DiscardHandler), Options{}); err == nil {
		t.Fatal("second Run on a taken port succeeded")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("first Run: %v", err)
	}
}
