package capacityplanner

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/weka/weka-operator/pkg/util"
)

// rep returns a slice of n nodes each with headroom h (homogeneous node headroom).
func rep(n, h int) []int {
	s := make([]int, n)
	for i := range s {
		s[i] = h
	}
	return s
}

func TestDeriveComputeLayout(t *testing.T) {
	const cap = 19 // MaxCoresPerContainer

	cases := []struct {
		name                                  string
		specCount, specCores, totalTlc, floor int
		maxPerNode                            int
		nodeHeadroom                          []int
		// nodeHugepagesMiB/hugepagesFor: zero value (nil, nil) keeps behavior byte-identical to
		// pre-hugepages cases; only the hugepages-aware cases below set them.
		nodeHugepagesMiB     []int
		hugepagesFor         func(count, cores int) int
		wantCount, wantCores int
		wantInfeasibleSub    string // substring in infeasible ("" => feasible)
		wantBinding          string // expected binding when wantInfeasibleSub != ""; "" => not checked
		wantWarnSub          string // substring in a warning ("" => no warning)
	}{
		{
			// OP-329 bug scenario: 200TiB, SW/RL/HS=3/2/1, 14 TLC nodes -> totalTlc=84; large nodes
			// let the per-container cap bind to a small count instead of 84 single-core containers.
			name:     "both unset: bug scenario, cap binds -> minimal count",
			totalTlc: 84, floor: 5, maxPerNode: cap, nodeHeadroom: rep(14, 26),
			wantCount: 5, wantCores: 17,
		},
		{
			name:     "both unset: floor dominates",
			totalTlc: 3, floor: 5, maxPerNode: cap, nodeHeadroom: rep(6, 16),
			wantCount: 5, wantCores: 1,
		},
		{
			name:     "both unset: small per-node headroom -> more, smaller containers",
			totalTlc: 84, floor: 5, maxPerNode: cap, nodeHeadroom: rep(14, 8),
			wantCount: 11, wantCores: 8, // perContainerCap=8, ceil(84/8)=11, ceil(84/11)=8
		},
		{
			name:     "both unset: cap disabled -> bounded by real headroom",
			totalTlc: 64, floor: 5, maxPerNode: 0, nodeHeadroom: rep(10, 32),
			wantCount: 5, wantCores: 13, // perContainerCap=32, ceil(64/32)=2 -> floor 5, ceil(64/5)=13
		},
		{
			name:     "both unset: need more containers than compute nodes -> infeasible",
			totalTlc: 200, floor: 5, maxPerNode: cap, nodeHeadroom: rep(10, 16),
			wantInfeasibleSub: "only 10 compute nodes", wantBinding: bindingCores,
		},
		{
			name:     "both unset: a node has zero compute headroom -> infeasible",
			totalTlc: 10, floor: 5, maxPerNode: cap, nodeHeadroom: []int{5, 0, 5, 5, 5},
			wantInfeasibleSub: "no compute core headroom",
		},
		{
			// deriveComputeLayout must reason per-node, not by a single global min: with count=3, topNMin
			// picks the 3rd-largest of [2,2,2,0,0]=2, so cores=ceil(6/3)=2 fits even though min(nodeHeadroom)=0.
			name:     "both unset: FIX 2 per-node reasoning tolerates unused zero-headroom nodes",
			totalTlc: 6, floor: 3, maxPerNode: cap, nodeHeadroom: []int{2, 2, 2, 0, 0},
			wantCount: 3, wantCores: 2,
		},
		{
			name:      "cores set under headroom: derive count to meet 1:1",
			specCores: 4, totalTlc: 20, floor: 5, maxPerNode: cap, nodeHeadroom: rep(8, 16),
			wantCount: 5, wantCores: 4, // ceil(20/4)=5
		},
		{
			name:      "cores set above headroom: fail fast (no clamp)",
			specCores: 32, totalTlc: 160, floor: 5, maxPerNode: cap, nodeHeadroom: rep(14, 26),
			wantInfeasibleSub: "exceeds the per-node compute core headroom (19)", wantBinding: bindingCores,
		},
		{
			name:      "cores set: real headroom below cap -> fail fast",
			specCores: 10, totalTlc: 84, floor: 5, maxPerNode: cap, nodeHeadroom: rep(14, 8),
			wantInfeasibleSub: "exceeds the per-node compute core headroom (8)",
		},
		{
			name:      "explicit count+cores break 1:1 -> fail fast",
			specCount: 2, specCores: 1, totalTlc: 10, floor: 5, maxPerNode: cap, nodeHeadroom: rep(5, 16),
			wantInfeasibleSub: "compute:drive core ratio not met",
		},
		{
			name:      "explicit count, cores unset: derive cores to meet 1:1",
			specCount: 6, totalTlc: 84, floor: 5, maxPerNode: cap, nodeHeadroom: rep(14, 26),
			wantCount: 6, wantCores: 14, // ceil(84/6)=14
		},
		{
			name:      "explicit count + cores above headroom: fail fast",
			specCount: 3, specCores: 20, totalTlc: 10, floor: 5, maxPerNode: cap, nodeHeadroom: rep(5, 16),
			wantInfeasibleSub: "exceeds the per-node compute core headroom (16)",
		},
		{
			name:      "explicit count exceeds compute nodes -> infeasible",
			specCount: 20, specCores: 2, totalTlc: 10, floor: 5, maxPerNode: cap, nodeHeadroom: rep(5, 16),
			wantInfeasibleSub: "exceeds the 5 compute nodes",
		},
		{
			// n=5 core-fits (c=12, cap 16) but needs 36000 MiB hugepages against 30000 MiB free per
			// node -> hugepages reject n=5 and the scan advances to n=6 (c=10, exactly 30000 MiB),
			// pushing the count up rather than reporting infeasible.
			name:     "auto: hugepages force count up from 5 to 6 (more, smaller containers)",
			totalTlc: 60, floor: 5, maxPerNode: cap, nodeHeadroom: rep(6, 16),
			nodeHugepagesMiB: rep(6, 30000),
			hugepagesFor:     func(count, cores int) int { return 3000 * cores },
			wantCount:        6, wantCores: 10,
		},
		{
			// Same core-feasible scan (n=5..6), but only 100 MiB hugepages free per node — far below
			// the 3000 MiB/core minimum at any n -> every candidate is hugepages-infeasible, and the
			// terminal message must attribute failure to hugepages specifically (cores are never short).
			name:     "auto: hugepages force infeasible at every count",
			totalTlc: 60, floor: 5, maxPerNode: cap, nodeHeadroom: rep(6, 16),
			nodeHugepagesMiB:  rep(6, 100),
			hugepagesFor:      func(count, cores int) int { return 3000 * cores },
			wantInfeasibleSub: "hugepages insufficient for", wantBinding: bindingHugepages,
		},
		{
			// computeContainers=6 core-fits (cores=10 via ceil(60/6), cap 16) but needs 30000 MiB
			// hugepages against only 100 MiB free -> hugepagesFitCheck must fail fast, not silently
			// accept a layout that starves on hugepages at placement time.
			name:      "explicit count: core-fits but hugepages reject -> fail fast with new message",
			specCount: 6, totalTlc: 60, floor: 5, maxPerNode: cap, nodeHeadroom: rep(6, 16),
			nodeHugepagesMiB:  rep(6, 100),
			hugepagesFor:      func(count, cores int) int { return 3000 * cores },
			wantInfeasibleSub: "needs 30000 MiB hugepages per container", wantBinding: bindingHugepages,
		},
		{
			// Same defect, pinned via specCores=10 instead — covers the other explicit branch
			// through the same shared hugepagesFitCheck gate.
			name:      "explicit cores: core-fits but hugepages reject -> fail fast with new message",
			specCores: 10, totalTlc: 60, floor: 5, maxPerNode: cap, nodeHeadroom: rep(6, 16),
			nodeHugepagesMiB:  rep(6, 100),
			hugepagesFor:      func(count, cores int) int { return 3000 * cores },
			wantInfeasibleSub: "needs 30000 MiB hugepages per container", wantBinding: bindingHugepages,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			count, cores, infeasible, binding, warnings := deriveComputeLayout(
				c.specCount, c.specCores, c.totalTlc, c.floor, c.maxPerNode, c.nodeHeadroom,
				c.nodeHugepagesMiB, c.hugepagesFor)

			joinedWarn := strings.Join(warnings, " | ")
			if c.wantInfeasibleSub != "" {
				if !strings.Contains(infeasible, c.wantInfeasibleSub) {
					t.Fatalf("expected infeasible containing %q, got infeasible=%q (count=%d cores=%d)", c.wantInfeasibleSub, infeasible, count, cores)
				}
				if c.wantBinding != "" && binding != c.wantBinding {
					t.Errorf("binding = %q, want %q", binding, c.wantBinding)
				}
				return
			}
			if infeasible != "" {
				t.Fatalf("unexpected infeasible: %q", infeasible)
			}
			if count != c.wantCount || cores != c.wantCores {
				t.Errorf("got count=%d cores=%d, want count=%d cores=%d", count, cores, c.wantCount, c.wantCores)
			}
			// 1:1 only holds when count or cores is auto-derived; pinning both may violate it (warned instead).
			if !(c.specCount != 0 && c.specCores != 0) && count*cores < c.totalTlc {
				t.Errorf("1:1 violated: count*cores=%d < totalTlc=%d", count*cores, c.totalTlc)
			}
			if c.wantWarnSub == "" {
				if len(warnings) != 0 {
					t.Errorf("expected no warnings, got %q", joinedWarn)
				}
			} else if !strings.Contains(joinedWarn, c.wantWarnSub) {
				t.Errorf("expected a warning containing %q, got %q", c.wantWarnSub, joinedWarn)
			}
		})
	}
}

