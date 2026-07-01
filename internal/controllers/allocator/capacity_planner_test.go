package allocator

import (
	"strings"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"

	globalconfig "github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/pkg/util"
)

// These tests are intentionally self-explanatory: each documents a clusterCapacity planning scenario
// (homogeneous/heterogeneous nodes, capacity taken by other clusters, migration, grow/convert/create,
// shrink, fail-fast) and asserts the planner's decisions. All capacities are in GiB.

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
		// Default matches enableDynamicDriveScalingForSharedDrives=true (in-place growth allowed); the
		// disabled-flag scenarios set this to false explicitly.
		AllowInPlaceGrowth: true,
		// Mirror the helm/config defaults so the uniform-increase path uses the production fractions.
		MinGrowthFraction:        0.2,
		MaxOverProvisionFraction: 0.2,
	}
}

func ratio(tlc, qlc int) *weka.DriveTypesRatio { return &weka.DriveTypesRatio{Tlc: tlc, Qlc: qlc} }

// allEligible marks every inventory node as compute-eligible (the converged default these tests assume:
// compute co-located on the drive nodes). Dedicated compute-node-pool tests pass an explicit map instead.
func allEligible(inv []NodeCapacity) map[string]bool {
	m := make(map[string]bool, len(inv))
	for _, nc := range inv {
		m[nc.NodeName] = true
	}
	return m
}

// netCompute subtracts each existing compute container's footprint (cores/hugepages/memory) from its
// node's headroom in the inventory, mirroring what buildNodeInventory does in production. PlanCapacity
// no longer charges compute itself — the inventory it receives is already net of every weka container on
// the node — so scenario tests that exercise compute-aware drive placement / freeze logic pre-net their
// inventory through this. The math is identical to the planner's former per-node compute charge.
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

// planCap wraps PlanCapacity with every inventory node compute-eligible, so the existing scenario tests
// (which size compute over the drive nodes) keep asserting the same behavior.
func planCap(desired DesiredCapacity, scheme ProtectionScheme, existingDrives []ExistingContainer, inventory []NodeCapacity, cons *CapacityConstraints) CapacityPlan {
	return PlanCapacity(desired, scheme, existingDrives, nil, inventory, allEligible(inventory), cons)
}

// desiredFrom mirrors the controller: inflate usable to raw, then split by ratio.
func desiredFrom(usableGiB int, s ProtectionScheme, r *weka.DriveTypesRatio) DesiredCapacity {
	raw := RawCapacityGiB(usableGiB, s.StripeWidth, s.RedundancyLevel, s.HotSpare)
	tlc, qlc := weka.GetTlcQlcCapacity(raw, r)
	return DesiredCapacity{TlcRawGiB: tlc, QlcRawGiB: qlc}
}

// node builds a candidate node with generous hugepages/memory so only drive capacity and cores bind
// (unless a test overrides them). FDValue defaults to the node name (AUTO mode, FD = host).
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

// withAllowSingleParity flips the operator-level AllowSingleParity flag for the duration of the test
// and restores it afterwards (the flag is a process-global read by MinProtectionFloor).
func withAllowSingleParity(t *testing.T, enabled bool) {
	t.Helper()
	prev := globalconfig.Config.DriveSharing.AllowSingleParity
	globalconfig.Config.DriveSharing.AllowSingleParity = enabled
	t.Cleanup(func() { globalconfig.Config.DriveSharing.AllowSingleParity = prev })
}

func Test_MinProtectionFloor_GatedBySingleParityFlag(t *testing.T) {
	withAllowSingleParity(t, false)
	if sw, rl, hs := MinProtectionFloor(); sw != 3 || rl != 2 || hs != 0 {
		t.Fatalf("default floor must be 3/2/0, got %d/%d/%d", sw, rl, hs)
	}
	withAllowSingleParity(t, true)
	if sw, rl, hs := MinProtectionFloor(); sw != 2 || rl != 1 || hs != 0 {
		t.Fatalf("single-parity floor must be 2/1/0, got %d/%d/%d", sw, rl, hs)
	}
}

func Test_SingleParity_RejectedWhenFlagOff(t *testing.T) {
	withAllowSingleParity(t, false)
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
	withAllowSingleParity(t, true)
	s := singleParityScheme() // minFdNum = 3
	// 30 TiB usable, QLC-only. raw = usable × (2+1+0)/2 = 1.5× = 45 TiB across 3 FDs ⇒ 15 TiB/FD.
	plan := planCap(
		desiredFrom(30*tib, s, ratio(0, 1)),
		s,
		nil,
		nodes(3, 0, 100*tib, 64, "q"),
		testCons(),
	)
	if plan.Infeasible != "" {
		t.Fatalf("2+1+0 must be feasible with the flag on, got Infeasible=%q", plan.Infeasible)
	}
	if len(plan.Create) != 3 {
		t.Fatalf("want 3 QLC containers (one per FD = minFdNum), got %d", len(plan.Create))
	}
	if got := sumCreateQlc(plan); got != 45*tib {
		t.Fatalf("want total created QLC raw 45 TiB (1.5× usable), got %d GiB", got)
	}
}

func Test_Greenfield_Homogeneous_TLOnly_SpreadsEvenlyAcrossMinFdNum(t *testing.T) {
	s := testScheme()
	plan := planCap(
		desiredFrom(90*tib, s, ratio(1, 0)), // rawTLC = 180 TiB
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
	if got := sumCreateTlc(plan); got != 180*tib {
		t.Fatalf("want total created TLC 180 TiB, got %d GiB", got)
	}
	for _, c := range plan.Create {
		if c.Type != DriveTypeTLC || c.QlcGiB != 0 || c.TlcGiB != 30*tib {
			t.Fatalf("want each container TLC=30 TiB type=tlc, got %+v", c)
		}
	}
}

func Test_Greenfield_MoreNodesThanMinFd_UsesMinFdNum(t *testing.T) {
	s := testScheme() // minFdNum = 6
	// 12 capable nodes, but the target fits comfortably in minFdNum: the planner must create exactly
	// minFdNum (6) containers, NOT spread thinly across all 12.
	plan := planCap(
		desiredFrom(90*tib, s, ratio(1, 0)), // rawTLC = 180 TiB
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
		if c.TlcGiB != 30*tib { // 180 / 6
			t.Fatalf("want each container TLC=30 TiB, got %+v", c)
		}
	}
}

func Test_Greenfield_MinFdNumTooSmall_ExtendsFdCount(t *testing.T) {
	s := testScheme() // minFdNum = 6
	// Each node holds only 20 TiB, so minFdNum FDs (6 × 20 = 120 TiB) cannot hold rawTLC 180 TiB.
	// The planner must extend beyond minFdNum until the capacity fits (9 FDs × 20 TiB = 180 TiB).
	plan := planCap(
		desiredFrom(90*tib, s, ratio(1, 0)), // rawTLC = 180 TiB
		s,
		nil,
		nodes(12, 20*tib, 0, 64, "n"),
		testCons(),
	)
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Create) != 9 {
		t.Fatalf("want 9 containers (extended from minFdNum to fit capacity), got %d", len(plan.Create))
	}
	if got := sumCreateTlc(plan); got != 180*tib {
		t.Fatalf("want total created TLC 180 TiB, got %d GiB", got)
	}
}

