package capacityplanner

// autofulldrives_fit.go decides the single question the auto-full-drives node walk asks of a node: can it
// take a drive container of a given size? There is no partial answer — drives are never dropped to make a
// container fit, so a "no" here becomes a plan-wide infeasibility.

// The three dimensions a drive container can be short on, spelled with the InfeasibilityReport.Binding
// vocabulary so the structured report needs no translation table.
const (
	bindingCores     = "cores"
	bindingHugepages = "hugepages"
	bindingMemory    = "memory"
)

// fitKindCreate and fitKindGrowth classify an autoFitFailure: a create failure has no existing container to
// grow from, a growth failure does. Constants, not string literals, so a typo can't silently disable the
// growth-hazard diagnostic that keys off this value.
const (
	fitKindCreate = "create"
	fitKindGrowth = "growth"
)

// autoFootprint is what a drive container is on a node: the cores it runs and the drives it holds. The zero
// value means "nothing there yet", which is the create path.
type autoFootprint struct {
	cores  int
	drives int
}

// autoFitCost is what a footprint change costs a node, per dimension. Named fields rather than a
// string-keyed list: there are exactly three, they never vary, and chargeFit is then three subtractions.
type autoFitCost struct {
	cpu          int
	hugepagesMiB int
	memoryMiB    int
}

// autoFitResult is autoNodeFit's verdict: the delta it charged, and — when it does not fit — the dimension
// with the largest relative deficit, ready for a NodeRejection.
type autoFitResult struct {
	ok   bool
	cost autoFitCost

	binding   string
	unit      string
	needed    int
	available int
}

// autoNodeFit decides whether node can host a drive container moving from `from` to `to`, charging only the
// delta: `from` is the zero value on create, else the existing container's size (already netted out of node
// headroom). Hugepages charge per core and per drive, so more drives at the same core count still costs
// headroom; CPU and memory are functions of cores alone. Compute correctness lives in planComputeAutoFullDrives.
func autoNodeFit(node *NodeCapacity, from, to autoFootprint, cons *CapacityConstraints) autoFitResult {
	oldCPU, oldHugepagesMiB, oldMemoryMiB := 0, 0, 0
	if from.cores > 0 || from.drives > 0 {
		// DriveContainerHugepagesMiB charges a per-drive term independently of cores, and the inventory has
		// already netted the existing container's footprint out of node headroom — so a container with
		// cores==0 and drives>0 must still credit its old footprint back, or its per-drive term is charged
		// twice.
		oldHugepagesMiB = DriveContainerHugepagesMiB(from.cores, from.drives, cons)
		oldMemoryMiB = ComputeMemoryFootprintMiB(from.cores, cons)
		oldCPU = physicalCPUCost(node, from.cores, cons, true)
	}
	newHugepagesMiB := DriveContainerHugepagesMiB(to.cores, to.drives, cons)
	newMemoryMiB := ComputeMemoryFootprintMiB(to.cores, cons)

	res := autoFitResult{ok: true, cost: autoFitCost{
		cpu:          max(physicalCPUCost(node, to.cores, cons, true)-oldCPU, 0),
		hugepagesMiB: max(newHugepagesMiB-oldHugepagesMiB, 0),
		memoryMiB:    max(newMemoryMiB-oldMemoryMiB, 0),
	}}

	// Tightest relative deficit wins: an absolute comparison would always name hugepages/memory (thousands of
	// MiB) over CPU (single digits) regardless of which is actually the harder wall to move.
	worst := -1.0
	for _, d := range []struct {
		binding, unit     string
		needed, available int
	}{
		{bindingCores, "physical CPU", res.cost.cpu, node.AllocatableCPU},
		{bindingHugepages, "MiB hugepages", res.cost.hugepagesMiB, node.AvailableHugepagesMiB},
		{bindingMemory, "MiB memory", res.cost.memoryMiB, node.AvailableMemoryMiB},
	} {
		// needed <= 0 short-circuits before the comparison: an over-committed node can report negative
		// availability, and `0 <= -100` is false — which would turn a zero-delta reconcile (nothing about this
		// container changed) into a fleet-wide infeasibility. Charging nothing always fits.
		if d.needed <= 0 || d.needed <= d.available {
			continue
		}
		res.ok = false
		// available == 0 is an infinite relative deficit; guard the divisor and let the larger `needed` win.
		if rel := float64(d.needed) / float64(max(d.available, 1)); rel > worst {
			worst = rel
			res.binding, res.unit, res.needed, res.available = d.binding, d.unit, d.needed, d.available
		}
	}
	return res
}

// chargeFit subtracts an accepted fit's cost from a node's remaining headroom, in place, so the compute step
// sizes against what the drive containers actually leave behind.
func chargeFit(nc *NodeCapacity, cost autoFitCost) {
	nc.AllocatableCPU -= cost.cpu
	nc.AvailableHugepagesMiB -= cost.hugepagesMiB
	nc.AvailableMemoryMiB -= cost.memoryMiB
}

// autoFitFailure is one node that cannot host the container its own drive set implies. Collected across the
// whole walk so the infeasibility names every offender rather than the first one reached.
type autoFitFailure struct {
	node      string
	kind      string // fitKindCreate | fitKindGrowth
	numDrives int
	toCores   int
	fit       autoFitResult
	// ownCompute is whether this cluster runs a compute container on the node, which is what makes the
	// growth-hazard diagnostic applicable — its remedy is to delete that container.
	ownCompute bool
}
