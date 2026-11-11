package transform

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Strings and time functions of the DESIGN §7.3 standard library. lower, upper, trim, split, join
// and replace are expr-lang builtins.

func argErr(fn string, want string, got []any) error {
	return fmt.Errorf("%s: want %s, got %d argument(s)", fn, want, len(got))
}

func asString(fn string, v any) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s: want a string, got %T", fn, v)
	}
	return s, nil
}

func asInt(fn string, v any) (int64, error) {
	switch n := v.(type) {
	case int:
		return int64(n), nil
	case int64:
		return n, nil
	case float64:
		if n != math.Trunc(n) {
			return 0, fmt.Errorf("%s: want a whole number, got %v", fn, n)
		}
		return int64(n), nil
	}
	return 0, fmt.Errorf("%s: want a number, got %T", fn, v)
}

// format(layout, args...) formats like fmt.Sprintf with two differences that make it fit JSON data
// (HUB-447). JSON numbers are float64, so a whole-number float (3, 1000000) is formatted as an
// integer: %d, %x and %v print 3 and 1000000, %s prints "3"; %f, %e and %g keep the float. And a
// layout that does not fit its arguments (bad verb, wrong type, too few or too many arguments)
// is an error, never the "%!d(float64=3)" text Sprintf would put in the vendor request.
func fnFormat(params ...any) (any, error) {
	if len(params) < 1 {
		return nil, argErr("format", "(layout, args...)", params)
	}
	layout, err := asString("format", params[0])
	if err != nil {
		return nil, err
	}
	args := params[1:]
	verbs, scanned := formatVerbs(layout)
	candidates := [][]any{wholeFloatsToInts(args, verbs, scanned)}
	if !scanned {
		candidates = append(candidates, args) // *, [n]: the verb of an argument is unknown, try both
	}
	for _, c := range candidates {
		out := fmt.Sprintf(layout, c...)
		if !strings.Contains(out, "%!") || !formatFailed(layout, c) {
			return out, nil
		}
	}
	return nil, fmt.Errorf("format: layout %q does not fit its arguments (bad verb, wrong type, or wrong argument count)", layout)
}

// formatFailed re-runs the layout with every string argument replaced by a plain word: a "%!" that
// is still there comes from the layout, not from data that happens to contain "%!".
func formatFailed(layout string, args []any) bool {
	plain := make([]any, len(args))
	for i, a := range args {
		if _, ok := a.(string); ok {
			a = "x"
		}
		plain[i] = a
	}
	return strings.Contains(fmt.Sprintf(layout, plain...), "%!")
}

// formatVerbs returns the verb of each argument in order. ok is false when the layout uses *
// widths or [n] indexes, where arguments no longer map one to one onto verbs.
func formatVerbs(layout string) (verbs []byte, ok bool) {
	for i := 0; i < len(layout); i++ {
		if layout[i] != '%' {
			continue
		}
		i++
		for i < len(layout) && strings.IndexByte("+-# 0123456789.", layout[i]) >= 0 {
			i++
		}
		if i >= len(layout) {
			break
		}
		switch layout[i] {
		case '*', '[':
			return nil, false
		case '%':
		default:
			verbs = append(verbs, layout[i])
		}
	}
	return verbs, true
}

// wholeFloatsToInts converts finite whole-number float64 arguments inside the int64 range to
// integers: to int64 for integer verbs (and for %v and unknown verbs), to a decimal string for %s.
func wholeFloatsToInts(args []any, verbs []byte, scanned bool) []any {
	out := make([]any, len(args))
	for i, a := range args {
		out[i] = a
		f, isFloat := a.(float64)
		if !isFloat || f != math.Trunc(f) || f < -9.223372036854775808e18 || f >= 9.223372036854775808e18 {
			continue
		}
		verb := byte('v')
		if scanned {
			if i >= len(verbs) {
				continue
			}
			verb = verbs[i]
		}
		switch verb {
		case 's':
			out[i] = strconv.FormatInt(int64(f), 10)
		case 'd', 'v', 'x', 'X', 'o', 'O', 'b', 'c', 'U', 'q':
			out[i] = int64(f)
		}
	}
	return out
}

// truncate(s, n[, suffix]) keeps at most n characters (runes), the suffix included.
func fnTruncate(params ...any) (any, error) {
	if len(params) != 2 && len(params) != 3 {
		return nil, argErr("truncate", "(s, n[, suffix])", params)
	}
	s, err := asString("truncate", params[0])
	if err != nil {
		return nil, err
	}
	n, err := asInt("truncate", params[1])
	if err != nil || n < 0 {
		return nil, errors.New("truncate: n must be a number >= 0")
	}
	suffix := ""
	if len(params) == 3 {
		if suffix, err = asString("truncate", params[2]); err != nil {
			return nil, err
		}
	}
	if int64(utf8.RuneCountInString(s)) <= n {
		return s, nil
	}
	keep := n - int64(utf8.RuneCountInString(suffix))
	if keep < 0 {
		keep, suffix = n, ""
	}
	r := []rune(s)
	return string(r[:keep]) + suffix, nil
}

