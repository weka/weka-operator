package validation

import (
	"context"
	"fmt"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	globalconfig "github.com/weka/weka-operator/internal/config"
)

// clusterAutoFullDrivesMinNodes rejects an auto-full-drives cluster whose role nodeSelector matches
// fewer nodes than the form-cluster floor (FormClusterMinDrive/ComputeContainers — 5 by default, 3
// under ALLOW_SINGLE_PARITY). Both container counts are 0 in this mode and the operator places exactly
// one container per eligible node, so the matched node count IS the container count and no sizing field
// can raise it — which is also why clusterSelectedNodesCount and clusterMinContainers, both driven by
// pinned counts, no-op here. The two legs fail differently at runtime; see consequence below.
//
// Counts MATCHED nodes, not signed ones: labelling and drive-signing are independent, and a labelled
// but unsigned node still hosts a container once signing runs. So there is no signing state to be
// mid-rollout on, and none of the bootstrap gating the drive-capacity rules need.
type clusterAutoFullDrivesMinNodes struct{}

func (clusterAutoFullDrivesMinNodes) ID() string { return "cluster_auto_full_drives_min_nodes" }

func (clusterAutoFullDrivesMinNodes) Validate(ctx context.Context, c client.Client, obj runtime.Object) field.ErrorList {
	cluster, ok := obj.(*weka.WekaCluster)
	if !ok {
		return nil
	}
	// Nil dynamicTemplate is auto-full-drives mode (nothing was set), so no nil guard here.
	if !cluster.Spec.Dynamic.UsesAutoFullDrives() {
		return nil
	}

	legs := []struct {
		role string
		// selectorField is the role-specific selector path named in the remedy.
		selectorField string
		min           int
		// consequence describes what actually happens at runtime if this is left as-is.
		consequence string
	}{
		{
			role:          weka.WekaContainerModeDrive,
			selectorField: "spec.roleNodeSelector.drive",
			min:           globalconfig.Consts.FormClusterMinDriveContainers,
			consequence: "Nothing reports this at runtime: the plan is feasible and the drive containers " +
				"run healthy, but the cluster waits on MinContainersNotReady forever",
		},
		{
			role:          weka.WekaContainerModeCompute,
			selectorField: "spec.roleNodeSelector.compute",
			min:           globalconfig.Consts.FormClusterMinComputeContainers,
			consequence: "The planner reports the whole plan infeasible (AutoFullDrivesInfeasible) and " +
				"creates nothing",
		},
	}

	var out field.ErrorList
	for _, leg := range legs {
		if leg.min <= 0 { // floor disabled by configuration — nothing to enforce
			continue
		}
		selector := cluster.GetNodeSelectorForRole(leg.role)
		fldPath := field.NewPath("spec", "roleNodeSelector", leg.role)

		var nodes corev1.NodeList
		if err := c.List(ctx, &nodes, client.MatchingLabels(selector)); err != nil {
			out = append(out, field.InternalError(fldPath,
				fmt.Errorf("listing %s-role nodes: %w", leg.role, err)))
			continue
		}
		matched := len(nodes.Items)
		if matched >= leg.min {
			continue
		}

		detail := fmt.Sprintf(
			"the %s-role nodeSelector (%s) matches %d node(s), below the %d %s container(s) weka needs "+
				"to form a cluster. This cluster sets no container counts, so it acts as a daemonset and "+
				"places exactly one %s container per eligible node — the matched node count IS the "+
				"container count, and no sizing field can raise it. %s. Label at least %d node(s) for "+
				"%s (it falls back to spec.nodeSelector when unset).",
			leg.role, formatSelector(selector), matched, leg.min, leg.role,
			leg.role, leg.consequence, leg.min, leg.selectorField,
		)
		out = append(out, field.Invalid(fldPath, matched, detail))
	}
	return out
}
