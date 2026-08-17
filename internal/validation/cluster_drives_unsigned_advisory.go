package validation

import (
	"context"
	"fmt"
	"sort"
	"strings"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/consts"
)

// clusterDrivesUnsignedAdvisory warns when no node matching the drive-role nodeSelector carries the
// drive annotation this cluster's mode consumes (shared-drives vs full-drives are disjoint, so a node
// signed the other way gets a distinct re-signing message). Without this, unsigned nodes silently
// bypass clusterSignedDrives and the auto-full-drives projection, admitting a misconfigured apply
// unnoticed. Warn-only.
type clusterDrivesUnsignedAdvisory struct{}

func (clusterDrivesUnsignedAdvisory) ID() string {
	return "cluster_drives_unsigned_advisory"
}

func (clusterDrivesUnsignedAdvisory) Validate(ctx context.Context, c client.Client, obj runtime.Object) field.ErrorList {
	cluster, ok := obj.(*weka.WekaCluster)
	if !ok {
		return nil
	}
	// A nil dynamicTemplate is no longer out of scope: it is auto-full-drives mode (one drive container
	// per eligible node), which needs signed drives just as much as an explicit template does.
	// clusterMinDrivesFeasibility already rejects auto-full-drives + positive minNumDrives with zero
	// signed drives; staying silent there avoids double-reporting the same condition.
	if cluster.Spec.Dynamic.UsesAutoFullDrives() && cluster.Spec.GetStartIoConditions().MinNumDrives > 0 {
		return nil
	}

	selector := cluster.GetNodeSelectorForRole(weka.WekaContainerModeDrive)
	nodes, errs := listDriveRoleNodes(ctx, c, cluster, field.NewPath("spec", "nodeSelector"))
	if errs != nil {
		return errs
	}
	if len(nodes) == 0 {
		// clusterSelectedNodesCount owns "the selector matches nothing".
		return nil
	}

	// Which annotation matters is a property of the cluster, not of what happens to be on the nodes.
	wantAnn, wantMode := consts.AnnotationWekaFullDrives, "full-drives"
	otherAnn, otherMode := consts.AnnotationSharedDrives, "drive-sharing"
	if cluster.IsDriveSharing() {
		wantAnn, wantMode, otherAnn, otherMode = otherAnn, otherMode, wantAnn, wantMode
	}

	otherModeNodes := 0
	for i := range nodes {
		ann := nodes[i].Annotations
		if _, ok := ann[wantAnn]; ok {
			return nil
		}
		if _, ok := ann[otherAnn]; ok {
			otherModeNodes++
		}
	}

	detail := fmt.Sprintf(
		"none of the %d node(s) matching the drive-role nodeSelector (%s) has drives signed for this "+
			"cluster's %s mode — no %s annotation on %s. Drive containers cannot claim a drive until "+
			"sign-drives runs there, and the drive-count and capacity checks are skipped in the "+
			"meantime, so a misconfigured spec would be admitted unnoticed. Sign drives on the "+
			"matched nodes in %s mode.",
		len(nodes), formatSelector(selector), wantMode, wantAnn,
		formatNodeNames(nodes), wantMode,
	)
	if otherModeNodes > 0 {
		detail = fmt.Sprintf(
			"%d of the %d node(s) matching the drive-role nodeSelector (%s) are signed in %s mode "+
				"(%s), but this cluster is %s mode and consumes %s — the two are disjoint, so those "+
				"drives are unusable here and no drive container will be able to claim one. "+
				"Re-sign the matched nodes (%s) in %s mode, or change the cluster's sizing to match "+
				"how the nodes are signed.",
			otherModeNodes, len(nodes), formatSelector(selector), otherMode, otherAnn,
			wantMode, wantAnn, formatNodeNames(nodes), wantMode,
		)
	}
	return field.ErrorList{
		field.Invalid(field.NewPath("spec", "nodeSelector"), formatSelector(selector), detail),
	}
}

// formatSelector renders a label selector deterministically as "k=v,k=v" for message text.
func formatSelector(selector map[string]string) string {
	if len(selector) == 0 {
		return "<empty — matches all nodes>"
	}
	parts := make([]string, 0, len(selector))
	for k, v := range selector {
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// formatNodeNames lists up to three node names, so the warning names something the user can act on
// without pasting an entire fleet into an admission response.
func formatNodeNames(nodes []corev1.Node) string {
	const maxNamed = 3
	names := make([]string, 0, len(nodes))
	for i := range nodes {
		names = append(names, nodes[i].Name)
	}
	// Sort before truncating so the named subset is stable across List orderings.
	sort.Strings(names)
	if len(names) > maxNamed {
		return fmt.Sprintf("%s and %d more", strings.Join(names[:maxNamed], ", "), len(names)-maxNamed)
	}
	return strings.Join(names, ", ")
}
