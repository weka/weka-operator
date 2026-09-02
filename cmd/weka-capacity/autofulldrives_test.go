package main

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"

	"github.com/weka/weka-operator/internal/capacityplanner"
)

// TestPlanValidate_AutoFullDrives covers --auto-full-drives' contract: valid only alongside
// --new-cluster, an alternative to --cluster-capacity, not an addition to it.
func TestPlanValidate_AutoFullDrives(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{"auto-full-drives with cluster is rejected", []string{"plan", "--cluster", "c", "--auto-full-drives"}, true},
		{"auto-full-drives alone satisfies new-cluster's required input", []string{"plan", "--new-cluster", "--auto-full-drives"}, false},
		{"new-cluster with neither cluster-capacity nor auto-full-drives", []string{"plan", "--new-cluster"}, true},
		{"auto-full-drives and cluster-capacity together is fine (cluster-capacity wins at buildSyntheticCluster)", []string{"plan", "--new-cluster", "--auto-full-drives", "--cluster-capacity", "30TiB"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := parseOpts(t, tc.args)
			err := o.Plan.validate()
			if tc.wantErr && err == nil {
				t.Errorf("validate() = nil, want error")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("validate() = %v, want nil", err)
			}
		})
	}
}

// TestBuildSyntheticCluster_AutoFullDrives pins that the mode is reached by building an EMPTY template
// rather than setting any field: the mode is implicit, so --auto-full-drives has nothing to write and
// exists only to waive --cluster-capacity. The --cluster-capacity leg is the regression that matters —
// UsesAutoFullDrives() is a catch-all, so it must go false the moment a capacity field appears.
func TestBuildSyntheticCluster_AutoFullDrives(t *testing.T) {
	o := parseOpts(t, []string{"plan", "--new-cluster", "--auto-full-drives", "--node-selector", "weka.io/x=y"})
	cluster, err := o.Plan.buildSyntheticCluster()
	if err != nil {
		t.Fatalf("buildSyntheticCluster() unexpected error: %v", err)
	}
	if cluster.Spec.Dynamic == nil {
		t.Fatalf("Spec.Dynamic is nil")
	}
	if !cluster.Spec.Dynamic.UsesAutoFullDrives() {
		t.Errorf("UsesAutoFullDrives() = false, want true (this is the exact predicate Execute's routing reads)")
	}
	if cluster.Spec.Dynamic.UsesClusterCapacity() {
		t.Errorf("UsesClusterCapacity() = true, want false — routing tests it FIRST, so it must not match here")
	}

	// With --cluster-capacity, routing must land on clusterCapacity instead.
	o2 := parseOpts(t, []string{"plan", "--new-cluster", "--cluster-capacity", "30TiB"})
	cluster2, err := o2.Plan.buildSyntheticCluster()
	if err != nil {
		t.Fatalf("buildSyntheticCluster() unexpected error: %v", err)
	}
	if !cluster2.Spec.Dynamic.UsesClusterCapacity() {
		t.Errorf("UsesClusterCapacity() = false, want true when --cluster-capacity is set")
	}
	if cluster2.Spec.Dynamic.UsesAutoFullDrives() {
		t.Errorf("UsesAutoFullDrives() = true with --cluster-capacity set; the catch-all predicate must yield to a capacity field")
	}
}

// TestBuildSyntheticCluster_NumDrives covers the --num-drives override: a per-node drive-count pin that
// must NOT take the synthetic cluster out of the daemonset mode (it is a pin, not a mode selector).
func TestBuildSyntheticCluster_NumDrives(t *testing.T) {
	o := parseOpts(t, []string{"plan", "--new-cluster", "--auto-full-drives", "--num-drives", "4"})
	cluster, err := o.Plan.buildSyntheticCluster()
	if err != nil {
		t.Fatalf("buildSyntheticCluster() unexpected error: %v", err)
	}
	if cluster.Spec.Dynamic.NumDrives != 4 {
		t.Errorf("Dynamic.NumDrives = %d, want 4", cluster.Spec.Dynamic.NumDrives)
	}
	if !cluster.Spec.Dynamic.UsesAutoFullDrives() {
		t.Errorf("UsesAutoFullDrives() = false with only numDrives pinned; numDrives is a per-node override, not a mode selector")
	}

	// Same for the other two pins, together.
	o2 := parseOpts(t, []string{"plan", "--new-cluster", "--auto-full-drives", "--num-drives", "5", "--drive-cores", "3", "--compute-cores", "8"})
	cluster2, err := o2.Plan.buildSyntheticCluster()
	if err != nil {
		t.Fatalf("buildSyntheticCluster() unexpected error: %v", err)
	}
	if !cluster2.Spec.Dynamic.UsesAutoFullDrives() {
		t.Errorf("UsesAutoFullDrives() = false with numDrives/driveCores/computeCores pinned; all three are pinnable in this mode")
	}
	if cluster2.Spec.Dynamic.DriveCores != 3 || cluster2.Spec.Dynamic.ComputeCores != 8 {
		t.Errorf("driveCores/computeCores = %d/%d, want 3/8", cluster2.Spec.Dynamic.DriveCores, cluster2.Spec.Dynamic.ComputeCores)
	}
}

// TestApplyOverrides_ContainerCountsBothOrNeither covers the flag-level mirror of the CRD's
// both-or-neither CEL rule: a dry run must not model a spec the apiserver would reject, and one count
// alone would silently knock a daemonset cluster out of its mode.
func TestApplyOverrides_ContainerCountsBothOrNeither(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "drive-containers alone on a daemonset cluster is rejected",
			args:    []string{"plan", "--new-cluster", "--auto-full-drives", "--drive-containers", "6"},
			wantErr: true,
		},
		{
			name:    "compute-containers alone on a daemonset cluster is rejected",
			args:    []string{"plan", "--new-cluster", "--auto-full-drives", "--compute-containers", "6"},
			wantErr: true,
		},
		{
			name:    "both together is accepted (explicit container counts)",
			args:    []string{"plan", "--new-cluster", "--auto-full-drives", "--drive-containers", "6", "--compute-containers", "6"},
			wantErr: false,
		},
		{
			name:    "neither is accepted (stays a daemonset)",
			args:    []string{"plan", "--new-cluster", "--auto-full-drives"},
			wantErr: false,
		},
		{
			name:    "one count alongside --cluster-capacity is accepted; the CRD rule is guarded the same way",
			args:    []string{"plan", "--new-cluster", "--cluster-capacity", "70TiB", "--drive-containers", "7"},
			wantErr: false,
		},
		{
			name:    "zero means unset, so --drive-containers 0 with a compute count set is still lopsided",
			args:    []string{"plan", "--new-cluster", "--auto-full-drives", "--drive-containers", "0", "--compute-containers", "6"},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := parseOpts(t, tc.args)
			_, err := o.Plan.buildSyntheticCluster()
			if tc.wantErr && err == nil {
				t.Errorf("buildSyntheticCluster() = nil error, want rejection")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("buildSyntheticCluster() = %v, want nil", err)
			}
			if tc.wantErr && err != nil && !strings.Contains(err.Error(), "must be set together") {
				t.Errorf("error = %q, want it to name the both-or-neither rule", err)
			}
		})
	}
}

