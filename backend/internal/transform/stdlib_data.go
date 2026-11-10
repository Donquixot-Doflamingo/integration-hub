package transform

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// Data, encoding/identity and domain functions of the DESIGN §7.3 standard library. ??, map,
// filter and len are expr-lang operators/builtins; uuid() comes from the evaluation input.

func asMap(fn string, v any) (map[string]any, error) {
	switch m := v.(type) {
	case map[string]any:
		return m, nil
	case nil:
		return map[string]any{}, nil
	}
	return nil, fmt.Errorf("%s: want an object, got %T", fn, v)
}

// keysOf accepts keys as varargs or as one list.
func keysOf(fn string, params []any) ([]string, error) {
	if len(params) == 1 {
		if list, ok := params[0].([]any); ok {
			params = list
		}
	}
	keys := make([]string, 0, len(params))
	for _, p := range params {
		k, ok := p.(string)
		if !ok {
			return nil, fmt.Errorf("%s: keys must be strings, got %T", fn, p)
		}
		keys = append(keys, k)
	}
	return keys, nil
}

// merge(a, b, ...) returns a new object; later objects win. nil arguments are skipped.
func fnMerge(params ...any) (any, error) {
	if len(params) < 2 {
		return nil, argErr("merge", "(a, b, ...)", params)
	}
	out := map[string]any{}
	for _, p := range params {
		m, err := asMap("merge", p)
		if err != nil {
			return nil, err
		}
		for k, v := range m {
			out[k] = v
		}
	}
	return out, nil
}

// pick(obj, keys...) keeps only the given keys.
func fnPick(params ...any) (any, error) {
	if len(params) < 2 {
		return nil, argErr("pick", "(obj, keys...)", params)
	}
	m, err := asMap("pick", params[0])
	if err != nil {
		return nil, err
	}
	keys, err := keysOf("pick", params[1:])
	if err != nil {
		return nil, err
	}
	out := make(map[string]any, len(keys))
	for _, k := range keys {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

// omit(obj, keys...) drops the given keys.
func fnOmit(params ...any) (any, error) {
	if len(params) < 2 {
		return nil, argErr("omit", "(obj, keys...)", params)
	}
	m, err := asMap("omit", params[0])
	if err != nil {
		return nil, err
	}
	keys, err := keysOf("omit", params[1:])
	if err != nil {
		return nil, err
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if !slices.Contains(keys, k) {
			out[k] = v
		}
	}
	return out, nil
}

// flatten(obj) turns nested objects into dotted keys ({"a": {"b": 1}} → {"a.b": 1});
// flatten(list) flattens nested lists one level.
func fnFlatten(params ...any) (any, error) {
	if len(params) != 1 {
		return nil, argErr("flatten", "(obj | list)", params)
	}
	switch x := params[0].(type) {
	case map[string]any:
		out := map[string]any{}
		flattenInto(out, "", x)
		return out, nil
	case []any:
		var out []any
		for _, e := range x {
			if inner, ok := e.([]any); ok {
				out = append(out, inner...)
				continue
			}
			out = append(out, e)
		}
		return out, nil
	}
	return nil, fmt.Errorf("flatten: want an object or a list, got %T", params[0])
}

func flattenInto(out map[string]any, prefix string, m map[string]any) {
	for k, v := range m {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		if inner, ok := v.(map[string]any); ok && len(inner) > 0 {
			flattenInto(out, key, inner)
			continue
		}
		out[key] = v
	}
}

// toJSON(v) encodes compactly with sorted keys (deterministic for signatures and goldens).
func fnToJSON(params ...any) (any, error) {
	if len(params) != 1 {
		return nil, argErr("toJSON", "(value)", params)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(params[0]); err != nil {
		return nil, fmt.Errorf("toJSON: %w", err)
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// fromJSON(s) decodes JSON text.
func fnFromJSON(params ...any) (any, error) {
	if len(params) != 1 {
		return nil, argErr("fromJSON", "(text)", params)
	}
	s, err := asString("fromJSON", params[0])
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil, fmt.Errorf("fromJSON: invalid JSON: %w", err)
	}
	return v, nil
}

func fnSHA256(params ...any) (any, error) {
	if len(params) != 1 {
		return nil, argErr("sha256", "(text)", params)
	}
	s, err := asString("sha256", params[0])
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:]), nil
}

// hmacSHA256(key, message) returns the hex HMAC-SHA256 (vendor request signing).
func fnHMACSHA256(params ...any) (any, error) {
	if len(params) != 2 {
		return nil, argErr("hmacSHA256", "(key, message)", params)
	}
	key, err := asString("hmacSHA256", params[0])
	if err != nil {
		return nil, err
	}
	msg, err := asString("hmacSHA256", params[1])
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(msg))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func fnBase64(params ...any) (any, error) {
	if len(params) != 1 {
		return nil, argErr("base64", "(text)", params)
	}
	s, err := asString("base64", params[0])
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.EncodeToString([]byte(s)), nil
}

func fnURLEncode(params ...any) (any, error) {
	if len(params) != 1 {
		return nil, argErr("urlEncode", "(text)", params)
	}
	s, err := asString("urlEncode", params[0])
	if err != nil {
		return nil, err
	}
	return url.QueryEscape(s), nil
}

// phoneE164(phone[, countryCode]) normalises to E.164 ("+919812345678"). Without a leading + or 00
// the country code (digits, e.g. "91") is prefixed. 8–15 digits are required.
func fnPhoneE164(params ...any) (any, error) {
	if len(params) != 1 && len(params) != 2 {
		return nil, argErr("phoneE164", "(phone[, countryCode])", params)
	}
	s, err := asString("phoneE164", params[0])
	if err != nil {
		return nil, err
	}
	s = strings.TrimSpace(s)
	international := strings.HasPrefix(s, "+") || strings.HasPrefix(s, "00")
	s = strings.TrimPrefix(strings.TrimPrefix(s, "+"), "00")
	var digits strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits.WriteRune(r)
		case r == ' ' || r == '-' || r == '(' || r == ')' || r == '.':
		default:
			return nil, fmt.Errorf("phoneE164: invalid character %q", r)
		}
	}
	d := digits.String()
	if !international {
		if len(params) != 2 {
			return nil, errors.New("phoneE164: number has no country code and none was given")
		}
		cc, err := asString("phoneE164", params[1])
		if err != nil {
			return nil, err
		}
		d = strings.TrimPrefix(cc, "+") + strings.TrimLeft(d, "0")
	}
	if len(d) < 8 || len(d) > 15 || d[0] == '0' {
		return nil, fmt.Errorf("phoneE164: %d digits is not a valid E.164 number", len(d))
	}
	return "+" + d, nil
}

