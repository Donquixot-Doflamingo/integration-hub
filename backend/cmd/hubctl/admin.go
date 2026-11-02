package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// hubctl talks to the control plane (/admin/v1) with an OIDC bearer token (DESIGN §16.1).
// Token: -token, else HUB_ADMIN_TOKEN, else the file `hubctl login` writes.

const adminUsage = `admin commands (control plane, HUB_ADMIN_URL default http://localhost:8081):
  login -dev [-role admin|editor|viewer|tenant_viewer] [-sub ID] [-email E] [-tenant T]
      Dev only: get a token from the admin's OIDC stub (HUB_ADMIN_DEV_OIDC=true) and save it
      (mode 0600) for later commands. With a real issuer, export HUB_ADMIN_TOKEN instead.
  whoami
      Print the subject, roles and tenant scope of the current token (GET /admin/v1/me).

common flags: -admin-url URL, -token TOKEN`

// adminFlags are the connection flags every admin command takes.
type adminFlags struct {
	url, token string
}

func (a *adminFlags) register(fs *flag.FlagSet) {
	def := os.Getenv("HUB_ADMIN_URL")
	if def == "" {
		def = "http://localhost:8081"
	}
	fs.StringVar(&a.url, "admin-url", def, "control plane base URL")
	fs.StringVar(&a.token, "token", "", "bearer token (default HUB_ADMIN_TOKEN, then the saved login)")
}

// tokenFile is where `hubctl login` keeps the token: HUBCTL_TOKEN_FILE, else
// $XDG_CONFIG_HOME/hubctl/token (~/.config/hubctl/token).
func tokenFile() string {
	if p := os.Getenv("HUBCTL_TOKEN_FILE"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "hubctl", "token")
}

// client builds the admin client from the flags and the token sources.
func (a *adminFlags) client() (*adminClient, error) {
	tok := a.token
	if tok == "" {
		tok = os.Getenv("HUB_ADMIN_TOKEN")
	}
	if tok == "" {
		b, err := os.ReadFile(tokenFile())
		if err != nil {
			return nil, errors.New("no admin token: run `hubctl login -dev` (dev) or export HUB_ADMIN_TOKEN")
		}
		tok = strings.TrimSpace(string(b))
	}
	return &adminClient{base: strings.TrimSuffix(a.url, "/"), token: tok, http: &http.Client{Timeout: 60 * time.Second}}, nil
}

type adminClient struct {
	base, token string
	http        *http.Client
}

// apiError is an admin error envelope, printed with its request id so support can find it.
type apiError struct {
	Status  int
	Code    string `json:"code"`
	Message string `json:"message"`
	Details []struct {
		Pointer string `json:"pointer"`
		Message string `json:"message"`
	} `json:"details"`
	RequestID string `json:"request_id"`
}

func (e *apiError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s (HTTP %d, request %s)", e.Code, e.Message, e.Status, e.RequestID)
	for _, d := range e.Details {
		if d.Pointer != "" {
			fmt.Fprintf(&b, "\n    %s: %s", d.Pointer, d.Message)
		} else if d.Message != e.Message {
			fmt.Fprintf(&b, "\n    %s", d.Message)
		}
	}
	return b.String()
}

// do sends one /admin/v1 request; body (if non-nil) is sent as JSON and out (if non-nil)
// receives the JSON answer.
func (c *adminClient) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+"/admin/v1"+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("admin %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var env struct {
			Error *apiError `json:"error"`
		}
		if json.Unmarshal(b, &env) == nil && env.Error != nil {
			env.Error.Status = resp.StatusCode
			if env.Error.RequestID == "" {
				env.Error.RequestID = resp.Header.Get("X-Hub-Request-Id")
			}
			return env.Error
		}
		return fmt.Errorf("admin %s %s: HTTP %d (request %s)", method, path, resp.StatusCode, resp.Header.Get("X-Hub-Request-Id"))
	}
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}

func loginCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("hubctl login", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var a adminFlags
	a.register(fs)
	dev := fs.Bool("dev", false, "use the admin's dev OIDC stub")
	role := fs.String("role", "admin", "role to request")
	sub := fs.String("sub", "", "subject (default $USER@hubctl)")
	email := fs.String("email", "", "email (the change log's actor)")
	tenant := fs.String("tenant", "", "tenant scope (tenant_viewer)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return 2
	}
	if !*dev {
		fmt.Fprintln(stderr, "hubctl: only `login -dev` is built in; with a real issuer, get a token from it and export HUB_ADMIN_TOKEN")
		return 2
	}
	if *sub == "" {
		*sub = os.Getenv("USER") + "@hubctl"
	}
	in := map[string]any{"sub": *sub, "email": *email, "roles": []string{*role}, "ttl_s": 8 * 3600}
	if *tenant != "" {
		in["tenants"] = []string{*tenant}
	}
	b, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, strings.TrimSuffix(a.url, "/")+"/oidc/dev-token", bytes.NewReader(b))
	if err != nil {
		fmt.Fprintf(stderr, "hubctl: %v\n", err)
		return 1
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "hubctl: dev token: %v\n", err)
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&out) != nil || out.AccessToken == "" {
		fmt.Fprintf(stderr, "hubctl: dev token: HTTP %d (is the admin running with HUB_ADMIN_DEV_OIDC=true?)\n", resp.StatusCode)
		return 1
	}
	path := tokenFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		fmt.Fprintf(stderr, "hubctl: %v\n", err)
		return 1
	}
	if err := os.WriteFile(path, []byte(out.AccessToken+"\n"), 0o600); err != nil {
		fmt.Fprintf(stderr, "hubctl: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "logged in as %s (%s) for %s; token saved to %s\n", *sub, *role, time.Duration(out.ExpiresIn)*time.Second, path)
	return 0
}

func whoamiCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("hubctl whoami", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var a adminFlags
	a.register(fs)
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return 2
	}
	c, err := a.client()
	if err != nil {
		fmt.Fprintf(stderr, "hubctl: %v\n", err)
		return 1
	}
	var me struct {
		Subject string   `json:"subject"`
		Email   *string  `json:"email"`
		Roles   []string `json:"roles"`
		Tenants []string `json:"tenants"`
		Expires *string  `json:"token_expires_at"`
	}
	if err := c.do(context.Background(), http.MethodGet, "/me", nil, &me); err != nil {
		fmt.Fprintf(stderr, "hubctl: %v\n", err)
		return 1
	}
	scope := "all tenants"
	if len(me.Tenants) > 0 {
		scope = "tenants " + strings.Join(me.Tenants, ", ")
	}
	exp := "unknown"
	if me.Expires != nil {
		exp = *me.Expires
	}
	fmt.Fprintf(stdout, "%s roles=%s scope=%s expires=%s\n", me.Subject, strings.Join(me.Roles, ","), scope, exp)
	return 0
}
