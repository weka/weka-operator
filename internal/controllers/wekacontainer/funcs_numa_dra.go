package wekacontainer

import (
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	resourcev1 "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/capacityplanner"
	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/controllers/resources"
)

// numaClaimRequeueDelay is how long ensureNumaResourceClaimForCPUCount asks the reconciler to wait
// before retrying: both after deleting a stale claim (the delete may not have fully propagated,
// or the object may still be terminating behind a finalizer) and after Create hits AlreadyExists
// (the old object is still being removed). Short because both are just waiting on the API server
// to finish a removal already in flight, not on external/slow state.
const numaClaimRequeueDelay = 5 * time.Second

// needsNumaDraClaim reports whether this container requests NUMA region confinement via the DRA
// method (as opposed to the device-plugin extended-resource method, or no NUMA confinement at all).
func (r *containerReconcilerLoop) needsNumaDraClaim() bool {
	numa := r.container.Spec.Numa
	return numa != nil && numa.Single && numa.Region != nil && numa.Method == weka.WekaNumaMethodDra
}

// ensureNumaDraClaim verifies dra-driver-cpu's DeviceClass is installed, then ensures this
// container's namespace-scoped ResourceClaim exists and matches the desired shape. Called only
// when needsNumaDraClaim is true, and only while PodNotSet (see flow_active_state.go) — this is
// pod-creation-time wiring, not steady-state reconciliation.
func (r *containerReconcilerLoop) ensureNumaDraClaim(ctx context.Context) error {
	if err := r.checkNumaDeviceClassInstalled(ctx); err != nil {
		return err
	}
	return r.ensureNumaResourceClaim(ctx)
}

// checkNumaDeviceClassInstalled verifies the dra.cpu DeviceClass exists. Unlike the old
// numa-region.weka.io DeviceClass this replaces, dra.cpu is owned and installed by dra-driver-cpu
// (kubernetes-sigs/dra-driver-cpu) itself — there is nothing here for the operator to create, so
// a missing class means the driver isn't installed rather than "not yet reconciled".
func (r *containerReconcilerLoop) checkNumaDeviceClassInstalled(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "checkNumaDeviceClassInstalled")
	defer logger.End()

	existing := &resourcev1.DeviceClass{}
	err := r.Get(ctx, client.ObjectKey{Name: consts.WekaDraDeviceClassName}, existing)
	if err == nil {
		return nil
	}
	if apierrors.IsNotFound(err) {
		return errors.Errorf("DeviceClass %s not found: dra-driver-cpu does not appear to be installed on this cluster (required for numa.method=dra)", consts.WekaDraDeviceClassName)
	}
	return errors.Wrapf(err, "failed to check for %s DeviceClass (resource.k8s.io/v1 API may be unavailable on this cluster)", consts.WekaDraDeviceClassName)
}

// numaClaimCPUCount computes the integer CPU core count to request via the DRA NUMA claim's
// dra.cpu/cpu capacity, reusing the exact same capacityplanner.CPURequestCores helper and node
// topology (IsHt, FullPcpusOnly) that PodFactory.setResources uses to size the pod's own CPU
// request, so the claim and the pod it backs can never diverge — enforced here by rejecting any
// cpu policy that doesn't resolve to dedicated/dedicated_ht (manual/shared don't reserve whole
// cores, so they can't back an exclusive-CPU DRA claim).
func (r *containerReconcilerLoop) numaClaimCPUCount(ctx context.Context) (int, error) {
	container := r.container

	var nodeAffinity weka.NodeName
	if r.node != nil {
		// The reconcile flow already resolved the target node earlier in this pass (see GetNode
		// in ContainerReconcileSteps) whenever the container has node affinity — reuse it instead
		// of re-running pickMatchingNode, which has no guarantee of landing on the same node twice
		// if cluster state shifts between the two calls within one reconcile.
		nodeAffinity = weka.NodeName(r.node.Name)
	} else if aff := container.GetNodeAffinity(); aff != "" {
		nodeAffinity = aff
	} else {
		node, err := r.pickMatchingNode(ctx)
		if err != nil {
			return 0, err
		}
		nodeAffinity = weka.NodeName(node.Name)
	}

	nodeInfo, err := r.GetNodeInfo(ctx, nodeAffinity)
	if err != nil {
		return 0, err
	}

	cpuPolicy, err := resolveNumaClaimCPUPolicy(&container.Spec, nodeInfo.IsHt)
	if err != nil {
		return 0, err
	}

	// specForCPU copies the spec and overwrites CpuPolicy with the resolved value, mirroring
	// pod.go's specForCPU pattern exactly (~1223-1224) so CPURequestCores computes the same
	// integer the pod's own CPU request will use, and does not re-resolve auto itself.
	specForCPU := container.Spec
	specForCPU.CpuPolicy = cpuPolicy
	topo := capacityplanner.NodeCPUTopology{
		IsHt:          nodeInfo.IsHt,
		FullPcpusOnly: config.Config.FullPcpusOnly || nodeInfo.NodeFullPcpusOnly,
	}
	return capacityplanner.CPURequestCores(&specForCPU, topo), nil
}