// emailNormalize trims and lower-cases an address.
func fnEmailNormalize(params ...any) (any, error) {
	if len(params) != 1 {
		return nil, argErr("emailNormalize", "(email)", params)
	}
	s, err := asString("emailNormalize", params[0])
	if err != nil {
		return nil, err
	}
	s = strings.ToLower(strings.TrimSpace(s))
	if at := strings.Index(s, "@"); at <= 0 || at == len(s)-1 || strings.Count(s, "@") != 1 {
		return nil, errors.New("emailNormalize: not an email address")
	}
	return s, nil
}

// minorDigits is the ISO 4217 minor-unit exponent of every active currency, grouped by exponent.
// An unknown code is refused rather than guessed as 2: a missing zero-decimal currency would
// overcharge 100x. Add a code here when ISO publishes one.
var minorDigits = buildMinorDigits(map[int]string{
	0: "BIF CLP DJF GNF ISK JPY KMF KRW PYG RWF UGX UYI VND VUV XAF XOF XPF",
	2: "AED AFN ALL AMD ANG AOA ARS AUD AWG AZN BAM BBD BDT BGN BMD BND BOB BOV BRL BSD BTN BWP BYN BZD " +
		"CAD CDF CHE CHF CHW CNY COP COU CRC CUP CVE CZK DKK DOP DZD EGP ERN ETB EUR FJD FKP GBP GEL GHS GIP GMD " +
		"GTQ GYD HKD HNL HTG HUF IDR ILS INR IRR JMD KES KGS KHR KPW KYD KZT LAK LBP LKR LRD LSL MAD MDL MGA MKD " +
		"MMK MNT MOP MRU MUR MVR MWK MXN MXV MYR MZN NAD NGN NIO NOK NPR NZD PAB PEN PGK PHP PKR PLN QAR RON RSD " +
		"RUB SAR SBD SCR SDG SEK SGD SHP SLE SOS SRD SSP STN SVC SYP SZL THB TJS TMT TOP TRY TTD TWD TZS UAH USD " +
		"USN UYU UZS VED VES WST XCD YER ZAR ZMW ZWG",
	3: "BHD IQD JOD KWD LYD OMR TND",
	4: "CLF UYW",
})

