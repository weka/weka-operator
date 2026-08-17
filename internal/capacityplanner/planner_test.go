package capacityplanner

import (
	"strings"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"

	"github.com/weka/weka-operator/pkg/util"
)

// Each test documents a clusterCapacity planning scenario and asserts the planner's decisions; capacities are in GiB.

const (
	tib = 1024 // 1 TiB in GiB
)

func testScheme() ProtectionScheme {
	return ProtectionScheme{StripeWidth: 3, RedundancyLevel: 2, HotSpare: 1}
} // minFdNum = 6

func testCons() *CapacityConstraints {
	return &CapacityConstraints{
		TlcCapacityPerCoreGiB: 5 * tib,
		QlcCapacityPerCoreGiB: 50 * tib,
		MinChunkSizeGiB:       384,
		ImbalanceFactor:       2.0,
		HugepagesPerCoreMiB:   1600,
		MemoryBaseMiB:         8000,
		MemoryPerCoreMiB:      3000,
		// Default matches enableDynamicDriveScalingForSharedDrives=true; disabled-flag scenarios override to false.
		AllowInPlaceGrowth: true,
		// Mirrors helm/config production defaults so the uniform-increase path exercises real fractions/ratios.
		MinGrowthFraction:                 0.2,
		MaxOverProvisionFraction:          0.2,
		MaxCoresPerContainer:              DefaultMaxCoresPerContainer,
		ComputeToTlcDriveCoreRatio:        1.0,
		ComputeToQlcDriveCoreRatio:        0.0,
		FullDrivesComputeToDriveCoreRatio: 2.0,
	}
}

func ratio(tlc, qlc int) *weka.DriveTypesRatio { return &weka.DriveTypesRatio{Tlc: tlc, Qlc: qlc} }

// allEligible marks every inventory node as compute-eligible; dedicated compute-node-pool tests pass an explicit map instead.
func allEligible(inv []NodeCapacity) map[string]bool {
	m := make(map[string]bool, len(inv))
	for _, nc := range inv {
		m[nc.NodeName] = true
	}
	return m
}

// netCompute subtracts each existing compute container's footprint from its node's headroom, mirroring production's buildNodeInventory.
func netCompute(inv []NodeCapacity, existingCompute []ExistingComputeContainer, cons *CapacityConstraints) []NodeCapacity {
	idx := make(map[string]int, len(inv))
	for i := range inv {
		idx[inv[i].NodeName] = i
	}
	out := append([]NodeCapacity(nil), inv...)
	for _, ec := range existingCompute {
		i, ok := idx[ec.Node]
		if !ok {
			continue
		}
		out[i].AllocatableCPU = max(0, out[i].AllocatableCPU-ec.NumCores)
		out[i].AvailableHugepagesMiB = max(0, out[i].AvailableHugepagesMiB-ec.HugepagesMiB)
		out[i].AvailableMemoryMiB = max(0, out[i].AvailableMemoryMiB-ComputeMemoryFootprintMiB(ec.NumCores, cons))
	}
	return out
}

// planCap wraps PlanCapacity with every node compute-eligible, matching the existing scenario tests.
func planCap(desired DesiredCapacity, scheme ProtectionScheme, existingDrives []ExistingContainer, inventory []NodeCapacity, cons *CapacityConstraints) CapacityPlan {
	return PlanCapacity(desired, scheme, existingDrives, nil, inventory, allEligible(inventory), cons)
}

// desiredFrom mirrors the controller: inflate usable to raw, then split by ratio.
func desiredFrom(usableGiB int, s ProtectionScheme, r *weka.DriveTypesRatio) DesiredCapacity {
	raw := RawCapacityGiB(usableGiB, s.StripeWidth, s.RedundancyLevel, s.HotSpare)
	tlc, qlc := weka.GetTlcQlcCapacity(raw, r)
	return DesiredCapacity{TlcRawGiB: tlc, QlcRawGiB: qlc}
}

// node builds a candidate node with generous hugepages/memory so only drive capacity and cores bind; FDValue defaults to the node name (AUTO mode).
func node(name string, tlcGiB, qlcGiB, cores int) NodeCapacity {
	return NodeCapacity{
		NodeName:              name,
		FDValue:               name,
		TlcGiB:                tlcGiB,
		QlcGiB:                qlcGiB,
		AllocatableCPU:        cores,
		AvailableHugepagesMiB: 1 << 28,
		AvailableMemoryMiB:    1 << 28,
	}
}

func nodes(n, tlcGiB, qlcGiB, cores int, prefix string) []NodeCapacity {
	out := make([]NodeCapacity, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, node(prefix+itoa(i), tlcGiB, qlcGiB, cores))
	}
	return out
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}

func sumCreateTlc(p CapacityPlan) (sum int) {
	for _, c := range p.Create {
		sum += c.TlcGiB
	}
	return
}
func sumCreateQlc(p CapacityPlan) (sum int) {
	for _, c := range p.Create {
		sum += c.QlcGiB
	}
	return
}

// singleParityScheme is weka 2+1+0: stripeWidth/data=2, redundancyLevel/parity=1, hotSpare=0 ⇒ minFdNum=3.
func singleParityScheme() ProtectionScheme {
	return ProtectionScheme{StripeWidth: 2, RedundancyLevel: 1, HotSpare: 0}
}

func Test_MinProtectionFloor_GatedBySingleParityFlag(t *testing.T) {
	if sw, rl, hs := MinProtectionFloor(false); sw != 3 || rl != 2 || hs != 0 {
		t.Fatalf("default floor must be 3/2/0, got %d/%d/%d", sw, rl, hs)
	}
	if sw, rl, hs := MinProtectionFloor(true); sw != 2 || rl != 1 || hs != 0 {
		t.Fatalf("single-parity floor must be 2/1/0, got %d/%d/%d", sw, rl, hs)
	}
}

func Test_SingleParity_RejectedWhenFlagOff(t *testing.T) {
	s := singleParityScheme()
	plan := planCap(
		desiredFrom(60*tib, s, ratio(0, 1)),
		s,
		nil,
		nodes(3, 0, 100*tib, 64, "q"),
		testCons(),
	)
	if !strings.Contains(plan.Infeasible, "stripeWidth>=3") {
		t.Fatalf("2+1+0 must be rejected by the default floor, got Infeasible=%q", plan.Infeasible)
	}
}

func Test_SingleParity_AcceptedWhenFlagOn_SpreadsAcross3FDs(t *testing.T) {
	s := singleParityScheme() // minFdNum = 3
	cons := testCons()
	cons.AllowSingleParity = true
	// 30 TiB usable, QLC-only. raw = usable × (2+1+0)/2 / 0.9 ≈ 1.667× → 50 TiB raw; ceil(50×1024/3)=17067 GiB/FD → total 51201 GiB across 3 FDs.
	plan := planCap(
		desiredFrom(30*tib, s, ratio(0, 1)),
		s,
		nil,
		nodes(3, 0, 100*tib, 64, "q"),
		cons,
	)
	if plan.Infeasible != "" {
		t.Fatalf("2+1+0 must be feasible with the flag on, got Infeasible=%q", plan.Infeasible)
	}
	if len(plan.Create) != 3 {
		t.Fatalf("want 3 QLC containers (one per FD = minFdNum), got %d", len(plan.Create))
	}
	if got := sumCreateQlc(plan); got != 51201 {
		t.Fatalf("want total created QLC raw 51201 GiB (50 TiB raw / 0.9 factor, 3 FDs ceil-rounded), got %d GiB", got)
	}
}

func Test_Greenfield_Homogeneous_TLOnly_SpreadsEvenlyAcrossMinFdNum(t *testing.T) {
	s := testScheme()
	plan := planCap(
		desiredFrom(90*tib, s, ratio(1, 0)), // rawTLC = 200 TiB (= 90×2/0.9; ceil/6 = 34134 GiB/FD)
		s,
		nil,
		nodes(6, 100*tib, 0, 64, "n"),
		testCons(),
	)
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Grow) != 0 {
		t.Fatalf("greenfield should grow nothing, got %d", len(plan.Grow))
	}
	if len(plan.Create) != 6 {
		t.Fatalf("want 6 TLC containers (one per FD), got %d", len(plan.Create))
	}
	if got := sumCreateTlc(plan); got != 204804 {
		t.Fatalf("want total created TLC 204804 GiB (200 TiB raw / 0.9 factor, 6 FDs ceil-rounded), got %d GiB", got)
	}
	for _, c := range plan.Create {
		if c.Type != DriveTypeTLC || c.QlcGiB != 0 || c.TlcGiB != 34134 {
			t.Fatalf("want each container TLC=34134 GiB (ceil(204800/6)) type=tlc, got %+v", c)
		}
	}
}

func Test_Greenfield_MoreNodesThanMinFd_UsesMinFdNum(t *testing.T) {
	s := testScheme() // minFdNum = 6
	// 12 capable nodes, but target fits within minFdNum: must create exactly 6, not spread across all 12.
	plan := planCap(
		desiredFrom(90*tib, s, ratio(1, 0)), // rawTLC = 200 TiB (= 90×2/0.9; ceil/6 = 34134 GiB/FD)
		s,
		nil,
		nodes(12, 100*tib, 0, 64, "n"),
		testCons(),
	)
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Create) != 6 {
		t.Fatalf("want exactly minFdNum (6) containers, got %d", len(plan.Create))
	}
	for _, c := range plan.Create {
		if c.TlcGiB != 34134 { // ceil(204800 GiB / 6 FDs) = 34134 GiB
			t.Fatalf("want each container TLC=34134 GiB (ceil(204800/6)), got %+v", c)
		}
	}
}

func Test_Greenfield_MinFdNumTooSmall_ExtendsFdCount(t *testing.T) {
	s := testScheme() // minFdNum = 6
	// 20 TiB/node: minFdNum FDs (120 TiB) can't hold rawTLC 200 TiB, so it extends to 10 FDs × 20 TiB.
	plan := planCap(
		desiredFrom(90*tib, s, ratio(1, 0)), // rawTLC = 200 TiB (= 90×2/0.9 = 204800 GiB)
		s,
		nil,
		nodes(12, 20*tib, 0, 64, "n"),
		testCons(),
	)
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Create) != 10 {
		t.Fatalf("want 10 containers (extended from minFdNum to fit 204800 GiB on 20 TiB nodes), got %d", len(plan.Create))
	}
	if got := sumCreateTlc(plan); got != 204800 {
		t.Fatalf("want total created TLC 204800 GiB (200 TiB raw / 0.9 factor, exactly 10 × 20 TiB), got %d GiB", got)
	}
}

func Test_Greenfield_Heterogeneous_AddsFdToBalance(t *testing.T) {
	s := testScheme() // minFdNum = 6
	// 2 big + 5 medium nodes: stopping at minFdNum=6 would fill unevenly, so add-FD-until-even opens a
	// 7th FD at 60 TiB each instead, with no imbalance warning (issue #13).
	inv := append(nodes(2, 100*tib, 0, 64, "big"), nodes(5, 64*tib, 0, 64, "med")...)
	plan := planCap(DesiredCapacity{TlcRawGiB: 420 * tib}, s, nil, inv, testCons())
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Create) != 7 {
		t.Fatalf("want 7 containers (extended from minFdNum to even out), got %d", len(plan.Create))
	}
	for _, c := range plan.Create {
		if c.TlcGiB != 60*tib { // 420 / 7, fits under every ceiling
			t.Fatalf("want every FD evenly 60 TiB, got %d on %s", c.TlcGiB, c.Node)
		}
	}
	if got := sumCreateTlc(plan); got != 420*tib {
		t.Fatalf("want total 420 TiB placed, got %d", got)
	}
}

func Test_Greenfield_TLCplusQLC_DisjointNodes(t *testing.T) {
	s := testScheme()
	inv := append(
		nodes(6, 100*tib, 0, 64, "t"),
		nodes(6, 0, 100*tib, 64, "q")...,
	)
	plan := planCap(desiredFrom(120*tib, s, ratio(1, 3)), s, nil, inv, testCons())
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	// raw ≈ 267 TiB (= 120×2/0.9 = 273066 GiB); tlcRaw = 273066/4 = 68266 GiB → ceil/6 = 11378 GiB/FD → total 68268 GiB; qlcRaw = 204800 GiB.
	if got := sumCreateTlc(plan); got != 68268 {
		t.Fatalf("want total TLC 68268 GiB (raw 273066/4=68266, ceil/6=11378 per FD), got %d", got)
	}
	if got := sumCreateQlc(plan); got != 204804 {
		t.Fatalf("want total QLC 204804 GiB (204800 raw / 0.9 factor, ceil/6=34134 per FD), got %d", got)
	}
	tlcCount, qlcCount := 0, 0
	for _, c := range plan.Create {
		switch c.Type {
		case DriveTypeTLC:
			tlcCount++
		case DriveTypeQLC:
			qlcCount++
		default:
			t.Fatalf("unexpected mixed container on disjoint nodes: %+v", c)
		}
	}
	if tlcCount != 6 || qlcCount != 6 {
		t.Fatalf("want 6 TLC + 6 QLC containers, got %d + %d", tlcCount, qlcCount)
	}
}

// QLC must co-locate onto the same nodes TLC took, not spread onto emptier nodes that would otherwise look more attractive.
func Test_Greenfield_TLCplusQLC_PrefersSameNode(t *testing.T) {
	s := testScheme() // minFdNum = 6
	inv := nodes(12, 100*tib, 100*tib, 64, "n")
	plan := planCap(desiredFrom(60*tib, s, ratio(1, 1)), s, nil, inv, testCons())
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Create) != 6 {
		t.Fatalf("want 6 mixed containers co-located on 6 nodes, got %d containers: %+v", len(plan.Create), plan.Create)
	}
	for _, c := range plan.Create {
		if c.Type != DriveTypeMixed || c.TlcGiB <= 0 || c.QlcGiB <= 0 {
			t.Fatalf("want every container mixed (TLC+QLC on the same node), got %+v", c)
		}
	}
}

// When no drive node has cores for both pools' per-FD share, co-location must fall back to a split (TLC
// on 6 nodes, QLC on 6 others); compute uses a dedicated diskless pool instead.
func Test_Greenfield_TLCplusQLC_SplitsWhenNodeCannotHoldBoth(t *testing.T) {
	s := testScheme() // minFdNum = 6
	// 4 cores/node = TLC's 3 data cores + 1 management core; once TLC lands, 0 remain for QLC.
	inv := append(nodes(12, 100*tib, 100*tib, 4, "d"), nodes(8, 0, 0, 32, "c")...)
	computeNodes := map[string]bool{}
	for i := 1; i <= 8; i++ {
		computeNodes["c"+itoa(i)] = true // only the diskless pool is compute-eligible
	}
	plan := PlanCapacity(desiredFrom(60*tib, s, ratio(1, 1)), s, nil, nil, inv, computeNodes, testCons())
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	tlcCount, qlcCount := 0, 0
	for _, c := range plan.Create {
		switch c.Type {
		case DriveTypeTLC:
			tlcCount++
		case DriveTypeQLC:
			qlcCount++
		default:
			t.Fatalf("unexpected mixed container when no node can hold both: %+v", c)
		}
	}
	if tlcCount != 6 || qlcCount != 6 {
		t.Fatalf("want 6 TLC + 6 QLC containers (split), got %d + %d", tlcCount, qlcCount)
	}
}

// Regression: new FDs must cover the delta with the fewest containers (largest per-FD up to maxPerFdCap),
// not anchor the count on the existing FD size: delta=2314, maxPerFdCap=1464 -> 2 replacements of 1157.
func Test_UniformIncrease_AntiFragmentation_FewestContainers(t *testing.T) {
	s := ProtectionScheme{StripeWidth: 3, RedundancyLevel: 2, HotSpare: 0} // minFd = 5
	cons := testCons()
	// 5 existing TLC FDs (mirrors the cluster after earlier deletes left an uneven set).
	sizes := []int{1250, 939, 939, 939, 939}
	var existingDrives []ExistingContainer
	var inv []NodeCapacity
	current := 0
	for i, sz := range sizes {
		n := "old" + itoa(i+1)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i+1), Node: n, FDValue: n, TlcGiB: sz, NumCores: 1})
		inv = append(inv, node(n, 0, 0, 64)) // drive-full: existing FDs cannot grow, so the delta is covered by new FDs
		current += sz
	}
	inv = append(inv, nodes(4, 100*tib, 0, 64, "new")...) // fresh nodes to host the replacements
	delta := 2314                                         // == 2×1157, as if two 1157-GiB TLC FDs were deleted
	plan := planCap(DesiredCapacity{TlcRawGiB: current + delta}, s, existingDrives, inv, cons)
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Grow) != 0 {
		t.Fatalf("want create-new (no grow), got grows: %+v", plan.Grow)
	}
	if len(plan.Create) != 2 {
		t.Fatalf("want 2 replacement FDs (fewest containers), not 3 smaller ones; got %d: %+v", len(plan.Create), plan.Create)
	}
	for _, c := range plan.Create {
		if c.TlcGiB != 1157 { // CeilDiv(2314, 2)
			t.Fatalf("want each new FD = 1157 GiB (CeilDiv(2314,2)), got %+v", c)
		}
	}
}

func Test_UniformIncrease_HeterogeneousReadd_RecreatesOneHighFD(t *testing.T) {
	s := testScheme() // minFdNum = 6
	cons := testCons()

	var existingDrives []ExistingContainer
	var inv []NodeCapacity

	// 6 small FDs left over from earlier heterogeneous growth.
	for i := 1; i <= 6; i++ {
		n := "small" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, TlcGiB: 512, NumCores: 1})
		inv = append(inv, node(n, 0, 0, 64)) // drive-full
	}
	// 5 high-tier FDs still present after one (of an original 6) was deleted.
	for i := 1; i <= 5; i++ {
		n := "high" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "h" + itoa(i), Node: n, FDValue: n, TlcGiB: 40960, NumCores: 4})
		inv = append(inv, node(n, 0, 0, 64)) // drive-full
	}
	// Fresh capacity: the freed node plus a spare, both empty with plenty of headroom.
	inv = append(inv, nodes(2, 100*tib, 0, 64, "new")...)

	desired := DesiredCapacity{TlcRawGiB: 6 * 40960} // target: 6 high FDs restored
	plan := planCap(desired, s, existingDrives, inv, cons)

	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Grow) != 0 {
		t.Fatalf("old nodes are drive-full; want 0 grows, got %d: %+v", len(plan.Grow), plan.Grow)
	}
	// A candidate size <= Tmax (the largest existing FD) is exempt from the fewest-containers search, so
	// k=1 recreates one high-tier FD.
	if len(plan.Create) != 1 {
		t.Fatalf("want exactly 1 new FD (recreate the deleted high-tier FD), got %d: %+v", len(plan.Create), plan.Create)
	}
	if plan.Create[0].TlcGiB < 37*tib {
		t.Fatalf("want the new FD sized to the high tier (>= 37 TiB), got %+v", plan.Create[0])
	}
	if len(plan.OverProvisions) != 0 {
		t.Fatalf("even-split reaches desired exactly; want no over-provision advisory, got %v", plan.OverProvisions)
	}
}

func Test_UniformIncrease_Homogeneous_ReaddSameSize_Unchanged(t *testing.T) {
	s := testScheme()
	cons := testCons()

	var existingDrives []ExistingContainer
	var inv []NodeCapacity
	for i := 1; i <= 5; i++ {
		n := "old" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, TlcGiB: 20 * tib, NumCores: 4})
		inv = append(inv, node(n, 0, 0, 64)) // drive-full
	}
	inv = append(inv, nodes(1, 100*tib, 0, 64, "new")...)

	desired := DesiredCapacity{TlcRawGiB: 120 * tib}
	plan := planCap(desired, s, existingDrives, inv, cons)

	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Grow) != 0 {
		t.Fatalf("old nodes are drive-full; want 0 grows, got %d: %+v", len(plan.Grow), plan.Grow)
	}
	if len(plan.Create) != 1 {
		t.Fatalf("want exactly 1 new FD (matches existing size), got %d: %+v", len(plan.Create), plan.Create)
	}
	if plan.Create[0].TlcGiB != 20*tib {
		t.Fatalf("want the new FD sized identically to existing FDs (20 TiB; T0==Tmax so the guard is a no-op), got %+v", plan.Create[0])
	}
	if len(plan.OverProvisions) != 0 {
		t.Fatalf("even-split reaches desired exactly; want no over-provision advisory, got %v", plan.OverProvisions)
	}
}

// Fresh nodes smaller than existing FDs must not go infeasible: the fewest-containers search falls back
// to more, smaller FDs that fit (k=2's 1157 doesn't fit 900-GiB nodes, k=3's 772 does).
func Test_UniformIncrease_SmallFreshNodes_StaysFeasible(t *testing.T) {
	s := ProtectionScheme{StripeWidth: 3, RedundancyLevel: 2, HotSpare: 0} // minFd = 5
	cons := testCons()
	sizes := []int{1250, 939, 939, 939, 939}
	var existingDrives []ExistingContainer
	var inv []NodeCapacity
	current := 0
	for i, sz := range sizes {
		n := "old" + itoa(i+1)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i+1), Node: n, FDValue: n, TlcGiB: sz, NumCores: 1})
		inv = append(inv, node(n, 0, 0, 64)) // drive-full: existing FDs cannot grow
		current += sz
	}
	inv = append(inv, nodes(5, 900, 0, 64, "new")...) // fresh nodes smaller than every existing FD
	delta := 2314
	plan := planCap(DesiredCapacity{TlcRawGiB: current + delta}, s, existingDrives, inv, cons)
	if plan.Infeasible != "" {
		t.Fatalf("small fresh nodes must still cover the delta (fell through to infeasible): %s", plan.Infeasible)
	}
	if len(plan.Grow) != 0 {
		t.Fatalf("existing FDs are drive-full; want create-new, got grows: %+v", plan.Grow)
	}
	if len(plan.Create) != 3 { // k=2 needs 1157>900 (skip) → k=3 perFd=772<=900
		t.Fatalf("want 3 new FDs of 772 (largest that fits the 900-GiB fresh nodes), got %d: %+v", len(plan.Create), plan.Create)
	}
	for _, c := range plan.Create {
		if c.TlcGiB > 900 {
			t.Fatalf("new FD must fit the 900-GiB fresh nodes, got %+v", c)
		}
	}
}

