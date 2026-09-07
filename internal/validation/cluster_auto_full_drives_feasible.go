package validation

import (
	"context"
	"fmt"
	"strings"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/capacityplanner"
	"github.com/weka/weka-operator/internal/capacityplanner/inventory"
	globalconfig "github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/services/discovery"
)

// clusterAutoFullDrivesFeasible rejects an auto-full-drives cluster whose plan the planner would
// declare infeasible — a plan that converges on nothing, leaving the cluster with no containers and an
// AutoFullDrivesInfeasible event. It runs the planner itself (the same FullDrivesInventory +
// PlanAutoFullDrives pair the controller runs in funcs_fd_planning.go, and weka-capacity's dry run) and
// reports the planner's own verdict, rather than projecting what that verdict would be. A projection
// has to be conservative where the planner is exact, and it silently drifts as the planner changes.
//
// The verdict depends on live fleet state — signed drives, running containers, foreign pods holding
// node resources — so it can change for an unedited spec. That is the same state the controller plans
// against: anything this admits and the fleet later cannot host still surfaces at runtime.
//
// Reads go through the manager's cached client, so the inventory walk is an informer-store scan rather
// than a burst of API calls.
type clusterAutoFullDrivesFeasible struct{}

func (clusterAutoFullDrivesFeasible) ID() string { return "cluster_auto_full_drives_feasible" }

func (clusterAutoFullDrivesFeasible) Validate(ctx context.Context, c client.Client, obj runtime.Object) field.ErrorList {
	cluster, ok := obj.(*weka.WekaCluster)
	if !ok {
		return nil
	}
	// Nil dynamicTemplate is auto-full-drives mode (nothing was set); UsesAutoFullDrives is nil-safe.
	dyn := cluster.Spec.Dynamic
	if !dyn.UsesAutoFullDrives() {
		return nil
	}
	base := field.NewPath("spec", "dynamicTemplate")

	// Too few matched nodes is clusterAutoFullDrivesMinNodes' territory, and it names the offending
	// selector and the labels to add. Recorded here, weighed against the planner's verdict below.
	below, err := selectorBelowFormClusterFloor(ctx, c, cluster)
	if err != nil {
		// Fail CLOSED, as clusterSizingModeFlip does: treating a List failure as "floors are met" would
		// let a below-floor compute layout be reported in the wrong voice.
		return field.ErrorList{field.InternalError(base, err)}
	}

	own, err := clusterOwnContainers(ctx, c, cluster)
	if err != nil {
		return field.ErrorList{field.InternalError(base, err)}
	}

	cons := allocator.ConstraintsForClusterSpec(&cluster.Spec)
	fdByNode, nodeInv, computeNodes, err := inventory.NewCollector(c).FullDrivesInventory(ctx, cluster, own, cons)
	if err != nil {
		return field.ErrorList{field.InternalError(base, fmt.Errorf("collecting full-drives inventory: %w", err))}
	}
	if !inventory.HasSignedFullDrives(nodeInv) {
		// No drive reached the inventory: either nothing is signed yet — labelling and drive-signing are
		// independent, so that is a normal bootstrap state rather than a bad spec — or every signed drive is
		// still held by a drive container being deleted. Admitting is right for both; the controller tells
		// them apart because it has to decide whether to wait, and clusterDrivesUnsignedAdvisory owns saying
		// that nothing is signed.
		return nil
	}

	var desired capacityplanner.AutoFullDrivesDesired
	if dyn != nil {
		// Container counts are unrepresentable in this mode — setting either is what takes a template out
		// of it — so only the three pins carry over.
		desired = capacityplanner.AutoFullDrivesDesired{
			ComputeCores: dyn.ComputeCores,
			DriveCores:   dyn.DriveCores,
			NumDrives:    dyn.NumDrives,
		}
	}

	plan := capacityplanner.PlanAutoFullDrives(
		desired,
		inventory.ExistingDrives(ctx, cluster, own, fdByNode),
		inventory.ExistingCompute(ctx, own),
		nodeInv, computeNodes, cons,
	)
	report := plan.Infeasibility
	if report == nil {
		return nil
	}
	// A below-floor selector reaches the planner as a compute-layout failure — the same misconfiguration
	// clusterAutoFullDrivesMinNodes reports in a more actionable voice, so that verdict alone defers. A
	// drive-pool report stands: PlanAutoFullDrives gates the compute leg behind a feasible drive leg, so a
	// drive verdict is reached independently of the node count. Deferring it too would hide an impossible
	// pin behind a label fix, and admit it outright wherever min_nodes is overridden to warn.
	if below && report.Pool == "compute" {
		return nil
	}

	detail := report.Reason
	if len(report.Fixes) > 0 {
		detail += " — " + strings.Join(report.Fixes, "; ")
	}
	fldPath, value := infeasibilityField(dyn, report)
	return field.ErrorList{field.Invalid(fldPath, value, detail)}
}

