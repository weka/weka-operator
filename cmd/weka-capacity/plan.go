package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/capacityplanner"
	"github.com/weka/weka-operator/internal/capacityplanner/inventory"
	"github.com/weka/weka-operator/internal/controllers/allocator"
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
	//nolint:lll // struct tag: a go-flags description must be one string literal.
	AutoFullDrives bool `long:"auto-full-drives" description:"For --new-cluster: build a hypothetical daemonset cluster (full drives, one pinned drive container per eligible node taking all its drives) instead of clusterCapacity. There is no spec field for this mode — it is what an empty dynamicTemplate means — so a --cluster cluster's mode is always derived from its live spec and this flag does not apply to --cluster."`

	NodeSelector string `long:"node-selector" description:"Node label selector (k=v[,k=v...]) for --new-cluster; which nodes the hypothetical cluster could land on. Optional — empty considers all nodes"`
	FDLabel      string `long:"fd-label" description:"Failure-domain label key for --new-cluster (label-based FD); default AUTO = one FD per host"`

	ClusterCapacity string `long:"cluster-capacity" description:"Override dynamicTemplate.clusterCapacity (e.g. 11022TiB)"`
	DriveTypesRatio string `long:"drive-types-ratio" description:"Override driveTypesRatio as tlc:qlc (e.g. 1:90)"`
	StripeWidth     *int   `long:"stripe-width" description:"Override stripeWidth"`
	Redundancy      *int   `long:"redundancy" description:"Override redundancyLevel"`
	HotSpare        *int   `long:"hot-spare" description:"Override hotSpare"`
	//nolint:lll // struct tag: a go-flags description must be one string literal.
	DriveContainers *int `long:"drive-containers" description:"Override dynamicTemplate.driveContainers. Outside a capacity mode this must be set together with --compute-containers, mirroring the CRD's both-or-neither rule"`
	//nolint:lll // struct tag: a go-flags description must be one string literal.
	ComputeContainers *int `long:"compute-containers" description:"Override dynamicTemplate.computeContainers. Outside a capacity mode this must be set together with --drive-containers, mirroring the CRD's both-or-neither rule"`
	ComputeCores      *int `long:"compute-cores" description:"Override dynamicTemplate.computeCores"`
	DriveCores        *int `long:"drive-cores" description:"Override dynamicTemplate.driveCores"`
	//nolint:lll // struct tag: a go-flags description must be one string literal.
	NumDrives *int `long:"num-drives" description:"Override dynamicTemplate.numDrives. In the daemonset mode this is a per-node override: every eligible node takes exactly this many of its LARGEST signed full drives instead of all of them"`

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

// driveGrowRow pairs the CURRENT (from) capacity/cores with the planner's target (to) so the renderer can
// show the transition; unlike the JSON growDrive array (the planner's ContainerGrowth), it is not serialized.
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
	Plan            *capacityplanner.CapacityPlan
	DriveGrow       []driveGrowRow
	ComputeCreate   []computeRow
	ComputeGrow     []computeRow
	Summary         string
}

// autoFullDrivesNodeRow is the per-node dry-run view: DrivesAvail vs DrivesUsed, with State one of
// create/grow/existing/not-planned. Note explains a row holding fewer drives than it offers — derived from
// that node's own condition, not from the warning text — and points at WARNINGS for the fleet-wide detail.
type autoFullDrivesNodeRow struct {
	Node        string `json:"node"`
	FD          string `json:"fd"`
	DrivesUsed  int    `json:"drivesUsed"`
	DrivesAvail int    `json:"drivesAvail"`
	TlcGiB      int    `json:"tlcGiB"`
	Cores       int    `json:"cores"`
	State       string `json:"state"` // create / grow / existing / not-planned
	Note        string `json:"note,omitempty"`
}

// nodeStateNotPlanned marks a node the plan places nothing on. It does not imply infeasibility: on a
// feasible plan it means the node was withheld from a new container because it is cordoned, not ready or
// carrying an untolerated taint (WarningKindNodeIneligible), which the row's Note names.
const nodeStateNotPlanned = "not-planned"

