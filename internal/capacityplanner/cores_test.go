package capacityplanner

import "testing"

// The split must add up to the cores the container actually has. An over-count reaches totalTlcDriveCores
// and from there both terms of RequiredComputeCores, over-stating a mixed-pool cluster's compute.
func TestDriveCoresForContainer_SplitSumsToAssignedCores(t *testing.T) {
	cons := testCons() // TlcCapacityPerCoreGiB=5120, QlcCapacityPerCoreGiB=51200

	tests := []struct {
		name                     string
		tlcGiB, qlcGiB, assigned int
		wantTlc, wantQlc         int
	}{
		{
			// 10240GiB derives 2 TLC cores, above the 1 assigned.
			name:   "mixed, assigned below capacity-derived tlc share",
			tlcGiB: 10240, qlcGiB: 51200, assigned: 1,
			wantTlc: 1, wantQlc: 0,
		},
		{
			name:   "mixed, assigned above tlc share leaves remainder to qlc",
			tlcGiB: 10240, qlcGiB: 51200, assigned: 5,
			wantTlc: 2, wantQlc: 3,
		},
		{
			name:   "mixed, assigned exactly equals tlc share",
			tlcGiB: 10240, qlcGiB: 51200, assigned: 2,
			wantTlc: 2, wantQlc: 0,
		},
		{
			name:   "tlc-only attributes every assigned core to tlc",
			tlcGiB: 10240, qlcGiB: 0, assigned: 4,
			wantTlc: 4, wantQlc: 0,
		},
		{
			name:   "qlc-only attributes every assigned core to qlc",
			tlcGiB: 0, qlcGiB: 51200, assigned: 3,
			wantTlc: 0, wantQlc: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTlc := tlcDriveCoresForContainer(tt.tlcGiB, tt.qlcGiB, tt.assigned, cons)
			gotQlc := qlcDriveCoresForContainer(tt.tlcGiB, tt.qlcGiB, tt.assigned, cons)

			if gotTlc != tt.wantTlc || gotQlc != tt.wantQlc {
				t.Errorf("tlc/qlc = %d/%d, want %d/%d", gotTlc, gotQlc, tt.wantTlc, tt.wantQlc)
			}
			if got := gotTlc + gotQlc; got != tt.assigned {
				t.Errorf("tlc+qlc = %d, want %d (the split must not invent or drop cores)", got, tt.assigned)
			}
		})
	}
}

// With no assigned count to split, both sides fall back to their capacity-derived values and the sum is
// not bound to assignedCores — 2 TLC cores for 10240GiB plus 1 QLC core for 51200GiB.
func TestDriveCoresForContainer_UnassignedFallsBackToCapacityDerived(t *testing.T) {
	cons := testCons()

	gotTlc := tlcDriveCoresForContainer(10240, 51200, 0, cons)
	gotQlc := qlcDriveCoresForContainer(10240, 51200, 0, cons)

	if gotTlc != 2 || gotQlc != 1 {
		t.Errorf("tlc/qlc = %d/%d, want 2/1 (capacity-derived on both sides)", gotTlc, gotQlc)
	}
}
