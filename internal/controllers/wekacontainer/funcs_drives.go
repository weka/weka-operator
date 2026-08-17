package wekacontainer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

	"github.com/weka/weka-operator/internal/capacityplanner"
	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/controllers/operations"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/controllers/utils"
	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/services"
	"github.com/weka/weka-operator/pkg/util"
	"github.com/weka/weka-operator/pkg/util/podexec"
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
	c, err := resources.GetWekaPodContainer(r.pod)
	if err != nil {
		// no weka container to size against (nil pod or not found) — skip the feasibility check
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
	// DriveDpdkPerCoreMiB comes from this container's own spec, what its pod (resources/pod.go) was built
	// from, since the cluster's current override may have drifted from that by now.
	cons.DriveDpdkPerCoreMiB = container.Spec.DpdkBaseMemoryMb
	// Spec.NumDrives is the pod's drive term (nonzero only under numDrives+driveCapacity, 200 MiB/drive);
	// RequiredDriveResources must read it, not Status.Allocations.VirtualDrives, to match the pod's request.
	reqHpMiB, reqMemMiB := capacityplanner.RequiredDriveResources(tlcGiB, qlcGiB, container.Spec.NumDrives, cons)

	availHpMiB := podHugepagesRequestMiB(c)
	availMemMiB := int(c.Resources.Requests.Memory().Value() / (1 << 20))

	// Both hugepages and memory scale with the drive-core count, so a pod whose NumCores lags the grown
	// capacity trips one of these before the drives are added. CPU is deliberately NOT gated here: the
	// pod's CPU request is PHYSICAL (numCores*2+1 under dedicated_ht) while the drive-core requirement is
	// in weka DATA cores, so comparing them is meaningless — and the hugepages/memory checks already catch
	// the same under-sizing. Physical-CPU headroom is enforced at cluster-plan time (capacityplanner/cpu.go).
	var shortfall string
	switch {
	case availHpMiB < reqHpMiB:
		shortfall = fmt.Sprintf("hugepages: pod requests %d MiB, need %d MiB", availHpMiB, reqHpMiB)
	case availMemMiB < reqMemMiB:
		shortfall = fmt.Sprintf("memory: pod requests %d MiB, need %d MiB", availMemMiB, reqMemMiB)
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

	executor, err := podexec.NewExecInPod(r.RestClient, r.Manager.GetConfig(), pod)
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

		// A blocked VID is on its way out: re-adding it would undo the removal in progress, and would
		// keep doing so on every pass for as long as its proxy erase kept failing.
		blockedVids := r.blockedVirtualUuidSet(ctx)

		// Add each virtual drive to the cluster
		for _, vd := range container.Status.Allocations.VirtualDrives {
			_, l := logger.WithValues("virtual_uuid", vd.VirtualUUID, "serial", vd.Serial, "physical_uuid", vd.PhysicalUUID)

			if blockedVids[vd.VirtualUUID] {
				l.Info("Virtual drive is blocked on the node, skipping until its removal completes")
				continue
			}

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

	if errs := r.removeDrivesFromWeka(ctx, toRemoveDrives); len(errs) > 0 {
		return fmt.Errorf("errors during drive replacement: %v", errs)
	}

	// adding of new drive is covered by EnsureDrives
	return nil
}

// removeDrivesFromWeka removes every drive in the set, collecting failures rather than stopping at
// the first: one drive that will not deactivate must not strand the others.
func (r *containerReconcilerLoop) removeDrivesFromWeka(ctx context.Context, drives map[string]weka.Drive) []error {
	timeout := time.Minute * 2
	wekaService := services.NewWekaServiceWithTimeout(r.ExecService, r.container, &timeout)
	useDriveSharing := r.container.UsesDriveSharing()

	var errs []error
	for _, drive := range drives {
		if err := r.removeDriveFromWeka(ctx, &drive, wekaService, useDriveSharing); err != nil {
			errs = append(errs, err)
		}
	}

	return errs
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

	if errs := r.removeDrivesFromWeka(ctx, toRemoveDrives); len(errs) > 0 {
		return fmt.Errorf("errors during drive removal: %v", errs)
	}

	return nil
}

// RemoveDrivesByVirtualUuids removes the virtual drives named in the node's blocked-virtual-uuids
// annotation, leaving every other VID on the same physical drives — including other tenants' —
// alone.
//
// The work list comes from the allocation record, not Status.AddedDrives: AddedDrives is refreshed
// from weka, so once the cluster removal lands the drive disappears from it and a retry would have
// nothing to iterate, stranding a record that still claims a VID whose erase never completed.
func (r *containerReconcilerLoop) RemoveDrivesByVirtualUuids(ctx context.Context) error {
	logger := instrumentation.CurrentSpanLogger(ctx)

	container := r.container

	blockedVirtualUuids, err := r.getNodeBlockedDriveVirtualUuids(ctx)
	if err != nil {
		return fmt.Errorf("failed to get blocked virtual drive UUIDs from node: %w", err)
	}

	if len(blockedVirtualUuids) == 0 {
		return nil
	}

	if container.Status.Allocations == nil {
		return nil
	}

	toRemove := make([]string, 0, len(blockedVirtualUuids))
	for _, vd := range container.Status.Allocations.VirtualDrives {
		if slices.Contains(blockedVirtualUuids, vd.VirtualUUID) {
			toRemove = append(toRemove, vd.VirtualUUID)
		}
	}

	// Nothing this container owns is blocked. Returning here keeps the common case to a couple of
	// slice scans, with no proxy or cluster contact at all.
	if len(toRemove) == 0 {
		logger.Info("No virtual drives to remove for container", "container", container.Name)
		return nil
	}

	// Resolve the proxy, its node-agent pod and an auth token, and confirm it is reachable, before
	// touching the cluster. Removing a VID from weka and only then discovering the proxy is down
	// would leave it signed on the physical drive with nothing to retry the erase, so on an
	// unreachable proxy nothing is removed and the step simply retries. Resolving once here, rather
	// than inside the loop below, saves a K8s List call per VID for both the ssdproxy container and
	// the agent pod. Skipped when SkipVirtualDrivesRemoval is set: removeVirtualDriveFromWekaAndProxy
	// never uses these values in that case, since the erase they guard doesn't run.
	var (
		ssdproxyUID string
		agentPod    *v1.Pod
		token       string
	)
	if !container.Spec.GetOverrides().SkipVirtualDrivesRemoval {
		ssdproxyUID, agentPod, token, err = r.resolveSSDProxy(ctx)
		if err != nil {
			return fmt.Errorf("ssdproxy unreachable, removing no virtual drives: %w", err)
		}
	}

	timeout := time.Minute * 2
	wekaService := services.NewWekaServiceWithTimeout(r.ExecService, container, &timeout)

	removedVids := make([]string, 0, len(toRemove))

	var errs []error
	for _, vid := range toRemove {
		if err := r.removeVirtualDriveFromWekaAndProxy(ctx, vid, wekaService, ssdproxyUID, agentPod, token); err != nil {
			errs = append(errs, fmt.Errorf("virtual drive %s: %w", vid, err))
			continue
		}
		removedVids = append(removedVids, vid)
	}

	// Only VIDs whose cluster removal AND proxy erase both succeeded may leave the record. Dropping
	// one whose erase failed would orphan it: nothing would claim it, so nothing would retry.
	if len(removedVids) > 0 {
		if err := r.deallocateDrivesByVirtualUuids(ctx, removedVids); err != nil {
			errs = append(errs, err)
		}
	}

	if len(removedVids) > 0 && !config.Config.DriveSharing.EnableDynamicDriveScaling {
		_ = r.RecordEventThrottled(v1.EventTypeWarning, "VirtualDriveReplacementDisabled", //nolint:errcheck // error return value intentionally not checked
			fmt.Sprintf("Removed virtual drives %v; no replacement will be created because dynamic drive "+
				"scaling is disabled (ENABLE_DYNAMIC_DRIVE_SCALING_FOR_SHARED_DRIVES). The container stays "+
				"below its target capacity until it is enabled.", removedVids), time.Minute*10)
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors during virtual drive removal: %v", errs)
	}

	return nil
}

// resolveSSDProxy resolves the node's ssdproxy container UID, its node-agent pod and an auth token,
// and confirms the proxy answers before a caller mutates cluster state on the assumption that it
// will. The three are returned so a caller doing per-VID work can resolve them once up front instead
// of once per VID.
func (r *containerReconcilerLoop) resolveSSDProxy(ctx context.Context) (ssdproxyUID string, agentPod *v1.Pod, token string, err error) {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "resolveSSDProxy", "node", r.container.GetNodeAffinity())
	defer logger.End()

	ssdproxyContainer, err := r.findSSDProxyOnNode(ctx)
	if err != nil {
		return "", nil, "", fmt.Errorf("failed to find ssdproxy container: %w", err)
	}

	agentPod, err = r.GetNodeAgentPod(ctx, r.container.GetNodeAffinity())
	if err != nil {
		return "", nil, "", fmt.Errorf("failed to get node agent pod: %w", err)
	}

	token, err = r.getNodeAgentToken(ctx)
	if err != nil {
		return "", nil, "", fmt.Errorf("failed to get node agent token: %w", err)
	}

	ssdproxyUID = string(ssdproxyContainer.GetUID())

	if _, err := r.ssdProxyListVirtualDrives(ctx, ssdproxyUID, agentPod, token); err != nil {
		return "", nil, "", fmt.Errorf("failed to list virtual drives: %w", err)
	}

	return ssdproxyUID, agentPod, token, nil
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

func (r *containerReconcilerLoop) getKernelDrives(ctx context.Context, executor podexec.Exec) (map[string]domain.DriveInfo, error) {
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
	url := fmt.Sprintf("http://%s:8090/findDrives", agentPod.Status.PodIP)

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

func (r *containerReconcilerLoop) getKernelDrivesFromPod(ctx context.Context, executor podexec.Exec) (map[string]domain.DriveInfo, error) {
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

// readNodeBlockedList decodes one of the node's blocked-drive annotations via the given domain
// reader, under a span named for the caller.
func (r *containerReconcilerLoop) readNodeBlockedList(
	ctx context.Context, spanName, logKey string, read func(*v1.Node) ([]string, error),
) ([]string, error) {
	_, logger := instrumentation.CreateLogSpan(ctx, spanName)
	defer logger.End()

	if r.node == nil {
		return nil, errors.New("node is nil")
	}

	blocked, err := read(r.node)
	if err != nil {
		return nil, err
	}

	logger.Debug("Fetched blocked drives from node annotation", logKey, blocked)

	return blocked, nil
}

// drives blocked by physical UUID (drive sharing / proxy mode)
func (r *containerReconcilerLoop) getNodeBlockedDriveUuids(ctx context.Context) ([]string, error) {
	return r.readNodeBlockedList(ctx, "getNodeBlockedDriveUuids", "blocked_drives_uuids",
		domain.ReadBlockedDrivePhysicalUUIDs)
}

// drives blocked by serial ID
func (r *containerReconcilerLoop) getNodeBlockedDriveSerials(ctx context.Context) ([]string, error) {
	return r.readNodeBlockedList(ctx, "getNodeBlockedDriveSerials", "blocked_drives_serials",
		domain.ReadBlockedDriveSerials)
}

// individual virtual drives blocked by virtual UUID (drive sharing only)
func (r *containerReconcilerLoop) getNodeBlockedDriveVirtualUuids(ctx context.Context) ([]string, error) {
	return r.readNodeBlockedList(ctx, "getNodeBlockedDriveVirtualUuids", "blocked_drives_virtual_uuids",
		domain.ReadBlockedDriveVirtualUUIDs)
}

// blockedVirtualUuidSet returns the node's blocked VIDs as a lookup set, for the enforcement loops to
// skip. Without it a VID whose proxy erase keeps failing gets re-signed and re-added every pass,
// churning the cluster for as long as the proxy is down.
//
// A read failure yields an empty set and a log line rather than an error: a malformed annotation must
// never stop drives being signed or added.
func (r *containerReconcilerLoop) blockedVirtualUuidSet(ctx context.Context) map[string]bool {
	blocked, err := r.getNodeBlockedDriveVirtualUuids(ctx)
	if err != nil {
		instrumentation.CurrentSpanLogger(ctx).Warn(
			"Could not read blocked virtual drives from node, treating none as blocked", "error", err)
		return map[string]bool{}
	}

	set := make(map[string]bool, len(blocked))
	for _, vid := range blocked {
		set[vid] = true
	}

	return set
}

// driveNaming supplies the human-facing wording for cluster-removal events, which differs between
// physical drives (identified by serial) and virtual drives (identified by VID).
type driveNaming struct {
	eventPrefix   string // prefixes the event Reason: "Drive" -> DriveRemoved
	noun          string // opens the event message: "Virtual drive %s removed from the cluster"
	removedSuffix string // appended to the "removed" event message only, e.g. " from the cluster"
}

var (
	physicalDriveNaming = driveNaming{eventPrefix: "Drive", noun: "Drive"}
	virtualDriveNaming  = driveNaming{eventPrefix: "VirtualDrive", noun: "Virtual drive", removedSuffix: " from the cluster"}
)

// removeDriveFromCluster deactivates a drive and removes it from the weka cluster.
// Returns an error wrapping *services.DriveNotFound when the drive is already gone, for the caller
// to classify via errors.As.
// driveRef is the identifier shown to a human in events: a serial number for a physical drive, a
// virtual UUID for a virtual one. It is not necessarily the uuid used for the API calls.
func (r *containerReconcilerLoop) removeDriveFromCluster(
	ctx context.Context, uuid, driveRef string, naming driveNaming, wekaService services.WekaService,
) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "removeDriveFromCluster", "drive_uuid", uuid, "drive_ref", driveRef)
	defer logger.End()

	noun := strings.ToLower(naming.noun)

	reFetchedDrive, err := wekaService.GetClusterDrive(ctx, uuid)
	if err != nil {
		ref := driveRef
		if uuid != driveRef {
			ref = fmt.Sprintf("%s (%s)", driveRef, uuid)
		}
		return fmt.Errorf("error fetching %s %s before removal: %w", noun, ref, err)
	}

	switch reFetchedDrive.Status {
	case services.DriveStatusActive, services.DriveStatusInactive:
		// Weka's RemoveDrive rejects unless should_be_active=false has been set via
		// DeactivateDrive, even when the drive is already INACTIVE.
		logger.Info(fmt.Sprintf("Deactivating %s", noun))
		if err := wekaService.DeactivateDrive(ctx, uuid); err != nil {
			return fmt.Errorf("error deactivating %s %s: %w", noun, driveRef, err)
		}

		_ = r.RecordEvent("", naming.eventPrefix+"Deactivated", fmt.Sprintf("%s %s deactivated", naming.noun, driveRef)) //nolint:errcheck // error return value intentionally not checked
	default:
		return fmt.Errorf("%s has status '%s', wait for it to become '%s'", noun, reFetchedDrive.Status, services.DriveStatusInactive)
	}

	logger.Info(fmt.Sprintf("Removing %s", noun))
	if err := wekaService.RemoveDrive(ctx, uuid); err != nil {
		return fmt.Errorf("error removing %s %s: %w", noun, driveRef, err)
	}

	_ = r.RecordEvent("", naming.eventPrefix+"Removed", fmt.Sprintf("%s %s removed%s", naming.noun, driveRef, naming.removedSuffix)) //nolint:errcheck // error return value intentionally not checked

	return nil
}

// removeVirtualDriveFromWekaAndProxy removes one VID from the cluster and then erases it from its
// physical drive via the proxy. ssdproxyUID, agentPod and token must come from resolveSSDProxy,
// resolved once by the caller instead of per VID.
//
// Unlike removeDriveFromWeka, a drive already absent from the cluster is not treated as fully
// removed: the erase still runs. Otherwise a successful cluster removal followed by a failed erase
// would be indistinguishable from success on the next attempt, and the VID would stay signed on disk
// forever. Both halves must succeed before the caller may drop the VID from the allocation record.
func (r *containerReconcilerLoop) removeVirtualDriveFromWekaAndProxy(
	ctx context.Context, virtualUUID string, wekaService services.WekaService,
	ssdproxyUID string, agentPod *v1.Pod, token string,
) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "removeVirtualDriveFromWekaAndProxy", "virtual_uuid", virtualUUID)
	defer logger.End()

	err := r.removeDriveFromCluster(ctx, virtualUUID, virtualUUID, virtualDriveNaming, wekaService)

	var notFoundErr *services.DriveNotFound
	switch {
	case errors.As(err, &notFoundErr):
		logger.Info("Virtual drive not in weka, already removed from the cluster; still erasing it from the drive")
	case err != nil:
		return err
	}

	if r.container.Spec.GetOverrides().SkipVirtualDrivesRemoval {
		logger.Info("Skipping proxy erase, SkipVirtualDrivesRemoval override is set")
		return nil
	}

	if err := r.removeVirtualDriveViaProxy(ctx, virtualUUID, ssdproxyUID, agentPod, token); err != nil {
		return fmt.Errorf("error erasing virtual drive %s from the physical drive: %w", virtualUUID, err)
	}

	_ = r.RecordEvent("", "VirtualDriveErased", fmt.Sprintf("Virtual drive %s erased from its physical drive", virtualUUID)) //nolint:errcheck // error return value intentionally not checked

	return nil
}

func (r *containerReconcilerLoop) removeDriveFromWeka(ctx context.Context, drive *weka.Drive, wekaService services.WekaService, useDriveSharing bool) error {
	ctx, logger := instrumentation.CreateLogSpan(ctx, "removeReplacedDriveFromWeka", "drive_uuid", drive.Uuid, "drive_serial", drive.SerialNumber)
	defer logger.End()

	err := r.removeDriveFromCluster(ctx, drive.Uuid, drive.SerialNumber, physicalDriveNaming, wekaService)

	var notFoundErr *services.DriveNotFound
	if errors.As(err, &notFoundErr) {
		logger.Info("Drive not found in weka, assuming already removed")
		return nil
	}
	if err != nil {
		return err
	}

	logger.Info("Drive removed from weka")

	if useDriveSharing && !r.container.Spec.GetOverrides().SkipVirtualDrivesRemoval {
		// remove virtual drive on ssdproxy
		if err := r.removeVirtualDrive(ctx, drive.Uuid); err != nil {
			return err
		}
	}

	return nil
}