// autoFullDrivesDriveGrowRow mirrors driveGrowRow for the daemonset mode's expand-only growth,
// additionally carrying the drive-count transition (only PlanAutoFullDrives sets
// ContainerGrowth.NewNumDrives; see planner.go). Drives and cores move independently: a growth may
// raise drives alone (free, no pod restart), cores alone, or both.
type autoFullDrivesDriveGrowRow struct {
	Name                       string
	Node                       string
	FromTlcGiB, ToTlcGiB       int
	FromCores, ToCores         int
	FromNumDrives, ToNumDrives int
}

type autoFullDrivesPlanData struct {
	Cluster       string
	Plan          *capacityplanner.CapacityPlan
	Nodes         []autoFullDrivesNodeRow
	DriveGrow     []autoFullDrivesDriveGrowRow
	ComputeCreate []computeRow
	ComputeGrow   []computeRow
	Summary       string
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
			// A nil dynamicTemplate is not an error: "nothing set" IS the daemonset mode, which is why
			// UsesAutoFullDrives() returns true on a nil receiver. Materialize the empty template the nil
			// stands for so the routing below and applyOverrides both see a value.
			cluster.Spec.Dynamic = &weka.WekaClusterTemplate{}
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
	// Same per-role DPDK/CpuPolicy overrides planClusterCapacity/planAutoFullDrives feed the planner, layered
	// onto the CLI/config-derived cons rather than rebuilt from scratch.
	cons = allocator.ApplyClusterSpecOverrides(cons, &cluster.Spec)

	// Route exactly as the controller derives the mode, and in this ORDER. UsesAutoFullDrives() is now true
	// for any template that is neither capacity- nor count-based (including a nil/empty one), so it must be
	// tested AFTER UsesClusterCapacity() — otherwise it is not a discriminator, it is a catch-all.
	//
	// The default arm matters: the drive-sharing modes (containerCapacity, numDrives+driveCapacity) and
	// explicit container counts are real templates that this CLI cannot plan, because neither has a planner
	// behind it. They need naming as unsupported modes — routing them to executeClusterCapacity would fail
	// on "parsing clusterCapacity: empty", which names the wrong field and reads like a malformed spec.
	switch {
	case cluster.Spec.Dynamic.UsesClusterCapacity():
		return cmd.executeClusterCapacity(ctx, c, &cluster, own, cons, displayName)
	case cluster.Spec.Dynamic.UsesAutoFullDrives():
		return cmd.executeAutoFullDrives(ctx, c, &cluster, own, cons, displayName)
	default:
		return fmt.Errorf(
			"cluster %q is not planner-managed: its dynamicTemplate sizes containers directly (%s), and only "+
				"clusterCapacity and the daemonset mode (no container counts and no capacity field) have a "+
				"capacity planner to dry-run. Use `weka-capacity explore-nodes` to inspect node inventory instead",
			displayName, describeSizingMode(cluster.Spec.Dynamic))
	}
}

// describeSizingMode names the non-planner-managed mode a template selected, for the routing error
// above. Ordered as the CRD's own precedence reads: capacity fields first, then container counts.
func describeSizingMode(d *weka.WekaClusterTemplate) string {
	switch {
	case d.ContainerCapacity > 0:
		return "containerCapacity — drive sharing"
	case d.DriveCapacity > 0:
		return "numDrives + driveCapacity — drive sharing"
	case d.ComputeContainers > 0 && d.DriveContainers > 0:
		return "computeContainers + driveContainers — explicit container counts"
	default:
		// Unreachable: exactly one count set is rejected by the CRD's both-or-neither rule, and neither set
		// with no capacity field is the daemonset mode, which the switch above already claimed.
		return "an unrecognized combination of dynamicTemplate fields"
	}
}

