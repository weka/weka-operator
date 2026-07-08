package wekacontainer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/weka/go-steps-engine/lifecycle"
	"github.com/weka/go-weka-observability/instrumentation"
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	"go.opentelemetry.io/otel/codes"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/controllers/utils"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/pkg/util"
)

// podHugepagesRequestMiB returns the pod's hugepages request in MiB. This is the same raw-hugepages
// figure the cluster planner charges per container (Spec.Hugepages) and compares against, so it lines up
// with reqHpMiB (cores × HugepagesPerCoreMiB). The MEMORY env (request minus the reserved offset) is a
// different unit and would bias the check, so it is not used here.
func podHugepagesRequestMiB(c *v1.Container) int {
	for _, name := range []v1.ResourceName{"hugepages-2Mi", "hugepages-1Gi"} {
		if q, ok := c.Resources.Requests[name]; ok {
			return int(q.Value() / (1 << 20))
		}
	}
	return 0
}

// wekaPodContainer returns the pod's main weka container spec (matched by name), or nil when the pod
// has no such container. Resource requests must be read from this container specifically — assuming
// index 0 would read an injected sidecar's requests if one is ever placed first.
func wekaPodContainer(pod *v1.Pod) *v1.Container {
	if pod == nil {
		return nil
	}
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == consts.WekaContainerName {
			return &pod.Spec.Containers[i]
		}
	}
	return nil
}

// checkDriveResourceFeasibility verifies the live pod has enough cores, hugepages and RSS to host the
// virtual-drive capacity about to be added, using the same per-core model as planClusterCapacity.
// clusterCapacity grows containerCapacity live (it is excluded from the pod config hash), so the running
// pod can lag the capacity it must serve. On a shortfall it emits an event and returns a WaitError to
// defer the add until the pod is (re)sized.
func (r *containerReconcilerLoop) checkDriveResourceFeasibility(ctx context.Context) error {
	container := r.container
	if !container.UsesDriveSharing() || container.Status.Allocations == nil {
		return nil
	}
	c := wekaPodContainer(r.pod)
	if c == nil {
		return nil
	}

	_, logger := instrumentation.CreateLogSpan(ctx, "checkDriveResourceFeasibility")
	defer logger.End()

	var tlcGiB, qlcGiB int
	for _, vd := range container.Status.Allocations.VirtualDrives {
		if vd.Type == "QLC" {
			qlcGiB += vd.CapacityGiB
		} else {
			tlcGiB += vd.CapacityGiB
		}
	}
	if tlcGiB == 0 && qlcGiB == 0 {
		return nil
	}

	cons := allocator.CapacityConstraintsFromConfig()
	reqCores, reqHpMiB, reqMemMiB := allocator.RequiredDriveResources(tlcGiB, qlcGiB, cons)

	availCores := int(c.Resources.Requests.Cpu().Value())
	availHpMiB := podHugepagesRequestMiB(c)
	availMemMiB := int(c.Resources.Requests.Memory().Value() / (1 << 20))

	var shortfall string
	switch {
	case availCores < reqCores:
		shortfall = fmt.Sprintf("cores: pod reserves %d, need %d for %d GiB TLC + %d GiB QLC", availCores, reqCores, tlcGiB, qlcGiB)
	case availHpMiB < reqHpMiB:
		shortfall = fmt.Sprintf("hugepages: pod requests %d MiB, need %d MiB (%d cores)", availHpMiB, reqHpMiB, reqCores)
	case availMemMiB < reqMemMiB:
		shortfall = fmt.Sprintf("memory: pod requests %d MiB, need %d MiB (%d cores)", availMemMiB, reqMemMiB, reqCores)
	}
	if shortfall == "" {
		return nil
	}

	msg := fmt.Sprintf("deferring drive add: pod under-resourced for target capacity (%s)", shortfall)
	_ = r.RecordEvent(v1.EventTypeWarning, "DriveCapacityResourceShortfall", msg) //nolint:errcheck // event recording is best effort

	return lifecycle.NewWaitErrorWithDuration(errors.New(msg), time.Minute)
}

