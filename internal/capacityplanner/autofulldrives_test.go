package capacityplanner

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// autofulldrives_test.go covers PlanAutoFullDrives (autofulldrives.go): the auto-full-drives mode, in
// which the cluster acts as a daemonset — one drive container per eligible node, claiming all signed
// full drives (or the numDrives pin's worth of the largest). Drives and cores are independent: cores
// are driveCores when pinned, else min(drives, MaxCoresPerContainer). A node that cannot fit its own
// drives fails the whole plan; drives are never dropped and cores never traded away for compute. Test
// nodes are non-HT unless noted, so physicalCPUCost reduces to cost = dataCores + 1.

// uniformDrives returns n drives of equal size sizeEach GiB.
func uniformDrives(n, sizeEach int) []int {
	if n == 0 {
		return nil
	}
	out := make([]int, n)
	for i := range out {
		out[i] = sizeEach
	}
	return out
}

// sumInts (autofulldrives.go) keeps NodeCapacity.TlcGiB in sync with DriveCapacitiesGiB in fixtures.

// singleNodeAutoFullDrives runs PlanAutoFullDrives with one node and an empty (non-nil) computeNodes map,
// isolating the drive-placement decision from compute layout.
func singleNodeAutoFullDrives(nc NodeCapacity, desired AutoFullDrivesDesired, cons *CapacityConstraints) CapacityPlan {
	return PlanAutoFullDrives(desired, nil, nil, []NodeCapacity{nc}, map[string]bool{}, cons)
}

func TestPlanAutoFullDrives_DriveNodeCases(t *testing.T) {
	cons := testCons()
	const bigFree = 1 << 28 // hugepages/memory headroom large enough to never bind in these cases

	cases := []struct {
		name string
		nc   NodeCapacity
		// desired.DriveCores; 0 unless the case is exercising the exact override.
		driveCores int

		wantInfeasible  bool // node cannot fit its own drives => the WHOLE plan fails, nothing is created
		wantCreate      bool // false => node contributes no plan.Create entry
		wantNumDrives   int
		wantTlcGiB      int
		wantNumCores    int
		wantWarnSub     string // substring expected in SOME plan.Warnings entry; "" => no warnings at all
		wantNoWarnAtAll bool   // asserted separately from wantWarnSub=="" for the zero-drive silent-skip case
	}{
		{
			name: "single node N drives -> 1 container pinned correctly",
			nc: NodeCapacity{
				NodeName: "n1", FDValue: "fdA", DriveCapacitiesGiB: uniformDrives(5, 1000), TlcGiB: 5000,
				AllocatableCPU: 100, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
			},
			// cores=FullDriveCores(5,cons)=5 (unbounded); cpuNeed=5+1=6 <= 100 -> fits, no drop.
			wantCreate: true, wantNumDrives: 5, wantTlcGiB: 5000, wantNumCores: 5,
		},
		{
			name: "0-drive node skipped silently",
			nc: NodeCapacity{
				NodeName: "n1", FDValue: "fdA", DriveCapacitiesGiB: nil, TlcGiB: 0,
				AllocatableCPU: 100, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
			},
			wantCreate: false, wantNoWarnAtAll: true,
		},
		{
			// Sorted descending {3000,2000,1000}: keeping 2 drives needs cpu=2+1=3 > AllocatableCPU=2, so
			// only the single largest (3000) fits.
			name: "can't fit all drives -> smallest dropped exactly + Warning",
			nc: NodeCapacity{
				NodeName: "n1", FDValue: "fdA", DriveCapacitiesGiB: []int{3000, 2000, 1000}, TlcGiB: 6000,
				AllocatableCPU: 2, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
			},
			// Drives are never dropped to fit; a node that cannot host all of them fails the whole plan.
			wantInfeasible: true,
		},
		{
			// cores=FullDriveCores(1,cons)=1, cpuNeed=1+1=2 > allocCPU=1 -> even the only drive is skipped.
			name: "can't fit even 1 drive -> skipped + Warning",
			nc: NodeCapacity{
				NodeName: "n1", FDValue: "fdA", DriveCapacitiesGiB: []int{6000}, TlcGiB: 6000,
				AllocatableCPU: 1, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
			},
			wantInfeasible: true,
		},
		{
			// A pin below the node's drive count is lossless: all 3 drives are claimed and run on 1 core,
			// with no warning.
			name: "DriveCores pinned below node's drive count -> all drives kept, fewer cores",
			nc: NodeCapacity{
				NodeName: "n1", FDValue: "fdA", DriveCapacitiesGiB: uniformDrives(3, 20000), TlcGiB: 60000,
				AllocatableCPU: 10, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
			},
			driveCores:      1,
			wantCreate:      true,
			wantNumDrives:   3,
			wantTlcGiB:      60000,
			wantNumCores:    1,
			wantNoWarnAtAll: true,
		},
		{
			name: "DriveCores unset uses derived",
			nc: NodeCapacity{
				NodeName: "n1", FDValue: "fdA", DriveCapacitiesGiB: []int{20000}, TlcGiB: 20000,
				AllocatableCPU: 10, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
			},
			// 1 physical drive -> cores=FullDriveCores(1,cons)=1 regardless of its 20000 GiB capacity.
			wantCreate: true, wantNumDrives: 1, wantTlcGiB: 20000, wantNumCores: 1,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plan := singleNodeAutoFullDrives(c.nc, AutoFullDrivesDesired{DriveCores: c.driveCores}, cons)

			if c.wantInfeasible {
				if plan.Infeasible == "" {
					t.Fatalf("expected the plan to be infeasible, got Create=%+v", plan.Create)
				}
				if len(plan.ComputeLayout) != 0 {
					t.Errorf("infeasible plan must carry no compute layout, got %+v", plan.ComputeLayout)
				}
				return
			}

			if !c.wantCreate {
				if len(plan.Create) != 0 {
					t.Fatalf("expected no Create entry, got %+v", plan.Create)
				}
			} else {
				if len(plan.Create) != 1 {
					t.Fatalf("expected exactly 1 Create entry, got %d: %+v", len(plan.Create), plan.Create)
				}
				got := plan.Create[0]
				if got.Node != c.nc.NodeName {
					t.Errorf("Node = %q, want %q", got.Node, c.nc.NodeName)
				}
				if got.FDValue != c.nc.FDValue {
					t.Errorf("FDValue = %q, want %q", got.FDValue, c.nc.FDValue)
				}
				if got.NumDrives != c.wantNumDrives {
					t.Errorf("NumDrives = %d, want %d", got.NumDrives, c.wantNumDrives)
				}
				if got.TlcGiB != c.wantTlcGiB {
					t.Errorf("TlcGiB = %d, want %d", got.TlcGiB, c.wantTlcGiB)
				}
				if got.QlcGiB != 0 {
					t.Errorf("QlcGiB = %d, want 0 (full drives are TLC-only)", got.QlcGiB)
				}
				if got.NumCores != c.wantNumCores {
					t.Errorf("NumCores = %d, want %d", got.NumCores, c.wantNumCores)
				}
				if got.Type != DriveTypeTLC {
					t.Errorf("Type = %q, want %q", got.Type, DriveTypeTLC)
				}
				if got.Ratio != nil {
					t.Errorf("Ratio = %+v, want nil (full-drives mode has no TLC/QLC ratio)", got.Ratio)
				}
			}

			joined := strings.Join(WarningMessages(plan.Warnings), " | ")
			if c.wantNoWarnAtAll {
				if len(plan.Warnings) != 0 {
					t.Errorf("expected no warnings at all, got %q", joined)
				}
				return
			}
			if c.wantWarnSub == "" {
				return // not asserting on warnings either way for this case
			}
			if !strings.Contains(joined, c.wantWarnSub) {
				t.Errorf("expected a warning containing %q, got %q", c.wantWarnSub, joined)
			}
		})
	}
}

// TestPlanAutoFullDrives_DriveCoresPinnedAboveDriveCount_Infeasible: pinning DriveCores above what a node's full
// drives can back is infeasible, not silently capped — autoSizeNode rejects a pin the node
// can't supply one physical drive per requested core for (1 drive, pin=5).
func TestPlanAutoFullDrives_DriveCoresPinnedAboveDriveCount_Infeasible(t *testing.T) {
	cons := testCons()
	nc := NodeCapacity{
		NodeName: "n1", FDValue: "fdA", DriveCapacitiesGiB: []int{1000}, TlcGiB: 1000,
		AllocatableCPU: 10, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28,
	}
	plan := singleNodeAutoFullDrives(nc, AutoFullDrivesDesired{DriveCores: 5}, cons)

	if len(plan.Create) != 0 {
		t.Fatalf("expected no Create entry (infeasible), got %+v", plan.Create)
	}
	if plan.Infeasible == "" {
		t.Fatalf("expected plan.Infeasible to be set")
	}
	if plan.Infeasibility == nil {
		t.Fatalf("expected plan.Infeasibility to be set")
	}
	if plan.Infeasibility.Pool != "drive" {
		t.Errorf("Infeasibility.Pool = %q, want %q", plan.Infeasibility.Pool, "drive")
	}
	if plan.Infeasibility.Binding != "driveCores" {
		t.Errorf("Infeasibility.Binding = %q, want %q", plan.Infeasibility.Binding, "driveCores")
	}
	if !strings.Contains(plan.Infeasible, "exceeds the 1 full drive(s)") {
		t.Errorf("Infeasible = %q, want it to mention the 1 available full drive", plan.Infeasible)
	}
}

// TestPlanAutoFullDrives_DriveCoresPinned_CPUBoundFailsWholePlan: a node that cannot fit the pinned core
// count fails the whole plan, naming itself and its binding resource. The pin (2) is within the node's
// drive count, so the driveCores-above-drives rule does not fire and physical CPU is what binds.
func TestPlanAutoFullDrives_DriveCoresPinned_CPUBoundFailsWholePlan(t *testing.T) {
	cons := testCons()
	drives := []int{1000, 1000}
	nc := NodeCapacity{
		NodeName: "n1", FDValue: "fdA", DriveCapacitiesGiB: drives, TlcGiB: sumInts(drives),
		AllocatableCPU: 1, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28,
	}

	// A 2-core container costs 2+1 = 3 physical CPU against the node's 1.
	plan := singleNodeAutoFullDrives(nc, AutoFullDrivesDesired{DriveCores: 2}, cons)

	if plan.Infeasible == "" {
		t.Fatalf("expected infeasible, got Create=%+v", plan.Create)
	}
	if len(plan.ComputeLayout) != 0 {
		t.Errorf("infeasible plan must carry no compute layout, got %+v", plan.ComputeLayout)
	}
	if plan.Infeasibility == nil || len(plan.Infeasibility.RejectedNodes) != 1 {
		t.Fatalf("want exactly 1 rejected node, got %+v", plan.Infeasibility)
	}
	if r := plan.Infeasibility.RejectedNodes[0]; r.Node != "n1" || r.Binding != "cores" || r.Unit != "physical CPU" {
		t.Errorf("RejectedNodes[0] = %+v, want n1 bound on cores / physical CPU", r)
	}
}

// TestPlanAutoFullDrives_TotalTlcDriveCores_FollowsPinBelowCapacityDerived: totalTlcDriveCores must read the
// container's actually-assigned cores (the pinned DriveCores=1), not recompute from drive count (which
// would naturally derive FullDriveCores(3,cons)=3).
func TestPlanAutoFullDrives_TotalTlcDriveCores_FollowsPinBelowCapacityDerived(t *testing.T) {
	cons := testCons()
	const bigFree = 1 << 28
	nc := NodeCapacity{
		NodeName: "n1", FDValue: "fdA", DriveCapacitiesGiB: uniformDrives(3, 2000), TlcGiB: 6000,
		AllocatableCPU: 10, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
	}

	plan := singleNodeAutoFullDrives(nc, AutoFullDrivesDesired{DriveCores: 1}, cons)

	if len(plan.Create) != 1 {
		t.Fatalf("expected exactly 1 Create entry, got %d: %+v", len(plan.Create), plan.Create)
	}
	if got := plan.Create[0]; got.NumCores != 1 || got.NumDrives != 3 || got.TlcGiB != 6000 {
		t.Errorf("Create entry = %+v, want NumCores=1 NumDrives=3 TlcGiB=6000 — the pin sets cores only, "+
			"every drive is still claimed", got)
	}
	if plan.TotalTlcDriveCores != 1 {
		t.Errorf("TotalTlcDriveCores = %d, want 1 (must follow the pinned core count, NOT the "+
			"natural unpinned FullDriveCores(3,cons)=3)", plan.TotalTlcDriveCores)
	}
}

// TestPlanAutoFullDrives_TotalTlcDriveCores_PinAboveCapLimitedDerived_OverridesCap: mirror direction. With
// MaxCoresPerContainer=2, 4 drives would naturally derive FullDriveCores(4,cons)=2 (capped). Pinning
// DriveCores=4 overrides the per-container core limit entirely — all 4 drives kept, and
// TotalTlcDriveCores must reflect the pinned 4, not the cap-limited 2.
func TestPlanAutoFullDrives_TotalTlcDriveCores_PinAboveCapLimitedDerived_OverridesCap(t *testing.T) {
	cons := *testCons()
	cons.MaxCoresPerContainer = 2
	const bigFree = 1 << 28
	nc := NodeCapacity{
		NodeName: "n1", FDValue: "fdA", DriveCapacitiesGiB: uniformDrives(4, 1000), TlcGiB: 4000,
		AllocatableCPU: 10, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
	}

	plan := singleNodeAutoFullDrives(nc, AutoFullDrivesDesired{DriveCores: 4}, &cons)

	if len(plan.Create) != 1 {
		t.Fatalf("expected exactly 1 Create entry, got %d: %+v", len(plan.Create), plan.Create)
	}
	if got := plan.Create[0]; got.NumCores != 4 || got.NumDrives != 4 || got.TlcGiB != 4000 {
		t.Errorf("Create entry = %+v, want NumCores=4 NumDrives=4 (the pin overrides MaxCoresPerContainer=2) TlcGiB=4000", got)
	}
	if plan.TotalTlcDriveCores != 4 {
		t.Errorf("TotalTlcDriveCores = %d, want 4 (must follow the pinned core count, NOT the "+
			"cap-limited natural FullDriveCores(4,cons)=2)", plan.TotalTlcDriveCores)
	}
	if len(plan.Warnings) != 0 {
		t.Errorf("expected no warnings (all 4 drives fit within the pin), got %v", plan.Warnings)
	}
}

// TestPlanAutoFullDrives_Heterogeneous covers per-node independence: two nodes with different drive counts and
// capacities each get their own correctly-sized container in one PlanAutoFullDrives call. computeNodes is
// empty, so the compute pool is unconditionally infeasible and the Create values asserted below depend only
// on drive count and MaxCoresPerContainer, not FullDrivesComputeToDriveCoreRatio.
func TestPlanAutoFullDrives_Heterogeneous(t *testing.T) {
	cons := testCons()
	inv := []NodeCapacity{
		{ // cores = FullDriveCores(2, cons) = 2 (one core per drive, well under the 19 cap)
			NodeName: "small", FDValue: "small", DriveCapacitiesGiB: uniformDrives(2, 1000), TlcGiB: 2000,
			AllocatableCPU: 100, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28,
		},
		{ // cores = FullDriveCores(20, cons) = 19 (one core per drive, capped at MaxCoresPerContainer=19;
			// one of the 20 uniform-size drives is dropped)
			NodeName: "big", FDValue: "big", DriveCapacitiesGiB: uniformDrives(20, 1000), TlcGiB: 20000,
			AllocatableCPU: 100, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28,
		},
	}

	plan := PlanAutoFullDrives(AutoFullDrivesDesired{}, nil, nil, inv, map[string]bool{}, cons)

	if len(plan.Create) != 2 {
		t.Fatalf("expected 2 Create entries, got %d: %+v", len(plan.Create), plan.Create)
	}
	byNode := map[string]NewContainer{}
	for _, c := range plan.Create {
		byNode[c.Node] = c
	}
	small, ok := byNode["small"]
	if !ok {
		t.Fatalf("missing Create entry for node %q: %+v", "small", plan.Create)
	}
	if small.NumDrives != 2 || small.TlcGiB != 2000 || small.NumCores != 2 {
		t.Errorf("small node: got NumDrives=%d TlcGiB=%d NumCores=%d, want 2/2000/2",
			small.NumDrives, small.TlcGiB, small.NumCores)
	}
	big, ok := byNode["big"]
	if !ok {
		t.Fatalf("missing Create entry for node %q: %+v", "big", plan.Create)
	}
	if big.NumDrives != 20 || big.TlcGiB != 20000 || big.NumCores != 19 {
		t.Errorf("big node: got NumDrives=%d TlcGiB=%d NumCores=%d, want 20/20000/19 — every drive is kept; "+
			"only CORES are capped at MaxCoresPerContainer=19", big.NumDrives, big.TlcGiB, big.NumCores)
	}
}

// TestPlanAutoFullDrives_ExpandOnly_NoDuplicateCreate covers the expand-only diff: a node that already hosts a
// drive container must not get a second Create entry, even though it still has full drives in the
// inventory. NumDrives==3 matches the node's live drive count, so this also covers "same drive count -> no
// Grow" (TestPlanAutoFullDrives_A4Growth has the dedicated versions of that and the other A4 cases).
func TestPlanAutoFullDrives_ExpandOnly_NoDuplicateCreate(t *testing.T) {
	cons := testCons()
	inv := []NodeCapacity{
		{ // steady state: all 3 full drives already owned (OwnDriveCapacitiesGiB), none free.
			NodeName: "hasContainer", FDValue: "hasContainer",
			OwnDriveCapacitiesGiB: uniformDrives(3, 1000),
			AllocatableCPU:        100, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28,
		},
		{
			NodeName: "fresh", FDValue: "fresh", DriveCapacitiesGiB: uniformDrives(3, 1000), TlcGiB: 3000,
			AllocatableCPU: 100, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28,
		},
	}
	existingDrives := []ExistingContainer{
		{Name: "drive-hasContainer", Node: "hasContainer", FDValue: "hasContainer", TlcGiB: 3000, NumCores: 1, NumDrives: 3},
	}

	plan := PlanAutoFullDrives(AutoFullDrivesDesired{}, existingDrives, nil, inv, map[string]bool{}, cons)

	// The drive count is unchanged, but the container was recorded with 1 core for 3 drives, so it is
	// under-cored for what it holds: a cores-only growth.
	if len(plan.Grow) != 1 {
		t.Fatalf("expected exactly 1 cores-only Grow entry, got %+v", plan.Grow)
	}
	if g := plan.Grow[0]; g.NewNumDrives != 3 || g.NewCores != 3 {
		t.Errorf("Grow = %+v, want NewNumDrives=3 (unchanged) NewCores=3 (raised from 1)", g)
	}
	if len(plan.Create) != 1 {
		t.Fatalf("expected exactly 1 Create entry (only the fresh node), got %d: %+v", len(plan.Create), plan.Create)
	}
	if plan.Create[0].Node != "fresh" {
		t.Fatalf("expected the Create entry to target %q, got %q (existing-container node must be skipped)",
			"fresh", plan.Create[0].Node)
	}
}

// TestPlanAutoFullDrives_A4Growth covers the expand-only growth diff: PlanAutoFullDrives compares a node's live
// total full-drive count (own + free) against the existing container's NumDrives and, only on more drives,
// appends a Grow entry raising NumDrives/TlcGiB/cores to match — never on equal/fewer, and never re-deriving
// cores when driveCores is pinned.
func TestPlanAutoFullDrives_A4Growth(t *testing.T) {
	cons := testCons() // MaxCoresPerContainer unset -> FullDriveCores(n, cons) == n: one core per drive, unbounded.
	const bigFree = 1 << 28

	cases := []struct {
		name string
		// existing container fixture: current drive count / capacity / cores.
		existingNumDrives int
		existingTlcGiB    int
		existingNumCores  int
		// ownDrives: already allocated to this cluster's own container (coherent with existingNumDrives
		// above). freeDrives: newly-signed, unallocated drives the node additionally reports.
		ownDrives  []int
		freeDrives []int
		driveCores int // desired.DriveCores; 0 unless pinning.

		wantGrow         bool
		wantNewNumDrives int
		wantNewTlcGiB    int
		wantNewCores     int
	}{
		{
			// Lab scenario at smaller numbers: 3 own + 2 free -> 5 total. Grow raises NumDrives=5,
			// TlcGiB=5000, cores=FullDriveCores(5,cons)=5 — re-derived from the new total, not carried over.
			name:              "more drives -> Grow entry with new count/cores",
			existingNumDrives: 3, existingTlcGiB: 3000, existingNumCores: 1,
			ownDrives: uniformDrives(3, 1000), freeDrives: uniformDrives(2, 1000),
			wantGrow: true, wantNewNumDrives: 5, wantNewTlcGiB: 5000, wantNewCores: 5,
		},
		{
			// own 3 + free 8 = 11 total -> FullDriveCores(11, cons)=11, up from the existing 1.
			name:              "more drives crossing a cores boundary -> cores raised too",
			existingNumDrives: 3, existingTlcGiB: 3000, existingNumCores: 1,
			ownDrives: uniformDrives(3, 1000), freeDrives: uniformDrives(8, 1000),
			wantGrow: true, wantNewNumDrives: 11, wantNewTlcGiB: 11000, wantNewCores: 11,
		},
		{
			// Steady state, no free drives: caught by step 1's zero-free-drives skip before existingByNode
			// is reached (TestPlanAutoFullDrives_ExpandOnly_NoDuplicateCreate pins that path independently).
			name:              "same drive count and cores -> no Grow",
			existingNumDrives: 3, existingTlcGiB: 3000, existingNumCores: 3,
			ownDrives: uniformDrives(3, 1000), freeDrives: nil,
			wantGrow: false,
		},
		{
			// existingNumDrives (5) exceeds the node's reported own+free (4) — shouldn't happen in
			// practice, but guards the hard "never shrink" invariant. Non-zero free exercises the actual
			// ">" comparison inside existingByNode, not step 1's skip.
			name:              "fewer drives reported -> no Grow (never shrink)",
			existingNumDrives: 5, existingTlcGiB: 5000, existingNumCores: 5,
			ownDrives: uniformDrives(3, 1000), freeDrives: uniformDrives(1, 1000),
			wantGrow: false,
		},
		{
			// Drives unchanged, but the container holds 3 of them on 1 core — under-cored, so cores alone
			// rise.
			name:              "same drives, fewer cores than drives -> cores-only Grow",
			existingNumDrives: 3, existingTlcGiB: 3000, existingNumCores: 1,
			ownDrives: uniformDrives(3, 1000), freeDrives: nil,
			wantGrow: true, wantNewNumDrives: 3, wantNewTlcGiB: 3000, wantNewCores: 3,
		},
		{
			// Cores pinned at 11, matching the node's total available drives (own 3 + free 8) exactly, so
			// so cores and drives coincide here (a lower pin would simply mean fewer cores, same drives).
			name:              "pinned driveCores -> drive count grows but cores stay pinned",
			existingNumDrives: 3, existingTlcGiB: 3000, existingNumCores: 11,
			ownDrives: uniformDrives(3, 1000), freeDrives: uniformDrives(8, 1000),
			driveCores: 11,
			wantGrow:   true, wantNewNumDrives: 11, wantNewTlcGiB: 11000, wantNewCores: 11, // pinned cores carried over
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			nc := NodeCapacity{
				NodeName: "n1", FDValue: "fdA",
				DriveCapacitiesGiB: c.freeDrives, TlcGiB: sumInts(c.freeDrives),
				OwnDriveCapacitiesGiB: c.ownDrives,
				AllocatableCPU:        100, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
			}
			existingDrives := []ExistingContainer{
				{Name: "drive-n1", Node: "n1", FDValue: "fdA", TlcGiB: c.existingTlcGiB, NumCores: c.existingNumCores, NumDrives: c.existingNumDrives},
			}
			desired := AutoFullDrivesDesired{DriveCores: c.driveCores}

			plan := PlanAutoFullDrives(desired, existingDrives, nil, []NodeCapacity{nc}, map[string]bool{}, cons)

			if len(plan.Create) != 0 {
				t.Fatalf("expected no Create entry (node already has a container), got %+v", plan.Create)
			}

			if !c.wantGrow {
				if len(plan.Grow) != 0 {
					t.Fatalf("expected no Grow entries, got %+v", plan.Grow)
				}
				return
			}
			if len(plan.Grow) != 1 {
				t.Fatalf("expected exactly 1 Grow entry, got %d: %+v", len(plan.Grow), plan.Grow)
			}
			got := plan.Grow[0]
			if got.Name != "drive-n1" {
				t.Errorf("Name = %q, want %q", got.Name, "drive-n1")
			}
			if got.NewNumDrives != c.wantNewNumDrives {
				t.Errorf("NewNumDrives = %d, want %d", got.NewNumDrives, c.wantNewNumDrives)
			}
			if got.NewTlcGiB != c.wantNewTlcGiB {
				t.Errorf("NewTlcGiB = %d, want %d", got.NewTlcGiB, c.wantNewTlcGiB)
			}
			if got.NewQlcGiB != 0 {
				t.Errorf("NewQlcGiB = %d, want 0 (full drives are TLC-only)", got.NewQlcGiB)
			}
			if got.NewCores != c.wantNewCores {
				t.Errorf("NewCores = %d, want %d", got.NewCores, c.wantNewCores)
			}
		})
	}
}

