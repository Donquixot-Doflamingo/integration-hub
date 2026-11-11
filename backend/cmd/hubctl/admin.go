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
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/pkg/hubclient"
)

// hubctl talks to the control plane (/admin/v1) with an OIDC bearer token (DESIGN §16.1).
// Token: -token, else HUB_ADMIN_TOKEN, else the file `hubctl login` writes. In ISFC mode the token
// is the user's ISFC access token and HUB_ADMIN_URL is the gateway route. Automation (CI) sets
// HUBCTL_ISFC_CLIENT_ID and HUBCTL_ISFC_CLIENT_SECRET instead: hubctl fetches that client's key
// from auth-service and calls the admin directly with x-isfc-client-id and x-isfc-api-key
// (AUTH_PLAN §7.5). HUB_TENANT_ID sends X-Tenant-Id.

const adminUsage = `admin commands (control plane, HUB_ADMIN_URL default http://localhost:8081):
  login -isfc [-isfc-auth-url URL] [-port N] [-no-browser]
      Sign in with ISFC: authorization code with PKCE and a loopback redirect
      (http://127.0.0.1:<port>/callback) on the public "hubctl" client. The token is saved (mode
      0600) for later commands. Set HUB_ADMIN_URL to the gateway route of the Hub admin API (hubctl
      appends /admin/v1). The auth URL defaults to HUBCTL_ISFC_AUTH_URL.
  login -dev [-role admin|editor|viewer|tenant_viewer] [-sub ID] [-email E] [-tenant T]
      Dev only: get a token from the admin's OIDC stub (HUB_ADMIN_DEV_OIDC=true) and save it
      (mode 0600) for later commands. With another issuer, export HUB_ADMIN_TOKEN instead.
  whoami
      Print the subject, roles and tenant scope of the current token (GET /admin/v1/me).

common flags: -admin-url URL, -token TOKEN, -tenant-id UUID (HUB_TENANT_ID), -isfc-auth-url URL
machine mode (CI): HUBCTL_ISFC_CLIENT_ID + HUBCTL_ISFC_CLIENT_SECRET + HUBCTL_ISFC_AUTH_URL, and
  HUB_ADMIN_URL set to the admin listener itself (not the gateway). Machines cannot unseal or purge.`

// adminFlags are the connection flags every admin command takes.
type adminFlags struct {
	url, token, tenantID, isfcAuthURL string
}

