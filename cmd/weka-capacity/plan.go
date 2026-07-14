package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/capacityplanner"
	"github.com/weka/weka-operator/internal/capacityplanner/inventory"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/controllers/utils"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/pkg/util"
)

// newClusterDisplayName is the label shown in output for a --new-cluster (bool) dry-run, which has no
// live WekaCluster name to display.
const newClusterDisplayName = "new-cluster"

// planCommand dry-runs the capacity planner for a live WekaCluster with optional spec overrides, or for a
// hypothetical --new-cluster synthesized entirely from flags.
type planCommand struct {
	Cluster    string `long:"cluster" description:"WekaCluster name to plan for (in --namespace); mutually exclusive with --new-cluster"`
	NewCluster bool   `long:"new-cluster" description:"Plan for a hypothetical cluster that does not exist yet, synthesized from flags; mutually exclusive with --cluster"`

	NodeSelector string `long:"node-selector" description:"Node label selector (k=v[,k=v...]) for --new-cluster; which nodes the hypothetical cluster could land on. Optional — empty considers all nodes"`
	FDLabel      string `long:"fd-label" description:"Failure-domain label key for --new-cluster (label-based FD); default AUTO = one FD per host"`

	ClusterCapacity   string `long:"cluster-capacity" description:"Override dynamicTemplate.clusterCapacity (e.g. 11022TiB)"`
	DriveTypesRatio   string `long:"drive-types-ratio" description:"Override driveTypesRatio as tlc:qlc (e.g. 1:90)"`
	StripeWidth       *int   `long:"stripe-width" description:"Override stripeWidth"`
	Redundancy        *int   `long:"redundancy" description:"Override redundancyLevel"`
	HotSpare          *int   `long:"hot-spare" description:"Override hotSpare"`
	DriveContainers   *int   `long:"drive-containers" description:"Override dynamicTemplate.driveContainers"`
	ComputeContainers *int   `long:"compute-containers" description:"Override dynamicTemplate.computeContainers"`
	ComputeCores      *int   `long:"compute-cores" description:"Override dynamicTemplate.computeCores"`
	DriveCores        *int   `long:"drive-cores" description:"Override dynamicTemplate.driveCores"`

	Constraints constraintFlags `group:"Constraint overrides"`
}

type computeRow struct {
	Name             string `json:"name"` // container name; empty for creates (a new container has no name yet — it is identified by Node)
	Node             string `json:"node"`
	FromCores        int    `json:"fromCores"`
	ToCores          int    `json:"toCores"`
	FromHugepagesMiB int    `json:"fromHugepagesMiB"` // current hugepages on the node (0 for creates); HugepagesMiB is the target
	HugepagesMiB     int    `json:"hugepagesMiB"`
	Deferred         bool   `json:"deferred"`
}

// driveGrowRow is a text-only view of an in-place drive-container grow: the CURRENT (from) capacity/cores
// joined to the planner's target (to), so the renderer can show the transition the way compute grow does.
// It is NOT serialized to JSON — the JSON growDrive array stays the planner's ContainerGrowth (new targets
// only). From* come from the matching ExistingContainer (zero if the name is somehow unmatched).
type driveGrowRow struct {
	Name                                       string
	Node                                       string
	FromTlcGiB, ToTlcGiB, FromQlcGiB, ToQlcGiB int
	FromCores, ToCores                         int
}

type planData struct {
	Cluster         string
	ClusterCapacity string
	Ratio           string
	SW, RL, HS      int
	MinChunkGiB     int
	DesiredTlcRaw   int
	DesiredQlcRaw   int
	CurrentTlc      int
	CurrentQlc      int
	Plan            *allocator.CapacityPlan
	DriveGrow       []driveGrowRow
	ComputeCreate   []computeRow
	ComputeGrow     []computeRow
	Summary         string
}