// TestPlanAutoFullDrives_A4Growth_TotalsReflectGrownCapacity covers the growth-aware totals fix bundled with A4:
// plan.TotalTlcDriveCores must reflect existing containers as grown this same pass, not pre-growth capacity
// — otherwise a node that just gained drives wouldn't feed its new TLC drive cores into compute sizing until
// a whole reconcile later.
func TestPlanAutoFullDrives_A4Growth_TotalsReflectGrownCapacity(t *testing.T) {
	cons := testCons()
	// Owns 1 drive/1000 GiB/1 core; node additionally reports 5 free drives/5000 GiB -> 6-drive/6000 GiB
	// total -> cores=FullDriveCores(6,cons)=6. TotalTlcDriveCores must reflect the grown total, not stale state.
	inv := []NodeCapacity{
		{
			NodeName: "n1", FDValue: "fdA",
			DriveCapacitiesGiB: uniformDrives(5, 1000), TlcGiB: 5000,
			OwnDriveCapacitiesGiB: uniformDrives(1, 1000),
			AllocatableCPU:        100, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28,
		},
	}
	existingDrives := []ExistingContainer{
		{Name: "drive-n1", Node: "n1", FDValue: "fdA", TlcGiB: 1000, NumCores: 1, NumDrives: 1},
	}

	plan := PlanAutoFullDrives(AutoFullDrivesDesired{}, existingDrives, nil, inv, map[string]bool{}, cons)

	if len(plan.Grow) != 1 {
		t.Fatalf("expected exactly 1 Grow entry, got %d: %+v", len(plan.Grow), plan.Grow)
	}
	if plan.TotalTlcDriveCores != 6 {
		t.Errorf("TotalTlcDriveCores = %d, want 6 (derived from the GROWN 6-drive total, not the stale 1-drive count)",
			plan.TotalTlcDriveCores)
	}
}

// TestPlanAutoFullDrives_ComputeOnlyPool_DriveFitUnaffectedByComputeHeadroomReservation counterparts the test
// above: hc1 (6x1000GiB, not compute-eligible) must keep all 6 drives at 6 cores even with a separate
// compute-only node (cmp1) providing headroom. AllocatableCPU=7 is the exact physicalCPUCost of a 6-core
// container (6+1) with no slack, proving hc1's fit-check never adds the co-located reservation.
func TestPlanAutoFullDrives_ComputeOnlyPool_DriveFitUnaffectedByComputeHeadroomReservation(t *testing.T) {
	cons := testCons()
	const bigFree = 1 << 28

	inv := []NodeCapacity{
		{ // drive-only node: identical to the hyperconverged test's hc1, but NOT compute-eligible here.
			NodeName: "hc1", FDValue: "fdA", DriveCapacitiesGiB: uniformDrives(6, 1000), TlcGiB: 6000,
			AllocatableCPU: 7, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		},
		{ // dedicated compute-only node.
			NodeName: "cmp1", FDValue: "fdB",
			AllocatableCPU: 100, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		},
	}
	computeNodes := map[string]bool{"cmp1": true} // hc1 absent/false: NOT compute-eligible.
	desired := AutoFullDrivesDesired{}

	plan := PlanAutoFullDrives(desired, nil, nil, inv, computeNodes, cons)

	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s (cmp1 provides ample dedicated compute headroom)", plan.Infeasible)
	}
	if len(plan.Create) != 1 {
		t.Fatalf("expected exactly 1 Create entry, got %d: %+v", len(plan.Create), plan.Create)
	}
	if got := plan.Create[0]; got.NumDrives != 6 || got.TlcGiB != 6000 || got.NumCores != 6 {
		t.Errorf("drive Create entry = %+v, want NumDrives=6 TlcGiB=6000 NumCores=6 (all 6 drives kept at 6 "+
			"cores: hc1 is not compute-eligible, so no compute-headroom reservation should ever be added to "+
			"its fit-check thresholds)", got)
	}
	if len(plan.ComputeLayout) != 1 {
		t.Fatalf("ComputeLayout = %+v, want exactly 1 entry", plan.ComputeLayout)
	}
	if plan.ComputeLayout[0].Node != "cmp1" {
		t.Errorf("compute container placed on %q, want %q (hc1 is drive-only; cmp1 is the dedicated "+
			"compute-eligible node)", plan.ComputeLayout[0].Node, "cmp1")
	}
}

// TestPlanAutoFullDrives_ComputeLayout_AutoDerive covers deriving the compute layout from the TLC drive cores
// PlanAutoFullDrives itself just planned: two drive nodes contribute 1 and 2 TLC drive cores (total 3), and a
// dedicated compute-only node pool (cmp1, cmp2, separate from the drive nodes) must produce a single 3-core
// compute container on the better-fitting/tie-broken node.
func TestPlanAutoFullDrives_ComputeLayout_AutoDerive(t *testing.T) {
	cons := testCons()
	inv := []NodeCapacity{
		{ // one core per drive, unbounded: 1 drive -> FullDriveCores(1) = 1
			NodeName: "drv1", FDValue: "drv1", DriveCapacitiesGiB: []int{5000}, TlcGiB: 5000,
			AllocatableCPU: 100, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28,
		},
		{ // one core per drive, unbounded: 2 drives -> FullDriveCores(2) = 2
			NodeName: "drv2", FDValue: "drv2", DriveCapacitiesGiB: []int{5000, 5000}, TlcGiB: 10000,
			AllocatableCPU: 100, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28,
		},
		{
			NodeName: "cmp1", FDValue: "cmp1",
			AllocatableCPU: 100, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28,
		},
		{
			NodeName: "cmp2", FDValue: "cmp2",
			AllocatableCPU: 100, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28,
		},
	}
	computeNodes := map[string]bool{"cmp1": true, "cmp2": true}

	plan := PlanAutoFullDrives(AutoFullDrivesDesired{}, nil, nil, inv, computeNodes, cons)

	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if plan.TotalTlcDriveCores != 3 {
		t.Fatalf("TotalTlcDriveCores = %d, want 3 (1 from drv1 + 2 from drv2)", plan.TotalTlcDriveCores)
	}
	// required=RequiredComputeCores(3,0,fullDrives=true,cons)=max(3,ceil(2.0*3))=6 (ratio floor exceeds 1:1);
	// coreHeadroom=99 on both compute nodes -> count=ceil(6/99)=1, cores=ceil(6/1)=6.
	if plan.ComputeContainers != 1 {
		t.Errorf("ComputeContainers = %d, want 1", plan.ComputeContainers)
	}
	if plan.ComputeCores != 6 {
		t.Errorf("ComputeCores = %d, want 6", plan.ComputeCores)
	}
	if len(plan.ComputeLayout) != 1 || plan.ComputeLayout[0].NumCores != 6 {
		t.Fatalf("ComputeLayout = %+v, want exactly 1 entry with NumCores=6", plan.ComputeLayout)
	}
	// cmp1/cmp2 tie on headroom (both 99); the node-name tie-break picks cmp1.
	if plan.ComputeLayout[0].Node != "cmp1" {
		t.Errorf("compute container placed on %q, want %q (tie-break by node name)", plan.ComputeLayout[0].Node, "cmp1")
	}
}

// TestPlanAutoFullDrives_ComputeLayout_HugepagesBound_MoreSmallerContainers is the auto-full-drives twin of
// planner_test.go's Test_Compute_HugepagesBound_PrefersMoreSmallerContainers. 6 drive-only nodes pinned
// (DriveCores=15) contribute 90 TLC drive cores; 8 compute-only nodes have ample cores (64) but only 36000
// MiB hugepages each. Cores alone never force n=8: n=7 (39000 MiB) fails, n=8 (36000) fits.
func TestPlanAutoFullDrives_ComputeLayout_HugepagesBound_MoreSmallerContainers(t *testing.T) {
	cons := testCons()
	// Zero the full-drives ratio so required compute cores stay at the 1:1 floor (90) the fixture is tuned
	// around; left at 2.0, requiredComputeCores=180 hits the 19-core MaxCoresPerContainer cap outright
	// (180/8=22.5>19), an unrelated ceiling masking the hugepages mechanism this test guards.
	cons.FullDrivesComputeToDriveCoreRatio = 0
	const bigFree = 1 << 28

	var inv []NodeCapacity
	// 6 drive-only nodes pinned to 15 cores each (90 total); each given exactly 15 signed drives so the
	// pin exactly saturates the node's full drive set (autoSizeNode rejects a pin above the drive count).
	for i := 1; i <= 6; i++ {
		inv = append(inv, NodeCapacity{
			NodeName: "d" + itoa(i), FDValue: "d" + itoa(i),
			DriveCapacitiesGiB: uniformDrives(15, 100), TlcGiB: 1500,
			AllocatableCPU: 100, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		})
	}
	// 8 dedicated compute-only nodes: ample cores (64), but only 36000 MiB hugepages each.
	for i := 1; i <= 8; i++ {
		inv = append(inv, tightNode("c"+itoa(i), 0, 64, 36000))
	}
	computeNodes := computeNodeSet("c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8")
	desired := AutoFullDrivesDesired{DriveCores: 15} // pin drive cores; compute stays auto-derive (1:1 from drive cores)

	plan := PlanAutoFullDrives(desired, nil, nil, inv, computeNodes, cons)

	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if plan.TotalTlcDriveCores != 90 {
		t.Fatalf("TotalTlcDriveCores = %d, want 90 (6 drive nodes x 15 pinned cores)", plan.TotalTlcDriveCores)
	}
	if plan.ComputeContainers != 8 || plan.ComputeCores != 12 {
		t.Fatalf("want compute 8x12 (hugepages-bound, more/smaller containers), got %dx%d", plan.ComputeContainers, plan.ComputeCores)
	}
	if plan.ComputeContainers*plan.ComputeCores < plan.TotalTlcDriveCores {
		t.Fatalf("compute:drive 1:1 violated: %d < %d", plan.ComputeContainers*plan.ComputeCores, plan.TotalTlcDriveCores)
	}
}

// TestPlanAutoFullDrives_Infeasible_ReportsFullClaimAndNoCompute: an infeasible plan still reports what it
// would have claimed, so an operator can see the size of what they are being denied, and carries no compute
// layout at all.
func TestPlanAutoFullDrives_Infeasible_ReportsFullClaimAndNoCompute(t *testing.T) {
	cons := testCons()
	const bigFree = 1 << 28

	inv := []NodeCapacity{
		{
			NodeName: "d1", FDValue: "d1",
			DriveCapacitiesGiB: uniformDrives(10, 5120), TlcGiB: 51200,
			AllocatableCPU: 100, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		},
	}
	computeNodes := computeNodeSet() // no compute-eligible nodes at all

	plan := PlanAutoFullDrives(AutoFullDrivesDesired{}, nil, nil, inv, computeNodes, cons)

	if plan.Infeasible == "" {
		t.Fatalf("expected infeasible plan (no compute-eligible nodes), got feasible: %+v", plan)
	}
	if plan.DriveSizing == nil {
		t.Fatalf("DriveSizing is nil even on infeasibility, want a populated rationale")
	}
	if plan.DriveSizing.DrivesTaken != 10 || plan.DriveSizing.DrivesAvailable != 10 {
		t.Errorf("DrivesTaken/Available = %d/%d, want 10/10 — the report must show the full claim the plan "+
			"was denied, not a reduced one", plan.DriveSizing.DrivesTaken, plan.DriveSizing.DrivesAvailable)
	}
	if len(plan.ComputeLayout) != 0 || plan.ComputeContainers != 0 {
		t.Errorf("infeasible plan carries compute: %d layout entries, %d containers",
			len(plan.ComputeLayout), plan.ComputeContainers)
	}
}

// TestPlanAutoFullDrives_Deterministic_SameInputsSamePlan verifies the determinism requirement: the
// same inputs, run through PlanAutoFullDrives twice with freshly-built slices/maps (no accidental aliasing), yield
// byte-identical Create and DriveSizing output.
func TestPlanAutoFullDrives_Deterministic_SameInputsSamePlan(t *testing.T) {
	buildInv := func() []NodeCapacity {
		var inv []NodeCapacity
		for i := 1; i <= 6; i++ {
			inv = append(inv, NodeCapacity{
				NodeName: "d" + itoa(i), FDValue: "d" + itoa(i),
				DriveCapacitiesGiB: uniformDrives(10, 5120), TlcGiB: 51200,
				AllocatableCPU: 100, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28,
			})
		}
		for i := 1; i <= 8; i++ {
			inv = append(inv, tightNode("c"+itoa(i), 0, 64, 40000))
		}
		return inv
	}
	buildCons := func() *CapacityConstraints {
		c := testCons()
		c.ComputeHugepagesTlcRatio = 1024
		return c
	}
	buildComputeNodes := func() map[string]bool {
		return computeNodeSet("c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8")
	}

	plan1 := PlanAutoFullDrives(AutoFullDrivesDesired{}, nil, nil, buildInv(), buildComputeNodes(), buildCons())
	plan2 := PlanAutoFullDrives(AutoFullDrivesDesired{}, nil, nil, buildInv(), buildComputeNodes(), buildCons())

	if plan1.Infeasible != plan2.Infeasible {
		t.Fatalf("Infeasible mismatch: %q vs %q", plan1.Infeasible, plan2.Infeasible)
	}
	if !reflect.DeepEqual(plan1.Create, plan2.Create) {
		t.Errorf("Create mismatch:\n  run1: %+v\n  run2: %+v", plan1.Create, plan2.Create)
	}
	if !reflect.DeepEqual(plan1.DriveSizing, plan2.DriveSizing) {
		t.Errorf("DriveSizing mismatch:\n  run1: %+v\n  run2: %+v", plan1.DriveSizing, plan2.DriveSizing)
	}
}

// TestPlanAutoFullDrives_PinnedDriveCores_UsedVerbatimAndKeepsAllDrives: a pinned dynamicTemplate.driveCores
// is the container's core count exactly, and does not bound its drive count — stranding has exactly one
// cause left (a numDrives pin), so there must be no warning here at all.
func TestPlanAutoFullDrives_PinnedDriveCores_UsedVerbatimAndKeepsAllDrives(t *testing.T) {
	cons := testCons()
	const bigFree = 1 << 28

	inv := []NodeCapacity{
		{
			NodeName: "d1", FDValue: "d1",
			DriveCapacitiesGiB: uniformDrives(10, 5120), TlcGiB: 51200,
			AllocatableCPU: 100, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		},
		tightNode("c1", 0, 64, bigFree),
	}
	computeNodes := computeNodeSet("c1")
	desired := AutoFullDrivesDesired{DriveCores: 3}

	plan := PlanAutoFullDrives(desired, nil, nil, inv, computeNodes, cons)

	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Create) != 1 || plan.Create[0].NumCores != 3 || plan.Create[0].NumDrives != 10 ||
		plan.Create[0].TlcGiB != 51200 {
		t.Fatalf("Create = %+v, want NumCores=3 NumDrives=10 TlcGiB=51200 — the pin sets cores only; all 10 "+
			"signed drives are still claimed and run on 3 cores", plan.Create)
	}
	for _, w := range plan.Warnings {
		if w.Kind == WarningKindDrivesStranded {
			t.Errorf("unexpected DrivesStranded warning %q — a driveCores pin strands nothing now that drives "+
				"are decoupled from cores", w.Message)
		}
	}
}

// Regression test for the "t==0 phantom compute container" guard in planComputeAutoFullDrives: zero signed
// drives means TotalTlcDriveCores is genuinely 0; two ample compute nodes prove it's the guard, not a lack of capacity.
func TestPlanAutoFullDrives_NoSignedDrives_NoPhantomCompute(t *testing.T) {
	cons := testCons()
	inv := []NodeCapacity{
		{NodeName: "drv1", FDValue: "drv1", AllocatableCPU: 100, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28},
		{NodeName: "drv2", FDValue: "drv2", AllocatableCPU: 100, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28},
		{NodeName: "cmp1", FDValue: "cmp1", AllocatableCPU: 100, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28},
		{NodeName: "cmp2", FDValue: "cmp2", AllocatableCPU: 100, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28},
	}
	computeNodes := computeNodeSet("cmp1", "cmp2")

	plan := PlanAutoFullDrives(AutoFullDrivesDesired{}, nil, nil, inv, computeNodes, cons)

	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Create) != 0 {
		t.Fatalf("plan.Create = %+v, want none (no drives anywhere to place)", plan.Create)
	}
	if plan.TotalTlcDriveCores != 0 {
		t.Fatalf("TotalTlcDriveCores = %d, want 0", plan.TotalTlcDriveCores)
	}
	if plan.ComputeContainers != 0 || plan.ComputeCores != 0 {
		t.Fatalf("want no compute derived (0x0), got %dx%d", plan.ComputeContainers, plan.ComputeCores)
	}
	if len(plan.ComputeLayout) != 0 {
		t.Fatalf("ComputeLayout = %+v, want empty (no phantom compute container)", plan.ComputeLayout)
	}
}

// AutoFullDrivesDesired.ComputeCores is honored exactly; container count = ceil(requiredComputeCores/cores).
// 5 drive cores (3+2 drives) x 2.0 ratio = 10 required compute cores; at the pinned 5/container -> 2 containers.
func TestPlanAutoFullDrives_ComputeLayout_ExplicitCoresPin(t *testing.T) {
	cons := testCons()
	inv := []NodeCapacity{
		{
			NodeName: "drv1", FDValue: "drv1", DriveCapacitiesGiB: uniformDrives(3, 5000), TlcGiB: 15000,
			AllocatableCPU: 100, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28,
		},
		{
			NodeName: "drv2", FDValue: "drv2", DriveCapacitiesGiB: uniformDrives(2, 10000), TlcGiB: 20000,
			AllocatableCPU: 100, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28,
		},
		{
			NodeName: "cmp1", FDValue: "cmp1",
			AllocatableCPU: 100, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28,
		},
		{
			NodeName: "cmp2", FDValue: "cmp2",
			AllocatableCPU: 100, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28,
		},
	}
	computeNodes := map[string]bool{"cmp1": true, "cmp2": true}
	desired := AutoFullDrivesDesired{ComputeCores: 5}

	plan := PlanAutoFullDrives(desired, nil, nil, inv, computeNodes, cons)

	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if plan.ComputeContainers != 2 {
		t.Errorf("ComputeContainers = %d, want 2 derived from 10 required cores at the pinned 5/container",
			plan.ComputeContainers)
	}
	if plan.ComputeCores != 5 {
		t.Errorf("ComputeCores = %d, want the explicit pin of 5", plan.ComputeCores)
	}
	if len(plan.ComputeNodes) != 2 {
		t.Fatalf("ComputeNodes = %v, want both compute nodes used", plan.ComputeNodes)
	}
	if len(plan.ComputeLayout) != 2 {
		t.Fatalf("ComputeLayout = %+v, want exactly 2 entries", plan.ComputeLayout)
	}
	for _, l := range plan.ComputeLayout {
		if l.NumCores != 5 {
			t.Errorf("compute container on %q has NumCores=%d, want the pinned 5", l.Node, l.NumCores)
		}
	}
}

// fdSpreadDriveNode returns a drive-role node with n full drives, kept out of computeNodes so it feeds drive
// cores without becoming a compute candidate. With ComputeCores pinned to c, count = ceil(requiredComputeCores/c),
// and requiredComputeCores = ratio x n drive cores.
func fdSpreadDriveNode(n int) NodeCapacity {
	return NodeCapacity{
		NodeName: "drv", FDValue: "fdDrv",
		DriveCapacitiesGiB: uniformDrives(n, 5000), TlcGiB: n * 5000,
		AllocatableCPU: 100, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28,
	}
}

// computeLayoutNodes returns the sorted node names from a plan's ComputeLayout.
func computeLayoutNodes(layout []ComputeContainerSpec) []string {
	out := make([]string, 0, len(layout))
	for _, l := range layout {
		out = append(out, l.Node)
	}
	sort.Strings(out)
	return out
}

// This mode ignores FDs when placing compute: it occupies every node its selector matches, ordering by free
// core headroom then node name. planCompute (clusterCapacity) still does FD spread.
func TestPlanAutoFullDrives_ComputeLayout_PicksHighestHeadroomNodes(t *testing.T) {
	cons := testCons()
	big := 1 << 28
	node := func(name, fd string, cpu int) NodeCapacity {
		return NodeCapacity{
			NodeName: name, FDValue: fd,
			AllocatableCPU: cpu, AvailableHugepagesMiB: big, AvailableMemoryMiB: big,
		}
	}

	for _, tc := range []struct {
		name string
		inv  []NodeCapacity
		want []string
	}{
		{
			// The two highest-headroom nodes share one FD. FD-spread would have skipped h2 in favour of a1;
			// headroom order takes both.
			name: "shared FD does not deprioritise the second-best node",
			inv:  []NodeCapacity{node("h1", "fdHigh", 101), node("h2", "fdHigh", 91), node("a1", "fdA", 51), node("b1", "fdB", 31)},
			want: []string{"h1", "h2"},
		},
		{
			// AUTO FD mode: FDValue == node name, so every node is its own FD and the answer is the same.
			name: "auto FD mode",
			inv:  []NodeCapacity{node("c1", "c1", 101), node("c2", "c2", 91), node("c3", "c3", 71)},
			want: []string{"c1", "c2"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			computeNodes := map[string]bool{}
			for _, n := range tc.inv {
				computeNodes[n.NodeName] = true
			}
			inv := append(append([]NodeCapacity(nil), tc.inv...), fdSpreadDriveNode(1)) // 1 drive core x 2.0 = 2 required
			desired := AutoFullDrivesDesired{ComputeCores: 1}                           // 2 cores at 1/container -> 2 containers

			plan := PlanAutoFullDrives(desired, nil, nil, inv, computeNodes, cons)

			if plan.Infeasible != "" {
				t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
			}
			if got := computeLayoutNodes(plan.ComputeLayout); len(got) != 2 || got[0] != tc.want[0] || got[1] != tc.want[1] {
				t.Errorf("ComputeLayout nodes = %v, want %v (top-2 by core headroom)", got, tc.want)
			}
		})
	}
}