func Test_Greenfield_Heterogeneous_AddsFdToBalance(t *testing.T) {
	s := testScheme() // minFdNum = 6
	// Heterogeneous ceilings: 2 big (100 TiB) + 5 medium (64 TiB) = 7 FDs available, rawTLC 420 TiB.
	// The minFdNum=6 prefix already has cumulative headroom >= target (100+100+64*4 = 456 TiB), so the
	// old "fewest FDs that hold the capacity" rule would stop at 6 and fill UNEVENLY: the 64 TiB nodes
	// cap below the ceil(420/6)=70 TiB even share, so the two big nodes absorb the surplus (~82 vs 64).
	// The add-FD-until-even rule instead opens a 7th FD because 70 TiB exceeds a chosen node's 64 TiB
	// ceiling; at 7 FDs the even share ceil(420/7)=60 TiB fits under every ceiling -> 7 x 60 TiB, even,
	// no imbalance warning. (Mirrors the live greenfield "7 x 58515" layout — issue #13.)
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
	// raw 240 TiB; tlcRaw 60, qlcRaw 180.
	if got := sumCreateTlc(plan); got != 60*tib {
		t.Fatalf("want total TLC 60 TiB, got %d", got)
	}
	if got := sumCreateQlc(plan); got != 180*tib {
		t.Fatalf("want total QLC 180 TiB, got %d", got)
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

// labelNode builds a candidate node whose FAILURE DOMAIN is an explicit label value (rack), distinct
// from the node name. Several nodes may share one fd (a multi-host failure domain), the label-based mode
// the AUTO-mode `node` helper cannot express (it forces FDValue == NodeName).
func labelNode(name, fd string, tlcGiB, cores int) NodeCapacity {
	nc := node(name, tlcGiB, 0, cores)
	nc.FDValue = fd
	return nc
}

// Greenfield, LABEL-BASED FD mode with MULTIPLE HOSTS PER FD (mirrors example-12: 6 racks × 2 hosts).
// createSpread must select FAILURE DOMAINS, not the globally-largest-headroom NODES. Picking the minFd
// (6) largest-headroom nodes would collapse into fewer than 6 distinct racks (several racks share their
// two hosts' headroom), falsely tripping poolFeasibility ("only N of 6 required failure domains have
// capacity"). The fix groups candidates by FDValue and spans >= minFd distinct FDs.
func Test_Greenfield_LabelBasedFD_MultiHostPerFD_SpansAllFDs(t *testing.T) {
	s := testScheme() // minFdNum = 6
	// 6 racks, 2 hosts each (12 nodes). Per-rack headroom is uneven across racks (so a node-greedy
	// pick would concentrate on the few fattest racks), but EVERY rack has far more than the 30 TiB/FD
	// target, so an FD-aware planner places one balanced share per rack.
	rackTlc := []int{75 * tib, 70 * tib, 65 * tib, 60 * tib, 55 * tib, 50 * tib} // rack-1..rack-6
	var inv []NodeCapacity
	for r := 1; r <= 6; r++ {
		fd := "rack-" + itoa(r)
		// two hosts in this rack, each with the rack's per-node headroom
		inv = append(inv,
			labelNode("h"+itoa(r)+"a", fd, rackTlc[r-1], 64),
			labelNode("h"+itoa(r)+"b", fd, rackTlc[r-1], 64),
		)
	}

	plan := planCap(desiredFrom(90*tib, s, ratio(1, 0)), s, nil, inv, testCons()) // rawTLC = 180 TiB

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
	if got := sumCreateTlc(plan); got != 180*tib {
		t.Fatalf("want total created TLC 180 TiB, got %d GiB", got)
	}
	// Capacity is balanced PER FD: each rack carries the same 30 TiB share (180 / 6).
	for fd, v := range perFD {
		if v != 30*tib {
			t.Fatalf("FD %s holds %d GiB, want 30 TiB equal across all FDs (per-FD balance): %v", fd, v, perFD)
		}
	}
}

// Same label-based topology, but each FD's capacity needs MORE THAN ONE HOST to hold its share. The
// chosen FD set must still span exactly minFd distinct racks while using both hosts within a rack for
// capacity — distinct-FD count stays at minFd, never collapsing below it.
func Test_Greenfield_LabelBasedFD_UnevenHosts_UsesMultipleHostsPerFD(t *testing.T) {
	s := testScheme() // minFdNum = 6, 30 TiB per FD for 180 TiB raw
	// 6 racks, 2 hosts each, but each host holds only 20 TiB (< the 30 TiB per-FD share). Both hosts in
	// a rack are needed to carry that rack's 30 TiB; the plan must still land on exactly 6 racks.
	var inv []NodeCapacity
	for r := 1; r <= 6; r++ {
		fd := "rack-" + itoa(r)
		inv = append(inv,
			labelNode("h"+itoa(r)+"a", fd, 20*tib, 64),
			labelNode("h"+itoa(r)+"b", fd, 20*tib, 64),
		)
	}

	plan := planCap(desiredFrom(90*tib, s, ratio(1, 0)), s, nil, inv, testCons()) // rawTLC = 180 TiB

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
	if got := sumCreateTlc(plan); got != 180*tib {
		t.Fatalf("want total created TLC 180 TiB, got %d GiB", got)
	}
	for fd, v := range perFD {
		if v != 30*tib {
			t.Fatalf("FD %s holds %d GiB, want 30 TiB per FD across two hosts: %v", fd, v, perFD)
		}
	}
	// More than one container per FD is expected here (20 TiB host cap < 30 TiB FD share).
	if len(plan.Create) <= 6 {
		t.Fatalf("want multiple hosts per FD (>6 containers) to hold each 30 TiB FD share, got %d", len(plan.Create))
	}
}

// disc #13: greenfield label-based FD with an UNEVEN HOST COUNT per FD must balance capacity PER FAILURE
// DOMAIN, not per node. rack-1 has 3 hosts, racks 2-6 have 2 hosts each (13 nodes). raw 180 TiB => every
// rack gets the same 30 TiB share regardless of host count: rack-1 splits 30 TiB across its 3 hosts (10
// TiB each, 3 containers), the 2-host racks across 2 hosts (15 TiB each, 2 containers). Before the fix the
// create rounds loop sized per node (raw/13 ~ 14.2 TiB each), so rack-1 got ~42.5 TiB vs ~28.4 TiB for the
// 2-host racks, tripping the FD-imbalance warning ("usable gated by smallest FD"). Verified live as Test K.
func Test_Greenfield_LabelBasedFD_UnevenHostCount_BalancesPerFD(t *testing.T) {
	s := testScheme() // minFdNum = 6, 30 TiB per FD for 180 TiB raw
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

	plan := planCap(desiredFrom(90*tib, s, ratio(1, 0)), s, nil, inv, testCons()) // rawTLC = 180 TiB

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
	if got := sumCreateTlc(plan); got != 180*tib {
		t.Fatalf("want total created TLC 180 TiB, got %d GiB", got)
	}
	// KEY (disc #13): per-FD balance regardless of host count — every rack holds 30 TiB, NOT proportional
	// to its host count.
	for fd, v := range perFD {
		if v != 30*tib {
			t.Fatalf("FD %s holds %d GiB, want 30 TiB equal across all FDs (per-FD balance, not per-node): %v", fd, v, perFD)
		}
	}
	// rack-1 (3 hosts) splits its 30 TiB share across all 3 hosts; the 2-host racks across 2.
	if perFDcount["rack-1"] != 3 {
		t.Fatalf("rack-1 (3 hosts) should split its 30 TiB across 3 containers, got %d: %v", perFDcount["rack-1"], perFDcount)
	}
	for r := 2; r <= 6; r++ {
		fd := "rack-" + itoa(r)
		if perFDcount[fd] != 2 {
			t.Fatalf("%s (2 hosts) should split its 30 TiB across 2 containers, got %d: %v", fd, perFDcount[fd], perFDcount)
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

// Bug #11: COMPUTE layout must span >= MinFdNum (SW+RL+HS) distinct failure domains in label-based FD
// mode (mirrors example-12: 6 racks × 2 hosts). The drive side (#10) already spreads across all 6 racks;
// before this fix the compute selection was a pure best-fit-by-cores pick that could pile the compute
// containers onto a few high-headroom racks (e.g. rack-5 ×2, rack-6 ×2 + 2 others), landing compute on
// only 4 distinct racks. Weka then refuses to initialize ("would leave 4 failure domains with compute
// nodes but Weka requires at least 5"). The fix orders the free compute nodes for FD spread so the chosen
// nodes cover >= MinFdNum distinct FDs (held to the same minFdNum as the drive side / count floor, one FD
// above Weka's strict SW+RL minimum, for FD-level recreation headroom).
func Test_Greenfield_LabelBasedFD_Compute_SpansMinFdNumFDs(t *testing.T) {
	s := testScheme() // SW=3, RL=2, HS=1 => minFdNum=6, compute must span >= MinFdNum = 6 FDs
	cons := testCons()
	cons.MaxComputeCoresPerNode = 16
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

// Bug #11 fail-fast: if the COMPUTE-ELIGIBLE inventory has fewer than MinFdNum distinct failure domains,
// the planner must surface a clear Infeasible reason rather than placing compute on too few FDs. Compute is
// held to MinFdNum=6 (SW+RL+HS), one FD above Weka's strict SW+RL=5 minimum. Here drives span all 6 racks
// (feasible); compute is restricted (roleSelector) to nodes in only 5 racks — Weka alone would accept 5,
// but we require MinFdNum=6 for consistency + recreation headroom, so this must fail fast.
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
	// 12 capable nodes, rawTLC = 180 TiB, driveContainers=8 -> exactly 8 containers of 180/8 = 22.5 TiB.
	plan := planCap(desiredDrive(90*tib, s, ratio(1, 0), 8, 0), s, nil, nodes(12, 100*tib, 0, 64, "n"), testCons())
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Create) != 8 {
		t.Fatalf("want exactly 8 drive containers, got %d", len(plan.Create))
	}
	if got := sumCreateTlc(plan); got != 180*tib {
		t.Fatalf("want total TLC 180 TiB, got %d", got)
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
		if g.NewTlcGiB != 40*tib { // 30 -> 40 TiB (delta 60/6 = 10 each)
			t.Fatalf("want grown TLC 40 TiB, got %+v", g)
		}
	}
}

// Test_Grow_LabelBasedFD_BalancesPerFailureDomain_NotPerContainer exercises label-based mode where one
// FD spans TWO hosts and the rest span one. Balancing must equalize per FAILURE DOMAIN (the sum of its
// hosts), not per container — so the 2-host FD must NOT end up with double the capacity of the others.
// (Against per-container balancing this asserts the bug: the 2-host FD would grow to ~2x the others.)
func Test_Grow_LabelBasedFD_BalancesPerFailureDomain_NotPerContainer(t *testing.T) {
	s := testScheme() // minFdNum = 6

	// 6 FDs (rack labels). r1 has two hosts (n1a, n1b); r2..r6 have one host each. Every host runs a
	// 10 TiB TLC container, so r1 currently holds 20 TiB and r2..r6 hold 10 TiB — pre-existing skew.
	existingDrives := []ExistingContainer{
		{Name: "c1a", Node: "n1a", FDValue: "r1", TlcGiB: 10 * tib, NumCores: 2},
		{Name: "c1b", Node: "n1b", FDValue: "r1", TlcGiB: 10 * tib, NumCores: 2},
	}
	for i := 2; i <= 6; i++ {
		n := "n" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: "r" + itoa(i), TlcGiB: 10 * tib, NumCores: 2})
	}

	// Ample per-node headroom so placement is gated only by the per-FD target. The grow path keys on
	// node name; the FD identity that matters lives on the existing containers above.
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

// Cross-pool conversion on the INCREASE path (planPoolUniformIncrease, QLC pool already has FDs) — distinct
// from the greenfield conversion above. A QLC increase with no spare empty node converts the TLC-only
// containers on QLC-capable nodes to mixed (Step-4 create-as-grow), and must NOT double-count those cap-0
// nodes (they are fresh candidates, not existing QLC reach). Regression guard for the existingReach fix.
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
	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "n" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, TlcGiB: 40 * tib, NumCores: 8})
	}
	plan := planCap(desiredFrom(90*tib, s, ratio(1, 0)), s, existingDrives, nodes(6, 20*tib, 0, 20, "n"), testCons())
	if len(plan.Grow) != 0 || len(plan.Create) != 0 {
		t.Fatalf("shrink must be a no-op, got grow=%d create=%d", len(plan.Grow), len(plan.Create))
	}
	if len(plan.ShrinkEvents) == 0 {
		t.Fatalf("want a shrink event")
	}
}