// QLC can only live on mixed-capable nodes, so it must be planned first; TLC then co-locates onto those same nodes instead of the tempting high-headroom TLC-only nodes.
func Test_Greenfield_TLCplusQLC_ConstrainedPoolFirst_CoLocates(t *testing.T) {
	s := ProtectionScheme{StripeWidth: 3, RedundancyLevel: 2, HotSpare: 0} // minFd = 5
	inv := append(
		nodes(6, 20000, 20000, 64, "m"), // 6 mixed-capable nodes (both drive types)
		nodes(8, 50000, 0, 64, "t")...,  // 8 TLC-only nodes with HIGHER TLC headroom (tempt TLC away)
	)
	plan := planCap(desiredFrom(30*tib, s, ratio(1, 1)), s, nil, inv, testCons())
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Create) != 5 { // minFd = 5 FDs, all co-located onto the mixed-capable nodes
		t.Fatalf("want 5 mixed containers co-located on the mixed-capable nodes, got %d: %+v", len(plan.Create), plan.Create)
	}
	for _, c := range plan.Create {
		if c.Type != DriveTypeMixed || c.TlcGiB <= 0 || c.QlcGiB <= 0 {
			t.Fatalf("want every container mixed (TLC co-located with QLC), got %+v", c)
		}
		if c.Node[0] != 'm' {
			t.Fatalf("co-located containers must land on the mixed-capable m* nodes, got node %q", c.Node)
		}
	}
}

// Co-location on the increase path: both pools create new FDs; TLC must co-locate onto QLC's new (mixed-capable) nodes via colocatedFirst instead of splitting onto higher-headroom TLC-only nodes.
func Test_UniformIncrease_TLCplusQLC_CoLocatesNewFDs(t *testing.T) {
	s := ProtectionScheme{StripeWidth: 3, RedundancyLevel: 2, HotSpare: 0} // minFd = 5
	cons := testCons()

	var existingDrives []ExistingContainer
	var inv []NodeCapacity
	for i := 1; i <= 5; i++ { // 5 QLC FDs on drive-full mixed nodes, 5 TLC FDs on drive-full TLC-only nodes
		m, tn := "m"+itoa(i), "t"+itoa(i)
		existingDrives = append(existingDrives,
			ExistingContainer{Name: "q" + itoa(i), Node: m, FDValue: m, QlcGiB: 3000, NumCores: 1},
			ExistingContainer{Name: "t" + itoa(i), Node: tn, FDValue: tn, TlcGiB: 3000, NumCores: 1})
		inv = append(inv, node(m, 0, 0, 64), node(tn, 0, 0, 64)) // drive-full: no in-place growth
	}
	inv = append(inv, nodes(4, 20000, 20000, 64, "mf")...) // fresh mixed-capable nodes (only QLC-capable fresh)
	inv = append(inv, nodes(4, 50000, 0, 64, "tf")...)     // fresh TLC-only nodes with HIGHER TLC headroom

	// Raise both pools by 6000 GiB (current 15000 each) → each needs 2 new FDs of 3000 (== largest existing).
	plan := planCap(DesiredCapacity{TlcRawGiB: 21000, QlcRawGiB: 21000}, s, existingDrives, inv, cons)
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	mixed, qlcOnly := 0, 0
	for _, c := range plan.Create {
		if c.QlcGiB > 0 && c.TlcGiB > 0 {
			mixed++
		} else if c.QlcGiB > 0 {
			qlcOnly++
		}
	}
	if qlcOnly != 0 {
		t.Fatalf("every new QLC FD should be co-located with TLC (mixed); got %d QLC-only new FDs: %+v", qlcOnly, plan.Create)
	}
	if mixed == 0 {
		t.Fatalf("want new TLC FDs co-located onto the new QLC (mixed) nodes; got none mixed: %+v", plan.Create)
	}
}

// Both pools short, scaling off, freed mixed node's old QLC container still terminating vs. a finalized
// freed TLC-only node: co-location must still win, landing TLC on the freed mixed node (regression for takeFreshAtLevel's ranking).
func Test_UniformIncrease_BothShort_CoLocatesOnFreedNode_EvenWhileTerminating(t *testing.T) {
	s := ProtectionScheme{StripeWidth: 3, RedundancyLevel: 2, HotSpare: 0} // minFd = 5
	cons := testCons()
	cons.AllowInPlaceGrowth = false // matches the live cluster default (no conversion of occupied nodes)
	var existingDrives []ExistingContainer
	var inv []NodeCapacity
	// 3 fully-mixed + 2 QLC-only occupied mixed nodes; 6 occupied TLC-only nodes. (QLC FDs=5, TLC FDs=9.)
	for _, n := range []string{"m1", "m2", "m3"} {
		existingDrives = append(existingDrives, ExistingContainer{Name: n, Node: n, FDValue: n, TlcGiB: 1250, QlcGiB: 3750, NumCores: 3})
		inv = append(inv, node(n, 41000, 53000, 60))
	}
	for _, n := range []string{"q1", "q2"} {
		existingDrives = append(existingDrives, ExistingContainer{Name: n, Node: n, FDValue: n, QlcGiB: 4053, NumCores: 1})
		inv = append(inv, node(n, 64000, 53000, 60))
	}
	for i := 1; i <= 6; i++ {
		n := "t" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: n, Node: n, FDValue: n, TlcGiB: 1157, NumCores: 1})
		inv = append(inv, node(n, 70000, 0, 60))
	}
	// Freed MIXED node whose old QLC container is STILL terminating; freed TLC-only node already finalized.
	freedMixed := node("fmix", 42000, 57000, 60)
	freedMixed.HasDeletingDriveContainer = true
	inv = append(inv, freedMixed, node("fh6", 71000, 0, 60))

	curT, curQ := 0, 0
	for _, c := range existingDrives {
		curT += c.TlcGiB
		curQ += c.QlcGiB
	}
	plan := planCap(DesiredCapacity{TlcRawGiB: curT + 1157, QlcRawGiB: curQ + 4053}, s, existingDrives, inv, cons)
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Grow) != 0 {
		t.Fatalf("scaling off: must not convert/grow, got %+v", plan.Grow)
	}
	// Exactly one fresh MIXED container on the freed mixed node — no split onto fh6.
	if len(plan.Create) != 1 {
		t.Fatalf("want 1 fresh mixed container co-located on the freed node, got %d: %+v", len(plan.Create), plan.Create)
	}
	c := plan.Create[0]
	if c.Node != "fmix" || c.TlcGiB <= 0 || c.QlcGiB <= 0 {
		t.Fatalf("want a mixed container on fmix (the freed mixed node), got %+v", c)
	}
}

// Existing QLC-only containers sit on mixed-capable nodes; only TLC is short. Co-location must not convert
// a running single-pool container to mixed — the new TLC FD is created fresh on an empty node instead.
func Test_UniformIncrease_TLC_DoesNotConvertExistingQLCOnlyNode(t *testing.T) {
	s := ProtectionScheme{StripeWidth: 3, RedundancyLevel: 2, HotSpare: 0} // minFd = 5
	cons := testCons()
	var existingDrives []ExistingContainer
	var inv []NodeCapacity
	for i := 1; i <= 5; i++ { // QLC-only containers on mixed-capable nodes m1..m5 (free TLC + QLC available)
		n := "m" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "q" + itoa(i), Node: n, FDValue: n, QlcGiB: 3000, NumCores: 1})
		inv = append(inv, node(n, 20000, 5000, 64))
	}
	for i := 1; i <= 5; i++ { // TLC-only containers on drive-full t1..t5 (can't grow in place)
		n := "t" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "t" + itoa(i), Node: n, FDValue: n, TlcGiB: 3000, NumCores: 1})
		inv = append(inv, node(n, 0, 0, 64))
	}
	inv = append(inv, nodes(2, 60000, 0, 64, "tempty")...) // empty TLC-only nodes for the fresh TLC FD

	// QLC not short (15000==current); TLC raised by 3000 → needs 1 more FD.
	plan := planCap(DesiredCapacity{TlcRawGiB: 18000, QlcRawGiB: 15000}, s, existingDrives, inv, cons)
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	// No existing QLC-only container may be converted to mixed (no in-place cross-pool grow).
	for _, g := range plan.Grow {
		if g.NewTlcGiB > 0 && g.NewQlcGiB > 0 {
			t.Fatalf("must NOT convert an existing single-pool container to mixed, got grow %+v", g)
		}
	}
	// The new TLC FD is created fresh (on an empty node), not co-located onto an occupied QLC-only node.
	if len(plan.Create) == 0 {
		t.Fatalf("want a fresh TLC container created, got none (grow=%+v)", plan.Grow)
	}
	for _, c := range plan.Create {
		if c.QlcGiB != 0 {
			t.Fatalf("fresh TLC FD must be TLC-only (no conversion), got %+v", c)
		}
	}
}

func Test_QLCpool_TooFewFDs_FailFast(t *testing.T) {
	s := testScheme()
	inv := append(nodes(6, 100*tib, 0, 64, "t"), nodes(5, 0, 100*tib, 64, "q")...) // only 5 QLC nodes
	plan := planCap(desiredFrom(120*tib, s, ratio(1, 3)), s, nil, inv, testCons())
	if plan.Infeasible == "" {
		t.Fatalf("want fail-fast infeasible for QLC pool with 5 < 6 FDs")
	}
	// The message must explain WHY each rejected node does not qualify, not just the FD shortfall count.
	for _, want := range []string{
		"QLC: only 5 of 6 required failure domains have capacity",
		"node(s) cannot host a QLC failure domain",
		"no QLC drive capacity",          // the 6 TLC-only nodes have zero QLC drives
		"t1, t2, t3, t4, t5, t6: no QLC", // identical reasons are grouped into one clause
	} {
		if !strings.Contains(plan.Infeasible, want) {
			t.Errorf("infeasible message %q missing %q", plan.Infeasible, want)
		}
	}
}

// labelNode builds a candidate node whose failure domain is an explicit label value (rack), distinct from
// the node name — several nodes can share one FD, which the AUTO-mode `node` helper can't express.
func labelNode(name, fd string, tlcGiB, cores int) NodeCapacity {
	nc := node(name, tlcGiB, 0, cores)
	nc.FDValue = fd
	return nc
}

// Label-based FD mode, 6 racks x 2 hosts each, uneven per-rack headroom: createSpread must select failure
// domains (not globally-largest-headroom nodes) and span >= minFd distinct FDs.
func Test_Greenfield_LabelBasedFD_MultiHostPerFD_SpansAllFDs(t *testing.T) {
	s := testScheme()                                                            // minFdNum = 6
	rackTlc := []int{75 * tib, 70 * tib, 65 * tib, 60 * tib, 55 * tib, 50 * tib} // rack-1..rack-6
	var inv []NodeCapacity
	for r := 1; r <= 6; r++ {
		fd := "rack-" + itoa(r)
		inv = append(inv,
			labelNode("h"+itoa(r)+"a", fd, rackTlc[r-1], 64),
			labelNode("h"+itoa(r)+"b", fd, rackTlc[r-1], 64),
		)
	}

	plan := planCap(desiredFrom(90*tib, s, ratio(1, 0)), s, nil, inv, testCons()) // rawTLC = 200 TiB (204800 GiB / 0.9 factor)

	if plan.Infeasible != "" {
		t.Fatalf("label-based multi-host greenfield must be feasible, got infeasible: %s", plan.Infeasible)
	}
	if len(plan.Grow) != 0 {
		t.Fatalf("greenfield should grow nothing, got %d", len(plan.Grow))
	}

	// The created containers must span all 6 distinct racks (>= minFd), not collapse onto fewer.
	perFD := map[string]int{}
	for _, c := range plan.Create {
		if c.Type != DriveTypeTLC || c.QlcGiB != 0 {
			t.Fatalf("want TLC-only containers, got %+v", c)
		}
		perFD[c.FDValue] += c.TlcGiB
	}
	if len(perFD) != 6 {
		t.Fatalf("want capacity spanning all 6 distinct failure domains, got %d: %v", len(perFD), perFD)
	}
	if got := sumCreateTlc(plan); got != 204804 {
		t.Fatalf("want total created TLC 204804 GiB (200 TiB raw / 0.9 factor, 6 FDs ceil-rounded), got %d GiB", got)
	}
	// Capacity is balanced PER FD: each rack carries the same 34134 GiB share (ceil(204800/6)).
	for fd, v := range perFD {
		if v != 34134 {
			t.Fatalf("FD %s holds %d GiB, want 34134 GiB equal across all FDs (per-FD balance): %v", fd, v, perFD)
		}
	}
}

// Same topology, but each FD needs more than one host to hold its share; the chosen FD set must still span exactly minFd distinct racks, using both hosts within a rack.
func Test_Greenfield_LabelBasedFD_UnevenHosts_UsesMultipleHostsPerFD(t *testing.T) {
	s := testScheme() // minFdNum = 6, 34134 GiB per FD for 204800 GiB raw
	// 6 racks, 2 hosts each, but each host holds only 20 TiB (< the 34134 GiB per-FD share). Both hosts in
	// a rack are needed to carry that rack's share; the plan must still land on exactly 6 racks.
	var inv []NodeCapacity
	for r := 1; r <= 6; r++ {
		fd := "rack-" + itoa(r)
		inv = append(inv,
			labelNode("h"+itoa(r)+"a", fd, 20*tib, 64),
			labelNode("h"+itoa(r)+"b", fd, 20*tib, 64),
		)
	}

	plan := planCap(desiredFrom(90*tib, s, ratio(1, 0)), s, nil, inv, testCons()) // rawTLC = 200 TiB (204800 GiB / 0.9 factor)

	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	perFD := map[string]int{}
	for _, c := range plan.Create {
		perFD[c.FDValue] += c.TlcGiB
	}
	if len(perFD) != 6 {
		t.Fatalf("want capacity spanning exactly 6 distinct failure domains, got %d: %v", len(perFD), perFD)
	}
	if got := sumCreateTlc(plan); got != 204804 {
		t.Fatalf("want total created TLC 204804 GiB (200 TiB raw / 0.9 factor, 6 FDs ceil-rounded), got %d GiB", got)
	}
	for fd, v := range perFD {
		if v != 34134 {
			t.Fatalf("FD %s holds %d GiB, want 34134 GiB per FD across two hosts: %v", fd, v, perFD)
		}
	}
	// More than one container per FD is expected here (20 TiB host cap < 34134 GiB per-FD share).
	if len(plan.Create) <= 6 {
		t.Fatalf("want multiple hosts per FD (>6 containers) to hold each 34134 GiB FD share, got %d", len(plan.Create))
	}
}

// disc #13: uneven host count per FD (rack-1: 3 hosts, racks 2-6: 2) must balance capacity per failure
// domain, not per node.
func Test_Greenfield_LabelBasedFD_UnevenHostCount_BalancesPerFD(t *testing.T) {
	s := testScheme() // minFdNum = 6, 34134 GiB per FD for 204800 GiB raw (200 TiB / 0.9 factor)
	var inv []NodeCapacity
	// rack-1: THREE hosts
	inv = append(inv,
		labelNode("h1a", "rack-1", 57*tib, 64),
		labelNode("h1b", "rack-1", 57*tib, 64),
		labelNode("h1c", "rack-1", 57*tib, 64),
	)
	// rack-2..rack-6: two hosts each
	for r := 2; r <= 6; r++ {
		fd := "rack-" + itoa(r)
		inv = append(inv,
			labelNode("h"+itoa(r)+"a", fd, 57*tib, 64),
			labelNode("h"+itoa(r)+"b", fd, 57*tib, 64),
		)
	}

	plan := planCap(desiredFrom(90*tib, s, ratio(1, 0)), s, nil, inv, testCons()) // rawTLC = 200 TiB (204800 GiB / 0.9 factor)

	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Grow) != 0 {
		t.Fatalf("greenfield should grow nothing, got %d", len(plan.Grow))
	}
	perFD := map[string]int{}
	perFDcount := map[string]int{}
	for _, c := range plan.Create {
		perFD[c.FDValue] += c.TlcGiB
		perFDcount[c.FDValue]++
	}
	if len(perFD) != 6 {
		t.Fatalf("want 6 distinct FDs, got %d: %v", len(perFD), perFD)
	}
	if got := sumCreateTlc(plan); got != 204804 {
		t.Fatalf("want total created TLC 204804 GiB (200 TiB raw / 0.9 factor, 6 FDs ceil-rounded), got %d GiB", got)
	}
	// disc #13: per-FD balance regardless of host count.
	for fd, v := range perFD {
		if v != 34134 {
			t.Fatalf("FD %s holds %d GiB, want 34134 GiB equal across all FDs (per-FD balance, not per-node): %v", fd, v, perFD)
		}
	}
	// rack-1 (3 hosts) splits its 34134 GiB share across all 3 hosts; the 2-host racks across 2.
	if perFDcount["rack-1"] != 3 {
		t.Fatalf("rack-1 (3 hosts) should split its 34134 GiB across 3 containers, got %d: %v", perFDcount["rack-1"], perFDcount)
	}
	for r := 2; r <= 6; r++ {
		fd := "rack-" + itoa(r)
		if perFDcount[fd] != 2 {
			t.Fatalf("%s (2 hosts) should split its 34134 GiB across 2 containers, got %d: %v", fd, perFDcount[fd], perFDcount)
		}
	}
}

// fdByNodeFromInv maps node name -> FDValue from an inventory slice (for asserting compute FD spread).
func fdByNodeFromInv(inv []NodeCapacity) map[string]string {
	m := make(map[string]string, len(inv))
	for _, nc := range inv {
		m[nc.NodeName] = nc.FDValue
	}
	return m
}

// Compute layout must span >= MinFdNum distinct FDs (6 racks x 2 hosts): free compute nodes are ordered
// for FD spread rather than picked by best-fit-by-cores alone.
func Test_Greenfield_LabelBasedFD_Compute_SpansMinFdNumFDs(t *testing.T) {
	s := testScheme() // SW=3, RL=2, HS=1 => minFdNum=6, compute must span >= MinFdNum = 6 FDs
	cons := testCons()
	cons.MaxCoresPerContainer = 19
	// 6 racks × 2 hosts. Uneven per-rack headroom (cores AND capacity) so a node-greedy best-fit would
	// concentrate compute onto the fattest racks (rack-1/rack-2), collapsing the distinct-FD count.
	rackTlc := []int{75 * tib, 70 * tib, 65 * tib, 60 * tib, 55 * tib, 50 * tib}
	rackCores := []int{64, 60, 48, 40, 36, 32}
	var inv []NodeCapacity
	for r := 1; r <= 6; r++ {
		fd := "rack-" + itoa(r)
		inv = append(inv,
			labelNode("h"+itoa(r)+"a", fd, rackTlc[r-1], rackCores[r-1]),
			labelNode("h"+itoa(r)+"b", fd, rackTlc[r-1], rackCores[r-1]),
		)
	}

	plan := planCap(desiredFrom(90*tib, s, ratio(1, 0)), s, nil, inv, cons)
	if plan.Infeasible != "" {
		t.Fatalf("label-based compute must be feasible, got infeasible: %s", plan.Infeasible)
	}
	fdByNode := fdByNodeFromInv(inv)
	computeFDs := map[string]struct{}{}
	for _, c := range plan.ComputeLayout {
		computeFDs[fdByNode[c.Node]] = struct{}{}
	}
	want := s.MinFdNum() // 6
	if len(computeFDs) < want {
		t.Fatalf("compute layout spans only %d distinct failure domains, want >= %d (MinFdNum): nodes=%v fds=%v",
			len(computeFDs), want, plan.ComputeNodes, computeFDs)
	}
	if len(plan.ComputeLayout) < s.MinFdNum() {
		t.Fatalf("want >= %d compute containers (count floor), got %d", s.MinFdNum(), len(plan.ComputeLayout))
	}
}

// Fail-fast: compute-eligible inventory with fewer than MinFdNum distinct FDs must surface a clear reason.
// Drives span all 6 racks (feasible); compute is restricted to only 5, one short of MinFdNum=6.
func Test_LabelBasedFD_Compute_TooFewFDs_FailsFast(t *testing.T) {
	s := testScheme() // drives need 6 FDs (feasible here); compute needs >= MinFdNum = 6 distinct FDs
	cons := testCons()
	var inv []NodeCapacity
	eligible := map[string]bool{}
	for r := 1; r <= 6; r++ {
		fd := "rack-" + itoa(r)
		a, b := "h"+itoa(r)+"a", "h"+itoa(r)+"b"
		inv = append(inv, labelNode(a, fd, 80*tib, 64), labelNode(b, fd, 80*tib, 64))
		if r <= 5 { // compute eligible on only 5 distinct racks — one short of MinFdNum=6
			eligible[a], eligible[b] = true, true
		}
	}
	plan := PlanCapacity(desiredFrom(60*tib, s, ratio(1, 0)), s, nil, nil, inv, eligible, cons)
	if plan.Infeasible == "" {
		t.Fatalf("want fail-fast infeasible: compute eligible on only 5 distinct FDs but needs MinFdNum=%d", s.MinFdNum())
	}
	if !strings.Contains(plan.Infeasible, "compute") || !strings.Contains(plan.Infeasible, "failure domain") {
		t.Fatalf("want a clear compute-FD infeasibility reason, got: %s", plan.Infeasible)
	}
}

// desiredDrive builds a DesiredCapacity with explicit drive sizing (driveContainers/driveCores).
func desiredDrive(usableGiB int, s ProtectionScheme, r *weka.DriveTypesRatio, driveContainers, driveCores int) DesiredCapacity {
	d := desiredFrom(usableGiB, s, r)
	d.DriveContainers = driveContainers
	d.DriveCores = driveCores
	return d
}

// --- explicit driveContainers: honored exactly, fail fast on constraint violation ---

func Test_DriveContainers_Exact_Greenfield_TLOnly(t *testing.T) {
	s := testScheme() // minFdNum = 6
	// 12 capable nodes, rawTLC = 200 TiB (204800 GiB / 0.9 factor), driveContainers=8 -> exactly 8 containers of 204800/8 = 25600 GiB each.
	plan := planCap(desiredDrive(90*tib, s, ratio(1, 0), 8, 0), s, nil, nodes(12, 100*tib, 0, 64, "n"), testCons())
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Create) != 8 {
		t.Fatalf("want exactly 8 drive containers, got %d", len(plan.Create))
	}
	if got := sumCreateTlc(plan); got != 204800 {
		t.Fatalf("want total TLC 204800 GiB (200 TiB raw / 0.9 factor, 8 containers of 25600 GiB), got %d", got)
	}
}

