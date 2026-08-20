package validation

import (
	"context"
	"fmt"
	"sort"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/capacityplanner"
	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/pkg/util"
)

// clusterAutoFullDrivesComputeHugepages rejects an auto-full-drives cluster whose compute containers
// can never be placed, because the hugepages each one needs exceeds what any compute-eligible node has.
//
// Decidable at admission because nothing here is negotiable: every signed drive is claimed, so total
// TLC capacity is fixed by the hardware, and each node's drive cores are min(effectiveDriveCount,
// maxCoresPerContainer) or the driveCores pin — never traded away to make compute fit. So a compute
// container's hugepages share (totalTlcMiB / computeHugepagesTlcRatio / containerCount) and the core
// requirement on top of it are both fixed the moment the drives are signed. If no container count fits,
// nothing is ever created.
//
// The only free variable is the container count, swept from the form-cluster floor up to the
// compute-eligible node count (compute spreads one per node), taking the fewest cores that still cover
// the requirement; one fitting count anywhere admits the cluster. Mirrors the worked example in
// doc/operator/deployment/act-as-daemonset.md — the two must agree number for number.
//
// Hyperconverged nodes are charged for their own drive container: on a node that is also a signed
// drive-role node, cores × (HugepagesPerCoreMiB + DriveDpdkPerCoreMiB) comes off allocatable before the
// remainder is offered to compute. Skipping that would over-state headroom on exactly the fleet shape
// this mode is built for.
//
// Headroom is read as ALLOCATABLE hugepages-2Mi, like clusterHugepagesAvailable, so foreign pods are
// not subtracted — over-stating what is free is the safe direction for an Error policy. Also skipped
// when the form-cluster floor already exceeds the compute-eligible node count: a distinct
// infeasibility this message would misattribute to hugepages.
type clusterAutoFullDrivesComputeHugepages struct{}

func (clusterAutoFullDrivesComputeHugepages) ID() string {
	return "cluster_auto_full_drives_compute_hugepages"
}

