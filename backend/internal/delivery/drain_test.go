package delivery

import (
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