// TestApplyOverrides_CountOverrideCompletesLiveSpec pins that the guard judges the POST-override state:
// overriding ONE count on a cluster whose live spec already sets the other is legal, because the result
// still has both.
func TestApplyOverrides_CountOverrideCompletesLiveSpec(t *testing.T) {
	o := parseOpts(t, []string{"plan", "--cluster", "c", "--drive-containers", "6"})
	cluster := weka.WekaCluster{}
	cluster.Spec.Dynamic = &weka.WekaClusterTemplate{ComputeContainers: 6}
	if err := o.Plan.applyOverrides(&cluster); err != nil {
		t.Errorf("applyOverrides() = %v, want nil (live computeContainers=6 + flag driveContainers=6 is both-set)", err)
	}

	// ...and dropping the live spec's only remaining count to 0 is what the guard must catch.
	o2 := parseOpts(t, []string{"plan", "--cluster", "c", "--drive-containers", "0"})
	cluster2 := weka.WekaCluster{}
	cluster2.Spec.Dynamic = &weka.WekaClusterTemplate{ComputeContainers: 6, DriveContainers: 6}
	if err := o2.Plan.applyOverrides(&cluster2); err == nil {
		t.Errorf("applyOverrides() = nil, want rejection: zeroing driveContainers leaves computeContainers=6 alone")
	}
}

// TestDescribeSizingMode covers the routing error's mode naming — the message a user gets when they
// point the CLI at a template no planner backs.
func TestDescribeSizingMode(t *testing.T) {
	cases := []struct {
		name string
		tmpl *weka.WekaClusterTemplate
		want string
	}{
		{"containerCapacity", &weka.WekaClusterTemplate{ContainerCapacity: 3500}, "containerCapacity"},
		{"numDrives + driveCapacity", &weka.WekaClusterTemplate{NumDrives: 6, DriveCapacity: 3500}, "driveCapacity"},
		{"explicit counts", &weka.WekaClusterTemplate{ComputeContainers: 6, DriveContainers: 6}, "explicit container counts"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Precondition: each of these really is outside both planner-managed modes, or the routing
			// switch would never reach describeSizingMode.
			if tc.tmpl.UsesClusterCapacity() || tc.tmpl.UsesAutoFullDrives() {
				t.Fatalf("fixture %+v is planner-managed; describeSizingMode is unreachable for it", tc.tmpl)
			}
			if got := describeSizingMode(tc.tmpl); !strings.Contains(got, tc.want) {
				t.Errorf("describeSizingMode() = %q, want it to mention %q", got, tc.want)
			}
		})
	}
}

// TestAutoFullDrivesNodeRows covers the four per-node states (create/grow/existing/not-planned),
// drives-used-vs-avail accounting, and reading a not-planned row's Note from the node's own
// IneligibleReason rather than from the aggregated fleet warning.
func TestAutoFullDrivesNodeRows(t *testing.T) {
	nodeInv := []capacityplanner.NodeCapacity{
		{NodeName: "n-create", FDValue: "n-create", DriveCapacitiesGiB: []int{3840, 3840}},
		{NodeName: "n-grow", FDValue: "n-grow", DriveCapacitiesGiB: []int{3840, 3840, 3840}},
		{NodeName: "n-existing", FDValue: "n-existing", DriveCapacitiesGiB: []int{3840}},
		// IneligibleReason, not the plan's aggregated warning, is what the row's Note must read.
		{NodeName: "n-ineligible", FDValue: "n-ineligible", DriveCapacitiesGiB: []int{3840, 3840}, IneligibleReason: "not ready"},
		{NodeName: "n-empty", FDValue: "n-empty", DriveCapacitiesGiB: nil}, // 0 signed drives — must be skipped entirely
		// n-own-only: both drives already own-claimed (0 free); avail must be len(Own)+len(Drive) (plan.go §7).
		{NodeName: "n-own-only", FDValue: "n-own-only", OwnDriveCapacitiesGiB: []int{3840, 3840}, DriveCapacitiesGiB: nil},
		// n-unsched: growth deferred because its pod has not bound, so used < avail with no numDrives pin.
		{NodeName: "n-unsched", FDValue: "n-unsched", OwnDriveCapacitiesGiB: []int{3840}, DriveCapacitiesGiB: []int{3840, 3840}},
	}
	existing := []capacityplanner.ExistingContainer{
		{Name: "c-grow", Node: "n-grow", TlcGiB: 7680, NumCores: 2, NumDrives: 2},
		{Name: "c-existing", Node: "n-existing", TlcGiB: 3840, NumCores: 1, NumDrives: 1},
		{Name: "c-own-only", Node: "n-own-only", TlcGiB: 7680, NumCores: 2, NumDrives: 2},
		{Name: "c-unsched", Node: "n-unsched", TlcGiB: 3840, NumCores: 1, NumDrives: 1, Unscheduled: true},
	}
	plan := &capacityplanner.CapacityPlan{
		Create: []capacityplanner.NewContainer{
			{Node: "n-create", FDValue: "n-create", TlcGiB: 7680, NumCores: 2, NumDrives: 2, Type: "tlc"},
		},
		Grow: []capacityplanner.ContainerGrowth{
			{Name: "c-grow", NewTlcGiB: 11520, NewCores: 3, NewNumDrives: 3},
		},
		Warnings: []capacityplanner.Warning{{
			Kind:    capacityplanner.WarningKindNodeIneligible,
			Message: "1 node(s) holding 2 signed free full drive(s) are ineligible for a new drive container: n-ineligible (not ready)",
		}},
	}

	rows := autoFullDrivesNodeRows(nodeInv, existing, plan)
	if len(rows) != 6 {
		t.Fatalf("autoFullDrivesNodeRows() returned %d rows, want 6 (n-empty must be skipped, n-own-only must NOT be); rows=%+v", len(rows), rows)
	}
	byNode := map[string]autoFullDrivesNodeRow{}
	for _, r := range rows {
		byNode[r.Node] = r
	}

	create := byNode["n-create"]
	if create.State != "create" || create.DrivesUsed != 2 || create.DrivesAvail != 2 || create.TlcGiB != 7680 || create.Cores != 2 {
		t.Errorf("create row = %+v, want state=create used=2 avail=2 tlc=7680 cores=2", create)
	}
	if create.Note != "" {
		t.Errorf("create row Note = %q, want empty (used == avail)", create.Note)
	}

	grow := byNode["n-grow"]
	if grow.State != "grow" || grow.DrivesUsed != 3 || grow.DrivesAvail != 3 || grow.TlcGiB != 11520 || grow.Cores != 3 {
		t.Errorf("grow row = %+v, want state=grow used=3 avail=3 tlc=11520 cores=3", grow)
	}

	existingRow := byNode["n-existing"]
	if existingRow.State != "existing" || existingRow.DrivesUsed != 1 || existingRow.DrivesAvail != 1 || existingRow.TlcGiB != 3840 || existingRow.Cores != 1 {
		t.Errorf("existing row = %+v, want state=existing used=1 avail=1 tlc=3840 cores=1", existingRow)
	}

	ineligible := byNode["n-ineligible"]
	if ineligible.State != nodeStateNotPlanned || ineligible.DrivesUsed != 0 || ineligible.DrivesAvail != 2 {
		t.Errorf("unplanned row = %+v, want state=%s used=0 avail=2", ineligible, nodeStateNotPlanned)
	}
	if !strings.Contains(ineligible.Note, "not ready") || !strings.Contains(ineligible.Note, "WARNINGS") {
		t.Errorf("unplanned row Note = %q, want the node's own reason verbatim plus a pointer to WARNINGS", ineligible.Note)
	}

	ownOnly, ok := byNode["n-own-only"]
	if !ok {
		t.Fatalf("n-own-only missing from autoFullDrivesNodeRows() output, want it present (own-claimed-only node must not be skipped as unsigned)")
	}
	if ownOnly.State != "existing" || ownOnly.DrivesUsed != 2 || ownOnly.DrivesAvail != 2 || ownOnly.TlcGiB != 7680 || ownOnly.Cores != 2 {
		t.Errorf("n-own-only row = %+v, want state=existing used=2 avail=2 tlc=7680 cores=2 (avail = len(Own)+len(Free) = 2+0)", ownOnly)
	}
	if ownOnly.Note != "" {
		t.Errorf("n-own-only row Note = %q, want empty (used == avail, nothing held back)", ownOnly.Note)
	}

	unsched := byNode["n-unsched"]
	if unsched.State != "existing" || unsched.DrivesUsed != 1 || unsched.DrivesAvail != 3 {
		t.Errorf("n-unsched row = %+v, want state=existing used=1 avail=3 (no Grow entry is written while the pod is unbound)", unsched)
	}
	if !strings.Contains(unsched.Note, "not been scheduled") {
		t.Errorf("n-unsched row Note = %q, want the deferred-growth reason", unsched.Note)
	}
}