func (clusterAutoFullDrivesComputeHugepages) Validate(ctx context.Context, c client.Client, obj runtime.Object) field.ErrorList {
	cluster, ok := obj.(*weka.WekaCluster)
	if !ok {
		return nil
	}
	// Nil dynamicTemplate is auto-full-drives mode (nothing was set), so no nil guard here.
	config := cluster.Spec.Dynamic
	if !config.UsesAutoFullDrives() {
		return nil
	}

	fldPath := field.NewPath("spec", "dynamicTemplate")
	cons := allocator.ConstraintsForClusterSpec(&cluster.Spec)

	claim, errs := projectAutoFullDrivesClaim(ctx, c, cluster, config, cons, fldPath)
	if errs != nil {
		return errs
	}
	claimedTlcGiB, driveCores, annotatedNodes := claim.tlcGiB, claim.driveCores, claim.annotatedNodes
	if annotatedNodes == 0 || claimedTlcGiB <= 0 || driveCores <= 0 {
		return nil // pre-signing state; clusterDrivesUnsignedAdvisory owns it
	}

	freeMiB, maxAllocatableMiB, errs := computeEligibleHugepagesMiB(ctx, c, cluster, claim.driveHugepagesByNode, fldPath)
	if errs != nil {
		return errs
	}
	if len(freeMiB) == 0 {
		return nil
	}
	// Descending, so "the first n nodes" is the most generous set of size n.
	sort.Sort(sort.Reverse(sort.IntSlice(freeMiB)))
	largestFreeMiB := freeMiB[0]

	requiredComputeCores := capacityplanner.RequiredComputeCores(driveCores, 0, true, cons)
	// deriveComputeLayout sweeps n from the form-cluster floor up to the placeable node count and
	// rejects anything above it ("compute spreads one-per-node"), so those are this search's bounds.
	minCount := max(1, cons.MinComputeContainers)
	if minCount > len(freeMiB) {
		// The floor alone is unreachable — a distinct infeasibility that has nothing to do with
		// hugepages, and this message would misattribute it. clusterAutoFullDrivesMinNodes owns it,
		// and reports on the same apply.
		return nil
	}

	// A pinned computeCores takes deriveComputeLayout's specCores branch at runtime, not its default
	// sweep: cores is honored EXACTLY and count is derived from it, rather than cores being derived
	// from a swept count. autoFullDrivesComputeHugepagesMiB below does the opposite (derives cores
	// from count), so it would silently ignore the pin; this is a distinct code path, not an addition
	// to the sweep.
	if config != nil && config.ComputeCores > 0 {
		return validateAutoFullDrivesPinnedComputeCores(
			config, claim, cons, fldPath, claimedTlcGiB, driveCores, annotatedNodes,
			requiredComputeCores, minCount, freeMiB, maxAllocatableMiB,
		)
	}

	// A (count, cores) pair fits when the `count` most-capacious nodes each clear the per-container
	// requirement. Cores are the smallest that still cover requiredComputeCores across `count`
	// containers — larger cores only raise hugepages, so the minimum is the best case per count.
	// Swept rather than evaluated at count == len(freeMiB) alone: on a heterogeneous fleet a smaller
	// count can fit where the full spread does not, since fewer nodes have to clear the bar.
	coresFit := false
	for count := minCount; count <= len(freeMiB); count++ {
		perContainerMiB, ok := autoFullDrivesComputeHugepagesMiB(claimedTlcGiB, requiredComputeCores, count, cons)
		if !ok {
			continue // too few containers to carry the cores; a larger count may still work
		}
		coresFit = true
		if freeMiB[count-1] >= perContainerMiB {
			return nil
		}
	}

	// Infeasible. Quantify against the most generous configuration available: every compute-eligible
	// node hosting one container.
	bestCount := len(freeMiB)
	if !coresFit {
		// Not a hugepages shortfall at all: even one container per compute-eligible node, each at the
		// per-container core cap, cannot carry the required cores. Saying "the most any node has free is
		// N MiB" here would name the wrong binding resource and send the operator after memory.
		return field.ErrorList{field.Invalid(fldPath, "auto-full-drives", fmt.Sprintf(
			"%s Compute spreads one container per node, at most %d core(s) each, so the "+
				"%d compute-eligible node(s) top out at %d compute core(s) — short of the requirement no "+
				"matter how much memory they have. The planner reports the plan infeasible "+
				"(AutoFullDrivesInfeasible) and creates nothing. Remedies: label more nodes for "+
				"spec.roleNodeSelector.compute, or spec.nodeSelector when that is unset; raise the "+
				"maxCoresPerContainer Helm value (currently %d); %sor pin spec.dynamicTemplate.numDrives "+
				"lower so each node contributes less capacity.",
			autoFullDrivesClaimPreamble(claimedTlcGiB, annotatedNodes, driveCores, requiredComputeCores),
			cons.MaxCoresPerContainer, bestCount, bestCount*cons.MaxCoresPerContainer,
			cons.MaxCoresPerContainer, driveCoresRemedyText(config, claim),
		))}
	}
	// coresFit is true, so the largest count is core-feasible: ceil(required/count) is non-increasing in
	// count, so whichever count cleared the cap, bestCount clears it too.
	requiredMiB, _ := autoFullDrivesComputeHugepagesMiB(claimedTlcGiB, requiredComputeCores, bestCount, cons)

	sufficient := autoFullDrivesSufficientComputeNodes(claimedTlcGiB, requiredComputeCores, largestFreeMiB, minCount, cons)
	sufficientText := fmt.Sprintf("%d compute-eligible node(s) of that size would be needed", sufficient)
	if sufficient == 0 {
		sufficientText = "no number of nodes of that size is enough — even a single-core compute container " +
			"does not fit, so the shortfall is per-node hugepages, not node count"
	}

	// When the cap binds, the base is already clamped and raising the ratio changes nothing; say so
	// rather than listing a remedy that cannot help.
	capNote := ""
	if cons.ComputeMaxHugepagesMiB > 0 && requiredMiB >= cons.ComputeMaxHugepagesMiB {
		capNote = " (note: the per-container base is already clamped at computeMaxHugepagesMiB, so " +
			"raising computeHugepagesTlcRatio will not move it — lowering the cap is the lever that does)"
	}

	// Lowering driveCores is the operator's most direct lever now that the planner will not reduce
	// cores on its own, and it costs zero drives — but only when the PER-CORE term is what binds. The
	// floor of the per-container figure is its capacity share plus a single core; if even that clears
	// the largest node outright, no core reduction of any size can rescue the fleet, and offering it
	// as a remedy would send the operator down a dead end.
	floorAtOneCore := capacityplanner.ComputeContainerHugepagesMiB(claimedTlcGiB, 0, bestCount, 1, cons)
	driveCoresRemedy := driveCoresRemedyText(config, claim)
	if floorAtOneCore > maxAllocatableMiB {
		driveCoresRemedy = ""
		capNote += fmt.Sprintf(
			" Lowering driveCores will NOT help here: at %d container(s) the capacity share alone is "+
				"%d MiB even at one compute core, above the %d MiB the largest compute-eligible node has "+
				"allocatable — the binding term is capacity, not cores.",
			bestCount, floorAtOneCore, maxAllocatableMiB,
		)
	}

	// Remedies mirror the planner's runtime fixesAutoFullDrivesCompute, in the same order, so
	// admission and the infeasibility report say the same thing. There is deliberately no
	// computeContainers lever: setting one takes the cluster out of this mode entirely.
	detail := fmt.Sprintf(
		"%s Spread over all %d compute-eligible node(s) that is %d MiB of hugepages "+
			"per compute container, but the most any compute-eligible node has free is %d MiB, and %s. "+
			"Drive cores are never reduced to make compute fit, so no container-count/core combination "+
			"fits: the plan is reported infeasible (AutoFullDrivesInfeasible) and no container is ever "+
			"created.%s Remedies: add compute-eligible nodes (label more nodes for "+
			"spec.roleNodeSelector.compute, or spec.nodeSelector when that is unset); %sraise the "+
			"hugepagesTlcRatio Helm value (currently %d) so each GiB of capacity costs less hugepages; "+
			"lower the computeMaxHugepagesMiB Helm value (currently %d) to cap the per-container "+
			"request; or pin spec.dynamicTemplate.numDrives lower so each node contributes less "+
			"capacity.",
		autoFullDrivesClaimPreamble(claimedTlcGiB, annotatedNodes, driveCores, requiredComputeCores),
		bestCount, requiredMiB, largestFreeMiB, sufficientText, capNote, driveCoresRemedy,
		cons.ComputeHugepagesTlcRatio, cons.ComputeMaxHugepagesMiB,
	)
	return field.ErrorList{field.Invalid(fldPath, "auto-full-drives", detail)}
}