// resolveNumaClaimCPUPolicy resolves the effective cpu policy exactly like PodFactory.setResources
// does (pod.go ~1171-1185: invalid policies rejected, "auto" resolved via CoreIds/node HT) so it
// mirrors what the pod will actually run with, then rejects anything that isn't
// dedicated/dedicated_ht — NUMA confinement via a DRA claim hands the pod an exclusive cpuset, so
// manual/shared policies (which don't reserve whole cores) can't back it. A pure function, split
// out from numaClaimCPUCount so it's testable without faking node discovery (it only needs isHt,
// not the full discovery.DiscoveryNodeInfo).
func resolveNumaClaimCPUPolicy(spec *weka.WekaContainerSpec, isHt bool) (weka.CpuPolicy, error) {
	cpuPolicy := spec.CpuPolicy
	if !cpuPolicy.IsValid() {
		return "", fmt.Errorf("invalid CPU policy: %s", cpuPolicy)
	}
	if cpuPolicy == weka.CpuPolicyAuto {
		if len(spec.CoreIds) > 0 {
			cpuPolicy = weka.CpuPolicyManual
		}
		if isHt {
			cpuPolicy = weka.CpuPolicyDedicatedHT
		} else {
			cpuPolicy = weka.CpuPolicyDedicated
		}
	}
	if cpuPolicy != weka.CpuPolicyDedicated && cpuPolicy != weka.CpuPolicyDedicatedHT {
		return "", errors.Errorf(
			"numa method \"dra\" requires a dedicated cpu policy (got %q): manual/shared cpu policies cannot mirror an exclusive-CPU resource claim",
			cpuPolicy,
		)
	}
	return cpuPolicy, nil
}

// ensureNumaResourceClaim resolves the CPU count this container's claim must request (mirroring
// PodFactory.setResources exactly — see numaClaimCPUCount) and delegates the create/compare/
// recreate work to ensureNumaResourceClaimForCPUCount. Split in two so tests can drive the CRUD
// logic directly against an explicit CPU count, without needing to fake node discovery.
func (r *containerReconcilerLoop) ensureNumaResourceClaim(ctx context.Context) error {
	cpuCount, err := r.numaClaimCPUCount(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to compute CPU count for NUMA DRA claim")
	}
	return r.ensureNumaResourceClaimForCPUCount(ctx, cpuCount)
}

// ensureNumaResourceClaimForCPUCount ensures a namespace-scoped ResourceClaim exists for this
// container requesting exactly one device from the dra.cpu DeviceClass, filtered to the
// container's configured NUMA region, with a dra.cpu/cpu capacity request of cpuCount — the same
// integer the pod's own CPU request/limit will carry (dra-driver-cpu requires the two to match for
// scheduler accounting).
func (r *containerReconcilerLoop) ensureNumaResourceClaimForCPUCount(ctx context.Context, cpuCount int) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "ensureNumaResourceClaim")
	defer logger.End()

	container := r.container
	numa := container.Spec.Numa
	if numa == nil || numa.Region == nil {
		// Guarded by needsNumaDraClaim at the call site; defensive no-op if reached otherwise.
		return nil
	}
	if cpuCount <= 0 {
		return errors.Errorf("computed %d CPU cores for NUMA DRA claim on container %s; check spec.cpuPolicy/resources", cpuCount, container.Name)
	}

	region := *numa.Region
	claimName := resources.NumaClaimNameForContainer(container.Name)

	existing := &resourcev1.ResourceClaim{}
	err := r.Get(ctx, client.ObjectKey{Name: claimName, Namespace: container.Namespace}, existing)
	switch {
	case err == nil:
		if !claimNeedsRecreate(existing, region, cpuCount) {
			return nil
		}

		if len(existing.Status.ReservedFor) > 0 {
			// The claim is still reserved by a consumer (a pod). Deleting it now would fight with
			// that reservation instead of resolving it — this step only runs while PodNotSet (see
			// flow_active_state.go), so reaching a reserved+drifted claim here means the old pod
			// hasn't finished terminating yet. Its own replacement clears the reservation; a later
			// reconcile (once the pod is actually gone) will pick the drift back up.
			logger.Info("NUMA DRA claim is drifted but still reserved by a consumer, leaving it in place",
				"claim", claimName, "reservedFor", len(existing.Status.ReservedFor))
			return nil
		}

		// ResourceClaim device requests are immutable once created (and doubly so once allocated
		// to a pod), so a spec drift — region or CPU-count change, or migrating off the old
		// numa-region.weka.io shape — can only be applied by deleting and recreating the claim.
		// Preconditions{UID} guards against a race where the object we read has already been
		// replaced by the time the delete reaches the API server.
		if delErr := r.Delete(ctx, existing, client.Preconditions{UID: &existing.UID}); delErr != nil && !apierrors.IsNotFound(delErr) {
			return errors.Wrapf(delErr, "failed to delete stale %s ResourceClaim", claimName)
		}
		// Don't create in the same pass: the delete may not have fully propagated, or the object
		// may still be terminating behind a finalizer. Requeue and let the next reconcile see it
		// actually gone before recreating.
		return lifecycle.NewWaitErrorWithDuration(
			errors.Errorf("deleted stale %s ResourceClaim, waiting for removal before recreating", claimName),
			numaClaimRequeueDelay,
		)
	case apierrors.IsNotFound(err):
		// fall through to create
	default:
		return errors.Wrapf(err, "failed to check for existing %s ResourceClaim (resource.k8s.io/v1 API may be unavailable on this cluster)", claimName)
	}

	claim := buildNumaResourceClaim(container, region, cpuCount)

	if refErr := ctrl.SetControllerReference(container, claim, r.Scheme); refErr != nil {
		return errors.Wrap(refErr, "failed to set controller reference on numa ResourceClaim")
	}

	if createErr := r.Create(ctx, claim); createErr != nil {
		if apierrors.IsAlreadyExists(createErr) {
			// The old object is still terminating — requeue rather than treat this as done, so a
			// later pass actually verifies (and if needed re-drives) the claim's shape once it's
			// really gone.
			return lifecycle.NewWaitErrorWithDuration(
				errors.Errorf("%s ResourceClaim already exists (old object still terminating), retrying", claimName),
				numaClaimRequeueDelay,
			)
		}
		return errors.Wrapf(createErr, "failed to create %s ResourceClaim (resource.k8s.io/v1 API may be unavailable on this cluster)", claimName)
	}

	return nil
}

