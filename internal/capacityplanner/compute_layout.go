package capacityplanner

import (
	"fmt"
	"sort"

	"github.com/weka/weka-operator/pkg/util"
)

// compute_layout.go sizes and orders compute containers against per-node core/hugepages headroom, shared
// by both planners. Must not depend on the clusterCapacity planner's private per-node tracking struct —
// these primitives speak only []int and closures, so either planner's headroom representation can feed them.

// topNMin returns the per-container core cap achievable by choosing the n largest-headroom nodes out of
// nodeHeadroom, capped by maxCoresPerContainer (0 disables). A layout of n containers only needs to fit
// the weakest of the chosen n, not the weakest node overall (the global-min bug this replaces). n <= 0 or
// n > len(nodeHeadroom) returns 0.
func topNMin(nodeHeadroom []int, n, maxCoresPerContainer int) int {
	if n <= 0 || n > len(nodeHeadroom) {
		return 0
	}
	sorted := append([]int(nil), nodeHeadroom...)
	sort.Sort(sort.Reverse(sort.IntSlice(sorted)))
	return topNMinSorted(sorted, n, maxCoresPerContainer)
}

// topNMinSorted is topNMin given nodeHeadroom pre-sorted descending, so a caller scanning many n values
// (deriveComputeLayout's default branch) can sort once and reuse it.
func topNMinSorted(sorted []int, n, maxCoresPerContainer int) int {
	if n <= 0 || n > len(sorted) {
		return 0
	}
	capVal := sorted[n-1]
	if maxCoresPerContainer > 0 && maxCoresPerContainer < capVal {
		capVal = maxCoresPerContainer
	}
	return capVal
}

// nodesFitting counts candidate nodes that can independently host one `cores`-core, `hpMiB`-hugepage
// container (each node's headroom capped at maxCoresPerContainer, 0 disables). Unlike topNMin's joint
// best-n set, this is a per-node predicate count; nil nodeHugepagesMiB skips the hugepages test.
func nodesFitting(nodeHeadroom, nodeHugepagesMiB []int, cores, hpMiB, maxCoresPerContainer int) int {
	count := 0
	for i, h := range nodeHeadroom {
		eff := h
		if maxCoresPerContainer > 0 && maxCoresPerContainer < eff {
			eff = maxCoresPerContainer
		}
		if eff < cores {
			continue
		}
		if i < len(nodeHugepagesMiB) && nodeHugepagesMiB[i] < hpMiB {
			continue
		}
		count++
	}
	return count
}

// hugepagesFitCheck verifies at least `count` nodes (already core-feasible via topNMin) can also host
// `cores`-cores' worth of hugepages, returning a fail-fast message otherwise. nil hugepagesFor disables it.
func hugepagesFitCheck(nodeHeadroom, nodeHugepagesMiB []int, count, cores, maxCoresPerContainer int, hugepagesFor func(count, cores int) int) string {
	if hugepagesFor == nil {
		return ""
	}
	hp := hugepagesFor(count, cores)
	if nodesFitting(nodeHeadroom, nodeHugepagesMiB, cores, hp, maxCoresPerContainer) >= count {
		return ""
	}
	// hpFit (message only) counts nodes meeting the hugepages bar alone.
	hpFit := len(nodeHeadroom)
	if nodeHugepagesMiB != nil {
		hpFit = 0
		for _, avail := range nodeHugepagesMiB {
			if avail >= hp {
				hpFit++
			}
		}
	}
	return fmt.Sprintf(
		"computeCores=%d needs %d MiB hugepages per container, but only %d of %d compute node(s) have that much free after drive placement",
		cores, hp, hpFit, len(nodeHeadroom))
}