// validateAutoFullDrivesPinnedComputeCores mirrors deriveComputeLayout's specCores branch for a
// pinned spec.dynamicTemplate.computeCores in auto-full-drives mode: cores is honored exactly (never
// derived from count, unlike the unpinned sweep), count is the smallest that still meets
// requiredComputeCores at that fixed core size — max(floor, ceil(required/cores)) — and the pair is
// infeasible when that count exceeds the compute-eligible node count or the count-th most generous
// node cannot hold its hugepages. Same hugepages-only headroom fidelity as the unpinned sweep above
// (see the file doc comment): per-node core headroom is not part of either check.
//
// Not mirrored: deriveComputeLayout also caps cores against topNMin(nodeHeadroom, count,
// maxCoresPerContainer), the weakest of the chosen count nodes' REAL per-node core headroom, derived
// from raw CPU allocatable plus HT/FullPcpusOnly topology via capacityplanner's unexported
// physicalCPUToDataCores/dataCoresCapacityShared. This validator has never fetched that data and
// cannot call those functions from outside the capacityplanner package; reimplementing the
// conversion here would duplicate CPU accounting this file does not own and risk drifting from it.
// clusterCoresPerContainerLimit already rejects computeCores above the global maxCoresPerContainer
// cap in every mode, and clusterCoresAvailable checks a pin against raw per-node CPU for other
// roles — but its containers>0 guard never fires for AFD-mode compute (computeContainers is always 0
// there), so the real per-node core fit specifically is left to the planner at reconcile time
// (AutoFullDrivesInfeasible) rather than caught here.
func validateAutoFullDrivesPinnedComputeCores(
	config *weka.WekaClusterTemplate,
	claim autoFullDrivesClaim,
	cons *capacityplanner.CapacityConstraints,
	fldPath *field.Path,
	claimedTlcGiB, driveCores, annotatedNodes, requiredComputeCores, minCount int,
	freeMiB []int,
	maxAllocatableMiB int,
) field.ErrorList {
	cores := config.ComputeCores
	count := max(minCount, util.CeilDiv(requiredComputeCores, cores))

	if count > len(freeMiB) {
		detail := fmt.Sprintf(
			"%s With spec.dynamicTemplate.computeCores pinned at %d, that takes %d "+
				"compute container(s) (one per node), but only %d node(s) are compute-eligible. The planner "+
				"reports the plan infeasible (AutoFullDrivesInfeasible) and creates nothing. Remedies: add "+
				"compute-eligible nodes (label more nodes for spec.roleNodeSelector.compute, or "+
				"spec.nodeSelector when that is unset); raise computeCores so fewer containers are "+
				"needed; unpin computeCores to let it auto-derive; %sor pin "+
				"spec.dynamicTemplate.numDrives lower so each node contributes less capacity.",
			autoFullDrivesClaimPreamble(claimedTlcGiB, annotatedNodes, driveCores, requiredComputeCores),
			cores, count, len(freeMiB),
			driveCoresRemedyText(config, claim),
		)
		return field.ErrorList{field.Invalid(fldPath, "auto-full-drives", detail)}
	}

	perContainerMiB := capacityplanner.ComputeContainerHugepagesMiB(claimedTlcGiB, 0, count, cores, cons)
	// Descending, so index count-1 is the count-th most generous node — same convention as the
	// unpinned sweep above.
	if freeMiB[count-1] >= perContainerMiB {
		return nil
	}

	detail := fmt.Sprintf(
		"%s With spec.dynamicTemplate.computeCores pinned at %d, that takes %d compute "+
			"container(s), each needing %d MiB of hugepages, but the %d-th most generous compute-eligible "+
			"node has only %d MiB free after drive placement (the largest has %d MiB allocatable). The "+
			"planner reports the plan infeasible (AutoFullDrivesInfeasible) and creates nothing. Remedies: "+
			"add compute-eligible nodes (label more nodes for spec.roleNodeSelector.compute, or "+
			"spec.nodeSelector when that is unset); unpin spec.dynamicTemplate.computeCores to let it "+
			"auto-derive; raise the hugepagesTlcRatio Helm "+
			"value (currently %d) so each GiB of capacity costs less hugepages; lower the "+
			"computeMaxHugepagesMiB Helm value (currently %d) to cap the per-container request; %sor pin "+
			"spec.dynamicTemplate.numDrives lower so each node contributes less capacity.",
		autoFullDrivesClaimPreamble(claimedTlcGiB, annotatedNodes, driveCores, requiredComputeCores),
		cores, count, perContainerMiB,
		count, freeMiB[count-1], maxAllocatableMiB,
		cons.ComputeHugepagesTlcRatio, cons.ComputeMaxHugepagesMiB, driveCoresRemedyText(config, claim),
	)
	return field.ErrorList{field.Invalid(fldPath, "auto-full-drives", detail)}
}