// An over-provision within MaxOverProvisionFraction (the intentional create-new rounding overage) must
// NOT emit the ClusterCapacityShrink advisory — it would contradict ClusterCapacityOverProvisioned.
func Test_Shrink_WithinOverProvisionCap_NoEvent(t *testing.T) {
	s := testScheme()
	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "n" + itoa(i)
		// 6×34 = 204 TiB current vs 180 desired (90 usable): overage 24 TiB < 20% cap (36 TiB) → silent.
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
	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "n" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, TlcGiB: 30 * tib, NumCores: 6})
	}
	// desired raw == current 180 TiB.
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

// NEW behavior (uniform-or-infeasible): with the existing pool at a small uniform chunk T0=10 TiB and a
// large +180 TiB increase, covering the delta needs ceil(180/10)=18 fresh FDs of 10 TiB, but only 6 big
// fresh nodes exist and the small existing nodes are drive-near-full (5 TiB headroom < the 10 TiB chunk),
// so no uniform layout reaches the target -> ClusterCapacityInfeasible. The old heterogeneous "fresh
// balanced set, delete the small ones" fallback is gone: a heterogeneous fill wastes raw for the same usable
// as a uniform tiling, so we never build it (was: 6 fresh containers of 40 TiB + a delete-old warning).
// Heterogeneous-growth fallback (balancedFresh): when a fresh per-FD chunk would DWARF the existing tiny
// FDs (detectImbalance: chunk >= ImbalanceFactor × existing per-FD average), the existing small FDs are
// abandoned and a fresh UNIFORM set is laid out on the empty big nodes — the dwarfed FDs would otherwise
// cap the uniform level and gate the pool's usable capacity. The old containers are left running and
// flagged deletable via a ClusterCapacityHeterogeneousGrowth Warning.
func Test_Heterogeneous_BalancedFresh_IgnoresExisting_WarnsDeleteOld(t *testing.T) {
	s := testScheme()
	// 6 small existing TLC containers (10 TiB each) on small nodes (near-full), plus 6 big empty nodes.
	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "small" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, TlcGiB: 10 * tib, NumCores: 2})
	}
	// Small nodes are near-full on DRIVE capacity (5 TiB) but still have CPU cores for a compute
	// container (cores are independent of drive-capacity headroom).
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

// Gate-drop: the heterogeneous fallback is a pure CREATE-on-fresh-nodes op (it abandons the dwarfed FDs,
// never grows them), so it must fire REGARDLESS of enableDynamicDriveScalingForSharedDrives. This is the
// same scenario as Test_Heterogeneous_BalancedFresh_IgnoresExisting_WarnsDeleteOld but with
// AllowInPlaceGrowth=false — the plan must be identical (6 fresh 40 TiB FDs, 0 grows, deletion Warning).
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

// NEW behavior: even-split-to-delta at the PREFERRED count. 6 full existing FDs at T0=20 TiB + 6 fresh
// nodes; delta=90 TiB. The preferred count is kBase=CeilDiv(delta,T0)=CeilDiv(90,20)=5 — the same count
// T0-cloning would use — and the per-FD size CeilDiv(90,5)=18 TiB comes out <= T0 (never bigger than the
// frozen existing FDs). 6 fresh nodes cover the 5 FDs, so 5 new FDs of 18 TiB = 90 TiB EXACTLY (no
// over-provision). Existing specs are untouched. This replaces the old T0-clone behavior (5 FDs of the full
// T0=20 TiB = 100 TiB, over-provisioning by 10 TiB): sizing each new FD to delta/kBase reaches desired
// exactly while keeping the new FDs <= the existing ones.
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
	// current 120 TiB; raise to 210 TiB raw -> delta 90 TiB lands on the fresh nodes as 5 even-split FDs.
	desired := DesiredCapacity{TlcRawGiB: 210 * tib}
	plan := planCap(desired, s, existingDrives, inv, testCons())
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Grow) != 0 {
		t.Fatalf("old nodes are full; want 0 grows, got %d", len(plan.Grow))
	}
	if len(plan.Create) != 5 { // preferred count kBase=CeilDiv(90,20)=5; perFd=CeilDiv(90,5)=18 TiB (<= T0)
		t.Fatalf("want 5 new even-split FDs (18 TiB each), got %d: %v", len(plan.Create), plan.Create)
	}
	for _, c := range plan.Create {
		if c.TlcGiB != 18*tib {
			t.Fatalf("want each new FD at CeilDiv(delta=90 TiB, kBase=5)=18 TiB, got %+v", c)
		}
	}
	if got := sumCreateTlc(plan); got != 90*tib { // 5 × 18 TiB = the delta EXACTLY
		t.Fatalf("want 90 TiB created (even-split to delta), got %d", got)
	}
	if len(plan.OverProvisions) != 0 {
		t.Fatalf("even-split reaches desired exactly; want no over-provision advisory, got %v", plan.OverProvisions)
	}
}

// NEW behavior (uniform-or-infeasible): with only minFdNum heterogeneous nodes (3 big + 3 small) the
// planner cannot tile 180 TiB into 6 uniform FDs — at N=6 the per-FD share is ceil(180/6)=30 TiB but the
// small nodes cap at 25 TiB, and there are no extra candidates to grow N (-> a smaller share). A uniform
// fill is gated by the smallest FD, so the heterogeneous 35/25 fill (same usable as 6×25 but more raw) is
// never built; the pool is ClusterCapacityInfeasible (was: a feasible uneven fill + per-FD imbalance advisory).
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
	// 14 existing TLC containers (well above minFdNum), all equal at 30 TiB, on nodes with ample
	// headroom. The pre-fix planner would top up only ~minFdNum of them (delta/minFd per FD); the
	// fix spreads delta evenly across ALL 14 so they converge to an equal per-FD capacity.
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
	// Pre-existing skew (e.g. after an earlier concentrated grow): 8 small FDs + 6 large FDs.
	// A further grow with enough delta should top up the smaller FDs MORE than the larger ones,
	// driving all FDs toward an equal per-FD capacity instead of preserving the skew.
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