// deriveComputeLayout sizes compute containers to supply requiredComputeCores (see RequiredComputeCores)
// against real per-node headroom (fit is checked only for the nodes actually chosen; see topNMin), returning
// a non-empty infeasible reason when it cannot. Invariants when feasible: count in [floor, len(nodeHeadroom)];
// count*cores >= requiredComputeCores whenever either is auto-derived; hugepagesFor (if set) must also pass.
func deriveComputeLayout(specCount, specCores, requiredComputeCores, floor, maxCoresPerContainer int, nodeHeadroom, nodeHugepagesMiB []int, hugepagesFor func(count, cores int) int) (count, cores int, infeasible string, warnings []string) {
	d := len(nodeHeadroom)
	t := requiredComputeCores

	switch {
	case specCount != 0:
		// Explicit count (one-per-node, <= compute node count); derive cores to meet the requirement when
		// unset. The cap only needs to hold for the `count` nodes actually used.
		count = specCount
		if count > d {
			return 0, 0, fmt.Sprintf(
				"computeContainers=%d exceeds the %d compute nodes; compute spreads one-per-node", count, d), nil
		}
		cores = specCores
		if cores == 0 {
			cores = max(1, util.CeilDiv(t, count))
		}
		perContainerCap := topNMin(nodeHeadroom, count, maxCoresPerContainer)
		// Explicit values are honored exactly — fail fast, don't clamp.
		if perContainerCap > 0 && cores > perContainerCap {
			return 0, 0, fmt.Sprintf(
				"computeCores=%d exceeds the per-node compute core headroom (%d) after drive placement",
				cores, perContainerCap), nil
		}
		if reason := hugepagesFitCheck(nodeHeadroom, nodeHugepagesMiB, count, cores, maxCoresPerContainer, hugepagesFor); reason != "" {
			return 0, 0, reason, nil
		}
		if count*cores < t {
			return 0, 0, fmt.Sprintf(
				"compute:drive core ratio not met: %d compute containers × %d cores = %d compute cores < the %d compute "+
					"core(s) required by the compute:drive ratio (at least 1 per drive core); "+
					"increase computeContainers or computeCores, or remove them to enable auto-derivation",
				count, cores, count*cores, t), nil
		}
		return count, cores, "", warnings

	case specCores != 0:
		// Cores set, count unset: honor cores exactly, derive count against the floor, then check the cap —
		// it depends on which `count` nodes end up in play, not a global figure.
		cores = specCores
		if cores <= 0 {
			return 0, 0, "no compute core headroom on the compute nodes after drive placement", nil
		}
		count = max(floor, util.CeilDiv(t, cores))
		if count > d {
			return 0, 0, fmt.Sprintf(
				"cannot satisfy the compute:drive ratio: need %d compute containers of %d cores but only %d compute nodes",
				count, cores, d), nil
		}
		perContainerCap := topNMin(nodeHeadroom, count, maxCoresPerContainer)
		if perContainerCap <= 0 {
			return 0, 0, "no compute core headroom on the compute nodes after drive placement", nil
		}
		if cores > perContainerCap {
			return 0, 0, fmt.Sprintf(
				"computeCores=%d exceeds the per-node compute core headroom (%d) after drive placement",
				cores, perContainerCap), nil
		}
		if reason := hugepagesFitCheck(nodeHeadroom, nodeHugepagesMiB, count, cores, maxCoresPerContainer, hugepagesFor); reason != "" {
			return 0, 0, reason, nil
		}
		return count, cores, "", warnings

	default:
		// Neither set: minimize count subject to one-per-node fit and the required cores. For candidate n,
		// cores=ceil(t/n) and the n largest-headroom nodes (topNMin) must host that many; not a closed-form
		// cap, hence a linear scan from floor..d (d is small). hugepagesFor is non-increasing in n, so
		// skipping an n that fails hugepages naturally prefers more, smaller containers.
		sortedHeadroom := append([]int(nil), nodeHeadroom...)
		sort.Sort(sort.Reverse(sort.IntSlice(sortedHeadroom)))

		found := false
		for n := max(floor, 1); n <= d; n++ {
			c := max(1, util.CeilDiv(t, n))
			capVal := topNMinSorted(sortedHeadroom, n, maxCoresPerContainer)
			if capVal < c {
				continue
			}
			if hugepagesFor != nil {
				hp := hugepagesFor(n, c)
				if nodesFitting(nodeHeadroom, nodeHugepagesMiB, c, hp, maxCoresPerContainer) < n {
					continue // cores fit but hugepages don't at this n — try more, smaller containers
				}
			}
			count, cores, found = n, c, true
			break
		}
		if found {
			return count, cores, "", warnings
		}
		if d == 0 || topNMinSorted(sortedHeadroom, d, maxCoresPerContainer) <= 0 {
			return 0, 0, "no compute core headroom on the compute nodes after drive placement", nil
		}
		// No n reached the requirement — report against n=d (most permissive), diagnosing which dimension
		// actually failed there rather than always naming both (misleading if only hugepages were short).
		capAll := topNMinSorted(sortedHeadroom, d, maxCoresPerContainer)
		cAtD := max(1, util.CeilDiv(t, d))
		neededContainers := util.CeilDiv(t, max(capAll, 1))
		coresFit := capAll >= cAtD
		if hugepagesFor == nil {
			return 0, 0, fmt.Sprintf(
				"cannot satisfy the compute:drive ratio: need %d compute containers but only %d compute nodes (max %d compute cores < the %d required compute core(s))",
				neededContainers, d, d*capAll, t), nil
		}
		// nil nodeHugepagesMiB disables the check, matching other call sites.
		hugepagesFit := true
		hpFitAlone := d
		hpAtD := 0
		if nodeHugepagesMiB != nil {
			hpAtD = hugepagesFor(d, cAtD)
			hpFitAlone = 0
			for _, h := range nodeHugepagesMiB {
				if h >= hpAtD {
					hpFitAlone++
				}
			}
			hugepagesFit = hpFitAlone >= d
		}
		switch {
		case !coresFit && !hugepagesFit:
			return 0, 0, fmt.Sprintf(
				"cannot satisfy the compute:drive ratio: neither compute cores nor hugepages suffice for %d compute containers "+
					"across %d compute nodes (max %d compute cores < the %d required compute core(s); only %d of %d nodes meet the "+
					"%d MiB/container hugepages bar)",
				d, d, d*capAll, t, hpFitAlone, d, hpAtD), nil
		case !coresFit:
			return 0, 0, fmt.Sprintf(
				"cannot satisfy the compute:drive ratio: need %d compute containers but only %d compute nodes (max %d compute "+
					"cores < the %d required compute core(s); hugepages are sufficient)",
				neededContainers, d, d*capAll, t), nil
		case !hugepagesFit:
			return 0, 0, fmt.Sprintf(
				"cannot satisfy the compute:drive ratio: hugepages insufficient for %d compute containers across %d compute "+
					"nodes (only %d of %d nodes meet the %d MiB/container bar at %d cores/container; compute cores are sufficient)",
				d, d, hpFitAlone, d, hpAtD, cAtD), nil
		default:
			// Unreachable in practice (the scan already tries n=d); kept as a defensive fallback so a
			// latent scan bug never surfaces a blank message.
			return 0, 0, fmt.Sprintf(
				"cannot satisfy the compute:drive ratio: need %d compute containers but only %d compute nodes (max %d compute "+
					"cores < the %d required compute core(s)) [unexpected: cores and hugepages both fit at n=%d]",
				neededContainers, d, d*capAll, t, d), nil
		}
	}
}