// Pins the same numbers as internal/validation TestAutoFullDrivesComputeHugepages_CoreCapBindsBeforeHugepages
// (96 required compute cores, MaxCoresPerContainer=19): deriveComputeLayout and the validator's sweep must
// reject the same counts for the same reason, or admission would pass a plan the planner refuses to build.
func TestDeriveComputeLayout_AgreesWithAutoFullDrivesHugepagesValidator(t *testing.T) {
	const requiredComputeCores = 96
	const maxCoresPerContainer = 19
	const floor = 5
	const bigHeadroom = 200 // headroom is not the binding constraint in this scenario; the cap is

	// 5 compute-eligible nodes: ceil(96/5)=20 exceeds the cap of 19 at the only count the sweep can try
	// (floor==nodeCount==5), so no count fits and the plan is infeasible.
	_, _, infeasible, _, _ := deriveComputeLayout(0, 0, requiredComputeCores, floor, maxCoresPerContainer, rep(5, bigHeadroom), nil, nil)
	if infeasible == "" {
		t.Fatalf("expected infeasible with 5 nodes (cap binds at every reachable count), got feasible")
	}

	// A sixth node lowers the requirement to ceil(96/6)=16, under the cap, so n=6 fits exactly as the
	// validator's control case expects.
	count, cores, infeasible, _, _ := deriveComputeLayout(0, 0, requiredComputeCores, floor, maxCoresPerContainer, rep(6, bigHeadroom), nil, nil)
	if infeasible != "" {
		t.Fatalf("expected feasible with 6 nodes, got infeasible: %q", infeasible)
	}
	if count != 6 || cores != 16 {
		t.Errorf("got count=%d cores=%d, want count=6 cores=16", count, cores)
	}

	// Pinned computeCores takes the specCores branch: cores is honored exactly, and count follows as
	// max(floor, ceil(required/cores)) — mirrored by validateAutoFullDrivesPinnedComputeCores's own formula.
	// 18 pinned cores need ceil(96/18)=6 containers; 5 compute-eligible nodes cannot host 6 one-per-node.
	const pinnedCores = 18
	_, _, infeasible, _, _ = deriveComputeLayout(0, pinnedCores, requiredComputeCores, floor, maxCoresPerContainer, rep(5, bigHeadroom), nil, nil)
	if infeasible == "" {
		t.Fatalf("expected infeasible with computeCores=%d pinned and only 5 compute nodes (needs 6), got feasible", pinnedCores)
	}

	// A sixth node supplies the 6th container the pin needs, so it fits at exactly the pinned cores —
	// deriveComputeLayout must never re-derive cores away from the pin.
	count, cores, infeasible, _, _ = deriveComputeLayout(0, pinnedCores, requiredComputeCores, floor, maxCoresPerContainer, rep(6, bigHeadroom), nil, nil)
	if infeasible != "" {
		t.Fatalf("expected feasible with computeCores=%d pinned and 6 compute nodes, got infeasible: %q", pinnedCores, infeasible)
	}
	if count != 6 || cores != pinnedCores {
		t.Errorf("got count=%d cores=%d, want count=6 cores=%d (pin honored exactly, not re-derived)", count, cores, pinnedCores)
	}
}