func (r *containerReconcilerLoop) EnsureDrives(ctx context.Context) error {
	container := r.container
	pod := r.pod
	if container.Status.ClusterContainerID == nil {
		err := errors.New("container cluster ID is not set, cannot ensure drives")
		return lifecycle.NewWaitErrorWithDuration(err, time.Second*10)
	}

	ctx, logger := instrumentation.CreateLogSpan(ctx, "EnsureDrives", "cluster_guid", container.Status.ClusterID, "container_id", *container.Status.ClusterContainerID)
	defer logger.End()

	// Determine expected drive count based on mode
	var expectedDriveCount int
	if container.UsesDriveSharing() {
		expectedDriveCount = len(container.Status.Allocations.VirtualDrives)
	} else {
		expectedDriveCount = len(container.Status.Allocations.Drives)
	}

	if len(container.Status.AddedDrives) == expectedDriveCount {
		return r.updateContainerStatusIfNotEquals(ctx, weka.Running)
	}

	// Before adding more capacity, make sure the live pod can actually host it (cores/hugepages/RSS),
	// using the same per-core model the cluster planner uses. Blocks with a WaitError otherwise.
	if err := r.checkDriveResourceFeasibility(ctx); err != nil {
		return err
	}

	executor, err := util.NewExecInPod(r.RestClient, r.Manager.GetConfig(), pod)
	if err != nil {
		return err
	}

	// get drives that were discovered
	// (these drives are requested in allocations and exist in kernel)
	var kDrives map[string]domain.DriveInfo
	// NOTE: used closure not to execute this function if we don't need to add any drives
	getKernelDrives := func() error {
		if kDrives == nil {
			kDrives, err = r.getKernelDrives(ctx, executor)
			if err != nil {
				return fmt.Errorf("error getting kernel drives: %v", err)
			} else {
				logger.Info("Kernel drives fetched", "drives", kDrives)
			}
		}
		return nil
	}

	timeout := time.Minute * 2
	wekaService := services.NewWekaServiceWithTimeout(r.ExecService, container, &timeout)

	var errs []error

	// Handle drive sharing mode (virtual drives) vs regular mode (exclusive drives)
	if container.UsesDriveSharing() {
		// Drive sharing mode: add virtual drives using virtual uuids
		// Build map of added drives by device path
		drivesAddedByVids := make(map[string]bool)
		for _, d := range container.Status.AddedDrives {
			drivesAddedByVids[d.Uuid] = true
		}

		cluster, err := r.getCluster(ctx)
		if err != nil {
			return fmt.Errorf("error getting cluster: %w", err)
		}

		// Decide whether all of this container's drives go to the single "legacy" pool, or are
		// routed to type-specific pools (iubig for QLC, iu4k for TLC).
		var allSameType bool
		if cluster.Spec.Dynamic != nil && cluster.Spec.Dynamic.DriveTypesRatio != nil {
			ratio := cluster.Spec.Dynamic.DriveTypesRatio
			allSameType = ratio.Qlc == 0 || ratio.Tlc == 0
		} else {
			allSameType = true
			for _, vd := range container.Status.Allocations.VirtualDrives {
				if vd.Type != container.Status.Allocations.VirtualDrives[0].Type {
					allSameType = false
					break
				}
			}
		}

		// Add each virtual drive to the cluster
		for _, vd := range container.Status.Allocations.VirtualDrives {
			_, l := logger.WithValues("virtual_uuid", vd.VirtualUUID, "serial", vd.Serial, "physical_uuid", vd.PhysicalUUID)

			// Check if drive is already added to weka
			if drivesAddedByVids[vd.VirtualUUID] {
				l.Info("Virtual drive already added to weka")
				continue
			}

			var pool string
			switch {
			case allSameType:
				pool = "legacy"
			case vd.Type == "QLC":
				pool = "iubig"
			default:
				pool = "iu4k"
			}

			vdCtx, l := l.WithValues("pool", pool)
			l.Info("Adding virtual drive to cluster")

			// Add drive using virtual UUID (virtual UUID was already signed on device via AddVirtualDrives)
			err = wekaService.AddDrive(vdCtx, *container.Status.ClusterContainerID, vd.VirtualUUID, &pool)
			if err != nil {
				l.Error(err, "Error adding virtual drive to cluster")
				errs = append(errs, err)
				continue
			}

			l.Info("Virtual drive added to cluster")
			_ = r.RecordEvent("", "VirtualDriveAdded", fmt.Sprintf("Virtual drive %s added to cluster", vd.VirtualUUID)) //nolint:errcheck // error return value intentionally not checked
		}
	} else {
		drivesAddedBySerial := make(map[string]bool)
		for _, s := range container.Status.GetAddedDrivesSerials() {
			drivesAddedBySerial[s] = true
		}

		// Check if --pool flag is supported (only once for all drives in this container)
		supportsPool := wekaService.SupportsFlag(ctx, "weka cluster drive add", "pool")
		var pool *string
		if supportsPool {
			legacyPool := "legacy"
			pool = &legacyPool
			logger.Info("Weka version supports --pool flag, using legacy pool for all drives")
		} else {
			logger.Info("Weka version does not support --pool flag, drives will be added without pool specification")
		}

		// Regular mode: add exclusive drives
		// Adding drives to weka one by one
		for _, drive := range container.Status.Allocations.Drives {
			_, l := logger.WithValues("drive_name", drive)

			// check if drive is already added to weka
			if _, ok := drivesAddedBySerial[drive]; ok {
				l.Info("drive is already added to weka")
				continue
			}

			l.Info("Attempting to configure drive")

			err := getKernelDrives()
			if err != nil {
				return err
			}
			if _, ok := kDrives[drive]; !ok {
				driveErr := fmt.Errorf("drive %s not found in kernel", drive)
				l.Error(driveErr, "Error configuring drive")
				errs = append(errs, driveErr)
				continue
			}

			if kDrives[drive].Partition == "" {
				partErr := fmt.Errorf("drive %v is not partitioned", kDrives[drive])
				l.Error(partErr, "Error configuring drive")
				errs = append(errs, partErr)
				continue
			}

			driveCtx, l := l.WithValues("partition", kDrives[drive].Partition, "weka_guid", kDrives[drive].WekaGuid)

			if kDrives[drive].IsSigned {
				l.Info("Drive has Weka signature on it, forbidding usage")
				sigErr := fmt.Errorf("drive %s has Weka signature on it, forbidding usage", drive)
				errs = append(errs, sigErr)
				continue
			}

			if pool != nil {
				driveCtx, l = l.WithValues("pool", *pool)
			}
			l.Info("Adding drive into system")
			// TODO: We need to login here. Maybe handle it on wekaauthcli level?
			err = wekaService.AddDrive(driveCtx, *container.Status.ClusterContainerID, kDrives[drive].DevicePath, pool)
			if err != nil {
				l.Error(err, "Error adding drive into system")
				errs = append(errs, err)
				continue
			} else {
				l.Info("Drive added into system")
				_ = r.RecordEvent("", "DriveAdded", fmt.Sprintf("Drive %s added", drive)) //nolint:errcheck // error return value intentionally not checked
			}
		}
	}

	if len(errs) > 0 {
		err := fmt.Errorf("errors while adding drives: %v", errs)
		return err
	}

	logger.InfoWithStatus(codes.Ok, "All drives added")

	return r.updateContainerStatusIfNotEquals(ctx, weka.Running)
}