// computeFDFeasibility returns a non-empty reason when the compute layout (coveredFDs from pinned existing
// computes, plus newNodes resolved via fdOf) would span fewer than minFds distinct failure domains — Weka
// refuses to initialize otherwise, so fail fast here instead of after containers are created.
func computeFDFeasibility(minFds int, coveredFDs map[string]struct{}, newNodes []string, fdOf func(node string) string) string {
	fds := map[string]struct{}{}
	for fd := range coveredFDs {
		fds[fd] = struct{}{}
	}
	for _, n := range newNodes {
		fds[fdOf(n)] = struct{}{}
	}
	if len(fds) < minFds {
		return fmt.Sprintf(
			"compute spans only %d of %d required failure domains (minFdNum = stripeWidth+redundancyLevel+hotSpare); "+
				"add compute-eligible nodes in more distinct failure domains",
			len(fds), minFds)
	}
	return ""
}

// unboundedComputeHeadroom is a per-node headroom large enough to never bind, so ComputeLayoutWouldGrow
// derives the compute target under the policy cap alone (real headroom can only shrink that target).
const unboundedComputeHeadroom = 1 << 30

// ComputeLayoutWouldGrow is the steady-state skip gate: reports whether clusterCapacity's compute
// derivation could require more than the current healthy set (curCount containers, curMinCores cores each),
// assuming unbounded per-node headroom (real, finite headroom can only lower the target). True when there's
// no current compute, it's infeasible at curCount, or it needs more containers/cores than the current set has.
func ComputeLayoutWouldGrow(specCount, specCores, requiredComputeCores, floor, maxCoresPerContainer, curCount, curMinCores, curTotalCores int) bool {
	if curCount <= 0 {
		return true
	}
	headroom := make([]int, curCount)
	for i := range headroom {
		headroom[i] = unboundedComputeHeadroom
	}
	// nil, nil: hugepages-awareness can only push the answer to a larger n (never lowers total cores below
	// count*cores >= t), so omitting it here can only cause more re-plans, never an incorrectly skipped one.
	count, cores, infeasible, _ := deriveComputeLayout(specCount, specCores, requiredComputeCores, floor, maxCoresPerContainer, headroom, nil, nil)
	if infeasible != "" {
		return true
	}
	if count > curCount {
		return true // need more containers than exist
	}
	// A heterogeneous layout (frozen compute below target, compensated by extra containers) can leave the
	// smallest compute below `cores` forever — steady state, not growth, as long as total cores already
	// cover the requirement. Only applies when cores are auto-derived; a pinned specCores must be reached
	// by every container.
	if cores > curMinCores {
		if specCores == 0 {
			return curTotalCores < requiredComputeCores
		}
		return true
	}
	return false
}