// TestComputeLayoutWouldGrow covers the steady-state skip gate: reports whether the planner could ever
// need MORE compute than is already running; false means it's safe to skip node inventory.
func TestComputeLayoutWouldGrow(t *testing.T) {
	const cap = 19 // MaxCoresPerContainer

	cases := []struct {
		name                                              string
		specCount, specCores, totalTlc, floor, maxPerNode int
		curCount, curMinCores, curTotalCores              int
		want                                              bool
	}{
		{
			name:     "auto: current matches cap-bound target -> no grow",
			totalTlc: 84, floor: 5, maxPerNode: cap, curCount: 6, curMinCores: 14, curTotalCores: 84, want: false,
		},
		{
			// Current set larger than needed (never shrink) -> no growth.
			name:     "auto: current exceeds target -> no grow",
			totalTlc: 84, floor: 5, maxPerNode: cap, curCount: 8, curMinCores: 16, curTotalCores: 128, want: false,
		},
		{
			name:     "auto: current cores short -> grow",
			totalTlc: 84, floor: 5, maxPerNode: cap, curCount: 6, curMinCores: 10, curTotalCores: 60, want: true,
		},
		{
			name:     "auto: current count short -> grow",
			totalTlc: 84, floor: 5, maxPerNode: cap, curCount: 4, curMinCores: 16, curTotalCores: 64, want: true,
		},
		{
			name:     "no current compute -> grow",
			totalTlc: 10, floor: 5, maxPerNode: cap, curCount: 0, curMinCores: 0, curTotalCores: 0, want: true,
		},
		{
			// One compute frozen below the uniform target (minCores 10 < 14), but total cores already
			// cover 1:1 (>= 84) via compensating containers -> stable, no re-plan needed.
			name:     "frozen compute, total covers 1:1 -> no grow",
			totalTlc: 84, floor: 5, maxPerNode: cap, curCount: 7, curMinCores: 10, curTotalCores: 88, want: false,
		},
		{
			name:     "frozen compute, total short of 1:1 -> grow",
			totalTlc: 84, floor: 5, maxPerNode: cap, curCount: 6, curMinCores: 10, curTotalCores: 80, want: true,
		},
		{
			name:      "explicit count exceeds current -> grow",
			specCount: 10, totalTlc: 50, floor: 5, maxPerNode: cap, curCount: 6, curMinCores: 16, curTotalCores: 96, want: true,
		},
		{
			name:      "explicit count met, cores fit -> no grow",
			specCount: 6, totalTlc: 60, floor: 5, maxPerNode: cap, curCount: 6, curMinCores: 16, curTotalCores: 96, want: false,
		},
		{
			name:      "explicit cores above current -> grow",
			specCores: 12, totalTlc: 12, floor: 5, maxPerNode: cap, curCount: 6, curMinCores: 8, curTotalCores: 48, want: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ComputeLayoutWouldGrow(c.specCount, c.specCores, c.totalTlc, c.floor, c.maxPerNode, c.curCount, c.curMinCores, c.curTotalCores)
			if got != c.want {
				t.Errorf("ComputeLayoutWouldGrow() = %v, want %v", got, c.want)
			}
		})
	}
}