// toTime accepts a time.Time, an RFC 3339 string or epoch seconds.
func toTime(fn string, v any) (time.Time, error) {
	switch t := v.(type) {
	case time.Time:
		return t, nil
	case string:
		p, err := time.Parse(time.RFC3339Nano, t)
		if err != nil {
			return time.Time{}, fmt.Errorf("%s: %q is not an RFC 3339 time", fn, t)
		}
		return p, nil
	case int, int64, float64:
		secs, err := asInt(fn, t)
		if err != nil {
			return time.Time{}, err
		}
		return time.Unix(secs, 0).UTC(), nil
	}
	return time.Time{}, fmt.Errorf("%s: want a time, RFC 3339 string or epoch seconds, got %T", fn, v)
}

func fnEpochSeconds(params ...any) (any, error) {
	if len(params) != 1 {
		return nil, argErr("epochSeconds", "(time)", params)
	}
	t, err := toTime("epochSeconds", params[0])
	if err != nil {
		return nil, err
	}
	return t.Unix(), nil
}

func fnEpochMillis(params ...any) (any, error) {
	if len(params) != 1 {
		return nil, argErr("epochMillis", "(time)", params)
	}
	t, err := toTime("epochMillis", params[0])
	if err != nil {
		return nil, err
	}
	return t.UnixMilli(), nil
}

// layouts maps names to Go layouts; any other value is used as a Go layout itself.
var layouts = map[string]string{
	"RFC3339": time.RFC3339, "RFC3339Nano": time.RFC3339Nano, "DateOnly": time.DateOnly,
	"DateTime": time.DateTime, "TimeOnly": time.TimeOnly, "RFC1123": time.RFC1123,
}

func layoutOf(name string) string {
	if l, ok := layouts[name]; ok {
		return l
	}
	return name
}

// parseTime(s[, layout]) parses s (RFC 3339 by default).
func fnParseTime(params ...any) (any, error) {
	if len(params) != 1 && len(params) != 2 {
		return nil, argErr("parseTime", "(s[, layout])", params)
	}
	s, err := asString("parseTime", params[0])
	if err != nil {
		return nil, err
	}
	layout := time.RFC3339Nano
	if len(params) == 2 {
		name, err := asString("parseTime", params[1])
		if err != nil {
			return nil, err
		}
		layout = layoutOf(name)
	}
	t, err := time.Parse(layout, s)
	if err != nil {
		return nil, fmt.Errorf("parseTime: %q does not match layout %q", s, layout)
	}
	return t, nil
}

var offsetPattern = regexp.MustCompile(`^([+-])(\d{2}):(\d{2})$`)

// zone accepts "UTC" or a fixed offset "+05:30". IANA names are not supported: loading them reads
// the system zoneinfo from disk, and transformers are pure (DESIGN §7.1).
func zone(name string) (*time.Location, error) {
	if name == "UTC" || name == "Z" {
		return time.UTC, nil
	}
	m := offsetPattern.FindStringSubmatch(name)
	if m == nil {
		return nil, fmt.Errorf(`formatTime: zone %q must be "UTC" or an offset like "+05:30"`, name)
	}
	h, _ := strconv.Atoi(m[2])
	mi, _ := strconv.Atoi(m[3])
	secs := h*3600 + mi*60
	if m[1] == "-" {
		secs = -secs
	}
	return time.FixedZone(name, secs), nil
}

// formatTime(t, layout[, zone]) formats t (default zone UTC).
func fnFormatTime(params ...any) (any, error) {
	if len(params) != 2 && len(params) != 3 {
		return nil, argErr("formatTime", "(time, layout[, zone])", params)
	}
	t, err := toTime("formatTime", params[0])
	if err != nil {
		return nil, err
	}
	name, err := asString("formatTime", params[1])
	if err != nil {
		return nil, err
	}
	loc := time.UTC
	if len(params) == 3 {
		zname, err := asString("formatTime", params[2])
		if err != nil {
			return nil, err
		}
		if loc, err = zone(zname); err != nil {
			return nil, err
		}
	}
	return t.In(loc).Format(layoutOf(name)), nil
}

// addDuration(t, d) adds a Go duration ("90m", "-1h30m") or whole days ("2d").
func fnAddDuration(params ...any) (any, error) {
	if len(params) != 2 {
		return nil, argErr("addDuration", "(time, duration)", params)
	}
	t, err := toTime("addDuration", params[0])
	if err != nil {
		return nil, err
	}
	ds, err := asString("addDuration", params[1])
	if err != nil {
		return nil, err
	}
	if days, ok := strings.CutSuffix(ds, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil {
			return nil, fmt.Errorf("addDuration: invalid duration %q", ds)
		}
		return t.AddDate(0, 0, n), nil
	}
	d, err := time.ParseDuration(ds)
	if err != nil {
		return nil, fmt.Errorf("addDuration: invalid duration %q", ds)
	}
	return t.Add(d), nil
}