// A node whose FD already hosts compute is not deprioritised: an existing container on h1 (fdHigh) does not
// push placement away from h2 (also fdHigh) toward the lower-headroom a1.
func TestPlanAutoFullDrives_ComputeLayout_DoesNotAvoidAlreadyCoveredFDs(t *testing.T) {
	cons := testCons()
	big := 1 << 28
	inv := []NodeCapacity{
		{NodeName: "h1", FDValue: "fdHigh", AllocatableCPU: 100, AvailableHugepagesMiB: big, AvailableMemoryMiB: big},
		{NodeName: "h2", FDValue: "fdHigh", AllocatableCPU: 91, AvailableHugepagesMiB: big, AvailableMemoryMiB: big},
		{NodeName: "a1", FDValue: "fdA", AllocatableCPU: 51, AvailableHugepagesMiB: big, AvailableMemoryMiB: big},
	}
	inv = append(inv, fdSpreadDriveNode(1)) // 1 drive core x 2.0 = 2 required; the kept ec1 supplies 1
	computeNodes := map[string]bool{"h1": true, "h2": true, "a1": true}
	existingCompute := []ExistingComputeContainer{{Name: "ec1", Node: "h1", NumCores: 1, HugepagesMiB: 1600}}
	desired := AutoFullDrivesDesired{ComputeCores: 1} // deficit of 1 at 1/container -> exactly 1 new

	plan := PlanAutoFullDrives(desired, nil, existingCompute, inv, computeNodes, cons)

	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.ComputeLayout) != 2 {
		t.Fatalf("ComputeLayout = %+v, want 2 entries (1 kept + 1 new)", plan.ComputeLayout)
	}
	newNode := ""
	for _, l := range plan.ComputeLayout {
		if l.Node != "h1" {
			newNode = l.Node
		}
	}
	if newNode != "h2" {
		t.Errorf("new compute container placed on %q, want %q (highest free headroom; fdHigh already hosting "+
			"compute is not a reason to prefer a1)", newNode, "h2")
	}
}

// TestPlanAutoFullDrives_ComputeLayout_FitPreFilterSkipsUnfittingFreshFD covers the fit pre-filter: a
// fresh-FD node (fdA, only node in its FD) outranks fdB by orderNodesByFDSpread but lacks the hugepages to
// host the uniform footprint, so the fit check before FD-ordering (mirroring orderFitNodesByFreshFD,
// planner.go) must skip it in favour of fdB rather than aborting the whole plan.
func TestPlanAutoFullDrives_ComputeLayout_FitPreFilterSkipsUnfittingFreshFD(t *testing.T) {
	cons := testCons()
	inv := []NodeCapacity{
		{ // headroom 99, highest, ranks first by FD-spread — but hugepages (5000) fall short of
			// perContainerHP (3000*3 cores = 9000 MiB).
			NodeName: "fitsCPUnotHP", FDValue: "fdA",
			AllocatableCPU: 100, AvailableHugepagesMiB: 5000, AvailableMemoryMiB: 1 << 28,
		},
		{ // headroom 49, lower, but ample hugepages — the only node that fits cores=3/perContainerHP=9000.
			NodeName: "fits", FDValue: "fdB",
			AllocatableCPU: 50, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28,
		},
	}
	cons.FullDrivesComputeToDriveCoreRatio = 0 // strict 1:1, so 1 drive core means 1 required compute core
	inv = append(inv, fdSpreadDriveNode(1))
	computeNodes := map[string]bool{"fitsCPUnotHP": true, "fits": true}
	desired := AutoFullDrivesDesired{ComputeCores: 3} // 1 required core at 3/container -> 1 container

	plan := PlanAutoFullDrives(desired, nil, nil, inv, computeNodes, cons)

	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s (fit pre-filter should have excluded the unfitting fresh-FD "+
			"node up front and used the fitting one instead)", plan.Infeasible)
	}
	if len(plan.ComputeLayout) != 1 {
		t.Fatalf("ComputeLayout = %+v, want exactly 1 entry", plan.ComputeLayout)
	}
	if plan.ComputeLayout[0].Node != "fits" {
		t.Errorf("compute container placed on %q, want %q (the only node that actually fits the uniform "+
			"footprint; the higher-headroom-but-not-fitting node must be skipped, not cause infeasibility)",
			plan.ComputeLayout[0].Node, "fits")
	}
}

// The growth's incremental CPU cost must be charged against remaining[node] before compute placement runs, or
// compute over-commits onto CPU the grown drive container already took.
//
// n1: 1 own drive + 2 free -> grows to 3 drives/3 cores. Growth CPU delta = (3+1)-(1+1)=2. Strict 1:1 makes
// compute require 3 cores, costing 3+1=4 physical CPU. Node needs 2+4=6, so 5 must fail.
func TestPlanAutoFullDrives_A4Growth_ChargesDeltaAgainstComputeHeadroom(t *testing.T) {
	const bigFree = 1 << 28
	for _, tc := range []struct {
		name           string
		allocatableCPU int
		wantFeasible   bool
	}{
		{name: "exactly enough for the growth delta plus compute", allocatableCPU: 6, wantFeasible: true},
		{name: "one short — only detectable if the delta is charged", allocatableCPU: 5, wantFeasible: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cons := testCons()
			cons.FullDrivesComputeToDriveCoreRatio = 0 // strict 1:1, matching the arithmetic above.

			inv := []NodeCapacity{{
				NodeName: "n1", FDValue: "fdA",
				OwnDriveCapacitiesGiB: uniformDrives(1, 1000),
				DriveCapacitiesGiB:    uniformDrives(2, 1000),
				TlcGiB:                2000,
				AllocatableCPU:        tc.allocatableCPU,
				AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
			}}
			existingDrives := []ExistingContainer{
				{Name: "drive-n1", Node: "n1", FDValue: "fdA", TlcGiB: 1000, NumCores: 1, NumDrives: 1},
			}
			plan := PlanAutoFullDrives(AutoFullDrivesDesired{}, existingDrives, nil, inv,
				computeNodeSet("n1"), cons)

			if tc.wantFeasible && plan.Infeasible != "" {
				t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
			}
			if !tc.wantFeasible {
				if plan.Infeasible == "" {
					t.Fatalf("expected infeasible: with the growth delta of 2 charged, only %d physical CPU "+
						"remain and the 3-core compute container needs 4 — a pass here means the delta was "+
						"never charged", tc.allocatableCPU-2)
				}
				return
			}

			// The drive container always claims every drive; only compute is at issue.
			if len(plan.Grow) != 1 {
				t.Fatalf("expected exactly 1 Grow entry, got %d: %+v", len(plan.Grow), plan.Grow)
			}
			if got := plan.Grow[0]; got.NewNumDrives != 3 || got.NewTlcGiB != 3000 || got.NewCores != 3 {
				t.Fatalf("Grow entry = %+v, want NewNumDrives=3 NewTlcGiB=3000 NewCores=3", got)
			}
			if len(plan.ComputeLayout) != 1 || plan.ComputeLayout[0].Node != "n1" {
				t.Fatalf("ComputeLayout = %+v, want exactly 1 entry on n1", plan.ComputeLayout)
			}
		})
	}
}

// TestPlanAutoFullDrives_NoOscillationAcrossReconciles proves the planner is a stable fixed point: feeding
// one pass's output back in as existing state must produce no further change. The fixed point is simply
// "every drive, at one core per drive" — reached in a single pass; a naive "grow to match every free
// drive" reconcile that forgot the drives were already claimed would still oscillate here.
func TestPlanAutoFullDrives_NoOscillationAcrossReconciles(t *testing.T) {
	cons := testCons()
	cons.FullDrivesComputeToDriveCoreRatio = 0 // isolate fixed-point stability from the compute:drive ratio.
	const bigFree = 1 << 28

	driveNode := func(own, free []int) NodeCapacity {
		return NodeCapacity{
			NodeName: "n1", FDValue: "fdA",
			DriveCapacitiesGiB:    free,
			TlcGiB:                sumInts(free),
			OwnDriveCapacitiesGiB: own,
			AllocatableCPU:        100, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		}
	}
	computeNode := tightNode("c1", 0, 64, bigFree)
	computeNodes := computeNodeSet("c1")

	// Pass 1: fresh cluster, no existing containers — claims all 10 drives at 10 cores.
	inv1 := []NodeCapacity{driveNode(nil, uniformDrives(10, 5120)), computeNode}
	plan1 := PlanAutoFullDrives(AutoFullDrivesDesired{}, nil, nil, inv1, computeNodes, cons)

	if plan1.Infeasible != "" {
		t.Fatalf("pass 1 unexpected infeasible: %s", plan1.Infeasible)
	}
	if len(plan1.Create) != 1 {
		t.Fatalf("pass 1: expected exactly 1 Create entry, got %d: %+v", len(plan1.Create), plan1.Create)
	}
	create := plan1.Create[0]
	if create.NumDrives != 10 || create.TlcGiB != 51200 || create.NumCores != 10 {
		t.Fatalf("pass 1 Create = %+v, want NumDrives=10 TlcGiB=51200 NumCores=10", create)
	}

	// Pass 2: feed pass 1's Create back as the existing container. Every drive is now owned, none free.
	existingDrives := []ExistingContainer{
		{Name: "drive-n1", Node: "n1", FDValue: "fdA", TlcGiB: create.TlcGiB, NumCores: create.NumCores, NumDrives: create.NumDrives},
	}
	inv2 := []NodeCapacity{driveNode(uniformDrives(10, 5120), nil), computeNode}
	plan2 := PlanAutoFullDrives(AutoFullDrivesDesired{}, existingDrives, nil, inv2, computeNodes, cons)

	if plan2.Infeasible != "" {
		t.Fatalf("pass 2 unexpected infeasible: %s (the fixed point must be stable, not oscillate into "+
			"infeasibility)", plan2.Infeasible)
	}
	if len(plan2.Create) != 0 {
		t.Fatalf("pass 2: expected no Create entries (node already has a container), got %+v", plan2.Create)
	}
	if len(plan2.Grow) != 0 {
		t.Fatalf("pass 2: expected NO Grow entry — the container already holds every drive on the node at "+
			"one core per drive, so there is nothing left to grow into; got %+v", plan2.Grow)
	}
	if plan2.TotalTlcDriveCores != create.NumCores {
		t.Errorf("pass 2 TotalTlcDriveCores = %d, want %d (unchanged from pass 1 — no oscillation)",
			plan2.TotalTlcDriveCores, create.NumCores)
	}
}

// TestPlanAutoFullDrives_Growth_NeverShrinksBelowExisting: the growth ratchet never reduces a running
// container. Lowering dynamicTemplate.driveCores on a live cluster leaves existing containers where they
// are — the pin governs what is created, and the planner will not rewrite a pod spec downward.
func TestPlanAutoFullDrives_Growth_NeverShrinksBelowExisting(t *testing.T) {
	cons := testCons()
	cons.FullDrivesComputeToDriveCoreRatio = 0 // isolate the never-shrink invariant from the compute:drive ratio.
	const bigFree = 1 << 28

	existing := []ExistingContainer{
		{Name: "drive-n1", Node: "n1", FDValue: "fdA", TlcGiB: 8 * 5120, NumCores: 8, NumDrives: 8},
	}
	inv := []NodeCapacity{
		{
			NodeName: "n1", FDValue: "fdA",
			OwnDriveCapacitiesGiB: uniformDrives(8, 5120),
			TlcGiB:                0,
			AllocatableCPU:        1000, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		},
		tightNode("c1", 0, 64, bigFree),
	}
	computeNodes := computeNodeSet("c1")

	// The pin asks for 3 cores; the running container has 8.
	plan := PlanAutoFullDrives(AutoFullDrivesDesired{DriveCores: 3}, existing, nil, inv, computeNodes, cons)

	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Grow) != 0 {
		t.Fatalf("Grow = %+v, want NO entries — the container already exceeds the pin and must not be "+
			"shrunk to meet it", plan.Grow)
	}
	if plan.TotalTlcDriveCores != 8 {
		t.Errorf("TotalTlcDriveCores = %d, want 8 (the existing container's own core count, never reduced "+
			"to the lowered pin)", plan.TotalTlcDriveCores)
	}
}

// TestPlanAutoFullDrives_Growth_ExistingCoresMakePlanInfeasible_ReportedClearly: an existing container's
// core count is a floor the planner cannot go under, so when the compute those cores require does not fit,
// the plan is infeasible and says so — it does not quietly shrink the running container to fit.
func TestPlanAutoFullDrives_Growth_ExistingCoresMakePlanInfeasible_ReportedClearly(t *testing.T) {
	cons := testCons()
	cons.ComputeHugepagesTlcRatio = 1024
	const bigFree = 1 << 28

	existing := []ExistingContainer{
		{Name: "drive-n1", Node: "n1", FDValue: "fdA", TlcGiB: 8 * 5120, NumCores: 8, NumDrives: 8},
	}
	inv := []NodeCapacity{
		{
			NodeName: "n1", FDValue: "fdA",
			OwnDriveCapacitiesGiB: uniformDrives(8, 5120),
			DriveCapacitiesGiB:    uniformDrives(2, 5120),
			TlcGiB:                2 * 5120,
			AllocatableCPU:        1000, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		},
		tightNode("c1", 0, 64, 50000), // short of what the resulting compute container needs
	}
	computeNodes := computeNodeSet("c1")

	plan := PlanAutoFullDrives(AutoFullDrivesDesired{}, existing, nil, inv, computeNodes, cons)

	if plan.Infeasible == "" {
		t.Fatalf("expected infeasible (compute cannot host the existing container's drive cores), got: %+v", plan)
	}
	if len(plan.ComputeLayout) != 0 {
		t.Errorf("infeasible plan must carry no compute layout, got %+v", plan.ComputeLayout)
	}
	if plan.DriveSizing == nil {
		t.Fatalf("DriveSizing is nil")
	}
	if !strings.Contains(plan.DriveSizing.Reason, "infeasible") {
		t.Errorf("DriveSizing.Reason = %q, want it to state the plan is infeasible", plan.DriveSizing.Reason)
	}
}

// TestPlanAutoFullDrives_Growth_PinnedCores_GrowthCoresUntouched verifies that when desired.DriveCores is
// pinned, growing an existing container onto newly-freed drives never recomputes its core count — NewCores
// stays at the pin, only NewNumDrives/NewTlcGiB grow. The pin (4) also doubles as a drive ceiling: existing
// starts below it (2 own drives) and growth may only walk up to 4 total.
func TestPlanAutoFullDrives_Growth_PinnedCores_GrowthCoresUntouched(t *testing.T) {
	cons := testCons()
	const bigFree = 1 << 28

	existing := []ExistingContainer{
		{Name: "drive-n1", Node: "n1", FDValue: "fdA", TlcGiB: 2 * 5120, NumCores: 4, NumDrives: 2},
	}
	inv := []NodeCapacity{
		{
			NodeName: "n1", FDValue: "fdA",
			OwnDriveCapacitiesGiB: uniformDrives(2, 5120),
			DriveCapacitiesGiB:    uniformDrives(3, 5120), // 3 more free drives just showed up on this node
			TlcGiB:                3 * 5120,
			AllocatableCPU:        100, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		},
		tightNode("c1", 0, 64, bigFree),
	}
	computeNodes := computeNodeSet("c1")
	desired := AutoFullDrivesDesired{DriveCores: 4}

	plan := PlanAutoFullDrives(desired, existing, nil, inv, computeNodes, cons)

	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Grow) != 1 {
		t.Fatalf("Grow = %+v, want exactly 1 entry (the node gained free drives)", plan.Grow)
	}
	g := plan.Grow[0]
	if g.NewNumDrives != 5 || g.NewTlcGiB != 5*5120 {
		t.Errorf("Grow[0] = %+v, want NewNumDrives=5 NewTlcGiB=%d — ALL of the node's own 2 plus 3 newly "+
			"freed drives are claimed; the pin bounds cores, not drives", g, 5*5120)
	}
	if g.NewCores != 4 {
		t.Errorf("Grow[0].NewCores = %d, want 4 (pinned — must NOT be recomputed from the grown capacity)", g.NewCores)
	}
}

// Per-node (not global-min) compute headroom: reuses the ChargesDeltaAgainstComputeHeadroom n1 fixture
// (post-growth headroom zeroed) plus a healthy n2; a single-container layout needs only one fitting node, so
// it skips n1 and lands on n2. hp(t) shorthand: ComputeHugepagesTlcRatio=1024, 1 core/drive -> hp(t)=6820*t.
func TestPlanAutoFullDrives_ComputeLayout_FallsBackToHealthyNodeDespiteZeroHeadroomNode(t *testing.T) {
	cons := testCons()
	const bigFree = 1 << 28

	inv := []NodeCapacity{
		{ // 1 own drive + 5 free. Growing to all 6 costs a delta of (6+1)-(1+1) = 5 of its 6 CPU, leaving
			// 1 — exactly 0 data cores once a new container's management core is reserved.
			NodeName: "n1", FDValue: "fdA",
			DriveCapacitiesGiB: uniformDrives(5, 1000), TlcGiB: 5000,
			OwnDriveCapacitiesGiB: uniformDrives(1, 1000),
			AllocatableCPU:        6, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		},
		{ // healthy, drive-free, compute-only node with ample CPU/hugepages/memory.
			NodeName: "n2", FDValue: "fdB",
			AllocatableCPU: 100, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		},
	}
	existingDrives := []ExistingContainer{
		{Name: "drive-n1", Node: "n1", FDValue: "fdA", TlcGiB: 1000, NumCores: 1, NumDrives: 1},
	}
	computeNodes := map[string]bool{"n1": true, "n2": true}
	desired := AutoFullDrivesDesired{} // auto-derive, so real per-node headroom fully governs sizing/placement

	plan := PlanAutoFullDrives(desired, existingDrives, nil, inv, computeNodes, cons)

	// n1 claims every drive it has; the point of the fixture is what that leaves for compute.
	if len(plan.Grow) != 1 {
		t.Fatalf("expected exactly 1 Grow entry, got %d: %+v", len(plan.Grow), plan.Grow)
	}
	if got := plan.Grow[0]; got.NewNumDrives != 6 || got.NewTlcGiB != 6000 || got.NewCores != 6 {
		t.Fatalf("Grow entry = %+v, want NewNumDrives=6 NewTlcGiB=6000 NewCores=6", got)
	}
	if plan.TotalTlcDriveCores != 6 {
		t.Fatalf("TotalTlcDriveCores = %d, want 6 (post-growth)", plan.TotalTlcDriveCores)
	}

	// n1's 0 real headroom must not drag down the plan-wide compute headroom; n2's real headroom must
	// carry the plan on its own.
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s (a healthy second node should let the plan succeed despite "+
			"n1 correctly reporting 0 compute headroom)", plan.Infeasible)
	}
	if len(plan.ComputeLayout) != 1 {
		t.Fatalf("ComputeLayout = %+v, want exactly 1 entry", plan.ComputeLayout)
	}
	if plan.ComputeLayout[0].Node != "n2" {
		t.Errorf("compute container placed on %q, want %q (n1 has 0 real headroom post growth-delta-charge "+
			"and must be excluded, not merely deprioritized)", plan.ComputeLayout[0].Node, "n2")
	}
}

// A node hosting a mid-deletion drive container (HasDeletingDriveContainer=true) must not get a second one
// via the create path, even though inventory still shows its drives as allocated.
func TestPlanAutoFullDrives_SkipsNodeWithDeletingDriveContainer(t *testing.T) {
	cons := testCons()
	const bigFree = 1 << 28

	nc := NodeCapacity{
		NodeName: "n1", FDValue: "fdA", DriveCapacitiesGiB: uniformDrives(3, 1000), TlcGiB: 3000,
		AllocatableCPU: 100, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		HasDeletingDriveContainer: true,
	}

	// No existingDrives entry for n1 — mirrors ExistingDrives already filtering out the deleting container.
	plan := singleNodeAutoFullDrives(nc, AutoFullDrivesDesired{}, cons)

	if len(plan.Create) != 0 {
		t.Fatalf("expected no Create entry (node still hosts a mid-deletion drive container), got %+v", plan.Create)
	}
	if len(plan.Grow) != 0 {
		t.Fatalf("expected no Grow entry (no existing container in existingByNode to grow), got %+v", plan.Grow)
	}
	joined := strings.Join(WarningMessages(plan.Warnings), " | ")
	if !strings.Contains(joined, "n1") || !strings.Contains(joined, "still being deleted") {
		t.Errorf("expected a warning naming n1 and explaining the skip, got %q", joined)
	}
}

// A deleting node's drives must not inflate the compute-hugepages numerator. Compares plans with and
// without a HasDeletingDriveContainer node present; any difference means its would-be capacity leaked in.
func TestPlanAutoFullDrives_DeletingNode_ContributesZeroToComputeSizing(t *testing.T) {
	cons := testCons()
	cons.ComputeHugepagesTlcRatio = 1024
	// Isolate from the compute:drive ratio with the 1:1 floor.
	cons.FullDrivesComputeToDriveCoreRatio = 0
	const bigFree = 1 << 28

	existingD1 := []ExistingContainer{
		{Name: "drv-d1", Node: "d1", FDValue: "fdD1", TlcGiB: 0, NumCores: 10, NumDrives: 10},
	}
	d1 := NodeCapacity{
		NodeName: "d1", FDValue: "fdD1", OwnDriveCapacitiesGiB: uniformDrives(10, 5120),
		AllocatableCPU: 1000, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
	}
	c1 := NodeCapacity{NodeName: "c1", FDValue: "fdC1", AllocatableCPU: 1000, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree}
	deleting := NodeCapacity{
		NodeName: "n1", FDValue: "fdN1", DriveCapacitiesGiB: uniformDrives(3, 1000), TlcGiB: 3000,
		AllocatableCPU: 100, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		HasDeletingDriveContainer: true,
	}

	without := PlanAutoFullDrives(AutoFullDrivesDesired{}, existingD1, nil, []NodeCapacity{d1, c1}, computeNodeSet("c1"), cons)
	with := PlanAutoFullDrives(AutoFullDrivesDesired{}, existingD1, nil, []NodeCapacity{d1, c1, deleting}, computeNodeSet("c1"), cons)

	if without.Infeasible != "" || with.Infeasible != "" {
		t.Fatalf("unexpected infeasible: without=%q with=%q", without.Infeasible, with.Infeasible)
	}
	if len(without.ComputeLayout) != 1 || len(with.ComputeLayout) != 1 {
		t.Fatalf("ComputeLayout: without=%+v with=%+v, want exactly 1 entry each", without.ComputeLayout, with.ComputeLayout)
	}
	// d1 alone: 51200 GiB TLC / 10 cores -> 68200 MiB. If n1's 3000 GiB / 3 cores leaked in, this would be
	// 54200 GiB / 13 cores -> a different figure.
	if got := with.ComputeLayout[0]; got != without.ComputeLayout[0] {
		t.Errorf("ComputeLayout with the deleting node present = %+v, want identical to without it = %+v",
			got, without.ComputeLayout[0])
	}
	if got := with.ComputeLayout[0]; got.NumCores != 10 || got.HugepagesMiB != 68200 {
		t.Errorf("ComputeLayout[0] = %+v, want {NumCores: 10, HugepagesMiB: 68200} — d1's capacity only", got)
	}
}

