package transform

import (
	"strings"
	"testing"
)

func TestDataFunctions(t *testing.T) {
	runCases(t, []fnCase{
		{src: `nil ?? "d"`, want: "d"},
		{src: `"" ?? "d"`, want: ""},
		{src: `req.missing.deep ?? 0`, want: 0},
		{src: `merge({a: 1, b: 2}, {b: 3}, nil)`, want: map[string]any{"a": 1, "b": 3}},
		{src: `merge({a: 1})`, wantErr: "merge: want (a, b, ...)"},
		{src: `merge({a: 1}, "x")`, wantErr: "merge: want an object"},
		{src: `pick({a: 1, b: 2, c: 3}, "a", "c", "z")`, want: map[string]any{"a": 1, "c": 3}},
		{src: `pick({a: 1, b: 2}, ["b"])`, want: map[string]any{"b": 2}},
		{src: `omit({a: 1, b: 2, c: 3}, "b")`, want: map[string]any{"a": 1, "c": 3}},
		{src: `omit({a: 1}, 7)`, wantErr: "keys must be strings"},
		{src: `flatten({a: {b: 1, c: {d: 2}}, e: 3, f: {}})`, want: map[string]any{"a.b": 1, "a.c.d": 2, "e": 3, "f": map[string]any{}}},
		{src: `flatten([1, [2, 3], [[4]]])`, want: []any{1, 2, 3, []any{4}}},
		{src: `flatten("x")`, wantErr: "flatten: want an object or a list"},
		{src: `map([1, 2, 3], # * 2)`, want: []any{2, 4, 6}},
		{src: `filter([1, 2, 3, 4], # % 2 == 0)`, want: []any{2, 4}},
		{src: `len([1, 2])`, want: 2},
		{src: `len(req.s)`, want: 9},
		{src: `len("")`, want: 0},
		{src: `toJSON({b: 1, a: [true, nil], c: "<&>"})`, want: `{"a":[true,null],"b":1,"c":"<&>"}`},
		{src: `fromJSON("{\"a\": [1, 2]}").a[1]`, want: 2},
		{src: `fromJSON("{nope")`, wantErr: "fromJSON: invalid JSON"},
	})
}