// nodeSet turns a compute-node name list into a set for membership assertions.
func nodeSet(names []string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// Test_UnscheduledCompute_CountedAsCommitted_NoFreshNodes: a still-Pending (Unscheduled) existing
// compute must count as committed/frozen capacity so the planner doesn't recreate a fresh batch
// elsewhere — regression for a bug where best-fit preferred emptier fresh nodes over the pinned ones.
func Test_UnscheduledCompute_CountedAsCommitted_NoFreshNodes(t *testing.T) {
	s := testScheme() // minFdNum = 6
	cons := testCons()
	cons.MaxCoresPerContainer = 0 // disable policy cap; real per-node headroom binds

	var existingDrives []ExistingContainer
	for i := 1; i <= 6; i++ {
		n := "drv" + itoa(i)
		existingDrives = append(existingDrives, ExistingContainer{
			Name: "drive" + itoa(i), Node: n, FDValue: n,
			TlcGiB: 60 * tib, NumCores: 12,
		})
	}

	// Six existing computes covering the 6×12 target, each pinned to its node but its pod still Pending.
	const curHP = 1600 * 12 // plausible frozen hugepages; nodes are huge so it never binds
	var existingCompute []ExistingComputeContainer
	for i := 1; i <= 6; i++ {
		existingCompute = append(existingCompute, ExistingComputeContainer{
			Name: "compute" + itoa(i), Node: "cmp" + itoa(i), NumCores: 12, HugepagesMiB: curHP,
			Unscheduled: true,
		})
	}

	inv := make([]NodeCapacity, 0, 18)
	for i := 1; i <= 6; i++ {
		inv = append(inv, NodeCapacity{
			NodeName: "drv" + itoa(i), FDValue: "drv" + itoa(i),
			TlcGiB: 100 * tib, AllocatableCPU: 64, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28,
		})
	}
	// cmp nodes: 25 cores → after the inventory charges the 12-core unscheduled compute, 13 free
	// (still ≥ the 12-core target, so hmin=13 keeps count=6/cores=12) but below the fresh nodes' 64.
	for i := 1; i <= 6; i++ {
		inv = append(inv, NodeCapacity{
			NodeName: "cmp" + itoa(i), FDValue: "cmp" + itoa(i),
			TlcGiB: 0, AllocatableCPU: 25, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28,
		})
	}
	// Six ample fresh compute nodes — the buggy planner steers the duplicate batch here.
	for i := 1; i <= 6; i++ {
		inv = append(inv, NodeCapacity{
			NodeName: "free" + itoa(i), FDValue: "free" + itoa(i),
			TlcGiB: 0, AllocatableCPU: 64, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28,
		})
	}

	computeNodes := computeNodeSet(
		"cmp1", "cmp2", "cmp3", "cmp4", "cmp5", "cmp6",
		"free1", "free2", "free3", "free4", "free5", "free6",
	)
	inv = netCompute(inv, existingCompute, cons)
	plan := PlanCapacity(DesiredCapacity{TlcRawGiB: 6 * 60 * tib}, s, existingDrives, existingCompute, inv, computeNodes, cons)

	if plan.Infeasible != "" {
		t.Fatalf("unexpected infeasible: %s", plan.Infeasible)
	}
	if plan.ComputeContainers != 6 || plan.ComputeCores != 12 {
		t.Fatalf("want compute 6x12, got %dx%d", plan.ComputeContainers, plan.ComputeCores)
	}
	if len(plan.ComputeNodes) != 6 {
		t.Fatalf("want exactly 6 compute nodes (the pinned unscheduled set), got %d: %v",
			len(plan.ComputeNodes), plan.ComputeNodes)
	}
	pinned := nodeSet([]string{"cmp1", "cmp2", "cmp3", "cmp4", "cmp5", "cmp6"})
	for _, n := range plan.ComputeNodes {
		if !pinned[n] {
			t.Fatalf("fresh node %q consumed — the unscheduled compute set was recreated instead of "+
				"counted as committed capacity; ComputeNodes=%v", n, plan.ComputeNodes)
		}
	}
}

// Test_MultiPass_UnscheduledCompute_DoesNotGrow: pass-1's still-Pending compute set must be frozen and
// re-targeted identically in pass 2, not duplicated onto untouched fresh nodes (which a buggy planner
// prefers for their higher headroom, doubling the node set).
func Test_MultiPass_UnscheduledCompute_DoesNotGrow(t *testing.T) {
	s := testScheme() // minFdNum = 6
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
	desired := DesiredCapacity{TlcRawGiB: 6 * 60 * tib} // 72 TLC drive cores → compute 6×12

	baseInv := func() []NodeCapacity {
		inv := make([]NodeCapacity, 0, 18)
		for i := 1; i <= 6; i++ {
			inv = append(inv, NodeCapacity{
				NodeName: "drv" + itoa(i), FDValue: "drv" + itoa(i),
				TlcGiB: 100 * tib, AllocatableCPU: 64, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28,
			})
		}
		for i := 1; i <= 12; i++ {
			inv = append(inv, NodeCapacity{
				NodeName: "cmp" + itoa(i), FDValue: "cmp" + itoa(i),
				TlcGiB: 0, AllocatableCPU: 24, AvailableHugepagesMiB: 1 << 28, AvailableMemoryMiB: 1 << 28,
			})
		}
		return inv
	}
	var cmpNodes []string
	for i := 1; i <= 12; i++ {
		cmpNodes = append(cmpNodes, "cmp"+itoa(i))
	}
	computeNodes := computeNodeSet(cmpNodes...)

	// Pass 1: no existing compute — the planner places the initial 6×12 set on fresh nodes.
	plan1 := PlanCapacity(desired, s, existingDrives, nil, baseInv(), computeNodes, cons)
	if plan1.Infeasible != "" {
		t.Fatalf("pass 1 unexpected infeasible: %s", plan1.Infeasible)
	}
	if plan1.ComputeContainers != 6 {
		t.Fatalf("pass 1 want 6 compute containers, got %d (%v)", plan1.ComputeContainers, plan1.ComputeNodes)
	}
	pass1Nodes := nodeSet(plan1.ComputeNodes)

	// Feed pass 1's layout back as still-Pending existing compute, charged against inventory as production would.
	var existingCompute []ExistingComputeContainer
	for i, c := range plan1.ComputeLayout {
		existingCompute = append(existingCompute, ExistingComputeContainer{
			Name: "compute" + itoa(i+1), Node: c.Node, NumCores: c.NumCores, HugepagesMiB: c.HugepagesMiB,
			Unscheduled: true,
		})
	}
	inv2 := netCompute(baseInv(), existingCompute, cons)

	// Pass 2: the same target, now with the pass-1 computes present but unscheduled.
	plan2 := PlanCapacity(desired, s, existingDrives, existingCompute, inv2, computeNodes, cons)
	if plan2.Infeasible != "" {
		t.Fatalf("pass 2 unexpected infeasible: %s", plan2.Infeasible)
	}
	if plan2.ComputeContainers != 6 {
		t.Fatalf("pass 2 want 6 compute containers, got %d", plan2.ComputeContainers)
	}

	// The set must not grow: pass 2 must re-target exactly the pass-1 nodes. BEFORE the fix, pass 2
	// picks the untouched higher-headroom nodes (disjoint), so the union doubles to 12.
	union := nodeSet(plan1.ComputeNodes)
	for _, n := range plan2.ComputeNodes {
		union[n] = true
	}
	if len(union) != 6 {
		t.Fatalf("compute set grew across passes — pass1=%v pass2=%v (union=%d); the "+
			"unscheduled pass-1 computes were recreated on fresh nodes instead of frozen",
			plan1.ComputeNodes, plan2.ComputeNodes, len(union))
	}
	for _, n := range plan2.ComputeNodes {
		if !pass1Nodes[n] {
			t.Fatalf("pass 2 targeted new node %q not in the pass-1 set %v", n, plan1.ComputeNodes)
		}
	}
}

// TestNodesFitting_EquivalenceToTopNMin proves nodesFitting degenerates to topNMin's core-only
// semantics when nodeHugepagesMiB == nil, keeping deriveComputeLayout byte-identical for hugepages-blind cases:
//
//	nodesFitting(h, nil, c, 0, cap) >= n   <=>   topNMin(h, n, cap) >= c
//
// Swept over randomized headroom vectors, including zero-headroom nodes and cap == 0.
func TestNodesFitting_EquivalenceToTopNMin(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	caps := []int{0, 1, 8, 16, 32}

	for trial := 0; trial < 200; trial++ {
		size := 1 + rng.Intn(12)
		h := make([]int, size)
		for i := range h {
			h[i] = rng.Intn(33) // 0..32 inclusive, includes zero-headroom nodes
		}
		cap := caps[rng.Intn(len(caps))]

		for n := 1; n <= size; n++ {
			for c := 1; c <= 33; c++ {
				got := nodesFitting(h, nil, c, 0, cap) >= n
				want := topNMin(h, n, cap) >= c
				if got != want {
					t.Fatalf("trial=%d h=%v cap=%d n=%d c=%d: nodesFitting-derived=%v topNMin-derived=%v (mismatch)",
						trial, h, cap, n, c, got, want)
				}
			}
		}
	}
}

// TestHugepagesFor_MonotonicInN checks the monotonicity the auto-scan relies on to prefer more, smaller
// containers: for a fixed total core target t, hugepagesFor(n, ceil(t/n)) must be non-increasing as n
// grows (both the capacity-based share and the per-core term shrink with larger n).
func TestHugepagesFor_MonotonicInN(t *testing.T) {
	cons := testCons()
	cons.ComputeHugepagesTlcRatio = 500 // deliberately non-zero (non-negotiable #6) so capacityBased binds
	const t_ = 84                       // totalTlcDriveCores, matches the OP-329 bug scenario fixture
	const tlcRawGiB = 200 * tib

	hugepagesFor := func(count, cores int) int {
		return ComputeContainerHugepagesMiB(tlcRawGiB, 0, count, cores, cons)
	}

	prevHP := -1
	for n := 1; n <= t_; n++ {
		cores := max(1, util.CeilDiv(t_, n))
		hp := hugepagesFor(n, cores)
		if prevHP >= 0 && hp > prevHP {
			t.Fatalf("hugepagesFor not monotonic non-increasing in n: n=%d hp=%d > previous hp=%d", n, hp, prevHP)
		}
		prevHP = hp
	}
}

// Test_ComputeLayoutWouldGrow_HugepagesCannotFlipSkipGate proves hugepages-awareness in the auto-scan
// can only push the derived count up (or infeasible), never down — checked against deriveComputeLayout
// directly since ComputeLayoutWouldGrow's signature stays cheap/headroom-only by design.
func Test_ComputeLayoutWouldGrow_HugepagesCannotFlipSkipGate(t *testing.T) {
	cases := []struct {
		name         string
		totalTlc     int
		floor        int
		maxPerNode   int
		nodeHeadroom []int
	}{
		{
			name:     "OP-329 bug scenario fixture",
			totalTlc: 84, floor: 5, maxPerNode: 16, nodeHeadroom: rep(14, 26),
		},
		{
			name:     "small per-node headroom fixture",
			totalTlc: 84, floor: 5, maxPerNode: 16, nodeHeadroom: rep(14, 8),
		},
		{
			name:     "cap disabled fixture",
			totalTlc: 64, floor: 5, maxPerNode: 0, nodeHeadroom: rep(10, 32),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			count0, _, infeasible0, _, _ := deriveComputeLayout(
				0, 0, c.totalTlc, c.floor, c.maxPerNode, c.nodeHeadroom, nil, nil)
			if infeasible0 != "" {
				t.Fatalf("blind (hugepages-unaware) derivation unexpectedly infeasible: %s", infeasible0)
			}

			// Artificially tight hugepages (1 MiB free, huge per-container cost) so hugepages can only
			// force a larger n or infeasible, never a smaller one.
			nodeHugepagesMiB := rep(len(c.nodeHeadroom), 1)
			hugepagesFor := func(count, cores int) int { return 1_000_000 * cores }

			count1, _, infeasible1, _, _ := deriveComputeLayout(
				0, 0, c.totalTlc, c.floor, c.maxPerNode, c.nodeHeadroom, nodeHugepagesMiB, hugepagesFor)

			if infeasible1 == "" && count1 < count0 {
				t.Fatalf("hugepages-awareness produced a SMALLER count than the blind derivation: "+
					"blind count=%d, hugepages-aware count=%d — this would mean hugepages-awareness could "+
					"incorrectly cause ComputeLayoutWouldGrow's skip gate to skip a needed re-plan",
					count0, count1)
			}
		})
	}
}