func buildMinorDigits(groups map[int]string) map[string]int {
	m := make(map[string]int, 180)
	for exp, codes := range groups {
		for _, c := range strings.Fields(codes) {
			m[c] = exp
		}
	}
	return m
}

var decimalPattern = regexp.MustCompile(`^-?\d+(\.\d+)?$`)

// currencyMinor(amount, currency) converts an amount ("1234.5" or 1234.5) to integer minor units
// (123450 for INR). Amounts with more decimals than the currency allows are rejected, never rounded;
// a currency missing from the ISO 4217 table is rejected, never assumed to have 2 digits.
func fnCurrencyMinor(params ...any) (any, error) {
	if len(params) != 2 {
		return nil, argErr("currencyMinor", "(amount, currency)", params)
	}
	var s string
	switch a := params[0].(type) {
	case string:
		s = strings.TrimSpace(a)
	case int:
		s = strconv.Itoa(a)
	case int64:
		s = strconv.FormatInt(a, 10)
	case float64:
		s = strconv.FormatFloat(a, 'f', -1, 64)
	default:
		return nil, fmt.Errorf("currencyMinor: want a number or decimal string, got %T", a)
	}
	cur, err := asString("currencyMinor", params[1])
	if err != nil {
		return nil, err
	}
	cur = strings.ToUpper(cur)
	if len(cur) != 3 || strings.IndexFunc(cur, func(r rune) bool { return !unicode.IsUpper(r) }) >= 0 {
		return nil, fmt.Errorf("currencyMinor: %q is not an ISO 4217 code", cur)
	}
	if !decimalPattern.MatchString(s) {
		return nil, fmt.Errorf("currencyMinor: %q is not a decimal amount", s)
	}
	exp, ok := minorDigits[cur]
	if !ok {
		return nil, fmt.Errorf("currencyMinor: %q is not a known ISO 4217 currency (its minor unit is not defined)", cur)
	}
	whole, frac, _ := strings.Cut(s, ".")
	frac = strings.TrimRight(frac, "0")
	if len(frac) > exp {
		return nil, fmt.Errorf("currencyMinor: %s allows %d decimals, got %q", cur, exp, s)
	}
	n, err := strconv.ParseInt(whole+frac+strings.Repeat("0", exp-len(frac)), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("currencyMinor: %q is out of range", s)
	}
	return n, nil
}

// maskTail keeps the last keep characters and replaces the rest with X. Short values are masked
// entirely, so the full identifier is never returned.
func maskTail(s string, keep int) string {
	r := []rune(s)
	if len(r) <= keep {
		return strings.Repeat("X", len(r))
	}
	return strings.Repeat("X", len(r)-keep) + string(r[len(r)-keep:])
}

// maskPAN masks an Indian PAN (ABCDE1234F) or a card number to its last 4 characters.
func fnMaskPAN(params ...any) (any, error) {
	if len(params) != 1 {
		return nil, argErr("maskPAN", "(pan)", params)
	}
	s, err := asString("maskPAN", params[0])
	if err != nil {
		return nil, err
	}
	s = strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(s), " ", ""), "-", "")
	return maskTail(s, 4), nil
}

// maskAadhaar masks a 12-digit Aadhaar number to its last 4 digits.
func fnMaskAadhaar(params ...any) (any, error) {
	if len(params) != 1 {
		return nil, argErr("maskAadhaar", "(aadhaar)", params)
	}
	s, err := asString("maskAadhaar", params[0])
	if err != nil {
		return nil, err
	}
	s = strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(s), " ", ""), "-", "")
	return maskTail(s, 4), nil
}
