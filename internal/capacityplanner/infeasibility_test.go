package capacityplanner

import (
	"strings"
	"testing"
)

func hasFix(fixes []string, substr string) bool {
	for _, f := range fixes {
		if strings.Contains(f, substr) {
			return true
		}
	}
	return false
}

// TestInfeasibilityReport_ByClass drives one representative scenario per infeasibility class and asserts
// the structured report's Pool / Binding and that its Fixes carry the class-appropriate remediation.
func TestInfeasibilityReport_ByClass(t *testing.T) {
	cases := []struct {
		name        string
		plan        func() CapacityPlan
		wantPool    string
		wantBinding string
		wantFix     string // substring that must appear in at least one Fix
	}{
		{
			name: "protection floor (single-parity scheme, flag off)",
			plan: func() CapacityPlan {
				s := singleParityScheme()
				return planCap(desiredFrom(30*tib, s, ratio(0, 1)), s, nil, nodes(3, 0, 100*tib, 64, "q"), testCons())
			},
			wantPool: "", wantBinding: "protection", wantFix: "stripeWidth",
		},
		{
			name: "pinned driveContainers below minFdNum",
			plan: func() CapacityPlan {
				s := testScheme()
				d := desiredFrom(30*tib, s, ratio(1, 0))
				d.DriveContainers = 2 // below minFdNum = 6
				return planCap(d, s, nil, nodes(10, 70*tib, 0, 64, "n"), testCons())
			},
			wantPool: "", wantBinding: "driveContainers", wantFix: "driveContainers",
		},
		{
			name: "driveCores pinned too small",
			plan: func() CapacityPlan {
				s := testScheme()
				d := desiredFrom(30*tib, s, ratio(1, 0))
				d.DriveCores = 1 // far below what a multi-TiB FD needs
				return planCap(d, s, nil, nodes(6, 70*tib, 0, 64, "n"), testCons())
			},
			wantPool: "", wantBinding: "driveCores", wantFix: "driveCores",
		},
		{
			name: "drive-capacity bound: FDs too small to tile uniformly",
			plan: func() CapacityPlan {
				s := testScheme()
				// 6 candidate nodes (>= minFdNum), each just above the min chunk but far below the per-FD share.
				return planCap(desiredFrom(50*tib, s, ratio(1, 0)), s, nil, nodes(6, 400, 0, 64, "n"), testCons())
			},
			wantPool: "tlc", wantBinding: "drive capacity", wantFix: "driveTypesRatio",
		},
		{
			name: "failure-domains bound: too few candidate nodes",
			plan: func() CapacityPlan {
				s := testScheme()
				return planCap(desiredFrom(10*tib, s, ratio(1, 0)), s, nil, nodes(3, 70*tib, 0, 64, "n"), testCons())
			},
			wantPool: "tlc", wantBinding: "failure domains", wantFix: "minFdNum",
		},
		{
			name: "growth disabled: existing FDs frozen, no spare nodes to add",
			plan: func() CapacityPlan {
				cons := testCons()
				cons.AllowInPlaceGrowth = false
				s := testScheme() // minFdNum = 6
				// 6 existing FDs, one per node; every node is occupied so no FRESH node is available, and
				// growing the existing FDs in place is disabled → the only cover is more FDs, which needs
				// spare nodes that don't exist.
				var existing []ExistingContainer
				for i := 1; i <= 6; i++ {
					n := "n" + itoa(i)
					existing = append(existing, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, TlcGiB: 30 * tib, NumCores: 6})
				}
				// Nodes carry ample free headroom (so growing the existing FDs COULD reach the target — best
				// != nil, best.L > T0), but every node is occupied so no fresh FD can be added; with growth
				// disabled the only cover is more FDs, which needs spare nodes that don't exist.
				return planCap(desiredFrom(240*tib, s, ratio(1, 0)), s, existing, nodes(6, 200*tib, 0, 64, "n"), cons)
			},
			wantPool: "tlc", wantBinding: "failure domains", wantFix: "enableDynamicDriveScalingForSharedDrives",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := tc.plan()
			if plan.Infeasible == "" {
				t.Fatalf("expected an infeasible plan, got feasible")
			}
			if plan.Infeasibility == nil {
				t.Fatalf("Infeasibility report must be populated whenever Infeasible is set")
			}
			if plan.Infeasibility.Reason != plan.Infeasible {
				t.Fatalf("Infeasibility.Reason must mirror Infeasible byte-for-byte:\n  reason=%q\n  infeasible=%q", plan.Infeasibility.Reason, plan.Infeasible)
			}
			if plan.Infeasibility.Pool != tc.wantPool {
				t.Errorf("Pool = %q, want %q", plan.Infeasibility.Pool, tc.wantPool)
			}
			if plan.Infeasibility.Binding != tc.wantBinding {
				t.Errorf("Binding = %q, want %q", plan.Infeasibility.Binding, tc.wantBinding)
			}
			if len(plan.Infeasibility.Fixes) == 0 {
				t.Errorf("Fixes must be non-empty for an infeasible plan")
			}
			if !hasFix(plan.Infeasibility.Fixes, tc.wantFix) {
				t.Errorf("Fixes %v must contain a tip mentioning %q", plan.Infeasibility.Fixes, tc.wantFix)
			}
		})
	}
}

// An ineligible node must be rejected for its own reason ("ineligible (<reason>)"), not passed as a
// usable candidate because it has free headroom.
func TestInfeasibilityReport_RejectedNodes_IneligibleNode(t *testing.T) {
	s := testScheme() // minFdNum = 6
	inv := nodes(6, 70*tib, 0, 64, "n")
	// n6 has plenty of TLC headroom otherwise.
	inv[5].IneligibleReason = "cordoned"
	plan := planCap(desiredFrom(10*tib, s, ratio(1, 0)), s, nil, inv, testCons())

	if plan.Infeasible == "" {
		t.Fatalf("expected an infeasible plan (only 5 of 6 candidate nodes usable — n6 is cordoned), got feasible")
	}
	if plan.Infeasibility == nil {
		t.Fatalf("Infeasibility report must be populated whenever Infeasible is set")
	}
	var got *NodeRejection
	for i := range plan.Infeasibility.RejectedNodes {
		if plan.Infeasibility.RejectedNodes[i].Node == "n6" {
			got = &plan.Infeasibility.RejectedNodes[i]
		}
	}
	if got == nil {
		t.Fatalf("RejectedNodes = %+v, want an entry for n6 (cordoned)", plan.Infeasibility.RejectedNodes)
	}
	if got.Binding != "ineligible (cordoned)" {
		t.Errorf("n6's Binding = %q, want %q — an ineligible node's own rejection cause, not a headroom binding",
			got.Binding, "ineligible (cordoned)")
	}
}

// TestInfeasibilityReport_NilWhenFeasible asserts a feasible plan leaves the structured report nil.
func TestInfeasibilityReport_NilWhenFeasible(t *testing.T) {
	s := testScheme()
	plan := planCap(desiredFrom(30*tib, s, ratio(1, 0)), s, nil, nodes(6, 70*tib, 0, 64, "n"), testCons())
	if plan.Infeasible != "" {
		t.Fatalf("expected a feasible plan, got Infeasible=%q", plan.Infeasible)
	}
	if plan.Infeasibility != nil {
		t.Fatalf("Infeasibility must be nil on a feasible plan, got %+v", plan.Infeasibility)
	}
}