func (r *containerReconcilerLoop) UpdateWekaAddedDrives(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	container := r.container

	timeout := time.Minute * 2
	wekaService := services.NewWekaServiceWithTimeout(r.ExecService, container, &timeout)

	// NOTE: this is a costly operation weka-side, so we should do it only once per container reconciliation
	drivesAdded, err := wekaService.ListContainerDrives(ctx, *container.Status.ClusterContainerID)
	if err != nil {
		return err
	}

	logger.Info("Fetched added drives from weka", "count", len(drivesAdded), "drives", drivesAdded)

	container.Status.AddedDrives = drivesAdded
	err = r.Status().Update(ctx, container)
	if err != nil {
		err = fmt.Errorf("cannot update container status with added drives: %w", err)
		return err
	}

	logger.Info("Updated container status with added drives", "count", len(drivesAdded))

	return nil
}

// UpdateFullDrivesAnnotationFromAddedDrives updates the weka-full-drives node annotation
// based on the drive container's Status.AddedDrives field. This is the upgrade path for
// existing Weka clusters where drives are already in Weka (not visible in kernel).
// Only runs for exclusive-drive containers (not drive-sharing mode).
func (r *containerReconcilerLoop) UpdateFullDrivesAnnotationFromAddedDrives(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	container := r.container

	// Collect drives with known capacity from AddedDrives status
	type driveCapacity struct {
		serial      string
		capacityGiB int
	}
	var drivesWithCapacity []driveCapacity
	for _, d := range container.Status.AddedDrives {
		if d.SerialNumber == "" || d.SizeBytes == 0 {
			continue
		}
		drivesWithCapacity = append(drivesWithCapacity, driveCapacity{
			serial:      d.SerialNumber,
			capacityGiB: int(d.SizeBytes / (1024 * 1024 * 1024)),
		})
	}
	if len(drivesWithCapacity) == 0 {
		return nil // nothing to update
	}

	node := r.node

	// Read existing full-drives annotation entries
	existingEntries, err := domain.ReadDriveAnnotations(node.Annotations[consts.AnnotationWekaFullDrives])
	if err != nil {
		return fmt.Errorf("failed to read weka-full-drives annotation: %w", err)
	}

	// Build map of existing entries (serial → entry)
	entryMap := make(map[string]domain.DriveEntry, len(existingEntries))
	for _, e := range existingEntries {
		entryMap[e.Serial] = e
	}

	// Merge: add or update entries from AddedDrives (only if not already present with capacity)
	updated := false
	for _, d := range drivesWithCapacity {
		if existing, ok := entryMap[d.serial]; ok && existing.CapacityGiB > 0 {
			continue // already have good capacity data
		}
		entryMap[d.serial] = domain.DriveEntry{Serial: d.serial, CapacityGiB: d.capacityGiB}
		updated = true
	}

	if !updated {
		return nil // annotation already has capacity for all AddedDrives
	}

	updatedEntries := make([]domain.DriveEntry, 0, len(entryMap))
	for _, e := range entryMap {
		updatedEntries = append(updatedEntries, e)
	}
	// Sort entries by serial for stable annotation (not strictly necessary, but helps with readability and testing)
	slices.SortFunc(updatedEntries, func(a, b domain.DriveEntry) int {
		return strings.Compare(a.Serial, b.Serial)
	})
	annotationBytes, err := json.Marshal(updatedEntries)
	if err != nil {
		return fmt.Errorf("failed to marshal weka-full-drives annotation: %w", err)
	}
	if node.Annotations == nil {
		node.Annotations = make(map[string]string)
	}
	node.Annotations[consts.AnnotationWekaFullDrives] = string(annotationBytes)

	var blockedDrives []string
	if blockedStr, ok := node.Annotations[consts.AnnotationBlockedDrives]; ok && blockedStr != "" {
		if err := json.Unmarshal([]byte(blockedStr), &blockedDrives); err != nil {
			return fmt.Errorf("failed to unmarshal blocked-drives annotation on node %s: %w", node.Name, err)
		}
	}
	totalSerials := domain.DriveEntrySerials(updatedEntries)
	domain.SetNodeDriveAllocatable(node, totalSerials, blockedDrives)

	if err := r.Status().Update(ctx, node); err != nil {
		return fmt.Errorf("failed to update node status: %w", err)
	}
	if err := r.Update(ctx, node); err != nil {
		return fmt.Errorf("failed to update node annotations: %w", err)
	}

	logger.Info("Updated weka-full-drives annotation from AddedDrives", "node", node.Name, "count", len(drivesWithCapacity), "total", len(totalSerials))
	return nil
}