// NEW behavior: even-split-to-delta covers the increase on FRESH FDs WITHOUT growing the existing ones.
// Only 3 existing FDs (T0=30 TiB) + 6 fresh nodes; delta=270 TiB. maxPerFdCap = 360 TiB/minFd(6) = 60 TiB.
// The preferred count is kBase=CeilDiv(delta,T0)=CeilDiv(270,30)=9, but only 6 fresh nodes are available, so
// node scarcity caps the count at 6: perFd=CeilDiv(270,6)=45 TiB (<= maxPerFdCap 60, below the imbalance
// boundary existingAvg=30 TiB × 2.0 = 60) → 6 new FDs of 45 TiB = 270 TiB EXACTLY, 0 grows. The new FDs are
// LARGER than T0 here only because there aren't enough spare nodes for the preferred 9-FD (30 TiB each)
// layout. Final layout = 3 existing (untouched) + 6 new = 9 FDs (>= minFd). This replaces the old behavior
// (grow existing 30->40 TiB + 6 new at 40 TiB): the preferred no-grow cover reaches desired exactly with no
// in-place growth.
func Test_Grow_ExistingFewerThanMinFd_TopsUpExistingAndCreatesNew(t *testing.T) {
	s := testScheme() // minFdNum = 6
	var existingDrives []ExistingContainer
	for i := 1; i <= 3; i++ {
		n := "old" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, TlcGiB: 30 * tib, NumCores: 6})
	}
	// Existing nodes have ample headroom; 6 fresh nodes host the new even-split FDs.
	inv := append(nodes(3, 30*tib, 0, 64, "old"), nodes(6, 100*tib, 0, 64, "new")...)
	// current 90 TiB; raise to 360 TiB -> delta 270 TiB, covered by fresh FDs only.
	desired := DesiredCapacity{TlcRawGiB: 360 * tib}
	plan := planCap(desired, s, existingDrives, inv, testCons())
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if len(plan.Grow) != 0 {
		t.Fatalf("even-split covers the delta on fresh FDs; want 0 grows, got %d: %+v", len(plan.Grow), plan.Grow)
	}
	if len(plan.Create) != 6 { // preferred kBase=9 capped by 6 spare nodes -> k=6, CeilDiv(270,6)=45 TiB
		t.Fatalf("want 6 new even-split FDs (45 TiB each), got %d: %v", len(plan.Create), plan.Create)
	}
	for _, c := range plan.Create {
		if c.TlcGiB != 45*tib { // CeilDiv(delta=270 TiB, k=6) = 45 TiB
			t.Fatalf("want new FDs at CeilDiv(delta=270 TiB, k=6)=45 TiB, got %+v", c)
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
	if got := 90*tib + sumCreateTlc(plan); got != 360*tib { // current 90 + created 6×45=270 = 360 exactly
		t.Fatalf("want total raw 360 TiB placed, got %d (created %d)", got, sumCreateTlc(plan))
	}
}

// NEW behavior (uniform-or-infeasible): when other clusters have reduced 3 of the 6 nodes to 25 TiB
// headroom (the other 3 at 100 TiB), 180 TiB cannot tile into 6 uniform FDs — the 30 TiB per-FD share over 6
// FDs exceeds the small nodes' 25 TiB cap and no extra candidate exists to lower the share. The pool is
// ClusterCapacityInfeasible (was: a feasible uneven fill capping the small nodes at 25 TiB).
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
		name                          string
		tlcGiB, qlcGiB                int
		wantCores, wantHpMiB, wantMem int
	}{
		{"tlc exactly one core", 5 * tib, 0, 1, 1600, 11000},
		{"tlc rounds up", 5*tib + 1, 0, 2, 3200, 14000},
		{"qlc only one core", 0, 50 * tib, 1, 1600, 11000},
		{"mixed sums per-pool cores", 5 * tib, 50 * tib, 2, 3200, 14000},
		{"zero capacity floors at one core", 0, 0, 1, 1600, 11000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cores, hp, mem := RequiredDriveResources(tt.tlcGiB, tt.qlcGiB, cons)
			if cores != tt.wantCores || hp != tt.wantHpMiB || mem != tt.wantMem {
				t.Fatalf("RequiredDriveResources(%d,%d) = (cores %d, hp %d, mem %d), want (%d, %d, %d)",
					tt.tlcGiB, tt.qlcGiB, cores, hp, mem, tt.wantCores, tt.wantHpMiB, tt.wantMem)
			}
		})
	}
}