// Invariant guard: the pinned-driveContainers path must NOT grow an existing container in place when
// dynamic drive scaling is off; it reports infeasible instead.
func Test_DriveContainers_Pinned_ScalingOff_DoesNotGrowExisting(t *testing.T) {
	s := testScheme() // minFdNum = 6
	cons := testCons()
	cons.AllowInPlaceGrowth = false
	// 3 existing 10 TiB FDs + 4 spare nodes; driveContainers=6 needs a uniform T=15 TiB, which would
	// require growing the existing FDs — disabled, so the guard must report infeasible.
	var existingDrives []ExistingContainer
	var inv []NodeCapacity
	for i := 1; i <= 3; i++ {
		n := "old" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, TlcGiB: 10 * tib, NumCores: 2})
		inv = append(inv, node(n, 40*tib, 0, 64)) // headroom exists (growth would fit if allowed)
	}
	inv = append(inv, nodes(4, 100*tib, 0, 64, "new")...) // spare empty nodes for the fresh FDs
	// raw ~ 90 TiB, driveContainers=6 → T = 15 TiB/FD > existing 10 TiB → existing would need growth.
	plan := planCap(desiredDrive(90*tib, s, ratio(1, 0), 6, 0), s, existingDrives, inv, cons)
	if plan.Infeasible == "" {
		t.Fatalf("want infeasible (growth disabled, pinned per-FD exceeds existing), got Create=%+v Grow=%+v", plan.Create, plan.Grow)
	}
	if len(plan.Grow) != 0 {
		t.Fatalf("must not grow existing containers when scaling is off, got %+v", plan.Grow)
	}
	if !strings.Contains(plan.Infeasible, "growing it in place is disabled") {
		t.Fatalf("want a flag-aware infeasibility message, got %q", plan.Infeasible)
	}
}

func Test_DriveContainers_BelowMinFd_Infeasible(t *testing.T) {
	s := testScheme() // minFdNum = 6
	plan := planCap(desiredDrive(90*tib, s, ratio(1, 0), 5, 0), s, nil, nodes(12, 100*tib, 0, 64, "n"), testCons())
	if plan.Infeasible == "" {
		t.Fatalf("want infeasible for driveContainers=5 < minFdNum=6")
	}
}

func Test_DriveContainers_ExceedsAvailableFds_Infeasible(t *testing.T) {
	s := testScheme() // minFdNum = 6
	// driveContainers=8 but only 6 candidate nodes -> cannot reach 8 distinct FDs.
	plan := planCap(desiredDrive(90*tib, s, ratio(1, 0), 8, 0), s, nil, nodes(6, 100*tib, 0, 64, "n"), testCons())
	if plan.Infeasible == "" {
		t.Fatalf("want infeasible for driveContainers=8 with only 6 nodes")
	}
}

func Test_DriveContainers_Mixed_SplitByRatio(t *testing.T) {
	s := testScheme() // minFdNum = 6
	// rawTLC=60, qlcRaw=180 (ratio 1:3). driveContainers=24 -> split 6 TLC + 18 QLC by raw ratio.
	inv := append(nodes(6, 100*tib, 0, 64, "t"), nodes(18, 0, 100*tib, 64, "q")...)
	plan := planCap(desiredDrive(120*tib, s, ratio(1, 3), 24, 0), s, nil, inv, testCons())
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	tlcCount, qlcCount := 0, 0
	for _, c := range plan.Create {
		switch c.Type {
		case DriveTypeTLC:
			tlcCount++
		case DriveTypeQLC:
			qlcCount++
		}
	}
	if tlcCount != 6 || qlcCount != 18 {
		t.Fatalf("want 6 TLC + 18 QLC containers (24 split by ratio), got %d + %d", tlcCount, qlcCount)
	}
}

func Test_DriveContainers_Mixed_SplitBelowMinFd_Infeasible(t *testing.T) {
	s := testScheme() // minFdNum = 6
	// driveContainers=12, ratio 1:3 -> TLC share round(12*60/240)=3 < minFdNum -> fail fast.
	inv := append(nodes(6, 100*tib, 0, 64, "t"), nodes(18, 0, 100*tib, 64, "q")...)
	plan := planCap(desiredDrive(120*tib, s, ratio(1, 3), 12, 0), s, nil, inv, testCons())
	if plan.Infeasible == "" {
		t.Fatalf("want infeasible: ratio split drops TLC pool below minFdNum")
	}
}

// --- explicit driveCores: fixed per-container cores, fail fast when too small or unfittable ---

func Test_DriveCores_Honored(t *testing.T) {
	s := testScheme() // minFdNum = 6
	// 6 nodes, rawTLC 180 -> 30 TiB/container, derived cores = ceil(30/5)=6. Pin driveCores=8 (>=6).
	plan := planCap(desiredDrive(90*tib, s, ratio(1, 0), 0, 8), s, nil, nodes(6, 100*tib, 0, 64, "n"), testCons())
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Create) != 6 {
		t.Fatalf("want 6 containers, got %d", len(plan.Create))
	}
	for _, c := range plan.Create {
		if c.NumCores != 8 {
			t.Fatalf("want pinned NumCores=8, got %+v", c)
		}
	}
}

func Test_DriveCores_TooSmall_Infeasible(t *testing.T) {
	s := testScheme() // minFdNum = 6
	// 30 TiB/container needs 6 cores; driveCores=3 is too small -> fail fast.
	plan := planCap(desiredDrive(90*tib, s, ratio(1, 0), 0, 3), s, nil, nodes(6, 100*tib, 0, 64, "n"), testCons())
	if plan.Infeasible == "" {
		t.Fatalf("want infeasible for driveCores=3 too small for 30 TiB container")
	}
}

func Test_DriveCores_NodeLacksCores_Infeasible(t *testing.T) {
	s := testScheme() // minFdNum = 6
	// Nodes have only 8 cores; 30 TiB/container consumes 6, leaving 2. Pinning driveCores=10 needs 4
	// more than available -> fail fast on node fit.
	plan := planCap(desiredDrive(90*tib, s, ratio(1, 0), 0, 10), s, nil, nodes(6, 100*tib, 0, 8, "n"), testCons())
	if plan.Infeasible == "" {
		t.Fatalf("want infeasible: node cannot host the pinned driveCores")
	}
}

func Test_Grow_FitsInPlace_OnExistingNodes(t *testing.T) {
	s := testScheme()
	// 6 existing TLC containers at 30 TiB, on nodes that still have 70 TiB / 58 cores headroom.
	// desiredFrom(120 TiB) -> raw = int(float64(120*1024*2)/0.9) = 273066 GiB; per FD = ceil(273066/6) = 45511 GiB.
	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "n" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, TlcGiB: 30 * tib, NumCores: 6})
	}
	plan := planCap(desiredFrom(120*tib, s, ratio(1, 0)), s, existingDrives, nodes(6, 70*tib, 0, 58, "n"), testCons())
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Create) != 0 {
		t.Fatalf("growth should fit in place, want 0 created, got %d", len(plan.Create))
	}
	if len(plan.Grow) != 6 {
		t.Fatalf("want 6 in-place grows, got %d", len(plan.Grow))
	}
	for _, g := range plan.Grow {
		if g.NewTlcGiB != 45511 { // 30 TiB existing -> 45511 GiB (= ceil(273066/6); raw 120 TiB usable = 273066 GiB / 0.9)
			t.Fatalf("want grown TLC 45511 GiB, got %+v", g)
		}
	}
}

// One FD spans two hosts, the rest one; balancing must equalize per failure domain, not per container, or the 2-host FD would grow to ~2x the others.
func Test_Grow_LabelBasedFD_BalancesPerFailureDomain_NotPerContainer(t *testing.T) {
	s := testScheme() // minFdNum = 6

	// r1 has two hosts (each running a 10 TiB container, so 20 TiB total); r2..r6 have one host = 10 TiB — pre-existing skew.
	existingDrives := []ExistingContainer{
		{Name: "c1a", Node: "n1a", FDValue: "r1", TlcGiB: 10 * tib, NumCores: 2},
		{Name: "c1b", Node: "n1b", FDValue: "r1", TlcGiB: 10 * tib, NumCores: 2},
	}
	for i := 2; i <= 6; i++ {
		n := "n" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: "r" + itoa(i), TlcGiB: 10 * tib, NumCores: 2})
	}

	// Ample per-node headroom so placement is gated only by the per-FD target (FD identity lives on existingDrives above).
	inv := []NodeCapacity{node("n1a", 100*tib, 0, 58), node("n1b", 100*tib, 0, 58)}
	for i := 2; i <= 6; i++ {
		inv = append(inv, node("n"+itoa(i), 100*tib, 0, 58))
	}

	// current TLC = 70 TiB; target raw 180 TiB ⇒ delta 110 ⇒ post-grow target 180/6 = 30 TiB per FD.
	plan := planCap(DesiredCapacity{TlcRawGiB: 180 * tib}, s, existingDrives, inv, testCons())
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Create) != 0 {
		t.Fatalf("growth should fit in place, want 0 created, got %d", len(plan.Create))
	}

	// Final per-FD TLC (existing as grown). Every FD must converge to the SAME 30 TiB.
	grown := map[string]int{}
	for _, g := range plan.Grow {
		grown[g.Name] = g.NewTlcGiB
	}
	perFD := map[string]int{}
	for _, e := range existingDrives {
		v := e.TlcGiB
		if ng, ok := grown[e.Name]; ok {
			v = ng
		}
		perFD[e.FDValue] += v
	}
	if len(perFD) != 6 {
		t.Fatalf("want 6 failure domains, got %d", len(perFD))
	}
	for fd, v := range perFD {
		if v != 30*tib {
			t.Fatalf("FD %s holds %d GiB, want 30 TiB equal across all FDs (per-FD balance): %v", fd, v, perFD)
		}
	}
}

func Test_Grow_TlcOnlyContainer_ConvertedToMixed_WhenNodeHasQlc(t *testing.T) {
	s := testScheme()
	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "n" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, TlcGiB: 28 * tib, NumCores: 6})
	}
	// Same nodes also expose QLC headroom. TLC stays the same; QLC must grow.
	inv := nodes(6, 0, 100*tib, 58, "n") // TLC headroom 0 (full), QLC available
	desired := DesiredCapacity{TlcRawGiB: 6 * 28 * tib, QlcRawGiB: 6 * 28 * tib}
	plan := planCap(desired, s, existingDrives, inv, testCons())
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Create) != 0 {
		t.Fatalf("conversion should not create new containers, got %d", len(plan.Create))
	}
	if len(plan.Grow) != 6 {
		t.Fatalf("want 6 conversions, got %d", len(plan.Grow))
	}
	for _, g := range plan.Grow {
		if g.NewTlcGiB != 28*tib || g.NewQlcGiB != 28*tib {
			t.Fatalf("want converted to mixed TLC28+QLC28, got %+v", g)
		}
	}
}

// Cross-pool conversion on the increase path: a QLC increase with no spare empty node converts TLC-only containers on QLC-capable nodes to mixed, without double-counting cap-0 nodes.
func Test_UniformIncrease_QlcConvertsTlcOnlyContainer_ToMixed(t *testing.T) {
	s := testScheme() // minFdNum = 6
	var existingDrives []ExistingContainer
	var inv []NodeCapacity
	// 6 mixed FDs already carrying QLC (so poolExistingFds(QLC) > 0 → the increase path), QLC-full nodes.
	for i := 1; i <= 6; i++ {
		n := "m" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "m" + itoa(i), Node: n, FDValue: n, TlcGiB: 28 * tib, QlcGiB: 20 * tib, NumCores: 7})
		inv = append(inv, node(n, 0, 0, 58))
	}
	// 2 TLC-only FDs on nodes that DO expose QLC headroom — candidates for conversion, not yet QLC FDs.
	for i := 1; i <= 2; i++ {
		n := "t" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "t" + itoa(i), Node: n, FDValue: n, TlcGiB: 28 * tib, NumCores: 6})
		inv = append(inv, node(n, 0, 50*tib, 58))
	}
	// TLC stays put (224 = 8×28); QLC grows 120 → 160 (delta 40 = 2 × T0=20).
	desired := DesiredCapacity{TlcRawGiB: 8 * 28 * tib, QlcRawGiB: 160 * tib}
	plan := planCap(desired, s, existingDrives, inv, testCons())
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Create) != 0 {
		t.Fatalf("conversion should not create new containers, got %d: %+v", len(plan.Create), plan.Create)
	}
	if len(plan.Grow) != 2 {
		t.Fatalf("want exactly 2 TLC-only→mixed conversions, got %d: %+v", len(plan.Grow), plan.Grow)
	}
	for _, g := range plan.Grow {
		if g.NewTlcGiB != 28*tib || g.NewQlcGiB != 20*tib {
			t.Fatalf("want TLC-only converted to mixed TLC28+QLC20, got %+v", g)
		}
	}
}

func Test_RatioChange_SameTarget_GrowsOnePool_NoOpsOther(t *testing.T) {
	s := testScheme()
	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "n" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, TlcGiB: 10 * tib, QlcGiB: 20 * tib, NumCores: 5})
	}
	inv := nodes(6, 100*tib, 100*tib, 58, "n")
	// Flip ratio 1:2 -> 2:1 at same target: TLC grows 60->120, QLC shrinks 120->60 (no-op).
	desired := DesiredCapacity{TlcRawGiB: 120 * tib, QlcRawGiB: 60 * tib}
	plan := planCap(desired, s, existingDrives, inv, testCons())
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Create) != 0 {
		t.Fatalf("want no new containers, got %d", len(plan.Create))
	}
	if len(plan.ShrinkEvents) == 0 {
		t.Fatalf("want a QLC shrink event")
	}
	if len(plan.Grow) != 6 {
		t.Fatalf("want 6 TLC grows, got %d", len(plan.Grow))
	}
	for _, g := range plan.Grow {
		if g.NewTlcGiB != 20*tib || g.NewQlcGiB != 20*tib { // QLC unchanged (never shrunk)
			t.Fatalf("want TLC grown to 20 TiB and QLC unchanged at 20 TiB, got %+v", g)
		}
	}
}

func Test_Shrink_NoOp_EmitsDeleteEvent(t *testing.T) {
	s := testScheme()
	// 6×41 TiB = 251904 GiB existing; desired raw = int(float64(90*1024*2)/0.9) = 204800 GiB.
	// Overprovision = 47104 GiB > 20% of 204800 (= 40960 GiB) → shrink event emitted.
	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "n" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, TlcGiB: 41 * tib, NumCores: 8})
	}
	plan := planCap(desiredFrom(90*tib, s, ratio(1, 0)), s, existingDrives, nodes(6, 20*tib, 0, 20, "n"), testCons())
	if len(plan.Grow) != 0 || len(plan.Create) != 0 {
		t.Fatalf("shrink must be a no-op, got grow=%d create=%d", len(plan.Grow), len(plan.Create))
	}
	if len(plan.ShrinkEvents) == 0 {
		t.Fatalf("want a shrink event")
	}
}

// An over-provision within MaxOverProvisionFraction must not emit ClusterCapacityShrink — it would contradict ClusterCapacityOverProvisioned.
func Test_Shrink_WithinOverProvisionCap_NoEvent(t *testing.T) {
	s := testScheme()
	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "n" + itoa(i)
		// 6×34 TiB = 208896 GiB current vs 204800 GiB desired (90 TiB usable / 0.9): overage 4096 GiB < 20% cap (40960 GiB) → silent.
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, TlcGiB: 34 * tib, NumCores: 7})
	}
	plan := planCap(desiredFrom(90*tib, s, ratio(1, 0)), s, existingDrives, nodes(6, 20*tib, 0, 20, "n"), testCons())
	if len(plan.Grow) != 0 || len(plan.Create) != 0 {
		t.Fatalf("over-provisioned: must be a no-op, got grow=%d create=%d", len(plan.Grow), len(plan.Create))
	}
	if len(plan.ShrinkEvents) != 0 {
		t.Fatalf("overage within MaxOverProvisionFraction must NOT emit a shrink advisory, got %v", plan.ShrinkEvents)
	}
}

func Test_Idempotent_WhenCurrentEqualsDesired_EmptyPlan(t *testing.T) {
	s := testScheme()
	// desired raw = 204800 GiB, ceil/6 = 34134/FD; existing at 34134 each (204804 total) is within the
	// 20% overprovision cap, so the plan is empty.
	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "n" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, TlcGiB: 34134, NumCores: 7})
	}
	// desired raw == current 200 TiB (ceil-rounded to 204804 GiB).
	plan := planCap(desiredFrom(90*tib, s, ratio(1, 0)), s, existingDrives, nodes(6, 70*tib, 0, 58, "n"), testCons())
	if len(plan.Grow) != 0 || len(plan.Create) != 0 || plan.Infeasible != "" {
		t.Fatalf("want empty stable plan, got grow=%d create=%d infeasible=%q", len(plan.Grow), len(plan.Create), plan.Infeasible)
	}
}

func Test_MigrationFromContainerCapacity_CurrentCoversDesired_NoOp(t *testing.T) {
	s := testScheme()
	// Pre-existing containerCapacity containers (mixed) already exceed a modest clusterCapacity target.
	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "n" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, TlcGiB: 40 * tib, QlcGiB: 40 * tib, NumCores: 9})
	}
	plan := planCap(desiredFrom(60*tib, s, ratio(1, 1)), s, existingDrives, nodes(6, 10*tib, 10*tib, 20, "n"), testCons())
	if len(plan.Grow) != 0 || len(plan.Create) != 0 {
		t.Fatalf("migration adoption should be a no-op when current covers desired, got grow=%d create=%d", len(plan.Grow), len(plan.Create))
	}
}

// A tiny existing pool (T0=10 TiB) can't uniformly cover a +180 TiB increase on only 6 big fresh nodes, so
// the balancedFresh fallback abandons the small FDs (chunk would dwarf them) for a fresh uniform set, flagging the old containers deletable.
func Test_Heterogeneous_BalancedFresh_IgnoresExisting_WarnsDeleteOld(t *testing.T) {
	s := testScheme()
	// 6 small existing TLC containers (10 TiB each) on small nodes (near-full), plus 6 big empty nodes.
	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "small" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, TlcGiB: 10 * tib, NumCores: 2})
	}
	// Small nodes are near-full on drive capacity but still have CPU cores (independent headroom).
	inv := append(nodes(6, 5*tib, 0, 16, "small"), nodes(6, 100*tib, 0, 64, "big")...)
	desired := DesiredCapacity{TlcRawGiB: 240 * tib} // each fresh container ~40 TiB >= 2x existing 10 TiB
	plan := planCap(desired, s, existingDrives, inv, testCons())
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Grow) != 0 {
		t.Fatalf("balanced-fresh ignores existing, want 0 grows, got %d", len(plan.Grow))
	}
	if len(plan.Create) != 6 {
		t.Fatalf("want 6 fresh containers on big nodes, got %d", len(plan.Create))
	}
	for _, c := range plan.Create {
		if c.TlcGiB != 40*tib {
			t.Fatalf("want fresh containers of 40 TiB each (240/6), got %+v", c)
		}
	}
	if len(plan.Warnings) == 0 {
		t.Fatalf("want a heterogeneous-fallback warning suggesting deletion of old containers")
	}
}

// Same scenario as the IgnoresExisting test above but with scaling disabled: the fallback is a pure create op, so it fires regardless of AllowInPlaceGrowth and the plan must be identical.
func Test_Heterogeneous_BalancedFresh_FiresWithScalingDisabled(t *testing.T) {
	s := testScheme()
	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "small" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, TlcGiB: 10 * tib, NumCores: 2})
	}
	inv := append(nodes(6, 5*tib, 0, 16, "small"), nodes(6, 100*tib, 0, 64, "big")...)
	desired := DesiredCapacity{TlcRawGiB: 240 * tib}
	cons := testCons()
	cons.AllowInPlaceGrowth = false // dynamic drive scaling OFF — fallback must STILL fire (it only creates)
	plan := planCap(desired, s, existingDrives, inv, cons)
	if plan.Infeasible != "" {
		t.Fatalf("fallback must fire with scaling disabled (pure create), got infeasible: %s", plan.Infeasible)
	}
	if len(plan.Grow) != 0 {
		t.Fatalf("fallback never grows existing FDs, want 0 grows, got %d", len(plan.Grow))
	}
	if len(plan.Create) != 6 {
		t.Fatalf("want 6 fresh containers on big nodes, got %d", len(plan.Create))
	}
	for _, c := range plan.Create {
		if c.TlcGiB != 40*tib {
			t.Fatalf("want fresh containers of 40 TiB each (240/6), got %+v", c)
		}
	}
	if len(plan.Warnings) == 0 {
		t.Fatalf("want a heterogeneous-fallback warning even with scaling disabled")
	}
}

// Cover the delta with the FEWEST new FDs: maxPerFdCap=35 TiB, kMin=CeilDiv(90,35)=3, so 3 new FDs of 30
// TiB each — fewer, larger containers than the old 5x18 TiB even-split, with 0 over-provision.
func Test_Grow_PartialInPlace_RemainderCreatesNew(t *testing.T) {
	s := testScheme() // minFdNum = 6
	// 6 existing TLC containers on full nodes (no headroom) + 6 fresh empty nodes available.
	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "old" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, TlcGiB: 20 * tib, NumCores: 4})
	}
	// Old nodes are full on DRIVE capacity (0 GiB free) but retain CPU cores for compute containers.
	inv := append(nodes(6, 0, 0, 64, "old"), nodes(6, 100*tib, 0, 64, "new")...) // old nodes drive-full
	// current 120 TiB; raise to 210 TiB raw -> delta 90 TiB lands on the fresh nodes as the fewest FDs.
	desired := DesiredCapacity{TlcRawGiB: 210 * tib}
	plan := planCap(desired, s, existingDrives, inv, testCons())
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Grow) != 0 {
		t.Fatalf("old nodes are full; want 0 grows, got %d", len(plan.Grow))
	}
	if len(plan.Create) != 3 { // fewest: kMin=CeilDiv(90,maxPerFdCap=35)=3; perFd=CeilDiv(90,3)=30 TiB
		t.Fatalf("want 3 new FDs (30 TiB each — fewest containers), got %d: %v", len(plan.Create), plan.Create)
	}
	for _, c := range plan.Create {
		if c.TlcGiB != 30*tib {
			t.Fatalf("want each new FD at CeilDiv(delta=90 TiB, k=3)=30 TiB, got %+v", c)
		}
	}
	if got := sumCreateTlc(plan); got != 90*tib { // 3 × 30 TiB = the delta EXACTLY
		t.Fatalf("want 90 TiB created (fewest FDs cover delta), got %d", got)
	}
	if len(plan.OverProvisions) != 0 {
		t.Fatalf("even-split reaches desired exactly; want no over-provision advisory, got %v", plan.OverProvisions)
	}
}

