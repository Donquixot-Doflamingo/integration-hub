package mockvendor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"
)

// Start runs a mock in-process on random local ports for the duration of the test.
//
//	mv := mockvendor.Start(t)
//	mv.SetRoute(t, "ct-upload", mockvendor.RouteSpec{Match: ..., Behaviour: ...})
//	// point the code under test at mv.VendorURL, then assert on mv.Requests(t, nil)
func Start(t testing.TB) *Instance {
	t.Helper()
	i, err := Listen(t.Context(), New(nil), "127.0.0.1:0", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("start mockvendor: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		if err := i.Close(ctx); err != nil {
			t.Errorf("close mockvendor: %v", err)
		}
	})
	return i
}

// Control sends a control API request to the dedicated control listener and decodes a JSON
// response into out (may be nil). It returns the status code.
func (i *Instance) Control(t testing.TB, method, path string, in, out any) int {
	t.Helper()
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			t.Fatalf("marshal %T: %v", in, err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, i.ControlURL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil && resp.StatusCode < 300 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode %s %s: %v", method, path, err)
		}
	}
	return resp.StatusCode
}

// SetRoute creates or replaces a route and fails the test on error.
func (i *Instance) SetRoute(t testing.TB, id string, spec RouteSpec) Route {
	t.Helper()
	var rt Route
	if code := i.Control(t, http.MethodPut, "/__control/routes/"+url.PathEscape(id), spec, &rt); code != http.StatusOK {
		t.Fatalf("set route %s: status %d", id, code)
	}
	return rt
}

// DeleteRoute removes a route.
func (i *Instance) DeleteRoute(t testing.TB, id string) {
	t.Helper()
	if code := i.Control(t, http.MethodDelete, "/__control/routes/"+url.PathEscape(id), nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete route %s: status %d", id, code)
	}
}

// Reset clears routes, the default behaviour and recordings.
func (i *Instance) Reset(t testing.TB) {
	t.Helper()
	if code := i.Control(t, http.MethodPost, "/__control/reset", nil, nil); code != http.StatusOK {
		t.Fatalf("reset: status %d", code)
	}
}

// Requests returns recorded requests (all pages), oldest first. query may be nil.
func (i *Instance) Requests(t testing.TB, query url.Values) []RecordedRequest {
	t.Helper()
	var all []RecordedRequest
	q := url.Values{}
	for k, v := range query {
		q[k] = v
	}
	q.Set("limit", "200")
	for {
		var page struct {
			Items      []RecordedRequest `json:"items"`
			NextCursor *string           `json:"next_cursor"`
		}
		if code := i.Control(t, http.MethodGet, "/__control/requests?"+q.Encode(), nil, &page); code != http.StatusOK {
			t.Fatalf("list requests: status %d", code)
		}
		all = append(all, page.Items...)
		if page.NextCursor == nil {
			return all
		}
		q.Set("cursor", *page.NextCursor)
	}
}
