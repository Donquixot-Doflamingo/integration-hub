package hubclient_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/pkg/hubclient"
)

// Example_invoke sends one event and handles each outcome. A 202 arrives as *Queued and must be
// polled; it is never a success.
func Example_invoke() {
	// A stand-in Hub whose wait window has elapsed (in production: hubclient.New(hubURL, key)).
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Hub-Request-Id", "req_01J9Z3K8X2V7Q4M6N8P0R2S4T6")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"request_id":"req_01J9Z3K8X2V7Q4M6N8P0R2S4T6","status":"in_flight",` +
			`"reason":"wait_window_elapsed","poll":"/v1/requests/req_01J9Z3K8X2V7Q4M6N8P0R2S4T6"}`))
	}))
	defer hub.Close()

	client, err := hubclient.New(hub.URL, "hk_test_EXAMPLE_not_a_real_key")
	if err != nil {
		fmt.Println(err)
		return
	}
	body := map[string]any{
		"user":  map[string]any{"id": "u_48213"},
		"event": map[string]any{"name": "order_placed"},
	}
	out, err := client.Invoke(context.Background(), "int_evt_brandA", body,
		hubclient.WithIdempotencyKey("order-8821-created"))
	var hubErr *hubclient.Error
	switch {
	case errors.As(err, &hubErr):
		fmt.Println("hub error:", hubErr.Code, "retryable:", hubErr.Retryable, "after:", hubErr.RetryAfter)
		return
	case err != nil:
		fmt.Println("transport error:", err)
		return
	}

	switch o := out.(type) {
	case *hubclient.Result:
		var resp struct {
			Status string `json:"status"`
		}
		if err := o.Decode(&resp); err != nil {
			fmt.Println(err)
			return
		}
		fmt.Println("done:", resp.Status)
	case *hubclient.Queued:
		fmt.Println("not done yet:", o.Status, o.Reason)
		fmt.Println("poll:", o.Poll)
	}
	// Output:
	// not done yet: in_flight wait_window_elapsed
	// poll: /v1/requests/req_01J9Z3K8X2V7Q4M6N8P0R2S4T6
}