// TestAutoFullDrivesNodeRows_FeasiblePlanPlacesEveryNode is the semantic guard behind the not-planned
// relabel: on a FEASIBLE plan every node with signed drives gets a container holding ALL of them, so no
// row may come back not-planned and none may show used < avail. This is the property the old "dropped"
// state used to violate by design.
func TestAutoFullDrivesNodeRows_FeasiblePlanPlacesEveryNode(t *testing.T) {
	nodeInv := []capacityplanner.NodeCapacity{
		{NodeName: "n1", FDValue: "n1", DriveCapacitiesGiB: []int{3840, 3840, 3840}},
		{NodeName: "n2", FDValue: "n2", DriveCapacitiesGiB: []int{3840, 3840}},
	}
	// A feasible plan: a container per node, each taking every drive its node has, cores capped below the
	// drive count (the search holds CORES back, never drives).
	plan := &capacityplanner.CapacityPlan{
		Create: []capacityplanner.NewContainer{
			{Node: "n1", FDValue: "n1", TlcGiB: 11520, NumCores: 2, NumDrives: 3, Type: "tlc"},
			{Node: "n2", FDValue: "n2", TlcGiB: 7680, NumCores: 2, NumDrives: 2, Type: "tlc"},
		},
	}
	for _, r := range autoFullDrivesNodeRows(nodeInv, nil, plan) {
		if r.State == nodeStateNotPlanned {
			t.Errorf("node %s came back %s on a feasible plan; that state is reachable only when the whole plan is infeasible", r.Node, nodeStateNotPlanned)
		}
		if r.DrivesUsed != r.DrivesAvail {
			t.Errorf("node %s used %d of %d drives; a feasible plan claims every drive (cores, not drives, are what the cap holds back)", r.Node, r.DrivesUsed, r.DrivesAvail)
		}
	}
}

// TestHasFleetWarning covers the helper autoFullDrivesNodeRows uses to point a row's NOTE at the
// fleet-wide DrivesStranded/NodeIneligible warning that explains it.
func TestHasFleetWarning(t *testing.T) {
	stranded := capacityplanner.Warning{Kind: capacityplanner.WarningKindDrivesStranded, Message: "numDrives=2 pinned"}
	other := capacityplanner.Warning{Kind: capacityplanner.WarningKindTransient, Message: "deferred"}

	if !hasFleetWarning([]capacityplanner.Warning{stranded}, capacityplanner.WarningKindDrivesStranded) {
		t.Error("hasFleetWarning() = false, want true for a matching DrivesStranded warning")
	}
	if hasFleetWarning([]capacityplanner.Warning{other}, capacityplanner.WarningKindDrivesStranded) {
		t.Error("hasFleetWarning() = true, want false — no DrivesStranded warning present")
	}
	if hasFleetWarning(nil, capacityplanner.WarningKindDrivesStranded) {
		t.Error("hasFleetWarning(nil) = true, want false")
	}
}

// A not-planned row must take its NOTE from its own node state, never from the presence of a fleet-wide
// NodeIneligible warning: that warning names every ineligible node in the fleet, so keying off its mere
// existence labels EVERY not-planned row "cordoned/not ready/untolerated taint" — including a node skipped
// only because a drive container on it is still being deleted, and every fit-failure node on an infeasible
// plan. Each of the three nodes below is not-planned with used < avail for a different reason, and each must
// get its own answer.
func TestAutoFullDrivesNodeRows_NotPlannedNoteFromNodeStateNotFleetWarning(t *testing.T) {
	nodeInv := []capacityplanner.NodeCapacity{
		{NodeName: "n-cordoned", FDValue: "n-cordoned", DriveCapacitiesGiB: []int{3840}, IneligibleReason: "cordoned"},
		{NodeName: "n-deleting", FDValue: "n-deleting", DriveCapacitiesGiB: []int{3840}, HasDeletingDriveContainer: true},
		// Neither ineligible nor mid-deletion — a plain fit failure, which renderRejectedNodes explains
		// instead. Its NOTE must stay empty rather than borrow n-cordoned's reason.
		{NodeName: "n-unfit", FDValue: "n-unfit", DriveCapacitiesGiB: []int{3840}},
	}
	plan := &capacityplanner.CapacityPlan{
		Warnings: []capacityplanner.Warning{{
			Kind:    capacityplanner.WarningKindNodeIneligible,
			Message: "auto full drives: 1 node(s) holding 1 signed free full drive(s) are ineligible for a new drive container: n-cordoned (cordoned)",
		}},
	}

	byNode := map[string]autoFullDrivesNodeRow{}
	for _, r := range autoFullDrivesNodeRows(nodeInv, nil, plan) {
		if r.State != nodeStateNotPlanned {
			t.Fatalf("row %+v is not %s — the fixture no longer exercises the not-planned branch", r, nodeStateNotPlanned)
		}
		byNode[r.Node] = r
	}

	if got := byNode["n-cordoned"].Note; !strings.Contains(got, "cordoned") {
		t.Errorf("n-cordoned Note = %q, want its own IneligibleReason verbatim", got)
	}
	if got := byNode["n-deleting"].Note; !strings.Contains(got, "being deleted") {
		t.Errorf("n-deleting Note = %q, want the deletion reason — not n-cordoned's condition", got)
	}
	if got := byNode["n-deleting"].Note; strings.Contains(got, "cordoned") {
		t.Errorf("n-deleting Note = %q, must not inherit another node's cordon reason", got)
	}
	if got := byNode["n-unfit"].Note; got != "" {
		t.Errorf("n-unfit Note = %q, want empty — no per-node condition applies, so nothing to attribute", got)
	}
}

