package main

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// adminClient reads the control plane (contracts/openapi/hub-admin.v1.yaml) with a viewer OIDC
// token. It will only send GET requests (contract rule 4); the read tools arrive in HUB-305.
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