// autoFullDrivesClaimPreamble is the sentence every infeasibility message below opens with: what the
// signed-drive claim commits the cluster to before compute is placed — total TLC, the drive-role nodes
// it came from, the drive cores that run it, and the compute cores that requirement translates to.
// Extracted so the four emission sites cannot drift from each other one word at a time; it ends in a
// period with no trailing space, so callers splice it in with "%s " followed by their own continuation.
func autoFullDrivesClaimPreamble(claimedTlcGiB, annotatedNodes, driveCores, requiredComputeCores int) string {
	return fmt.Sprintf(
		"auto-full-drives mode claims every signed drive, so this cluster would hold %d GiB of TLC "+
			"capacity across %d drive-role node(s), running on %d drive core(s) and therefore needing "+
			"%d compute core(s).",
		claimedTlcGiB, annotatedNodes, driveCores, requiredComputeCores,
	)
}

// driveCoresRemedyText renders the "lower driveCores" remedy sentence. When the operator pinned
// driveCores, the pin is a single number that applies to every node, so it is named directly. When it
// is unpinned, driveCores is derived per node from that node's own drive count, and on the
// heterogeneous fleets this mode exists to serve there is no single per-node figure to name — averaging
// claim.driveCores across claim.annotatedNodes would describe a value no actual node has, and calling
// it "current" would misrepresent a field that is unset. So the derived case reports the two real
// totals instead and lets the reader see how they relate, rather than fabricating a per-node average.
func driveCoresRemedyText(config *weka.WekaClusterTemplate, claim autoFullDrivesClaim) string {
	const tail = " — every drive is still claimed, just run on fewer cores, which cuts both the " +
		"compute-core requirement and each node's own drive-container reservation; "
	if config != nil && config.DriveCores > 0 {
		return fmt.Sprintf("lower spec.dynamicTemplate.driveCores (currently %d per node)%s", config.DriveCores, tail)
	}
	if claim.annotatedNodes == 0 {
		return ""
	}
	return fmt.Sprintf(
		"pin spec.dynamicTemplate.driveCores — it is currently derived per node from each node's drive "+
			"count (%d core(s) across %d node(s))%s",
		claim.driveCores, claim.annotatedNodes, tail,
	)
}

