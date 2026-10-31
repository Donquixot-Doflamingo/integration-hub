package core

import "net/http"

// VendorRequest is the request a transformer built for a vendor endpoint (DESIGN §7). The base
// URL comes from the account's region at send time, so it is not part of the request.
type VendorRequest struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Query   map[string]string `json:"query,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	// Body is the JSON tree to send; nil means no body.
	Body any `json:"body,omitempty"`
}

// VendorResponse is what a vendor answered, as the response transformer sees it.
type VendorResponse struct {
	Status  int         `json:"status"`
	Headers http.Header `json:"headers,omitempty"`
	// Body is the parsed JSON body, or the raw text when it is not JSON, or nil when empty.
	Body any `json:"body"`
}
