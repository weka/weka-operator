package capacityplanner

import (
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
)

// RatioFromCaps must store a gcd-reduced proportion (not raw capacity): a TLC-only drive container
// carries {1,0} rather than {13166,0}. See OP-329 bug 3.
func TestRatioFromCaps(t *testing.T) {
	cases := []struct {
		name             string
		tlcGiB, qlcGiB   int
		wantTlc, wantQlc int
	}{
		{"tlc-only", 13166, 0, 1, 0},
		{"qlc-only", 0, 57221, 0, 1},
		{"mixed-reduces", 13166, 57221, 13166, 57221}, // gcd(13166,57221)=1 here → already minimal
		{"mixed-common-factor", 2000, 6000, 1, 3},
		{"both-zero", 0, 0, 0, 0},
		{"equal", 4096, 4096, 1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RatioFromCaps(tc.tlcGiB, tc.qlcGiB)
			if got.Tlc != tc.wantTlc || got.Qlc != tc.wantQlc {
				t.Fatalf("RatioFromCaps(%d,%d) = {%d,%d}, want {%d,%d}",
					tc.tlcGiB, tc.qlcGiB, got.Tlc, got.Qlc, tc.wantTlc, tc.wantQlc)
			}
		})
	}
}

// Reducing the stored ratio must not change the capacity split: GetTlcQlcCapacity is purely
// proportional, so the raw-capacity ratio and its gcd-reduced form yield identical TLC/QLC splits.
func TestRatioFromCapsPreservesSplit(t *testing.T) {
	caps := []struct{ tlc, qlc int }{
		{13166, 0},
		{0, 57221},
		{13166, 57221},
		{2000, 6000},
		{4096, 4096},
	}
	const total = 1_000_000 // arbitrary raw total GiB
	for _, c := range caps {
		rawRatio := &weka.DriveTypesRatio{Tlc: c.tlc, Qlc: c.qlc}
		reduced := RatioFromCaps(c.tlc, c.qlc)

		rawT, rawQ := weka.GetTlcQlcCapacity(total, rawRatio)
		redT, redQ := weka.GetTlcQlcCapacity(total, reduced)
		if rawT != redT || rawQ != redQ {
			t.Fatalf("split changed for caps {%d,%d}: raw={%d,%d} reduced={%d,%d}",
				c.tlc, c.qlc, rawT, rawQ, redT, redQ)
		}
	}
}

func TestGcdInt(t *testing.T) {
	cases := []struct{ a, b, want int }{
		{0, 0, 0},
		{13166, 0, 13166},
		{0, 57221, 57221},
		{2000, 6000, 2000},
		{4096, 4096, 4096},
		{-12, 8, 4},
	}
	for _, tc := range cases {
		if got := gcdInt(tc.a, tc.b); got != tc.want {
			t.Fatalf("gcdInt(%d,%d) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
