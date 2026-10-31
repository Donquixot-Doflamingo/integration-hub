package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func TestRunServesAndStops(t *testing.T) {
	vendor, control := freeAddr(t), freeAddr(t)
	ctx, cancel := context.WithCancel(t.Context())
	var stderr lockedBuffer
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"-addr", vendor, "-control-addr", control}, func(string) string { return "" }, &stderr)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+control+"/__control/healthz", nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("mockvendor did not come up: %v", err)
		}
		timer := time.NewTimer(20 * time.Millisecond)
		<-timer.C
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			stderr.mu.Lock()
			defer stderr.mu.Unlock()
			t.Fatalf("exit %d: %s", code, stderr.b.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("mockvendor did not stop")
	}
}

func TestRunRejectsBadFlags(t *testing.T) {
	var stderr bytes.Buffer
	if code := run(t.Context(), []string{"-nope"}, func(string) string { return "" }, &stderr); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "flag provided but not defined") {
		t.Fatalf("stderr %q", stderr.String())
	}
}