// EnsureNodeFullDrivesAnnotation ensures the weka-full-drives annotation is present on the
// container's node before allowing drive or compute containers to proceed.
// If the annotation is missing, it triggers NewDiscoverDrivesOperation (using the WekaCluster
// as owner) to create adhoc discover-drives containers on the cluster's drive nodes.
// The adhoc containers' wekacontainer reconciler will write the annotation upon completion.
//
// Note: for the upgrade path, UpdateFullDrivesAnnotationFromAddedDrives (run earlier in the
// pipeline for drive containers) may already have populated the annotation from AddedDrives.
func (r *containerReconcilerLoop) EnsureNodeFullDrivesAnnotation(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	nodeName := r.container.Status.NodeAffinity
	if nodeName == "" {
		return nil
	}

	node := &v1.Node{}
	if err := r.Get(ctx, client.ObjectKey{Name: string(nodeName)}, node); err != nil {
		return fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}

	if node.Annotations[consts.AnnotationWekaFullDrives] != "" {
		return nil // annotation already present
	}

	logger.Info("Node missing weka-full-drives annotation, triggering discover-drives operation", "node", nodeName)

	cluster, err := r.getCluster(ctx)
	if err != nil {
		return fmt.Errorf("failed to get owner cluster: %w", err)
	}

	nodeSelector := cluster.GetNodeSelectorForRole(weka.WekaContainerModeDrive)
	if nodeSelector == nil {
		nodeSelector = make(map[string]string)
	}
	// Scope discovery to the specific node that is missing the annotation.
	// Each drive container's reconciler handles its own node independently.
	nodeSelector["kubernetes.io/hostname"] = string(nodeName)
	ownerDetails := cluster.ToOwnerObject()

	discoverOp := operations.NewDiscoverDrivesOperation(
		r.Manager,
		&weka.DiscoverDrivesPayload{NodeSelector: nodeSelector},
		cluster,
		*ownerDetails,
		"",
		func(ctx context.Context) error { return nil }, // no-op: annotation written by adhoc container's own reconciler
		false,
	)

	if err := operations.ExecuteOperation(ctx, discoverOp); err != nil {
		// Operation is still in progress (WaitError) or failed — propagate
		return err
	}

	// Operation completed — re-check annotation (adhoc container reconciler may not have written it yet)
	if err := r.Get(ctx, client.ObjectKey{Name: string(nodeName)}, node); err != nil {
		return fmt.Errorf("failed to re-read node %s after discovery: %w", nodeName, err)
	}
	if node.Annotations[consts.AnnotationWekaFullDrives] == "" {
		return lifecycle.NewWaitErrorWithDuration(
			fmt.Errorf("waiting for weka-full-drives annotation to be written on node %s", nodeName),
			10*time.Second,
		)
	}

	return nil
}

