package wekacluster

import (
	"context"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	"k8s.io/apimachinery/pkg/types"

	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/services/discovery"
)

func (r *WekaClusterReconciler) GC(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "GC")
	defer end()

	containers := discovery.GetAllContainers(ctx, r.Client)
	resourcesAllocator, err := allocator.GetAllocator(ctx, r.Client)
	if err != nil {
		return err
	}

	existingContainers := make(map[string]map[string]bool)
	for _, container := range containers {
		if existingContainers[container.Namespace] == nil {
			existingContainers[container.Namespace] = make(map[string]bool)
		}
		existingContainers[container.Namespace][container.Name] = true
	}

	allocations, err := resourcesAllocator.GetAllocations(ctx)
	if err != nil {
		return err
	}

	misses := make(map[types.NamespacedName]bool)
	for _, nodeAlloc := range allocations.NodeMap {
		for owner, _ := range nodeAlloc.AllocatedRanges {
			if existingContainers[owner.Namespace] == nil || existingContainers[owner.Namespace][owner.Container] == false {
				misses[owner.ToNamespacedName()] = true
			}
		}

		for owner, _ := range nodeAlloc.EthSlots {
			if existingContainers[owner.Namespace] == nil || existingContainers[owner.Namespace][owner.Container] == false {
				misses[owner.ToNamespacedName()] = true
			}
		}

		for owner, _ := range nodeAlloc.Drives {
			if existingContainers[owner.Namespace] == nil || existingContainers[owner.Namespace][owner.Container] == false {
				misses[owner.ToNamespacedName()] = true
			}
		}
	}

	detectZombiesTime := config.Config.WekaAllocZombieDeleteAfter

	if r.DetectedZombies == nil {
		r.DetectedZombies = make(map[types.NamespacedName]time.Time)
	}

	for owner := range misses {
		if firstDetected, ok := r.DetectedZombies[owner]; ok {
			if time.Since(firstDetected) > detectZombiesTime {
				err := resourcesAllocator.DeallocateNamespacedObject(ctx, owner)
				if err != nil {
					logger.Error(err, "Failed to deallocate container", "owner", owner)
				}
				delete(r.DetectedZombies, owner)
			}
		} else {
			r.DetectedZombies[owner] = time.Now()
		}
	}
	return nil
}
