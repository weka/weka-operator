package controllers

import (
	"context"
	"time"

	"github.com/weka/go-weka-observability/instrumentation"
	"github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/allocator"
	"github.com/weka/weka-operator/internal/services/discovery"
	"github.com/weka/weka-operator/pkg/util"
)

func (r *WekaClusterReconciler) GC(ctx context.Context) error {
	ctx, logger, end := instrumentation.GetLogSpan(ctx, "GC")
	defer end()

	containers := discovery.GetAllContainers(ctx, r.Client)
	configStore, err := allocator.NewConfigMapStore(ctx, r.Client)
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

	allocations, err := configStore.GetAllocations(ctx)
	if err != nil {
		return err
	}

	misses := make(map[util.NamespacedObject]bool)
	for _, nodeAlloc := range allocations.NodeMap {
		for owner, _ := range nodeAlloc.AllocatedRanges {
			if existingContainers[owner.Namespace] == nil || existingContainers[owner.Namespace][owner.Container] == false {
				misses[owner.ToNamespacedObject()] = true
			}
		}

		for owner, _ := range nodeAlloc.EthSlots {
			if existingContainers[owner.Namespace] == nil || existingContainers[owner.Namespace][owner.Container] == false {
				misses[owner.ToNamespacedObject()] = true
			}
		}

		for owner, _ := range nodeAlloc.Drives {
			if existingContainers[owner.Namespace] == nil || existingContainers[owner.Namespace][owner.Container] == false {
				misses[owner.ToNamespacedObject()] = true
			}
		}
	}

	detectZombiesTime := config.Config.WekaAllocZombieDeleteAfter

	if r.DetectedZombies == nil {
		r.DetectedZombies = make(map[util.NamespacedObject]time.Time)
	}

	for owner, _ := range misses {
		if firstDetected, ok := r.DetectedZombies[owner]; ok {
			if time.Since(firstDetected) > detectZombiesTime {
				err := allocator.DeallocateNamespacedObject(ctx, owner, configStore)
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
