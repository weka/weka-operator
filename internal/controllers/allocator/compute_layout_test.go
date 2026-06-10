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