// A cordoned node already hosting this cluster's drive container keeps that capacity counted like an
// eligible steady-state node; a different ineligible node with free signed drives but no container gets none.
func TestPlanAutoFullDrives_IneligibleNode_ExistingCapacityCountedNoNewContainer(t *testing.T) {
	cons := testCons()
	// testCons floors ComputeLayout at 3000*cores when ComputeHugepagesTlcRatio=0; raise it so the
	// assertion below actually depends on n1's capacity being counted and n2's not.
	cons.ComputeHugepagesTlcRatio = 256
	const bigFree = 1 << 28

	existing := []ExistingContainer{
		{Name: "drive-n1", Node: "n1", FDValue: "fdA", TlcGiB: 0, NumCores: 4, NumDrives: 4},
	}
	inv := []NodeCapacity{
		{
			// n1 is cordoned but already hosts drive-n1 — its capacity must stay in the plan.
			NodeName: "n1", FDValue: "fdA",
			OwnDriveCapacitiesGiB: uniformDrives(4, 1000),
			AllocatableCPU:        1000, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
			IneligibleReason: "cordoned",
		},
		{
			// n2 is ineligible too and has free signed drives, but no container of ours yet — it must not get one.
			NodeName: "n2", FDValue: "fdB",
			DriveCapacitiesGiB: uniformDrives(4, 1000),
			AllocatableCPU:     1000, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
			IneligibleReason: "not ready",
		},
	}
	computeNodes := computeNodeSet("n1", "n2")

	plan := PlanAutoFullDrives(AutoFullDrivesDesired{}, existing, nil, inv, computeNodes, cons)

	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Create) != 0 {
		t.Fatalf("Create = %+v, want none — neither ineligible node may receive a new container, "+
			"regardless of n2's free drives", plan.Create)
	}
	if len(plan.Grow) != 0 {
		t.Fatalf("Grow = %+v, want none — n1's existing container is already at steady state", plan.Grow)
	}
	if plan.TotalTlcDriveCores != 4 {
		t.Fatalf("TotalTlcDriveCores = %d, want 4 — n1's existing container's cores must still be counted "+
			"even though the node is cordoned", plan.TotalTlcDriveCores)
	}
	if plan.DriveSizing == nil || plan.DriveSizing.DrivesTaken != 4 || plan.DriveSizing.TlcGiBTaken != 4000 {
		t.Fatalf("DriveSizing = %+v, want DrivesTaken=4 TlcGiBTaken=4000 — n1's existing capacity counted, "+
			"n2's free drives never claimed since it has no container and may not start one", plan.DriveSizing)
	}
	// n2's 4 free signed drives swell the fleet denominator (so "N of M" reflects every signed drive) but
	// never the numerator above: DrivesAvailable/TlcGiBAvailable = n1's 4 + n2's 4, DrivesTaken/TlcGiBTaken
	// stay at n1's 4 alone.
	if plan.DriveSizing.DrivesAvailable != 8 || plan.DriveSizing.TlcGiBAvailable != 8000 {
		t.Fatalf("DriveSizing = %+v, want DrivesAvailable=8 TlcGiBAvailable=8000 — n2's free drives must "+
			"swell the denominator even though they were never claimed", plan.DriveSizing)
	}
	found := false
	for _, w := range plan.Warnings {
		if w.Kind == WarningKindNodeIneligible {
			found = true
			if !strings.Contains(w.Message, "n2") || !strings.Contains(w.Message, "not ready") {
				t.Errorf("NodeIneligible warning message = %q, want it to name n2 and its reason verbatim (not ready)", w.Message)
			}
		}
	}
	if !found {
		t.Fatalf("Warnings = %+v, want a WarningKindNodeIneligible warning naming n2", plan.Warnings)
	}
	// DriveSizing.TlcGiBTaken is the exact number PlanAutoFullDrives hands to compute as its capacity
	// numerator, but pin it there too: 29600 only comes out of n1's 4000 GiB alone. If n2's free drives (which
	// must never be counted, since n2 has no container) leaked in, the total would be 8000 and this container
	// would come out at 16 cores / 59200 MiB instead.
	if len(plan.ComputeLayout) != 1 {
		t.Fatalf("ComputeLayout = %+v, want exactly 1 entry", plan.ComputeLayout)
	}
	if c := plan.ComputeLayout[0]; c.NumCores != 8 || c.HugepagesMiB != 29600 {
		t.Errorf("ComputeLayout[0] = %+v, want {NumCores: 8, HugepagesMiB: 29600}", c)
	}
}

// Regression test: TlcGiB is always 0 for an already-running full-drives container, so
// tlcDriveCoresForContainer must treat a real assigned core count as authoritative before the tlcGiB<=0
// short-circuit, or steady-state containers would contribute 0 to TotalTlcDriveCores.
//
// Fixture: 2-node steady state, TlcGiB=0/QlcGiB=0 but assigned cores 6 and 4 -> TotalTlcDriveCores=10, 1:1
// auto-derive lands on count=1, cores=10.
func TestPlanAutoFullDrives_SteadyState_ExistingDriveCoresCountedTowardCompute(t *testing.T) {
	cons := testCons()
	const bigFree = 1 << 28

	existing := []ExistingContainer{
		{Name: "drive-n1", Node: "n1", FDValue: "fdA", TlcGiB: 0, QlcGiB: 0, NumCores: 6, NumDrives: 6},
		{Name: "drive-n2", Node: "n2", FDValue: "fdB", TlcGiB: 0, QlcGiB: 0, NumCores: 4, NumDrives: 4},
	}
	inv := []NodeCapacity{
		{
			NodeName: "n1", FDValue: "fdA",
			OwnDriveCapacitiesGiB: uniformDrives(6, 5120),
			DriveCapacitiesGiB:    nil, // steady state: nothing new since creation
			TlcGiB:                0,
			AllocatableCPU:        1000, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		},
		{
			NodeName: "n2", FDValue: "fdB",
			OwnDriveCapacitiesGiB: uniformDrives(4, 5120),
			DriveCapacitiesGiB:    nil,
			TlcGiB:                0,
			AllocatableCPU:        1000, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		},
	}
	computeNodes := computeNodeSet("n1", "n2") // hyperconverged

	plan := PlanAutoFullDrives(AutoFullDrivesDesired{}, existing, nil, inv, computeNodes, cons)

	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s (steady state with ample compute headroom should be feasible)", plan.Infeasible)
	}
	// Pure steady state: no Create, no Grow (own == free-total already on both nodes).
	if len(plan.Create) != 0 {
		t.Fatalf("Create = %+v, want none (steady state: nothing new to place)", plan.Create)
	}
	if len(plan.Grow) != 0 {
		t.Fatalf("Grow = %+v, want none (steady state: own == free-total on both nodes already)", plan.Grow)
	}

	const wantCores = 6 + 4
	if plan.TotalTlcDriveCores != wantCores {
		t.Fatalf("TotalTlcDriveCores = %d, want %d (sum of the existing containers' NumCores, correctly "+
			"counted despite TlcGiB=0 on both — this is the Defect B regression: a broken branch order in "+
			"tlcDriveCoresForContainer collapses this to 0)", plan.TotalTlcDriveCores, wantCores)
	}
	if plan.TotalTlcDriveCores == 0 {
		t.Fatalf("TotalTlcDriveCores must never be 0 here: %d existing drive containers with real assigned "+
			"cores exist (n1=6, n2=4)", len(existing))
	}

	if plan.ComputeContainers == 0 || plan.ComputeCores == 0 {
		t.Fatalf("ComputeContainers/ComputeCores = %d/%d, want both > 0 (real drive cores exist, so real "+
			"compute must be sized — a phantom silent-zero result here is exactly Defect B)",
			plan.ComputeContainers, plan.ComputeCores)
	}
	if plan.ComputeContainers*plan.ComputeCores < wantCores {
		t.Fatalf("ComputeContainers*ComputeCores = %d*%d = %d, want >= %d (1:1 compute:drive-core ratio)",
			plan.ComputeContainers, plan.ComputeCores, plan.ComputeContainers*plan.ComputeCores, wantCores)
	}
	if len(plan.ComputeLayout) == 0 {
		t.Fatalf("ComputeLayout is empty, want a non-empty per-container layout backing ComputeContainers=%d",
			plan.ComputeContainers)
	}
	sumLayoutCores := 0
	for _, c := range plan.ComputeLayout {
		sumLayoutCores += c.NumCores
	}
	if sumLayoutCores < wantCores {
		t.Errorf("sum(ComputeLayout[*].NumCores) = %d, want >= %d (1:1 ratio against the real TLC drive cores)",
			sumLayoutCores, wantCores)
	}

	// Invariant: a feasible plan must never report an empty compute layout while real existing drive
	// containers are in play.
	if plan.Infeasible == "" && len(plan.ComputeLayout) == 0 && len(existing) > 0 {
		t.Fatalf("invariant violated: Infeasible==\"\" && ComputeLayout empty && %d existing drive "+
			"container(s) present — this is the exact Defect B failure mode (a feasible-looking plan that "+
			"silently sizes zero compute)", len(existing))
	}
}

// TestPlanAutoFullDrives_Growth_NodeThatCannotFitNewCoresFailsWholePlan: a node whose container must grow
// cores to cover newly signed drives, and cannot fit them, makes the whole plan infeasible — it is not
// quietly held at its old size while the rest of the fleet moves on.
func TestPlanAutoFullDrives_Growth_NodeThatCannotFitNewCoresFailsWholePlan(t *testing.T) {
	cons := testCons()
	const bigFree = 1 << 28

	existing := []ExistingContainer{
		{Name: "drive-tight", Node: "tight", FDValue: "fdTight", TlcGiB: 2 * 5120, NumCores: 2, NumDrives: 2},
		{Name: "drive-roomy", Node: "roomy", FDValue: "fdRoomy", TlcGiB: 2 * 5120, NumCores: 2, NumDrives: 2},
	}
	inv := []NodeCapacity{
		{
			NodeName: "tight", FDValue: "fdTight",
			OwnDriveCapacitiesGiB: uniformDrives(2, 5120),
			DriveCapacitiesGiB:    uniformDrives(2, 5120),
			TlcGiB:                2 * 5120,
			// Just under the 1600 MiB/core delta needed to grow past 2 cores; CPU/memory ample so
			// hugepages is unambiguously the sole binding resource.
			AllocatableCPU: 1000, AvailableHugepagesMiB: 1000, AvailableMemoryMiB: bigFree,
		},
		{
			NodeName: "roomy", FDValue: "fdRoomy",
			OwnDriveCapacitiesGiB: uniformDrives(2, 5120),
			DriveCapacitiesGiB:    uniformDrives(2, 5120),
			TlcGiB:                2 * 5120,
			AllocatableCPU:        1000, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		},
		tightNode("compute1", 0, 1000, 1<<28),
	}
	computeNodes := computeNodeSet("compute1")

	plan := PlanAutoFullDrives(AutoFullDrivesDesired{}, existing, nil, inv, computeNodes, cons)

	if plan.Infeasible == "" {
		t.Fatalf("expected the whole plan to be infeasible — tight cannot fit the 4 cores its 4 drives "+
			"imply; got feasible with Grow=%+v", plan.Grow)
	}
	// Grow entries for nodes that fit stay on the plan as diagnostics (the CLI renders them "partial, not
	// applied"), but nothing downstream applies a plan carrying an Infeasible reason.
	for _, g := range plan.Grow {
		if g.Name == "drive-tight" {
			t.Errorf("Grow contains an entry for the node that cannot fit: %+v", g)
		}
	}
	if len(plan.ComputeLayout) != 0 {
		t.Errorf("infeasible plan must carry no compute layout, got %+v", plan.ComputeLayout)
	}
	if plan.Infeasibility == nil {
		t.Fatalf("Infeasibility report is nil")
	}
	var named bool
	for _, r := range plan.Infeasibility.RejectedNodes {
		if r.Node == "tight" {
			named = true
			if r.Binding != "hugepages" {
				t.Errorf("RejectedNodes[tight].Binding = %q, want %q", r.Binding, "hugepages")
			}
			if r.Unit != "MiB hugepages" {
				t.Errorf("RejectedNodes[tight].Unit = %q, want %q", r.Unit, "MiB hugepages")
			}
		}
		if r.Node == "roomy" {
			t.Errorf("roomy must not be rejected — it fits its own drives fine: %+v", r)
		}
	}
	if !named {
		t.Errorf("RejectedNodes = %+v, want tight named", plan.Infeasibility.RejectedNodes)
	}
}

// A drive container recorded with drives but zero cores is repaired by an ordinary cores-only growth
// rather than poisoning the plan.
func TestPlanAutoFullDrives_ExistingDriveContainerWithZeroCores_SelfHeals(t *testing.T) {
	cons := testCons()
	const bigFree = 1 << 28

	existing := []ExistingContainer{
		{Name: "drive-n1", Node: "n1", FDValue: "fdA", TlcGiB: 0, QlcGiB: 0, NumCores: 0, NumDrives: 2},
	}
	inv := []NodeCapacity{
		{
			NodeName: "n1", FDValue: "fdA",
			OwnDriveCapacitiesGiB: uniformDrives(2, 5120),
			DriveCapacitiesGiB:    nil,
			TlcGiB:                0,
			AllocatableCPU:        1000, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		},
	}
	computeNodes := computeNodeSet("n1")

	plan := PlanAutoFullDrives(AutoFullDrivesDesired{}, existing, nil, inv, computeNodes, cons)

	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s — a 0-core container is repairable, not a contradiction",
			plan.Infeasible)
	}
	if len(plan.Grow) != 1 {
		t.Fatalf("Grow = %+v, want exactly 1 entry repairing the 0-core container", plan.Grow)
	}
	if g := plan.Grow[0]; g.NewCores != 2 || g.NewNumDrives != 2 {
		t.Errorf("Grow[0] = %+v, want NewCores=2 NewNumDrives=2 (one core per drive it already holds)", g)
	}
	if plan.TotalTlcDriveCores != 2 {
		t.Errorf("TotalTlcDriveCores = %d, want 2 (the repaired core count)", plan.TotalTlcDriveCores)
	}
}

// deriveComputeLayout's scan only guarantees count*cores >= t (a ceiling), so it can overshoot t by "slack"
// when existingCores already equals t; a steady-state cluster whose frozen compute exactly satisfies the
// 1:1 core requirement must stay feasible, not flip to Infeasible on that overshoot.
//
// Fixture: 6 hyperconverged nodes, each with a frozen 12-core compute + 12-core drive container ->
// TotalTlcDriveCores==existingCores==t==72. AllocatableCPU=3/node -> coreHeadroom=15/node; scan lands on
// count=5,cores=15 (75, 3 cores of ceiling slack over t=72). Since existingCores(72)>=t(72), target pins
// back to existingCores, shortfall=0, plan feasible with the 6 frozen containers unchanged.
func TestPlanAutoFullDrives_SteadyState_FrozenComputeAlreadyCoveringShortfall_NoCeilingSlackPhantom(t *testing.T) {
	cons := testCons()
	cons.FullDrivesComputeToDriveCoreRatio = 0 // keep t at the 1:1 floor (72) matching existingCores; not a ratio test
	const bigFree = 1 << 28

	var existing []ExistingContainer
	var existingCompute []ExistingComputeContainer
	var inv []NodeCapacity
	var nodeNames []string
	for i := 1; i <= 6; i++ {
		node := fmt.Sprintf("n%d", i)
		nodeNames = append(nodeNames, node)
		existing = append(existing, ExistingContainer{
			Name: fmt.Sprintf("drive-%s", node), Node: node, FDValue: fmt.Sprintf("fd%d", i),
			TlcGiB: 61440, QlcGiB: 0, NumCores: 12, NumDrives: 12,
		})
		existingCompute = append(existingCompute, ExistingComputeContainer{
			Name: fmt.Sprintf("compute-%s", node), Node: node, NumCores: 12, HugepagesMiB: 19200,
		})
		inv = append(inv, NodeCapacity{
			NodeName: node, FDValue: fmt.Sprintf("fd%d", i),
			OwnDriveCapacitiesGiB: uniformDrives(12, 5120),
			DriveCapacitiesGiB:    nil, // fully saturated: no free drives left on any node
			TlcGiB:                0,
			AllocatableCPU:        3, // -> coreHeadroom == 15 once the frozen 12-core container's CPU is reclaimed
			AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		})
	}
	computeNodes := computeNodeSet(nodeNames...)

	plan := PlanAutoFullDrives(AutoFullDrivesDesired{}, existing, existingCompute, inv, computeNodes, cons)

	if plan.TotalTlcDriveCores != 72 {
		t.Fatalf("TotalTlcDriveCores = %d, want 72 (fixture precondition: 6 nodes x 12 cores each)",
			plan.TotalTlcDriveCores)
	}
	if len(plan.Create) != 0 || len(plan.Grow) != 0 {
		t.Fatalf("Create=%+v Grow=%+v, want both empty (fixture precondition: fully steady-state, "+
			"no free drives anywhere, no growth or creation in play)", plan.Create, plan.Grow)
	}

	if plan.Infeasible != "" {
		t.Fatalf("Infeasible = %q, want empty — a cluster whose frozen compute already exactly covers "+
			"its drive cores (existingCores == TotalTlcDriveCores == 72) is fully satisfied and must be "+
			"feasible; a phantom compute container manufactured purely from deriveComputeLayout's ceiling "+
			"slack (5x15=75 overshooting t=72 by 3) must never demand placement when there is no free "+
			"fitting compute node left (Defect C)", plan.Infeasible)
	}

	if plan.ComputeContainers != 6 {
		t.Errorf("ComputeContainers = %d, want 6 (the pre-existing frozen containers, with zero new ones "+
			"added for ceiling slack)", plan.ComputeContainers)
	}
	if len(plan.ComputeLayout) != 6 {
		t.Fatalf("len(ComputeLayout) = %d, want 6", len(plan.ComputeLayout))
	}
	for _, entry := range plan.ComputeLayout {
		if entry.NumCores != 12 {
			t.Errorf("ComputeLayout entry for node %q has NumCores = %d, want 12 — existing/frozen "+
				"compute must never be resized to the freshly re-derived cores value (15) that only "+
				"applies to newly-created containers", entry.Node, entry.NumCores)
		}
	}
	// Not asserting ComputeCores == 12: that's the auto-derived summary target and legitimately still reads 15.
}

// Guards the `existingCores > 0` conjunct in the suppression condition (desired.ComputeContainers == 0 &&
// existingCores > 0 && existingCores >= plan.TotalTlcDriveCores): ComputeCores pinned, existingCores==0,
// TotalTlcDriveCores==0 (compute-only bootstrap ahead of drives).
//
// Derivation: specCores branch gives cores=1, count=1. target=1; existingCores(0)>0 is false, so target
// stays at 1 -> shortfall=1, nNew=1.
func TestPlanAutoFullDrives_ComputeCoresPinned_BootstrapAheadOfDrives_NoExistingCompute_ContainerStillCreated(t *testing.T) {
	cons := testCons()
	const bigFree = 1 << 28

	inv := []NodeCapacity{
		{ // dedicated compute-only node: no drives, ample headroom
			NodeName: "cmp1", FDValue: "cmp1",
			AllocatableCPU: 100, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		},
	}
	computeNodes := computeNodeSet("cmp1")
	desired := AutoFullDrivesDesired{ComputeCores: 1} // ComputeContainers deliberately left unset (auto-derived)

	plan := PlanAutoFullDrives(desired, nil, nil, inv, computeNodes, cons)

	if plan.TotalTlcDriveCores != 0 {
		t.Fatalf("TotalTlcDriveCores = %d, want 0 (fixture precondition: no drive containers, no signed "+
			"drives at all)", plan.TotalTlcDriveCores)
	}
	if plan.Infeasible != "" {
		t.Fatalf("Infeasible = %q, want empty — a pinned ComputeCores with ample headroom on a dedicated "+
			"compute node must be satisfiable even with zero existing compute and zero drive cores",
			plan.Infeasible)
	}
	if len(plan.ComputeLayout) != 1 {
		t.Fatalf("len(ComputeLayout) = %d, want 1 — the pinned ComputeCores must still produce a compute "+
			"container even though existingCores == TotalTlcDriveCores == 0 (both legitimately zero here, "+
			"a compute-only bootstrap ahead of any signed drives): without the existingCores > 0 guard, "+
			"\"0 >= 0\" would wrongly suppress this pinned container down to zero", len(plan.ComputeLayout))
	}
	if got := plan.ComputeLayout[0]; got.Node != "cmp1" || got.NumCores != 1 {
		t.Errorf("ComputeLayout[0] = %+v, want Node=cmp1 NumCores=1", got)
	}
}

// deriveComputeLayout's sizing search must run over the placeable node set (pinned/frozen nodes excluded):
// a new container can only land on an unpinned node, so deriving cores off a pinned node's larger headroom
// would size a container no node could actually host.
//
// Fixture: 2 drive nodes, 15 cores each existing (t=30). c1 (AllocatableCPU=36) hosts a frozen compute
// container (NumCores=4) and is pinned; c2/c3/c4 (AllocatableCPU=11) are placeable. existingCores=4,
// deficit=26. placeable={c2,c3,c4}, coreHeadroom=10 each; n=3 needs ceil(26/3)=9<=10 -> count=3 @ cores=9,
// balanced-fill 9+9+8; total 4+9+9+8=30==t, feasible.
func TestPlanAutoFullDrives_ComputeLayout_PlaceableSet_ExcludesPinnedNodeHeadroomFromDerivation(t *testing.T) {
	cons := testCons()
	cons.FullDrivesComputeToDriveCoreRatio = 0 // keep t at the 1:1 floor (30) the arithmetic above is built on
	const bigFree = 1 << 28

	existingDrives := []ExistingContainer{
		{Name: "drv-d1", Node: "d1", FDValue: "fdD1", TlcGiB: 0, NumCores: 15, NumDrives: 15},
		{Name: "drv-d2", Node: "d2", FDValue: "fdD2", TlcGiB: 0, NumCores: 15, NumDrives: 15},
	}
	driveNode := func(name string) NodeCapacity {
		return NodeCapacity{
			NodeName: name, FDValue: "fd" + name,
			OwnDriveCapacitiesGiB: uniformDrives(15, 5120),
			TlcGiB:                0,
			AllocatableCPU:        1000, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		}
	}
	inv := []NodeCapacity{
		driveNode("d1"), driveNode("d2"),
		// c1 hosts the frozen existing compute container below and is pinned — its (large, reclaimed)
		// headroom must never feed the derivation search that only new containers on c2/c3/c4 can satisfy.
		{NodeName: "c1", FDValue: "fdC1", AllocatableCPU: 36, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree},
		{NodeName: "c2", FDValue: "fdC2", AllocatableCPU: 11, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree},
		{NodeName: "c3", FDValue: "fdC3", AllocatableCPU: 11, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree},
		{NodeName: "c4", FDValue: "fdC4", AllocatableCPU: 11, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree},
	}
	computeNodes := computeNodeSet("c1", "c2", "c3", "c4")
	existingCompute := []ExistingComputeContainer{{Name: "ec1", Node: "c1", NumCores: 4, HugepagesMiB: 12000}}

	plan := PlanAutoFullDrives(AutoFullDrivesDesired{}, existingDrives, existingCompute, inv, computeNodes, cons)

	if plan.TotalTlcDriveCores != 30 {
		t.Fatalf("TotalTlcDriveCores = %d, want 30 (fixture precondition: 2 drive nodes x 15 cores each)",
			plan.TotalTlcDriveCores)
	}
	if plan.Infeasible != "" {
		t.Fatalf("Infeasible = %q, want empty — c2/c3/c4 have ample real headroom for the 26-core deficit; "+
			"the old bug sized the derivation off pinned c1's headroom instead, leaving zero placeable "+
			"nodes for a container that size", plan.Infeasible)
	}

	total := 0
	var frozen *ComputeContainerSpec
	for i := range plan.ComputeLayout {
		entry := &plan.ComputeLayout[i]
		total += entry.NumCores
		if entry.Node == "c1" {
			frozen = entry
		}
	}
	if total < plan.TotalTlcDriveCores {
		t.Errorf("total compute layout cores = %d, want >= TotalTlcDriveCores (%d)", total, plan.TotalTlcDriveCores)
	}
	if frozen == nil {
		t.Fatalf("ComputeLayout has no entry for c1 — the frozen existing compute container must be preserved")
	}
	if frozen.NumCores != 4 {
		t.Errorf("frozen c1 entry NumCores = %d, want 4 — existing/frozen compute must never be resized to "+
			"the freshly re-derived cores value that only applies to newly-created containers", frozen.NumCores)
	}
}

