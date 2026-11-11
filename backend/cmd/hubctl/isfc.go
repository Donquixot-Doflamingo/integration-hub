package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ISFC sign-in for people (`hubctl login -isfc`, AUTH_PLAN §10): the authorization-code flow with
// PKCE (S256) and a loopback redirect on the public `hubctl` client, then the code is exchanged at
// auth-service. The token is saved like the dev token (mode 0600). Machines do not log in: see
// machineCredential in admin.go.

// isfcLoginClientID is the public ISFC client for people running hubctl.
const isfcLoginClientID = "hubctl"

// isfcLoginTimeout is how long login waits for the browser to come back.
const isfcLoginTimeout = 5 * time.Minute

// isfcLoginMethod is recorded in the ISFC session; the user picks the real method on the ISFC page.
const isfcLoginMethod = "EMAIL_OTP"

type isfcLoginOptions struct {
	authURL     string // auth-service / Hydra host, no trailing slash
	clientID    string
	port        int // loopback port, 0 = any free port
	noBrowser   bool
	timeout     time.Duration
	openBrowser func(url string) error // nil = the system browser
}

type isfcToken struct {
	AccessToken string
	ExpiresIn   time.Duration
}

// newPKCE returns an RFC 7636 S256 pair: a 64-byte random verifier and its SHA-256 challenge.
func newPKCE() (verifier, challenge string, err error) {
	b := make([]byte, 64)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// newDeviceID is an id of the console's form (device-<uuid>): auth-service binds a session to it.
func newDeviceID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	h := hex.EncodeToString(b)
	return "device-" + h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}

// loginISFC runs the loopback PKCE flow and returns the access token.
func loginISFC(ctx context.Context, o isfcLoginOptions, stdout io.Writer) (*isfcToken, error) {
	if o.authURL == "" {
		return nil, errors.New("the ISFC auth URL is required: -isfc-auth-url or HUBCTL_ISFC_AUTH_URL")
	}
	u, err := url.Parse(o.authURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.User != nil {
		return nil, errors.New("the ISFC auth URL must be an absolute http or https URL")
	}
	if o.timeout <= 0 {
		o.timeout = isfcLoginTimeout
	}
	verifier, challenge, err := newPKCE()
	if err != nil {
		return nil, err
	}
	state, err := randomToken(24)
	if err != nil {
		return nil, err
	}
	nonce, err := randomToken(24)
	if err != nil {
		return nil, err
	}
	deviceID, err := newDeviceID()
	if err != nil {
		return nil, err
	}

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(o.port)))
	if err != nil {
		return nil, fmt.Errorf("listen on the loopback address: %w", err)
	}
	redirectURI := "http://" + ln.Addr().String() + "/callback"

	type result struct {
		code string
		err  error
	}
	results := make(chan result, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var res result
		switch {
		case subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1:
			res.err = errors.New("the sign-in answer did not carry the expected state; start again")
		case q.Get("error") != "":
			res.err = fmt.Errorf("ISFC refused the sign-in: %s", sanitize(q.Get("error")))
		case q.Get("code") == "":
			res.err = errors.New("the sign-in answer carried no code")
		default:
			res.code = q.Get("code")
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if res.err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, "hubctl: sign-in failed. You can close this window and read the terminal.\n")
		} else {
			_, _ = io.WriteString(w, "hubctl: signed in. You can close this window.\n")
		}
		select {
		case results <- res:
		default: // a second request (favicon, reload): the first answer stands
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()

	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {o.clientID},
		"redirect_uri":          {redirectURI},
		"scope":                 {"offline_access"},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	authorize := strings.TrimRight(o.authURL, "/") + "/oauth2/auth?" + q.Encode()
	fmt.Fprintf(stdout, "Open this URL to sign in with ISFC (a browser should open by itself):\n\n  %s\n\nWaiting for the sign-in on %s ...\n", authorize, redirectURI)
	if !o.noBrowser {
		open := o.openBrowser
		if open == nil {
			open = openInBrowser
		}
		if err := open(authorize); err != nil {
			fmt.Fprintf(stdout, "(could not open a browser: %v; open the URL yourself)\n", err)
		}
	}

	wait := time.NewTimer(o.timeout)
	defer wait.Stop()
	var code string
	select {
	case r := <-results:
		if r.err != nil {
			return nil, r.err
		}
		code = r.code
	case <-wait.C:
		return nil, fmt.Errorf("no sign-in within %s", o.timeout)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return exchangeISFCCode(ctx, o, code, verifier, redirectURI, deviceID)
}

// exchangeISFCCode is `POST {auth}/oauth2/token` (authorization_code, code_verifier) with a device
// id, as the console does (AUTH_PLAN §9).
func exchangeISFCCode(ctx context.Context, o isfcLoginOptions, code, verifier, redirectURI, deviceID string) (*isfcToken, error) {
	body, _ := json.Marshal(map[string]any{
		"grant_type":    "authorization_code",
		"client_id":     o.clientID,
		"redirect_uri":  redirectURI,
		"code":          code,
		"code_verifier": verifier,
		"session_info":  map[string]any{"platform": "WEB", "login_method": isfcLoginMethod, "device_info": map[string]any{"device_id": deviceID}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(o.authURL, "/")+"/oauth2/token", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-device-id", deviceID)
	req.Header.Set("User-Agent", "hubctl")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) { // the URL error text repeats the request URL
			err = ue.Err
		}
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	var env struct {
		ErrorCode string `json:"error_code"`
		Error     string `json:"error"`
		Message   string `json:"message"`
		Data      struct {
			AccessToken string `json:"access_token"`
			ExpiresIn   int    `json:"expires_in"`
		} `json:"data"`
	}
	_ = json.Unmarshal(b, &env)
	if resp.StatusCode/100 != 2 {
		msg := sanitize(firstNonEmpty(env.Message, env.Error))
		return nil, fmt.Errorf("token exchange: HTTP %d %s %s", resp.StatusCode, sanitize(env.ErrorCode), msg)
	}
	if env.Data.AccessToken == "" {
		return nil, errors.New("token exchange: ISFC sent no access token")
	}
	return &isfcToken{AccessToken: env.Data.AccessToken, ExpiresIn: time.Duration(env.Data.ExpiresIn) * time.Second}, nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// sanitize shortens a server-supplied string and drops control characters before it reaches the terminal.
func sanitize(s string) string {
	if len(s) > 200 {
		s = s[:200]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// openInBrowser starts the system browser on a URL we built (https or http only).
func openInBrowser(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(context.Background(), "open", target) // #nosec G204 -- the URL is built by hubctl from an operator-supplied auth URL
	case "windows":
		cmd = exec.CommandContext(context.Background(), "rundll32", "url.dll,FileProtocolHandler", target) // #nosec G204 -- same
	default:
		cmd = exec.CommandContext(context.Background(), "xdg-open", target) // #nosec G204 -- same
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// saveToken writes the token file (directory 0700, file 0600).
func saveToken(token string) (string, error) {
	path := tokenFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	if err := os.Chmod(path, 0o600); err != nil { // an older file keeps its old mode otherwise
		return "", err
	}
	return path, nil
}