// buildNumaResourceClaim constructs the desired ResourceClaim for a container's NUMA region
// confinement via dra-driver-cpu: one device request named "cpus" for exactly one device from the
// dra.cpu DeviceClass, filtered to the given NUMA region via a CEL selector on the numaNodeID
// attribute, requesting cpuCount CPU cores of dra.cpu/cpu capacity from that device.
func buildNumaResourceClaim(container *weka.WekaContainer, region int, cpuCount int) *resourcev1.ResourceClaim {
	claimName := resources.NumaClaimNameForContainer(container.Name)

	return &resourcev1.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      claimName,
			Namespace: container.Namespace,
		},
		Spec: resourcev1.ResourceClaimSpec{
			Devices: resourcev1.DeviceClaim{
				Requests: []resourcev1.DeviceRequest{
					{
						Name: "cpus",
						Exactly: &resourcev1.ExactDeviceRequest{
							DeviceClassName: consts.WekaDraDeviceClassName,
							AllocationMode:  resourcev1.DeviceAllocationModeExactCount,
							Count:           1,
							Selectors: []resourcev1.DeviceSelector{
								{
									CEL: &resourcev1.CELDeviceSelector{
										Expression: numaRegionCELExpression(region),
									},
								},
							},
							Capacity: &resourcev1.CapacityRequirements{
								Requests: map[resourcev1.QualifiedName]resource.Quantity{
									resourcev1.QualifiedName(consts.WekaDraCPUCapacity): resource.MustParse(fmt.Sprintf("%d", cpuCount)),
								},
							},
						},
					},
				},
			},
		},
	}
}

// numaRegionCELExpression is the CEL selector filtering dra-driver-cpu's devices down to the ones
// on a specific NUMA node. Shared between buildNumaResourceClaim and claimNeedsRecreate so the two
// can never disagree on the expression shape.
func numaRegionCELExpression(region int) string {
	return fmt.Sprintf("device.attributes[%q].numaNodeID == %d", consts.WekaDraDriverName, region)
}

// claimNeedsRecreate reports whether an existing ResourceClaim's spec differs from the desired
// dra-driver-cpu shape (device class, request name, region selector, cpu-count capacity), meaning
// it must be deleted and recreated — covers both "stale cpu count" and "old numa-region.weka.io
// shape" drift.
func claimNeedsRecreate(existing *resourcev1.ResourceClaim, region int, cpuCount int) bool {
	reqs := existing.Spec.Devices.Requests
	if len(reqs) != 1 || reqs[0].Exactly == nil {
		return true
	}

	exact := reqs[0].Exactly
	if reqs[0].Name != "cpus" ||
		exact.DeviceClassName != consts.WekaDraDeviceClassName ||
		exact.AllocationMode != resourcev1.DeviceAllocationModeExactCount ||
		exact.Count != 1 {
		return true
	}

	if len(exact.Selectors) != 1 || exact.Selectors[0].CEL == nil ||
		exact.Selectors[0].CEL.Expression != numaRegionCELExpression(region) {
		return true
	}

	if exact.Capacity == nil {
		return true
	}
	existingQty, ok := exact.Capacity.Requests[resourcev1.QualifiedName(consts.WekaDraCPUCapacity)]
	if !ok || !existingQty.Equal(resource.MustParse(fmt.Sprintf("%d", cpuCount))) {
		return true
	}

	return false
}