// Test_PlaceUniform_DoesNotDoubleChargeBaseMemoryForMergedContainer reproduces the cross-pool scenario
// where the TLC pass has already created a (merged) new container on a node and the QLC pass then lands on
// that same node via placeUniform. Per-container base memory must be charged at most once per merged
// container — placeUniform must mirror its includeBase decision (existedNode => base already charged)
// rather than unconditionally charging base again.
func Test_PlaceUniform_DoesNotDoubleChargeBaseMemoryForMergedContainer(t *testing.T) {
	s := testScheme() // minFd = 6
	cons := testCons()
	minFd := s.MinFdNum()

	// Six nodes, each on its own FD, with generous drive/core/hugepages so only memory is observable.
	// TlcGiB seeds a per-node TLC container the earlier pass would have placed.
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

	// Build one chosen fdGroup per node (AUTO mode: FD == node) and place T = 30 TiB of QLC uniformly. Each
	// node already carries a new (TLC) container, so existedNode is true and base must NOT be re-charged.
	T := 30 * tib
	chosen := make([]*fdGroup, 0, minFd)
	for i := 1; i <= minFd; i++ {
		ns := states["n"+itoa(i)]
		chosen = append(chosen, &fdGroup{nodes: []*nodeState{ns}, headroom: ns.nodeHeadroom(poolQLC, cons, true)})
	}
	placeUniform(poolQLC, T, chosen, nil /*no existing drives*/, states, cons, nil /*never grows*/, newByNode, newFor)

	// Each node should have been charged QLC core memory only — its base was already accounted for by
	// the (simulated) TLC pass. A second MemoryBaseMiB charge here is the double-charge bug.
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

// Test_Compute_BugScenario_NodeCoreAware reproduces the OP-329 bug: 200 TiB / SW,RL,HS=3,2,1 /
// TLC-only across 14 large nodes -> 84 TLC drive cores. The planner must derive a MINIMAL compute
// layout bounded by the per-node core cap (6 containers x 14 cores), NOT 84 single-core containers.
func Test_Compute_BugScenario_NodeCoreAware(t *testing.T) {
	s := testScheme()
	cons := testCons()
	cons.MaxComputeCoresPerNode = 16 // policy cap (default in prod)

	inv := nodes(14, 100*tib, 0, 32, "n") // big nodes: ~6 drive cores leave ample compute headroom
	plan := planCap(desiredFrom(200*tib, s, nil), s, nil, inv, cons)
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if plan.TotalTlcDriveCores != 84 {
		t.Fatalf("want 84 TLC drive cores, got %d", plan.TotalTlcDriveCores)
	}
	if plan.ComputeContainers != 6 || plan.ComputeCores != 14 {
		t.Fatalf("want compute 6x14 (minimal count, cap-bound), got %dx%d", plan.ComputeContainers, plan.ComputeCores)
	}
	if plan.ComputeContainers*plan.ComputeCores < plan.TotalTlcDriveCores {
		t.Fatalf("compute:drive 1:1 violated: %d < %d", plan.ComputeContainers*plan.ComputeCores, plan.TotalTlcDriveCores)
	}
}

// Test_Compute_Infeasible_NoCoreHeadroom asserts the planner reports infeasibility (so the caller
// retries before creating drive containers) when the drive-bearing nodes have no spare cores for the
// 1:1 compute, instead of emitting unschedulable single-core compute containers.
func Test_Compute_Infeasible_NoCoreHeadroom(t *testing.T) {
	s := testScheme()
	cons := testCons()

	inv := nodes(14, 100*tib, 0, 6, "n") // each node's 6 cores are fully consumed by its drive container
	plan := planCap(desiredFrom(200*tib, s, nil), s, nil, inv, cons)
	if !strings.Contains(plan.Infeasible, "compute") {
		t.Fatalf("want a compute infeasibility, got infeasible=%q (compute %dx%d)", plan.Infeasible, plan.ComputeContainers, plan.ComputeCores)
	}
}

// Test_Compute_HeterogeneousNodes_BoundBySmallest asserts homogeneous, unpinned compute is bounded by
// the SMALLEST compute node's core headroom (a tiny node forces more, smaller containers). Drives
// concentrate on the minFdNum largest nodes, so the small node carries no drives and its 8 cores cap
// every compute container.
func Test_Compute_HeterogeneousNodes_BoundBySmallest(t *testing.T) {
	s := testScheme()
	cons := testCons() // cap disabled -> real per-node headroom binds

	// 13 big nodes (32 cores) + 1 small node (8 cores), all 100 TiB TLC. Drives land on 6 big nodes
	// (minFdNum, minimize-count); the small node is left drive-free and its 8 cores bound compute.
	inv := append(nodes(13, 100*tib, 0, 32, "big"), node("small1", 100*tib, 0, 8))
	plan := planCap(desiredFrom(200*tib, s, nil), s, nil, inv, cons)
	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if plan.ComputeCores > 8 {
		t.Fatalf("compute cores should be bounded by the smallest node (8), got %d", plan.ComputeCores)
	}
	if plan.ComputeContainers*plan.ComputeCores < plan.TotalTlcDriveCores {
		t.Fatalf("compute:drive 1:1 violated: %d < %d", plan.ComputeContainers*plan.ComputeCores, plan.TotalTlcDriveCores)
	}
}

// Test_Compute_DisklessNodes_SizedOnComputePool asserts compute is sized over the compute-selector node
// pool — which can be DISKLESS nodes outside the drive inventory — and not over the drive nodes. The
// drive nodes here are fully core-consumed by their drive containers (0 spare compute cores), so sizing
// compute over them (the old behavior) would be infeasible; the diskless compute pool makes it feasible.
func Test_Compute_DisklessNodes_SizedOnComputePool(t *testing.T) {
	s := testScheme()
	cons := testCons()
	cons.MaxComputeCoresPerNode = 16

	// 6 drive nodes with exactly enough cores for their drive container (14 cores → 0 left for compute),
	// plus 8 diskless compute-only nodes (no drives, 32 cores each).
	inv := append(nodes(6, 100*tib, 0, 14, "d"), nodes(8, 0, 0, 32, "c")...)
	computeNodes := map[string]bool{}
	for i := 1; i <= 8; i++ {
		computeNodes["c"+itoa(i)] = true // only the diskless pool is compute-eligible
	}

	plan := PlanCapacity(desiredFrom(200*tib, s, nil), s, nil, nil, inv, computeNodes, cons)
	if plan.Infeasible != "" {
		t.Fatalf("compute should fit on the diskless pool, got infeasible: %s", plan.Infeasible)
	}
	if plan.TotalTlcDriveCores != 84 {
		t.Fatalf("want 84 TLC drive cores, got %d", plan.TotalTlcDriveCores)
	}
	if plan.ComputeContainers != 6 || plan.ComputeCores != 14 {
		t.Fatalf("want compute 6x14 over the diskless pool, got %dx%d", plan.ComputeContainers, plan.ComputeCores)
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

// Test_Compute_SubsetSmallerThanRequired_Infeasible asserts the compute count is capped by the number of
// COMPUTE nodes (not drive nodes): when the compute selector matches fewer nodes than the 1:1 layout
// needs, the plan is infeasible and names the compute-node shortfall.
func Test_Compute_SubsetSmallerThanRequired_Infeasible(t *testing.T) {
	s := testScheme()
	cons := testCons()
	cons.MaxComputeCoresPerNode = 16 // needs ceil(84/16)=6 compute containers

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

// Test_Compute_NilComputeNodes_Infeasible guards the bug case: a nil compute-node set must surface
// loudly rather than silently sizing compute over an unintended node set.
func Test_Compute_NilComputeNodes_Infeasible(t *testing.T) {
	s := testScheme()
	plan := PlanCapacity(desiredFrom(200*tib, s, nil), s, nil, nil, nodes(14, 100*tib, 0, 32, "n"), nil, testCons())
	if !strings.Contains(plan.Infeasible, "compute node set not provided") {
		t.Fatalf("want internal nil-computeNodes infeasibility, got %q", plan.Infeasible)
	}
}

// tightNode builds a candidate node with a SPECIFIC hugepages budget (and generous memory) so the
// hugepages dimension binds — used to reproduce the drive+compute co-location hugepages oversubscription.
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

// Test_RequiredDriveResources_IncludesDpdk verifies the planner's drive hugepages reservation includes
// the per-core DPDK base memory that GetContainerHugepages adds to the actual pod request. Without this
// the planner under-reserves and co-locates pools the scheduler then rejects (Bug #1, cause 1).
func Test_RequiredDriveResources_IncludesDpdk(t *testing.T) {
	cons := testCons()
	cons.DriveDpdkPerCoreMiB = 64 // GetDpdkBaseMemoryMbByRole default
	// 5 TiB + 1 -> 2 cores; hugepages = 2 * (1600 + 64) = 3328.
	cores, hp, _ := RequiredDriveResources(5*tib+1, 0, cons)
	if cores != 2 || hp != 2*(1600+64) {
		t.Fatalf("got (cores %d, hp %d), want (2, %d)", cores, hp, 2*(1600+64))
	}
}

// Test_ComputeHugepages_IncludesDpdk verifies the planner's compute hugepages estimate adds the per-core
// DPDK base memory on top of the base formula, matching the actual compute pod request.
func Test_ComputeHugepages_IncludesDpdk(t *testing.T) {
	base := testCons()
	withDpdk := testCons()
	withDpdk.ComputeDpdkPerCoreMiB = 64
	raw := 180 * tib
	got := computeContainerHugepagesMiB(raw, 0, 6, 4, withDpdk)
	want := computeContainerHugepagesMiB(raw, 0, 6, 4, base) + 64*4
	if got != want {
		t.Fatalf("compute hugepages with DPDK = %d, want base+64*cores = %d", got, want)
	}
}

// Test_Compute_PinnedDisjointFromDrives_WhenHugepagesTight reproduces Bug #1 (cause 2): on nodes whose
// hugepages fit a drive OR a compute container but not both, the planner must reserve compute on nodes
// disjoint from the drive nodes (and expose them via ComputeNodes) so compute never lands on a
// drive-pinned node and leaves the pinned drive pod unschedulable.
func Test_Compute_PinnedDisjointFromDrives_WhenHugepagesTight(t *testing.T) {
	s := testScheme() // minFd = 6
	cons := testCons()
	cons.DriveDpdkPerCoreMiB = 64
	cons.ComputeDpdkPerCoreMiB = 64

	// 12 nodes, each 20000 MiB hugepages: a 6-core drive reserves 6*(1600+64)=9984 (fits), but a
	// 6-core compute needs 3000*6 + 64*6 = 18384 — so a drive node's post-drive remainder (10016)
	// cannot also host compute. Disjoint placement IS possible (6 drive + 6 compute <= 12 nodes).
	inv := make([]NodeCapacity, 0, 12)
	for i := 1; i <= 12; i++ {
		inv = append(inv, tightNode("n"+itoa(i), 100*tib, 64, 20000))
	}

	plan := planCap(desiredFrom(90*tib, s, ratio(1, 0)), s, nil, inv, cons) // rawTLC 180 -> 6x30TiB drives
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

// --- Grow-path fixes (OP-329): heterogeneous-ceiling, remainder consolidation, compute footprint ---

// Test_GrowA1_HeterogeneousCeiling_EvenGrow is the primary repro for Cause A: existing TLC drive FDs
// near per-node ceilings, grow substantially. Before the fix the projected FD count was wrong (old
// formula used len(existing) directly), which caused delta to be concentrated into a handful of FDs
// and left fragment remainder containers. After the fix the planner raises projected until spill fits
// and distributes evenly, yielding:
//   - Infeasible == "" (plan is valid)
//   - no failure-domain imbalance warning (all FDs converge within 10%)
//   - final per-FD sizes within ~10% of each other
//   - a small new-FD count (no MinChunk-floor fragment cluster)
func Test_GrowA1_HeterogeneousCeiling_EvenGrow(t *testing.T) {
	s := testScheme() // minFdNum = 6
	cons := testCons()

	// 6 existing containers at 25 TiB each on 6 nodes that have 15 TiB of additional headroom.
	// Ceiling per node: 25 + 15 = 40 TiB. Current: 6*25 = 150 TiB raw.
	// Target: 240 TiB raw -> delta 90 TiB. Each node can absorb 15 TiB, total in-place room = 90 TiB.
	// A correct planner grows each existing FD to 40 TiB and creates no new containers.
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
	// Collect final per-FD sizes.
	perFD := map[string]int{}
	for _, g := range plan.Grow {
		// find the existing container to get FDValue
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

// NEW behavior: the uniform-FD rule forbids creating any failure domain smaller than T0 (the existing
// per-FD chunk). When the existing FDs are drive-full (cannot grow) and no spare node can host a full-T0
// FD, the increase is INFEASIBLE — the planner never opens a sub-T0 fragment FD (was: spread the
// remainder across the fewest sub-T fresh FDs and fold a sub-MinChunk tail).
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

// Test_GrowA3_Homogeneous_ProjectedEqualsExisting guards the degenerate-to-old-formula property:
// when all FD ceilings are ample (homogeneous) the projected FD count equals len(existing) and the
// result is identical to the old formula.
func Test_GrowA3_Homogeneous_Unchanged(t *testing.T) {
	s := testScheme() // minFdNum = 6
	cons := testCons()

	// 10 existing TLC FDs of 20 TiB each. Nodes have 30 TiB headroom (ample).
	// Current: 200 TiB. Target: 300 TiB -> delta 100 TiB -> +10 TiB per FD -> 30 TiB each.
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

// NEW behavior: uniform grow with no spare nodes. The uniform-FD rule replaces the old asymmetric
// "overflow onto other existing FDs" with a single common level L: with NO fresh FD available, every
// existing FD is grown to the SAME L (each must be able to reach it). Here T0=10 TiB and all 6 nodes have
// ample headroom, so delta=60 TiB raises every FD uniformly to 20 TiB — no FD is left below the others
// and no FD is pushed above to compensate (was: n6 pinned at its 12 TiB ceiling while others overshoot).
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
	// Every FD converges to the same uniform level (20 TiB) — no asymmetric overflow.
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

// NEW behavior: uniform grow + create-new AT the uniform level. The grow must ADD failure domains and
// the no-grow attempt cannot cover the delta with whole-T0 FDs (too few fresh nodes), so the planner
// raises the uniform level L (above the 20% threshold) and brings EVERY final FD to L — existing FDs grow
// to L and the new FDs are created AT L (not T0). 6 existing 30 TiB FDs (ceiling 42 TiB) + only 3 fresh
// nodes; desired 378 TiB resolves to N=9 FDs at the uniform 42 TiB (a 40% grow, above minGrowthFraction).
func Test_Grow_AddsFds_LevelsExistingAndNew(t *testing.T) {
	s := testScheme() // minFdNum = 6
	cons := testCons()
	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "ex" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{Name: "c" + itoa(i), Node: n, FDValue: n, TlcGiB: 30 * tib, NumCores: 6})
	}
	// Existing nodes: 12 TiB headroom each (ceiling 42 TiB). Only 3 fresh nodes, so the no-grow attempt
	// cannot cover the delta and the uniform level must rise to 42 TiB across 9 FDs.
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

// NEW behavior (uniform-or-infeasible): the old heterogeneous "fresh balanced set, delete the small ones"
// fallback is gone. Here 6 tiny existing FDs (5 TiB, drive-full nodes) + 4 big fresh (100 TiB) + 2 fresh
// capped at 60 TiB must reach 372 TiB. A uniform fill over 6 fresh FDs needs ceil(372/6)=62 TiB per FD, but
// the 2 capped nodes hold only 60 TiB and there is no 7th fresh node to lower the share, and the existing
// FDs are drive-full so they cannot grow. No uniform tiling reaches 372 TiB -> ClusterCapacityInfeasible
// (was: a 6-FD heterogeneous fresh set totaling exactly 372 TiB + a delete-old warning).
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

// Test_GrowB1_ComputeSaturatedNode_NewDriveAvoidsIt verifies that when an existing compute container
// pins resources on a node, new drive FDs are steered away from that node (Cause B). The compute
// footprint is charged against states before drive placement, so the compute-saturated node has zero
// nodeHeadroom for drives and is naturally skipped.
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

	// "sat1" is a node with plentiful TLC drive space (100 TiB) but ALL its cores and hugepages are
	// consumed by an existing compute container. After charging compute, nodeHeadroom for sat1 = 0.
	// Cores: sat1 has exactly 30 cores and the compute container uses them all.
	// Hugepages: sat1 has exactly 30*1600 MiB and the compute container uses them all.
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

	// Inventory:
	// - old1-old6: drive-full (0 TiB headroom), ample cores/hugepages for compute only
	// - sat1: 100 TiB drive headroom but zero usable after compute charge (exact saturation)
	// - fresh1-fresh6: ample drive capacity, ample cores (these receive new drive FDs)
	// Use PlanCapacity directly with a compute node set that only marks fresh nodes as compute-eligible
	// (so the compute sizing doesn't interfere with the drive placement assertion).
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

	// Compute nodes: only the fresh nodes (which have ample headroom after drives) plus old nodes
	// (drive-full but cores free). Exclude sat1 from compute eligibility so compute sizing succeeds.
	computeNodes := make(map[string]bool, len(inv))
	for _, nc := range invOld {
		computeNodes[nc.NodeName] = true // old nodes: cores free after drive (drive-full, not core-full)
	}
	for _, nc := range invFresh {
		computeNodes[nc.NodeName] = true
	}
	// sat1 is NOT compute-eligible in this plan so compute sizing doesn't fail.

	// Grow: add 60 TiB. Old nodes are drive-full; new FDs must go to sat1 or fresh1-fresh6.
	// After charging sat1's compute footprint, sat1 has 0 drive headroom → must skip it.
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

// Test_GrowB2_DriveSharesNodeWithCompute_StillFeasible verifies that when drives and compute share
// nodes that DO have spare headroom after accounting for the existing compute footprint, the grow
// is feasible and drives are placed correctly.
func Test_GrowB2_DriveSharesNodeWithCompute_Feasible(t *testing.T) {
	s := testScheme() // minFdNum = 6
	cons := testCons()

	// 6 drive containers at 10 TiB on nodes that have 40 TiB more headroom.
	// Each node has 64 cores; drive takes 2 cores (ceil(10*1024/5*1024)=2); compute takes 4 cores.
	// Remaining cores after both: 64 - 2 - 4 = 58 — plenty for new drive growth.
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
	// Each node: 40 TiB free TLC headroom, 64 cores total, ample hugepages/memory.
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

// Test_GrowC1_Infeasible_PinnedComputeCannotGrow reproduces the deficit-cannot-be-covered case (fix #2):
// an existing SCHEDULED compute on "cmp1" cannot grow to the target (tight hugepages), so the planner
// FREEZES it at its current size and tries to cover the resulting core deficit with a compensating
// container. Here every other compute node is consumed by the net-new uniform computes, so there is NO
// free fitting node left to compensate — the plan is cleanly Infeasible (the deficit message), and no
// compute sizing is emitted (pre-mutation).
func Test_GrowC1_Infeasible_PinnedComputeCannotGrow(t *testing.T) {
	s := testScheme() // minFdNum = 6
	cons := testCons()
	cons.MaxComputeCoresPerNode = 0 // disable policy cap so only real headroom binds

	// Drives: 6 containers on nodes drv1-drv6, each with 25 TiB TLC.
	// After grow to 50 TiB: totalTlcDriveCores = 6 * ceil(50*1024/5120) = 6*10 = 60.
	// Compute layout: 6 compute-eligible nodes → ceil(60/6) = 10 cores each.
	// Derived perContainerHP ≈ max(1700*10, 3000*10) = 30000 MiB per compute container.
	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "drv" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{
			Name: "drive" + itoa(i), Node: n, FDValue: n,
			TlcGiB: 25 * tib, NumCores: 5,
		})
	}

	// cmp1 hosts an existing scheduled compute container with 4 cores and 4*1600=6400 MiB hugepages.
	// cmp1's node budget: 6400 MiB (exactly consumed by the existing compute) + a tiny spare that is
	// insufficient for the 30000 - 6400 = 23600 MiB hugepage delta the grown compute requires.
	currentHp := 4 * cons.HugepagesPerCoreMiB // 6400 MiB
	// Give cmp1 only 6400 + 1 MiB in total so hpDelta=23600 cannot fit (cmp1 spare = 1 MiB after charge).
	cmp1TotalHp := currentHp + 1
	existingCompute := []ExistingComputeContainer{
		{
			Name:         "compute01",
			Node:         "cmp1",
			NumCores:     4,
			HugepagesMiB: currentHp,
			// Unscheduled: false (default) — this is a scheduled/running container
		},
	}

	// Inventory: 6 drive nodes with ample hugepages + 6 compute-only nodes.
	// 5 of the 6 compute nodes have ample hugepages. cmp1 has the tight budget.
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
	// cmp1: zero drive capacity (diskless compute node), tight hugepages.
	inv = append(inv, NodeCapacity{
		NodeName:              "cmp1",
		FDValue:               "cmp1",
		TlcGiB:                0,
		AllocatableCPU:        64,
		AvailableHugepagesMiB: cmp1TotalHp,
		AvailableMemoryMiB:    1 << 28,
	})
	// cmp2-cmp5: ample hugepages for 5 more compute containers.
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

	// Compute-eligible: only cmp1-cmp6 (diskless compute pool). Drive nodes are NOT compute-eligible
	// so compute sizing is purely over the 6 cmp nodes.
	computeNodes := make(map[string]bool)
	for i := 1; i <= 6; i++ {
		computeNodes["cmp"+itoa(i)] = true
	}

	// Grow from 6*25 TiB to 6*50 TiB. Derived compute target: 10 cores, 30000 MiB per container.
	// cmp1 has only 1 MiB spare hugepages after existing charge; hpDelta=23600 doesn't fit → Cause C.
	inv = netCompute(inv, existingCompute, cons)
	plan := PlanCapacity(
		DesiredCapacity{TlcRawGiB: 6 * 50 * tib},
		s, existingDrives, existingCompute, inv, computeNodes, cons,
	)
	if plan.Infeasible == "" {
		t.Fatalf("C1: want infeasible when the frozen-compute deficit cannot be covered, got feasible plan (compute %dx%d)", plan.ComputeContainers, plan.ComputeCores)
	}
	if !strings.Contains(plan.Infeasible, "cannot place") || !strings.Contains(plan.Infeasible, "shortfall") {
		t.Fatalf("C1: infeasible message should report the uncoverable shortfall, got: %q", plan.Infeasible)
	}
	// ComputeCores/ComputeContainers/ComputeLayout must NOT be set on an infeasible plan (pre-mutation).
	if plan.ComputeCores != 0 || plan.ComputeContainers != 0 || len(plan.ComputeLayout) != 0 {
		t.Fatalf("C1: infeasible plan must not emit compute sizing, got %dx%d layout=%d", plan.ComputeContainers, plan.ComputeCores, len(plan.ComputeLayout))
	}
}

// Test_GrowC2_Feasible_PinnedComputeCanGrow verifies the complement of C1: the pinned compute node
// (cmp1) has ample hugepages, so the compute growth delta fits, plan.Infeasible == "", and
// ComputeCores reflects the grown target. This also guards against double-reservation: cmp1's current
// footprint is charged once (Step 1b), the delta is verified once (Cause C), and reserve is not
// double-claimed in the fitNodes pass.
func Test_GrowC2_Feasible_PinnedComputeCanGrow(t *testing.T) {
	s := testScheme() // minFdNum = 6
	cons := testCons()
	cons.MaxComputeCoresPerNode = 0 // disable policy cap

	// 6 TLC drive containers at 25 TiB each. Post-grow to 50 TiB → totalTlcDriveCores = 60.
	// Compute over 6 cmp nodes: ceil(60/6) = 10 cores each; perContainerHP ≈ 30000 MiB.
	// cmp1 has an existing compute container with 4 cores / 6400 MiB; ample budget to absorb
	// 6 more cores (delta 9600 MiB hugepages) out of its large (1<<28) allowance.
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

	// Drive nodes: 25 TiB free headroom each, ample hugepages.
	// Compute nodes (cmp1-cmp6): diskless (0 TiB), ample hugepages (1<<28 MiB).
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

// Test_GrowD1_ComputeCoreBump_RetainsExistingNodes_NoNetNew is the primary repro for the
// compute-core-grow double-count bug: every compute container is pinned to a dedicated compute node
// whose post-charge headroom comfortably fits its OWN growth delta but NOT a fresh full-footprint
// placement. The buggy planner reserved each delta on its node and then re-required the full footprint
// free on `count` nodes — the same nodes it had just delta-decremented failed the re-check, so only the
// one genuinely-free node passed and the grow was falsely rejected ("only 1 of 6 fit"). The fix places
// only the net-new computes (here 0): the 6 retained nodes are kept, no fresh node is consumed.
func Test_GrowD1_ComputeCoreBump_RetainsExistingNodes_NoNetNew(t *testing.T) {
	s := testScheme() // minFdNum = 6
	cons := testCons()
	cons.MaxComputeCoresPerNode = 0 // disable policy cap; real per-node headroom binds

	// 6 drive containers of 60 TiB each → 12 drive cores each, 72 total. Compute over 6 cmp nodes:
	// count = max(floor 6, ceil(72/64)) = 6, cores = ceil(72/6) = 12, perContainerHP = 3000*12 = 36000.
	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "drv" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{
			Name: "drive" + itoa(i), Node: n, FDValue: n,
			TlcGiB: 60 * tib, NumCores: 12,
		})
	}

	// Each cmp node hosts a 6-core / 18000-MiB existing compute and has a total hugepages budget of
	// 36001 MiB: after charging the current 18000 it has 18001 free — enough for the 18000-MiB growth
	// delta (→ 12 cores / 36000 MiB) but NOT for a fresh full 36000-MiB placement. The buggy planner
	// would reject these nodes on the full re-check; the fix retains them (net-new = 0).
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

// Test_GrowD2_ComputeCoreBump_StuckCompute_CompensatedOnFreeNode is fix #2 scenario 1: one existing
// compute (cmp1) cannot grow its delta on its node, so the planner FREEZES it at its current 6 cores
// (no disruption) and covers the 6-core deficit with a compensating container on the free fitting node
// (cmpfree). The grow is FEASIBLE; cmp2-6 grow to the 12-core target; cmp1 stays at 6; cmpfree holds a
// 6-core compensating container; total layout cores >= the uniform target total.
func Test_GrowD2_ComputeCoreBump_StuckCompute_CompensatedOnFreeNode(t *testing.T) {
	s := testScheme()
	cons := testCons()
	cons.MaxComputeCoresPerNode = 0

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
		// cmp1 is the offender: total 18100 MiB → only 100 free after the current 18000 charge, far below
		// the 18000-MiB delta. cmp2-6 are ample.
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

// Test_GrowD3_ComputeCountGrows_PlacesNetNewOnly verifies a count increase (6 existing computes → 8
// containers) places exactly the 2 net-new computes on free nodes while retaining the 6 existing pinned
// nodes: ComputeNodes is the union of the 6 existing + 2 new.
func Test_GrowD3_ComputeCountGrows_PlacesNetNewOnly(t *testing.T) {
	s := testScheme()
	cons := testCons()
	cons.MaxComputeCoresPerNode = 10 // count = max(6, ceil(72/10)) = 8, cores = ceil(72/8) = 9

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

// Test_GrowD4_DeficitSpreadAcrossMultipleCompensatingContainers is fix #2 scenario 3: THREE existing
// computes are stuck (frozen at 6 cores) on cmp1-3, contributing a combined 18-core deficit (target 12).
// That needs 2 compensating containers (ceil(18/12)); the deficit is split AS EVENLY AS POSSIBLE (9 + 9)
// across the two free fitting nodes. cmp4-6 grow to 12; cmp1-3 stay frozen at 6.
func Test_GrowD4_DeficitSpreadAcrossMultipleCompensatingContainers(t *testing.T) {
	s := testScheme()
	cons := testCons()
	cons.MaxComputeCoresPerNode = 0 // uniform target 12 cores (72 drive cores / 6)

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

// Test_GrowD5_FrozenPlusNetNew_BalancedFill pins the balanced-fill behavior: when a frozen existing
// compute COEXISTS with genuinely net-new slots, the new containers are sized UNIFORMLY (the whole
// shortfall split evenly), not "full ones at the uniform target plus a small remainder". One existing
// compute (cmp1) is frozen at 6 cores; the uniform target is 6 containers × 12 = 72 cores, of which the
// frozen compute supplies only 6 → a 66-core shortfall placed across 6 free nodes as 6 × 11 (not
// [12,12,12,12,12,6]). Same node count and total cores as the older full-then-remainder layout, just
// more even.
func Test_GrowD5_FrozenPlusNetNew_BalancedFill(t *testing.T) {
	s := testScheme()
	cons := testCons()
	cons.MaxComputeCoresPerNode = 0 // uniform target 12 cores (72 drive cores / 6)

	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "drv" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{
			Name: "drive" + itoa(i), Node: n, FDValue: n,
			TlcGiB: 60 * tib, NumCores: 12,
		})
	}
	// A SINGLE existing compute (cmp1), frozen: its node has only 100 MiB spare after the 18000 charge,
	// far below the 18000-MiB grow delta. The other 5 uniform slots and the frozen deficit are all net-new.
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

// Test_Grow_DynamicScalingDisabled_CreatesNewInsteadOfExtending asserts that with in-place growth
// disabled the planner does NOT grow any existing drive container (no CapacityGrowthApplied) — it places
// the whole delta as NEW containers on fresh failure domains, sized evenly to the common per-FD target.
// The enabled sub-run is the contrast: identical inputs grow in place.
func Test_Grow_DynamicScalingDisabled_CreatesNewInsteadOfExtending(t *testing.T) {
	s := testScheme() // minFdNum = 6
	// 6 existing TLC containers at 30 TiB on n1..n6 (each with 70 TiB / 58 cores headroom for growth),
	// plus 6 fresh failure domains n7..n12 available for new containers.
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
		if len(plan.Create) != 6 {
			t.Fatalf("want the 180 TiB delta placed as 6 new containers on fresh FDs, got %d", len(plan.Create))
		}
		for _, c := range plan.Create {
			if existingNodes[c.Node] {
				t.Fatalf("new container must land on a FRESH failure domain, got node %s", c.Node)
			}
			if c.TlcGiB != 30*tib { // 180 TiB delta / 6 fresh FDs, even with the frozen existing FDs
				t.Fatalf("want each new container TLC=30 TiB (even per-FD target), got %+v", c)
			}
		}
		if got := sumCreateTlc(plan); got != 180*tib {
			t.Fatalf("want total created TLC 180 TiB, got %d GiB", got)
		}
	})

	// NEW behavior: create-new-before-grow. With scaling enabled AND spare fresh FDs, a clean delta=180 TiB
	// is covered by 6 new full-T0 (30 TiB) FDs on the fresh nodes — existing specs are left untouched
	// (was: grow the 6 existing FDs to 60 TiB in place).
	t.Run("enabled_creates_new_at_T", func(t *testing.T) {
		cons := testCons() // AllowInPlaceGrowth = true
		plan := planCap(desired, s, existingDrives, inv, cons)
		if plan.Infeasible != "" {
			t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
		}
		if len(plan.Grow) != 0 {
			t.Fatalf("create-new-before-grow: existing specs must be untouched, got %d grows", len(plan.Grow))
		}
		if len(plan.Create) != 6 {
			t.Fatalf("want the 180 TiB delta as 6 new full-T0 (30 TiB) FDs, got %d", len(plan.Create))
		}
		for _, c := range plan.Create {
			if existingNodes[c.Node] {
				t.Fatalf("new container must land on a FRESH failure domain, got node %s", c.Node)
			}
			if c.TlcGiB != 30*tib { // uniform T0 chunk replicated
				t.Fatalf("want each new container TLC=30 TiB (uniform T0), got %+v", c)
			}
		}
		if len(plan.OverProvisions) != 0 { // exact multiple of T0, no overshoot
			t.Fatalf("exact multiple of T0 should not over-provision, got %v", plan.OverProvisions)
		}
	})
}

// Test_Grow_DynamicScalingDisabled_NoFreshFD_Infeasible asserts that when in-place growth is disabled
// and there are NO fresh failure domains to host new containers, the planner reports the plan infeasible
// (with a flag-aware message) and extends nothing — rather than silently growing existing containers.
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

// Test_Compute_DynamicScalingDisabled_FreezesExistingCreatesNew asserts that with in-place growth
// disabled every existing compute container is FROZEN at its current size (no CapacityGrowthApplied) and
// the 1:1 core deficit is covered by NEW compute containers on fresh nodes. The enabled sub-run is the
// contrast: the existing computes grow in place to the uniform target.
func Test_Compute_DynamicScalingDisabled_FreezesExistingCreatesNew(t *testing.T) {
	s := testScheme() // minFdNum = 6
	// Drives already at target (no drive growth): 6 x 30 TiB TLC -> 6 cores each -> 36 TLC drive cores,
	// so the compute 1:1 target is 36 cores (uniform 6 cores across 6 FDs).
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

// Test_UniformIncrease_PrefersNewFds_OverGrow: an existing pool with spare nodes and a small bump covers
// the delta with a single new FD sized to the SHORTFALL (not a full-T0 clone) and leaves every existing
// container's spec untouched (no grow). Sizing the new FD to the delta reaches desired EXACTLY, so there is
// no rounding over-provision (was: 1 new full-T0=30 TiB FD, over-provisioning by 10 TiB).
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
	// current 180 TiB; target 200 TiB -> delta 20 TiB. maxPerFdCap = 200/6 = 33 TiB, so k=1 -> one 20 TiB
	// FD fits (20 <= 33, >= MinChunk, below the imbalance boundary 2×30 TiB), reaching desired exactly.
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

// Test_UniformIncrease_NoSpare_BelowThreshold_Infeasible: with no spare node to host a new T0 FD and the
// only alternative being a sub-minGrowthFraction in-place grow, the plan is infeasible with the threshold
// message and places nothing.
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

// Test_UniformIncrease_NoSpare_MinGrowthFractionZero_GrowsUniformly mirrors
// Test_UniformIncrease_NoSpare_BelowThreshold_Infeasible but with MinGrowthFraction=0 ("always allow
// in-place grow"). The same ~3% grow that is infeasible at the 0.2 default must now succeed — proving 0
// is a meaningful value, not silently coerced to the default.
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

// Test_CapacityCoverTarget verifies the CapacityCoverTarget helper directly:
//   - fraction 0 (unset) → returns desired unchanged (strict mode).
//   - desired=6395, fraction=0.05 → band=ceil(319.75)=320 → 6075.
//   - desired=100, fraction=0.011 → band=ceil(1.1)=2 → 98.
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

// Test_UniformIncrease_EvenSplitToDelta_SizesNewFdsToShortfall validates the no-grow Step 4 even-split:
// replacement/increase FDs are sized to SUM to the missing capacity (delta) using the FEWEST new FDs whose
// even share stays within maxPerFdCap (= desiredRaw/minFd), NOT by cloning the smallest existing FD (T0) and
// rounding the count up.
//
// Scenario arithmetic (all values in GiB) — worked example (a):
//
//	scheme: stripeWidth=3 / redundancy=2 / hotSpare=0 → minFd=5
//	existing TLC pool: 3 FDs × 1250 GiB = 3750 GiB current
//	desired TLC raw: 6395 GiB → delta = 6395 - 3750 = 2645
//	maxPerFdCap = desiredRaw/minFd = 6395/5 = 1279  (no single FD may exceed this)
//	Choose fewest new FDs k with even share CeilDiv(delta,k) <= maxPerFdCap:
//	  k=1 → 2645 (>1279, no); k=2 → 1323 (>1279, no); k=3 → 882 (<=1279, yes)
//	→ 3 new FDs of 882, total = 3750 + 3×882 = 6396 ≈ desired (no over-provision beyond +1).
//
//	The old T0-clone behavior would instead have created ceil(2645/1250)=3 FDs of 1250 (total 7500),
//	over-provisioning by 1105 GiB. The new FDs (882) are SMALLER than the frozen existing FDs (1250) —
//	a heterogeneous but valid layout (largest FD 1250 <= 1279). AllowInPlaceGrowth=false freezes the
//	existing FDs so the only capacity added is the fresh even-split set.
func Test_UniformIncrease_EvenSplitToDelta_SizesNewFdsToShortfall(t *testing.T) {
	s := ProtectionScheme{StripeWidth: 3, RedundancyLevel: 2, HotSpare: 0} // minFd = 5
	cons := testCons()
	cons.CapacityDeadbandFraction = 0.05 // present but does NOT change Step 4 (which targets exact delta)
	cons.AllowInPlaceGrowth = false      // freeze existing FDs; all new capacity is fresh even-split FDs

	// 3 existing FDs of 1250 GiB each → current = 3750, T0 = 1250.
	existingDrives := []ExistingContainer{
		{Name: "c1", Node: "n1", FDValue: "n1", TlcGiB: 1250, NumCores: 1},
		{Name: "c2", Node: "n2", FDValue: "n2", TlcGiB: 1250, NumCores: 1},
		{Name: "c3", Node: "n3", FDValue: "n3", TlcGiB: 1250, NumCores: 1},
	}

	// Inventory: existing nodes (modest headroom — growth is frozen anyway) + 4 spare nodes
	// with 5000 GiB TLC each so 3 of them can host an 882 GiB FD.
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
	// KEY ASSERTION: even-split to delta → 3 new FDs (fewest k with even share <= maxPerFdCap), NOT the
	// old T0-clone count ceil(2645/1250)=3 of 1250, and NOT 2.
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

// Test_UniformIncrease_EvenSplit_SubT0_SingleSmallFd validates worked example (b): when the shortfall is
// smaller than a single existing FD, Step 4 covers it with ONE new FD sized to the shortfall itself, not a
// full T0-sized clone (no sub-T0 quantization overshoot).
//
// Scenario arithmetic (all values in GiB):
//
//	scheme: stripeWidth=3 / redundancy=2 / hotSpare=0 → minFd=5
//	existing TLC pool: 5 FDs × 1179 GiB = 5895 GiB current (T0 = 1179)
//	desired TLC raw: 6395 GiB → delta = 500
//	maxPerFdCap = 6395/5 = 1279
//	k=1 → CeilDiv(500,1)=500 (>= MinChunkSizeGiB=384, <= 1279) → one 500-GiB FD, total 6395 exact.
//	final FD count = 5 existing + 1 new = 6 >= minFd, feasible.
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
	// KEY ASSERTION: one new FD of 500 (the shortfall), NOT a 1179 T0 clone.
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

// Test_CapacityConstraintsFromConfig_ZeroFractionsNotCoerced asserts the fix for the PR #2604 review:
// an explicit 0 for MinGrowthFraction / MaxOverProvisionFraction is honored, not coerced back to 0.2.
func Test_CapacityConstraintsFromConfig_ZeroFractionsNotCoerced(t *testing.T) {
	prevMin := globalconfig.Config.DriveSharing.MinGrowthFraction
	prevMax := globalconfig.Config.DriveSharing.MaxOverProvisionFraction
	t.Cleanup(func() {
		globalconfig.Config.DriveSharing.MinGrowthFraction = prevMin
		globalconfig.Config.DriveSharing.MaxOverProvisionFraction = prevMax
	})

	globalconfig.Config.DriveSharing.MinGrowthFraction = 0
	globalconfig.Config.DriveSharing.MaxOverProvisionFraction = 0
	cons := CapacityConstraintsFromConfig()
	if cons.MinGrowthFraction != 0 {
		t.Errorf("MinGrowthFraction=0 must be honored, got %v", cons.MinGrowthFraction)
	}
	if cons.MaxOverProvisionFraction != 0 {
		t.Errorf("MaxOverProvisionFraction=0 must be honored, got %v", cons.MaxOverProvisionFraction)
	}

	globalconfig.Config.DriveSharing.MinGrowthFraction = 0.35
	globalconfig.Config.DriveSharing.MaxOverProvisionFraction = 0.1
	cons = CapacityConstraintsFromConfig()
	if cons.MinGrowthFraction != 0.35 || cons.MaxOverProvisionFraction != 0.1 {
		t.Errorf("explicit non-zero values must pass through, got min=%v max=%v", cons.MinGrowthFraction, cons.MaxOverProvisionFraction)
	}
}

// Test_UniformIncrease_NoSpare_AboveThreshold_GrowsUniformly: with no spare node and an in-place grow that
// clears minGrowthFraction, every existing FD is grown to one common uniform level (no sub-T fragment, no
// new FD).
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

// Test_UniformIncrease_OversizedAnchor_DoesNotRaiseFloor: an over-sized existing FD (anchor) must not
// raise T0 above the smallest existing FD; a new FD is created at T0 (the smallest existing chunk) and the
// anchor is left untouched.
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

// Test_GrowRestore_PrefersCleanNodeOverDeletingNode: deleting a drive container excludes it from the
// existing set, so its node re-enters the fresh-candidate pool while still being charged in the
// inventory. Once its capacity frees it is the emptiest (highest-headroom) node and, by pure
// headroom-desc, would win — recreating the replacement on the node it was just deleted from. The
// HasDeletingDriveContainer deprioritization must instead land the restored FD on a genuinely free node.
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
	ndel.HasDeletingDriveContainer = true // still hosts the just-deleted container; MOST headroom
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

// Test_GrowRestore_FallsBackToDeletingNodeWhenSoleCandidate: the deprioritization is last-resort, never
// an exclusion. When the only fresh candidate that can host the uniform chunk hosts a deleting container
// (the other free node is too small), the planner must still restore the FD there rather than go
// infeasible — this
// guards the scarce-drive case (e.g. every QLC-capable node already used).
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
	ndel.HasDeletingDriveContainer = true        // only candidate that can host the 30 TiB chunk
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

// Test_FrozenFDs_ImbalanceGuardBlocks: same setup as Test_FrozenFDs_NewFDSizedFromMaxCap_NotT0 but with
// ImbalanceFactor=1.1 so that 4436 >= 3750*1.1=4125 triggers the imbalance guard for all k values,
// preventing the new-FD path from completing and leaving the plan infeasible.
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