func (cmd *planCommand) Execute(_ []string) error {
	if err := cmd.validate(); err != nil {
		return err
	}

	ctx := context.Background()
	c, err := newClient()
	if err != nil {
		return err
	}

	var (
		cluster     weka.WekaCluster
		own         []*weka.WekaContainer
		displayName string
	)
	if cmd.NewCluster {
		cluster, err = cmd.buildSyntheticCluster()
		if err != nil {
			return err
		}
		displayName = newClusterDisplayName
		// own stays nil — a synthetic cluster owns no containers; skip GetClusterContainersNoFieldIndex.
	} else {
		if getErr := c.Get(ctx, client.ObjectKey{Namespace: opts.Namespace, Name: cmd.Cluster}, &cluster); getErr != nil {
			return fmt.Errorf("loading WekaCluster %s/%s: %w", opts.Namespace, cmd.Cluster, getErr)
		}
		if cluster.Spec.Dynamic == nil {
			return fmt.Errorf("cluster %q has no dynamicTemplate (not a clusterCapacity cluster)", cmd.Cluster)
		}
		if applyErr := cmd.applyOverrides(&cluster); applyErr != nil {
			return applyErr
		}
		// The CLI uses a cache-less direct client with no field indexer, so it must NOT filter via the
		// metadata.ownerReferences.uid field index (the apiserver rejects that field selector for CRDs).
		own, err = discovery.GetClusterContainersNoFieldIndex(ctx, c, &cluster, "")
		if err != nil {
			return fmt.Errorf("listing cluster containers: %w", err)
		}
		displayName = cmd.Cluster
	}

	cons, err := loadConstraints(ctx, c, opts.OperatorNamespace, &cmd.Constraints)
	if err != nil {
		return err
	}
	// Per-role DPDK base memory from the cluster spec, exactly as planClusterCapacity feeds the planner.
	cons.DriveDpdkPerCoreMiB = utils.GetDpdkBaseMemoryMbByRole(&cluster.Spec, weka.WekaContainerModeDrive)
	cons.ComputeDpdkPerCoreMiB = utils.GetDpdkBaseMemoryMbByRole(&cluster.Spec, weka.WekaContainerModeCompute)
	// Cluster cpuPolicy (empty == auto), so the planner projects fresh containers' physical CPU the same
	// way the controller does. See funcs_fd_planning.go / cpu.go.
	cons.CpuPolicy = cluster.Spec.CpuPolicy

	s := allocator.ProtectionScheme{
		StripeWidth:     cluster.Spec.StripeWidth,
		RedundancyLevel: cluster.Spec.RedundancyLevel,
		HotSpare:        cluster.Spec.HotSpare,
	}
	capGiB, err := cluster.Spec.Dynamic.GetClusterCapacityGiB()
	if err != nil {
		return fmt.Errorf("parsing clusterCapacity: %w", err)
	}
	raw := allocator.RawCapacityGiB(capGiB, s.StripeWidth, s.RedundancyLevel, s.HotSpare)
	tlcRaw, qlcRaw := weka.GetTlcQlcCapacity(raw, cluster.Spec.Dynamic.DriveTypesRatio)
	desired := allocator.DesiredCapacity{
		TlcRawGiB:         tlcRaw,
		QlcRawGiB:         qlcRaw,
		ComputeContainers: cluster.Spec.Dynamic.ComputeContainers,
		ComputeCores:      cluster.Spec.Dynamic.ComputeCores,
		DriveContainers:   cluster.Spec.Dynamic.DriveContainers,
		DriveCores:        cluster.Spec.Dynamic.DriveCores,
	}

	result, err := inventory.NewCollector(c).Collect(ctx, &cluster, own, cons)
	if err != nil {
		return err
	}
	plan := allocator.PlanCapacity(desired, s, result.ExistingDrives, result.ExistingCompute, result.Inventory, result.ComputeNodes, cons)

	var curTlc, curQlc int
	for _, e := range result.ExistingDrives {
		curTlc += e.TlcGiB
		curQlc += e.QlcGiB
	}
	ccreate, cgrow := computeGrowDiff(result.ExistingCompute, plan.ComputeLayout)
	dgrow := driveGrowDiff(result.ExistingDrives, plan.Grow)

	d := planData{
		Cluster:         displayName,
		ClusterCapacity: cluster.Spec.Dynamic.ClusterCapacity,
		Ratio:           ratioString(cluster.Spec.Dynamic.DriveTypesRatio),
		SW:              s.StripeWidth,
		RL:              s.RedundancyLevel,
		HS:              s.HotSpare,
		MinChunkGiB:     cons.MinChunkSizeGiB,
		DesiredTlcRaw:   tlcRaw,
		DesiredQlcRaw:   qlcRaw,
		CurrentTlc:      curTlc,
		CurrentQlc:      curQlc,
		Plan:            &plan,
		DriveGrow:       dgrow,
		ComputeCreate:   ccreate,
		ComputeGrow:     cgrow,
		Summary:         planSummary(&plan, &result, desired),
	}

	var out string
	if opts.Output == "json" {
		out, err = renderPlanJSON(&d)
		if err != nil {
			return err
		}
	} else {
		out = renderPlanText(&d)
	}
	if err := writeOutput(out); err != nil {
		return err
	}
	if plan.Infeasible != "" {
		os.Exit(2) // scriptable: non-zero exit on an infeasible plan (output already written)
	}
	return nil
}

