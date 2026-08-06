package services

import "testing"

func TestMeetsThreshold(t *testing.T) {
	cases := []struct {
		name                      string
		active, total, thresholdP int
		want                      bool
	}{
		{"nothing to threshold against", 0, 0, 80, true},
		{"well above", 90, 100, 80, true},
		{"well below", 10, 100, 80, false},
		{"one short of the line", 79, 100, 80, false},
		{"zero percent always passes", 0, 100, 0, true},
		{"all active at 100 percent", 100, 100, 100, true},
		{"one short at 100 percent", 99, 100, 100, false},

		// Exactly on the line must PASS. Computing the threshold as total*(pct/100) instead of
		// (total*pct)/100 makes these fail: pct/100 is not exactly representable in binary, so the
		// product lands a hair above the integer and the >= comparison flips.
		{"exactly on the line, 55%", 55, 100, 55, true},
		{"exactly on the line, 7%", 7, 100, 7, true},
		{"exactly on the line, 28% of 25", 7, 25, 28, true},
		{"exactly on the line, 56% of 150", 84, 150, 56, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MeetsThreshold(tc.active, tc.total, tc.thresholdP); got != tc.want {
				t.Errorf("MeetsThreshold(%d, %d, %d) = %v, want %v",
					tc.active, tc.total, tc.thresholdP, got, tc.want)
			}
		})
	}
}