// orderNodesByFDSpread reorders `nodes` so a prefix spans as many distinct FDs as possible before
// repeating one: group by FDValue (fdOf), rank groups by best member (headroom) desc, then round-robin
// across groups. A plain best-fit ordering could pile the first picks onto one FD, collapsing the chosen
// prefix into fewer distinct FDs than needed; round-robin guarantees the first k picks cover min(k, #FDs).
func orderNodesByFDSpread(nodes []string, headroom func(node string) int, fdOf func(node string) string) []string {
	type fdGroup struct {
		fd    string
		nodes []string // members, headroom desc
	}
	byFD := map[string]*fdGroup{}
	groups := make([]*fdGroup, 0, len(nodes))
	for _, n := range nodes {
		g := byFD[fdOf(n)]
		if g == nil {
			g = &fdGroup{fd: fdOf(n)}
			byFD[fdOf(n)] = g
			groups = append(groups, g)
		}
		g.nodes = append(g.nodes, n)
	}
	for _, g := range groups {
		sort.SliceStable(g.nodes, func(i, j int) bool {
			hi, hj := headroom(g.nodes[i]), headroom(g.nodes[j])
			if hi != hj {
				return hi > hj
			}
			return g.nodes[i] < g.nodes[j]
		})
	}
	sort.SliceStable(groups, func(i, j int) bool {
		hi, hj := headroom(groups[i].nodes[0]), headroom(groups[j].nodes[0])
		if hi != hj {
			return hi > hj
		}
		return groups[i].fd < groups[j].fd
	})
	out := make([]string, 0, len(nodes))
	for round := 0; len(out) < len(nodes); round++ {
		for _, g := range groups {
			if round < len(g.nodes) {
				out = append(out, g.nodes[round])
			}
		}
	}
	return out
}