// executeClusterCapacity is the clusterCapacity dry-run path: a whole-cluster TLC/QLC raw capacity target,
// FD-balanced shared-drive planning via inventory.Collect and capacityplanner.PlanCapacity.
func (cmd *planCommand) executeClusterCapacity(ctx context.Context, c client.Client, cluster *weka.WekaCluster, own []*weka.WekaContainer, cons *capacityplanner.CapacityConstraints, displayName string) error {
	s := capacityplanner.ProtectionScheme{
		StripeWidth:     cluster.Spec.StripeWidth,
		RedundancyLevel: cluster.Spec.RedundancyLevel,
		HotSpare:        cluster.Spec.HotSpare,
	}
	capGiB, err := cluster.Spec.Dynamic.GetClusterCapacityGiB()
	if err != nil {
		return fmt.Errorf("parsing clusterCapacity: %w", err)
	}
	raw := capacityplanner.RawCapacityGiB(capGiB, s.StripeWidth, s.RedundancyLevel, s.HotSpare)
	tlcRaw, qlcRaw := weka.GetTlcQlcCapacity(raw, cluster.Spec.Dynamic.DriveTypesRatio)
	desired := capacityplanner.DesiredCapacity{
		TlcRawGiB:         tlcRaw,
		QlcRawGiB:         qlcRaw,
		ComputeContainers: cluster.Spec.Dynamic.ComputeContainers,
		ComputeCores:      cluster.Spec.Dynamic.ComputeCores,
		DriveContainers:   cluster.Spec.Dynamic.DriveContainers,
		DriveCores:        cluster.Spec.Dynamic.DriveCores,
	}

	result, err := inventory.NewCollector(c).Collect(ctx, cluster, own, cons)
	if err != nil {
		return err
	}
	plan := capacityplanner.PlanCapacity(desired, s, result.ExistingDrives, result.ExistingCompute, result.Inventory, result.ComputeNodes, cons)

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

// executeAutoFullDrives is the daemonset dry-run path (FullDrivesInventory +
// capacityplanner.PlanAutoFullDrives), mirroring the controller; a dry run reports "no signed drives yet" as a
// summary instead of retrying like the controller would (see autoFullDrivesPlanSummary).
func (cmd *planCommand) executeAutoFullDrives(ctx context.Context, c client.Client, cluster *weka.WekaCluster, own []*weka.WekaContainer, cons *capacityplanner.CapacityConstraints, displayName string) error {
	dyn := cluster.Spec.Dynamic
	// Only the three pins survive: container counts are unrepresentable in this mode (setting either one
	// is what takes a template OUT of it), so there is nothing else to forward.
	desired := capacityplanner.AutoFullDrivesDesired{
		ComputeCores: dyn.ComputeCores,
		DriveCores:   dyn.DriveCores,
		NumDrives:    dyn.NumDrives,
	}

	fdByNode, nodeInv, computeNodes, err := inventory.NewCollector(c).FullDrivesInventory(ctx, cluster, own, cons)
	if err != nil {
		return err
	}
	existingDrives := inventory.ExistingDrives(ctx, cluster, own, fdByNode)
	existingCompute := inventory.ExistingCompute(ctx, own)

	plan := capacityplanner.PlanAutoFullDrives(desired, existingDrives, existingCompute, nodeInv, computeNodes, cons)

	ccreate, cgrow := computeGrowDiff(existingCompute, plan.ComputeLayout)
	dgrow := autoFullDrivesDriveGrowDiff(existingDrives, plan.Grow)
	nodes := autoFullDrivesNodeRows(nodeInv, existingDrives, &plan)

	d := autoFullDrivesPlanData{
		Cluster:       displayName,
		Plan:          &plan,
		Nodes:         nodes,
		DriveGrow:     dgrow,
		ComputeCreate: ccreate,
		ComputeGrow:   cgrow,
		Summary:       autoFullDrivesPlanSummary(&plan, nodeInv),
	}

	var out string
	if opts.Output == "json" {
		out, err = renderAutoFullDrivesPlanJSON(&d)
		if err != nil {
			return err
		}
	} else {
		out = renderAutoFullDrivesPlanText(&d)
	}
	if err := writeOutput(out); err != nil {
		return err
	}
	if plan.Infeasible != "" {
		os.Exit(2) // scriptable: non-zero exit on an infeasible plan (output already written)
	}
	return nil
}

// validate enforces exactly-one-of --cluster/--new-cluster plus --new-cluster's required inputs, and
// rejects --auto-full-drives with --cluster (that mode is derived from the live spec and would be
// silently ignored).
func (cmd *planCommand) validate() error {
	switch {
	case cmd.Cluster == "" && !cmd.NewCluster:
		return fmt.Errorf("exactly one of --cluster or --new-cluster must be set")
	case cmd.Cluster != "" && cmd.NewCluster:
		return fmt.Errorf("--cluster and --new-cluster are mutually exclusive; set exactly one")
	}
	if cmd.AutoFullDrives && cmd.Cluster != "" {
		return fmt.Errorf("--auto-full-drives only applies to --new-cluster; a --cluster cluster's mode is derived from which dynamicTemplate fields its live spec sets, not from a flag")
	}
	if cmd.NewCluster {
		if !cmd.AutoFullDrives && cmd.ClusterCapacity == "" {
			return fmt.Errorf("--new-cluster requires --cluster-capacity, or --auto-full-drives for a daemonset dry run")
		}
		// --node-selector is optional: empty selector => collector considers all nodes.
	}
	return nil
}

// buildSyntheticCluster populates a WekaCluster spec entirely from flags for --new-cluster mode (the
// flags define rather than override). DPDK base memory is left unset, so GetDpdkBaseMemoryMbByRole
// falls back to its 64 MiB/core default.
func (cmd *planCommand) buildSyntheticCluster() (weka.WekaCluster, error) {
	var cluster weka.WekaCluster
	cluster.Name = newClusterDisplayName
	sel, err := parseSelector(cmd.NodeSelector)
	if err != nil {
		return cluster, err
	}
	cluster.Spec.NodeSelector = sel
	// An EMPTY template is the daemonset mode — there is no field to set, which is the whole point of the
	// mode being implicit. --auto-full-drives therefore contributes nothing here; it exists only to say
	// "do not require --cluster-capacity" (see validate). --cluster-capacity, applied below, is what moves
	// this synthetic cluster off the daemonset default.
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
	if err := cmd.validateContainerCountOverrides(d); err != nil {
		return err
	}
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
	if cmd.NumDrives != nil {
		d.NumDrives = *cmd.NumDrives
	}
	return nil
}

// validateContainerCountOverrides mirrors the CRD's both-or-neither CEL rule on the flags, so a dry run
// cannot model a spec the apiserver would reject. Without it, `--drive-containers 6` on a daemonset
// cluster silently knocks it out of the mode and plans something that could never be applied.
//
// Runs BEFORE any override lands, so it judges the POST-override state from (live spec + flags) rather
// than a half-mutated template. The guard is skipped entirely when a capacity field is in play — from
// the live spec or from --cluster-capacity — because the CEL rule itself is guarded the same way, and
// one count alongside a capacity field is legal.
func (cmd *planCommand) validateContainerCountOverrides(d *weka.WekaClusterTemplate) error {
	if cmd.DriveContainers == nil && cmd.ComputeContainers == nil {
		return nil // not overriding counts — whatever the live spec says already passed admission
	}
	if cmd.ClusterCapacity != "" || d.UsesClusterCapacity() || d.ContainerCapacity > 0 || d.DriveCapacity > 0 {
		return nil // a capacity mode; the CRD rule does not apply and neither does this one
	}
	// nil means "not overridden", so fall back to the live spec's value for the count left alone.
	drive, compute := d.DriveContainers, d.ComputeContainers
	if cmd.DriveContainers != nil {
		drive = *cmd.DriveContainers
	}
	if cmd.ComputeContainers != nil {
		compute = *cmd.ComputeContainers
	}
	if (drive > 0) != (compute > 0) {
		return fmt.Errorf(
			"--drive-containers and --compute-containers must be set together (resulting driveContainers=%d, "+
				"computeContainers=%d): setting both sizes the cluster by container counts, leaving both unset "+
				"makes the operator act as a daemonset over its drive-role nodeSelector. The CRD enforces the "+
				"same rule, so a spec with only one of them cannot be applied. --num-drives, --drive-cores and "+
				"--compute-cores are pins and may be set either way",
			drive, compute)
	}
	return nil
}

// computeGrowDiff diffs existing compute containers against the planner's ComputeLayout (pinned
// one-per-node, so node == container). Grow edits live in the pod hash, so they're Deferred to the
// next pod (re)creation; a create has no name yet.
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
	// create: layout entries with no existing container on that node yet (identified by Node, not name).
	for _, spec := range layout {
		if !existingByNode[spec.Node] {
			create = append(create, computeRow{Node: spec.Node, ToCores: spec.NumCores, HugepagesMiB: spec.HugepagesMiB, Deferred: false})
		}
	}
	return create, grow
}

