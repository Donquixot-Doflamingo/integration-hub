package delivery

import (
	"math"
	"testing"
	"time"
)

func TestRampFactor(t *testing.T) {
	step := 30 * time.Second
	for _, tc := range []struct {
		since time.Duration
		want  float64
	}{
		{0, 0.2}, {29 * time.Second, 0.2}, {30 * time.Second, 0.4}, {60 * time.Second, 0.8}, {90 * time.Second, 1}, {time.Hour, 1}, {-time.Second, 0.2},
	} {
		if got := RampFactor(tc.since, step); got != tc.want {
			t.Errorf("RampFactor(%s) = %v, want %v", tc.since, got, tc.want)
		}
	}
}

// TestDrainQuotaUsesElapsedTime (HUB-155): the quota follows the time that really passed, so a
// late or dropped scheduler tick does not lower the drain rate, and a long stall is made up by at
// most one second of the rate (maxCatchUp) so the vendor never sees more than its account rate.
func TestDrainQuotaUsesElapsedTime(t *testing.T) {
	tick := 100 * time.Millisecond
	for _, tc := range []struct {
		name      string
		carry     float64
		rate      float64
		elapsed   time.Duration
		want      int
		wantCarry float64
	}{
		{"on-time tick", 0, 50, tick, 5, 0},
		{"late tick makes up the time that passed", 0, 50, 2 * tick, 10, 0},
		{"stall of several ticks is made up (scheduler pass slower than 2 ticks)", 0, 50, 5 * tick, 25, 0},
		{"long stall is capped at one second of the rate", 0, 50, 30 * tick, 50, 0},
		{"fraction is carried to the next tick", 0.5, 10, tick, 1, 0.5},
		{"clock step backwards releases nothing new", 0.25, 50, -tick, 0, 0.25},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, carry := drainQuota(tc.carry, tc.rate, tc.elapsed, tick)
			if n != tc.want || math.Abs(carry-tc.wantCarry) > 1e-9 {
				t.Fatalf("drainQuota = %d, %v; want %d, %v", n, carry, tc.want, tc.wantCarry)
			}
		})
	}
	// Over one second of ticks that each arrive 50 % late, the released total still matches the
	// rate (the old nominal-tick quota released only 2/3 of it).
	total, carry := 0, 0.0
	for elapsed := time.Duration(0); elapsed < time.Second; elapsed += 150 * time.Millisecond {
		var n int
		n, carry = drainQuota(carry, 50, 150*time.Millisecond, tick)
		total += n
	}
	// 7 ticks of 150 ms cover 1.05 s: 52 at 50/s. The nominal-tick quota gave 7 × 5 = 35.
	if total != 52 {
		t.Fatalf("released %d over 1.05 s at 50/s with late ticks, want 52", total)
	}
}

func TestMaxCatchUp(t *testing.T) {
	for _, tc := range []struct{ tick, want time.Duration }{
		{100 * time.Millisecond, time.Second}, {time.Second, 2 * time.Second},
	} {
		if got := maxCatchUp(tc.tick); got != tc.want {
			t.Errorf("maxCatchUp(%s) = %s, want %s", tc.tick, got, tc.want)
		}
	}
}