// TestPlanAutoFullDrives_ComputeHugepages_ExistingContainerCapacityFeedsSizing pins down that an existing
// full-drives drive container's realized capacity feeds totalTlcGiB (and thus compute hugepages sizing)
// even though TlcGiB is structurally 0 for every full-drives container: when finalPoolCap yields 0, it
// falls back to summing the node's OwnDriveCapacitiesGiB.
//
// Fixture: node d1 with 10 owned 5120 GiB drives (51200 GiB realized, TlcGiB reports 0), existing container
// NumCores=10 (t=10), ComputeHugepagesTlcRatio=1024 so capacityBased(51200) dominates the per-core floor:
// hp=max(51200+1700*cores, 3000*cores)=68200, far above the capacity-blind floor of 30000.
func TestPlanAutoFullDrives_ComputeHugepages_ExistingContainerCapacityFeedsSizing(t *testing.T) {
	cons := testCons()
	cons.ComputeHugepagesTlcRatio = 1024
	// At the default 2.0 ratio, 10 drive cores would need 2 compute containers, but only one
	// compute-eligible node exists here — use the 1:1 floor so "c1" alone can host the deficit.
	cons.FullDrivesComputeToDriveCoreRatio = 0
	const bigFree = 1 << 28

	existingDrives := []ExistingContainer{
		{Name: "drv-d1", Node: "d1", FDValue: "fdD1", TlcGiB: 0, NumCores: 10, NumDrives: 10},
	}
	inv := []NodeCapacity{
		{
			NodeName: "d1", FDValue: "fdD1",
			OwnDriveCapacitiesGiB: uniformDrives(10, 5120), // 51200 GiB realized; TlcGiB itself reports 0
			TlcGiB:                0,
			AllocatableCPU:        1000, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		},
		{NodeName: "c1", FDValue: "fdC1", AllocatableCPU: 1000, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree},
	}

	plan := PlanAutoFullDrives(AutoFullDrivesDesired{}, existingDrives, nil, inv, computeNodeSet("c1"), cons)

	if plan.Infeasible != "" {
		t.Fatalf("Infeasible = %q, want empty", plan.Infeasible)
	}
	if len(plan.ComputeLayout) == 0 {
		t.Fatalf("ComputeLayout is empty, want one derived compute container")
	}
	got := plan.ComputeLayout[0].HugepagesMiB
	floor := 3000 * plan.ComputeLayout[0].NumCores
	if got <= floor {
		t.Errorf("ComputeLayout[0].HugepagesMiB = %d, want strictly > the capacity-blind floor %d "+
			"(3000*%d cores) — the existing container's 51200 GiB of realized capacity (OwnDriveCapacitiesGiB) "+
			"must feed totalTlcGiB even though TlcGiB itself is structurally 0 for a full-drives container",
			got, floor, plan.ComputeLayout[0].NumCores)
	}
}

// An ExistingComputeContainer naming no node must not contribute its cores to existingCores or appear in
// the emitted layout — it is counted only when positionally placeable (node present in inventory).
//
// Fixture: t=30, one real 10-core compute, one node-less 20-core record. Buggy: existingCores=10+20=30>=30
// -> deficit 0, no compute planned despite only 10 real cores. Correct: only the real 10 count, so a
// 20-core deficit lands on the spare node.
func TestPlanAutoFullDrives_ExistingCompute_NodelessRecordNotCountedTowardRequirement(t *testing.T) {
	cons := testCons()
	// Isolate from the compute:drive ratio (default 2.0 would need 60 cores for 3 containers, more
	// than the single spare node can absorb) with the 1:1 floor.
	cons.FullDrivesComputeToDriveCoreRatio = 0
	const bigFree = 1 << 28

	existingDrives := []ExistingContainer{
		{Name: "drv-d1", Node: "d1", FDValue: "fdD1", TlcGiB: 0, NumCores: 30, NumDrives: 30},
	}
	inv := []NodeCapacity{
		{
			NodeName: "d1", FDValue: "fdD1",
			OwnDriveCapacitiesGiB: uniformDrives(30, 5120),
			TlcGiB:                0,
			AllocatableCPU:        1000, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		},
		{NodeName: "c1", FDValue: "fdC1", AllocatableCPU: 1000, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree},
		{NodeName: "c2", FDValue: "fdC2", AllocatableCPU: 1000, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree},
	}
	existingCompute := []ExistingComputeContainer{
		{Name: "ec-real", Node: "c1", NumCores: 10, HugepagesMiB: 30000},
		{Name: "ec-nodeless", Node: "", NumCores: 20, HugepagesMiB: 60000},
	}

	plan := PlanAutoFullDrives(AutoFullDrivesDesired{}, existingDrives, existingCompute, inv, computeNodeSet("c1", "c2"), cons)
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	totalCores := 0
	for _, l := range plan.ComputeLayout {
		totalCores += l.NumCores
		if l.Node == "" {
			t.Errorf("ComputeLayout contains a Node:\"\" entry (%+v) — an unplaceable record must never be "+
				"emitted as part of the layout", l)
		}
	}
	for _, n := range plan.ComputeNodes {
		if n == "" {
			t.Errorf("ComputeNodes contains an empty node name: %v", plan.ComputeNodes)
		}
	}
	if totalCores < plan.TotalTlcDriveCores {
		t.Errorf("layout supplies %d compute core(s) against TotalTlcDriveCores = %d — the node-less "+
			"record's 20 cores were counted as if real, suppressing the genuine deficit (audit F6)",
			totalCores, plan.TotalTlcDriveCores)
	}
}

// An existing compute container on a node absent from inventory must not be counted either (e.g. after
// narrowing spec.roleNodeSelector.compute) — nor treated as pinned with a blank FD, which would seed
// coveredFDs with "" and leave the container's real FD unmarked, defeating orderNodesByFDSpread.
//
// Fixture: t=20, existing 20-core compute sits on "gone" (not in inventory) -> contributes nothing, so the
// full 20-core requirement is planned onto c1.
func TestPlanAutoFullDrives_ExistingCompute_NodeOutsideInventoryNotCounted(t *testing.T) {
	cons := testCons()
	// Isolate from ratio/per-container-cap effects (orthogonal to this test) so the single node can
	// host the full 20-core requirement.
	cons.FullDrivesComputeToDriveCoreRatio = 0
	cons.MaxCoresPerContainer = 1000
	const bigFree = 1 << 28

	existingDrives := []ExistingContainer{
		{Name: "drv-d1", Node: "d1", FDValue: "fdD1", TlcGiB: 0, NumCores: 20, NumDrives: 20},
	}
	inv := []NodeCapacity{
		{
			NodeName: "d1", FDValue: "fdD1",
			OwnDriveCapacitiesGiB: uniformDrives(20, 5120),
			TlcGiB:                0,
			AllocatableCPU:        1000, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		},
		{NodeName: "c1", FDValue: "fdC1", AllocatableCPU: 1000, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree},
	}
	existingCompute := []ExistingComputeContainer{
		{Name: "ec-gone", Node: "gone", NumCores: 20, HugepagesMiB: 60000}, // "gone" is not in inv
	}

	plan := PlanAutoFullDrives(AutoFullDrivesDesired{}, existingDrives, existingCompute, inv, computeNodeSet("c1"), cons)
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	totalCores := 0
	for _, l := range plan.ComputeLayout {
		totalCores += l.NumCores
		if l.Node == "gone" {
			t.Errorf("ComputeLayout includes the out-of-inventory node (%+v) — it can be neither frozen "+
				"nor placed here, so it must not appear", l)
		}
	}
	if totalCores < plan.TotalTlcDriveCores {
		t.Errorf("layout supplies %d compute core(s) against TotalTlcDriveCores = %d — the "+
			"out-of-inventory container's cores were counted as if placeable (audit F6)",
			totalCores, plan.TotalTlcDriveCores)
	}
}

// A fully-converged node (all full drives owned, zero free) must still feed compute sizing: the compute
// hugepages fallback resolves realized capacity via OwnDriveCapacitiesGiB (full-drives containers report
// TlcGiB==0), so a node dropped from inventory when it has no free drives would collapse totalTlcGiB.
//
// Fixture: d1 owns 10x5120 GiB (51200 GiB realized), zero free drives, hosts a 10-core drive container
// (TlcGiB=0). ComputeHugepagesTlcRatio=1024 makes capacityBased==51200 exactly. Expected hugepages:
// max(51200+1700*10, 3000*10)=68200; a regression falls to the 3000*cores floor (30000).
func TestPlanAutoFullDrives_FullyConvergedNode_StillFeedsComputeSizing(t *testing.T) {
	cons := testCons()
	cons.ComputeHugepagesTlcRatio = 1024
	// This test is about capacity feeding compute hugepages sizing, not the compute:drive-core ratio.
	// At the default 2.0 full-drives ratio, the 10 drive cores would need 20 compute cores — 2
	// containers against the fixture's single compute node, making the plan infeasible before
	// hugepages sizing is reached. Isolate with the 1:1 floor so "c1" can host the whole thing.
	cons.FullDrivesComputeToDriveCoreRatio = 0
	const bigFree = 1 << 28

	existingDrives := []ExistingContainer{
		{Name: "drv-d1", Node: "d1", FDValue: "fdD1", TlcGiB: 0, NumCores: 10, NumDrives: 10},
	}
	inv := []NodeCapacity{
		{
			NodeName: "d1", FDValue: "fdD1",
			OwnDriveCapacitiesGiB: uniformDrives(10, 5120), // 100% owned
			DriveCapacitiesGiB:    nil,                     // ZERO free drives — fully converged
			TlcGiB:                0,
			AllocatableCPU:        1000, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		},
		{NodeName: "c1", FDValue: "fdC1", AllocatableCPU: 1000, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree},
	}

	plan := PlanAutoFullDrives(AutoFullDrivesDesired{}, existingDrives, nil, inv, computeNodeSet("c1"), cons)
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Grow) != 0 {
		t.Errorf("Grow = %+v, want none — a fully-converged node has nothing to grow onto", plan.Grow)
	}
	if len(plan.Create) != 0 {
		t.Errorf("Create = %+v, want none — the node already hosts this cluster's container", plan.Create)
	}
	if plan.TotalTlcDriveCores != 10 {
		t.Fatalf("TotalTlcDriveCores = %d, want 10", plan.TotalTlcDriveCores)
	}
	if len(plan.ComputeLayout) != 1 {
		t.Fatalf("ComputeLayout = %+v, want exactly 1 entry", plan.ComputeLayout)
	}
	got := plan.ComputeLayout[0]
	floor := 3000 * got.NumCores
	if got.HugepagesMiB <= floor {
		t.Errorf("compute hugepages = %d, want > the capacity-blind floor of %d (3000*%d) — the fully-owned "+
			"node's 51200 GiB did not reach totalTlcGiB, so either the inventory dropped the node or the F4 "+
			"OwnDriveCapacitiesGiB fallback regressed", got.HugepagesMiB, floor, got.NumCores)
	}
	if got.HugepagesMiB != 68200 {
		t.Errorf("compute hugepages = %d, want 68200 (max(51200 + 1700*10, 3000*10))", got.HugepagesMiB)
	}
}

// Compute hugepages must come out identical whether a node's drives sit in OwnDriveCapacitiesGiB (claimed)
// or DriveCapacitiesGiB (created but not yet claimed) — the numerator is what the drive walk sized the
// container to, not a claim-dependent readback of the node's capacity split.
//
// Fixture: node d1's existing 10-core/10-drive container, 5120 GiB each (51200 GiB total). Cores/ratio
// chosen so capacityBased(51200)+1700*cores=68200 clears the 3000*cores=30000 floor — a regression that
// falls back to 0 claimed capacity would show up as the floor instead.
func TestPlanAutoFullDrives_ComputeHugepages_ClaimedVsUnclaimedAgree(t *testing.T) {
	cons := testCons()
	cons.ComputeHugepagesTlcRatio = 1024
	// Isolate from the compute:drive ratio with the 1:1 floor — the single spare compute node can then
	// host the whole deficit.
	cons.FullDrivesComputeToDriveCoreRatio = 0
	const bigFree = 1 << 28

	existingDrives := []ExistingContainer{
		// TlcGiB: 0 is structural for a full-drives container regardless of claiming — see file header.
		{Name: "drv-d1", Node: "d1", FDValue: "fdD1", TlcGiB: 0, NumCores: 10, NumDrives: 10},
	}
	computeInv := NodeCapacity{
		NodeName: "c1", FDValue: "fdC1", AllocatableCPU: 1000, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
	}

	claimed := NodeCapacity{
		NodeName:              "d1",
		FDValue:               "fdD1",
		OwnDriveCapacitiesGiB: uniformDrives(10, 5120), // 51200 GiB claimed
		TlcGiB:                0,
		AllocatableCPU:        1000, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
	}
	unclaimed := NodeCapacity{
		NodeName:           "d1",
		FDValue:            "fdD1",
		DriveCapacitiesGiB: uniformDrives(10, 5120), // same 10 drives, not yet claimed — inventory reports them free
		TlcGiB:             51200,                   // matches what inventory sums for free drives
		AllocatableCPU:     1000, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
	}

	claimedPlan := PlanAutoFullDrives(
		AutoFullDrivesDesired{}, existingDrives, nil, []NodeCapacity{claimed, computeInv}, computeNodeSet("c1"), cons)
	unclaimedPlan := PlanAutoFullDrives(
		AutoFullDrivesDesired{}, existingDrives, nil, []NodeCapacity{unclaimed, computeInv}, computeNodeSet("c1"), cons)

	if claimedPlan.Infeasible != "" {
		t.Fatalf("claimed: Infeasible = %q, want empty", claimedPlan.Infeasible)
	}
	if unclaimedPlan.Infeasible != "" {
		t.Fatalf("unclaimed: Infeasible = %q, want empty", unclaimedPlan.Infeasible)
	}
	if len(claimedPlan.ComputeLayout) != 1 || len(unclaimedPlan.ComputeLayout) != 1 {
		t.Fatalf("want exactly 1 ComputeLayout entry each, got claimed=%+v unclaimed=%+v",
			claimedPlan.ComputeLayout, unclaimedPlan.ComputeLayout)
	}
	claimedHP := claimedPlan.ComputeLayout[0].HugepagesMiB
	unclaimedHP := unclaimedPlan.ComputeLayout[0].HugepagesMiB
	floor := 3000 * claimedPlan.ComputeLayout[0].NumCores
	if claimedHP <= floor {
		t.Fatalf("claimed: HugepagesMiB = %d, want > floor %d — fixture must exercise the capacity term", claimedHP, floor)
	}
	if claimedHP != unclaimedHP {
		t.Errorf("HugepagesMiB differs by claim state alone: claimed=%d unclaimed=%d — the compute-hugepages "+
			"total must not depend on whether Status.Allocations has caught up with a drive container the "+
			"walk already sized", claimedHP, unclaimedHP)
	}
}

// When the form-cluster floor (MinComputeContainers) asks for more containers than the compute:drive
// requirement needs, the surplus containers must still get at least one core each.
//
// 1 drive core at the strict 1:1 floor needs 1 compute core, but a cluster cannot form below
// MinComputeContainers=5 containers — 4 of the 5 are pure surplus, and each must still land at >=1 core.
func TestPlanAutoFullDrives_FormClusterFloorAboveDeficit_NoZeroCoreContainers(t *testing.T) {
	cons := testCons()
	// Strict 1:1 so the requirement is exactly the drive-core count, making the surplus arithmetic exact.
	cons.FullDrivesComputeToDriveCoreRatio = 0
	cons.MinComputeContainers = 5
	const bigFree = 1 << 28

	inv := []NodeCapacity{{
		NodeName: "drv1", FDValue: "fdDrv",
		DriveCapacitiesGiB: uniformDrives(1, 5120), TlcGiB: 5120,
		AllocatableCPU: 100, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
	}}
	for _, n := range []string{"s1", "s2", "s3", "s4", "s5"} {
		inv = append(inv, NodeCapacity{
			NodeName: n, FDValue: "fd" + n,
			AllocatableCPU: 100, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		})
	}
	computeNodes := computeNodeSet("s1", "s2", "s3", "s4", "s5")

	plan := PlanAutoFullDrives(AutoFullDrivesDesired{}, nil, nil, inv, computeNodes, cons)

	if plan.Infeasible != "" {
		t.Fatalf("Infeasible = %q, want feasible — 5 spare nodes for the 5-container floor", plan.Infeasible)
	}
	if len(plan.ComputeLayout) != 5 {
		t.Fatalf("ComputeLayout = %+v, want 5 entries (the form-cluster floor)", plan.ComputeLayout)
	}
	for _, spec := range plan.ComputeLayout {
		if spec.NumCores < 1 {
			t.Errorf("ComputeLayout entry for node %q has NumCores = %d, want >= 1 — a compute container "+
				"with zero cores is not a usable object; the floor must be met with minimally-sized "+
				"containers, not empty ones", spec.Node, spec.NumCores)
		}
	}
	// The surplus is surfaced, not silent — and in exactly one ComputeLayout warning. Every advisory from the
	// compute step is joined into one, because they all land on the single reason AutoFullDrivesComputeLayout
	// whose throttle key ignores the message: a second one would be dropped for the whole window, not shown.
	var layout []Warning
	for _, w := range plan.Warnings {
		if w.Kind == WarningKindComputeLayout {
			layout = append(layout, w)
		}
	}
	if len(layout) != 1 {
		t.Fatalf("WarningKindComputeLayout warnings = %+v, want exactly 1 joining every compute advisory", layout)
	}
	if !strings.Contains(layout[0].Message, "cannot form below 5 compute container(s)") ||
		!strings.Contains(layout[0].Message, "1-core minimum") {
		t.Errorf("warning = %q, want it to name the 5-container floor and the 1-core minimum", layout[0].Message)
	}
	if !strings.HasPrefix(layout[0].Message, "auto full drives: ") {
		t.Errorf("warning = %q, want the shared \"auto full drives: \" prefix the joined message carries once", layout[0].Message)
	}
}

// On a converged hyperconverged fleet, placeable (planComputeAutoFullDrives) excludes every node hosting a
// counted existing compute container; if that set is empty, deriveComputeLayout must still let newly
// signed drives grow existing compute in place rather than reporting infeasible.
//
// Fixture: n1 (6/6 owned + 4 free -> grows to 10/10), n2 (6/6, no free), n3 (5/5, no free) — each hosting
// a compute container 1:1 with its own drive cores (converged), all pinned, so placeable is empty.
//
// Post-growth t=10+6+5=21 vs existingCores=17 -> deficit=4. deriveComputeLayout over the empty placeable
// set fails; the growth pass then offers each pinned node its remaining CPU, covers the deficit in place,
// adding no container. Result: TotalTlcDriveCores stays at 21.
//
// Landing node: n2, not n1 — n1's own growth already charged 4 CPU against its remaining headroom, and
// largest-growable-first breaks the n2/n3 tie by node name.
func TestPlanAutoFullDrives_ConvergedFleet_NewlySignedDrivesGrowExistingCompute(t *testing.T) {
	cons := testCons()
	// Isolate from the compute:drive ratio (default 2.0 would spread growth across all 3 nodes,
	// invalidating the hand-derived numbers) with the 1:1 floor.
	cons.FullDrivesComputeToDriveCoreRatio = 0
	// Raise the per-container core cap: at the default 19, n3 (fewer existing cores) would have more
	// room before the ceiling than n2 despite equal true CPU headroom, breaking their tie by cap
	// instead of by name as this test intends.
	cons.MaxCoresPerContainer = 1000
	const bigFree = 1 << 28

	existingDrives := []ExistingContainer{
		{Name: "drv-n1", Node: "n1", FDValue: "fdN1", TlcGiB: 0, NumCores: 6, NumDrives: 6},
		{Name: "drv-n2", Node: "n2", FDValue: "fdN2", TlcGiB: 0, NumCores: 6, NumDrives: 6},
		{Name: "drv-n3", Node: "n3", FDValue: "fdN3", TlcGiB: 0, NumCores: 5, NumDrives: 5},
	}
	existingCompute := []ExistingComputeContainer{
		{Name: "ec-n1", Node: "n1", NumCores: 6, HugepagesMiB: 18000},
		{Name: "ec-n2", Node: "n2", NumCores: 6, HugepagesMiB: 18000},
		{Name: "ec-n3", Node: "n3", NumCores: 5, HugepagesMiB: 15000},
	}
	inv := []NodeCapacity{
		{
			NodeName: "n1", FDValue: "fdN1",
			OwnDriveCapacitiesGiB: uniformDrives(6, 5120),
			DriveCapacitiesGiB:    uniformDrives(4, 5120), // the newly signed drives
			TlcGiB:                4 * 5120,
			AllocatableCPU:        40, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		},
		{
			NodeName: "n2", FDValue: "fdN2",
			OwnDriveCapacitiesGiB: uniformDrives(6, 5120),
			TlcGiB:                0,
			AllocatableCPU:        40, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		},
		{
			NodeName: "n3", FDValue: "fdN3",
			OwnDriveCapacitiesGiB: uniformDrives(5, 5120),
			TlcGiB:                0,
			AllocatableCPU:        40, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		},
	}
	computeNodes := computeNodeSet("n1", "n2", "n3")

	plan := PlanAutoFullDrives(AutoFullDrivesDesired{}, existingDrives, existingCompute, inv, computeNodes, cons)

	if plan.Infeasible != "" {
		t.Fatalf("Infeasible = %q, want empty — every node has 40 CPU and unlimited hugepages/memory; the "+
			"only obstacle was the planner freezing existing compute on a fleet with no placeable node left",
			plan.Infeasible)
	}

	// The newly signed drives are adopted, at their full core count.
	if plan.TotalTlcDriveCores != 21 {
		t.Fatalf("TotalTlcDriveCores = %d, want 21 (6+6+5 existing, n1 growing 6 -> 10 on its 4 newly "+
			"signed drives) — a lower value means the plan quietly declined the new drives instead of "+
			"growing existing compute to cover them, which is exactly the lab wedge", plan.TotalTlcDriveCores)
	}
	var driveGrowth *ContainerGrowth
	for i := range plan.Grow {
		if plan.Grow[i].Name == "drv-n1" {
			driveGrowth = &plan.Grow[i]
		}
	}
	if driveGrowth == nil || driveGrowth.NewNumDrives != 10 {
		t.Fatalf("Grow for drv-n1 = %+v, want NewNumDrives=10 (6 owned + 4 newly signed)", driveGrowth)
	}

	// Growth resizes containers; it never adds them, so the container count is untouched.
	if plan.ComputeContainers != 3 || len(plan.ComputeLayout) != 3 {
		t.Fatalf("ComputeContainers = %d, len(ComputeLayout) = %d, want 3 and 3 — the deficit must be "+
			"covered by growing the existing containers, not by manufacturing new ones (there is no node "+
			"left to put one on)", plan.ComputeContainers, len(plan.ComputeLayout))
	}

	byNode := make(map[string]ComputeContainerSpec, len(plan.ComputeLayout))
	totalCores := 0
	for _, entry := range plan.ComputeLayout {
		byNode[entry.Node] = entry
		totalCores += entry.NumCores
	}
	if totalCores < plan.TotalTlcDriveCores {
		t.Errorf("total compute layout cores = %d, want >= TotalTlcDriveCores (%d) — the compute:drive 1:1 "+
			"requirement must be met by in-place growth", totalCores, plan.TotalTlcDriveCores)
	}
	// The whole 4-core deficit lands on n2: see the "where the growth lands" note above.
	if got := byNode["n2"].NumCores; got != 10 {
		t.Errorf("ComputeLayout[n2].NumCores = %d, want 10 — the 4-core deficit is covered in place on the "+
			"roomiest candidate (n1 already spent 4 CPU on its own drive-container growth)", got)
	}
	if got := byNode["n2"].HugepagesMiB; got <= 18000 {
		t.Errorf("ComputeLayout[n2].HugepagesMiB = %d, want > 18000 — a grown container must carry "+
			"hugepages re-derived for its NEW core count, or weka rejects the extra cores for being below "+
			"its minimum memory", got)
	}
	// Untouched containers stay exactly frozen: growth is spent only where it is needed.
	if got := byNode["n1"]; got.NumCores != 6 || got.HugepagesMiB != 18000 {
		t.Errorf("ComputeLayout[n1] = %+v, want NumCores=6 HugepagesMiB=18000 (frozen — the deficit was "+
			"already covered on n2)", got)
	}
	if got := byNode["n3"]; got.NumCores != 5 || got.HugepagesMiB != 15000 {
		t.Errorf("ComputeLayout[n3] = %+v, want NumCores=5 HugepagesMiB=15000 (frozen)", got)
	}
}