// uniform-or-infeasible: 3 big + 3 small nodes can't tile 180 TiB uniformly (ceil(180/6)=30 TiB exceeds the
// small nodes' 25 TiB cap); a heterogeneous 35/25 fill would waste raw for the same usable, so it's never built.
func Test_Imbalance_CannotTileUniformly_Infeasible(t *testing.T) {
	s := testScheme() // minFdNum = 6
	inv := append(nodes(3, 100*tib, 0, 64, "big"), nodes(3, 25*tib, 0, 64, "small")...)
	plan := planCap(DesiredCapacity{TlcRawGiB: 180 * tib}, s, nil, inv, testCons())
	if plan.Infeasible == "" {
		t.Fatalf("want ClusterCapacityInfeasible (cannot tile 3×100+3×25 uniformly), got Create=%v", plan.Create)
	}
	if !strings.Contains(plan.Infeasible, "uniformly") {
		t.Fatalf("want a uniform-tiling infeasible message, got %q", plan.Infeasible)
	}
	if len(plan.Create) != 0 {
		t.Fatalf("infeasible plan must create nothing, got %d", len(plan.Create))
	}
}

func Test_Grow_SpreadsEvenlyAcrossAllExistingFDs(t *testing.T) {
	s := testScheme() // minFdNum = 6
	// 14 existing FDs (well above minFdNum=6): the delta must spread evenly across all 14 so they converge
	// to equal per-FD capacity.
	var existingDrives []ExistingContainer
	for i := 1; i <= 14; i++ {
		n := "n" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, TlcGiB: 30 * tib, NumCores: 6})
	}
	// current raw 420 TiB; raise to 560 TiB -> delta 140 TiB, evenly +10 TiB per FD -> 40 TiB each.
	desired := DesiredCapacity{TlcRawGiB: 560 * tib}
	plan := planCap(desired, s, existingDrives, nodes(14, 70*tib, 0, 64, "n"), testCons())
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Create) != 0 {
		t.Fatalf("growth fits in place across existing FDs, want 0 created, got %d", len(plan.Create))
	}
	if len(plan.Grow) != 14 {
		t.Fatalf("delta must land on ALL 14 existing FDs, got %d grows", len(plan.Grow))
	}
	for _, g := range plan.Grow {
		if g.NewTlcGiB != 40*tib {
			t.Fatalf("want every FD grown evenly to 40 TiB, got %+v", g)
		}
	}
}

func Test_Grow_RebalancesPreExistingSkew_TowardAverage(t *testing.T) {
	s := testScheme() // minFdNum = 6
	// Pre-existing skew (8 small + 6 large FDs): a further grow should top up smaller FDs more, converging toward equal per-FD capacity instead of preserving the skew.
	var existingDrives []ExistingContainer
	for i := 1; i <= 8; i++ {
		n := "small" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "cs" + itoa(i), Node: n, FDValue: n, TlcGiB: 13166, NumCores: 3})
	}
	for i := 1; i <= 6; i++ {
		n := "large" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "cl" + itoa(i), Node: n, FDValue: n, TlcGiB: 23406, NumCores: 5})
	}
	// current 245764 GiB; raise to 420000 GiB -> target 30000 GiB/FD across all 14.
	desired := DesiredCapacity{TlcRawGiB: 420000}
	inv := append(nodes(8, 50*tib, 0, 64, "small"), nodes(6, 50*tib, 0, 64, "large")...)
	plan := planCap(desired, s, existingDrives, inv, testCons())
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Create) != 0 {
		t.Fatalf("rebalance fits in place, want 0 created, got %d", len(plan.Create))
	}
	byName := map[string]int{}
	for _, g := range plan.Grow {
		byName[g.Name] = g.NewTlcGiB
	}
	// All FDs converge to the same post-grow capacity.
	for name, cap := range byName {
		if cap != 30000 {
			t.Fatalf("want every FD leveled to 30000 GiB, got %s=%d", name, cap)
		}
	}
	// The smaller FDs were topped up MORE than the larger ones (rebalancing, not preserving skew).
	smallTopUp := byName["cs1"] - 13166
	largeTopUp := byName["cl1"] - 23406
	if !(smallTopUp > largeTopUp) {
		t.Fatalf("smaller FDs must be topped up more than larger (small +%d, large +%d)", smallTopUp, largeTopUp)
	}
}

// Cover the increase on fresh FDs with the fewest containers, without growing existing ones: maxPerFdCap=
// 360/6=60 TiB, kMin=CeilDiv(270,60)=5, so 5 new FDs of CeilDiv(270,5)=54 TiB (within [T0=30, cap=60], below
// imbalance boundary 2xavg=60). Final layout: 3 existing (untouched) + 5 new = 8 FDs, fewer than the old 6x45.
func Test_Grow_ExistingFewerThanMinFd_TopsUpExistingAndCreatesNew(t *testing.T) {
	s := testScheme() // minFdNum = 6
	var existingDrives []ExistingContainer
	for i := 1; i <= 3; i++ {
		n := "old" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, TlcGiB: 30 * tib, NumCores: 6})
	}
	// Existing nodes have ample headroom; fresh nodes host the new FDs.
	inv := append(nodes(3, 30*tib, 0, 64, "old"), nodes(6, 100*tib, 0, 64, "new")...)
	// current 90 TiB; raise to 360 TiB -> delta 270 TiB, covered by fresh FDs only.
	desired := DesiredCapacity{TlcRawGiB: 360 * tib}
	plan := planCap(desired, s, existingDrives, inv, testCons())
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Grow) != 0 {
		t.Fatalf("fewest-FD cover on fresh FDs; want 0 grows, got %d: %+v", len(plan.Grow), plan.Grow)
	}
	if len(plan.Create) != 5 { // fewest: kMin=CeilDiv(270,maxPerFdCap=60)=5; perFd=CeilDiv(270,5)=54 TiB
		t.Fatalf("want 5 new FDs (54 TiB each — fewest containers), got %d: %v", len(plan.Create), plan.Create)
	}
	for _, c := range plan.Create {
		if c.TlcGiB != 54*tib { // CeilDiv(delta=270 TiB, k=5) = 54 TiB
			t.Fatalf("want new FDs at CeilDiv(delta=270 TiB, k=5)=54 TiB, got %+v", c)
		}
	}
	// Distinct FDs across the (untouched) existing + created must reach minFdNum.
	fds := map[string]struct{}{}
	for i := 1; i <= 3; i++ {
		fds["old"+itoa(i)] = struct{}{}
	}
	for _, c := range plan.Create {
		fds[c.FDValue] = struct{}{}
	}
	if len(fds) < 6 {
		t.Fatalf("want >= minFdNum (6) distinct FDs, got %d", len(fds))
	}
	if got := 90*tib + sumCreateTlc(plan); got != 360*tib { // current 90 + created 5×54=270 = 360 exactly
		t.Fatalf("want total raw 360 TiB placed, got %d (created %d)", got, sumCreateTlc(plan))
	}
}

// uniform-or-infeasible: headroom reduced by other clusters can't fit the per-FD share -> Infeasible.
func Test_OtherClustersReduceHeadroom_CannotTileUniformly_Infeasible(t *testing.T) {
	s := testScheme()
	inv := append(nodes(3, 100*tib, 0, 64, "big"), nodes(3, 25*tib, 0, 64, "small")...)
	desired := DesiredCapacity{TlcRawGiB: 180 * tib}
	plan := planCap(desired, s, nil, inv, testCons())
	if plan.Infeasible == "" {
		t.Fatalf("want ClusterCapacityInfeasible (smallest FD caps below the per-FD share), got Create=%v", plan.Create)
	}
	if len(plan.Create) != 0 {
		t.Fatalf("infeasible plan must create nothing, got %d", len(plan.Create))
	}
}

// --- node-resource-headroom gate (nodeHeadroom is the function the planner places against) ---

// stateFrom builds a nodeState whose remaining budgets equal the given NodeCapacity (full headroom).
func stateFrom(nc NodeCapacity) *nodeState {
	return &nodeState{
		nc:           nc,
		tlcFree:      nc.TlcGiB,
		qlcFree:      nc.QlcGiB,
		coresFree:    nc.AllocatableCPU,
		hugepagesMiB: nc.AvailableHugepagesMiB,
		memoryMiB:    nc.AvailableMemoryMiB,
	}
}

func Test_Headroom_BoundByDriveCapacity(t *testing.T) {
	// Plenty of cores/hugepages/memory; the 10 TiB of TLC drive is the binding resource.
	ns := stateFrom(NodeCapacity{TlcGiB: 10 * tib, AllocatableCPU: 64, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28})
	if got := ns.nodeHeadroom(poolTLC, testCons(), false); got != 10*tib {
		t.Fatalf("want headroom bound by drive capacity 10 TiB, got %d", got)
	}
}

func Test_Headroom_BoundByCores(t *testing.T) {
	// 2 cores × 5 TiB/core TLC = 10 TiB, below the 50 TiB of drive capacity.
	ns := stateFrom(NodeCapacity{TlcGiB: 50 * tib, AllocatableCPU: 2, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28})
	if got := ns.nodeHeadroom(poolTLC, testCons(), false); got != 10*tib {
		t.Fatalf("want headroom bound by cores (2×5TiB=10TiB), got %d", got)
	}
}

func Test_Headroom_BoundByHugepages(t *testing.T) {
	// hugepages allow only 2 cores (3200/1600), so 2×5 TiB = 10 TiB.
	ns := stateFrom(NodeCapacity{TlcGiB: 50 * tib, AllocatableCPU: 64, AvailableHugepagesMiB: 3200, AvailableMemoryMiB: 1 << 28})
	if got := ns.nodeHeadroom(poolTLC, testCons(), false); got != 10*tib {
		t.Fatalf("want headroom bound by hugepages (2 cores → 10TiB), got %d", got)
	}
}

func Test_Headroom_BoundByMemory_WithBaseReservation(t *testing.T) {
	// includeBase reserves 8000 MiB; the remaining 6000 MiB / 3000 per core = 2 cores → 10 TiB.
	ns := stateFrom(NodeCapacity{TlcGiB: 50 * tib, AllocatableCPU: 64, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 14000})
	if got := ns.nodeHeadroom(poolTLC, testCons(), true); got != 10*tib {
		t.Fatalf("want headroom bound by memory after base reservation (2 cores → 10TiB), got %d", got)
	}
}

func Test_Headroom_NoCapacity_ReturnsZero(t *testing.T) {
	ns := stateFrom(NodeCapacity{TlcGiB: 0, AllocatableCPU: 64, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28})
	if got := ns.nodeHeadroom(poolTLC, testCons(), false); got != 0 {
		t.Fatalf("want 0 TLC headroom on a node with no TLC drives, got %d", got)
	}
}

func Test_RequiredDriveResources(t *testing.T) {
	cons := testCons() // 5 TiB/core TLC, 50 TiB/core QLC, 1600 hp/core, 8000 + 3000/core memory
	tests := []struct {
		name               string
		tlcGiB, qlcGiB     int
		numDrives          int
		wantHpMiB, wantMem int
	}{
		// numDrives == 0: the per-core-only branch (containerCapacity/clusterCapacity, whose pods carry no
		// drive term either).
		{"tlc exactly one core", 5 * tib, 0, 0, 1600, 11000},
		{"tlc rounds up", 5*tib + 1, 0, 0, 3200, 14000},
		{"qlc only one core", 0, 50 * tib, 0, 1600, 11000},
		{"mixed sums per-pool cores", 5 * tib, 50 * tib, 0, 3200, 14000},
		{"zero capacity floors at one core", 0, 0, 0, 1600, 11000},

		// numDrives > 0: 1400/core + 200/drive, matching allocator.CalculateDriveHugepages. Drives may
		// exceed cores, which is the whole reason the drive term cannot be folded into a per-core figure.
		{"one drive per core matches the per-core figure", 5 * tib, 0, 1, 1400 + 200, 11000},
		{"drives above cores add 200 MiB each", 5 * tib, 0, 4, 1400 + 800, 11000},
		// The numDrives+driveCapacity case from the docs: 6 drives x 3500 GiB -> 21000 GiB -> 5 cores.
		// Per-core-only would charge 5*1600 = 8000; the pod actually requests 5*1400 + 6*200 = 8200.
		{"numDrives+driveCapacity: 6 drives on 5 cores", 6 * 3500, 0, 6, 5*1400 + 6*200, 23000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hp, mem := RequiredDriveResources(tt.tlcGiB, tt.qlcGiB, tt.numDrives, cons)
			if hp != tt.wantHpMiB || mem != tt.wantMem {
				t.Fatalf("RequiredDriveResources(%d,%d,%d) = (hp %d, mem %d), want (%d, %d)",
					tt.tlcGiB, tt.qlcGiB, tt.numDrives, hp, mem, tt.wantHpMiB, tt.wantMem)
			}
		})
	}
}

// Cross-pool: the TLC pass already merged a new container on a node; the QLC pass must not re-charge
// base memory for that same (existedNode) container.
func Test_PlaceUniform_DoesNotDoubleChargeBaseMemoryForMergedContainer(t *testing.T) {
	s := testScheme() // minFd = 6
	cons := testCons()
	minFd := s.MinFdNum()

	// Six nodes, one FD each; TlcGiB seeds the per-node TLC container the earlier pass would have placed.
	const tlcPerNode = 30 * tib
	tlcCores := util.CeilDiv(tlcPerNode, perCoreCap(poolTLC, cons))

	states := map[string]*nodeState{}
	newByNode := map[string]*NewContainer{}
	for i := 1; i <= minFd; i++ {
		name := "n" + itoa(i)
		nc := node(name, 0 /*tlc already taken*/, 100*tib, 64)
		states[name] = &nodeState{
			nc:           nc,
			tlcFree:      0,
			qlcFree:      nc.QlcGiB,
			coresFree:    nc.AllocatableCPU - tlcCores,
			hugepagesMiB: nc.AvailableHugepagesMiB - tlcCores*cons.HugepagesPerCoreMiB,
			// As if the TLC createSpread pass charged this node: base once + TLC cores.
			memoryMiB: nc.AvailableMemoryMiB - cons.MemoryBaseMiB - tlcCores*cons.MemoryPerCoreMiB,
		}
		newByNode[name] = &NewContainer{Node: name, FDValue: name, TlcGiB: tlcPerNode}
	}

	memBefore := map[string]int{}
	for name, ns := range states {
		memBefore[name] = ns.memoryMiB
	}

	newFor := func(nodeName, fd string) *NewContainer {
		n, ok := newByNode[nodeName]
		if !ok {
			n = &NewContainer{Node: nodeName, FDValue: fd}
			newByNode[nodeName] = n
		}
		return n
	}

	// One fdGroup per node (AUTO mode); each already carries a new TLC container, so base must not re-charge.
	T := 30 * tib
	chosen := make([]*fdGroup, 0, minFd)
	for i := 1; i <= minFd; i++ {
		ns := states["n"+itoa(i)]
		chosen = append(chosen, &fdGroup{nodes: []*nodeState{ns}, headroom: ns.nodeHeadroom(poolQLC, cons, true)})
	}
	placeUniform(poolQLC, T, chosen, nil /*no existing drives*/, cons, nil /*never grows*/, newByNode, newFor)

	// Only QLC core memory should be charged; a second MemoryBaseMiB charge would be the double-charge bug.
	for name, ns := range states {
		addedQlc := newByNode[name].QlcGiB
		if addedQlc <= 0 {
			t.Fatalf("node %s: expected a fresh QLC placement, got %d", name, addedQlc)
		}
		qlcCores := util.CeilDiv(addedQlc, perCoreCap(poolQLC, cons))
		wantDelta := qlcCores * cons.MemoryPerCoreMiB
		gotDelta := memBefore[name] - ns.memoryMiB
		if gotDelta != wantDelta {
			t.Fatalf("node %s: memory charged %d MiB, want %d MiB (a %d MiB excess means base was double-charged)",
				name, gotDelta, wantDelta, gotDelta-wantDelta)
		}
	}
}

// --- compute sizing (OP-329): node-core-aware compute container layout ---

// OP-329: 90 TLC drive cores (6 FDs x 15) must derive a minimal compute layout (6x15), not 90 single-core containers.
func Test_Compute_BugScenario_NodeCoreAware(t *testing.T) {
	s := testScheme()
	cons := testCons()
	cons.MaxCoresPerContainer = 19 // policy cap (default in prod)

	inv := nodes(14, 100*tib, 0, 32, "n") // big nodes: ~6 drive cores leave ample compute headroom
	plan := planCap(desiredFrom(200*tib, s, nil), s, nil, inv, cons)
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if plan.TotalTlcDriveCores != 90 {
		t.Fatalf("want 90 TLC drive cores (15 cores × 6 FDs = 90), got %d", plan.TotalTlcDriveCores)
	}
	if plan.ComputeContainers != 6 || plan.ComputeCores != 15 {
		t.Fatalf("want compute 6x15 (minimal count, cap-bound by MaxCoresPerContainer=19), got %dx%d", plan.ComputeContainers, plan.ComputeCores)
	}
	if plan.ComputeContainers*plan.ComputeCores < plan.TotalTlcDriveCores {
		t.Fatalf("compute:drive 1:1 violated: %d < %d", plan.ComputeContainers*plan.ComputeCores, plan.TotalTlcDriveCores)
	}
}

// Reports infeasibility rather than emitting unschedulable single-core compute when drive-bearing nodes
// have no spare cores for 1:1 compute (each node's 8 CPUs are fully consumed by 7 data + 1 management core).
func Test_Compute_Infeasible_NoCoreHeadroom(t *testing.T) {
	s := testScheme()
	cons := testCons()

	inv := nodes(14, 100*tib, 0, 8, "n") // 7 data cores + 1 management CPU fully consume the node's 8 CPUs, 0 left for compute
	plan := planCap(desiredFrom(200*tib, s, nil), s, nil, inv, cons)
	if !strings.Contains(plan.Infeasible, "compute") {
		t.Fatalf("want a compute infeasibility, got infeasible=%q (compute %dx%d)", plan.Infeasible, plan.ComputeContainers, plan.ComputeCores)
	}
}

// Regression: unpinned compute sizing must be bounded only by the weakest node among the chosen N nodes,
// not the weakest across the entire candidate set, even one the plan never placed on.
func Test_Compute_HeterogeneousNodes_NotDraggedDownByUnusedTinyNode(t *testing.T) {
	s := testScheme()
	cons := testCons() // cap disabled -> real per-node headroom binds

	// 13 big nodes + 1 tiny (8-core) node; drives land on 6 big nodes (minFdNum), leaving 7 big nodes and
	// the tiny node untouched — compute must size off the untouched big nodes, not the tiny one.
	inv := append(nodes(13, 100*tib, 0, 32, "big"), node("small1", 100*tib, 0, 8))
	plan := planCap(desiredFrom(200*tib, s, nil), s, nil, inv, cons)
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if plan.ComputeCores <= 8 {
		t.Fatalf("compute cores should NOT be dragged down to the unused tiny node's 8 cores, got %d", plan.ComputeCores)
	}
	for _, n := range plan.ComputeNodes {
		if n == "small1" {
			t.Fatalf("compute should not have needed the tiny node small1; ComputeNodes=%v", plan.ComputeNodes)
		}
	}
	if plan.ComputeContainers*plan.ComputeCores < plan.TotalTlcDriveCores {
		t.Fatalf("compute:drive 1:1 violated: %d < %d", plan.ComputeContainers*plan.ComputeCores, plan.TotalTlcDriveCores)
	}
}

// Compute sizes over the compute-selector pool (which can be diskless), not the drive nodes: drive nodes
// here are fully core-consumed by their own containers, so sizing compute on them would be infeasible.
func Test_Compute_DisklessNodes_SizedOnComputePool(t *testing.T) {
	s := testScheme()
	cons := testCons()
	cons.MaxCoresPerContainer = 16

	// 6 drive nodes with exactly enough CPUs for their drive container (15 data cores + 1 management CPU =
	// 16 → 0 left for compute), plus 8 diskless compute-only nodes (no drives, 32 cores each).
	inv := append(nodes(6, 100*tib, 0, 16, "d"), nodes(8, 0, 0, 32, "c")...)
	computeNodes := map[string]bool{}
	for i := 1; i <= 8; i++ {
		computeNodes["c"+itoa(i)] = true // only the diskless pool is compute-eligible
	}

	plan := PlanCapacity(desiredFrom(200*tib, s, nil), s, nil, nil, inv, computeNodes, cons)
	if plan.Infeasible != "" {
		t.Fatalf("compute should fit on the diskless pool, got infeasible: %s", plan.Infeasible)
	}
	if plan.TotalTlcDriveCores != 90 {
		t.Fatalf("want 90 TLC drive cores (15 cores × 6 FDs), got %d", plan.TotalTlcDriveCores)
	}
	if plan.ComputeContainers != 6 || plan.ComputeCores != 15 {
		t.Fatalf("want compute 6x15 over the diskless pool (ceil(90/6)=15 cores per container), got %dx%d", plan.ComputeContainers, plan.ComputeCores)
	}
	// Drives must land only on the drive nodes, never on diskless compute nodes.
	if len(plan.Create) != 6 {
		t.Fatalf("want 6 drive containers, got %d", len(plan.Create))
	}
	for _, c := range plan.Create {
		if !strings.HasPrefix(c.Node, "d") {
			t.Fatalf("drive container created on a diskless compute node %q", c.Node)
		}
	}
}

// Compute count is capped by the number of compute nodes (not drive nodes); a shortfall is named in the
// infeasibility message.
func Test_Compute_SubsetSmallerThanRequired_Infeasible(t *testing.T) {
	s := testScheme()
	cons := testCons()
	cons.MaxCoresPerContainer = 16 // needs ceil(84/16)=6 compute containers

	inv := nodes(14, 100*tib, 0, 32, "n") // 14 drive nodes, ample compute headroom
	computeNodes := map[string]bool{}
	for i := 1; i <= 4; i++ {
		computeNodes["n"+itoa(i)] = true // compute selector narrows to only 4 nodes
	}

	plan := PlanCapacity(desiredFrom(200*tib, s, nil), s, nil, nil, inv, computeNodes, cons)
	if !strings.Contains(plan.Infeasible, "compute") || !strings.Contains(plan.Infeasible, "4 compute nodes") {
		t.Fatalf("want infeasibility naming the 4 compute nodes, got %q", plan.Infeasible)
	}
}

// A nil compute-node set must surface loudly, not silently size compute over an unintended node set.
func Test_Compute_NilComputeNodes_Infeasible(t *testing.T) {
	s := testScheme()
	plan := PlanCapacity(desiredFrom(200*tib, s, nil), s, nil, nil, nodes(14, 100*tib, 0, 32, "n"), nil, testCons())
	if !strings.Contains(plan.Infeasible, "compute node set not provided") {
		t.Fatalf("want internal nil-computeNodes infeasibility, got %q", plan.Infeasible)
	}
}

