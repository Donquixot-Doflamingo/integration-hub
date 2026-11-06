// Command client is the Go version of the Aadhaar OTP e-KYC demo (HUB-351): the two calls an
// onboarding backend makes, with the Hub's Go SDK (backend/pkg/hubclient). It talks to a Hub that
// examples/aadhaar-okyc/run.sh serve started, against mockvendor or the provider's sandbox.
//
//	examples/aadhaar-okyc/run.sh serve                          # terminal 1
//	cd backend && go run ../examples/aadhaar-okyc/client/main.go # terminal 2
//
// Environment: HUB_URL (default http://127.0.0.1:18080), HUB_API_KEY (default the example
// tenant's local test key), AADHAAR (default the sandbox test number), OTP (default: asked).
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/pkg/hubclient"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	if err := run(context.Background()); err != nil {
		var he *hubclient.Error
		if errors.As(err, &he) {
			// What the app shows depends only on canonical fields: code, message, retryable.
			fmt.Fprintf(os.Stderr, "%d %s: %s (retryable %v, retry after %s, request %s)\n",
				he.Status, he.Code, he.Message, he.Retryable, he.RetryAfter, he.RequestID)
		} else {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	hub, err := hubclient.New(env("HUB_URL", "http://127.0.0.1:18080"), env("HUB_API_KEY", "hk_test_example_kyc_onboarding_local_only"))
	if err != nil {
		return err
	}
	attempt := fmt.Sprintf("onboarding-%d", time.Now().UnixNano()) // one per user attempt, reused on retries

	// Step 1: send the OTP. A double tap reuses the same Idempotency-Key: one SMS, same session.
	out, err := hub.Invoke(ctx, "int_aadhaar_okyc", map[string]any{
		"aadhaar_number": env("AADHAAR", "655675523712"),
		"consent":        true,
		"purpose":        "KYC for a loan application (demo)",
	}, hubclient.WithOperation("send_otp"), hubclient.WithIdempotencyKey(attempt+"-send"))
	if err != nil {
		return err
	}
	sent, ok := out.(*hubclient.Result)
	if !ok {
		return errors.New("unexpected queued answer: an interactive operation is always sync")
	}
	fmt.Printf("step 1: %s (request %s, session %s)\n", strings.TrimSpace(string(sent.Body)), sent.RequestID, sent.Session)
	// Keep sent.Session with the user's onboarding attempt; it lives 10 minutes.

	// The app shows the OTP screen; the person reads the SMS on the Aadhaar-linked phone.
	otp := os.Getenv("OTP")
	if otp == "" {
		fmt.Print("OTP (mockvendor and the sandbox accept 111000): ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		otp = strings.TrimSpace(line)
	}

	// Step 2: verify the OTP inside the session. A new key per attempt at typing the OTP.
	out, err = hub.Invoke(ctx, "int_aadhaar_okyc", map[string]any{"otp": otp},
		hubclient.WithOperation("verify_otp"), hubclient.WithSession(sent.Session),
		hubclient.WithIdempotencyKey(attempt+"-verify-1"))
	if err != nil {
		return err
	}
	var kyc struct {
		Status      string  `json:"status"`
		Name        *string `json:"name"`
		DateOfBirth *string `json:"date_of_birth"`
		Gender      *string `json:"gender"`
		Address     *struct {
			State   *string `json:"state"`
			Pincode *string `json:"pincode"`
		} `json:"address"`
	}
	if err := out.(*hubclient.Result).Decode(&kyc); err != nil {
		return err
	}
	fmt.Printf("step 2: %s %s, born %s\n", kyc.Status, deref(kyc.Name), deref(kyc.DateOfBirth))
	return nil
}

func deref(s *string) string {
	if s == nil {
		return "-"
	}
	return *s
}
