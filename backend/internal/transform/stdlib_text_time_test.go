package transform

import (
	"testing"
	"time"
)

func TestStringFunctions(t *testing.T) {
	runCases(t, []fnCase{
		{src: `lower("ÀBC")`, want: "àbc"},
		{src: `upper(req.s)`, want: "ÜNÏCØDÉ ✓"},
		{src: `upper("")`, want: ""},
		{src: `trim("  a b  ")`, want: "a b"},
		{src: `trim("")`, want: ""},
		{src: `split("a,b,,c", ",")`, want: []any{"a", "b", "", "c"}},
		{src: `split("", ",")`, want: []any{""}},
		{src: `join(["a", "b"], "-")`, want: "a-b"},
		{src: `join([], "-")`, want: ""},
		{src: `replace("a-b-c", "-", "+")`, want: "a+b+c"},
		{src: `format("%s/%d", "x", 7)`, want: "x/7"},
		{src: `format("no args")`, want: "no args"},
		{src: `format("%d", req.n)`, want: "3"},
		{src: `format("%s", req.n)`, want: "3"},
		{src: `format("%v", req.n)`, want: "3"},
		{src: `format("%v", 1000000.0)`, want: "1000000"},
		{src: `format("%05d|%x", req.n, 255.0)`, want: "00003|ff"},
		{src: `format("%.2f", req.n)`, want: "3.00"},
		{src: `format("%d items at %.1f", req.n, 2.5)`, want: "3 items at 2.5"},
		{src: `format("%d %.2f", 3.0, 4.0)`, want: "3 4.00"},
		{src: `format("%v", 2.5)`, want: "2.5"},
		{src: `format("%d%%", req.n)`, want: "3%"},
		{src: `format("%[1]d-%[1]d", req.n)`, want: "3-3"},
		{src: `format("%s", "100%!")`, want: "100%!"},
		{src: `format("%d", 3.5)`, wantErr: "format: layout \"%d\" does not fit its arguments"},
		{src: `format("%d", "x")`, wantErr: "does not fit its arguments"},
		{src: `format("%s", 3.5)`, wantErr: "does not fit its arguments"},
		{src: `format("%z", 1)`, wantErr: "does not fit its arguments"},
		{src: `format("%d %d", 1)`, wantErr: "does not fit its arguments"},
		{src: `format("%d", 1, 2)`, wantErr: "does not fit its arguments"},
		{src: `format("%d", 1e30)`, wantErr: "does not fit its arguments"},
		{src: `format(7)`, wantErr: "format: want a string"},
		{src: `truncate(req.s, 4)`, want: "Ünïc"},
		{src: `truncate(req.s, 5, "…")`, want: "Ünïc…"},
		{src: `truncate("short", 10)`, want: "short"},
		{src: `truncate("", 0)`, want: ""},
		{src: `truncate("abc", 1, "...")`, want: "a"},
		{src: `truncate("abc", -1)`, wantErr: "n must be a number >= 0"},
	})
}

func TestTimeFunctions(t *testing.T) {
	runCases(t, []fnCase{
		{src: `epochSeconds(now())`, want: fixedNow.Unix()},
		{src: `epochMillis(now())`, want: fixedNow.UnixMilli()},
		{src: `epochSeconds("2026-10-31T09:15:00Z")`, want: int64(1793438100)},
		{src: `epochSeconds("2026-10-31T14:45:00+05:30")`, want: int64(1793438100)},
		{src: `epochSeconds(nil ?? now())`, want: fixedNow.Unix()},
		{src: `epochSeconds("31/10/2026")`, wantErr: `epochSeconds: "31/10/2026" is not an RFC 3339 time`},
		{src: `epochSeconds(true)`, wantErr: "want a time"},
		{src: `formatTime(now(), "RFC3339")`, want: "2026-10-31T09:15:00Z"},
		{src: `formatTime(now(), "DateOnly", "+05:30")`, want: "2026-10-31"},
		{src: `formatTime(now(), "15:04", "+05:30")`, want: "14:45"},
		{src: `formatTime(now(), "15:04", "Asia/Kolkata")`, wantErr: `zone "Asia/Kolkata" must be "UTC" or an offset`},
		{src: `formatTime(parseTime("31/10/2026", "02/01/2006"), "RFC3339")`, want: "2026-10-31T00:00:00Z"},
		{src: `formatTime(parseTime("2026-10-31"), "RFC3339")`, wantErr: "does not match layout"},
		{src: `formatTime(addDuration(now(), "90m"), "RFC3339")`, want: "2026-10-31T10:45:00Z"},
		{src: `formatTime(addDuration(now(), "2d"), "DateOnly")`, want: "2026-11-02"},
		{src: `formatTime(addDuration(now(), "-1h"), "TimeOnly")`, want: "08:15:00"},
		{src: `addDuration(now(), "soon")`, wantErr: `addDuration: invalid duration "soon"`},
	})
}

func TestNowIsInjectable(t *testing.T) {
	pinned := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	got, err := run(t, `epochSeconds(now())`, Input{Now: pinned})
	if err != nil || got != pinned.Unix() {
		t.Fatalf("now() = %v (%v), want the injected clock", got, err)
	}
}