// TestAutoFullDrivesNodeRows_StrandedNodeNoteFromFleetWarning covers §8b: a node stranded by a numDrives
// pin (used < avail) — formatStrandedWarning aggregates every stranded node into one fleet-wide warning
// rather than fanning one out per node — so the NOTE column must fall back to a pointer at that fleet
// warning instead of staying empty.
func TestAutoFullDrivesNodeRows_StrandedNodeNoteFromFleetWarning(t *testing.T) {
	nodeInv := []capacityplanner.NodeCapacity{
		{NodeName: "n1", FDValue: "n1", DriveCapacitiesGiB: []int{3840, 3840, 3840}},
	}
	plan := &capacityplanner.CapacityPlan{
		Create: []capacityplanner.NewContainer{{Node: "n1", FDValue: "n1", TlcGiB: 7680, NumCores: 2, NumDrives: 2, Type: "tlc"}},
		Warnings: []capacityplanner.Warning{
			{Kind: capacityplanner.WarningKindDrivesStranded, Message: "numDrives=2 pinned; n1 (2 of 3)"},
		},
	}
	rows := autoFullDrivesNodeRows(nodeInv, nil, plan)
	if len(rows) != 1 {
		t.Fatalf("autoFullDrivesNodeRows() = %d rows, want 1", len(rows))
	}
	row := rows[0]
	if row.DrivesUsed >= row.DrivesAvail {
		t.Fatalf("test setup: row %+v does not actually strand (used must be < avail)", row)
	}
	if row.Note == "" {
		t.Errorf("autoFullDrivesNodeRows() row %+v: Note is empty, want a pointer to the fleet DrivesStranded warning", row)
	}
	if !strings.Contains(row.Note, "WARNINGS") {
		t.Errorf("autoFullDrivesNodeRows() Note = %q, want it to point at the WARNINGS section", row.Note)
	}
}

// TestAutoFullDrivesNodeRows_NotPlannedRowGetsNoStrandedNote guards the row.State != nodeStateNotPlanned
// gate: a node the walk never sized at all (reachable only on an infeasible plan) must not be
// misattributed to a numDrives pin just because a fleet DrivesStranded warning happens to be present.
func TestAutoFullDrivesNodeRows_NotPlannedRowGetsNoStrandedNote(t *testing.T) {
	nodeInv := []capacityplanner.NodeCapacity{
		{NodeName: "n1", FDValue: "n1", DriveCapacitiesGiB: []int{3840, 3840}},
	}
	plan := &capacityplanner.CapacityPlan{
		Infeasible: "some other node cannot fit",
		Warnings: []capacityplanner.Warning{
			{Kind: capacityplanner.WarningKindDrivesStranded, Message: "numDrives=2 pinned elsewhere"},
		},
	}
	rows := autoFullDrivesNodeRows(nodeInv, nil, plan)
	if len(rows) != 1 {
		t.Fatalf("autoFullDrivesNodeRows() = %d rows, want 1", len(rows))
	}
	if rows[0].State != nodeStateNotPlanned {
		t.Fatalf("test setup: row %+v, want State=%s", rows[0], nodeStateNotPlanned)
	}
	if rows[0].Note != "" {
		t.Errorf("autoFullDrivesNodeRows() Note = %q, want empty on a not-planned row even with a fleet warning present", rows[0].Note)
	}
}

// TestAutoFullDrivesNodeRows_ConditionWithoutWarning_NoWarningsPointer is the regression for the
// mid-walk-abort shape: the walk stops collecting fleet warnings the moment it hits an infeasible node
// (autofulldrives.go), so a node sorting after it can carry a condition in the inventory with
// plan.Warnings left empty. Each of the three gated arms must still surface the node's own reason, but
// none may point at a WARNINGS section that was never written.
func TestAutoFullDrivesNodeRows_ConditionWithoutWarning_NoWarningsPointer(t *testing.T) {
	nodeInv := []capacityplanner.NodeCapacity{
		{NodeName: "n-ineligible", FDValue: "n-ineligible", DriveCapacitiesGiB: []int{3840, 3840}, IneligibleReason: "cordoned"},
		{NodeName: "n-deleting", FDValue: "n-deleting", DriveCapacitiesGiB: []int{3840, 3840}, HasDeletingDriveContainer: true},
		{NodeName: "n-unsched", FDValue: "n-unsched", DriveCapacitiesGiB: []int{3840, 3840}},
	}
	existing := []capacityplanner.ExistingContainer{
		{Name: "c-unsched", Node: "n-unsched", TlcGiB: 3840, NumCores: 1, NumDrives: 1, Unscheduled: true},
	}
	plan := &capacityplanner.CapacityPlan{
		Infeasible: "some other node cannot fit",
		Warnings:   nil,
	}

	byNode := map[string]autoFullDrivesNodeRow{}
	for _, r := range autoFullDrivesNodeRows(nodeInv, existing, plan) {
		byNode[r.Node] = r
	}

	ineligible := byNode["n-ineligible"]
	if !strings.Contains(ineligible.Note, "cordoned") {
		t.Errorf("n-ineligible Note = %q, want its own IneligibleReason verbatim", ineligible.Note)
	}
	if strings.Contains(ineligible.Note, "WARNINGS") {
		t.Errorf("n-ineligible Note = %q, must not point at WARNINGS — plan.Warnings is empty", ineligible.Note)
	}

	deleting := byNode["n-deleting"]
	if !strings.Contains(deleting.Note, "being deleted") {
		t.Errorf("n-deleting Note = %q, want the deletion reason", deleting.Note)
	}
	if strings.Contains(deleting.Note, "WARNINGS") {
		t.Errorf("n-deleting Note = %q, must not point at WARNINGS — plan.Warnings is empty", deleting.Note)
	}

	unsched := byNode["n-unsched"]
	if !strings.Contains(unsched.Note, "not been scheduled") {
		t.Errorf("n-unsched Note = %q, want the deferred-growth reason", unsched.Note)
	}
	if strings.Contains(unsched.Note, "WARNINGS") {
		t.Errorf("n-unsched Note = %q, must not point at WARNINGS — plan.Warnings is empty", unsched.Note)
	}
}

