package validation

import (
	"context"
	"fmt"
	"sort"
	"strings"

	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/pkg/util"
)

// clusterDrivesUnsignedAdvisory warns when no node matching the drive-role nodeSelector carries the
// drive annotation this cluster's mode consumes (shared-drives vs full-drives are disjoint, so a node
// signed the other way gets a distinct re-signing message). Without this, unsigned nodes silently
// bypass clusterSignedDrives, admitting a misconfigured apply unnoticed. In auto-full-drives mode it
// instead lists every drive-role node lacking the annotation. Warn-only.
type clusterDrivesUnsignedAdvisory struct{}

func (clusterDrivesUnsignedAdvisory) ID() string {
	return "cluster_drives_unsigned_advisory"
}

func (clusterDrivesUnsignedAdvisory) Validate(ctx context.Context, c client.Client, obj runtime.Object) field.ErrorList {
	cluster, ok := obj.(*weka.WekaCluster)
	if !ok {
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

	if cluster.Spec.Dynamic.UsesAutoFullDrives() {
		return unsignedAutoFullDrives(cluster, nodes, selector)
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
		// Full-drives-signed nodes are the expected state mid-migration: the migrate-to-drive-sharing
		// campaign re-signs each node's drives as it drains them, so re-signing up front would be wrong.
		if cluster.IsDriveSharing() &&
			cluster.Annotations[consts.AnnotationSizingModeMigration] == consts.SizingModeMigrationDriveSharing {
			return nil
		}
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

// unsignedAutoFullDrives warns about every drive-role node with no usable full drives (no annotation, an
// unparsable one, or every drive blocked): in auto-full-drives mode such a node silently gets no drive and
// no compute container.
func unsignedAutoFullDrives(cluster *weka.WekaCluster, nodes []corev1.Node, selector map[string]string) field.ErrorList {
	var unsigned []string
	for i := range nodes {
		drivesGiB, signed, err := allocator.SignedFullDrivesGiB(&nodes[i])
		if !signed || err != nil || len(drivesGiB) == 0 {
			unsigned = append(unsigned, nodes[i].Name)
		}
	}
	if len(unsigned) == 0 {
		return nil
	}
	sort.Strings(unsigned)
	detail := fmt.Sprintf(
		"%d of the %d node(s) matching the drive-role nodeSelector (%s) have no usable %s drives: %s. "+
			"This cluster acts as a daemonset, and no drive or compute container is created on a node "+
			"until sign-drives has run there in full-drives mode and left at least one unblocked drive.",
		len(unsigned), len(nodes), formatSelector(selector), consts.AnnotationWekaFullDrives, util.JoinCapped(unsigned, ", ", 10),
	)
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