// With cons.FullDrivesComputeToDriveCoreRatio raised above 1.0, RequiredComputeCores must reflect the
// ratio (ceil(ratio*driveCores)), not the 1:1 floor testCons() otherwise exercises. Observable even with an
// empty computeNodes set since planComputeAutoFullDrives sets it unconditionally before layout derivation.
func TestPlanAutoFullDrives_FullDrivesComputeRatio_DerivesDoubleDriveCoresNotFloor(t *testing.T) {
	cons := testCons()
	cons.FullDrivesComputeToDriveCoreRatio = 2.0
	const bigFree = 1 << 28

	nc := NodeCapacity{
		NodeName: "n1", FDValue: "fdA", DriveCapacitiesGiB: uniformDrives(4, 1000), TlcGiB: 4000,
		AllocatableCPU: 1000, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
	}

	plan := singleNodeAutoFullDrives(nc, AutoFullDrivesDesired{}, cons)

	if len(plan.Create) != 1 {
		t.Fatalf("expected exactly 1 Create entry, got %d: %+v", len(plan.Create), plan.Create)
	}
	if got := plan.Create[0].NumCores; got != 4 {
		t.Fatalf("drive container NumCores = %d, want 4 (one core per drive is unaffected by the compute "+
			"ratio)", got)
	}
	if plan.TotalTlcDriveCores != 4 {
		t.Fatalf("TotalTlcDriveCores = %d, want 4", plan.TotalTlcDriveCores)
	}
	if want := 8; plan.RequiredComputeCores != want {
		t.Errorf("RequiredComputeCores = %d, want %d (2:1 ratio × 4 drive cores, NOT the 1:1 floor of 4)",
			plan.RequiredComputeCores, want)
	}
}

// TestPlanAutoFullDrives_Create_NumDrivesIndependentOfNumCores pins the headline invariant: a container's
// drive count is its node's full signed set, and its core count is a separate number — auto-derived cores
// happen to equal the drive count below the 19-core limit, but a driveCores pin does not drag the drive
// count down with it.
func TestPlanAutoFullDrives_Create_NumDrivesIndependentOfNumCores(t *testing.T) {
	cons := testCons()
	const bigFree = 1 << 28

	t.Run("auto-derive", func(t *testing.T) {
		inv := []NodeCapacity{
			{
				NodeName: "n1", FDValue: "fdN1",
				DriveCapacitiesGiB: uniformDrives(3, 5120), TlcGiB: sumInts(uniformDrives(3, 5120)),
				AllocatableCPU: 1000, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
			},
			{
				NodeName: "n2", FDValue: "fdN2",
				DriveCapacitiesGiB: uniformDrives(7, 5120), TlcGiB: sumInts(uniformDrives(7, 5120)),
				AllocatableCPU: 1000, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
			},
		}
		// Empty compute node set isolates drive placement from the compute layout step, same as
		// singleNodeAutoFullDrives — the compute-infeasible result never touches plan.Create.
		plan := PlanAutoFullDrives(AutoFullDrivesDesired{}, nil, nil, inv, map[string]bool{}, cons)

		if len(plan.Create) != 2 {
			t.Fatalf("expected exactly 2 Create entries, got %d: %+v", len(plan.Create), plan.Create)
		}
		for _, c := range plan.Create {
			if c.NumDrives != c.NumCores {
				t.Errorf("node %s: Create entry NumDrives=%d, NumCores=%d — want equal (one core per drive "+
					"in full-drives mode)", c.Node, c.NumDrives, c.NumCores)
			}
		}
	})

	t.Run("pinned", func(t *testing.T) {
		inv := []NodeCapacity{
			{
				// DriveCores pinned to 2, below this node's 5 signed drives: cores follow the pin, drives
				// drives at the pin, dropping the smallest 3 (largest-kept-first).
				NodeName: "n1", FDValue: "fdN1",
				DriveCapacitiesGiB: uniformDrives(5, 5120), TlcGiB: sumInts(uniformDrives(5, 5120)),
				AllocatableCPU: 1000, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
			},
			{
				// This node's drive count (2) matches the pin exactly, so nothing is dropped here.
				NodeName: "n2", FDValue: "fdN2",
				DriveCapacitiesGiB: uniformDrives(2, 5120), TlcGiB: sumInts(uniformDrives(2, 5120)),
				AllocatableCPU: 1000, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
			},
		}
		plan := PlanAutoFullDrives(AutoFullDrivesDesired{DriveCores: 2}, nil, nil, inv, map[string]bool{}, cons)

		if len(plan.Create) != 2 {
			t.Fatalf("expected exactly 2 Create entries, got %d: %+v", len(plan.Create), plan.Create)
		}
		wantDrives := map[string]int{"n1": 5, "n2": 2} // each node's FULL signed set, pin notwithstanding
		for _, c := range plan.Create {
			if c.NumCores != 2 {
				t.Errorf("node %s: Create entry NumCores=%d, want the pinned 2", c.Node, c.NumCores)
			}
			if want := wantDrives[c.Node]; c.NumDrives != want {
				t.Errorf("node %s: Create entry NumDrives=%d, want %d — the pin sets cores only, so a node "+
					"with more drives than the pin keeps every one of them", c.Node, c.NumDrives, want)
			}
		}
	})
}

// TestPlanAutoFullDrives_ProductionRatio_LabFleetGroundTruth is the executable record of the
// compute-hugepages ceiling — the single most consequential consequence of claiming every drive — on the
// real lab fleet this change was validated against, at the shipped coefficients.
//
// Because every drive is claimed at every point, totalTlcGiB is fixed by the hardware, so the
// capacity-based share of ComputeContainerHugepagesMiB cannot be reduced by any planner decision. The
// fleet is infeasible: the operator signed 48 drives and the cluster cannot host the compute they require.
// Do not loosen these assertions to make it green — the infeasibility is the behaviour under test, caught
// at kubectl apply by cluster_auto_full_drives_compute_hugepages.
//
// Numbers below are the worked example in doc/operator/deployment/act-as-daemonset.md and must stay in
// step with it:
//
//	claimed capacity   8 x 6 x 14,307            = 686,736 GiB
//	cluster hugepages  686,736 x 1024 / 1000     = 703,217 MiB to divide across compute containers
//	drive containers   6 drives -> 6 cores each, reserving 6 x 1664 = 9,984 MiB, leaving 50,016 free
//	compute needed     2.0 x 48 drive cores      = 96 cores
//	at 8 containers    703,217/8 + 1700x12 + 768 = 109,070 MiB  vs 50,016  -> INFEASIBLE
//	at 17 containers   703,217/17 + 1700x6 + 384 =  51,950 MiB  vs 50,016  -> still short
//	at 18 containers   703,217/18 + 1700x6 + 384 =  49,652 MiB  vs 50,016  -> fits
func TestPlanAutoFullDrives_ProductionRatio_LabFleetGroundTruth(t *testing.T) {
	// Shipped coefficients: values.yaml hugepages{Tlc,Qlc}Ratio + computeMaxHugepagesMiB, the production
	// 2.0 full-drives ratio, and the 64 MiB/core DPDK base both roles carry. testCons() omits these, which
	// would zero the capacity-based term and make the whole ceiling disappear.
	labCons := func() *CapacityConstraints {
		c := testCons()
		c.ComputeHugepagesTlcRatio = 1000
		c.ComputeHugepagesQlcRatio = 6000
		c.ComputeMaxHugepagesMiB = 360000
		c.DriveDpdkPerCoreMiB = 64
		c.ComputeDpdkPerCoreMiB = 64
		return c
	}
	// labFleet returns the 8 hyperconverged lab nodes plus `diskless` compute-only nodes, and the
	// eligibility set naming all of them.
	labFleet := func(diskless int) ([]NodeCapacity, map[string]bool) {
		var inv []NodeCapacity
		eligible := map[string]bool{}
		for i := 1; i <= 8; i++ {
			n := "lab" + itoa(i)
			inv = append(inv, NodeCapacity{
				NodeName: n, FDValue: n,
				DriveCapacitiesGiB:    uniformDrives(6, 14307),
				TlcGiB:                6 * 14307,
				AllocatableCPU:        63,
				AvailableHugepagesMiB: 60000,
				AvailableMemoryMiB:    197600,
			})
			eligible[n] = true
		}
		for j := 1; j <= diskless; j++ {
			n := "cmp" + itoa(j)
			inv = append(inv, NodeCapacity{
				NodeName: n, FDValue: n,
				AllocatableCPU: 63, AvailableHugepagesMiB: 60000, AvailableMemoryMiB: 197600,
			})
			eligible[n] = true
		}
		return inv, eligible
	}

	t.Run("as built: 8 hyperconverged nodes are infeasible", func(t *testing.T) {
		inv, eligible := labFleet(0)
		plan := PlanAutoFullDrives(AutoFullDrivesDesired{}, nil, nil, inv, eligible, labCons())

		if plan.Infeasible == "" {
			t.Fatalf("expected INFEASIBLE — the fleet cannot host the compute its 48 claimed drives "+
				"require; got a feasible plan: %+v", plan.DriveSizing)
		}
		if !strings.Contains(plan.Infeasible, "hugepages") {
			t.Errorf("Infeasible = %q, want it to name hugepages as the binding resource", plan.Infeasible)
		}
		if len(plan.ComputeLayout) != 0 || plan.ComputeContainers != 0 {
			t.Errorf("infeasible plan carries compute: %d layout entries, %d containers",
				len(plan.ComputeLayout), plan.ComputeContainers)
		}
		// The drives were never traded away to chase feasibility: the report still shows the full claim.
		if plan.DriveSizing == nil {
			t.Fatalf("DriveSizing is nil")
		}
		if plan.DriveSizing.DrivesTaken != 48 || plan.DriveSizing.DrivesAvailable != 48 {
			t.Errorf("DrivesTaken/Available = %d/%d, want 48/48 — every signed drive stays claimed even in "+
				"the infeasible report", plan.DriveSizing.DrivesTaken, plan.DriveSizing.DrivesAvailable)
		}
		if plan.RequiredComputeCores != 96 {
			t.Errorf("RequiredComputeCores = %d, want 96 (2.0 x 48 drive cores at the full derived core "+
				"count — nothing shrinks it)", plan.RequiredComputeCores)
		}
	})

	// The doc quotes 18 as the sufficient compute-node count and 17 as the one that misses. Both are
	// asserted, so the boundary cannot drift out of step with the doc unnoticed.
	t.Run("17 compute-eligible nodes still miss", func(t *testing.T) {
		inv, eligible := labFleet(9)
		if plan := PlanAutoFullDrives(AutoFullDrivesDesired{}, nil, nil, inv, eligible, labCons()); plan.Infeasible == "" {
			t.Fatalf("expected 17 compute-eligible nodes to be infeasible (51,950 MiB/container needed " +
				"against 50,016 free on the hyperconverged nodes), got feasible")
		}
	})

	t.Run("18 compute-eligible nodes suffice", func(t *testing.T) {
		inv, eligible := labFleet(10)
		plan := PlanAutoFullDrives(AutoFullDrivesDesired{}, nil, nil, inv, eligible, labCons())

		if plan.Infeasible != "" {
			t.Fatalf("expected 18 compute-eligible nodes to be feasible, got: %s", plan.Infeasible)
		}
		if plan.ComputeContainers != 18 {
			t.Errorf("ComputeContainers = %d, want 18", plan.ComputeContainers)
		}
		if plan.ComputeCores != 6 {
			t.Errorf("ComputeCores = %d, want 6 (ceil(96/18))", plan.ComputeCores)
		}
		// All 48 drives, all 48 drive cores — the whole point of needing 18 nodes.
		drives, cores := 0, 0
		for _, c := range plan.Create {
			drives += c.NumDrives
			cores += c.NumCores
		}
		if drives != 48 || cores != 48 {
			t.Errorf("got %d drives / %d drive cores, want 48/48", drives, cores)
		}
		if len(plan.ComputeLayout) > 0 && plan.ComputeLayout[0].HugepagesMiB != 49652 {
			t.Errorf("compute hugepages/container = %d, want 49652 — the doc's worked example; if this "+
				"changed, act-as-daemonset.md must change with it", plan.ComputeLayout[0].HugepagesMiB)
		}
	})
}

// TestPlanAutoFullDrives_NoOscillationAcrossReconciles_ProductionRatio verifies the fixed-point property at
// the 2:1 ratio the operator ships, with production hugepages coefficients that make the capacity-based
// term non-zero: pass 1 claims every drive at its full core count, so pass 2 must add nothing, and the
// frozen compute from pass 1 must not be re-derived into a spurious growth.
func TestPlanAutoFullDrives_NoOscillationAcrossReconciles_ProductionRatio(t *testing.T) {
	newCons := func() *CapacityConstraints {
		c := testCons() // ratio LEFT at the shipped 2.0.
		c.ComputeHugepagesTlcRatio = 1000
		c.ComputeHugepagesQlcRatio = 6000
		c.ComputeMaxHugepagesMiB = 360000
		return c
	}
	const bigFree = 1 << 28
	driveNode := func(own, free []int) NodeCapacity {
		return NodeCapacity{
			NodeName: "n1", FDValue: "fdA",
			DriveCapacitiesGiB:    free,
			TlcGiB:                sumInts(free),
			OwnDriveCapacitiesGiB: own,
			AllocatableCPU:        100, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		}
	}
	// Roomy enough to host 20 compute cores at the 2:1 ratio; the ceiling itself is covered by
	// TestPlanAutoFullDrives_ProductionRatio_LabFleetGroundTruth.
	computeInv := func() []NodeCapacity {
		return []NodeCapacity{tightNode("c1", 0, 64, bigFree), tightNode("c2", 0, 64, bigFree)}
	}
	computeNodes := computeNodeSet("c1", "c2")

	// Pass 1: greenfield — all 10 drives at 10 cores, requiring 20 compute cores.
	inv1 := append([]NodeCapacity{driveNode(nil, uniformDrives(10, 5120))}, computeInv()...)
	plan1 := PlanAutoFullDrives(AutoFullDrivesDesired{}, nil, nil, inv1, computeNodes, newCons())
	if plan1.Infeasible != "" {
		t.Fatalf("pass 1 infeasible: %s", plan1.Infeasible)
	}
	if len(plan1.Create) != 1 {
		t.Fatalf("pass 1: want exactly 1 Create, got %d: %+v", len(plan1.Create), plan1.Create)
	}
	c1 := plan1.Create[0]
	if c1.NumDrives != 10 || c1.NumCores != 10 {
		t.Fatalf("pass 1 Create = %+v, want all 10 drives at 10 cores", c1)
	}
	if plan1.RequiredComputeCores != 20 {
		t.Errorf("pass 1 RequiredComputeCores = %d, want 20 (2.0 x 10) — the shipped ratio must actually "+
			"bind, or this test is vacuous", plan1.RequiredComputeCores)
	}

	// Pass 2: feed pass 1's drive container and its compute layout back in. Nothing may move.
	var existingCompute []ExistingComputeContainer
	for _, l := range plan1.ComputeLayout {
		existingCompute = append(existingCompute, ExistingComputeContainer{
			Name: "compute-" + l.Node, Node: l.Node, NumCores: l.NumCores, HugepagesMiB: l.HugepagesMiB,
		})
	}
	existingDrives := []ExistingContainer{{
		Name: "drive-n1", Node: "n1", FDValue: "fdA",
		TlcGiB: c1.TlcGiB, NumCores: c1.NumCores, NumDrives: c1.NumDrives,
	}}
	inv2 := append([]NodeCapacity{driveNode(uniformDrives(10, 5120), nil)}, computeInv()...)
	plan2 := PlanAutoFullDrives(AutoFullDrivesDesired{}, existingDrives, existingCompute, inv2, computeNodes, newCons())

	if plan2.Infeasible != "" {
		t.Fatalf("pass 2 infeasible: %s — oscillated into infeasibility", plan2.Infeasible)
	}
	if len(plan2.Create) != 0 || len(plan2.Grow) != 0 {
		t.Errorf("pass 2 must be a no-op, got Create=%+v Grow=%+v", plan2.Create, plan2.Grow)
	}
	if plan2.TotalTlcDriveCores != plan1.TotalTlcDriveCores {
		t.Errorf("pass 2 TotalTlcDriveCores = %d, want %d (unchanged)",
			plan2.TotalTlcDriveCores, plan1.TotalTlcDriveCores)
	}
	if plan2.ComputeContainers != plan1.ComputeContainers {
		t.Errorf("pass 2 ComputeContainers = %d, want %d — the frozen compute must be recognised, not "+
			"re-derived into extra containers", plan2.ComputeContainers, plan1.ComputeContainers)
	}
}

// afdDrives/afdComputeNodes/afdPlan build a fleet with dedicated compute nodes, so cases using them
// measure drive placement without compute placement interfering.

// afdDrives returns n drives of sizeEach GiB, descending-uniform.
func afdDrives(n, sizeEach int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = sizeEach
	}
	return out
}

// afdComputeNodes returns count drive-less inventory nodes with headroom that never binds compute
// placement, plus the eligibility map naming exactly them; drive nodes are left out of the map so each
// case measures drive placement, not co-location.
func afdComputeNodes(count int) ([]NodeCapacity, map[string]bool) {
	const big = 1 << 28
	nodes := make([]NodeCapacity, 0, count)
	eligible := make(map[string]bool, count)
	for i := range count {
		name := string(rune('a'+i)) + "-compute"
		nodes = append(nodes, NodeCapacity{
			NodeName: name, FDValue: name,
			AllocatableCPU: 200, AvailableHugepagesMiB: big, AvailableMemoryMiB: big,
		})
		eligible[name] = true
	}
	return nodes, eligible
}

func afdPlan(t *testing.T, desired AutoFullDrivesDesired, existing []ExistingContainer, driveNodes ...NodeCapacity) CapacityPlan {
	t.Helper()
	computeInv, eligible := afdComputeNodes(4)
	return PlanAutoFullDrives(desired, existing, nil, append(driveNodes, computeInv...), eligible, testCons())
}

// A node with more drives than the per-container core limit keeps every drive and is simply capped on
// cores, since drives and cores aren't tracked one-for-one.
func TestPlanAutoFullDrives_TakesAllDrivesCoresCappedAt19(t *testing.T) {
	const big = 1 << 28
	plan := afdPlan(t, AutoFullDrivesDesired{}, nil, NodeCapacity{
		NodeName: "n1", FDValue: "fdA",
		DriveCapacitiesGiB: afdDrives(30, 1000), TlcGiB: 30000,
		AllocatableCPU: 100, AvailableHugepagesMiB: big, AvailableMemoryMiB: big,
	})

	if plan.Infeasible != "" {
		t.Fatalf("expected feasible, got %q", plan.Infeasible)
	}
	if len(plan.Create) != 1 {
		t.Fatalf("expected 1 create, got %d", len(plan.Create))
	}
	c := plan.Create[0]
	if c.NumDrives != 30 || c.NumCores != DefaultMaxCoresPerContainer || c.TlcGiB != 30000 {
		t.Fatalf("want 30 drives / %d cores / 30000 GiB, got %d / %d / %d",
			DefaultMaxCoresPerContainer, c.NumDrives, c.NumCores, c.TlcGiB)
	}
	if plan.DriveSizing == nil || plan.DriveSizing.DrivesTaken != plan.DriveSizing.DrivesAvailable {
		t.Fatalf("cap must not cost a drive: %+v", plan.DriveSizing)
	}
}

