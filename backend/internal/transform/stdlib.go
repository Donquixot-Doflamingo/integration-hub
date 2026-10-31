package transform

import "github.com/expr-lang/expr"

// stdlib is the DESIGN §7.3 standard library beyond the allowed expr builtins. It is extended only
// by platform code releases, never per transformer. Every function is pure.
var stdlib = map[string]func(params ...any) (any, error){
	// strings (plus the lower, upper, trim, split, join, replace builtins)
	"format":   fnFormat,
	"truncate": fnTruncate,
	// time (now() comes from the evaluation input so goldens are deterministic)
	"epochSeconds": fnEpochSeconds,
	"epochMillis":  fnEpochMillis,
	"parseTime":    fnParseTime,
	"formatTime":   fnFormatTime,
	"addDuration":  fnAddDuration,
	// data (plus the ?? operator and the map, filter, len builtins)
	"merge":    fnMerge,
	"pick":     fnPick,
	"omit":     fnOmit,
	"flatten":  fnFlatten,
	"toJSON":   fnToJSON,
	"fromJSON": fnFromJSON,
	// encoding / identity (uuid() comes from the evaluation input)
	"sha256":     fnSHA256,
	"hmacSHA256": fnHMACSHA256,
	"base64":     fnBase64,
	"urlEncode":  fnURLEncode,
	// domain
	"phoneE164":      fnPhoneE164,
	"emailNormalize": fnEmailNormalize,
	"currencyMinor":  fnCurrencyMinor,
	"maskPAN":        fnMaskPAN,
	"maskAadhaar":    fnMaskAadhaar,
	// response mapping
	"header": fnHeader,
}

func stdlibOptions() []expr.Option {
	opts := make([]expr.Option, 0, len(stdlib))
	for name, fn := range stdlib {
		opts = append(opts, expr.Function(name, fn))
	}
	return opts
}