// TestAutoFullDrivesDriveGrowDiff covers the drive-count transition (FromNumDrives/ToNumDrives)
// alongside TLC/cores, joined by container name — including the newly-possible growths where drives and
// cores move independently.
func TestAutoFullDrivesDriveGrowDiff(t *testing.T) {
	existing := []capacityplanner.ExistingContainer{
		{Name: "c-both", Node: "n1", TlcGiB: 7680, NumCores: 2, NumDrives: 2},
		{Name: "c-drives-only", Node: "n2", TlcGiB: 7680, NumCores: 2, NumDrives: 2},
		{Name: "c-cores-only", Node: "n3", TlcGiB: 11520, NumCores: 2, NumDrives: 3},
	}
	grow := []capacityplanner.ContainerGrowth{
		{Name: "c-both", NewTlcGiB: 11520, NewCores: 3, NewNumDrives: 3},
		// Drives rise, cores stay: free (zero resource delta), applies live, no pod restart.
		{Name: "c-drives-only", NewTlcGiB: 15360, NewCores: 2, NewNumDrives: 4},
		// Cores rise, drives stay: newly possible when a later reconcile finds a higher feasible cap.
		{Name: "c-cores-only", NewTlcGiB: 11520, NewCores: 3, NewNumDrives: 3},
	}
	rows := autoFullDrivesDriveGrowDiff(existing, grow)
	if len(rows) != 3 {
		t.Fatalf("autoFullDrivesDriveGrowDiff() = %+v, want 3 rows", rows)
	}
	byName := map[string]autoFullDrivesDriveGrowRow{}
	for _, r := range rows {
		byName[r.Name] = r
	}

	both := byName["c-both"]
	if both.Node != "n1" || both.FromTlcGiB != 7680 || both.ToTlcGiB != 11520 ||
		both.FromCores != 2 || both.ToCores != 3 || both.FromNumDrives != 2 || both.ToNumDrives != 3 {
		t.Errorf("c-both row = %+v, want full from->to transition incl. drive count", both)
	}

	drivesOnly := byName["c-drives-only"]
	if drivesOnly.FromNumDrives != 2 || drivesOnly.ToNumDrives != 4 || drivesOnly.FromCores != drivesOnly.ToCores {
		t.Errorf("c-drives-only row = %+v, want drives 2->4 with cores unchanged (drive-only growth is free)", drivesOnly)
	}

	coresOnly := byName["c-cores-only"]
	if coresOnly.FromCores != 2 || coresOnly.ToCores != 3 || coresOnly.FromNumDrives != coresOnly.ToNumDrives {
		t.Errorf("c-cores-only row = %+v, want cores 2->3 with drives unchanged (cores-only growth is newly possible)", coresOnly)
	}
}

// TestAutoFullDrivesPlanSummary covers three cases: no signed drives, steady state (with/without
// warnings), and normal create/grow.
func TestAutoFullDrivesPlanSummary(t *testing.T) {
	noSigned := []capacityplanner.NodeCapacity{{NodeName: "n1", DriveCapacitiesGiB: nil}}
	if got := autoFullDrivesPlanSummary(&capacityplanner.CapacityPlan{}, noSigned); !strings.Contains(got, "no node has signed full drives yet") {
		t.Errorf("autoFullDrivesPlanSummary() = %q, want the no-signed-drives message", got)
	}

	// Regression for plan.go §7: an own-claimed node (Own nonzero, Drive empty) must still count as signed.
	ownOnlySigned := []capacityplanner.NodeCapacity{{NodeName: "n1", OwnDriveCapacitiesGiB: []int{3840, 3840}, DriveCapacitiesGiB: nil}}
	if got := autoFullDrivesPlanSummary(&capacityplanner.CapacityPlan{}, ownOnlySigned); strings.Contains(got, "no node has signed full drives yet") {
		t.Errorf("autoFullDrivesPlanSummary() = %q, must not claim no signed drives when a node's own drives are entirely claimed (own-only)", got)
	} else if !strings.Contains(got, "steady state") {
		t.Errorf("autoFullDrivesPlanSummary() = %q, want steady state (own-only node is signed, nothing to create/grow)", got)
	}

	signedSteady := []capacityplanner.NodeCapacity{{NodeName: "n1", DriveCapacitiesGiB: []int{3840}}}
	steady := autoFullDrivesPlanSummary(&capacityplanner.CapacityPlan{}, signedSteady)
	if !strings.Contains(steady, "steady state") {
		t.Errorf("autoFullDrivesPlanSummary() = %q, want steady state (no create/grow, signed drives exist)", steady)
	}
	if strings.Contains(steady, "warning") {
		t.Errorf("autoFullDrivesPlanSummary() = %q, want no warning mention when Warnings is empty", steady)
	}

	steadyWithWarnings := autoFullDrivesPlanSummary(&capacityplanner.CapacityPlan{Warnings: []capacityplanner.Warning{{Kind: capacityplanner.WarningKindTransient, Message: "placement deferred"}}}, signedSteady)
	if !strings.Contains(steadyWithWarnings, "steady state") || !strings.Contains(steadyWithWarnings, "1 warning") {
		t.Errorf("autoFullDrivesPlanSummary() = %q, want steady state AND a warning count", steadyWithWarnings)
	}

	normal := autoFullDrivesPlanSummary(&capacityplanner.CapacityPlan{
		Create: []capacityplanner.NewContainer{{Node: "n1", TlcGiB: 3840}},
		Grow:   []capacityplanner.ContainerGrowth{{Name: "c1"}},
	}, signedSteady)
	for _, want := range []string{"would create 1 drive container", "would grow 1 drive container"} {
		if !strings.Contains(normal, want) {
			t.Errorf("autoFullDrivesPlanSummary() = %q, missing %q", normal, want)
		}
	}
}

// TestAutoFullDrivesPlanSummary_Infeasible covers §8a: the INFEASIBLE branch must be checked first, so
// it leads with "INFEASIBLE", names the blocking pool, and labels a partial Create as diagnostic-only —
// mirroring planSummary's own infeasible branch. Before this branch existed, an infeasible plan with a
// partial Create fell through to the "would create N" wording below with no caveat, reading as an
// applied plan.
func TestAutoFullDrivesPlanSummary_Infeasible(t *testing.T) {
	nodeInv := []capacityplanner.NodeCapacity{{NodeName: "n1", DriveCapacitiesGiB: []int{3840}}}

	t.Run("names the blocking pool and labels partial placement diagnostic-only", func(t *testing.T) {
		plan := &capacityplanner.CapacityPlan{
			Infeasible:    "1 node cannot host a container sized for all its drives",
			Infeasibility: &capacityplanner.InfeasibilityReport{Pool: "compute"},
			Create:        []capacityplanner.NewContainer{{Node: "n2", TlcGiB: 3840}},
		}
		got := autoFullDrivesPlanSummary(plan, nodeInv)
		if !strings.HasPrefix(got, "INFEASIBLE") {
			t.Errorf("autoFullDrivesPlanSummary() = %q, want it to lead with INFEASIBLE", got)
		}
		for _, want := range []string{"Blocking pool: compute", "diagnostic only", "will NOT be applied"} {
			if !strings.Contains(got, want) {
				t.Errorf("autoFullDrivesPlanSummary() = %q, missing %q", got, want)
			}
		}
		if strings.Contains(got, "would create") {
			t.Errorf("autoFullDrivesPlanSummary() = %q, the \"would create N\" phrasing must be unreachable on an infeasible plan", got)
		}
	})

	t.Run("no placement at all", func(t *testing.T) {
		plan := &capacityplanner.CapacityPlan{
			Infeasible:    "no node has enough headroom",
			Infeasibility: &capacityplanner.InfeasibilityReport{Pool: "drives"},
		}
		got := autoFullDrivesPlanSummary(plan, nodeInv)
		if !strings.Contains(got, "No placement could be made") {
			t.Errorf("autoFullDrivesPlanSummary() = %q, want the no-placement wording with an empty Create", got)
		}
	})
}

