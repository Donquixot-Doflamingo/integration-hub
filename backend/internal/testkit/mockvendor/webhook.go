package mockvendor

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultSignatureFormat = "t={ts},v1={hex}"
	webhookTimeout         = 10 * time.Second
	maxTargetBody          = 64 << 10
)

// SignHMAC returns the signature header value for body at unix time ts:
// format with {ts} and {hex} = hex(HMAC-SHA256(secret, ts + "." + body)) substituted.
// Hub inbound-webhook tests verify signatures with the same rule (DESIGN §12.2).
func SignHMAC(secret string, ts int64, body []byte, format string) string {
	if format == "" {
		format = defaultSignatureFormat
	}
	t := strconv.FormatInt(ts, 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(t + "."))
	mac.Write(body)
	return strings.NewReplacer("{ts}", t, "{hex}", hex.EncodeToString(mac.Sum(nil))).Replace(format)
}

func (w *WebhookTrigger) validate() error {
	v := &validationError{}
	u, err := url.Parse(w.TargetURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		v.add("/target_url", "must be an absolute http(s) URL")
	}
	if w.Method != "" && w.Method != http.MethodPost && w.Method != http.MethodPut {
		v.add("/method", "must be POST or PUT")
	}
	if (len(w.Body) > 0) == (w.BodyRaw != nil) {
		v.add("/body", "set exactly one of body or body_raw")
	}
	if w.DelayMS < 0 {
		v.add("/delay_ms", "must be >= 0")
	}
	if sig := w.Signature; sig != nil {
		if sig.Scheme != "hmac_sha256" && sig.Scheme != "basic" {
			v.add("/signature/scheme", "must be hmac_sha256 or basic")
		}
		if sig.Secret == "" || sig.Header == "" {
			v.add("/signature", "secret and header are required")
		}
	}
	return v.err()
}

// triggerWebhook sends an inbound webhook to the Hub and reports its answer (triggerInboundWebhook).
// A transport failure is reported in the result's error field with status 200, as the
// contract describes, so tests can assert on unreachable targets.
func (s *Server) triggerWebhook(w http.ResponseWriter, r *http.Request) {
	var trig WebhookTrigger
	if !decode(w, r, &trig) {
		return
	}
	if err := trig.validate(); err != nil {
		writeValidation(w, err)
		return
	}
	payload := []byte(trig.Body)
	if trig.BodyRaw != nil {
		payload = []byte(*trig.BodyRaw)
	}
	method := trig.Method
	if method == "" {
		method = http.MethodPost
	}
	if trig.DelayMS > 0 && !wait(r.Context(), s.stop, time.Duration(trig.DelayMS)*time.Millisecond) {
		return // caller went away
	}

	req, err := http.NewRequestWithContext(r.Context(), method, trig.TargetURL, bytes.NewReader(payload))
	if err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", err.Error(), nil)
		return
	}
	if trig.BodyRaw == nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range trig.Headers {
		req.Header.Set(k, v)
	}
	now := s.now()
	if sig := trig.Signature; sig != nil {
		switch sig.Scheme {
		case "hmac_sha256":
			ts := now.Add(time.Duration(sig.TimestampOffsetMS) * time.Millisecond).Unix()
			req.Header.Set(sig.Header, SignHMAC(sig.Secret, ts, payload, sig.Format))
		case "basic":
			req.Header.Set(sig.Header, "Basic "+base64.StdEncoding.EncodeToString([]byte(sig.Secret)))
		}
	}

	res := WebhookTriggerResult{SentAt: now.UTC()}
	client := &http.Client{Timeout: webhookTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	start := time.Now()
	resp, err := client.Do(req)
	res.DurationMS = time.Since(start).Milliseconds()
	if err != nil {
		msg := err.Error()
		res.Error = &msg
		writeJSON(w, http.StatusOK, res)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxTargetBody))
	status, text := resp.StatusCode, string(body)
	res.TargetStatus, res.TargetBody, res.TargetHeaders = &status, &text, resp.Header
	writeJSON(w, http.StatusOK, res)
}