// driveGrowDiff joins each planned in-place drive grow (by name) to its existing container's current
// capacity/cores, so the renderer can show from→to. Unmatched names keep zero From* values (defensive).
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

// planSummary is the one-line SUMMARY footer: on FEASIBLE, raw delta/new-nodes/idle/target; on
// INFEASIBLE, leads with "nothing will be created or grown" and flags any partial placement as diagnostic-only.
func planSummary(p *capacityplanner.CapacityPlan, result *inventory.Result, desired capacityplanner.DesiredCapacity) string {
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

// coveredPlacementPhrase describes an infeasible plan's partial placement in its SUMMARY, e.g.
// "TLC +22.2TiB across 6 node(s)" or "TLC +X + QLC +Y across N node(s)".
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

// autoFullDrivesDriveGrowDiff is driveGrowDiff's daemonset counterpart, additionally carrying the
// drive-count transition (drive count and cores grow independently now — see PlanAutoFullDrives's
// growth branch in autofulldrives.go).
func autoFullDrivesDriveGrowDiff(existing []capacityplanner.ExistingContainer, grow []capacityplanner.ContainerGrowth) []autoFullDrivesDriveGrowRow {
	cur := make(map[string]capacityplanner.ExistingContainer, len(existing))
	for _, e := range existing {
		cur[e.Name] = e
	}
	rows := make([]autoFullDrivesDriveGrowRow, 0, len(grow))
	for _, g := range grow {
		e := cur[g.Name]
		rows = append(rows, autoFullDrivesDriveGrowRow{
			Name:       g.Name,
			Node:       e.Node,
			FromTlcGiB: e.TlcGiB, ToTlcGiB: g.NewTlcGiB,
			FromCores: e.NumCores, ToCores: g.NewCores,
			FromNumDrives: e.NumDrives, ToNumDrives: g.NewNumDrives,
		})
	}
	return rows
}

// hasFleetWarning reports whether plan carries a warning of the given kind. Every planner warning is
// fleet-wide (see capacityplanner.Warning), naming every affected node in its Message rather than in a
// per-warning field, so a row can only point at the warning by Kind — it can never pull its own text out of
// one. Deriving a row's text from a kind is only sound for a single-cause kind (DrivesStranded); gating a
// pointer at the WARNINGS section is sound for any kind.
func hasFleetWarning(warnings []capacityplanner.Warning, kind capacityplanner.WarningKind) bool {
	for _, w := range warnings {
		if w.Kind == kind {
			return true
		}
	}
	return false
}

// autoFullDrivesNodeRows builds the NODES table: one row per node with a signed drive (0-drive nodes
// skipped). State is grow/existing/create by cross-referencing plan.Grow/Create, else nodeStateNotPlanned
// — reachable on a FEASIBLE plan for a node withheld from a new container (cordoned/not ready/untolerated
// taint), not only on an infeasible one. DrivesAvail sums own-claimed + free (not free-only, so
// self-claimed nodes still show); when DrivesUsed < DrivesAvail, Note explains the gap. For a row the walk
// never sized at all (nodeStateNotPlanned) the node's own inventory holds the authoritative answer, so the
// reason is read from IneligibleReason/HasDeletingDriveContainer rather than from the aggregated warning —
// which names every affected node in one message and so cannot say which condition applies to THIS row. A
// row the walk did size but couldn't claim every signed drive on gets a pointer at DrivesStranded (a
// numDrives pin), whose one cause needs no per-node disambiguation. Sorted by node name.
func autoFullDrivesNodeRows(nodeInv []capacityplanner.NodeCapacity, existing []capacityplanner.ExistingContainer, plan *capacityplanner.CapacityPlan) []autoFullDrivesNodeRow {
	existingByNode := make(map[string]capacityplanner.ExistingContainer, len(existing))
	for _, e := range existing {
		existingByNode[e.Node] = e
	}
	growByName := make(map[string]capacityplanner.ContainerGrowth, len(plan.Grow))
	for _, g := range plan.Grow {
		growByName[g.Name] = g
	}
	createByNode := make(map[string]capacityplanner.NewContainer, len(plan.Create))
	for _, cr := range plan.Create {
		createByNode[cr.Node] = cr
	}

	// The reason is the node's own; the pointer is only earned when the matching aggregate reached
	// plan.Warnings — a mid-walk infeasible abort stops collecting, leaving later nodes a condition
	// with no warning to point at.
	note := func(kind capacityplanner.WarningKind, reason string) string {
		if hasFleetWarning(plan.Warnings, kind) {
			return reason + " — see WARNINGS"
		}
		return reason
	}

	rows := make([]autoFullDrivesNodeRow, 0, len(nodeInv))
	for i := range nodeInv {
		n := &nodeInv[i]
		avail := len(n.OwnDriveCapacitiesGiB) + len(n.DriveCapacitiesGiB)
		if avail == 0 {
			continue
		}
		row := autoFullDrivesNodeRow{Node: n.NodeName, FD: n.FDValue, DrivesAvail: avail}
		switch {
		case existingByNode[n.NodeName].Name != "":
			e := existingByNode[n.NodeName]
			if g, ok := growByName[e.Name]; ok {
				row.State = "grow"
				row.DrivesUsed = g.NewNumDrives
				row.TlcGiB = g.NewTlcGiB
				row.Cores = g.NewCores
			} else {
				row.State = "existing"
				row.DrivesUsed = e.NumDrives
				// ExistingContainer.TlcGiB is structurally 0 in this mode (a driveCapacity/containerCapacity
				// template is a different mode by construction), so fall back to the node's own allocated
				// drives — mirroring planAutoFullDrivesAttempt — unless a populated TlcGiB already wins.
				row.TlcGiB = e.TlcGiB
				if row.TlcGiB == 0 {
					for _, gib := range n.OwnDriveCapacitiesGiB {
						row.TlcGiB += gib
					}
				}
				row.Cores = e.NumCores
			}
		case createByNode[n.NodeName].Node != "":
			cr := createByNode[n.NodeName]
			row.State = "create"
			row.DrivesUsed = cr.NumDrives
			row.TlcGiB = cr.TlcGiB
			row.Cores = cr.NumCores
		default:
			row.State = nodeStateNotPlanned
			row.DrivesUsed = 0
		}
		if row.DrivesUsed < row.DrivesAvail {
			switch {
			case row.State == nodeStateNotPlanned && n.IneligibleReason != "":
				row.Note = note(capacityplanner.WarningKindNodeIneligible, n.IneligibleReason)
			case row.State == nodeStateNotPlanned && n.HasDeletingDriveContainer:
				row.Note = note(capacityplanner.WarningKindTransient, "drive container being deleted")
			case existingByNode[n.NodeName].Unscheduled:
				row.Note = note(capacityplanner.WarningKindTransient, "pod has not been scheduled yet")
			case row.State != nodeStateNotPlanned && hasFleetWarning(plan.Warnings, capacityplanner.WarningKindDrivesStranded):
				row.Note = "drives held back by the numDrives pin — see WARNINGS"
			}
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Node < rows[j].Node })
	return rows
}

// autoFullDrivesPlanSummary is the daemonset SUMMARY footer, a dry-run superset of the controller's
// formatAutoFullDrivesPlanSummary: "would create/grow" phrasing plus counts the controller instead
// reports via events.
func autoFullDrivesPlanSummary(plan *capacityplanner.CapacityPlan, nodeInv []capacityplanner.NodeCapacity) string {
	// Mirrors planSummary's INFEASIBLE branch. Checked first so the "would create N" phrasing below is
	// unreachable on an infeasible plan — the partial Create/Grow entries the walk still recorded for
	// diagnostics (autofulldrives.go) are never mistaken for what will actually be applied.
	if plan.Infeasible != "" {
		var b strings.Builder
		b.WriteString("INFEASIBLE — no drive containers will be created or grown.")
		if plan.Infeasibility != nil && plan.Infeasibility.Pool != "" {
			fmt.Fprintf(&b, " Blocking pool: %s.", plan.Infeasibility.Pool)
		}
		nodes := map[string]struct{}{}
		var placedTlcGiB int
		for _, c := range plan.Create {
			nodes[c.Node] = struct{}{}
			placedTlcGiB += c.TlcGiB
		}
		if len(nodes) > 0 {
			// QLC is 0: full drives is TLC-only by construction, so the phrase collapses to the TLC clause.
			fmt.Fprintf(&b, " Partial placement shown below (%s) is diagnostic only and will NOT be applied.",
				coveredPlacementPhrase(placedTlcGiB, 0, len(nodes)))
		} else {
			b.WriteString(" No placement could be made.")
		}
		return b.String()
	}

	if len(plan.Create) == 0 && len(plan.Grow) == 0 {
		anySigned := false
		for i := range nodeInv {
			n := &nodeInv[i]
			if len(n.OwnDriveCapacitiesGiB) > 0 || len(n.DriveCapacitiesGiB) > 0 {
				anySigned = true
				break
			}
		}
		if !anySigned {
			return "no node has signed full drives yet — nothing to plan (the controller would wait and retry here)"
		}
		// A steady state needs no qualifier: drive cores are never traded for compute, so there is no
		// "converged, but capped" variant to spell out. The full sizing breakdown is printed in the
		// DRIVE SIZING section regardless.
		if len(plan.Warnings) > 0 {
			return fmt.Sprintf("steady state: no drive containers to create or grow, but %d warning(s) — see WARNINGS below", len(plan.Warnings))
		}
		return "steady state: no drive containers to create or grow"
	}

	nodes := map[string]struct{}{}
	var placedGiB int
	for _, c := range plan.Create {
		nodes[c.Node] = struct{}{}
		placedGiB += c.TlcGiB + c.QlcGiB
	}
	summary := fmt.Sprintf("daemonset plan: would create %d drive container(s) across %d node(s), placing %s",
		len(plan.Create), len(nodes), util.HumanReadableGiB(placedGiB))
	if len(plan.Grow) > 0 {
		summary += fmt.Sprintf("; would grow %d drive container(s)", len(plan.Grow))
	}
	if len(plan.ComputeLayout) > 0 {
		computeNodes := map[string]struct{}{}
		var totalCores int
		for _, c := range plan.ComputeLayout {
			computeNodes[c.Node] = struct{}{}
			totalCores += c.NumCores
		}
		summary += fmt.Sprintf("; compute %d container(s), %d cores on %d node(s)",
			len(plan.ComputeLayout), totalCores, len(computeNodes))
	}
	if len(plan.Warnings) > 0 {
		summary += fmt.Sprintf("; %d warning(s) — see WARNINGS below", len(plan.Warnings))
	}
	// ds.Reason is deliberately not appended: it restates the create/grow counts this summary already
	// carries, and the DRIVE SIZING section prints it in full a few lines above.
	return summary
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