// tightNode builds a node with a specific hugepages budget (generous memory) so hugepages binds.
func tightNode(name string, tlcGiB, cores, hugepagesMiB int) NodeCapacity {
	return NodeCapacity{
		NodeName:              name,
		FDValue:               name,
		TlcGiB:                tlcGiB,
		QlcGiB:                0,
		AllocatableCPU:        cores,
		AvailableHugepagesMiB: hugepagesMiB,
		AvailableMemoryMiB:    1 << 28,
	}
}

// Drive hugepages reservation must include per-core DPDK base memory, else the planner under-reserves
// and co-locates pools the scheduler then rejects.
func Test_RequiredDriveResources_IncludesDpdk(t *testing.T) {
	cons := testCons()
	cons.DriveDpdkPerCoreMiB = 64 // GetDpdkBaseMemoryMbByRole default
	// 5 TiB + 1 -> 2 cores; hugepages = 2 * (1600 + 64) = 3328.
	hp, _ := RequiredDriveResources(5*tib+1, 0, 0, cons)
	if hp != 2*(1600+64) {
		t.Fatalf("got hp %d, want %d", hp, 2*(1600+64))
	}
	// With a drive term the per-core part drops to 1400 but DPDK still applies per core, and each drive adds
	// 200 MiB: 6 drives x 3500 GiB -> 5 cores -> 5*(1400+64) + 6*200 = 8520, the figure the pod requests.
	hpDrives, _ := RequiredDriveResources(6*3500, 0, 6, cons)
	if want := 5*(1400+64) + 6*200; hpDrives != want {
		t.Fatalf("got hp %d, want %d", hpDrives, want)
	}
}

// Compute hugepages estimate must add per-core DPDK base memory on top of the base formula.
func Test_ComputeHugepages_IncludesDpdk(t *testing.T) {
	base := testCons()
	withDpdk := testCons()
	withDpdk.ComputeDpdkPerCoreMiB = 64
	raw := 180 * tib
	got := ComputeContainerHugepagesMiB(raw, 0, 6, 4, withDpdk)
	want := ComputeContainerHugepagesMiB(raw, 0, 6, 4, base) + 64*4
	if got != want {
		t.Fatalf("compute hugepages with DPDK = %d, want base+64*cores = %d", got, want)
	}
}

// On nodes whose hugepages fit a drive OR a compute container but not both, compute must reserve nodes
// separate from the drive nodes (via ComputeNodes) rather than leave the pinned drive pod unschedulable.
func Test_Compute_PinnedOffDriveNodes_WhenHugepagesTight(t *testing.T) {
	s := testScheme() // minFd = 6
	cons := testCons()
	cons.DriveDpdkPerCoreMiB = 64
	cons.ComputeDpdkPerCoreMiB = 64

	// 12 nodes @ 22000 MiB hugepages: a 7-core drive reserves 11648 (10352 left, not enough for the
	// 21448 MiB a 7-core compute needs), but a fresh 22000 MiB node can host compute — separate placement fits.
	inv := make([]NodeCapacity, 0, 12)
	for i := 1; i <= 12; i++ {
		inv = append(inv, tightNode("n"+itoa(i), 100*tib, 64, 22000))
	}

	plan := planCap(desiredFrom(90*tib, s, ratio(1, 0)), s, nil, inv, cons) // rawTLC 200 TiB (204800 GiB) -> 6 FDs × 34134 GiB
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if plan.ComputeContainers == 0 {
		t.Fatalf("expected compute containers to be sized")
	}
	if len(plan.ComputeNodes) != plan.ComputeContainers {
		t.Fatalf("ComputeNodes len %d != ComputeContainers %d", len(plan.ComputeNodes), plan.ComputeContainers)
	}
	driveNodes := map[string]bool{}
	for _, c := range plan.Create {
		driveNodes[c.Node] = true
	}
	for _, n := range plan.ComputeNodes {
		if driveNodes[n] {
			t.Fatalf("compute reserved on drive node %s — hugepages would oversubscribe", n)
		}
	}
}

// Cores-only sizing (floor=6) would need 15-core/45000 MiB containers, but each compute node has only
// 36000 MiB free (64 cores never bind) — the scan must advance n until hugepages fit too: n=8/12 cores.
func Test_Compute_HugepagesBound_PrefersMoreSmallerContainers(t *testing.T) {
	s := testScheme() // minFdNum = 6
	cons := testCons()

	// 6 drive nodes, ample headroom (compute sizes over a separate diskless pool below).
	inv := nodes(6, 100*tib, 0, 32, "d")
	// 8 diskless compute nodes: ample cores, but only 36000 MiB hugepages — fits a 12-core container
	// (12*3000) but not 13 (39000) or 15 (45000).
	for i := 1; i <= 8; i++ {
		inv = append(inv, tightNode("c"+itoa(i), 0, 64, 36000))
	}
	computeNodes := computeNodeSet("c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8")

	// raw = int(float64(200*1024*2)/0.9) GiB; 6 FDs; ceil(raw/6/5120) = 15 cores/FD; totalDriveCores = 90.
	plan := PlanCapacity(desiredFrom(200*tib, s, nil), s, nil, nil, inv, computeNodes, cons)
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if plan.ComputeContainers != 8 || plan.ComputeCores != 12 {
		t.Fatalf("want compute 8x12 (hugepages-bound, more/smaller containers), got %dx%d", plan.ComputeContainers, plan.ComputeCores)
	}
	if plan.ComputeContainers*plan.ComputeCores < plan.TotalTlcDriveCores {
		t.Fatalf("compute:drive 1:1 violated: %d < %d", plan.ComputeContainers*plan.ComputeCores, plan.TotalTlcDriveCores)
	}
}

// A node hosting an existing compute is exempt from the aggregate hugepages gate (frozen-in-place is
// always safe), but the exemption must not leak to fresh nodes: cmp1 (net 1 MiB free) is exempted, while
// cmp2 (fresh, 100 MiB free) must still fail the real check.
func Test_Compute_ExistingComputeHugepagesReclaimed_NotDoubleCharged(t *testing.T) {
	s := testScheme() // minFdNum = 6
	cons := testCons()

	inv := nodes(6, 100*tib, 0, 32, "d")
	// cmp1: existing 4-core compute charging 6400 MiB; raw free 6401 -> only 1 MiB genuinely free (exempt).
	inv = append(inv, tightNode("cmp1", 0, 20, 6401))
	// cmp2: fresh, hugepages-starved (100 MiB) — the exemption must not leak here.
	inv = append(inv, tightNode("cmp2", 0, 64, 100))
	// cmp3-6: FRESH, ample hugepages.
	for i := 3; i <= 6; i++ {
		inv = append(inv, tightNode("cmp"+itoa(i), 0, 64, 1<<28))
	}
	computeNodes := computeNodeSet("cmp1", "cmp2", "cmp3", "cmp4", "cmp5", "cmp6")

	existingCompute := []ExistingComputeContainer{
		{Name: "cmp1-existing", Node: "cmp1", NumCores: 4, HugepagesMiB: 6400},
	}
	inv = netCompute(inv, existingCompute, cons)

	// Same drive sizing as the PrefersMoreSmallerContainers test: floor=d=6 means only n=6 (45000 MiB) is tried.
	plan := PlanCapacity(desiredFrom(200*tib, s, nil), s, nil, existingCompute, inv, computeNodes, cons)
	if plan.Infeasible == "" {
		t.Fatalf("want infeasible: cmp2's genuine hugepages shortage should still be caught (compute %dx%d)", plan.ComputeContainers, plan.ComputeCores)
	}
	// cmp2's shortage is hugepages-only (cores fit comfortably); the message must attribute it to hugepages
	// specifically rather than naming both dimensions ambiguously.
	if !strings.Contains(plan.Infeasible, "hugepages insufficient for") {
		t.Fatalf("want the hugepages-aware aggregate-gate message, got: %q", plan.Infeasible)
	}
}

// --- Grow-path fixes (OP-329): heterogeneous-ceiling, remainder consolidation, compute footprint ---

// Cause A: existing FDs near per-node ceilings grow substantially. The projected FD count must raise until
// spill fits, converging per-FD sizes within ~10% with no new containers.
func Test_GrowA1_HeterogeneousCeiling_EvenGrow(t *testing.T) {
	s := testScheme() // minFdNum = 6
	cons := testCons()

	// 6 containers @ 25 TiB on nodes with 15 TiB headroom (ceiling 40 TiB); 150->240 TiB delta (90 TiB)
	// fits entirely in-place (6*15), so every FD should grow to 40 TiB with 0 new containers.
	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "n" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{
			Name: "c" + itoa(i), Node: n, FDValue: n,
			TlcGiB: 25 * tib, NumCores: 5,
		})
	}
	// nodes() gives "huge" hugepages/memory, only drive capacity and cores bind.
	inv := nodes(6, 15*tib, 0, 59, "n") // 15 TiB headroom each

	plan := planCap(DesiredCapacity{TlcRawGiB: 240 * tib}, s, existingDrives, inv, cons)
	if plan.Infeasible != "" {
		t.Fatalf("A1: unexpected infeasible: %s", plan.Infeasible)
	}
	perFD := map[string]int{}
	for _, g := range plan.Grow {
		for _, e := range existingDrives {
			if e.Name == g.Name {
				perFD[e.FDValue] += g.NewTlcGiB
			}
		}
	}
	for _, e := range existingDrives {
		if _, grown := perFD[e.FDValue]; !grown {
			perFD[e.FDValue] += e.TlcGiB
		}
	}
	lo, hi := 0, 0
	for _, v := range perFD {
		if lo == 0 || v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	// All FDs should converge to the same 40 TiB; allow 1% rounding tolerance.
	if hi-lo > tib/10 {
		t.Fatalf("A1: per-FD sizes vary too much: lo=%d hi=%d GiB (want within ~10%%)", lo, hi)
	}
	// No new containers needed — all capacity fits in existing FDs.
	if len(plan.Create) != 0 {
		t.Fatalf("A1: want 0 new containers (growth fits in existing FDs), got %d", len(plan.Create))
	}
}

// uniform-FD rule forbids a new FD smaller than T0: existing FDs are drive-full (can't grow) and no spare
// node can host a full-T0 FD, so the increase is infeasible rather than opening a sub-T0 fragment.
func Test_GrowA2_RemainderLandsOnFewestFds(t *testing.T) {
	s := testScheme() // minFdNum = 6, MinChunkSizeGiB = 384
	cons := testCons()

	// 6 existing containers of 20 TiB each on drive-full nodes (0 TiB headroom) -> T0 = 20 TiB, no grow.
	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "old" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{
			Name: "c" + itoa(i), Node: n, FDValue: n,
			TlcGiB: 20 * tib, NumCores: 4,
		})
	}

	minChunk := cons.MinChunkSizeGiB // 384 GiB
	tail := 200                      // GiB, < minChunk

	invOld := nodes(6, 0, 0, 64, "old")             // drive-full (0 TiB headroom)
	invNew := nodes(6, minChunk+tail, 0, 64, "new") // 584 GiB each — far below a full T0 (20 TiB) FD
	invExtra := node("extra", 100, 0, 64)           // below MinChunk
	inv := append(invOld, invNew...)
	inv = append(inv, invExtra)

	// Delta is small; no spare node can hold a full-T0 (20 TiB) FD and existing FDs are full -> infeasible.
	delta := 6*minChunk + tail
	target := 6*20*tib + delta
	plan := planCap(DesiredCapacity{TlcRawGiB: target}, s, existingDrives, inv, cons)
	if plan.Infeasible == "" {
		t.Fatalf("A2: want infeasible (no spare node holds a full-T0 FD, existing full), got create=%d grow=%d", len(plan.Create), len(plan.Grow))
	}
	if len(plan.Create) != 0 || len(plan.Grow) != 0 {
		t.Fatalf("A2: uniform rule must not open a sub-T0 fragment FD: create=%v grow=%v", plan.Create, plan.Grow)
	}
}

// Degenerate case: with ample headroom on every node (homogeneous), projected FD count equals len(existing).
func Test_GrowA3_Homogeneous_Unchanged(t *testing.T) {
	s := testScheme() // minFdNum = 6
	cons := testCons()

	// 10 FDs @ 20 TiB, 30 TiB headroom each; 200->300 TiB delta (+10 TiB/FD) -> 30 TiB each.
	var existingDrives []ExistingContainer
	for i := 1; i <= 10; i++ {
		n := "n" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{
			Name: "c" + itoa(i), Node: n, FDValue: n,
			TlcGiB: 20 * tib, NumCores: 4,
		})
	}
	inv := nodes(10, 30*tib, 0, 60, "n")

	plan := planCap(DesiredCapacity{TlcRawGiB: 300 * tib}, s, existingDrives, inv, cons)
	if plan.Infeasible != "" {
		t.Fatalf("A3: unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Create) != 0 {
		t.Fatalf("A3: want 0 new containers (homogeneous, fits in-place), got %d", len(plan.Create))
	}
	if len(plan.Grow) != 10 {
		t.Fatalf("A3: want all 10 existing FDs grown, got %d", len(plan.Grow))
	}
	for _, g := range plan.Grow {
		if g.NewTlcGiB != 30*tib {
			t.Fatalf("A3: want each FD grown to 30 TiB (even), got %+v", g)
		}
	}
}

// uniform-FD rule: with no fresh FD available, every existing FD must grow to the same common level L,
// never leaving one FD pinned at its ceiling while others overshoot.
func Test_Grow_SymmetricOverflow_ExistingAbsorbsCeilingBoundDeficit(t *testing.T) {
	s := testScheme()  // minFdNum = 6
	cons := testCons() // scaling on (AllowInPlaceGrowth = true)

	// 6 existing FDs at 10 TiB each, every node with ample headroom. Current = 60 TiB; target = 120 TiB.
	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "n" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{
			Name: "c" + itoa(i), Node: n, FDValue: n,
			TlcGiB: 10 * tib, NumCores: 2,
		})
	}
	inv := nodes(6, 90*tib, 0, 64, "n") // ample headroom on every existing node, no fresh FDs

	plan := planCap(DesiredCapacity{TlcRawGiB: 120 * tib}, s, existingDrives, inv, cons)
	if plan.Infeasible != "" {
		t.Fatalf("uniform-grow: unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Create) != 0 {
		t.Fatalf("uniform-grow: want 0 new containers (no fresh FDs), got %d", len(plan.Create))
	}
	final := map[string]int{}
	for _, e := range existingDrives {
		final[e.FDValue] = e.TlcGiB
	}
	for _, g := range plan.Grow {
		for _, e := range existingDrives {
			if e.Name == g.Name {
				final[e.FDValue] = g.NewTlcGiB
			}
		}
	}
	sum := 0
	for fd, v := range final {
		if v != 20*tib {
			t.Fatalf("uniform-grow: FD %s holds %d GiB, want every FD at the uniform 20 TiB: %v", fd, v, final)
		}
		sum += v
	}
	if sum != 120*tib {
		t.Fatalf("uniform-grow: total placed = %d, want %d", sum, 120*tib)
	}
}

// Uniform grow + create-new AT the uniform level: when the no-grow attempt can't cover the delta with
// whole-T0 fresh FDs, the planner raises the level L and brings every final FD (grown + new) to L.
// 6 x 30 TiB FDs (ceiling 42 TiB) + 3 fresh nodes, desired 378 TiB -> 9 FDs at 42 TiB.
func Test_Grow_AddsFds_LevelsExistingAndNew(t *testing.T) {
	s := testScheme() // minFdNum = 6
	cons := testCons()
	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "ex" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, TlcGiB: 30 * tib, NumCores: 6})
	}
	// 12 TiB headroom/node (ceiling 42 TiB); only 3 fresh nodes, so no-grow can't cover delta -> level rises.
	inv := append(nodes(6, 12*tib, 0, 64, "ex"), nodes(3, 100*tib, 0, 64, "fresh")...)
	plan := planCap(DesiredCapacity{TlcRawGiB: 378 * tib}, s, existingDrives, inv, cons)
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Grow) != 6 {
		t.Fatalf("want all 6 existing FDs grown toward the common target, got %d", len(plan.Grow))
	}
	if len(plan.Create) != 3 {
		t.Fatalf("want 3 new FDs to reach the leveled target, got %d: %v", len(plan.Create), plan.Create)
	}
	grown := map[string]int{}
	for _, g := range plan.Grow {
		grown[g.Name] = g.NewTlcGiB
	}
	perFD := map[string]int{}
	for _, e := range existingDrives {
		v := e.TlcGiB
		if ng, ok := grown[e.Name]; ok {
			if ng < e.TlcGiB {
				t.Fatalf("existing FD %s shrank %d->%d (existing capacity may only increase)", e.Name, e.TlcGiB, ng)
			}
			v = ng
		}
		perFD[e.FDValue] += v
	}
	for _, c := range plan.Create {
		perFD[c.FDValue] += c.TlcGiB
	}
	if len(perFD) != 9 {
		t.Fatalf("want 9 final FDs (6 existing + 3 new), got %d: %v", len(perFD), perFD)
	}
	for fd, v := range perFD {
		if v != 42*tib {
			t.Fatalf("FD %s holds %d GiB, want every FD leveled to 42 TiB: %v", fd, v, perFD)
		}
	}
}

// uniform-or-infeasible: 6 drive-full FDs + 4 big fresh (100 TiB) + 2 fresh capped at 60 TiB must reach
// 372 TiB. A uniform fill needs ceil(372/6)=62 TiB/FD, but the capped nodes hold only 60 -> Infeasible.
func Test_Heterogeneous_Increase_CappedFreshNodes_Infeasible(t *testing.T) {
	s := testScheme() // minFdNum = 6
	cons := testCons()
	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "old" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, TlcGiB: 5 * tib, NumCores: 1})
	}
	invOld := nodes(6, 0, 0, 64, "old") // existing nodes drive-full
	invBig := nodes(4, 100*tib, 0, 64, "big")
	invSmall := []NodeCapacity{node("small1", 60*tib, 0, 64), node("small2", 60*tib, 0, 64)}
	inv := append(append(invOld, invBig...), invSmall...)
	plan := planCap(DesiredCapacity{TlcRawGiB: 372 * tib}, s, existingDrives, inv, cons)
	if plan.Infeasible == "" {
		t.Fatalf("want ClusterCapacityInfeasible (no uniform tiling reaches 372 TiB), got Create=%v Grow=%v", plan.Create, plan.Grow)
	}
	if len(plan.Create) != 0 || len(plan.Grow) != 0 {
		t.Fatalf("infeasible plan must place nothing, got Create=%d Grow=%d", len(plan.Create), len(plan.Grow))
	}
}

// Cause B: an existing compute container pins resources on a node, so new drive FDs steer away from it —
// compute is charged against state before drive placement, leaving the saturated node zero drive headroom.
func Test_GrowB1_ComputeSaturatedNode_NewDriveAvoidsIt(t *testing.T) {
	s := testScheme() // minFdNum = 6
	cons := testCons()

	// 6 existing TLC drive containers on nodes old1-old6 (drive-full: 0 TiB headroom).
	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "old" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{
			Name: "drive" + itoa(i), Node: n, FDValue: n,
			TlcGiB: 20 * tib, NumCores: 4,
		})
	}

	// "sat1" has plentiful TLC drive space (100 TiB) but all cores/hugepages are consumed by existing
	// compute, so nodeHeadroom for sat1 = 0 after charging.
	saturateCores := 30
	saturateHp := saturateCores * cons.HugepagesPerCoreMiB // 48000 MiB
	existingCompute := []ExistingComputeContainer{
		{
			Name:         "compute-sat",
			Node:         "sat1",
			NumCores:     saturateCores,
			HugepagesMiB: saturateHp,
		},
	}

	// old1-6: drive-full. sat1: 100 TiB drive but 0 usable after compute charge. fresh1-6: ample everything.
	invOld := nodes(6, 0, 0, 64, "old")
	invSat := NodeCapacity{
		NodeName:              "sat1",
		FDValue:               "sat1",
		TlcGiB:                100 * tib,
		AllocatableCPU:        saturateCores,
		AvailableHugepagesMiB: saturateHp,
		AvailableMemoryMiB:    1 << 28,
	}
	invFresh := nodes(6, 50*tib, 0, 64, "fresh")

	inv := append(invOld, invSat)
	inv = append(inv, invFresh...)

	computeNodes := make(map[string]bool, len(inv))
	for _, nc := range invOld {
		computeNodes[nc.NodeName] = true
	}
	for _, nc := range invFresh {
		computeNodes[nc.NodeName] = true
	}

	// Grow +60 TiB: old nodes drive-full, sat1 has 0 headroom -> new FDs must land on fresh1-6.
	inv = netCompute(inv, existingCompute, cons)
	plan := PlanCapacity(DesiredCapacity{TlcRawGiB: 6*20*tib + 60*tib}, s, existingDrives, existingCompute, inv, computeNodes, cons)
	if plan.Infeasible != "" {
		t.Fatalf("B1: unexpected infeasible: %s", plan.Infeasible)
	}
	// sat1 must NOT have a new drive container (its cores/hugepages are fully consumed by compute).
	for _, c := range plan.Create {
		if c.Node == "sat1" {
			t.Fatalf("B1: new drive container placed on compute-saturated node sat1; should be skipped")
		}
	}
	if len(plan.Create) == 0 {
		t.Fatalf("B1: expected new drive containers on fresh nodes, got 0")
	}
}

// When drives and compute share a node with spare headroom after the existing compute footprint, growth
// remains feasible and drives place correctly.
func Test_GrowB2_DriveSharesNodeWithCompute_Feasible(t *testing.T) {
	s := testScheme() // minFdNum = 6
	cons := testCons()

	computeHp := 4 * cons.HugepagesPerCoreMiB

	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "n" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{
			Name: "drive" + itoa(i), Node: n, FDValue: n,
			TlcGiB: 10 * tib, NumCores: 2,
		})
	}
	var existingCompute []ExistingComputeContainer
	for i := 1; i <= 6; i++ {
		n := "n" + itoa(i)
		existingCompute = append(existingCompute, ExistingComputeContainer{
			Name:         "compute" + itoa(i),
			Node:         n,
			NumCores:     4,
			HugepagesMiB: computeHp,
		})
	}
	inv := nodes(6, 40*tib, 0, 64, "n")

	// Grow to 120 TiB raw (from 60 TiB -> +60 TiB, +10 TiB per existing FD).
	inv = netCompute(inv, existingCompute, cons)
	plan := PlanCapacity(DesiredCapacity{TlcRawGiB: 120 * tib}, s, existingDrives, existingCompute, inv, allEligible(inv), cons)
	if plan.Infeasible != "" {
		t.Fatalf("B2: unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Grow) == 0 {
		t.Fatalf("B2: want drive containers grown, got 0 grows")
	}
}