func (r *containerReconcilerLoop) RemoveDrives(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	container := r.container

	blockedSerials, err := r.getNodeBlockedDriveSerials(ctx)
	if err != nil {
		return fmt.Errorf("failed to get blocked drives from node: %w", err)
	}

	if len(blockedSerials) == 0 {
		return nil
	}

	addedDrivesMap := make(map[string]weka.Drive)
	for _, d := range container.Status.AddedDrives {
		if d.SerialNumber == "" {
			logger.Warn("Drive has no serial number", "drive", d)
			continue
		}
		addedDrivesMap[d.SerialNumber] = d
	}

	toRemoveDrives := make(map[string]weka.Drive)

	// check which drives from "blocked drives" list are still present in weka
	for _, blockedDriveSerial := range blockedSerials {
		if d, ok := addedDrivesMap[blockedDriveSerial]; ok {
			toRemoveDrives[blockedDriveSerial] = d
		}
	}

	if len(toRemoveDrives) == 0 {
		logger.Info("No drives to remove from weka")
		return nil
	}

	err = r.deallocateDrivesBySerials(ctx, blockedSerials)
	if err != nil {
		return err
	}

	var errs []error

	timeout := time.Minute * 2
	wekaService := services.NewWekaServiceWithTimeout(r.ExecService, container, &timeout)

	for _, drive := range toRemoveDrives {
		err := r.removeDriveFromWeka(ctx, &drive, wekaService, container.UsesDriveSharing())
		if err != nil {
			errs = append(errs, err)
			continue
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors during drive replacement: %v", errs)
	}

	// adding of new drive is covered by EnsureDrives
	return nil
}

func (r *containerReconcilerLoop) RemoveDrivesByPhysicalUuids(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	container := r.container

	blockedPhysicalUuids, err := r.getNodeBlockedDriveUuids(ctx)
	if err != nil {
		return fmt.Errorf("failed to get blocked drive UUIDs from node: %w", err)
	}

	if len(blockedPhysicalUuids) == 0 {
		return nil
	}

	// get all virtual drives and create map of virtualUUID -> physicalUUID
	virtualToPhysicalUuidsMap := make(map[string]string)

	ssdproxyContainer, err := r.findSSDProxyOnNode(ctx)
	if err != nil {
		return fmt.Errorf("failed to find ssdproxy container: %w", err)
	}

	agentPod, err := r.GetNodeAgentPod(ctx, container.GetNodeAffinity())
	if err != nil {
		return fmt.Errorf("failed to get node agent pod: %w", err)
	}

	token, err := r.getNodeAgentToken(ctx)
	if err != nil {
		return fmt.Errorf("failed to get node agent token: %w", err)
	}

	virtualDrives, err := r.ssdProxyListVirtualDrives(ctx, string(ssdproxyContainer.GetUID()), agentPod, token)
	if err != nil {
		return fmt.Errorf("failed to list virtual drives: %w", err)
	}

	for _, vd := range virtualDrives {
		virtualToPhysicalUuidsMap[vd.VirtualUUID] = vd.PhysicalUUID
	}

	logger.Info("Built virtual to physical UUID map",
		"virtual_drives_count", len(virtualToPhysicalUuidsMap))

	addedDrivesByPhysicalUuidsMap := make(map[string]weka.Drive)
	for _, d := range container.Status.AddedDrives {
		physicalUuid, ok := virtualToPhysicalUuidsMap[d.Uuid]
		if !ok {
			logger.Warn("Added drive virtual UUID has no matching physical UUID", "virtual_uuid", d.Uuid)

			_ = r.RecordEventThrottled(v1.EventTypeWarning, "DriveRemovalSkipped", fmt.Sprintf("Added drive virtual UUID %s has no matching physical UUID", d.Uuid), time.Minute*1) //nolint:errcheck // error return value intentionally not checked
			continue
		}

		addedDrivesByPhysicalUuidsMap[physicalUuid] = d
	}

	toRemoveDrives := make(map[string]weka.Drive)

	for _, blockedDriveUuid := range blockedPhysicalUuids {
		if d, ok := addedDrivesByPhysicalUuidsMap[blockedDriveUuid]; ok {
			toRemoveDrives[blockedDriveUuid] = d
		}
	}

	if len(toRemoveDrives) == 0 {
		logger.Info("No drives to remove from weka")
		return nil
	}

	err = r.deallocateDrivesByPhysicalUuids(ctx, blockedPhysicalUuids)
	if err != nil {
		return err
	}

	var errs []error

	timeout := time.Minute * 2
	wekaService := services.NewWekaServiceWithTimeout(r.ExecService, container, &timeout)
	for _, drive := range toRemoveDrives {
		err := r.removeDriveFromWeka(ctx, &drive, wekaService, container.UsesDriveSharing())
		if err != nil {
			errs = append(errs, err)
			continue
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors during drive removal: %v", errs)
	}

	return nil
}

// TODO: make it work with physical UUIDs as well
func (r *containerReconcilerLoop) MarkDrivesForRemoval(ctx context.Context) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "MarkDrivesForRemoval")
	defer logger.End()

	container := r.container

	if unhealthy, _, _ := utils.IsUnhealthy(ctx, container); unhealthy { //nolint:errcheck // error return value intentionally not checked
		return errors.New("container is uneligible for drive allocation (unhealthy)")
	}

	var toRemoveSerialIDs []string

	// check if any container has failed drives
	driveFailures := container.Status.GetStats().Drives.DriveFailures
	if len(driveFailures) > 0 {
		toRemoveSerialIDs = make([]string, 0, len(driveFailures))
		for _, driveFailure := range driveFailures {
			logger.Info("Drive marked as failed, marking for removal", "drive", driveFailure.SerialId)
			toRemoveSerialIDs = append(toRemoveSerialIDs, driveFailure.SerialId)
		}
	}

	if len(toRemoveSerialIDs) == 0 {
		logger.Info("No drives to mark for removal")
		return nil
	}

	// check if drives are already "blocked" on the node
	blockedDriveSerials, err := r.getNodeBlockedDriveSerials(ctx)
	if err != nil {
		return fmt.Errorf("failed to get blocked drives from node: %w", err)
	}
	allBlocked := true
	for _, drive := range toRemoveSerialIDs {
		if !slices.Contains(blockedDriveSerials, drive) {
			allBlocked = false
			break
		}
	}
	if allBlocked {
		logger.Info("Drives are already blocked on the node, no need to block again", "drives", toRemoveSerialIDs)
		return nil
	}

	ctx, logger = instrumentation.CreateLogSpan(ctx, "BlockDrives", "drives", toRemoveSerialIDs)
	defer logger.End()

	logger.Info("Blocking drives on the node")

	// call "block-drives" manual operation for the drives to be removed
	payload := &weka.BlockDrivesPayload{
		SerialIDs: toRemoveSerialIDs,
		Node:      string(container.GetNodeAffinity()),
	}
	op := operations.NewBlockDrivesOperation(r.Manager, payload, nil, nil, nil)
	err = operations.ExecuteOperation(ctx, op)
	if err != nil {
		return fmt.Errorf("failed to block drives %v: %w", toRemoveSerialIDs, err)
	}

	_ = r.RecordEvent(v1.EventTypeWarning, "DrivesMarkedForRemoval", fmt.Sprintf("Drives %v marked for removal from container", toRemoveSerialIDs)) //nolint:errcheck // error return value intentionally not checked

	return nil
}