// autoFullDrivesComputeHugepagesMiB is one compute container's hugepages at `count` containers, using
// the fewest cores that still cover requiredComputeCores. Cores only ever raise the figure, so this is
// the best case for that count. Delegates the arithmetic to the planner so the two never drift.
//
// ok is false when `count` containers cannot carry requiredComputeCores at all, because the per-container
// share exceeds MaxCoresPerContainer. Clamping to the cap and reporting the clamped figure as a fit would
// price a layout that under-delivers cores no matter how much hugepages headroom the nodes have:
// deriveComputeLayout skips exactly those candidates (capVal < c) and ends in "cannot satisfy the
// compute:drive ratio", so treating one as feasible here admits a plan the planner never builds.
func autoFullDrivesComputeHugepagesMiB(claimedTlcGiB, requiredComputeCores, count int, cons *capacityplanner.CapacityConstraints) (hugepagesMiB int, ok bool) {
	cores := max(1, util.CeilDiv(requiredComputeCores, count))
	if cons.MaxCoresPerContainer > 0 && cores > cons.MaxCoresPerContainer {
		return 0, false
	}
	return capacityplanner.ComputeContainerHugepagesMiB(claimedTlcGiB, 0, count, cores, cons), true
}

// autoFullDrivesSufficientComputeNodes returns the smallest compute-container count whose per-container
// hugepages fit in perNodeMiB, i.e. how many nodes of the fleet's best size would be enough. Returns 0
// when no count works — the per-container figure is floored by the per-core terms, so a node too small
// for a 1-core container can never be satisfied by adding more of them.
func autoFullDrivesSufficientComputeNodes(claimedTlcGiB, requiredComputeCores, perNodeMiB, minCount int, cons *capacityplanner.CapacityConstraints) int {
	// Bounded: past requiredComputeCores containers the cores term is pinned at 1 and the capacity
	// share only shrinks, so if nothing has fit by then, nothing will.
	limit := max(minCount, requiredComputeCores) + 1
	for count := max(1, minCount); count <= limit; count++ {
		hugepagesMiB, ok := autoFullDrivesComputeHugepagesMiB(claimedTlcGiB, requiredComputeCores, count, cons)
		if !ok {
			continue // too few containers to carry the cores, whatever the memory
		}
		if hugepagesMiB <= perNodeMiB {
			return count
		}
	}
	return 0
}

// autoFullDrivesClaim is what the fleet's signed drives commit the cluster to, before compute is
// placed: total claimed TLC, the drive cores that runs on, how many nodes contributed, and each
// contributing node's own drive-container hugepages reservation.
type autoFullDrivesClaim struct {
	tlcGiB         int
	driveCores     int
	annotatedNodes int
	// driveHugepagesByNode: node name -> MiB its own drive container reserves. Charged against that
	// node's headroom when it is also compute-eligible (hyperconverged).
	driveHugepagesByNode map[string]int
}