// Cause C: a pinned compute (cmp1) can't grow to the target, gets frozen, and no free node remains to
// compensate the deficit -> cleanly Infeasible, no compute sizing emitted.
func Test_GrowC1_Infeasible_PinnedComputeCannotGrow(t *testing.T) {
	s := testScheme() // minFdNum = 6
	cons := testCons()
	cons.MaxCoresPerContainer = 0 // disable policy cap so only real headroom binds

	// 6 drives x 25 TiB -> grow to 50 TiB gives 60 TLC drive cores -> 10 cores/container over 6 compute
	// nodes -> perContainerHP ~= 30000 MiB.
	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "drv" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{
			Name: "drive" + itoa(i), Node: n, FDValue: n,
			TlcGiB: 25 * tib, NumCores: 5,
		})
	}

	// cmp1: 4 cores/6400 MiB hugepages, only 1 MiB spare — far short of the 30000-6400=23600 MiB delta needed.
	currentHp := 4 * cons.HugepagesPerCoreMiB // 6400 MiB
	cmp1TotalHp := currentHp + 1
	existingCompute := []ExistingComputeContainer{
		{
			Name:         "compute01",
			Node:         "cmp1",
			NumCores:     4,
			HugepagesMiB: currentHp,
		},
	}

	inv := make([]NodeCapacity, 0, 12)
	for i := 1; i <= 6; i++ {
		inv = append(inv, NodeCapacity{
			NodeName:              "drv" + itoa(i),
			FDValue:               "drv" + itoa(i),
			TlcGiB:                50 * tib, // 25 TiB headroom (existing 25 + 25 free)
			AllocatableCPU:        64,
			AvailableHugepagesMiB: 1 << 28,
			AvailableMemoryMiB:    1 << 28,
		})
	}
	inv = append(inv, NodeCapacity{
		NodeName:              "cmp1",
		FDValue:               "cmp1",
		TlcGiB:                0,
		AllocatableCPU:        64,
		AvailableHugepagesMiB: cmp1TotalHp,
		AvailableMemoryMiB:    1 << 28,
	})
	for i := 2; i <= 6; i++ {
		inv = append(inv, NodeCapacity{
			NodeName:              "cmp" + itoa(i),
			FDValue:               "cmp" + itoa(i),
			TlcGiB:                0,
			AllocatableCPU:        64,
			AvailableHugepagesMiB: 1 << 28,
			AvailableMemoryMiB:    1 << 28,
		})
	}

	// Only cmp1-cmp6 are compute-eligible; drive nodes are excluded from compute sizing.
	computeNodes := make(map[string]bool)
	for i := 1; i <= 6; i++ {
		computeNodes["cmp"+itoa(i)] = true
	}

	// cmp1's 1 MiB spare can't cover the 23600 MiB hpDelta. Existing compute is exempt from the aggregate
	// hugepages gate (it can always freeze in place), so the deficit is caught later at placement time —
	// rejecting at the aggregate gate instead would break Test_GrowD4.
	inv = netCompute(inv, existingCompute, cons)
	plan := PlanCapacity(
		DesiredCapacity{TlcRawGiB: 6 * 50 * tib},
		s, existingDrives, existingCompute, inv, computeNodes, cons,
	)
	if plan.Infeasible == "" {
		t.Fatalf("C1: want infeasible when the frozen-compute deficit cannot be covered, got feasible plan (compute %dx%d)", plan.ComputeContainers, plan.ComputeCores)
	}
	if !strings.Contains(plan.Infeasible, "cannot place") || !strings.Contains(plan.Infeasible, "shortfall") {
		t.Fatalf("C1: infeasible message should be the placement-time shortfall message (unchanged by Step 2), got: %q", plan.Infeasible)
	}
	// ComputeCores/ComputeContainers/ComputeLayout must NOT be set on an infeasible plan (pre-mutation).
	if plan.ComputeCores != 0 || plan.ComputeContainers != 0 || len(plan.ComputeLayout) != 0 {
		t.Fatalf("C1: infeasible plan must not emit compute sizing, got %dx%d layout=%d", plan.ComputeContainers, plan.ComputeCores, len(plan.ComputeLayout))
	}
}

// Test_GrowC2_Feasible_PinnedComputeCanGrow: complement of C1 — cmp1 has ample hugepages so the growth
// delta fits, and the fix doesn't double-charge/double-claim cmp1's footprint in the fitNodes pass.
func Test_GrowC2_Feasible_PinnedComputeCanGrow(t *testing.T) {
	s := testScheme() // minFdNum = 6
	cons := testCons()
	cons.MaxCoresPerContainer = 0 // disable policy cap

	// 6x25 TiB drives -> post-grow 60 TLC drive cores -> 10 cores/container, perContainerHP ~=30000 MiB.
	// cmp1's existing 4-core/6400 MiB compute easily absorbs the 6-core/9600 MiB delta.
	currentHp := 4 * cons.HugepagesPerCoreMiB // 6400 MiB

	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "drv" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{
			Name: "drive" + itoa(i), Node: n, FDValue: n,
			TlcGiB: 25 * tib, NumCores: 5,
		})
	}
	existingCompute := []ExistingComputeContainer{
		{
			Name:         "compute01",
			Node:         "cmp1",
			NumCores:     4,
			HugepagesMiB: currentHp,
		},
	}

	// Drive nodes: 25 TiB headroom. Compute nodes cmp1-cmp6: diskless, ample hugepages.
	inv := make([]NodeCapacity, 0, 12)
	for i := 1; i <= 6; i++ {
		inv = append(inv, NodeCapacity{
			NodeName:              "drv" + itoa(i),
			FDValue:               "drv" + itoa(i),
			TlcGiB:                50 * tib,
			AllocatableCPU:        64,
			AvailableHugepagesMiB: 1 << 28,
			AvailableMemoryMiB:    1 << 28,
		})
	}
	for i := 1; i <= 6; i++ {
		inv = append(inv, NodeCapacity{
			NodeName:              "cmp" + itoa(i),
			FDValue:               "cmp" + itoa(i),
			TlcGiB:                0,
			AllocatableCPU:        64,
			AvailableHugepagesMiB: 1 << 28,
			AvailableMemoryMiB:    1 << 28,
		})
	}
	computeNodes := map[string]bool{}
	for i := 1; i <= 6; i++ {
		computeNodes["cmp"+itoa(i)] = true
	}

	inv = netCompute(inv, existingCompute, cons)
	plan := PlanCapacity(
		DesiredCapacity{TlcRawGiB: 6 * 50 * tib},
		s, existingDrives, existingCompute, inv, computeNodes, cons,
	)
	if plan.Infeasible != "" {
		t.Fatalf("C2: unexpected infeasible: %s", plan.Infeasible)
	}
	// Post-grow: 6 drive containers * 10 cores each = 60 TLC drive cores.
	if plan.TotalTlcDriveCores != 60 {
		t.Fatalf("C2: want 60 TLC drive cores post-grow, got %d", plan.TotalTlcDriveCores)
	}
	// Compute sizing must reflect the grown target (10 cores), not the old 4.
	if plan.ComputeCores != 10 {
		t.Fatalf("C2: want ComputeCores=10 after grow, got %d", plan.ComputeCores)
	}
	if plan.ComputeContainers == 0 {
		t.Fatalf("C2: want non-zero ComputeContainers, got 0")
	}
	// 1:1 invariant must hold.
	if plan.ComputeContainers*plan.ComputeCores < plan.TotalTlcDriveCores {
		t.Fatalf("C2: compute:drive 1:1 violated: %d*%d=%d < %d",
			plan.ComputeContainers, plan.ComputeCores,
			plan.ComputeContainers*plan.ComputeCores, plan.TotalTlcDriveCores)
	}
}

// computeNodeSet marks every named node compute-eligible (helper for the compute-grow tests below).
func computeNodeSet(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// Cause D1: each pinned compute node's headroom fits its own growth delta but not a fresh full-footprint
// placement; re-checking the full footprint on already-decremented nodes falsely rejected the grow.
func Test_GrowD1_ComputeCoreBump_RetainsExistingNodes_NoNetNew(t *testing.T) {
	s := testScheme() // minFdNum = 6
	cons := testCons()
	cons.MaxCoresPerContainer = 0 // disable policy cap; real per-node headroom binds

	// 6x60 TiB drives -> 72 drive cores -> compute count=6, cores=12, perContainerHP=36000 MiB.
	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "drv" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{
			Name: "drive" + itoa(i), Node: n, FDValue: n,
			TlcGiB: 60 * tib, NumCores: 12,
		})
	}

	// Each cmp node: 6-core/18000-MiB existing compute, 36001-MiB total budget — enough for the
	// 18000-MiB delta but not a fresh full 36000-MiB placement (the bug's false-reject scenario).
	currentHp := 3000 * 6 // 18000 MiB
	var existingCompute []ExistingComputeContainer
	for i := 1; i <= 6; i++ {
		existingCompute = append(existingCompute, ExistingComputeContainer{
			Name: "compute" + itoa(i), Node: "cmp" + itoa(i), NumCores: 6, HugepagesMiB: currentHp,
		})
	}

	// Drive nodes (ample), 6 cmp nodes with the tight 36001-MiB budget, plus one fully-free cmp node.
	inv := make([]NodeCapacity, 0, 13)
	for i := 1; i <= 6; i++ {
		inv = append(inv, NodeCapacity{
			NodeName: "drv" + itoa(i), FDValue: "drv" + itoa(i),
			TlcGiB: 100 * tib, AllocatableCPU: 64, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28,
		})
	}
	for i := 1; i <= 6; i++ {
		inv = append(inv, NodeCapacity{
			NodeName: "cmp" + itoa(i), FDValue: "cmp" + itoa(i),
			TlcGiB: 0, AllocatableCPU: 64, AvailableHugepagesMiB: 36001, AvailableMemoryMiB: 1 << 28,
		})
	}
	inv = append(inv, NodeCapacity{
		NodeName: "cmpfree", FDValue: "cmpfree",
		TlcGiB: 0, AllocatableCPU: 64, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28,
	})

	computeNodes := computeNodeSet("cmp1", "cmp2", "cmp3", "cmp4", "cmp5", "cmp6", "cmpfree")
	inv = netCompute(inv, existingCompute, cons)
	plan := PlanCapacity(DesiredCapacity{TlcRawGiB: 6 * 60 * tib}, s, existingDrives, existingCompute, inv, computeNodes, cons)
	if plan.Infeasible != "" {
		t.Fatalf("D1: unexpected infeasible: %s", plan.Infeasible)
	}
	if plan.ComputeContainers != 6 || plan.ComputeCores != 12 {
		t.Fatalf("D1: want compute 6x12, got %dx%d", plan.ComputeContainers, plan.ComputeCores)
	}
	// All 6 existing cmp nodes retained; the free node must NOT be consumed (net-new = 0).
	want := map[string]bool{"cmp1": true, "cmp2": true, "cmp3": true, "cmp4": true, "cmp5": true, "cmp6": true}
	if len(plan.ComputeNodes) != 6 {
		t.Fatalf("D1: want 6 compute nodes (the existing pinned set), got %d: %v", len(plan.ComputeNodes), plan.ComputeNodes)
	}
	for _, n := range plan.ComputeNodes {
		if !want[n] {
			t.Fatalf("D1: net-new node %q consumed; expected only the 6 retained existing nodes: %v", n, plan.ComputeNodes)
		}
	}
}

// Test_GrowD2_ComputeCoreBump_StuckCompute_CompensatedOnFreeNode: cmp1 can't grow its delta, so it's
// frozen at 6 cores and the 6-core deficit is compensated on the free node (cmpfree); cmp2-6 grow to 12.
func Test_GrowD2_ComputeCoreBump_StuckCompute_CompensatedOnFreeNode(t *testing.T) {
	s := testScheme()
	cons := testCons()
	cons.MaxCoresPerContainer = 0

	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "drv" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{
			Name: "drive" + itoa(i), Node: n, FDValue: n,
			TlcGiB: 60 * tib, NumCores: 12,
		})
	}
	currentHp := 3000 * 6 // 18000 MiB; grown target needs 36000 → delta 18000
	var existingCompute []ExistingComputeContainer
	for i := 1; i <= 6; i++ {
		existingCompute = append(existingCompute, ExistingComputeContainer{
			Name: "compute" + itoa(i), Node: "cmp" + itoa(i), NumCores: 6, HugepagesMiB: currentHp,
		})
	}

	inv := make([]NodeCapacity, 0, 13)
	for i := 1; i <= 6; i++ {
		inv = append(inv, NodeCapacity{
			NodeName: "drv" + itoa(i), FDValue: "drv" + itoa(i),
			TlcGiB: 100 * tib, AllocatableCPU: 64, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28,
		})
	}
	for i := 1; i <= 6; i++ {
		// cmp1: 18100 MiB total, only 100 free after the 18000 charge — far below the 18000 delta.
		hp := 1 << 28
		if i == 1 {
			hp = currentHp + 100
		}
		inv = append(inv, NodeCapacity{
			NodeName: "cmp" + itoa(i), FDValue: "cmp" + itoa(i),
			TlcGiB: 0, AllocatableCPU: 64, AvailableHugepagesMiB: hp, AvailableMemoryMiB: 1 << 28,
		})
	}
	inv = append(inv, NodeCapacity{
		NodeName: "cmpfree", FDValue: "cmpfree",
		TlcGiB: 0, AllocatableCPU: 64, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28,
	})

	computeNodes := computeNodeSet("cmp1", "cmp2", "cmp3", "cmp4", "cmp5", "cmp6", "cmpfree")
	inv = netCompute(inv, existingCompute, cons)
	plan := PlanCapacity(DesiredCapacity{TlcRawGiB: 6 * 60 * tib}, s, existingDrives, existingCompute, inv, computeNodes, cons)
	if plan.Infeasible != "" {
		t.Fatalf("D2: want feasible (stuck compute frozen + compensated), got infeasible: %s", plan.Infeasible)
	}
	if len(plan.ComputeLayout) == 0 {
		t.Fatalf("D2: want a per-container ComputeLayout, got none")
	}
	byNode := map[string]int{}
	total := 0
	for _, e := range plan.ComputeLayout {
		byNode[e.Node] = e.NumCores
		total += e.NumCores
	}
	// cmp1 frozen at its current 6 cores (NOT grown to 12).
	if byNode["cmp1"] != 6 {
		t.Fatalf("D2: cmp1 must stay frozen at 6 cores, got %d", byNode["cmp1"])
	}
	// cmp2-6 grew to the uniform 12-core target.
	for i := 2; i <= 6; i++ {
		if byNode["cmp"+itoa(i)] != 12 {
			t.Fatalf("D2: cmp%d must grow to 12 cores, got %d", i, byNode["cmp"+itoa(i)])
		}
	}
	// A compensating container exists on the free node covering the 6-core deficit.
	if byNode["cmpfree"] != 6 {
		t.Fatalf("D2: cmpfree must host a 6-core compensating container, got %d (layout=%+v)", byNode["cmpfree"], plan.ComputeLayout)
	}
	// Total layout cores cover the uniform target total (6 containers × 12 = 72).
	if total < 6*12 {
		t.Fatalf("D2: total layout cores %d < uniform target total %d", total, 6*12)
	}
}

// Test_GrowD3_ComputeCountGrows_PlacesNetNewOnly: a count increase (6 -> 8 containers) places only the
// 2 net-new computes on free nodes; ComputeNodes is the union of the 6 existing + 2 new.
func Test_GrowD3_ComputeCountGrows_PlacesNetNewOnly(t *testing.T) {
	s := testScheme()
	cons := testCons()
	cons.MaxCoresPerContainer = 10 // count = max(6, ceil(72/10)) = 8, cores = ceil(72/8) = 9

	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "drv" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{
			Name: "drive" + itoa(i), Node: n, FDValue: n,
			TlcGiB: 60 * tib, NumCores: 12,
		})
	}
	// 6 existing computes at 6 cores / 18000 MiB; grown target 9 cores / 27000 MiB (delta 3 cores /
	// 9000 MiB) fits the ample cmp nodes. 2 free cmp nodes (cmp7, cmp8) take the net-new computes.
	currentHp := 3000 * 6
	var existingCompute []ExistingComputeContainer
	for i := 1; i <= 6; i++ {
		existingCompute = append(existingCompute, ExistingComputeContainer{
			Name: "compute" + itoa(i), Node: "cmp" + itoa(i), NumCores: 6, HugepagesMiB: currentHp,
		})
	}

	inv := make([]NodeCapacity, 0, 14)
	for i := 1; i <= 6; i++ {
		inv = append(inv, NodeCapacity{
			NodeName: "drv" + itoa(i), FDValue: "drv" + itoa(i),
			TlcGiB: 100 * tib, AllocatableCPU: 64, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28,
		})
	}
	for i := 1; i <= 8; i++ {
		inv = append(inv, NodeCapacity{
			NodeName: "cmp" + itoa(i), FDValue: "cmp" + itoa(i),
			TlcGiB: 0, AllocatableCPU: 64, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28,
		})
	}

	computeNodes := computeNodeSet("cmp1", "cmp2", "cmp3", "cmp4", "cmp5", "cmp6", "cmp7", "cmp8")
	inv = netCompute(inv, existingCompute, cons)
	plan := PlanCapacity(DesiredCapacity{TlcRawGiB: 6 * 60 * tib}, s, existingDrives, existingCompute, inv, computeNodes, cons)
	if plan.Infeasible != "" {
		t.Fatalf("D3: unexpected infeasible: %s", plan.Infeasible)
	}
	if plan.ComputeContainers != 8 || plan.ComputeCores != 9 {
		t.Fatalf("D3: want compute 8x9, got %dx%d", plan.ComputeContainers, plan.ComputeCores)
	}
	if len(plan.ComputeNodes) != 8 {
		t.Fatalf("D3: want 8 compute nodes, got %d: %v", len(plan.ComputeNodes), plan.ComputeNodes)
	}
	got := map[string]bool{}
	for _, n := range plan.ComputeNodes {
		got[n] = true
	}
	for i := 1; i <= 6; i++ { // all 6 existing pinned nodes retained
		if !got["cmp"+itoa(i)] {
			t.Fatalf("D3: existing pinned node cmp%d not retained: %v", i, plan.ComputeNodes)
		}
	}
	netNew := 0
	for i := 7; i <= 8; i++ {
		if got["cmp"+itoa(i)] {
			netNew++
		}
	}
	if netNew != 2 {
		t.Fatalf("D3: want exactly 2 net-new nodes among cmp7/cmp8, got %d: %v", netNew, plan.ComputeNodes)
	}
}

// Test_GrowD4_DeficitSpreadAcrossMultipleCompensatingContainers: 3 stuck computes (cmp1-3, frozen at 6)
// create an 18-core deficit, split evenly (9+9) across 2 compensating free nodes; cmp4-6 grow to 12.
func Test_GrowD4_DeficitSpreadAcrossMultipleCompensatingContainers(t *testing.T) {
	s := testScheme()
	cons := testCons()
	cons.MaxCoresPerContainer = 0 // uniform target 12 cores (72 drive cores / 6)

	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "drv" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{
			Name: "drive" + itoa(i), Node: n, FDValue: n,
			TlcGiB: 60 * tib, NumCores: 12,
		})
	}
	currentHp := 3000 * 6 // 18000 MiB; grown target 12 cores → 36000 MiB (delta 18000)
	var existingCompute []ExistingComputeContainer
	for i := 1; i <= 6; i++ {
		existingCompute = append(existingCompute, ExistingComputeContainer{
			Name: "compute" + itoa(i), Node: "cmp" + itoa(i), NumCores: 6, HugepagesMiB: currentHp,
		})
	}

	inv := make([]NodeCapacity, 0, 14)
	for i := 1; i <= 6; i++ {
		inv = append(inv, NodeCapacity{
			NodeName: "drv" + itoa(i), FDValue: "drv" + itoa(i),
			TlcGiB: 100 * tib, AllocatableCPU: 64, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28,
		})
	}
	// cmp1-3 are stuck: only 100 MiB free after the 18000 charge (cannot fit the 18000 delta) → frozen.
	// cmp4-6 are ample → grow to 12. cmpf1/cmpf2 are free fitting nodes for the 2 compensating containers.
	for i := 1; i <= 6; i++ {
		hp := 1 << 28
		if i <= 3 {
			hp = currentHp + 100
		}
		inv = append(inv, NodeCapacity{
			NodeName: "cmp" + itoa(i), FDValue: "cmp" + itoa(i),
			TlcGiB: 0, AllocatableCPU: 64, AvailableHugepagesMiB: hp, AvailableMemoryMiB: 1 << 28,
		})
	}
	for _, n := range []string{"cmpf1", "cmpf2"} {
		inv = append(inv, NodeCapacity{
			NodeName: n, FDValue: n,
			TlcGiB: 0, AllocatableCPU: 64, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28,
		})
	}

	computeNodes := computeNodeSet("cmp1", "cmp2", "cmp3", "cmp4", "cmp5", "cmp6", "cmpf1", "cmpf2")
	inv = netCompute(inv, existingCompute, cons)
	plan := PlanCapacity(DesiredCapacity{TlcRawGiB: 6 * 60 * tib}, s, existingDrives, existingCompute, inv, computeNodes, cons)
	if plan.Infeasible != "" {
		t.Fatalf("D4: want feasible (3 frozen + 2 compensating), got infeasible: %s", plan.Infeasible)
	}
	byNode := map[string]int{}
	for _, e := range plan.ComputeLayout {
		byNode[e.Node] = e.NumCores
	}
	for i := 1; i <= 3; i++ { // frozen at 6
		if byNode["cmp"+itoa(i)] != 6 {
			t.Fatalf("D4: cmp%d must stay frozen at 6 cores, got %d", i, byNode["cmp"+itoa(i)])
		}
	}
	for i := 4; i <= 6; i++ { // grown to 12
		if byNode["cmp"+itoa(i)] != 12 {
			t.Fatalf("D4: cmp%d must grow to 12 cores, got %d", i, byNode["cmp"+itoa(i)])
		}
	}
	// Two compensating containers, evenly split (9 + 9) across the two free nodes.
	c1, c2 := byNode["cmpf1"], byNode["cmpf2"]
	if c1 == 0 || c2 == 0 {
		t.Fatalf("D4: want compensating containers on BOTH free nodes, got cmpf1=%d cmpf2=%d (layout=%+v)", c1, c2, plan.ComputeLayout)
	}
	if c1+c2 != 18 {
		t.Fatalf("D4: compensating cores must total the 18-core deficit, got %d + %d = %d", c1, c2, c1+c2)
	}
	if c1 != 9 || c2 != 9 {
		t.Fatalf("D4: deficit must split evenly 9/9 across the two free nodes, got cmpf1=%d cmpf2=%d", c1, c2)
	}
}

