package utils

import (
	v1 "k8s.io/api/core/v1"
)

// PodUnschedulableCondition returns the PodScheduled condition explicitly reporting
// Reason == "Unschedulable", or nil. The condition's LastTransitionTime is the only record of when the
// scheduler gave its verdict, and Message carries the per-node detail ("0/8 nodes are available: 2
// Insufficient hugepages-2Mi") that no other field holds.
func PodUnschedulableCondition(pod *v1.Pod) *v1.PodCondition {
	for i := range pod.Status.Conditions {
		c := &pod.Status.Conditions[i]
		if c.Type == v1.PodScheduled && c.Status == v1.ConditionFalse && c.Reason == "Unschedulable" {
			return c
		}
	}
	return nil
}
