package sessions

import (
	"errors"
	"strings"
	"testing"
)

// TestCheckDataRefusesAadhaar: DESIGN §14 says the Aadhaar number is never stored. Any key or
// value holding 12 digits (optionally 4-4-4) is refused, wherever it sits in the data.
func TestCheckDataRefusesAadhaar(t *testing.T) {
	refused := []map[string]any{
		{"uid": "234567890123"},
		{"uid": "2345 6789 0123"},
		{"uid": "2345-6789-0123"},
		{"note": "aadhaar 234567890123 verified"},
		{"uid": 234567890123.0},
		{"uid": int64(234567890123)},
		{"nested": map[string]any{"list": []any{"x", "2345 6789 0123"}}},
		{"234567890123": "as a key"},
	}
	for _, d := range refused {
		if err := CheckData(d); !errors.Is(err, ErrSensitiveData) {
			t.Errorf("CheckData(%v) = %v, want ErrSensitiveData", d, err)
		}
	}
	allowed := []map[string]any{
		{"vendor_txn_id": "UKC:2026103109150012345678"}, // a longer digit run is not an Aadhaar number
		{"vendor_txn_id": "vr-1", "masked": "XXXX XXXX 0123"},
		{"ts": 1793438100.0, "otp_len": 6.0},
		{"phone": "+91 98765 43210"},
		{"at": "2026-10-31T09:15:00Z"},
		{},
	}
	for _, d := range allowed {
		if err := CheckData(d); err != nil {
			t.Errorf("CheckData(%v) = %v, want nil", d, err)
		}
	}
}

func TestCreateRefusesBeforeTouchingTheDatabase(t *testing.T) {
	base := Open{TenantID: "t", IntegrationID: "i", Version: 7, AccountID: "a"}
	o := base
	o.Data = map[string]any{"uid": "2345 6789 0123"}
	if _, err := Create(t.Context(), nil, o); !errors.Is(err, ErrSensitiveData) { // nil DB: never reached
		t.Fatalf("err %v", err)
	}
	o.Data = map[string]any{"blob": strings.Repeat("x", MaxDataBytes)}
	if _, err := Create(t.Context(), nil, o); !errors.Is(err, ErrInvalid) {
		t.Fatalf("large: err %v", err)
	}
	o = base
	o.AccountID = ""
	if _, err := Create(t.Context(), nil, o); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing account: err %v", err)
	}
}

func TestGetRejectsMalformedIDsWithoutAQuery(t *testing.T) {
	for _, id := range []string{"", "ses_x", "req_01J9Z6E5F6G7H8J9K0M1N2P3QR", "ses_01J9Z6E5F6G7H8J9K0M1N2P3Q'"} {
		if _, err := Get(t.Context(), nil, "t", "i", id); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get(%q) = %v", id, err)
		}
	}
}