// D5: a frozen compute (cmp1, 6 cores) coexisting with net-new slots must split the 66-core shortfall
// uniformly across 6 free nodes (6x11), not full-then-remainder (12,12,12,12,12,6).
func Test_GrowD5_FrozenPlusNetNew_BalancedFill(t *testing.T) {
	s := testScheme()
	cons := testCons()
	cons.MaxCoresPerContainer = 0 // uniform target 12 cores (72 drive cores / 6)

	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "drv" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{
			Name: "drive" + itoa(i), Node: n, FDValue: n,
			TlcGiB: 60 * tib, NumCores: 12,
		})
	}
	// cmp1: only 100 MiB spare after the 18000 charge, far below the 18000-MiB delta -> frozen.
	currentHp := 3000 * 6 // 18000 MiB
	existingCompute := []ExistingComputeContainer{
		{Name: "compute1", Node: "cmp1", NumCores: 6, HugepagesMiB: currentHp},
	}

	inv := make([]NodeCapacity, 0, 13)
	for i := 1; i <= 6; i++ {
		inv = append(inv, NodeCapacity{
			NodeName: "drv" + itoa(i), FDValue: "drv" + itoa(i),
			TlcGiB: 100 * tib, AllocatableCPU: 64, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28,
		})
	}
	// cmp1: frozen (tight hugepages). cmpf1-6: free fitting nodes for the balanced fill.
	inv = append(inv, NodeCapacity{
		NodeName: "cmp1", FDValue: "cmp1",
		TlcGiB: 0, AllocatableCPU: 64, AvailableHugepagesMiB: currentHp + 100, AvailableMemoryMiB: 1 << 28,
	})
	for i := 1; i <= 6; i++ {
		inv = append(inv, NodeCapacity{
			NodeName: "cmpf" + itoa(i), FDValue: "cmpf" + itoa(i),
			TlcGiB: 0, AllocatableCPU: 64, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28,
		})
	}

	computeNodes := computeNodeSet("cmp1", "cmpf1", "cmpf2", "cmpf3", "cmpf4", "cmpf5", "cmpf6")
	inv = netCompute(inv, existingCompute, cons)
	plan := PlanCapacity(DesiredCapacity{TlcRawGiB: 6 * 60 * tib}, s, existingDrives, existingCompute, inv, computeNodes, cons)
	if plan.Infeasible != "" {
		t.Fatalf("D5: want feasible (frozen + balanced net-new fill), got infeasible: %s", plan.Infeasible)
	}
	// 1 frozen existing + 6 new containers covering the 66-core shortfall = 7 containers.
	if plan.ComputeContainers != 7 {
		t.Fatalf("D5: want 7 compute containers (1 frozen + 6 fill), got %d", plan.ComputeContainers)
	}
	byNode := map[string]int{}
	total := 0
	for _, e := range plan.ComputeLayout {
		byNode[e.Node] = e.NumCores
		total += e.NumCores
	}
	if byNode["cmp1"] != 6 {
		t.Fatalf("D5: cmp1 must stay frozen at 6 cores, got %d", byNode["cmp1"])
	}
	// The 6 new containers are UNIFORMLY balanced at 11 cores each (66 / 6), not [12×5, 6].
	for i := 1; i <= 6; i++ {
		if got := byNode["cmpf"+itoa(i)]; got != 11 {
			t.Fatalf("D5: cmpf%d must be balanced at 11 cores (not full-then-remainder), got %d (layout=%+v)", i, got, plan.ComputeLayout)
		}
	}
	if total != 6+6*11 { // frozen 6 + 66 = 72, matching the uniform target total
		t.Fatalf("D5: total layout cores %d must equal the uniform target total 72", total)
	}
}

// --- enableDynamicDriveScalingForSharedDrives=false: never extend existing containers ---

// With in-place growth disabled, the delta is placed as new containers on fresh FDs (fewest,
// imbalance-guarded), never grown in place. The enabled sub-run contrasts create-new-before-grow.
func Test_Grow_DynamicScalingDisabled_CreatesNewInsteadOfExtending(t *testing.T) {
	s := testScheme() // minFdNum = 6
	// 6 existing TLC containers on n1-n6 (70 TiB/58 cores headroom each) + fresh FDs n7-n12.
	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "n" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, TlcGiB: 30 * tib, NumCores: 6})
	}
	inv := nodes(6, 70*tib, 0, 58, "n") // n1..n6
	for i := 7; i <= 12; i++ {
		inv = append(inv, node("n"+itoa(i), 100*tib, 0, 58)) // n7..n12 fresh FDs
	}
	desired := DesiredCapacity{TlcRawGiB: 360 * tib} // current 180 TiB -> delta 180 TiB
	existingNodes := computeNodeSet("n1", "n2", "n3", "n4", "n5", "n6")

	t.Run("disabled_creates_new", func(t *testing.T) {
		cons := testCons()
		cons.AllowInPlaceGrowth = false
		plan := planCap(desired, s, existingDrives, inv, cons)
		if plan.Infeasible != "" {
			t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
		}
		if len(plan.Grow) != 0 {
			t.Fatalf("dynamic scaling disabled: must not extend existing containers, got %d grows: %+v", len(plan.Grow), plan.Grow)
		}
		if len(plan.Create) != 4 { // fewest: kMin=CeilDiv(180,maxPerFdCap=60)=3 is imbalanced (60==2×avg), so k=4
			t.Fatalf("want the 180 TiB delta placed as 4 new containers on fresh FDs (fewest), got %d", len(plan.Create))
		}
		for _, c := range plan.Create {
			if existingNodes[c.Node] {
				t.Fatalf("new container must land on a FRESH failure domain, got node %s", c.Node)
			}
			if c.TlcGiB != 45*tib { // CeilDiv(180 TiB, k=4) = 45 TiB
				t.Fatalf("want each new container TLC=45 TiB (fewest FDs), got %+v", c)
			}
		}
		if got := sumCreateTlc(plan); got != 180*tib {
			t.Fatalf("want total created TLC 180 TiB, got %d GiB", got)
		}
	})

	// create-new-before-grow: with spare fresh FDs, delta=180 TiB is covered by 4x45 TiB new FDs;
	// existing specs stay untouched.
	t.Run("enabled_creates_new_at_T", func(t *testing.T) {
		cons := testCons() // AllowInPlaceGrowth = true
		plan := planCap(desired, s, existingDrives, inv, cons)
		if plan.Infeasible != "" {
			t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
		}
		if len(plan.Grow) != 0 {
			t.Fatalf("create-new-before-grow: existing specs must be untouched, got %d grows", len(plan.Grow))
		}
		if len(plan.Create) != 4 { // fewest FDs: 4 × 45 TiB (kMin=3 is imbalanced at 60==2×avg)
			t.Fatalf("want the 180 TiB delta as 4 new FDs (fewest), got %d", len(plan.Create))
		}
		for _, c := range plan.Create {
			if existingNodes[c.Node] {
				t.Fatalf("new container must land on a FRESH failure domain, got node %s", c.Node)
			}
			if c.TlcGiB != 45*tib { // CeilDiv(180 TiB, k=4) = 45 TiB
				t.Fatalf("want each new container TLC=45 TiB (fewest FDs), got %+v", c)
			}
		}
		if len(plan.OverProvisions) != 0 { // exact cover of delta, no overshoot
			t.Fatalf("exact cover of delta should not over-provision, got %v", plan.OverProvisions)
		}
	})
}

// Test_Grow_DynamicScalingDisabled_NoFreshFD_Infeasible: no fresh FDs + growth disabled -> infeasible
// with a flag-aware message, never a silent extend of existing containers.
func Test_Grow_DynamicScalingDisabled_NoFreshFD_Infeasible(t *testing.T) {
	s := testScheme()
	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "n" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, TlcGiB: 30 * tib, NumCores: 6})
	}
	// Inventory is ONLY the 6 nodes that already host this pool's containers — no fresh FD for new ones.
	inv := nodes(6, 70*tib, 0, 58, "n")
	cons := testCons()
	cons.AllowInPlaceGrowth = false

	plan := planCap(DesiredCapacity{TlcRawGiB: 360 * tib}, s, existingDrives, inv, cons)
	if plan.Infeasible == "" {
		t.Fatalf("want infeasible (no fresh FDs, in-place growth disabled), got grow=%d create=%d", len(plan.Grow), len(plan.Create))
	}
	if !strings.Contains(plan.Infeasible, "dynamic drive scaling") {
		t.Fatalf("want a flag-aware infeasibility message, got %q", plan.Infeasible)
	}
	if len(plan.Grow) != 0 {
		t.Fatalf("must not extend existing containers when disabled, got %d grows", len(plan.Grow))
	}
}

// With growth disabled, existing computes freeze at their current size and the deficit is covered by new
// containers on fresh nodes; the enabled sub-run contrasts growing in place.
func Test_Compute_DynamicScalingDisabled_FreezesExistingCreatesNew(t *testing.T) {
	s := testScheme() // minFdNum = 6
	// Drives at target: 6x30 TiB TLC -> 36 TLC drive cores -> compute 1:1 target = 6 cores x 6 FDs.
	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "n" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "drive" + itoa(i), Node: n, FDValue: n, TlcGiB: 30 * tib, NumCores: 6})
	}
	// 6 existing compute @ 2 cores on n1..n6 — roomy nodes, so they COULD grow to the 6-core target.
	const curComputeCores = 2
	var existingCompute []ExistingComputeContainer
	for i := 1; i <= 6; i++ {
		existingCompute = append(existingCompute, ExistingComputeContainer{
			Name: "compute" + itoa(i), Node: "n" + itoa(i),
			NumCores: curComputeCores, HugepagesMiB: curComputeCores * 1600,
		})
	}
	// 12 nodes, 64 cores each, no spare drive capacity (drives satisfied). n7..n12 are fresh compute FDs.
	invBase := nodes(12, 0, 0, 64, "n")

	desired := DesiredCapacity{TlcRawGiB: 180 * tib} // == current -> no drive growth
	existingNodes := computeNodeSet("n1", "n2", "n3", "n4", "n5", "n6")

	t.Run("disabled_freezes_and_creates_new", func(t *testing.T) {
		c := testCons()
		c.AllowInPlaceGrowth = false
		inv := netCompute(invBase, existingCompute, c)
		plan := PlanCapacity(desired, s, existingDrives, existingCompute, inv, allEligible(inv), c)
		if plan.Infeasible != "" {
			t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
		}
		total, newOnFresh := 0, 0
		for _, e := range plan.ComputeLayout {
			total += e.NumCores
			if existingNodes[e.Node] {
				if e.NumCores != curComputeCores {
					t.Fatalf("existing compute on %s must stay frozen at %d cores, got %d", e.Node, curComputeCores, e.NumCores)
				}
			} else {
				newOnFresh++
			}
		}
		if newOnFresh == 0 {
			t.Fatalf("deficit must be covered by NEW compute on fresh nodes, found none (layout=%+v)", plan.ComputeLayout)
		}
		if total < plan.TotalTlcDriveCores {
			t.Fatalf("compute:drive 1:1 violated: total compute cores %d < %d drive cores", total, plan.TotalTlcDriveCores)
		}
	})

	t.Run("enabled_grows_existing", func(t *testing.T) {
		cons := testCons()
		inv := netCompute(invBase, existingCompute, cons)
		plan := PlanCapacity(desired, s, existingDrives, existingCompute, inv, allEligible(inv), cons)
		if plan.Infeasible != "" {
			t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
		}
		for _, e := range plan.ComputeLayout {
			if existingNodes[e.Node] && e.NumCores == curComputeCores {
				t.Fatalf("with scaling enabled the existing compute on %s should have grown past %d cores", e.Node, curComputeCores)
			}
			if !existingNodes[e.Node] {
				t.Fatalf("with scaling enabled the existing computes cover the target in place, unexpected new container on %s", e.Node)
			}
		}
	})
}

// --- uniform-FD increase path (planPoolUniformIncrease) ---

// A small bump with spare nodes is covered by one new FD sized to the shortfall (not a full-T0 clone),
// leaving existing containers untouched and reaching desired exactly.
func Test_UniformIncrease_PrefersNewFds_OverGrow(t *testing.T) {
	s := testScheme() // minFdNum = 6
	cons := testCons()
	// 6 existing 30 TiB FDs with ample per-node headroom (grow WOULD be possible — but create-new wins).
	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "n" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, TlcGiB: 30 * tib, NumCores: 6})
	}
	inv := append(nodes(6, 70*tib, 0, 64, "n"), nodes(2, 100*tib, 0, 64, "spare")...)
	// delta=20 TiB, maxPerFdCap=33 TiB -> k=1 fits (>=MinChunk, below the 2x30 TiB imbalance boundary).
	plan := planCap(DesiredCapacity{TlcRawGiB: 200 * tib}, s, existingDrives, inv, cons)
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Grow) != 0 {
		t.Fatalf("create-new must be preferred over grow: want 0 grows, got %d", len(plan.Grow))
	}
	if len(plan.Create) != 1 {
		t.Fatalf("want exactly 1 new even-split FD, got %d: %v", len(plan.Create), plan.Create)
	}
	if plan.Create[0].TlcGiB != 20*tib {
		t.Fatalf("want the new FD sized to the delta (20 TiB), got %+v", plan.Create[0])
	}
	if len(plan.OverProvisions) != 0 {
		t.Fatalf("even-split reaches desired exactly; want no over-provision advisory, got %v", plan.OverProvisions)
	}
}

// Test_UniformIncrease_NoSpare_BelowThreshold_Infeasible: no spare node + a sub-minGrowthFraction grow
// -> infeasible with the threshold message, nothing placed.
func Test_UniformIncrease_NoSpare_BelowThreshold_Infeasible(t *testing.T) {
	s := testScheme() // minFdNum = 6
	cons := testCons()
	// 6 existing 30 TiB FDs; nodes have a little headroom (can reach 31 TiB) but there are NO fresh FDs.
	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "n" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, TlcGiB: 30 * tib, NumCores: 6})
	}
	inv := nodes(6, 2*tib, 0, 64, "n") // 2 TiB headroom each (ceiling 32 TiB), no fresh FDs
	// current 180 TiB; target 186 TiB -> uniform level 31 TiB == a ~3% grow, below minGrowthFraction=0.2.
	plan := planCap(DesiredCapacity{TlcRawGiB: 186 * tib}, s, existingDrives, inv, cons)
	if plan.Infeasible == "" {
		t.Fatalf("want infeasible (no spare, sub-threshold grow), got create=%d grow=%d", len(plan.Create), len(plan.Grow))
	}
	if !strings.Contains(plan.Infeasible, "minGrowthFraction") {
		t.Fatalf("want the threshold message, got %q", plan.Infeasible)
	}
	if len(plan.Create) != 0 || len(plan.Grow) != 0 {
		t.Fatalf("sub-threshold infeasible must place nothing: create=%v grow=%v", plan.Create, plan.Grow)
	}
}

// Test_UniformIncrease_NoSpare_MinGrowthFractionZero_GrowsUniformly: same ~3% grow as the BelowThreshold
// test but MinGrowthFraction=0 ("always allow"), proving 0 is honored and not coerced to the default.
func Test_UniformIncrease_NoSpare_MinGrowthFractionZero_GrowsUniformly(t *testing.T) {
	s := testScheme() // minFdNum = 6
	cons := testCons()
	cons.MinGrowthFraction = 0 // always allow in-place grow
	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "n" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, TlcGiB: 30 * tib, NumCores: 6})
	}
	inv := nodes(6, 2*tib, 0, 64, "n") // 2 TiB headroom each (ceiling 32 TiB), no fresh FDs
	// current 180 TiB; target 186 TiB -> uniform level 31 TiB == a ~3% grow (below the 0.2 default).
	plan := planCap(DesiredCapacity{TlcRawGiB: 186 * tib}, s, existingDrives, inv, cons)
	if plan.Infeasible != "" {
		t.Fatalf("MinGrowthFraction=0 must allow the sub-3%% grow, got infeasible: %s", plan.Infeasible)
	}
	if len(plan.Create) != 0 {
		t.Fatalf("no spare nodes: want 0 new FDs, got %d", len(plan.Create))
	}
	if len(plan.Grow) != 6 {
		t.Fatalf("want all 6 existing FDs grown to the uniform level, got %d", len(plan.Grow))
	}
	for _, g := range plan.Grow {
		if g.NewTlcGiB != 31*tib {
			t.Fatalf("want every FD grown to the uniform 31 TiB, got %+v", g)
		}
	}
}

// Test_CapacityCoverTarget verifies the CapacityCoverTarget helper: fraction=0 returns desired unchanged
// (strict mode); otherwise desired minus ceil(desired*fraction).
func Test_CapacityCoverTarget(t *testing.T) {
	cases := []struct {
		desired  int
		fraction float64
		want     int
		desc     string
	}{
		{6395, 0, 6395, "fraction=0 returns desired unchanged"},
		{6395, 0.05, 6075, "6395*0.05=319.75, ceil=320, 6395-320=6075"},
		{100, 0.011, 98, "100*0.011=1.1, ceil=2, 100-2=98"},
	}
	for _, tc := range cases {
		cons := &CapacityConstraints{CapacityDeadbandFraction: tc.fraction}
		got := CapacityCoverTarget(tc.desired, cons)
		if got != tc.want {
			t.Errorf("CapacityCoverTarget(%d, fraction=%.3f): got %d, want %d (%s)", tc.desired, tc.fraction, got, tc.want, tc.desc)
		}
	}
}

// New FDs sum to the delta using the fewest FDs whose even share stays within maxPerFdCap, not by
// cloning T0 and rounding up count (old: 3x1250=7500, over-provisioning by 1105; new: 3x882=6396).
func Test_UniformIncrease_EvenSplitToDelta_SizesNewFdsToShortfall(t *testing.T) {
	s := ProtectionScheme{StripeWidth: 3, RedundancyLevel: 2, HotSpare: 0} // minFd = 5
	cons := testCons()
	cons.CapacityDeadbandFraction = 0.05 // present but does NOT change the even-split (which targets exact delta)
	cons.AllowInPlaceGrowth = false      // freeze existing FDs; all new capacity is fresh even-split FDs

	// 3 existing FDs of 1250 GiB each → current = 3750, T0 = 1250.
	existingDrives := []ExistingContainer{
		{Name: "c1", Node: "n1", FDValue: "n1", TlcGiB: 1250, NumCores: 1},
		{Name: "c2", Node: "n2", FDValue: "n2", TlcGiB: 1250, NumCores: 1},
		{Name: "c3", Node: "n3", FDValue: "n3", TlcGiB: 1250, NumCores: 1},
	}

	// Existing nodes (frozen anyway) + 4 spare nodes with 5000 GiB TLC to host the 882-GiB FDs.
	inv := append(
		nodes(3, 2000, 0, 8, "n"),
		nodes(4, 5000, 0, 64, "spare")...,
	)

	plan := planCap(DesiredCapacity{TlcRawGiB: 6395}, s, existingDrives, inv, cons)

	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Grow) != 0 {
		t.Fatalf("AllowInPlaceGrowth=false: want 0 grows, got %d: %v", len(plan.Grow), plan.Grow)
	}
	// 3 new FDs (fewest k with even share <= maxPerFdCap), not the old T0-clone count.
	if len(plan.Create) != 3 {
		t.Fatalf("even-split to delta should create 3 new FDs, got %d: %v\n"+
			"  delta=2645, maxPerFdCap=1279; k=1→2645, k=2→1323 both exceed cap; k=3→882 fits",
			len(plan.Create), plan.Create)
	}
	for i, c := range plan.Create {
		// CeilDiv(2645, 3) = 882; accept 882 or 883 defensively (CeilDiv rounding).
		if c.TlcGiB != 882 && c.TlcGiB != 883 {
			t.Errorf("create[%d].TlcGiB = %d, want ~882 (CeilDiv(delta=2645, k=3))", i, c.TlcGiB)
		}
	}
	if got := 3750 + sumCreateTlc(plan); got < 6395 || got > 6396 {
		t.Errorf("total realized TLC = %d, want ~6396 (3750 + 3×882); should reach desired without over-provisioning", got)
	}
}

// When the shortfall is smaller than a single existing FD, even-split covers it with one new FD sized
// to the shortfall itself (500 GiB), not a full T0-sized clone (1179).
func Test_UniformIncrease_EvenSplit_SubT0_SingleSmallFd(t *testing.T) {
	s := ProtectionScheme{StripeWidth: 3, RedundancyLevel: 2, HotSpare: 0} // minFd = 5
	cons := testCons()
	cons.AllowInPlaceGrowth = false // freeze existing FDs; the shortfall is covered by one fresh FD

	// 5 existing FDs of 1179 GiB each → current = 5895, T0 = 1179.
	existingDrives := []ExistingContainer{
		{Name: "c1", Node: "n1", FDValue: "n1", TlcGiB: 1179, NumCores: 1},
		{Name: "c2", Node: "n2", FDValue: "n2", TlcGiB: 1179, NumCores: 1},
		{Name: "c3", Node: "n3", FDValue: "n3", TlcGiB: 1179, NumCores: 1},
		{Name: "c4", Node: "n4", FDValue: "n4", TlcGiB: 1179, NumCores: 1},
		{Name: "c5", Node: "n5", FDValue: "n5", TlcGiB: 1179, NumCores: 1},
	}

	// Existing nodes (modest headroom — frozen) + 2 spare nodes so one can host a 500 GiB FD.
	inv := append(
		nodes(5, 2000, 0, 8, "n"),
		nodes(2, 5000, 0, 64, "spare")...,
	)

	plan := planCap(DesiredCapacity{TlcRawGiB: 6395}, s, existingDrives, inv, cons)

	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Grow) != 0 {
		t.Fatalf("AllowInPlaceGrowth=false: want 0 grows, got %d: %v", len(plan.Grow), plan.Grow)
	}
	if len(plan.Create) != 1 {
		t.Fatalf("sub-T0 delta should create exactly 1 new FD, got %d: %v", len(plan.Create), plan.Create)
	}
	if c := plan.Create[0]; c.TlcGiB != 500 {
		t.Errorf("create[0].TlcGiB = %d, want 500 (the shortfall, not a T0=1179 clone)", c.TlcGiB)
	}
	if len(plan.OverProvisions) != 0 {
		t.Errorf("total 6395 == desired exactly; want no OverProvision advisory, got %v", plan.OverProvisions)
	}
}