// A pinned numDrives takes that many of the node's largest drives and reports the rest as stranded, once,
// for the whole fleet.
func TestPlanAutoFullDrives_NumDrivesPinTakesLargest(t *testing.T) {
	const big = 1 << 28
	plan := afdPlan(t, AutoFullDrivesDesired{NumDrives: 3}, nil, NodeCapacity{
		NodeName: "n1", FDValue: "fdA",
		DriveCapacitiesGiB: []int{100, 600, 300, 500, 200, 400}, TlcGiB: 2100,
		AllocatableCPU: 100, AvailableHugepagesMiB: big, AvailableMemoryMiB: big,
	})

	if plan.Infeasible != "" {
		t.Fatalf("expected feasible, got %q", plan.Infeasible)
	}
	c := plan.Create[0]
	if c.NumDrives != 3 || c.NumCores != 3 || c.TlcGiB != 1500 { // 600+500+400
		t.Fatalf("want 3 largest drives (1500 GiB) / 3 cores, got %d / %d / %d", c.NumDrives, c.NumCores, c.TlcGiB)
	}

	var stranded int
	for _, w := range plan.Warnings {
		if w.Kind == WarningKindDrivesStranded {
			stranded++
			if !strings.Contains(w.Message, "n1 (3 of 6)") {
				t.Fatalf("stranded warning lacks the per-node breakdown: %q", w.Message)
			}
		}
	}
	if stranded != 1 {
		t.Fatalf("want exactly 1 aggregated DrivesStranded warning, got %d", stranded)
	}
}

// driveCores below the drive count is lossless: every claimed drive is kept, on fewer cores.
func TestPlanAutoFullDrives_DriveCoresPinBelowDriveCountKeepsAllDrives(t *testing.T) {
	const big = 1 << 28
	plan := afdPlan(t, AutoFullDrivesDesired{NumDrives: 3, DriveCores: 2}, nil, NodeCapacity{
		NodeName: "n1", FDValue: "fdA",
		DriveCapacitiesGiB: afdDrives(6, 500), TlcGiB: 3000,
		AllocatableCPU: 100, AvailableHugepagesMiB: big, AvailableMemoryMiB: big,
	})

	if plan.Infeasible != "" {
		t.Fatalf("expected feasible, got %q", plan.Infeasible)
	}
	if c := plan.Create[0]; c.NumDrives != 3 || c.NumCores != 2 {
		t.Fatalf("want 3 drives / 2 cores, got %d / %d", c.NumDrives, c.NumCores)
	}
}

// driveCores above the effective drive count is infeasible: weka needs a physical drive per drive core.
func TestPlanAutoFullDrives_DriveCoresPinAboveEffectiveDrivesInfeasible(t *testing.T) {
	const big = 1 << 28
	plan := afdPlan(t, AutoFullDrivesDesired{NumDrives: 3, DriveCores: 8}, nil, NodeCapacity{
		NodeName: "n1", FDValue: "fdA",
		DriveCapacitiesGiB: afdDrives(10, 500), TlcGiB: 5000,
		AllocatableCPU: 100, AvailableHugepagesMiB: big, AvailableMemoryMiB: big,
	})

	if plan.Infeasible == "" {
		t.Fatal("expected infeasible")
	}
	if plan.Infeasibility.Binding != "driveCores" {
		t.Fatalf("want Binding=driveCores, got %q (%s)", plan.Infeasibility.Binding, plan.Infeasible)
	}
	if len(plan.ComputeLayout) != 0 {
		t.Fatalf("infeasible plan must carry no compute layout, got %d entries", len(plan.ComputeLayout))
	}
}

// numDrives above a node's signed count is infeasible — the pin is fleet-wide, so the shortest node binds.
func TestPlanAutoFullDrives_NumDrivesPinAboveNodeCountInfeasible(t *testing.T) {
	const big = 1 << 28
	plan := afdPlan(t, AutoFullDrivesDesired{NumDrives: 10}, nil, NodeCapacity{
		NodeName: "n1", FDValue: "fdA",
		DriveCapacitiesGiB: afdDrives(4, 500), TlcGiB: 2000,
		AllocatableCPU: 100, AvailableHugepagesMiB: big, AvailableMemoryMiB: big,
	})

	if plan.Infeasible == "" {
		t.Fatal("expected infeasible")
	}
	if plan.Infeasibility.Binding != "numDrives" {
		t.Fatalf("want Binding=numDrives, got %q (%s)", plan.Infeasibility.Binding, plan.Infeasible)
	}
	if len(plan.Create) != 0 || len(plan.ComputeLayout) != 0 {
		t.Fatalf("nothing may be planned: %d create, %d compute", len(plan.Create), len(plan.ComputeLayout))
	}
}

// One node that cannot fit its own drives fails the whole plan — no partial cluster, no compute layout —
// and every offender is named in RejectedNodes with a non-GiB unit.
func TestPlanAutoFullDrives_OneNodeCannotFitFailsWholePlan(t *testing.T) {
	const big = 1 << 28
	ok := NodeCapacity{
		NodeName: "n-ok", FDValue: "fdA",
		DriveCapacitiesGiB: afdDrives(2, 500), TlcGiB: 1000,
		AllocatableCPU: 100, AvailableHugepagesMiB: big, AvailableMemoryMiB: big,
	}
	// 2 drives -> at least 1 core -> 1+1 = 2 physical CPU, more than this node has at any cap.
	short := NodeCapacity{
		NodeName: "n-short", FDValue: "fdB",
		DriveCapacitiesGiB: afdDrives(2, 500), TlcGiB: 1000,
		AllocatableCPU: 1, AvailableHugepagesMiB: big, AvailableMemoryMiB: big,
	}
	plan := afdPlan(t, AutoFullDrivesDesired{}, nil, ok, short)

	if plan.Infeasible == "" {
		t.Fatal("expected the whole plan to be infeasible")
	}
	if len(plan.ComputeLayout) != 0 || plan.ComputeContainers != 0 || len(plan.ComputeNodes) != 0 {
		t.Fatalf("infeasible plan must never be given a compute layout: %d/%d/%d",
			len(plan.ComputeLayout), plan.ComputeContainers, len(plan.ComputeNodes))
	}
	if len(plan.Infeasibility.RejectedNodes) != 1 || plan.Infeasibility.RejectedNodes[0].Node != "n-short" {
		t.Fatalf("want n-short rejected, got %+v", plan.Infeasibility.RejectedNodes)
	}
	if u := plan.Infeasibility.RejectedNodes[0].Unit; u == "" {
		t.Fatal("RejectedNodes must carry a Unit so renderers do not print a CPU count as GiB")
	}
}

func TestPlanAutoFullDrives_EveryNonFittingNodeIsNamed(t *testing.T) {
	const big = 1 << 28
	mk := func(name string) NodeCapacity {
		return NodeCapacity{
			NodeName: name, FDValue: name,
			DriveCapacitiesGiB: afdDrives(2, 500), TlcGiB: 1000,
			AllocatableCPU: 1, AvailableHugepagesMiB: big, AvailableMemoryMiB: big,
		}
	}
	plan := afdPlan(t, AutoFullDrivesDesired{}, nil, mk("n1"), mk("n2"))

	if len(plan.Infeasibility.RejectedNodes) != 2 {
		t.Fatalf("both offenders must be named, got %+v", plan.Infeasibility.RejectedNodes)
	}
	for _, n := range []string{"n1", "n2"} {
		if !strings.Contains(plan.Infeasible, n) {
			t.Fatalf("message must name %s: %s", n, plan.Infeasible)
		}
	}
}

// The migration case: an existing 19-core/19-drive container on a 30-drive node absorbs all 30 drives at
// the same core count. Cores and memory are unchanged, but hugepages are not — each added drive costs
// DriveHugepagesPerDriveMiB (allocator.CalculateDriveHugepages is 1400*cores + 200*numDrives), so the
// growth is charged for 11 added drives and gated on node headroom like any other.
func TestPlanAutoFullDrives_DriveOnlyGrowthChargesPerDriveHugepages(t *testing.T) {
	const addedDrives = 30 - DefaultMaxCoresPerContainer // 11
	wantDelta := addedDrives * DriveHugepagesPerDriveMiB // 2200 MiB

	mkExisting := func() []ExistingContainer {
		return []ExistingContainer{{
			Name: "c1", Node: "n1", FDValue: "fdA",
			NumCores: DefaultMaxCoresPerContainer, NumDrives: DefaultMaxCoresPerContainer,
		}}
	}
	mkNode := func(freeHugepagesMiB int) NodeCapacity {
		return NodeCapacity{
			NodeName: "n1", FDValue: "fdA",
			OwnDriveCapacitiesGiB: afdDrives(19, 1000),
			DriveCapacitiesGiB:    afdDrives(11, 1000),
			TlcGiB:                30000,
			// Cores and memory are untouched by a drives-only growth, so only hugepages can bind here.
			AllocatableCPU: 0, AvailableHugepagesMiB: freeHugepagesMiB, AvailableMemoryMiB: 0,
		}
	}

	t.Run("exhausted node cannot absorb the drives", func(t *testing.T) {
		plan := afdPlan(t, AutoFullDrivesDesired{}, mkExisting(), mkNode(0))
		if plan.Infeasible == "" {
			t.Fatalf("a drives-only growth with no hugepages headroom must be infeasible, got a feasible plan")
		}
		if len(plan.Infeasibility.RejectedNodes) != 1 || plan.Infeasibility.RejectedNodes[0].Node != "n1" {
			t.Fatalf("n1 must be the named offender, got %+v", plan.Infeasibility.RejectedNodes)
		}
		if got := plan.Infeasibility.RejectedNodes[0]; got.Binding != bindingHugepages || got.Needed != wantDelta {
			t.Fatalf("want binding %q needing %d MiB (200 per added drive), got %q needing %d",
				bindingHugepages, wantDelta, got.Binding, got.Needed)
		}
	})

	t.Run("exactly enough hugepages lets it through", func(t *testing.T) {
		plan := afdPlan(t, AutoFullDrivesDesired{}, mkExisting(), mkNode(wantDelta))
		if plan.Infeasible != "" {
			t.Fatalf("growth must fit with exactly %d MiB free, got %q", wantDelta, plan.Infeasible)
		}
		if len(plan.Grow) != 1 {
			t.Fatalf("expected 1 grow, got %d", len(plan.Grow))
		}
		g := plan.Grow[0]
		if g.NewNumDrives != 30 || g.NewCores != DefaultMaxCoresPerContainer || g.NewTlcGiB != 30000 {
			t.Fatalf("want 30 drives / %d cores / 30000 GiB, got %d / %d / %d",
				DefaultMaxCoresPerContainer, g.NewNumDrives, g.NewCores, g.NewTlcGiB)
		}
	})
}

// TestPlanAutoFullDrives_ZeroCoreExistingContainer_GrowthDoesNotDoubleChargePerDriveHugepages pins the
// autoNodeFit guard: DriveContainerHugepagesMiB charges a per-drive term independently of cores, and
// inventory has already netted the existing container's footprint out of node headroom.
//
// A container recorded with NumCores==0 must still credit its old per-drive footprint back when it grows
// cores, or that per-drive term is charged a second time on top of what the inventory already withheld.
func TestPlanAutoFullDrives_ZeroCoreExistingContainer_GrowthDoesNotDoubleChargePerDriveHugepages(t *testing.T) {
	cons := testCons()

	// n1 already holds both its drives (no free drives left), so growth is cores-only: 0 -> FullDriveCores(2,
	// cons) = 2. Drives stay at 2 throughout, so their per-drive hugepages term (2*200 MiB) must not appear
	// in the charged delta at all.
	existing := []ExistingContainer{
		{Name: "c1", Node: "n1", FDValue: "fdA", NumCores: 0, NumDrives: 2, TlcGiB: 10000},
	}
	mkNode := func(freeHugepagesMiB int) NodeCapacity {
		return NodeCapacity{
			NodeName: "n1", FDValue: "fdA",
			OwnDriveCapacitiesGiB: afdDrives(2, 5000),
			TlcGiB:                10000,
			AllocatableCPU:        1000, AvailableHugepagesMiB: freeHugepagesMiB, AvailableMemoryMiB: 1 << 28,
		}
	}

	// correctCost is the real delta: only the cores term (0->2), since drives don't move. buggyCost is what a
	// zeroed-out old footprint would charge instead — the cores term plus the 2 pre-existing drives'
	// per-drive term counted a second time.
	correctCost := DriveContainerHugepagesMiB(2, 2, cons) - DriveContainerHugepagesMiB(0, 2, cons)
	buggyCost := DriveContainerHugepagesMiB(2, 2, cons)

	t.Run("headroom between the real cost and the double-charged one still fits", func(t *testing.T) {
		headroom := (correctCost + buggyCost) / 2
		plan := afdPlan(t, AutoFullDrivesDesired{}, existing, mkNode(headroom))
		if plan.Infeasible != "" {
			t.Fatalf("growth must fit with %d MiB free (real cost is %d MiB, a double-charged per-drive term "+
				"would need %d MiB) — got infeasible: %s", headroom, correctCost, buggyCost, plan.Infeasible)
		}
		if len(plan.Grow) != 1 {
			t.Fatalf("expected 1 grow, got %d", len(plan.Grow))
		}
		if g := plan.Grow[0]; g.NewCores != 2 || g.NewNumDrives != 2 {
			t.Fatalf("want NewCores=2 NewNumDrives=2 (cores catch up, drives unchanged), got %+v", g)
		}
	})

	t.Run("exactly the real cost still fits", func(t *testing.T) {
		plan := afdPlan(t, AutoFullDrivesDesired{}, existing, mkNode(correctCost))
		if plan.Infeasible != "" {
			t.Fatalf("growth must fit with exactly %d MiB free, got %q", correctCost, plan.Infeasible)
		}
	})
}

// Growth whose cores must rise and cannot fit is infeasible — here under a driveCores pin, which forces
// cores up independently of the drive count. TestPlanAutoFullDrives_Growth_NodeThatCannotFitNewCoresFailsWholePlan
// covers the unpinned case, where newly signed drives raise the derived core count on their own.
func TestPlanAutoFullDrives_CoreGrowthThatCannotFitIsInfeasible(t *testing.T) {
	existing := []ExistingContainer{{Name: "c1", Node: "n1", FDValue: "fdA", NumCores: 2, NumDrives: 2}}
	node := NodeCapacity{
		NodeName: "n1", FDValue: "fdA",
		OwnDriveCapacitiesGiB: afdDrives(2, 1000),
		DriveCapacitiesGiB:    afdDrives(3, 1000),
		TlcGiB:                5000,
		AllocatableCPU:        0, AvailableHugepagesMiB: 0, AvailableMemoryMiB: 0,
	}
	plan := afdPlan(t, AutoFullDrivesDesired{DriveCores: 5}, existing, node)

	if plan.Infeasible == "" {
		t.Fatal("expected infeasible: the pin forces cores from 2 to 5 on a node with nothing free")
	}
	if len(plan.Grow) != 0 {
		t.Fatalf("a failed node must contribute no growth entry, got %+v", plan.Grow)
	}
	if len(plan.ComputeLayout) != 0 {
		t.Fatalf("infeasible plan must carry no compute layout, got %d entries", len(plan.ComputeLayout))
	}
}

// A pod that has not been scheduled yet holds no node resources to grow into, and raising its spec would
// only make it strictly harder to schedule. This mirrors clusterCapacity's existing freeze in
// layOutExistingCompute (planner.go:990: `if ec.Unscheduled || freezeExisting || ...`), which auto-full-drives
// has its own copy of on both the drive and compute side.
func TestPlanAutoFullDrives_UnscheduledDriveContainer_FreezesGrowth(t *testing.T) {
	cons := testCons()
	const bigFree = 1 << 28

	existingDrives := []ExistingContainer{
		{Name: "drive-scheduled", Node: "scheduled", FDValue: "scheduled", TlcGiB: 3000, NumCores: 1, NumDrives: 3},
		{Name: "drive-unscheduled", Node: "unscheduled", FDValue: "unscheduled", TlcGiB: 3000, NumCores: 1, NumDrives: 3, Unscheduled: true},
	}
	inv := []NodeCapacity{
		{
			NodeName: "scheduled", FDValue: "scheduled",
			OwnDriveCapacitiesGiB: uniformDrives(3, 1000),
			AllocatableCPU:        100, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		},
		{
			NodeName: "unscheduled", FDValue: "unscheduled",
			OwnDriveCapacitiesGiB: uniformDrives(3, 1000),
			AllocatableCPU:        100, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		},
	}
	computeNodes := computeNodeSet("scheduled", "unscheduled")

	plan := PlanAutoFullDrives(AutoFullDrivesDesired{}, existingDrives, nil, inv, computeNodes, cons)

	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}

	// Both containers are under-cored for the 3 drives they each hold (1 core), so both have pending
	// growth available on paper — the scheduled one must take it, the unscheduled one must not.
	if len(plan.Grow) != 1 {
		t.Fatalf("plan.Grow = %+v, want exactly 1 entry (the scheduled container only)", plan.Grow)
	}
	if g := plan.Grow[0]; g.Name != "drive-scheduled" || g.NewCores != 3 || g.NewNumDrives != 3 {
		t.Errorf("Grow entry = %+v, want {Name: drive-scheduled, NewCores: 3, NewNumDrives: 3}", g)
	}
	for _, g := range plan.Grow {
		if g.Name == "drive-unscheduled" {
			t.Errorf("drive-unscheduled must not appear in plan.Grow — its pod is not scheduled yet, so "+
				"growing it would only make scheduling strictly harder: %+v", g)
		}
	}

	// The unscheduled container's would-be growth must not be silently assumed downstream: 4 (3 grown +
	// 1 frozen) proves the freeze took effect. 6 (both grown to 3) would mean the freeze only skipped the
	// Grow entry while TotalTlcDriveCores (which drives RequiredComputeCores and the whole compute
	// layout) still counted the growth as if it happened.
	if plan.TotalTlcDriveCores != 4 {
		t.Errorf("TotalTlcDriveCores = %d, want 4 (3 grown + 1 frozen)", plan.TotalTlcDriveCores)
	}

	found := false
	for _, w := range plan.Warnings {
		if w.Kind == WarningKindTransient {
			found = true
			if !strings.Contains(w.Message, "unscheduled") {
				t.Errorf("WarningKindTransient warning message = %q, want it to name node %q", w.Message, "unscheduled")
			}
		}
	}
	if !found {
		t.Errorf("plan.Warnings = %+v, want a WarningKindTransient warning naming node %q", plan.Warnings, "unscheduled")
	}
}

// Both placement-deferral causes (unscheduled pod, container being deleted) map to the same
// AutoFullDrivesPlacementDeferred reason, so a pass hitting both must still produce exactly one warning —
// two would let the event throttle (keyed on reason alone) silently drop one of them.
func TestPlanAutoFullDrives_UnscheduledAndDeletingCauses_MergeIntoOneWarning(t *testing.T) {
	cons := testCons()
	const bigFree = 1 << 28

	existingDrives := []ExistingContainer{
		{Name: "drive-scheduled", Node: "scheduled", FDValue: "scheduled", TlcGiB: 3000, NumCores: 3, NumDrives: 3},
		{Name: "drive-unscheduled", Node: "unscheduled", FDValue: "unscheduled", TlcGiB: 3000, NumCores: 3, NumDrives: 3, Unscheduled: true},
		// No entry for "deleting" — mirrors ExistingDrives already filtering out the mid-deletion container.
	}
	inv := []NodeCapacity{
		{
			NodeName: "scheduled", FDValue: "scheduled",
			OwnDriveCapacitiesGiB: uniformDrives(3, 1000),
			AllocatableCPU:        100, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		},
		{
			NodeName: "unscheduled", FDValue: "unscheduled",
			OwnDriveCapacitiesGiB: uniformDrives(3, 1000),
			AllocatableCPU:        100, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		},
		{
			NodeName: "deleting", FDValue: "deleting",
			DriveCapacitiesGiB: uniformDrives(3, 1000), TlcGiB: 3000,
			AllocatableCPU: 100, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
			HasDeletingDriveContainer: true,
		},
	}
	computeNodes := computeNodeSet("scheduled", "unscheduled", "deleting")

	plan := PlanAutoFullDrives(AutoFullDrivesDesired{}, existingDrives, nil, inv, computeNodes, cons)

	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}

	var transient []Warning
	for _, w := range plan.Warnings {
		if w.Kind == WarningKindTransient {
			transient = append(transient, w)
		}
	}
	if len(transient) != 1 {
		t.Fatalf("WarningKindTransient warnings = %+v, want exactly 1 covering both causes", transient)
	}
	w := transient[0]
	if !strings.Contains(w.Message, "unscheduled") {
		t.Errorf("warning message = %q, want it to name the unscheduled node %q", w.Message, "unscheduled")
	}
	if !strings.Contains(w.Message, "deleting") {
		t.Errorf("warning message = %q, want it to name the deleting node %q", w.Message, "deleting")
	}
	if !strings.Contains(w.Message, "both retry automatically") {
		t.Errorf("warning message = %q, want the merged-causes retry clause \"both retry automatically\"", w.Message)
	}
}

// The walk returns from inside the loop when a node's pins cannot be satisfied. Warnings collected before
// that node describe the plan it returns and must survive it: the CLI renders an ineligible node's row as
// "cordoned — see WARNINGS", so losing the warning leaves a row citing an entry nothing wrote.
func TestPlanAutoFullDrives_InfeasibleMidWalk_KeepsWarningsAlreadyCollected(t *testing.T) {
	cons := testCons()
	const bigFree = 1 << 28

	// Nodes are walked in name order, so a-cordoned records its warning before z-pinned aborts the walk.
	inv := []NodeCapacity{
		{
			NodeName: "a-cordoned", FDValue: "a-cordoned",
			DriveCapacitiesGiB: uniformDrives(3, 1000),
			AllocatableCPU:     100, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
			IneligibleReason: "cordoned",
		},
		// One signed drive under numDrives=2: autoSizeNode reports and planAutoFullDrivesDrives returns.
		{
			NodeName: "z-pinned", FDValue: "z-pinned",
			DriveCapacitiesGiB: uniformDrives(1, 1000),
			AllocatableCPU:     100, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		},
	}

	plan := PlanAutoFullDrives(AutoFullDrivesDesired{NumDrives: 2}, nil, nil, inv,
		computeNodeSet("a-cordoned", "z-pinned"), cons)

	if plan.Infeasible == "" {
		t.Fatalf("plan is feasible, so it no longer exercises the mid-walk return; plan=%+v", plan)
	}
	for _, w := range plan.Warnings {
		if w.Kind == WarningKindNodeIneligible && strings.Contains(w.Message, "a-cordoned") {
			return
		}
	}
	t.Errorf("Warnings = %+v, want the NodeIneligible warning naming a-cordoned to survive the mid-walk return",
		plan.Warnings)
}

