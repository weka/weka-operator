package validation

import (
	"cmp"
	"context"
	"fmt"
	"sort"

	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/pkg/util"
)

// clusterAutoFullDrivesPins rejects numDrives/driveCores pins that some signed drive-role node cannot
// satisfy. The container is still created with the pin, then loops on InsufficientDrives, so the
// failure is silent at runtime. Unsigned nodes are skipped — cluster_drives_unsigned_advisory owns them.
type clusterAutoFullDrivesPins struct{}

func (clusterAutoFullDrivesPins) ID() string { return "cluster_auto_full_drives_pins" }

func (clusterAutoFullDrivesPins) Validate(ctx context.Context, c client.Client, obj runtime.Object) field.ErrorList {
	cluster, ok := obj.(*weka.WekaCluster)
	if !ok {
		return nil
	}
	dyn := cluster.Spec.Dynamic
	if dyn == nil || !dyn.UsesAutoFullDrives() {
		return nil
	}
	numDrives, driveCores := dyn.NumDrives, dyn.DriveCores
	if numDrives <= 0 && driveCores <= 0 {
		return nil
	}

	specPath := field.NewPath("spec", "dynamicTemplate")
	nodes, errs := listDriveRoleNodes(ctx, c, cluster, specPath)
	if errs != nil {
		return errs
	}

	var badDrives, badCores []string
	for i := range nodes {
		// Unsigned, unparsable and no-usable-drive (empty or all blocked) nodes get no containers
		// (daemonset.go), so they cannot violate a pin.
		drives, signed, err := allocator.SignedFullDrivesGiB(&nodes[i])
		if !signed || err != nil || len(drives) == 0 {
			continue
		}
		n := len(drives)
		entry := fmt.Sprintf("%s (signed %d)", nodes[i].Name, n)
		if numDrives > n {
			badDrives = append(badDrives, entry)
		}
		if driveCores > cmp.Or(numDrives, n) {
			badCores = append(badCores, entry)
		}
	}

	var out field.ErrorList
	if len(badDrives) > 0 {
		sort.Strings(badDrives)
		out = append(out, field.Invalid(specPath.Child("numDrives"), numDrives, fmt.Sprintf(
			"numDrives exceeds the signed full drives on %d drive-role node(s): %s. A container pinned to "+
				"more drives than its node signed waits on InsufficientDrives forever. Lower numDrives, "+
				"unset it, or sign more drives.",
			len(badDrives), util.JoinCapped(badDrives, ", ", 10))))
	}
	if len(badCores) > 0 {
		sort.Strings(badCores)
		out = append(out, field.Invalid(specPath.Child("driveCores"), driveCores, fmt.Sprintf(
			"driveCores exceeds the drives a drive container takes (numDrives, or all signed drives when "+
				"unset) on %d drive-role node(s): %s. Lower driveCores or raise numDrives.",
			len(badCores), util.JoinCapped(badCores, ", ", 10))))
	}
	return out
}
