package wekacluster

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/internal/services/discovery"
	weka "github.com/weka/weka-operator/pkg/weka-k8s-api/api/v1alpha1"
)

var rotationRoleOrder = []string{
	weka.WekaContainerModeDrive, weka.WekaContainerModeCompute, weka.WekaContainerModeDataServices,
	weka.WekaContainerModeS3, weka.WekaContainerModeNfs, weka.WekaContainerModeSmbw,
}

var clusterHealthGate = func(ctx context.Context, r *wekaClusterReconcilerLoop) (bool, string) {
	v := operations.EvaluateClusterHealth(ctx, r.Manager, r.ExecService, r.cluster)
	return v.Allowed, v.Reason
}

func podRotationEnabled(ctx context.Context, cluster *weka.WekaCluster) bool {
	if v := cluster.Spec.GetOverrides().PodRotation; v != nil {
		return *v
	}
	return services.GetSettings(ctx).PodRotation.Enabled
}

// The cluster controller doesn't watch WekaContainers and its usual list is cached, so a just-approved
// container can still read as unapproved; approvals are decided on an uncached read only.
func (r *wekaClusterReconcilerLoop) rotationContainers(ctx context.Context) ([]*weka.WekaContainer, error) {
	all, err := discovery.GetClusterContainersNoFieldIndex(ctx, r.Manager.GetAPIReader(), r.cluster, "")
	if err != nil {
		return nil, err
	}
	scoped := all[:0]
	for _, c := range all {
		if c.IsBackend() {
			scoped = append(scoped, c)
		}
	}
	return scoped, nil
}

// Containers in deleting/destroying state never run the active flow, so they never clear podOutdated.
func rotationExempt(c *weka.WekaContainer) bool {
	return c.IsMarkedForDeletion() || c.IsDeletingState() || c.IsDestroyingState()
}

func anyApproved(containers []*weka.WekaContainer) bool {
	return slices.ContainsFunc(containers, func(c *weka.WekaContainer) bool { return c.Spec.RotatePod })
}

func (r *wekaClusterReconcilerLoop) setRotatePod(ctx context.Context, c *weka.WekaContainer, v bool) error {
	patch := client.RawPatch(types.MergePatchType, []byte(fmt.Sprintf(`{"spec":{"rotatePod":%t}}`, v)))
	return errors.Wrapf(client.IgnoreNotFound(r.getClient().Patch(ctx, c, patch)), "failed to set rotatePod=%t on %s", v, c.Name)
}

func (r *wekaClusterReconcilerLoop) clearFinishedRotations(ctx context.Context, containers []*weka.WekaContainer) (inFlight int, err error) {
	for _, c := range containers {
		if !c.Spec.RotatePod {
			continue
		}
		if !c.Status.PodOutdated || rotationExempt(c) {
			if err := r.setRotatePod(ctx, c, false); err != nil {
				return inFlight, err
			}
			continue
		}
		inFlight++
	}
	return inFlight, nil
}

func (r *wekaClusterReconcilerLoop) handlePodRotation(ctx context.Context) error {
	containers, err := r.rotationContainers(ctx)
	if err != nil {
		return err
	}

	hadApproved := anyApproved(containers)
	inFlight, err := r.clearFinishedRotations(ctx, containers)
	if err != nil {
		return err
	}
	if hadApproved {
		return lifecycle.NewWaitError(fmt.Errorf("pod rotation in progress (%d containers)", inFlight))
	}

	var candidates []*weka.WekaContainer
	for _, c := range containers {
		if c.Status.PodOutdated && !rotationExempt(c) && c.GetNodeAffinity() != "" {
			candidates = append(candidates, c)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	if !podRotationEnabled(ctx, r.cluster) {
		return nil
	}
	overrides := r.cluster.Spec.GetOverrides()
	if overrides.UpgradePaused {
		return lifecycle.NewWaitError(errors.New("pod rotation is paused"))
	}
	if overrides.UpgradeAllAtOnce {
		for _, c := range candidates {
			if err := r.setRotatePod(ctx, c, true); err != nil {
				return err
			}
		}
		return lifecycle.NewWaitError(fmt.Errorf("approved pod rotation of %d containers at once", len(candidates)))
	}
	if ok, reason := clusterHealthGate(ctx, r); !ok {
		_ = r.RecordEventThrottled("", "PodRotationWaiting", consts.ActionUpgrade, "pod rotation waiting for cluster health: "+reason, time.Minute) //nolint:errcheck // best effort
		return lifecycle.NewWaitError(errors.New("pod rotation waiting for cluster health: " + reason))
	}
	slices.SortFunc(candidates, func(a, b *weka.WekaContainer) int {
		if d := slices.Index(rotationRoleOrder, a.Spec.Mode) - slices.Index(rotationRoleOrder, b.Spec.Mode); d != 0 {
			return d
		}
		return strings.Compare(a.Name, b.Name)
	})
	next := candidates[0]
	if err := r.setRotatePod(ctx, next, true); err != nil {
		return err
	}
	return lifecycle.NewWaitError(fmt.Errorf("approved pod rotation of %s", next.Name))
}