func (r *containerReconcilerLoop) getKernelDrives(ctx context.Context, executor util.Exec) (map[string]domain.DriveInfo, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "getKernelDrives")
	defer logger.End()

	// Try to get drives from node-agent first
	drives, err := r.getKernelDrivesFromNodeAgent(ctx)
	if err != nil {
		logger.Info("Failed to get drives from node-agent, falling back to old implementation", "error", err)
		// Fallback to old implementation: read drives.json from pod
		drives, err = r.getKernelDrivesFromPod(ctx, executor)
		if err != nil {
			return nil, err
		}
	}

	return drives, nil
}

func (r *containerReconcilerLoop) getKernelDrivesFromNodeAgent(ctx context.Context) (map[string]domain.DriveInfo, error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "getKernelDrivesFromNodeAgent")
	defer logger.End()

	// Find node-agent pod on the same node as this container
	agentPod, err := r.GetNodeAgentPod(ctx, r.container.GetNodeAffinity())
	if err != nil {
		return nil, fmt.Errorf("failed to get node-agent pod: %w", err)
	}

	// Get token for authentication
	token, err := r.getNodeAgentToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get node-agent token: %w", err)
	}

	// Call /findDrives endpoint
	url := "http://" + net.JoinHostPort(agentPod.Status.PodIP, "8090") + "/findDrives"

	timeout := time.Second * 30
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resp, err := util.SendJsonRequest(ctx, url, []byte("{}"), util.RequestOptions{AuthHeader: "Token " + token})
	if err != nil {
		return nil, fmt.Errorf("failed to call node-agent: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // error return value intentionally not checked

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("findDrives failed: %s, status: %d", string(body), resp.StatusCode)
	}

	var response struct {
		Drives []domain.DriveInfo `json:"drives"`
		Error  string             `json:"error,omitempty"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if response.Error != "" {
		return nil, fmt.Errorf("node-agent returned error: %s", response.Error)
	}

	logger.Info("Successfully fetched drives from node-agent", "count", len(response.Drives))

	// Convert to map by serial ID
	serialIdMap := make(map[string]domain.DriveInfo)
	for _, drive := range response.Drives {
		serialIdMap[drive.SerialId] = drive
	}

	return serialIdMap, nil
}

func (r *containerReconcilerLoop) getKernelDrivesFromPod(ctx context.Context, executor util.Exec) (map[string]domain.DriveInfo, error) {
	stdout, _, err := executor.ExecNamed(ctx, "FetchKernelDrives",
		[]string{"bash", "-ce", "cat /opt/weka/k8s-runtime/drives.json"})
	if err != nil {
		return nil, err
	}
	var drives []domain.DriveInfo
	err = json.Unmarshal(stdout.Bytes(), &drives)
	if err != nil {
		return nil, err
	}
	serialIdMap := make(map[string]domain.DriveInfo)
	for _, drive := range drives {
		serialIdMap[drive.SerialId] = drive
	}

	return serialIdMap, nil
}

func (r *containerReconcilerLoop) getNodeBlockedDriveUuids(ctx context.Context) (blockedPhysicalUuids []string, err error) {
	_, logger := instrumentation.CreateLogSpan(ctx, "getNodeBlockedDriveUuids")
	defer logger.End()

	node := r.node
	if node == nil {
		return nil, errors.New("node is nil")
	}

	// drives blocked by physical UUIDs (for drive sharing / proxy mode)
	blockedPhysicalUuids = make([]string, 0)
	blockedUuidsStr, ok := node.Annotations[consts.AnnotationBlockedDrivesPhysicalUuids]
	if ok && blockedUuidsStr != "" {
		if err := json.Unmarshal([]byte(blockedUuidsStr), &blockedPhysicalUuids); err != nil {
			return nil, fmt.Errorf("failed to unmarshal blocked shared drives: %w", err)
		}
	}

	logger.Debug("Fetched blocked drives from node annotation", "blocked_drives_uuids", blockedPhysicalUuids)

	return blockedPhysicalUuids, nil
}

func (r *containerReconcilerLoop) getNodeBlockedDriveSerials(ctx context.Context) (blockedSerials []string, err error) {
	_, logger := instrumentation.CreateLogSpan(ctx, "getNodeBlockedDriveSerials")
	defer logger.End()

	node := r.node
	if node == nil {
		return nil, errors.New("node is nil")
	}

	// drives blocked by serial IDs
	blockedSerials = make([]string, 0)
	blockedDrivesStr, ok := node.Annotations[consts.AnnotationBlockedDrives]
	if ok && blockedDrivesStr != "" {
		err := json.Unmarshal([]byte(blockedDrivesStr), &blockedSerials)
		if err != nil {
			return nil, fmt.Errorf("failed to unmarshal blocked drives: %v", err)
		}
	}

	logger.Debug("Fetched blocked drives from node annotation", "blocked_drives_serials", blockedSerials)

	return blockedSerials, nil
}

func (r *containerReconcilerLoop) removeDriveFromWeka(ctx context.Context, drive *weka.Drive, wekaService services.WekaService, useDriveSharing bool) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "removeReplacedDriveFromWeka", "drive_uuid", drive.Uuid, "drive_serial", drive.SerialNumber)
	defer logger.End()

	reFetchedDrive, err := wekaService.GetClusterDrive(ctx, drive.Uuid)
	var notFoundErr *services.DriveNotFound
	if errors.As(err, &notFoundErr) {
		logger.Info("Drive not found in weka, assuming already removed")
		return nil
	}
	if err != nil {
		err = fmt.Errorf("error fetching drive %s (%s) before removal: %w", drive.SerialNumber, drive.Uuid, err)
		return err
	}

	switch reFetchedDrive.Status {
	case services.DriveStatusActive, services.DriveStatusInactive:
		// Weka's RemoveDrive rejects unless should_be_active=false has been set via
		// DeactivateDrive, even when the drive is already INACTIVE.
		logger.Info("Deactivating drive")
		deactivateErr := wekaService.DeactivateDrive(ctx, drive.Uuid)
		if deactivateErr != nil {
			return fmt.Errorf("error deactivating drive %s: %w", drive.SerialNumber, deactivateErr)
		}

		_ = r.RecordEvent("", "DriveDeactivated", fmt.Sprintf("Drive %s deactivated", drive.SerialNumber)) //nolint:errcheck // error return value intentionally not checked
	default:
		return fmt.Errorf("drive has status '%s', wait for it to become '%s'", drive.Status, services.DriveStatusInactive)
	}

	// remove failed (replaced) drive from weka
	logger.Info("Removing drive")

	err = wekaService.RemoveDrive(ctx, drive.Uuid)
	if err != nil {
		err = fmt.Errorf("error removing drive %s: %w", drive.SerialNumber, err)
		return err
	}

	_ = r.RecordEvent("", "DriveRemoved", fmt.Sprintf("Drive %s removed", drive.SerialNumber)) //nolint:errcheck // error return value intentionally not checked

	logger.Info("Drive removed from weka")

	if useDriveSharing && !r.container.Spec.GetOverrides().SkipVirtualDrivesRemoval {
		// remove virtual drive on ssdproxy
		err = r.removeVirtualDrive(ctx, drive.Uuid)
		if err != nil {
			return err
		}
	}

	return nil
}