func TestEncodingFunctions(t *testing.T) {
	runCases(t, []fnCase{
		{src: `sha256("")`, want: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{src: `sha256("abc")`, want: "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
		{src: `hmacSHA256("key", "The quick brown fox jumps over the lazy dog")`, want: "f7bc83f430538424b13298e6aa6fb143ef4d59a14946175997479dbc2d1a3cd8"},
		{src: `base64("✓ ok")`, want: "4pyTIG9r"},
		{src: `base64("")`, want: ""},
		{src: `urlEncode("a b&c=d/é")`, want: "a+b%26c%3Dd%2F%C3%A9"},
		{src: `sha256(7)`, wantErr: "sha256: want a string"},
	})
}

func TestUUIDIsDeterministicUnderASeededSource(t *testing.T) {
	n := 0
	seq := func() string {
		n++
		return []string{"00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002"}[n-1]
	}
	got, err := run(t, `[uuid(), uuid()]`, Input{UUID: seq})
	if err != nil || !equalJSON(got, []any{"00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002"}) {
		t.Fatalf("uuid() = %v (%v)", got, err)
	}
	random, err := run(t, `uuid()`, Input{})
	if err != nil || len(random.(string)) != 36 || random.(string)[14] != '4' {
		t.Fatalf("random uuid %v (%v)", random, err)
	}
}

func TestDomainFunctions(t *testing.T) {
	runCases(t, []fnCase{
		{src: `phoneE164("+91 98123-45678")`, want: "+919812345678"},
		{src: `phoneE164("0091 9812345678")`, want: "+919812345678"},
		{src: `phoneE164("098123 45678", "91")`, want: "+919812345678"},
		{src: `phoneE164("(415) 555-0100", "+1")`, want: "+14155550100"},
		{src: `phoneE164("9812345678")`, wantErr: "no country code"},
		{src: `phoneE164("+91 98x")`, wantErr: "invalid character"},
		{src: `phoneE164("+12")`, wantErr: "not a valid E.164 number"},
		{src: `emailNormalize("  Priya.Sharma@Example.COM ")`, want: "priya.sharma@example.com"},
		{src: `emailNormalize("not-an-email")`, wantErr: "not an email address"},
		{src: `currencyMinor("1234.5", "INR")`, want: 123450},
		{src: `currencyMinor(1234.56, "inr")`, want: 123456},
		{src: `currencyMinor(10, "JPY")`, want: 10},
		{src: `currencyMinor("1.234", "KWD")`, want: 1234},
		{src: `currencyMinor("0.10", "USD")`, want: 10},
		{src: `currencyMinor("-2.5", "EUR")`, want: -250},
		{src: `currencyMinor("1.005", "INR")`, wantErr: "INR allows 2 decimals"},
		{src: `currencyMinor("1.5", "JPY")`, wantErr: "JPY allows 0 decimals"},
		{src: `currencyMinor("abc", "INR")`, wantErr: "not a decimal amount"},
		{src: `currencyMinor("1000", "XOF")`, want: 1000},
		{src: `currencyMinor("1000", "XAF")`, want: 1000},
		{src: `currencyMinor(1000, "ISK")`, want: 1000},
		{src: `currencyMinor("5", "UGX")`, want: 5},
		{src: `currencyMinor("5", "XPF")`, want: 5},
		{src: `currencyMinor("1.5", "XOF")`, wantErr: "XOF allows 0 decimals"},
		{src: `currencyMinor("1.2345", "CLF")`, want: 12345},
		{src: `currencyMinor("1.2345", "UYW")`, want: 12345},
		{src: `currencyMinor("1.23", "BHD")`, want: 1230},
		{src: `currencyMinor("1.2345", "BHD")`, wantErr: "BHD allows 3 decimals"},
		{src: `currencyMinor("12.34", "usd")`, want: 1234},
		{src: `currencyMinor("1", "XXX")`, wantErr: "not a known ISO 4217 currency"},
		{src: `currencyMinor("1", "ABC")`, wantErr: "not a known ISO 4217 currency"},
		{src: `currencyMinor("1", "RUPEES")`, wantErr: "not an ISO 4217 code"},
		{src: `maskPAN("ABCDE1234F")`, want: "XXXXXX234F"},
		{src: `maskPAN("4111 1111 1111 1111")`, want: "XXXXXXXXXXXX1111"},
		{src: `maskPAN("AB1")`, want: "XXX"},
		{src: `maskAadhaar("1234 5678 9012")`, want: "XXXXXXXX9012"},
		{src: `maskAadhaar("9012")`, want: "XXXX"},
	})
}

// The masks must never return the full identifier, whatever the input.
func TestMasksNeverReturnTheFullIdentifier(t *testing.T) {
	for _, id := range []string{"ABCDE1234F", "123456789012", "1234", "A", "", "4111111111111111", "  ABCDE1234F "} {
		for _, fn := range []string{"maskPAN", "maskAadhaar"} {
			got, err := run(t, fn+`("`+id+`")`, Input{})
			if err != nil {
				t.Fatal(err)
			}
			clean := strings.TrimSpace(id)
			if clean != "" && got == clean {
				t.Errorf("%s(%q) returned the identifier unmasked", fn, id)
			}
		}
	}
}

// Every ISO 4217 exponent other than 2, and a sample of 2-digit codes, must be known: a missing
// zero-decimal currency would overcharge 100x, so an unknown code is refused, never guessed.
func TestCurrencyMinorExponents(t *testing.T) {
	zero := "BIF CLP DJF GNF ISK JPY KMF KRW PYG RWF UGX UYI VND VUV XAF XOF XPF"
	three := "BHD IQD JOD KWD LYD OMR TND"
	four := "CLF UYW"
	two := "AED INR USD EUR GBP BRL MXN ZAR NGN KES CNY AUD CAD CHF SGD HKD THB IDR PHP PKR BDT LKR NPR SAR QAR EGP TRY"
	for exp, codes := range map[int]string{0: zero, 3: three, 4: four, 2: two} {
		for _, c := range strings.Fields(codes) {
			got, err := run(t, `currencyMinor("1", "`+c+`")`, Input{})
			if err != nil {
				t.Errorf("%s: %v", c, err)
				continue
			}
			want := int64(1)
			for i := 0; i < exp; i++ {
				want *= 10
			}
			if got != want {
				t.Errorf("currencyMinor(1, %s) = %v, want %d", c, got, want)
			}
		}
	}
}