// TestRenderAutoFullDrivesPlanText covers the NODES table (drives used/avail per row) and that an
// unplanned node's row shows its Note, end to end through the renderer.
func TestRenderAutoFullDrivesPlanText(t *testing.T) {
	d := autoFullDrivesPlanData{
		Cluster: "test-cluster",
		Plan: &capacityplanner.CapacityPlan{
			Warnings: []capacityplanner.Warning{{Kind: capacityplanner.WarningKindTransient, Message: "node n-deferred still hosts a drive container being deleted — placement deferred"}},
		},
		Nodes: []autoFullDrivesNodeRow{
			{Node: "n-create", FD: "n-create", DrivesUsed: 2, DrivesAvail: 2, TlcGiB: 7680, Cores: 2, State: "create"},
			{Node: "n-deferred", FD: "n-deferred", DrivesUsed: 0, DrivesAvail: 2, State: nodeStateNotPlanned,
				Note: "node n-deferred still hosts a drive container being deleted — placement deferred"},
		},
		Summary: "daemonset plan: would create 1 drive container(s) across 1 node(s), placing 7.5TiB",
	}
	out := renderAutoFullDrivesPlanText(&d)
	for _, want := range []string{
		"CLUSTER test-cluster (daemonset / auto full drives)",
		"NODES",
		"n-create", "2/2",
		"n-deferred", "0/2",
		nodeStateNotPlanned,
		"placement deferred", // the Note visible right on the row
		"WARNINGS",
		"SUMMARY",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("renderAutoFullDrivesPlanText() missing %q\n---\n%s", want, out)
		}
	}
	// The old label must not come back: it described drive-dropping, which no longer happens.
	if strings.Contains(out, "dropped") {
		t.Errorf("renderAutoFullDrivesPlanText() still prints \"dropped\"; the planner cannot drop drives any more\n---\n%s", out)
	}
}

// TestRenderAutoFullDrivesPlanText_RejectedNodes covers the INFEASIBLE node table, which is the primary
// diagnostic in this mode (one node that cannot fit its drives kills the whole plan) and was previously
// rendered only on the clusterCapacity path. Pins the unit handling: a CPU count must NOT be humanized
// as GiB.
func TestRenderAutoFullDrivesPlanText_RejectedNodes(t *testing.T) {
	d := autoFullDrivesPlanData{
		Cluster: "c1",
		Plan: &capacityplanner.CapacityPlan{
			Infeasible: "1 node cannot host a container sized for all its drives",
			Infeasibility: &capacityplanner.InfeasibilityReport{
				Reason: "1 node cannot host a container sized for all its drives",
				RejectedNodes: []capacityplanner.NodeRejection{
					{Node: "n-cpu", Binding: "physical CPU", Needed: 20, Available: 16, Unit: "physical CPU"},
					{Node: "n-hp", Binding: "hugepages", Needed: 31616, Available: 24000, Unit: "MiB hugepages"},
				},
				Fixes: []string{"pin driveCores lower — you keep every drive and run them on fewer cores"},
			},
		},
	}
	out := renderAutoFullDrivesPlanText(&d)
	for _, want := range []string{
		"n-cpu", "16 physical CPU", "20 physical CPU",
		"n-hp", "24000 MiB hugepages", "31616 MiB hugepages",
		"pin driveCores lower",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("renderAutoFullDrivesPlanText() missing %q\n---\n%s", want, out)
		}
	}
	// The regression this guards: HumanReadableGiB(16) would render "16 GiB" for a CPU count.
	if strings.Contains(out, "16 GiB") || strings.Contains(out, "24000 GiB") {
		t.Errorf("renderAutoFullDrivesPlanText() ran a non-capacity quantity through the GiB humanizer\n---\n%s", out)
	}
}

// TestRenderPlanText_RejectedNodesCapacityUnit pins that the shared table still humanizes a CAPACITY
// rejection (empty Unit) as GiB — the clusterCapacity path must not regress from the unit-aware change.
func TestRenderPlanText_RejectedNodesCapacityUnit(t *testing.T) {
	d := planData{
		Cluster: "c1",
		Plan: &capacityplanner.CapacityPlan{
			Infeasible: "not enough capacity",
			Infeasibility: &capacityplanner.InfeasibilityReport{
				Reason: "not enough capacity",
				RejectedNodes: []capacityplanner.NodeRejection{
					{Node: "n1", Binding: "capacity", FreeGiB: 1024, NeededGiB: 384},
				},
			},
		},
	}
	out := renderPlanText(&d)
	for _, want := range []string{"n1", "1.0TiB", "384.0GiB"} {
		if !strings.Contains(out, want) {
			t.Errorf("renderPlanText() missing %q — a capacity rejection (empty Unit) must still humanize FreeGiB/NeededGiB\n---\n%s", want, out)
		}
	}
}