// The unscheduled node gets 2 free drives beyond the 1 it owns, so planned (3 drives, 3000 GiB) and
// frozen (1 drive, 1000 GiB) diverge — checks that compute sizing uses the planned figure, the same
// "count it anyway, after the totals" convention as the drive-side freeze (file header :204-205).
func TestPlanAutoFullDrives_UnscheduledDriveContainer_ComputeCountsPlannedNotFrozenCapacity(t *testing.T) {
	cons := testCons()
	cons.ComputeHugepagesTlcRatio = 1024
	cons.FullDrivesComputeToDriveCoreRatio = 0
	const bigFree = 1 << 28

	existingDrives := []ExistingContainer{
		{Name: "drive-unscheduled", Node: "unscheduled", FDValue: "unscheduled", TlcGiB: 0, NumCores: 1, NumDrives: 1, Unscheduled: true},
	}
	unscheduled := NodeCapacity{
		NodeName: "unscheduled", FDValue: "unscheduled",
		OwnDriveCapacitiesGiB: uniformDrives(1, 1000), // 1 drive already claimed — the frozen figure
		DriveCapacitiesGiB:    uniformDrives(2, 1000), // 2 more signed but unclaimed — only reachable via the plan
		AllocatableCPU:        100, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
	}
	c1 := NodeCapacity{NodeName: "c1", FDValue: "fdC1", AllocatableCPU: 1000, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree}

	plan := PlanAutoFullDrives(AutoFullDrivesDesired{}, existingDrives, nil, []NodeCapacity{unscheduled, c1}, computeNodeSet("c1"), cons)

	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Grow) != 0 {
		t.Fatalf("expected no Grow entry — the pod is unscheduled, got %+v", plan.Grow)
	}
	// 3000, not 1000: the frozen container is never grown, but its drives (all 3, since it already holds the
	// container) still count toward the numerator.
	if plan.DriveSizing == nil || plan.DriveSizing.TlcGiBTaken != 3000 {
		t.Fatalf("DriveSizing.TlcGiBTaken = %v, want 3000 (the planned 3-drive figure, not the frozen 1-drive one)",
			plan.DriveSizing)
	}
	if len(plan.ComputeLayout) != 1 {
		t.Fatalf("ComputeLayout = %+v, want exactly 1 entry", plan.ComputeLayout)
	}
	// 4700 comes from the planned 3000 GiB. The frozen figure (1000 GiB) would floor at 3000 instead — the
	// gap between the two is exactly what proves which one the numerator used.
	if c := plan.ComputeLayout[0]; c.HugepagesMiB != 4700 {
		t.Errorf("ComputeLayout[0].HugepagesMiB = %d, want 4700 (planned-capacity numerator, not the frozen 3000 floor)",
			c.HugepagesMiB)
	}
}

// Compute-side counterpart of TestPlanAutoFullDrives_UnscheduledDriveContainer_FreezesGrowth: on a fully
// hyperconverged fleet, an unscheduled existing compute container stays frozen at its input size and is
// never selected for growth, even with far more CPU headroom than the scheduled container — the deficit
// must land entirely on the scheduled container instead.
func TestPlanAutoFullDrives_UnscheduledComputeContainer_FreezesGrowth(t *testing.T) {
	cons := testCons()
	// Isolate from the compute:drive ratio, matching the neighboring ConvergedFleet test.
	cons.FullDrivesComputeToDriveCoreRatio = 0
	cons.MaxCoresPerContainer = 1000
	const bigFree = 1 << 28

	existingDrives := []ExistingContainer{
		{Name: "drv-n1", Node: "n1", FDValue: "fdN1", TlcGiB: 0, NumCores: 6, NumDrives: 6},
		{Name: "drv-n2", Node: "n2", FDValue: "fdN2", TlcGiB: 0, NumCores: 6, NumDrives: 6},
	}
	existingCompute := []ExistingComputeContainer{
		{Name: "ec-n1", Node: "n1", NumCores: 6, HugepagesMiB: 18000},
		// Given more CPU headroom than n1, so a buggy planner (missing the Unscheduled skip)
		// would prefer it for growth instead of freezing it.
		{Name: "ec-n2", Node: "n2", NumCores: 4, HugepagesMiB: 12000, Unscheduled: true},
	}
	inv := []NodeCapacity{
		{
			NodeName: "n1", FDValue: "fdN1",
			OwnDriveCapacitiesGiB: uniformDrives(6, 5120),
			AllocatableCPU:        40, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		},
		{
			NodeName: "n2", FDValue: "fdN2",
			OwnDriveCapacitiesGiB: uniformDrives(6, 5120),
			AllocatableCPU:        200, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		},
	}
	computeNodes := computeNodeSet("n1", "n2")

	plan := PlanAutoFullDrives(AutoFullDrivesDesired{}, existingDrives, existingCompute, inv, computeNodes, cons)

	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if plan.RequiredComputeCores != 12 {
		t.Fatalf("RequiredComputeCores = %d, want 12 (6+6 drive cores, ratio disabled)", plan.RequiredComputeCores)
	}

	// Growth resizes containers; it never adds them, so the container count is untouched.
	if plan.ComputeContainers != 2 || len(plan.ComputeLayout) != 2 {
		t.Fatalf("ComputeContainers = %d, len(ComputeLayout) = %d, want 2 and 2", plan.ComputeContainers, len(plan.ComputeLayout))
	}
	byNode := make(map[string]ComputeContainerSpec, len(plan.ComputeLayout))
	for _, entry := range plan.ComputeLayout {
		byNode[entry.Node] = entry
	}

	// ec-n2's pod is not scheduled: it must stay frozen at its input cores/hugepages, never counted as
	// available growth capacity — even though it has far more CPU headroom (200 vs 40) than n1.
	if got := byNode["n2"]; got.NumCores != 4 || got.HugepagesMiB != 12000 {
		t.Errorf("ComputeLayout[n2] = %+v, want NumCores=4 HugepagesMiB=12000 (frozen — an unscheduled "+
			"container must never be handed growth)", got)
	}

	// The full 2-core deficit (RequiredComputeCores=12 - existingCores=10) must land entirely on the
	// scheduled n1 container: RequiredComputeCores is not "covered" by n2's frozen (would-be) capacity.
	if got := byNode["n1"].NumCores; got != 8 {
		t.Errorf("ComputeLayout[n1].NumCores = %d, want 8 (6 + the full 2-core deficit, since n2 is frozen "+
			"and cannot absorb any of it)", got)
	}
	if got := byNode["n1"].HugepagesMiB; got <= 18000 {
		t.Errorf("ComputeLayout[n1].HugepagesMiB = %d, want > 18000 — a grown container must carry "+
			"hugepages re-derived for its new core count", got)
	}
}

// afdShortfallFleet builds a 2-drive node (4 required compute cores at the 2.0 ratio) plus an existing
// 1-core compute container on "e1" and optionally a free compute node "f1"; cpuE1/cpuF1 set each's core
// headroom (a free node reserves one core for management, an existing container's does not).
func afdShortfallFleet(cpuE1, cpuF1 int) (inv []NodeCapacity, eligible map[string]bool, existing []ExistingComputeContainer) {
	const big = 1 << 28
	inv = []NodeCapacity{
		{ // 2 drives -> 2 drive cores; deliberately not compute-eligible
			NodeName: "drv", FDValue: "fdDrv",
			DriveCapacitiesGiB: afdDrives(2, 5000), TlcGiB: 10000,
			AllocatableCPU: 64, AvailableHugepagesMiB: big, AvailableMemoryMiB: big,
		},
		{
			NodeName: "e1", FDValue: "fdE",
			AllocatableCPU: cpuE1, AvailableHugepagesMiB: big, AvailableMemoryMiB: big,
		},
	}
	eligible = map[string]bool{"e1": true}
	if cpuF1 > 0 {
		inv = append(inv, NodeCapacity{
			NodeName: "f1", FDValue: "fdF",
			AllocatableCPU: cpuF1, AvailableHugepagesMiB: big, AvailableMemoryMiB: big,
		})
		eligible["f1"] = true
	}
	existing = []ExistingComputeContainer{{Name: "ec1", Node: "e1", NumCores: 1, HugepagesMiB: 1600}}
	return inv, eligible, existing
}

func afdComputeCores(layout []ComputeContainerSpec) map[string]int {
	out := map[string]int{}
	for _, l := range layout {
		out[l.Node] = l.NumCores
	}
	return out
}

// The documented order is new containers first, in-place growth only for what they cannot carry, and
// infeasible only once both levers together fall short. The middle row is the one that matters: a free
// node too small to absorb the whole shortfall must still be used for as much as it can take, with
// growth covering the rest.
func TestPlanAutoFullDrives_ComputeShortfall_PrefersNewThenGrows(t *testing.T) {
	for _, tc := range []struct {
		name           string
		cpuE1, cpuF1   int
		wantInfeasible bool
		wantCores      map[string]int // node -> cores, over the whole final layout
	}{
		{
			// f1 can take all 3 missing cores, so nothing is grown.
			name:  "free node covers the whole shortfall",
			cpuE1: 9, cpuF1: 5,
			wantCores: map[string]int{"e1": 1, "f1": 3},
		},
		{
			// f1 can take exactly 1 core, so growth must supply the other 2 — and no more than 2.
			name:  "free node too small: growth tops up the remainder",
			cpuE1: 2, cpuF1: 2,
			wantCores: map[string]int{"e1": 3, "f1": 1},
		},
		{
			// No free node at all: growth is the only lever.
			name:      "no free node: growth alone",
			cpuE1:     3,
			wantCores: map[string]int{"e1": 4},
		},
		{
			// 1 core from f1 plus 1 from growth cannot cover 3.
			name:  "neither lever suffices",
			cpuE1: 1, cpuF1: 2,
			wantInfeasible: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inv, eligible, existing := afdShortfallFleet(tc.cpuE1, tc.cpuF1)
			plan := PlanAutoFullDrives(AutoFullDrivesDesired{}, nil, existing, inv, eligible, testCons())

			if tc.wantInfeasible {
				if plan.Infeasible == "" {
					t.Fatalf("want infeasible, got layout %+v", plan.ComputeLayout)
				}
				return
			}
			if plan.Infeasible != "" {
				t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
			}
			if plan.RequiredComputeCores != 4 {
				t.Fatalf("RequiredComputeCores = %d, want 4 (2 drive cores x the 2.0 ratio)", plan.RequiredComputeCores)
			}
			got := afdComputeCores(plan.ComputeLayout)
			if len(got) != len(tc.wantCores) {
				t.Fatalf("ComputeLayout = %+v, want %d entries", plan.ComputeLayout, len(tc.wantCores))
			}
			total := 0
			for node, want := range tc.wantCores {
				if got[node] != want {
					t.Errorf("%s has %d core(s), want %d (layout %+v)", node, got[node], want, plan.ComputeLayout)
				}
				total += got[node]
			}
			if total != plan.RequiredComputeCores {
				t.Errorf("layout supplies %d core(s), want exactly the required %d", total, plan.RequiredComputeCores)
			}
		})
	}
}

// TestPlanAutoFullDrives_ComputeShortfall_GrowthHugepagesUseFinalContainerCount pins the divisor fix: when
// a shortfall splits between growing an existing container and placing a new one, the grown container's
// hugepages must be sized against the plan's final steady-state count (kept + new) — the same count used
// to size the new container itself.
func TestPlanAutoFullDrives_ComputeShortfall_GrowthHugepagesUseFinalContainerCount(t *testing.T) {
	cons := testCons()
	cons.ComputeHugepagesTlcRatio = 1024 // makes the capacity-based hugepages term sensitive to the divisor

	// Same shape as the "free node too small" case above: e1 grows from 1 to 3 cores, f1 is created at 1
	// core — one kept container, one new, so the final total is 2.
	inv, eligible, existing := afdShortfallFleet(2, 2)
	plan := PlanAutoFullDrives(AutoFullDrivesDesired{}, nil, existing, inv, eligible, cons)

	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	byNode := map[string]ComputeContainerSpec{}
	for _, l := range plan.ComputeLayout {
		byNode[l.Node] = l
	}
	if byNode["e1"].NumCores != 3 || byNode["f1"].NumCores != 1 {
		t.Fatalf("ComputeLayout = %+v, want e1=3 cores (grown) f1=1 core (new)", plan.ComputeLayout)
	}

	// drv's 2x5000 GiB drives are a fresh Create, so they are the plan's only source of totalTlcGiB.
	const totalTlcGiB = 10000
	const finalCount = 2 // 1 kept (e1) + 1 new (f1)

	wantGrown := ComputeContainerHugepagesMiB(totalTlcGiB, 0, finalCount, 3, cons)
	if got := byNode["e1"].HugepagesMiB; got != wantGrown {
		t.Errorf("grown e1.HugepagesMiB = %d, want %d (ComputeContainerHugepagesMiB at the FINAL count %d, "+
			"matching what the derivation approved for the new container it grew alongside)",
			got, wantGrown, finalCount)
	}
	wantNew := ComputeContainerHugepagesMiB(totalTlcGiB, 0, finalCount, 1, cons)
	if got := byNode["f1"].HugepagesMiB; got != wantNew {
		t.Errorf("new f1.HugepagesMiB = %d, want %d", got, wantNew)
	}

	// Pins the growth-hugepages divisor: it must use the final count 2, not len(kept)=1.
	if buggy := ComputeContainerHugepagesMiB(totalTlcGiB, 0, 1, 3, cons); byNode["e1"].HugepagesMiB == buggy {
		t.Errorf("grown e1.HugepagesMiB = %d equals the len(kept)=1 divisor result %d — the fix regressed",
			byNode["e1"].HugepagesMiB, buggy)
	}
}

// The node walk runs creates and growths through one path, a create being a growth from the zero footprint.
// This pins both halves of that merge: identical nodes size identically whether or not a container already
// exists, and the never-shrink ratchet still holds for a container already larger than the derived size.
func TestPlanAutoFullDrives_CreateAndGrowthSizeIdentically(t *testing.T) {
	const big = 1 << 28
	driveNode := func(name string, own, free []int) NodeCapacity {
		return NodeCapacity{
			NodeName: name, FDValue: name,
			OwnDriveCapacitiesGiB: own, DriveCapacitiesGiB: free, TlcGiB: sumInts(free),
			AllocatableCPU: 64, AvailableHugepagesMiB: big, AvailableMemoryMiB: big,
		}
	}

	// n1 has no container and 4 free drives; n2 already holds a 4-drive container that has all 4 as `own`.
	// Both must end up describing a 4-drive/4-core container.
	existing := []ExistingContainer{{Name: "c2", Node: "n2", NumCores: 4, NumDrives: 4}}
	plan := afdPlan(t, AutoFullDrivesDesired{}, existing,
		driveNode("n1", nil, afdDrives(4, 1000)),
		driveNode("n2", afdDrives(4, 1000), nil),
	)
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Create) != 1 || plan.Create[0].Node != "n1" {
		t.Fatalf("Create = %+v, want exactly one entry on n1", plan.Create)
	}
	if plan.Create[0].NumDrives != 4 || plan.Create[0].NumCores != 4 {
		t.Errorf("created container = %d drives/%d cores, want 4/4", plan.Create[0].NumDrives, plan.Create[0].NumCores)
	}
	// n2 is already at that size, so there is nothing to grow — the merged path must not emit a no-op growth.
	if len(plan.Grow) != 0 {
		t.Errorf("Grow = %+v, want empty (n2 already matches its derived size)", plan.Grow)
	}

	// Never shrink: a container holding more than the derived size keeps what it has, and is not grown.
	over := []ExistingContainer{{Name: "c3", Node: "n3", NumCores: 9, NumDrives: 9}}
	plan = afdPlan(t, AutoFullDrivesDesired{}, over, driveNode("n3", afdDrives(4, 1000), nil))
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Grow) != 0 {
		t.Errorf("Grow = %+v, want empty (a container above its derived size is never shrunk or rewritten)", plan.Grow)
	}
}

// Compute placement must be byte-identical across two plans of the same fleet, including when nodes tie on
// headroom: cores and hugepages are pod-spec fields, so an unstable order would recreate pods every reconcile.
func TestPlanAutoFullDrives_ComputePlacementIsDeterministic(t *testing.T) {
	const big = 1 << 28
	inv := []NodeCapacity{fdSpreadDriveNode(2)} // 2 drive cores -> 4 required compute cores
	eligible := map[string]bool{}
	// Four candidates, all tied on headroom, so only the node-name tiebreak can order them.
	for _, name := range []string{"cD", "cB", "cA", "cC"} {
		inv = append(inv, NodeCapacity{
			NodeName: name, FDValue: name,
			AllocatableCPU: 6, AvailableHugepagesMiB: big, AvailableMemoryMiB: big,
		})
		eligible[name] = true
	}

	first := PlanAutoFullDrives(AutoFullDrivesDesired{}, nil, nil, inv, eligible, testCons())
	if first.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", first.Infeasible)
	}
	for i := range 5 {
		again := PlanAutoFullDrives(AutoFullDrivesDesired{}, nil, nil, inv, eligible, testCons())
		if !reflect.DeepEqual(first.ComputeLayout, again.ComputeLayout) {
			t.Fatalf("pass %d produced a different ComputeLayout:\n first: %+v\n again: %+v", i, first.ComputeLayout, again.ComputeLayout)
		}
	}
}

// Every feasible plan from either planner must carry one ComputeLayout entry per compute container. The
// controller relies on this: applyPlannerComputeGrowth and buildPlannerComputeContainers read compute sizing
// exclusively from the layout, with no count-based fallback, so a plan reporting containers without a layout
// would silently create none.
func TestPlansCarryOneComputeLayoutEntryPerContainer(t *testing.T) {
	const big = 1 << 28
	cons := testCons()

	driveNodes := []NodeCapacity{{
		NodeName: "d1", FDValue: "d1",
		DriveCapacitiesGiB: afdDrives(4, 5000), TlcGiB: 20000,
		AllocatableCPU: 64, AvailableHugepagesMiB: big, AvailableMemoryMiB: big,
	}}
	computeInv, eligible := afdComputeNodes(6)

	plan := PlanAutoFullDrives(AutoFullDrivesDesired{}, nil, nil, append(driveNodes, computeInv...), eligible, cons)
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if plan.ComputeContainers == 0 {
		t.Fatal("fixture produced no compute containers, so the invariant would be vacuous")
	}
	if len(plan.ComputeLayout) != plan.ComputeContainers {
		t.Errorf("ComputeLayout has %d entries for %d compute container(s) — the controller would create the "+
			"difference as nothing at all", len(plan.ComputeLayout), plan.ComputeContainers)
	}
	for _, e := range plan.ComputeLayout {
		if e.Node == "" || e.NumCores <= 0 || e.HugepagesMiB <= 0 {
			t.Errorf("layout entry %+v is incomplete; every field is required to build or grow a container", e)
		}
	}
}

// §9a: TotalTlcDriveCores and RequiredComputeCores are set unconditionally, before the feasibility gate,
// so an infeasible plan's DriveSizingRationale reports the compute cores the claimed drives actually
// imply, instead of a phantom "needing 0 compute core(s)".
func TestPlanAutoFullDrives_Infeasible_ReportsRealComputeCoreDemand(t *testing.T) {
	cons := testCons()
	const bigFree = 1 << 28

	inv := []NodeCapacity{
		{
			NodeName: "d1", FDValue: "d1",
			DriveCapacitiesGiB: uniformDrives(10, 5120), TlcGiB: 51200,
			AllocatableCPU: 100, AvailableHugepagesMiB: bigFree, AvailableMemoryMiB: bigFree,
		},
	}
	computeNodes := computeNodeSet() // no compute-eligible nodes at all

	plan := PlanAutoFullDrives(AutoFullDrivesDesired{}, nil, nil, inv, computeNodes, cons)

	if plan.Infeasible == "" {
		t.Fatalf("expected infeasible plan (no compute-eligible nodes), got feasible: %+v", plan)
	}
	if plan.TotalTlcDriveCores <= 0 {
		t.Errorf("TotalTlcDriveCores = %d, want it set from the 10 claimed drives even though the plan is "+
			"infeasible", plan.TotalTlcDriveCores)
	}
	wantCores := RequiredComputeCores(plan.TotalTlcDriveCores, 0, true, cons)
	if plan.RequiredComputeCores != wantCores {
		t.Errorf("RequiredComputeCores = %d, want %d (derived from TotalTlcDriveCores=%d)",
			plan.RequiredComputeCores, wantCores, plan.TotalTlcDriveCores)
	}
	if plan.RequiredComputeCores <= 0 {
		t.Fatalf("RequiredComputeCores = %d, want a positive floor now that TotalTlcDriveCores is set", plan.RequiredComputeCores)
	}
	if plan.DriveSizing == nil {
		t.Fatalf("DriveSizing is nil even on infeasibility, want a populated rationale")
	}
	if plan.DriveSizing.TotalTlcDriveCores != plan.TotalTlcDriveCores || plan.DriveSizing.RequiredComputeCores != plan.RequiredComputeCores {
		t.Errorf("DriveSizing did not pick up the hoisted values: %+v", plan.DriveSizing)
	}
	phantom := "needing 0 compute core(s)"
	if strings.Contains(plan.DriveSizing.Reason, phantom) {
		t.Errorf("DriveSizing.Reason = %q, still reports the phantom zero-core demand", plan.DriveSizing.Reason)
	}
	wantSubstr := fmt.Sprintf("needing %d drive core(s) and %d compute core(s)",
		plan.TotalTlcDriveCores, plan.RequiredComputeCores)
	if !strings.Contains(plan.DriveSizing.Reason, wantSubstr) {
		t.Errorf("DriveSizing.Reason = %q, want it to contain %q", plan.DriveSizing.Reason, wantSubstr)
	}
}

// Unlike the sibling test above, here the node fails its own resource fit, so nothing is placed and
// plan.Create is empty — deriving the core demand from the plan legs would yield 0 and pair "6 of 6
// drive(s) would be claimed" with a zero core demand. The figures must come from the walk totals instead.
func TestPlanAutoFullDrives_InfeasibleDriveFit_ReportsWouldBeCoreDemand(t *testing.T) {
	cons := testCons()
	inv := []NodeCapacity{
		{
			NodeName: "d1", FDValue: "d1",
			DriveCapacitiesGiB: uniformDrives(6, 5120), TlcGiB: 30720,
			// Hugepages far below what a 6-drive container needs: the fit fails, so this node is never placed.
			AllocatableCPU: 100, AvailableHugepagesMiB: 1, AvailableMemoryMiB: 1 << 28,
		},
	}

	plan := PlanAutoFullDrives(AutoFullDrivesDesired{}, nil, nil, inv, computeNodeSet("c1"), cons)

	if plan.Infeasible == "" {
		t.Fatalf("expected infeasible plan (node cannot fit its own drives), got feasible: %+v", plan)
	}
	if len(plan.Create) != 0 || len(plan.Grow) != 0 {
		t.Fatalf("expected an empty plan (nothing placed), got Create=%d Grow=%d — this test no longer covers "+
			"the empty-plan case it exists for", len(plan.Create), len(plan.Grow))
	}
	// One core per drive, 6 drives, under the per-container cap.
	if plan.TotalTlcDriveCores != 6 {
		t.Errorf("TotalTlcDriveCores = %d, want 6 from the 6 drives the walk claimed", plan.TotalTlcDriveCores)
	}
	wantCompute := RequiredComputeCores(6, 0, true, cons)
	if plan.RequiredComputeCores != wantCompute {
		t.Errorf("RequiredComputeCores = %d, want %d", plan.RequiredComputeCores, wantCompute)
	}
	if plan.RequiredComputeCores <= 0 {
		t.Fatalf("RequiredComputeCores = %d, want positive: the claimed drives imply real compute demand even "+
			"though nothing is created", plan.RequiredComputeCores)
	}
	if plan.DriveSizing == nil {
		t.Fatalf("DriveSizing is nil on infeasibility, want a populated rationale")
	}
	if strings.Contains(plan.DriveSizing.Reason, "needing 0 drive core(s)") ||
		strings.Contains(plan.DriveSizing.Reason, "and 0 compute core(s)") {
		t.Errorf("DriveSizing.Reason = %q, still reports a phantom zero core demand", plan.DriveSizing.Reason)
	}
	wantSubstr := fmt.Sprintf("needing %d drive core(s) and %d compute core(s)", 6, wantCompute)
	if !strings.Contains(plan.DriveSizing.Reason, wantSubstr) {
		t.Errorf("DriveSizing.Reason = %q, want it to contain %q", plan.DriveSizing.Reason, wantSubstr)
	}
}