func (a *adminFlags) register(fs *flag.FlagSet) {
	def := os.Getenv("HUB_ADMIN_URL")
	if def == "" {
		def = "http://localhost:8081"
	}
	fs.StringVar(&a.url, "admin-url", def, "control plane base URL")
	fs.StringVar(&a.token, "token", "", "bearer token (default HUB_ADMIN_TOKEN, then the saved login)")
	fs.StringVar(&a.tenantID, "tenant-id", os.Getenv("HUB_TENANT_ID"), "ISFC tenant UUID, sent as X-Tenant-Id")
	fs.StringVar(&a.isfcAuthURL, "isfc-auth-url", os.Getenv("HUBCTL_ISFC_AUTH_URL"), "ISFC auth-service base URL (login -isfc and machine mode)")
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

// client builds the admin client from the flags and the credential sources: -token, else the ISFC
// machine credential (HUBCTL_ISFC_CLIENT_ID and _SECRET), else HUB_ADMIN_TOKEN, else the saved login.
func (a *adminFlags) client() (*adminClient, error) {
	hc := &http.Client{Timeout: 60 * time.Second}
	c := &adminClient{base: strings.TrimSuffix(a.url, "/"), http: hc, tenant: strings.TrimSpace(a.tenantID)}
	id, secret := os.Getenv("HUBCTL_ISFC_CLIENT_ID"), os.Getenv("HUBCTL_ISFC_CLIENT_SECRET")
	if (id == "") != (secret == "") {
		return nil, errors.New("HUBCTL_ISFC_CLIENT_ID and HUBCTL_ISFC_CLIENT_SECRET must be set together")
	}
	if a.token != "" {
		c.token = a.token
		return c, nil
	}
	if id != "" {
		if os.Getenv("HUB_ADMIN_TOKEN") != "" {
			return nil, errors.New("both an ISFC machine credential (HUBCTL_ISFC_CLIENT_ID) and HUB_ADMIN_TOKEN are set; unset one")
		}
		if a.isfcAuthURL == "" {
			return nil, errors.New("machine mode needs the ISFC auth-service URL: HUBCTL_ISFC_AUTH_URL or -isfc-auth-url")
		}
		cred, err := hubclient.NewISFCCredential(id, secret, a.isfcAuthURL, &http.Client{Timeout: 30 * time.Second})
		if err != nil {
			return nil, err
		}
		c.isfc = cred
		return c, nil
	}
	c.token = os.Getenv("HUB_ADMIN_TOKEN")
	if c.token == "" {
		b, err := os.ReadFile(tokenFile())
		if err != nil {
			return nil, errors.New("no admin credential: run `hubctl login -isfc`, or `hubctl login -dev` locally, export HUB_ADMIN_TOKEN, or set HUBCTL_ISFC_CLIENT_ID and HUBCTL_ISFC_CLIENT_SECRET")
		}
		c.token = strings.TrimSpace(string(b))
	}
	return c, nil
}

type adminClient struct {
	base, token string
	http        *http.Client
	isfc        *hubclient.ISFCCredential // machine mode: x-isfc-client-id and x-isfc-api-key instead of a bearer token
	tenant      string                    // X-Tenant-Id
}

// send performs one request with the client's credential. In machine mode a 401 or 403 makes it
// fetch a new key once and send the request again (the ISFC auth libraries' rule); a key that did
// not change is not retried. The caller closes the body.
func (c *adminClient) send(ctx context.Context, method, target string, body []byte, contentType string, extra http.Header) (*http.Response, error) {
	exchange := func(key string) (*http.Response, error) {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, target, rd)
		if err != nil {
			return nil, err
		}
		for k, vs := range extra {
			req.Header[k] = vs
		}
		if c.isfc != nil {
			req.Header.Set("x-isfc-client-id", c.isfc.ClientID())
			req.Header.Set("x-isfc-api-key", key)
		} else {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		if c.tenant != "" {
			req.Header.Set("X-Tenant-Id", c.tenant)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		return c.http.Do(req)
	}
	if c.isfc == nil {
		return exchange("")
	}
	key, err := c.isfc.Key(ctx, "")
	if err != nil {
		return nil, err
	}
	resp, err := exchange(key)
	if err != nil || (resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden) {
		return resp, err
	}
	fresh, ferr := c.isfc.Key(ctx, key)
	if ferr != nil || fresh == key {
		return resp, nil //nolint:nilerr // report the admin's answer, not a failed refresh
	}
	_ = resp.Body.Close()
	return exchange(fresh)
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
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = b
	}
	ct := ""
	if body != nil {
		ct = "application/json"
	}
	resp, err := c.send(ctx, method, c.base+"/admin/v1"+path, payload, ct, nil)
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
	isfc := fs.Bool("isfc", false, "sign in with ISFC (PKCE, loopback redirect)")
	port := fs.Int("port", 0, "login -isfc: loopback port for the redirect (default: any free port)")
	noBrowser := fs.Bool("no-browser", false, "login -isfc: print the URL, do not open a browser")
	role := fs.String("role", "admin", "role to request")
	sub := fs.String("sub", "", "subject (default $USER@hubctl)")
	email := fs.String("email", "", "email (the change log's actor)")
	tenant := fs.String("tenant", "", "tenant scope (tenant_viewer)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return 2
	}
	if *dev && *isfc {
		fmt.Fprintln(stderr, "hubctl: use -dev or -isfc, not both")
		return 2
	}
	if *isfc {
		return loginISFCCmd(isfcLoginOptions{authURL: strings.TrimSuffix(a.isfcAuthURL, "/"), clientID: isfcLoginClientID, port: *port, noBrowser: *noBrowser}, stdout, stderr)
	}
	if !*dev {
		fmt.Fprintln(stderr, "hubctl: use `login -isfc` (ISFC sign-in) or `login -dev` (local dev issuer); with another issuer, get a token from it and export HUB_ADMIN_TOKEN")
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
	path, err := saveToken(out.AccessToken)
	if err != nil {
		fmt.Fprintf(stderr, "hubctl: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "logged in as %s (%s) for %s; token saved to %s\n", *sub, *role, time.Duration(out.ExpiresIn)*time.Second, path)
	return 0
}

// loginISFCCmd signs in with ISFC and saves the token like `login -dev` does.
func loginISFCCmd(o isfcLoginOptions, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return loginISFCCmdWith(ctx, o, stdout, stderr)
}

func loginISFCCmdWith(ctx context.Context, o isfcLoginOptions, stdout, stderr io.Writer) int {
	tok, err := loginISFC(ctx, o, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "hubctl: login -isfc: %v\n", err)
		return 1
	}
	path, err := saveToken(tok.AccessToken)
	if err != nil {
		fmt.Fprintf(stderr, "hubctl: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "logged in with ISFC for %s; token saved to %s (set HUB_ADMIN_URL to the gateway route of the Hub admin API)\n", tok.ExpiresIn, path)
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