// With no spare node and an in-place grow that clears minGrowthFraction, every existing FD grows to one
// common uniform level (no sub-T fragment, no new FD).
func Test_UniformIncrease_NoSpare_AboveThreshold_GrowsUniformly(t *testing.T) {
	s := testScheme() // minFdNum = 6
	cons := testCons()
	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "n" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, TlcGiB: 30 * tib, NumCores: 6})
	}
	inv := nodes(6, 70*tib, 0, 64, "n") // ample headroom, no fresh FDs
	// current 180 TiB; target 240 TiB -> uniform level 40 TiB == a 33% grow, above minGrowthFraction.
	plan := planCap(DesiredCapacity{TlcRawGiB: 240 * tib}, s, existingDrives, inv, cons)
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Create) != 0 {
		t.Fatalf("no spare nodes: want 0 new FDs, got %d", len(plan.Create))
	}
	if len(plan.Grow) != 6 {
		t.Fatalf("want all 6 existing FDs grown to the uniform level, got %d", len(plan.Grow))
	}
	for _, g := range plan.Grow {
		if g.NewTlcGiB != 40*tib {
			t.Fatalf("want every FD grown to the uniform 40 TiB, got %+v", g)
		}
	}
}

// An over-sized existing FD (anchor) must not raise T0 above the smallest existing FD; a new FD is
// created at T0 and the anchor is left untouched.
func Test_UniformIncrease_OversizedAnchor_DoesNotRaiseFloor(t *testing.T) {
	s := testScheme() // minFdNum = 6
	cons := testCons()
	// 5 FDs at 10000 GiB + 1 anchor at 17000 GiB -> T0 = max(MinChunk, 10000) = 10000.
	var existingDrives []ExistingContainer
	for i := 1; i <= 5; i++ {
		n := "n" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, TlcGiB: 10000, NumCores: 2})
	}
	existingDrives = append(existingDrives, ExistingContainer{Name: "anchor", Node: "n6", FDValue: "n6", TlcGiB: 17000, NumCores: 4})
	inv := append(nodes(6, 50*tib, 0, 64, "n"), nodes(1, 100*tib, 0, 64, "spare")...)
	// current 67000; target 77000 -> delta 10000 = exactly one T0 chunk -> 1 new FD at 10000, no grow.
	plan := planCap(DesiredCapacity{TlcRawGiB: 77000}, s, existingDrives, inv, cons)
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Grow) != 0 {
		t.Fatalf("anchor and existing FDs must be untouched: want 0 grows, got %d: %v", len(plan.Grow), plan.Grow)
	}
	if len(plan.Create) != 1 {
		t.Fatalf("want 1 new FD at T0, got %d: %v", len(plan.Create), plan.Create)
	}
	if plan.Create[0].TlcGiB != 10000 { // T0, not the 17000 anchor size
		t.Fatalf("want the new FD at T0=10000 (not the 17000 anchor), got %+v", plan.Create[0])
	}
}

// Test_UniformIncrease_ScalingDisabled_NoSpare_Infeasible: with in-place growth disabled and no spare node
// to host a new T0 FD, the increase is infeasible with the scaling-disabled message and nothing is grown.
func Test_UniformIncrease_ScalingDisabled_NoSpare_Infeasible(t *testing.T) {
	s := testScheme() // minFdNum = 6
	cons := testCons()
	cons.AllowInPlaceGrowth = false
	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "n" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, TlcGiB: 30 * tib, NumCores: 6})
	}
	inv := nodes(6, 70*tib, 0, 64, "n") // ample headroom (but growth disabled), no fresh FDs
	plan := planCap(DesiredCapacity{TlcRawGiB: 240 * tib}, s, existingDrives, inv, cons)
	if plan.Infeasible == "" {
		t.Fatalf("want infeasible (scaling disabled, no spare), got create=%d grow=%d", len(plan.Create), len(plan.Grow))
	}
	if !strings.Contains(plan.Infeasible, "enableDynamicDriveScalingForSharedDrives") {
		t.Fatalf("want the scaling-disabled message, got %q", plan.Infeasible)
	}
	if len(plan.Grow) != 0 {
		t.Fatalf("scaling disabled must not grow anything, got %d grows", len(plan.Grow))
	}
}

// A deleting container's node re-enters the fresh-candidate pool as the highest-headroom node and would
// win by pure headroom-desc; deprioritization must instead land the restored FD on a genuinely free node.
func Test_GrowRestore_PrefersCleanNodeOverDeletingNode(t *testing.T) {
	s := testScheme() // minFdNum = 6
	cons := testCons()
	// 5 healthy TLC FDs @30 TiB (n1..n5); a 6th was just deleted ⇒ current=150 TiB, restore one 30 TiB FD.
	var existingDrives []ExistingContainer
	for i := 1; i <= 5; i++ {
		n := "n" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, TlcGiB: 30 * tib, NumCores: 6})
	}
	inv := nodes(5, 30*tib, 0, 64, "n") // existing nodes (TLC-used → excluded from fresh placement)
	ndel := node("ndel", 100*tib, 0, 64)
	ndel.HasDeletingDriveContainer = true   // still hosts the just-deleted container; MOST headroom
	nspare := node("nspare", 60*tib, 0, 64) // genuinely free, less (but sufficient) headroom
	inv = append(inv, ndel, nspare)

	plan := planCap(DesiredCapacity{TlcRawGiB: 180 * tib}, s, existingDrives, inv, cons)
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Create) != 1 {
		t.Fatalf("want exactly 1 new FD to restore the deleted one, got %d", len(plan.Create))
	}
	if got := plan.Create[0].Node; got != "nspare" {
		t.Fatalf("replacement FD must land on the free node, not the node it was just deleted from; got %q", got)
	}
}

// The deprioritization is last-resort, not an exclusion: when the deleting node is the only candidate
// that fits, the planner must still restore there rather than go infeasible.
func Test_GrowRestore_FallsBackToDeletingNodeWhenSoleCandidate(t *testing.T) {
	s := testScheme()
	cons := testCons()
	var existingDrives []ExistingContainer
	for i := 1; i <= 5; i++ {
		n := "n" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, TlcGiB: 30 * tib, NumCores: 6})
	}
	inv := nodes(5, 30*tib, 0, 64, "n")
	ndel := node("ndel", 100*tib, 0, 64)
	ndel.HasDeletingDriveContainer = true   // only candidate that can host the 30 TiB chunk
	nsmall := node("nsmall", 10*tib, 0, 64) // clean but too small for the chunk ⇒ must not be preferred
	inv = append(inv, ndel, nsmall)

	plan := planCap(DesiredCapacity{TlcRawGiB: 180 * tib}, s, existingDrives, inv, cons)
	if plan.Infeasible != "" {
		t.Fatalf("must restore on the deleting node when it is the only capable candidate; got infeasible: %s", plan.Infeasible)
	}
	if len(plan.Create) != 1 {
		t.Fatalf("want exactly 1 new FD, got %d", len(plan.Create))
	}
	if got := plan.Create[0].Node; got != "ndel" {
		t.Fatalf("want fallback onto the only capable (deleting) node ndel, got %q", got)
	}
}

// Test_FrozenFDs_NewFDSizedFromMaxCap_NotT0: with AllowInPlaceGrowth=false, a new FD is sized from
// maxPerFdCap (desiredRaw/minFd) rather than replicating T0, so one spare node with enough headroom suffices.
func Test_FrozenFDs_NewFDSizedFromMaxCap_NotT0(t *testing.T) {
	// minFd = stripeWidth(3) + redundancy(2) + hotSpare(0) = 5
	s := ProtectionScheme{StripeWidth: 3, RedundancyLevel: 2, HotSpare: 0}
	cons := testCons()
	cons.AllowInPlaceGrowth = false
	cons.ImbalanceFactor = 0 // disable imbalance guard so the size check is the only constraint

	// 5 existing QLC FDs frozen at 3750 GiB each.
	var existingDrives []ExistingContainer
	for i := 1; i <= 5; i++ {
		n := "n" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, QlcGiB: 3750, NumCores: 2})
	}
	// current = 5*3750 = 18750; desiredRaw = 18750 + 4436 = 23186; delta = 4436
	// maxPerFdCap = 23186/5 = 4637; CeilDiv(4436,1) = 4436 <= 4637 => k=1 fits.
	// One spare node with enough QLC headroom (>=4437 GiB) and sufficient cores/hugepages/memory.
	spare := node("nspare", 0, 10000, 64) // 10000 GiB QLC headroom, 64 cores
	inv := append(nodes(5, 0, 10000, 64, "n"), spare)

	plan := planCap(DesiredCapacity{QlcRawGiB: 23186}, s, existingDrives, inv, cons)
	if plan.Infeasible != "" {
		t.Fatalf("want feasible (new FD sized from maxPerFdCap), got infeasible: %s", plan.Infeasible)
	}
	if len(plan.Create) != 1 {
		t.Fatalf("want exactly 1 new container, got %d: %v", len(plan.Create), plan.Create)
	}
	if plan.Create[0].QlcGiB != 4436 {
		t.Fatalf("want new FD QlcGiB=4436 (CeilDiv(4436,1)), got %d", plan.Create[0].QlcGiB)
	}
}

// Same setup as Test_FrozenFDs_NewFDSizedFromMaxCap_NotT0 but with ImbalanceFactor=1.1, so
// 4436 >= 3750*1.1=4125 triggers the imbalance guard for all k, leaving the plan infeasible.
func Test_FrozenFDs_ImbalanceGuardBlocks(t *testing.T) {
	s := ProtectionScheme{StripeWidth: 3, RedundancyLevel: 2, HotSpare: 0}
	cons := testCons()
	cons.AllowInPlaceGrowth = false
	cons.ImbalanceFactor = 1.1 // 4436 >= 3750*1.1=4125 => imbalance triggered for k=1

	var existingDrives []ExistingContainer
	for i := 1; i <= 5; i++ {
		n := "n" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, QlcGiB: 3750, NumCores: 2})
	}
	spare := node("nspare", 0, 10000, 64)
	inv := append(nodes(5, 0, 10000, 64, "n"), spare)

	plan := planCap(DesiredCapacity{QlcRawGiB: 23186}, s, existingDrives, inv, cons)
	if plan.Infeasible == "" {
		t.Fatalf("want infeasible (imbalance guard blocks all k), got create=%d grow=%d", len(plan.Create), len(plan.Grow))
	}
}

// --- enableDynamicDriveScalingForSharedDrives=false: fresh placement may not grow OR convert an existing
// drive container; new capacity lands only on EMPTY nodes, else infeasible. ---

// Flag off + a QLC-only container asked for TLC too (cross-pool conversion), with no empty node: the
// planner must not convert the existing QLC container to mixed and must report infeasible.
func Test_ScalingDisabled_QlcOnly_AddTlc_NoEmptyNode_Infeasible(t *testing.T) {
	s := testScheme() // minFdNum = 6
	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "n" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, QlcGiB: 28 * tib, NumCores: 6})
	}
	// Same 6 nodes expose TLC headroom (so conversion WOULD be possible if allowed); no empty node exists.
	inv := nodes(6, 100*tib, 0, 58, "n")
	desired := DesiredCapacity{TlcRawGiB: 6 * 28 * tib, QlcRawGiB: 6 * 28 * tib}
	cons := testCons()
	cons.AllowInPlaceGrowth = false // dynamic drive scaling OFF
	plan := planCap(desired, s, existingDrives, inv, cons)
	if plan.Infeasible == "" {
		t.Fatalf("want infeasible (no empty node to place fresh TLC, conversion forbidden), got create=%d grow=%d", len(plan.Create), len(plan.Grow))
	}
	if len(plan.Grow) != 0 {
		t.Fatalf("flag off must not grow/convert any existing container, got %d: %+v", len(plan.Grow), plan.Grow)
	}
	for _, c := range plan.Create {
		if c.TlcGiB > 0 {
			t.Fatalf("flag off must not place TLC on an occupied node, got create %+v", c)
		}
	}
}

// Flag off + same request, but now an empty node is available: the planner must place the new TLC
// capacity as brand-new container(s) on the empty node(s) and never touch the existing QLC containers.
func Test_ScalingDisabled_QlcOnly_AddTlc_EmptyNodesAvailable_CreatesFresh(t *testing.T) {
	s := testScheme() // minFdNum = 6
	var existingDrives []ExistingContainer
	var inv []NodeCapacity
	// 6 QLC-only FDs on QLC-full nodes (no TLC headroom → cannot be converted even if allowed).
	for i := 1; i <= 6; i++ {
		n := "q" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "q" + itoa(i), Node: n, FDValue: n, QlcGiB: 28 * tib, NumCores: 6})
		inv = append(inv, node(n, 0, 0, 58))
	}
	// 6 EMPTY nodes with TLC headroom — the only legal home for the new TLC pool.
	for i := 1; i <= 6; i++ {
		n := "e" + itoa(i)
		inv = append(inv, node(n, 100*tib, 0, 58))
	}
	desired := DesiredCapacity{TlcRawGiB: 6 * 28 * tib, QlcRawGiB: 6 * 28 * tib}
	cons := testCons()
	cons.AllowInPlaceGrowth = false
	plan := planCap(desired, s, existingDrives, inv, cons)
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible (empty nodes should host fresh TLC): %s", plan.Infeasible)
	}
	if len(plan.Grow) != 0 {
		t.Fatalf("flag off must not grow/convert existing QLC containers, got %d: %+v", len(plan.Grow), plan.Grow)
	}
	if len(plan.Create) == 0 || sumCreateTlc(plan) < 6*28*tib {
		t.Fatalf("want fresh TLC containers covering 168 TiB on empty nodes, got create=%d tlc=%d", len(plan.Create), sumCreateTlc(plan))
	}
	for _, c := range plan.Create {
		if strings.HasPrefix(c.Node, "q") {
			t.Fatalf("flag off must not create on an occupied (q*) node, got %+v", c)
		}
	}
}

// Flag ON, same shape as the infeasible case: the existing QLC-only containers ARE converted to mixed in
// place (no new containers). Guards that the flag-ON path is unchanged by the flag-OFF exclusion rule.
func Test_ScalingEnabled_QlcOnly_AddTlc_ConvertsInPlace(t *testing.T) {
	s := testScheme() // minFdNum = 6
	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "n" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, QlcGiB: 28 * tib, NumCores: 6})
	}
	inv := nodes(6, 100*tib, 0, 58, "n") // QLC full, TLC headroom available on the same nodes
	desired := DesiredCapacity{TlcRawGiB: 6 * 28 * tib, QlcRawGiB: 6 * 28 * tib}
	cons := testCons() // AllowInPlaceGrowth = true (default)
	plan := planCap(desired, s, existingDrives, inv, cons)
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Create) != 0 {
		t.Fatalf("flag on: conversion should not create new containers, got %d: %+v", len(plan.Create), plan.Create)
	}
	if len(plan.Grow) != 6 {
		t.Fatalf("flag on: want 6 QLC-only→mixed conversions, got %d: %+v", len(plan.Grow), plan.Grow)
	}
	for _, g := range plan.Grow {
		if g.NewTlcGiB != 28*tib || g.NewQlcGiB != 28*tib {
			t.Fatalf("flag on: want converted to mixed TLC28+QLC28, got %+v", g)
		}
	}
}

// FullDriveCores: one core per drive, capped at cons.MaxCoresPerContainer when set (>0), unbounded when
// cons is nil or the cap is unset, and always 0 for numDrives <= 0.
func TestFullDriveCores(t *testing.T) {
	capped := &CapacityConstraints{MaxCoresPerContainer: 19}
	uncapped := &CapacityConstraints{} // MaxCoresPerContainer left zero-valued: unbounded

	cases := []struct {
		name      string
		numDrives int
		cons      *CapacityConstraints
		want      int
	}{
		{"zero drives, nil cons", 0, nil, 0},
		{"negative drives, capped cons", -3, capped, 0},
		{"nil cons is unbounded", 25, nil, 25},
		{"zero-valued cap is unbounded", 25, uncapped, 25},
		{"below the cap is unaffected", 10, capped, 10},
		{"exactly at the cap", 19, capped, 19},
		{"above the cap is clamped to the cap", 25, capped, 19},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := FullDriveCores(c.numDrives, c.cons); got != c.want {
				t.Errorf("FullDriveCores(%d, %+v) = %d, want %d", c.numDrives, c.cons, got, c.want)
			}
		})
	}
}

// RequiredComputeCores: the drive-core total is a hard floor the configured ratios can only exceed, never
// undercut; fullDrives selects FullDrivesComputeToDriveCoreRatio (TLC-only) instead of the TLC/QLC pair.
func TestRequiredComputeCores(t *testing.T) {
	unset := &CapacityConstraints{} // all ratios zero-valued: floor always wins
	sharing := &CapacityConstraints{ComputeToTlcDriveCoreRatio: 2.0, ComputeToQlcDriveCoreRatio: 1.0}
	subFloor := &CapacityConstraints{ComputeToTlcDriveCoreRatio: 0.5} // below 1:1 -> floor still wins
	fullDrivesRatio := &CapacityConstraints{FullDrivesComputeToDriveCoreRatio: 2.0}
	fractional := &CapacityConstraints{ComputeToTlcDriveCoreRatio: 1.3} // exercises the ceil()

	cases := []struct {
		name          string
		tlcDriveCores int
		qlcDriveCores int
		fullDrives    bool
		cons          *CapacityConstraints
		want          int
	}{
		{"nil cons floors to the total regardless of fullDrives", 5, 3, false, nil, 8},
		{"zero-valued ratios floor to the total", 5, 3, false, unset, 8},
		{"drive-sharing ratios exceed the floor", 5, 3, false, sharing, 13},                      // ceil(2*5+1*3)=13
		{"sub-1.0 ratio never takes compute below the floor", 10, 0, false, subFloor, 10},        // ceil(0.5*10)=5 < 10
		{"fullDrives selects the full-drives ratio (TLC-only)", 5, 0, true, fullDrivesRatio, 10}, // ceil(2*5)=10
		{"fullDrives ignores the drive-sharing ratios entirely", 5, 0, true, sharing, 5},         // qlcRatio forced to 0, tlcRatio from...
		{"fractional ratio rounds up via ceil", 5, 0, false, fractional, 7},                      // ceil(1.3*5)=ceil(6.5)=7
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RequiredComputeCores(c.tlcDriveCores, c.qlcDriveCores, c.fullDrives, c.cons)
			if got != c.want {
				t.Errorf("RequiredComputeCores(%d, %d, %v, %+v) = %d, want %d",
					c.tlcDriveCores, c.qlcDriveCores, c.fullDrives, c.cons, got, c.want)
			}
		})
	}
}

// MaxCoresPerContainer is a hard per-container limit: when a new drive container would need more cores
// than the cap, fail fast with Binding "driveCores" rather than silently over-sizing.
func TestPlanCapacity_MaxCoresPerContainer_DerivedCoresAboveLimit_Infeasible(t *testing.T) {
	s := singleParityScheme() // minFdNum = 3
	cons := testCons()
	cons.AllowSingleParity = true
	cons.MaxCoresPerContainer = 2 // each container may hold at most 2 cores

	// 46080 GiB / 3 FDs = 15360 GiB/container; at TlcCapacityPerCoreGiB=5120 that derives 3 cores, one
	// more than the 2-core cap.
	plan := planCap(
		DesiredCapacity{TlcRawGiB: 46080},
		s,
		nil,
		nodes(3, 100*tib, 0, 1000, "n"),
		cons,
	)
	if plan.Infeasible == "" {
		t.Fatalf("expected infeasible (derived cores exceed MaxCoresPerContainer), got a feasible plan: %+v", plan)
	}
	if !strings.Contains(plan.Infeasible, "needs 3 cores") || !strings.Contains(plan.Infeasible, "above the 2-core per-container limit") {
		t.Fatalf("Infeasible = %q, want it to cite the derived core count (3) and the configured limit (2)",
			plan.Infeasible)
	}
	if plan.Infeasibility == nil || plan.Infeasibility.Binding != "driveCores" {
		t.Fatalf("Infeasibility.Binding = %+v, want %q", plan.Infeasibility, "driveCores")
	}
}

// orderFitNodesByFreshFD ordering. AUTO mode throughout (fdOf is identity), so these isolate node
// selection from FD-spread grouping.

func autoFdOf(node string) string { return node }

func Test_OrderFitNodesByFreshFD_DeterministicAcrossRuns(t *testing.T) {
	cons := testCons()
	newStates := func() map[string]*nodeState {
		return map[string]*nodeState{
			"n1": stateFrom(node("n1", 0, 0, 100)),
			"n2": stateFrom(node("n2", 0, 0, 90)),
			"n3": stateFrom(node("n3", 0, 0, 50)),
		}
	}
	first := orderFitNodesByFreshFD([]string{"n1", "n2", "n3"}, newStates(), nil, nil, 4, 0, autoFdOf, cons)
	second := orderFitNodesByFreshFD([]string{"n3", "n1", "n2"}, newStates(), nil, nil, 4, 0, autoFdOf, cons)
	if strings.Join(first, ",") != strings.Join(second, ",") {
		t.Errorf("order not deterministic: got %v then %v for the same states regardless of input order", first, second)
	}
}

// Test_CountPoolCapableNodes_IneligibleNode asserts an ineligible node (cordoned/not ready/untolerated
// taint) is excluded from the "can physically host this pool" count unless poolUsed already credits it
// with a pool-p container — a fresh placement can never land on an ineligible node, so counting it anyway
// would inflate a pool's candidate count with a node no new container can actually reach.
func Test_CountPoolCapableNodes_IneligibleNode(t *testing.T) {
	states := map[string]*nodeState{
		"eligible":           stateFrom(NodeCapacity{NodeName: "eligible", TlcGiB: 10 * tib}),
		"ineligible-unused":  stateFrom(NodeCapacity{NodeName: "ineligible-unused", TlcGiB: 10 * tib, IneligibleReason: "cordoned"}),
		"ineligible-hosting": stateFrom(NodeCapacity{NodeName: "ineligible-hosting", TlcGiB: 10 * tib, IneligibleReason: "not ready"}),
		"no-tlc-capacity":    stateFrom(NodeCapacity{NodeName: "no-tlc-capacity", TlcGiB: 0}),
	}
	poolUsed := map[string]struct{}{"ineligible-hosting": {}}

	// eligible + ineligible-hosting (already credited via poolUsed) = 2; ineligible-unused and
	// no-tlc-capacity are both excluded.
	if got := countPoolCapableNodes(states, poolUsed, poolTLC); got != 2 {
		t.Fatalf("countPoolCapableNodes = %d, want 2 (eligible + ineligible-but-already-hosting; "+
			"ineligible-unused must not count)", got)
	}

	// Without any poolUsed credit, the ineligible-hosting node drops out too.
	if got := countPoolCapableNodes(states, nil, poolTLC); got != 1 {
		t.Fatalf("countPoolCapableNodes with no poolUsed = %d, want 1 (only the plain eligible node)", got)
	}
}