// TestRenderAutoFullDrivesPlanText_DriveSizing covers the DRIVE SIZING section: the flat sizing
// statement, and its position between INFEASIBLE and NODES. There is no longer a "capped" variant to
// cover — drive cores are derived once and never traded for compute, so the section has a single shape.
func TestRenderAutoFullDrivesPlanText_DriveSizing(t *testing.T) {
	sizing := func() *capacityplanner.DriveSizingRationale {
		return &capacityplanner.DriveSizingRationale{
			Reason:                   "48 drive core(s) across 8 node(s); 96 compute core(s) at the 2.0 full-drives ratio",
			DrivesTaken:              48,
			DrivesAvailable:          48,
			TlcGiBTaken:              686736,
			TlcGiBAvailable:          686736,
			TotalTlcDriveCores:       48,
			RequiredComputeCores:     96,
			ComputeContainers:        18,
			ComputeCoresPerContainer: 6,
			ComputeHugepagesMiB:      49652,
		}
	}

	t.Run("prints the sizing breakdown", func(t *testing.T) {
		d := autoFullDrivesPlanData{Cluster: "c1", Plan: &capacityplanner.CapacityPlan{DriveSizing: sizing()}}
		out := renderAutoFullDrivesPlanText(&d)
		for _, want := range []string{
			"DRIVE SIZING",
			"drives: 48/48 taken",
			"drive cores: 48",
			"compute cores required: 96",
			"compute: 18 container(s), 6 cores/container, 49652 MiB hugepages",
			"rationale: 48 drive core(s) across 8 node(s)",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("renderAutoFullDrivesPlanText() missing %q\n---\n%s", want, out)
			}
		}
	})

	// The cap vocabulary must not come back. Each of these described the co-sizing search, which no
	// longer exists; printing any of them would tell an operator that cores were traded away to fit
	// compute, which is exactly the behaviour this mode refuses.
	t.Run("no cap vocabulary survives", func(t *testing.T) {
		d := autoFullDrivesPlanData{Cluster: "c1", Plan: &capacityplanner.CapacityPlan{DriveSizing: sizing()}}
		out := renderAutoFullDrivesPlanText(&d)
		for _, forbidden := range []string{
			"cap:", "limited by:", "attempts:", "LIMITED NODES", "unconstrained",
			"held back", "unbounded", "CORES(used/",
		} {
			if strings.Contains(out, forbidden) {
				t.Errorf("renderAutoFullDrivesPlanText() still prints %q — the co-sizing search is gone\n---\n%s", forbidden, out)
			}
		}
	})

	// Every drive claimed is the mode's defining guarantee; the section must show it plainly rather
	// than leaving a reader to infer it.
	t.Run("drives taken equals drives available on a normal plan", func(t *testing.T) {
		d := autoFullDrivesPlanData{Cluster: "c1", Plan: &capacityplanner.CapacityPlan{DriveSizing: sizing()}}
		if out := renderAutoFullDrivesPlanText(&d); !strings.Contains(out, "48/48") {
			t.Errorf("renderAutoFullDrivesPlanText() should show drives taken == available\n---\n%s", out)
		}
	})

	t.Run("infeasible: DRIVE SIZING appears between INFEASIBLE and NODES", func(t *testing.T) {
		d := autoFullDrivesPlanData{
			Cluster: "c1",
			Plan: &capacityplanner.CapacityPlan{
				Infeasible: "the fleet cannot host the compute these drive cores require",
				Infeasibility: &capacityplanner.InfeasibilityReport{
					Reason: "the fleet cannot host the compute these drive cores require",
				},
				DriveSizing: sizing(),
			},
			Nodes: []autoFullDrivesNodeRow{
				{Node: "n1", FD: "n1", DrivesUsed: 3, DrivesAvail: 3, State: "create"},
			},
		}
		out := renderAutoFullDrivesPlanText(&d)
		infeasibleIdx := strings.Index(out, "INFEASIBLE")
		sizingIdx := strings.Index(out, "DRIVE SIZING")
		nodesIdx := strings.Index(out, "\nNODES")
		if infeasibleIdx < 0 || sizingIdx < 0 || nodesIdx < 0 {
			t.Fatalf("renderAutoFullDrivesPlanText() missing one of INFEASIBLE/DRIVE SIZING/NODES\n---\n%s", out)
		}
		if !(infeasibleIdx < sizingIdx && sizingIdx < nodesIdx) {
			t.Errorf("want section order INFEASIBLE < DRIVE SIZING < NODES, got indices %d, %d, %d\n---\n%s",
				infeasibleIdx, sizingIdx, nodesIdx, out)
		}
	})
}

// TestRenderAutoFullDrivesPlanJSON_DriveSizing pins that "driveSizing" is always present, and that the
// payload's mode carries the new name.
func TestRenderAutoFullDrivesPlanJSON_DriveSizing(t *testing.T) {
	d := autoFullDrivesPlanData{
		Cluster: "c1",
		Plan: &capacityplanner.CapacityPlan{
			DriveSizing: &capacityplanner.DriveSizingRationale{
				Reason:                   "48 drive core(s) across 8 node(s); 96 compute core(s) at the 2.0 full-drives ratio",
				DrivesTaken:              48,
				DrivesAvailable:          48,
				TotalTlcDriveCores:       48,
				RequiredComputeCores:     96,
				ComputeContainers:        18,
				ComputeCoresPerContainer: 6,
			},
		},
	}
	out, err := renderAutoFullDrivesPlanJSON(&d)
	if err != nil {
		t.Fatalf("renderAutoFullDrivesPlanJSON() unexpected error: %v", err)
	}

	var parsed map[string]any
	if unmarshalErr := json.Unmarshal([]byte(out), &parsed); unmarshalErr != nil {
		t.Fatalf("renderAutoFullDrivesPlanJSON() produced invalid JSON: %v\n---\n%s", unmarshalErr, out)
	}
	if parsed["mode"] != "autoFullDrives" {
		t.Errorf("mode = %v, want %q", parsed["mode"], "autoFullDrives")
	}
	sizing, ok := parsed["driveSizing"].(map[string]any)
	if !ok {
		t.Fatalf("driveSizing key missing or not an object in %s", out)
	}
	for key, want := range map[string]any{
		"drivesTaken": float64(48), "drivesAvailable": float64(48),
		"totalTlcDriveCores": float64(48), "requiredComputeCores": float64(96),
		"computeContainers": float64(18), "computeCoresPerContainer": float64(6),
	} {
		if got := sizing[key]; got != want {
			t.Errorf("driveSizing.%s = %v, want %v", key, got, want)
		}
	}
	// The cap-era keys must be absent from the payload, not merely zero: a consumer that still reads
	// limitedBy would silently see "" and conclude the plan was unconstrained by a search that no
	// longer exists.
	for _, gone := range []string{"limitedBy", "cap", "startCap", "attempts", "cappedNodes"} {
		if _, present := sizing[gone]; present {
			t.Errorf("driveSizing still carries the cap-era key %q: %s", gone, out)
		}
	}

	// Regression: driveSizing key must be present (as null) even with no DriveSizing set (unlike the text renderer).
	dNil := autoFullDrivesPlanData{Cluster: "c1", Plan: &capacityplanner.CapacityPlan{}}
	outNil, err := renderAutoFullDrivesPlanJSON(&dNil)
	if err != nil {
		t.Fatalf("renderAutoFullDrivesPlanJSON() unexpected error: %v", err)
	}
	var parsedNil map[string]any
	if err := json.Unmarshal([]byte(outNil), &parsedNil); err != nil {
		t.Fatalf("renderAutoFullDrivesPlanJSON() produced invalid JSON: %v\n---\n%s", err, outNil)
	}
	if v, ok := parsedNil["driveSizing"]; !ok {
		t.Errorf("driveSizing key must be present even when Plan.DriveSizing is nil, got: %s", outNil)
	} else if v != nil {
		t.Errorf("driveSizing = %v, want null when Plan.DriveSizing is nil", v)
	}
}

