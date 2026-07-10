package allocator

import (
	"strings"
	"testing"
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
	const cap = 16 // MaxComputeCoresPerNode

	cases := []struct {
		name                                  string
		specCount, specCores, totalTlc, floor int
		maxPerNode                            int
		nodeHeadroom                          []int
		wantCount, wantCores                  int
		wantInfeasibleSub                     string // substring in infeasible ("" => feasible)
		wantWarnSub                           string // substring in a warning ("" => no warning)
	}{
		{
			// OP-329 bug scenario: 200TiB / SW,RL,HS=3,2,1 / 14 TLC nodes -> totalTlc=84.
			// Nodes are large (drive took ~6 cores, plenty left) so cap binds: 6 x 14, NOT 84 x 1.
			name:     "both unset: bug scenario, cap binds -> minimal count",
			totalTlc: 84, floor: 5, maxPerNode: cap, nodeHeadroom: rep(14, 26),
			wantCount: 6, wantCores: 14,
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
			wantInfeasibleSub: "only 10 compute nodes",
		},
		{
			name:     "both unset: a node has zero compute headroom -> infeasible",
			totalTlc: 10, floor: 5, maxPerNode: cap, nodeHeadroom: []int{5, 0, 5, 5, 5},
			wantInfeasibleSub: "no compute core headroom",
		},
		{
			name:      "cores set under headroom: derive count to meet 1:1",
			specCores: 4, totalTlc: 20, floor: 5, maxPerNode: cap, nodeHeadroom: rep(8, 16),
			wantCount: 5, wantCores: 4, // ceil(20/4)=5
		},
		{
			name:      "cores set above headroom: fail fast (no clamp)",
			specCores: 32, totalTlc: 160, floor: 5, maxPerNode: cap, nodeHeadroom: rep(14, 26),
			wantInfeasibleSub: "exceeds the per-node compute core headroom (16)",
		},
		{
			name:      "cores set: real headroom below cap -> fail fast",
			specCores: 10, totalTlc: 84, floor: 5, maxPerNode: cap, nodeHeadroom: rep(14, 8),
			wantInfeasibleSub: "exceeds the per-node compute core headroom (8)",
		},
		{
			name:      "explicit count+cores break 1:1 -> fail fast",
			specCount: 2, specCores: 1, totalTlc: 10, floor: 5, maxPerNode: cap, nodeHeadroom: rep(5, 16),
			wantInfeasibleSub: "1:1 core ratio not met",
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
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			count, cores, infeasible, warnings := deriveComputeLayout(
				c.specCount, c.specCores, c.totalTlc, c.floor, c.maxPerNode, c.nodeHeadroom)

			joinedWarn := strings.Join(warnings, " | ")
			if c.wantInfeasibleSub != "" {
				if !strings.Contains(infeasible, c.wantInfeasibleSub) {
					t.Fatalf("expected infeasible containing %q, got infeasible=%q (count=%d cores=%d)", c.wantInfeasibleSub, infeasible, count, cores)
				}
				return
			}
			if infeasible != "" {
				t.Fatalf("unexpected infeasible: %q", infeasible)
			}
			if count != c.wantCount || cores != c.wantCores {
				t.Errorf("got count=%d cores=%d, want count=%d cores=%d", count, cores, c.wantCount, c.wantCores)
			}
			// The 1:1 invariant only holds when count or cores is auto-derived; when the user pins BOTH
			// it may be violated (and we warn instead).
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

// TestComputeLayoutWouldGrow covers the steady-state skip gate: given the current compute set and an
// unbounded-headroom (policy-cap-only) derivation, it must report whether the planner could ever ask
// for MORE compute than is already running. False => safe to leave compute as-is (skip node inventory).
func TestComputeLayoutWouldGrow(t *testing.T) {
	const cap = 16 // MaxComputeCoresPerNode

	cases := []struct {
		name                                              string
		specCount, specCores, totalTlc, floor, maxPerNode int
		curCount, curMinCores, curTotalCores              int
		want                                              bool
	}{
		{
			// Auto-derive, cap binds: target is 6x14. Current set already 6x14 -> no growth.
			name:     "auto: current matches cap-bound target -> no grow",
			totalTlc: 84, floor: 5, maxPerNode: cap, curCount: 6, curMinCores: 14, curTotalCores: 84, want: false,
		},
		{
			// Current set larger than needed (never shrink) -> no growth.
			name:     "auto: current exceeds target -> no grow",
			totalTlc: 84, floor: 5, maxPerNode: cap, curCount: 8, curMinCores: 16, curTotalCores: 128, want: false,
		},
		{
			// Current cores below the derived per-container target AND total short -> must grow cores.
			name:     "auto: current cores short -> grow",
			totalTlc: 84, floor: 5, maxPerNode: cap, curCount: 6, curMinCores: 10, curTotalCores: 60, want: true,
		},
		{
			// Current count below the derived container count -> must grow count.
			name:     "auto: current count short -> grow",
			totalTlc: 84, floor: 5, maxPerNode: cap, curCount: 4, curMinCores: 16, curTotalCores: 64, want: true,
		},
		{
			// No current compute at all -> must create.
			name:     "no current compute -> grow",
			totalTlc: 10, floor: 5, maxPerNode: cap, curCount: 0, curMinCores: 0, curTotalCores: 0, want: true,
		},
		{
			// Heterogeneous/frozen steady state: one compute frozen below the uniform target (minCores 10
			// < 14) but the TOTAL current cores already cover the 1:1 requirement (>= totalTlc 84) thanks
			// to compensating containers -> NOT growth (stable), must skip re-plan.
			name:     "frozen compute, total covers 1:1 -> no grow",
			totalTlc: 84, floor: 5, maxPerNode: cap, curCount: 7, curMinCores: 10, curTotalCores: 88, want: false,
		},
		{
			// Frozen below target AND total still short of 1:1 -> must re-plan to add compensation.
			name:     "frozen compute, total short of 1:1 -> grow",
			totalTlc: 84, floor: 5, maxPerNode: cap, curCount: 6, curMinCores: 10, curTotalCores: 80, want: true,
		},
		{
			// Explicit count exceeding current set -> infeasible at curCount -> grow.
			name:      "explicit count exceeds current -> grow",
			specCount: 10, totalTlc: 50, floor: 5, maxPerNode: cap, curCount: 6, curMinCores: 16, curTotalCores: 96, want: true,
		},
		{
			// Explicit count met, derived cores fit current -> no grow.
			name:      "explicit count met, cores fit -> no grow",
			specCount: 6, totalTlc: 60, floor: 5, maxPerNode: cap, curCount: 6, curMinCores: 16, curTotalCores: 96, want: false,
		},
		{
			// Explicit cores higher than current per-container cores, total short -> grow.
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

// Test_UnscheduledCompute_CountedAsCommitted_NoFreshNodes: an already-created compute whose pod is
// still Pending (Node pinned, Unscheduled) must count as committed capacity — frozen and its node
// pinned — so the planner does NOT recreate a fresh batch on other nodes.
//
// 6 scheduled drives (72 TLC drive cores) make compute derive to 6×12. Six existing Unscheduled
// computes on cmp1..cmp6 already cover that. Each cmp node keeps 13 cores free after its footprint is
// charged (still fit, so hmin stays ≥ 12) but less than the ample fresh nodes (free1..free6, 64), so
// best-fit prefers the fresh nodes: the buggy planner skips the unscheduled computes and fills the
// shortfall onto free1..free6 (FAILS); the fix freezes them, shortfall == 0, ComputeNodes = cmp1..cmp6.
func Test_UnscheduledCompute_CountedAsCommitted_NoFreshNodes(t *testing.T) {
	s := testScheme() // minFdNum = 6
	cons := testCons()
	cons.MaxComputeCoresPerNode = 0 // disable policy cap; real per-node headroom binds

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

// Test_MultiPass_UnscheduledCompute_DoesNotGrow reproduces the accumulation across re-plans: pass 1
// places the compute set; those pods are still Pending (Unscheduled) when pass 2 re-plans. The buggy
// planner ignores them and places another full batch on the remaining fresh nodes, so the two passes
// target DISJOINT node sets (union doubles). The fix freezes the pass-1 computes, so pass 2 re-targets
// the same nodes and the set does not grow.
//
// Twelve compute-eligible nodes, each 24 cores: one 12-core container leaves 12 free — still fit (hmin
// stays ≥ 12) but with less headroom than the six untouched 24-core nodes, so the buggy pass-2 shortfall
// deterministically prefers the untouched nodes, proving the disjoint double-create.
func Test_MultiPass_UnscheduledCompute_DoesNotGrow(t *testing.T) {
	s := testScheme() // minFdNum = 6
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

	// Feed pass 1's layout back as still-Pending (Unscheduled) existing compute, and charge its
	// footprint against the inventory exactly as production would.
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