// infeasibilityField is the field.Error path and value for a planner rejection: the pinned
// dynamicTemplate field the planner blamed and what it is currently set to, else the template itself with
// no value. The planner reports the field directly (InfeasibilityReport.SpecField) because it is the only
// place that knows whether a pin caused the rejection — Binding cannot be mapped here, since it also
// carries resource dimensions ("cores" is a node's physical CPU, not the driveCores pin) and several
// rejections carry one with no pin involved.
//
// The switch is over CRD field names, which this package already reads off the spec, so an unrecognised
// SpecField still yields the right path and merely omits the value.
//
// Where there is no value to show, the sentinel is field.OmitValueType rather than nil: apimachinery
// renders a nil BadValue as "Invalid value: null", and that is the path every fleet-caused rejection takes.
func infeasibilityField(dyn *weka.WekaClusterTemplate, report *capacityplanner.InfeasibilityReport) (path *field.Path, val any) {
	base := field.NewPath("spec", "dynamicTemplate")
	if report.SpecField == "" {
		return base, field.OmitValueType{}
	}
	path = base.Child(report.SpecField)
	if dyn == nil {
		return path, field.OmitValueType{}
	}
	switch report.SpecField {
	case "numDrives":
		return path, dyn.NumDrives
	case "driveCores":
		return path, dyn.DriveCores
	case "computeCores":
		return path, dyn.ComputeCores
	}
	return path, field.OmitValueType{}
}

// clusterOwnContainers lists the WekaContainers already belonging to cluster — the growth base the planner
// diffs against. It goes through discovery.GetClusterContainers, the same owner-UID lookup the controller
// feeds the planner (funcs_fd_planning.go), so the two see the same set by construction: selecting by the
// weka.io/cluster-id label instead would drop a container whose label was stripped, and this rule would
// then plan a create where the controller plans a grow and reject a cluster the controller accepts. The
// field index it uses is registered on the manager's cache (setupContainerIndexes), which the webhook
// shares.
//
// On CREATE the object has no UID yet, so there is nothing to list and the planner sees a greenfield fleet.
func clusterOwnContainers(ctx context.Context, c client.Client, cluster *weka.WekaCluster) ([]*weka.WekaContainer, error) {
	if cluster.GetUID() == "" {
		return nil, nil
	}
	own, err := discovery.GetClusterContainers(ctx, c, cluster, "")
	if err != nil {
		return nil, fmt.Errorf("listing the cluster's own containers: %w", err)
	}
	return own, nil
}

// selectorBelowFormClusterFloor reports whether either role selector matches fewer nodes than its
// form-cluster floor. Matched nodes, not signed or eligible ones, to match what
// clusterAutoFullDrivesMinNodes counts — otherwise the two rules disagree about whose case it is.
func selectorBelowFormClusterFloor(ctx context.Context, c client.Client, cluster *weka.WekaCluster) (bool, error) {
	floors := map[string]int{
		weka.WekaContainerModeDrive:   globalconfig.Consts.FormClusterMinDriveContainers,
		weka.WekaContainerModeCompute: globalconfig.Consts.FormClusterMinComputeContainers,
	}
	for role, min := range floors {
		if min <= 0 { // floor disabled by configuration
			continue
		}
		var nodes corev1.NodeList
		if err := c.List(ctx, &nodes, client.MatchingLabels(cluster.GetNodeSelectorForRole(role))); err != nil {
			return false, fmt.Errorf("listing %s-role nodes: %w", role, err)
		}
		if len(nodes.Items) < min {
			return true, nil
		}
	}
	return false, nil
}
