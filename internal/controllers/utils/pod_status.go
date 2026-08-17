package utils

import (
	v1 "k8s.io/api/core/v1"
)

// PodUnschedulableCondition returns the PodScheduled condition explicitly reporting
// Reason == "Unschedulable", or nil. Callers that need to know how LONG the pod has been unschedulable,
// or the scheduler's own explanation, must use this rather than PodUnschedulable: the condition's
// LastTransitionTime is the only record of when the scheduler gave its verdict, and Message carries the
// per-node detail ("0/8 nodes are available: 2 Insufficient hugepages-2Mi") that no other field holds.
func PodUnschedulableCondition(pod *v1.Pod) *v1.PodCondition {
	for i := range pod.Status.Conditions {
		c := &pod.Status.Conditions[i]
		if c.Type == v1.PodScheduled && c.Status == v1.ConditionFalse && c.Reason == "Unschedulable" {
			return c
		}
	}
	return nil
}

// PodUnschedulable reports whether pod has a PodScheduled condition explicitly reporting
// Reason == "Unschedulable" (as opposed to merely lacking Status.NodeName, which is also true of a pod
// that simply hasn't been evaluated by the scheduler yet).
func PodUnschedulable(pod *v1.Pod) bool {
	return PodUnschedulableCondition(pod) != nil
}
