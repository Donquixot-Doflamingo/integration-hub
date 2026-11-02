package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control/adminapi"
)

// adminServer runs the admin API (no database) and the dev issuer on one test server.
func adminServer(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	iss, err := adminapi.NewDevIssuer(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("/admin/v1/", adminapi.New(adminapi.Deps{Verifier: iss.Verifier()}).Handler())
	mux.Handle("/oidc/", iss.Handler())
	return srv.URL
}

func TestLoginThenWhoami(t *testing.T) {
	url := adminServer(t)
	file := filepath.Join(t.TempDir(), "token")
	t.Setenv("HUBCTL_TOKEN_FILE", file)
	t.Setenv("HUB_ADMIN_TOKEN", "")
	var out, errb bytes.Buffer
	if code := run([]string{"login", "-dev", "-admin-url", url, "-role", "editor", "-sub", "priya", "-email", "priya@example.com"}, &out, &errb); code != 0 {
		t.Fatalf("login %d: %s", code, errb.String())
	}
	st, err := os.Stat(file)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("token file %v %v", st, err)
	}
	out.Reset()
	if code := run([]string{"whoami", "-admin-url", url}, &out, &errb); code != 0 || !strings.Contains(out.String(), "priya roles=editor scope=all tenants") {
		t.Fatalf("whoami %d: %s %s", code, out.String(), errb.String())
	}
}

func TestAdminErrorsCarryTheRequestID(t *testing.T) {
	url := adminServer(t)
	t.Setenv("HUBCTL_TOKEN_FILE", filepath.Join(t.TempDir(), "none"))
	t.Setenv("HUB_ADMIN_TOKEN", "not.a.token")
	var out, errb bytes.Buffer
	if code := run([]string{"whoami", "-admin-url", url}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "UNAUTHORIZED") || !strings.Contains(errb.String(), "request req_") {
		t.Fatalf("bad token: %d %s", code, errb.String())
	}
	t.Setenv("HUB_ADMIN_TOKEN", "")
	if code := run([]string{"whoami", "-admin-url", url}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "hubctl login -dev") {
		t.Fatalf("no token: %d %s", code, errb.String())
	}
}

// fakeAdmin serves canned answers keyed "METHOD /path" (query ignored): []byte bodies raw,
// func(*http.Request) any computed per request, anything else as JSON. Requests must carry a bearer token.
func fakeAdmin(t *testing.T, routes map[string]any) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "no token", http.StatusUnauthorized)
			return
		}
		v, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
			return
		}
		if f, dynamic := v.(func(*http.Request) any); dynamic {
			v = f(r)
		}
		if b, raw := v.([]byte); raw {
			_, _ = w.Write(b)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}