// validate enforces the exactly-one-of --cluster / --new-cluster contract, and --new-cluster's own
// required inputs, before any client is created.
func (cmd *planCommand) validate() error {
	switch {
	case cmd.Cluster == "" && !cmd.NewCluster:
		return fmt.Errorf("exactly one of --cluster or --new-cluster must be set")
	case cmd.Cluster != "" && cmd.NewCluster:
		return fmt.Errorf("--cluster and --new-cluster are mutually exclusive; set exactly one")
	}
	if cmd.NewCluster {
		if cmd.ClusterCapacity == "" {
			return fmt.Errorf("--new-cluster requires --cluster-capacity")
		}
		// --node-selector is optional: empty selector => collector considers all nodes.
	}
	return nil
}

// buildSyntheticCluster populates a WekaCluster spec entirely from flags for --new-cluster mode.
// The spec-override flags act as the DEFINING values here (they populate rather than override).
// DPDK base memory is not set on the synthetic spec, so GetDpdkBaseMemoryMbByRole falls back to its
// 64 MiB/core default in Execute — the operator's default for a fresh dynamic cluster.
func (cmd *planCommand) buildSyntheticCluster() (weka.WekaCluster, error) {
	var cluster weka.WekaCluster
	cluster.Name = newClusterDisplayName
	sel, err := parseSelector(cmd.NodeSelector)
	if err != nil {
		return cluster, err
	}
	cluster.Spec.NodeSelector = sel
	cluster.Spec.Dynamic = &weka.WekaClusterTemplate{}
	if cmd.FDLabel != "" {
		lbl := cmd.FDLabel
		cluster.Spec.FailureDomain = &weka.FailureDomain{Label: &lbl}
	}
	if err := cmd.applyOverrides(&cluster); err != nil {
		return cluster, err
	}
	return cluster, nil
}

// applyOverrides layers the --flag overrides onto a copy of the cluster's live spec (only set fields).
func (cmd *planCommand) applyOverrides(cluster *weka.WekaCluster) error {
	d := cluster.Spec.Dynamic
	if cmd.ClusterCapacity != "" {
		d.ClusterCapacity = cmd.ClusterCapacity
	}
	if cmd.DriveTypesRatio != "" {
		r, err := parseRatio(cmd.DriveTypesRatio)
		if err != nil {
			return err
		}
		d.DriveTypesRatio = r
	}
	if cmd.StripeWidth != nil {
		cluster.Spec.StripeWidth = *cmd.StripeWidth
	}
	if cmd.Redundancy != nil {
		cluster.Spec.RedundancyLevel = *cmd.Redundancy
	}
	if cmd.HotSpare != nil {
		cluster.Spec.HotSpare = *cmd.HotSpare
	}
	if cmd.DriveContainers != nil {
		d.DriveContainers = *cmd.DriveContainers
	}
	if cmd.ComputeContainers != nil {
		d.ComputeContainers = *cmd.ComputeContainers
	}
	if cmd.ComputeCores != nil {
		d.ComputeCores = *cmd.ComputeCores
	}
	if cmd.DriveCores != nil {
		d.DriveCores = *cmd.DriveCores
	}
	return nil
}

// computeGrowDiff derives the compute create/grow rows by diffing the existing compute containers against
// the planner's per-container ComputeLayout, mirroring the controller's applyClusterCapacityComputeGrowth:
// the layout carries one target (cores + hugepages) per node, and each EXISTING container grows from ITS
// OWN cores/hugepages up to its node's target (compute is pinned one-per-node, so node == container). A
// grow row therefore shows the container's own before/after, not a node aggregate. A create is a brand-new
// compute container on a node with no existing compute (its cores/hugepages apply at creation, so it is not
// "deferred"); a grow edits an existing container's cores/hugepages, which live in the pod hash, so the
// change is deferred (applied on the next pod (re)creation).
func computeGrowDiff(existing []capacityplanner.ExistingComputeContainer, layout []capacityplanner.ComputeContainerSpec) (create, grow []computeRow) {
	targetByNode := make(map[string]capacityplanner.ComputeContainerSpec, len(layout))
	for _, spec := range layout {
		targetByNode[spec.Node] = spec
	}
	// grow: iterate existing containers so each row reports that container's own current cores/hugepages.
	existingByNode := make(map[string]bool, len(existing))
	for _, e := range existing {
		existingByNode[e.Node] = true
		t, ok := targetByNode[e.Node]
		if !ok {
			continue // not in the layout (unpinned/unknown node) — left untouched, like the controller
		}
		if t.NumCores > e.NumCores {
			grow = append(grow, computeRow{Name: e.Name, Node: e.Node, FromCores: e.NumCores, ToCores: t.NumCores, FromHugepagesMiB: e.HugepagesMiB, HugepagesMiB: t.HugepagesMiB, Deferred: true})
		}
	}
	// create: layout entries on nodes that have no existing compute container yet — a create has no
	// container name yet, so it is identified by Node only.
	for _, spec := range layout {
		if !existingByNode[spec.Node] {
			create = append(create, computeRow{Node: spec.Node, ToCores: spec.NumCores, HugepagesMiB: spec.HugepagesMiB, Deferred: false})
		}
	}
	return create, grow
}