// projectAutoFullDrivesClaim totals what the cluster would claim: every signed non-blocked full drive
// on every drive-role node, or the numDrives largest when pinned.
//
// Drive cores are the FULL derived count — the driveCores pin, else
// min(effectiveDriveCount, maxCoresPerContainer). They are never projected at a reduced value: there
// is no co-sizing search to descend, and cores are never traded away to make compute fit. Projecting
// anything lower would under-state the compute requirement and admit a cluster the planner
// immediately declares infeasible, which is precisely what this policy exists to prevent.
func projectAutoFullDrivesClaim(
	ctx context.Context,
	c client.Client,
	cluster *weka.WekaCluster,
	config *weka.WekaClusterTemplate,
	cons *capacityplanner.CapacityConstraints,
	fldPath *field.Path,
) (autoFullDrivesClaim, field.ErrorList) {
	claim := autoFullDrivesClaim{driveHugepagesByNode: map[string]int{}}

	nodes, errs := listDriveRoleNodes(ctx, c, cluster, fldPath)
	if errs != nil {
		return claim, errs
	}
	if len(nodes) == 0 {
		return claim, nil
	}
	infos, errs := driveRoleNodeInfos(nodes, fldPath)
	if errs != nil {
		return claim, errs
	}

	var pinnedNumDrives, pinnedDriveCores int
	if config != nil {
		pinnedNumDrives, pinnedDriveCores = config.NumDrives, config.DriveCores
	}
	for _, ni := range infos {
		if _, full := ni.Node.Annotations[consts.AnnotationWekaFullDrives]; !full {
			continue
		}
		caps := make([]int, 0, len(ni.Info.AvailableDrives))
		for _, d := range ni.Info.AvailableDrives {
			caps = append(caps, d.CapacityGiB)
		}
		if len(caps) == 0 {
			continue
		}
		claim.annotatedNodes++

		// Largest-first, so a numDrives pin takes the biggest drives — what the planner does.
		sort.Sort(sort.Reverse(sort.IntSlice(caps)))
		effective := len(caps)
		if pinnedNumDrives > 0 {
			// A pin ABOVE the signed count is its own rejection
			// (clusterAutoFullDrivesPinExceedsNodeDrives); clamp so this check still reports on the
			// capacity the node could actually contribute.
			effective = min(pinnedNumDrives, len(caps))
		}
		for _, capGiB := range caps[:effective] {
			claim.tlcGiB += capGiB
		}

		nodeCores := pinnedDriveCores
		if nodeCores <= 0 {
			nodeCores = capacityplanner.FullDriveCores(effective, cons)
		}
		claim.driveCores += nodeCores
		// The planner's own formula, not a mirror of it: a drive container reserves per CORE *and* per DRIVE,
		// so a per-core-only figure under-reserves by 200 MiB per drive on any node holding more drives than
		// cores — which is exactly the shape this mode creates — and would report room for compute that the
		// drive container has already taken.
		claim.driveHugepagesByNode[ni.Node.Name] = capacityplanner.DriveContainerHugepagesMiB(nodeCores, effective, cons)
	}
	return claim, nil
}

// computeEligibleHugepagesMiB returns each compute-role node's hugepages-2Mi available to a COMPUTE
// container, in MiB: its allocatable, less the reservation of a drive container this cluster would
// place on the same node (hyperconverged nodes carry both). maxAllocatableMiB is the largest RAW
// allocatable across the same nodes, before any drive reservation — the absolute ceiling a compute
// container could ever have, used to tell a per-core shortfall from a capacity one.
func computeEligibleHugepagesMiB(
	ctx context.Context,
	c client.Client,
	cluster *weka.WekaCluster,
	driveHugepagesByNode map[string]int,
	fldPath *field.Path,
) (freeMiB []int, maxAllocatableMiB int, errs field.ErrorList) {
	selector := cluster.GetNodeSelectorForRole(weka.WekaContainerModeCompute)
	var nodes corev1.NodeList
	if err := c.List(ctx, &nodes, client.MatchingLabels(selector)); err != nil {
		return nil, 0, field.ErrorList{field.InternalError(fldPath, fmt.Errorf("listing compute-role nodes: %w", err))}
	}
	hpResource := corev1.ResourceName(string(corev1.ResourceHugePagesPrefix) + "2Mi")
	out := make([]int, 0, len(nodes.Items))
	for i := range nodes.Items {
		qty := nodes.Items[i].Status.Allocatable[hpResource]
		allocatable := int(qty.Value() / mib)
		maxAllocatableMiB = max(maxAllocatableMiB, allocatable)
		out = append(out, max(0, allocatable-driveHugepagesByNode[nodes.Items[i].Name]))
	}
	return out, maxAllocatableMiB, nil
}