// TestRenderPlanJSON_NodeRejectionUnit pins the JSON side of the unit fix: a non-capacity rejection
// carries needed/available/unit, and a capacity rejection omits them entirely (payload unchanged).
func TestRenderPlanJSON_NodeRejectionUnit(t *testing.T) {
	d := planData{
		Cluster: "c1",
		Plan: &capacityplanner.CapacityPlan{
			Infeasible: "node fit",
			Infeasibility: &capacityplanner.InfeasibilityReport{
				Reason: "node fit",
				RejectedNodes: []capacityplanner.NodeRejection{
					{Node: "n-cpu", Binding: "physical CPU", Needed: 20, Available: 16, Unit: "physical CPU"},
					{Node: "n-cap", Binding: "capacity", FreeGiB: 100, NeededGiB: 384},
				},
			},
		},
	}
	out, err := renderPlanJSON(&d)
	if err != nil {
		t.Fatalf("renderPlanJSON() unexpected error: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("renderPlanJSON() produced invalid JSON: %v\n---\n%s", err, out)
	}
	infeas, ok := parsed["infeasibility"].(map[string]any)
	if !ok {
		t.Fatalf("infeasibility missing in %s", out)
	}
	rejected, ok := infeas["rejectedNodes"].([]any)
	if !ok || len(rejected) != 2 {
		t.Fatalf("rejectedNodes missing or not a 2-element array in %s", out)
	}

	cpu, _ := rejected[0].(map[string]any)
	if cpu["unit"] != "physical CPU" || cpu["needed"] != float64(20) || cpu["available"] != float64(16) {
		t.Errorf("rejectedNodes[0] = %v, want needed=20 available=16 unit=%q", cpu, "physical CPU")
	}

	capRej, _ := rejected[1].(map[string]any)
	for _, key := range []string{"needed", "available", "unit"} {
		if _, present := capRej[key]; present {
			t.Errorf("rejectedNodes[1] (a capacity rejection) must omit %q, got %v", key, capRej)
		}
	}
	if capRej["freeGiB"] != float64(100) || capRej["neededGiB"] != float64(384) {
		t.Errorf("rejectedNodes[1] = %v, want freeGiB=100 neededGiB=384 unchanged", capRej)
	}
}

// TestRenderAutoFullDrivesPlanJSON_DriveRowsAreCamelCase pins the JSON contract for the drive-side
// arrays: createDrive/growDrive serialise allocator types directly and shipped without json tags, so
// keys came out Capitalized while every sibling array was camelCase — pinned per-key here.
func TestRenderAutoFullDrivesPlanJSON_DriveRowsAreCamelCase(t *testing.T) {
	d := autoFullDrivesPlanData{
		Cluster: "c1",
		Plan: &capacityplanner.CapacityPlan{
			Create: []capacityplanner.NewContainer{{
				Node: "n1", FDValue: "fd1", TlcGiB: 42921, QlcGiB: 0,
				NumCores: 9, Type: "tlc", NumDrives: 3,
			}},
			Grow: []capacityplanner.ContainerGrowth{{
				Name: "c-drive-1", NewTlcGiB: 57228, NewQlcGiB: 0,
				NewCores: 12, NewNumDrives: 4,
			}},
		},
	}
	out, err := renderAutoFullDrivesPlanJSON(&d)
	if err != nil {
		t.Fatalf("renderAutoFullDrivesPlanJSON() unexpected error: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("renderAutoFullDrivesPlanJSON() produced invalid JSON: %v\n---\n%s", err, out)
	}

	createRows, ok := parsed["createDrive"].([]any)
	if !ok || len(createRows) != 1 {
		t.Fatalf("createDrive missing or not a 1-element array in %s", out)
	}
	create, ok := createRows[0].(map[string]any)
	if !ok {
		t.Fatalf("createDrive[0] is not an object in %s", out)
	}
	for key, want := range map[string]any{
		"node": "n1", "fdValue": "fd1", "tlcGiB": float64(42921), "qlcGiB": float64(0),
		"numCores": float64(9), "type": "tlc", "numDrives": float64(3),
	} {
		got, present := create[key]
		if !present {
			t.Errorf("createDrive[0] missing camelCase key %q (keys: %v)", key, mapKeys(create))
			continue
		}
		if got != want {
			t.Errorf("createDrive[0].%s = %v, want %v", key, got, want)
		}
	}

	growRows, ok := parsed["growDrive"].([]any)
	if !ok || len(growRows) != 1 {
		t.Fatalf("growDrive missing or not a 1-element array in %s", out)
	}
	grow, ok := growRows[0].(map[string]any)
	if !ok {
		t.Fatalf("growDrive[0] is not an object in %s", out)
	}
	for key, want := range map[string]any{
		"name": "c-drive-1", "newTlcGiB": float64(57228), "newQlcGiB": float64(0),
		"newCores": float64(12), "newNumDrives": float64(4),
	} {
		got, present := grow[key]
		if !present {
			t.Errorf("growDrive[0] missing camelCase key %q (keys: %v)", key, mapKeys(grow))
			continue
		}
		if got != want {
			t.Errorf("growDrive[0].%s = %v, want %v", key, got, want)
		}
	}

	// Guard the specific regression: no Capitalized Go field name may survive into the payload.
	for _, row := range []map[string]any{create, grow} {
		for _, bad := range []string{"Node", "FDValue", "TlcGiB", "QlcGiB", "NumCores", "NumDrives", "Type", "Name", "NewCores", "NewTlcGiB", "NewQlcGiB", "NewNumDrives"} {
			if _, present := row[bad]; present {
				t.Errorf("found untagged Go field name %q in JSON payload -- json tags were lost: %s", bad, out)
			}
		}
	}
}

func mapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestAutoFullDrivesNodeRows_ComputeDeletingNote covers the NOTE for the compute-blocked deferral. The
// contract for the column is that any row with used < avail explains the gap, and this cause reaches it on
// both paths: a create that never happens, and a growth held at the container's current size. Without the
// case the row shows a bare gap while WARNINGS names the node.
func TestAutoFullDrivesNodeRows_ComputeDeletingNote(t *testing.T) {
	nodeInv := []capacityplanner.NodeCapacity{
		// create deferred: no container of ours yet, nothing planned.
		{NodeName: "n-blk-create", FDValue: "n-blk-create", DriveCapacitiesGiB: []int{3840, 3840},
			HasDeletingComputeContainer: true},
		// growth deferred: holds 1 of its 3 drives, no Grow entry written this pass.
		{NodeName: "n-blk-grow", FDValue: "n-blk-grow", OwnDriveCapacitiesGiB: []int{3840},
			DriveCapacitiesGiB: []int{3840, 3840}, HasDeletingComputeContainer: true},
	}
	existing := []capacityplanner.ExistingContainer{
		{Name: "c-blk-grow", Node: "n-blk-grow", TlcGiB: 3840, NumCores: 1, NumDrives: 1},
	}
	plan := &capacityplanner.CapacityPlan{}

	byNode := map[string]autoFullDrivesNodeRow{}
	for _, r := range autoFullDrivesNodeRows(nodeInv, existing, plan) {
		byNode[r.Node] = r
	}

	for _, node := range []string{"n-blk-create", "n-blk-grow"} {
		row, ok := byNode[node]
		if !ok {
			t.Fatalf("%s: missing row", node)
		}
		if row.DrivesUsed >= row.DrivesAvail {
			t.Fatalf("%s: fixture must leave a gap to explain, got used=%d avail=%d", node, row.DrivesUsed, row.DrivesAvail)
		}
		if !strings.Contains(row.Note, "compute container being deleted") {
			t.Errorf("%s: Note = %q, want it to name the deleting compute container", node, row.Note)
		}
	}
}
