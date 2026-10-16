package wekacontainer

import (
	"context"
	"fmt"

	"github.com/weka/go-weka-observability/instrumentation"
	v1 "k8s.io/api/core/v1"

	"github.com/weka/weka-operator/internal/consts"
	"github.com/weka/weka-operator/internal/controllers/resources"
	"github.com/weka/weka-operator/internal/services/discovery"
)

// reportSharedCPUs warns about a new pod whose runtime picks weka cores from the pod's own cpuset but
// that gets no exclusive cpuset from the kubelet. Weka containers on the same node then pin the same
// cores (CoreOverlapping), which slows rebuild and redistribution without failing anything. Both
// inputs are fixed for the pod's lifetime, so this runs once, at pod creation.
func (r *containerReconcilerLoop) reportSharedCPUs(ctx context.Context, nodeInfo *discovery.DiscoveryNodeInfo) {
	// dra-driver-cpu assigns CPUs through DRA, outside the kubelet CPU manager.
	if r.container.IsDiscoveryContainer() || r.needsNumaDraClaim() || !resources.ExpectsExclusiveCPUs(r.pod) {
		return
	}
	// Without node affinity, nodeInfo describes a node picked by labels, not necessarily where the pod lands.
	var kubeletPolicy string
	if r.container.GetNodeAffinity() != "" {
		kubeletPolicy = nodeInfo.NodeCPUManagerPolicy
	}
	reason, msg := sharedCPUsReason(r.pod, kubeletPolicy)
	if reason == "" {
		return
	}
	instrumentation.CurrentSpanLogger(ctx).Info("Pod gets no exclusive CPUs", "reason", reason, "message", msg)
	_ = r.RecordEvent(v1.EventTypeWarning, reason, consts.ActionMonitorContainerHealth, msg) //nolint:errcheck // best-effort event
}

// sharedCPUsReason returns an empty reason when the pod gets exclusive CPUs or the kubelet policy is unknown.
func sharedCPUsReason(pod *v1.Pod, kubeletPolicy string) (reason, msg string) {
	switch {
	case pod.Status.QOSClass != "" && pod.Status.QOSClass != v1.PodQOSGuaranteed:
		return "PodNotGuaranteed", fmt.Sprintf("pod %s has QoS class %s: every container, init containers included, needs cpu and memory limits with requests equal to them to get exclusive CPUs", pod.Name, pod.Status.QOSClass)
	case kubeletPolicy != "" && kubeletPolicy != "static":
		return "NoStaticCPUManager", fmt.Sprintf("kubelet on node %s has cpuManagerPolicy=%s, not static: weka containers on this node can pin the same cores", pod.Spec.NodeName, kubeletPolicy)
	}
	return "", ""
}
