package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// adminClient reads the control plane (contracts/openapi/hub-admin.v1.yaml) with a viewer OIDC
// token. It can only send GET requests: there is no method that could reach a mutating admin
// operation (unseal, replay, purge, pause, force; contract rule 4).
type adminClient struct {
	base      string // e.g. http://localhost:8081/admin/v1, no trailing slash
	token     secret
	http      *http.Client
	userAgent string
}

func newAdminClient(base string, token secret, hc *http.Client, userAgent string) (*adminClient, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.User != nil {
		return nil, errors.New("HUB_ADMIN_BASE_URL must be an absolute http or https URL without query or user info")
	}
	return &adminClient{base: u.String(), token: token, http: hc, userAgent: userAgent}, nil
}

// adminError is a failed admin call: the AdminError object of the envelope (passed through
// verbatim), or the transport error when the control plane could not be reached.
type adminError struct {
	Status int
	Object json.RawMessage // AdminError; nil when none was received
	Err    error           // transport failure; nil when the admin answered
}

func (e *adminError) Error() string {
	if e.Err != nil {
		return "admin API: " + e.Err.Error()
	}
	return "admin API: HTTP " + strconv.Itoa(e.Status)
}

func (e *adminError) Unwrap() error { return e.Err }

const maxAdminResponse = 16 << 20

// get performs one GET below the admin base URL and decodes a 200 body into out. Any other
// status, or a transport failure, is an *adminError.
func (c *adminClient) get(ctx context.Context, path string, query url.Values, out any) error {
	target := c.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return &adminError{Err: fmt.Errorf("build request: %w", err)}
	}
	req.Header.Set("Authorization", "Bearer "+string(c.token))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return &adminError{Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxAdminResponse+1))
	if err != nil {
		return &adminError{Err: fmt.Errorf("read response: %w", err)}
	}
	if len(b) > maxAdminResponse {
		return &adminError{Err: fmt.Errorf("response exceeds %d bytes", maxAdminResponse)}
	}
	if resp.StatusCode != http.StatusOK {
		var env struct {
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal(b, &env) == nil && len(env.Error) > 0 && env.Error[0] == '{' {
			return &adminError{Status: resp.StatusCode, Object: env.Error}
		}
		return &adminError{Status: resp.StatusCode}
	}
	if err := json.Unmarshal(b, out); err != nil {
		return &adminError{Status: resp.StatusCode, Err: fmt.Errorf("decode response: %w", err)}
	}
	return nil
}