// driveGrowDiff joins each planned in-place drive grow (ContainerGrowth, by name) to the current
// capacity/cores of the matching existing drive container, so the renderer can show from→to transitions.
// A name with no match keeps zero From* values (defensive — the planner only grows containers it saw).
func driveGrowDiff(existing []capacityplanner.ExistingContainer, grow []capacityplanner.ContainerGrowth) []driveGrowRow {
	cur := make(map[string]capacityplanner.ExistingContainer, len(existing))
	for _, e := range existing {
		cur[e.Name] = e
	}
	rows := make([]driveGrowRow, 0, len(grow))
	for _, g := range grow {
		e := cur[g.Name]
		rows = append(rows, driveGrowRow{
			Name:       g.Name,
			Node:       e.Node,
			FromTlcGiB: e.TlcGiB, ToTlcGiB: g.NewTlcGiB,
			FromQlcGiB: e.QlcGiB, ToQlcGiB: g.NewQlcGiB,
			FromCores: e.NumCores, ToCores: g.NewCores,
		})
	}
	return rows
}

// planSummary is the one-line SUMMARY footer. On a FEASIBLE plan it reports the raw delta placed, new
// nodes used, idle inventory and target. On an INFEASIBLE plan it leads with the fact that nothing will
// be created or grown (the controller discards the whole plan when infeasible), names the blocking pool,
// and flags any partial placement shown as diagnostic-only — so the create table is never mistaken for
// an actionable plan.
func planSummary(p *allocator.CapacityPlan, result *inventory.Result, desired allocator.DesiredCapacity) string {
	newNodes := map[string]struct{}{}
	var createRaw, createTlc, createQlc int
	for _, c := range p.Create {
		newNodes[c.Node] = struct{}{}
		createRaw += c.TlcGiB + c.QlcGiB
		createTlc += c.TlcGiB
		createQlc += c.QlcGiB
	}
	target := fmt.Sprintf("target raw %s (TLC %s + QLC %s)",
		util.HumanReadableGiB(desired.TlcRawGiB+desired.QlcRawGiB),
		util.HumanReadableGiB(desired.TlcRawGiB), util.HumanReadableGiB(desired.QlcRawGiB))

	if p.Infeasible != "" {
		var b strings.Builder
		b.WriteString("INFEASIBLE — no containers will be created or grown.")
		if p.Infeasibility != nil && p.Infeasibility.Pool != "" {
			fmt.Fprintf(&b, " Blocking pool: %s.", p.Infeasibility.Pool)
		}
		if len(newNodes) > 0 {
			fmt.Fprintf(&b, " Partial placement shown below (%s) is diagnostic only and will NOT be applied.",
				coveredPlacementPhrase(createTlc, createQlc, len(newNodes)))
		} else {
			b.WriteString(" No placement could be made.")
		}
		fmt.Fprintf(&b, " %s.", target)
		return b.String()
	}

	idle := 0
	for _, n := range result.Inventory {
		if _, used := newNodes[n.NodeName]; !used {
			idle++
		}
	}
	return fmt.Sprintf("create raw +%s across %d new node(s); %d other inventory node(s) not used by creates; %s",
		util.HumanReadableGiB(createRaw), len(newNodes), idle, target)
}

// coveredPlacementPhrase describes the partial placement in an infeasible plan's SUMMARY: which pool(s)
// the shown creates cover and across how many nodes, e.g. "TLC +22.2TiB across 6 node(s)" or
// "TLC +X + QLC +Y across N node(s)".
func coveredPlacementPhrase(createTlc, createQlc, nNodes int) string {
	var pools []string
	if createTlc > 0 {
		pools = append(pools, "TLC +"+util.HumanReadableGiB(createTlc))
	}
	if createQlc > 0 {
		pools = append(pools, "QLC +"+util.HumanReadableGiB(createQlc))
	}
	return fmt.Sprintf("%s across %d node(s)", strings.Join(pools, " + "), nNodes)
}

func parseRatio(s string) (*weka.DriveTypesRatio, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid drive-types-ratio %q (want tlc:qlc, e.g. 1:90)", s)
	}
	tlc, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return nil, fmt.Errorf("invalid tlc in drive-types-ratio %q: %w", s, err)
	}
	qlc, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return nil, fmt.Errorf("invalid qlc in drive-types-ratio %q: %w", s, err)
	}
	return &weka.DriveTypesRatio{Tlc: tlc, Qlc: qlc}, nil
}

func ratioString(r *weka.DriveTypesRatio) string {
	if r == nil {
		return "1:0 (TLC-only default)"
	}
	return fmt.Sprintf("%d:%d", r.Tlc, r.Qlc)
}
