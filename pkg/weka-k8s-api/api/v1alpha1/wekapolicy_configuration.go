package v1alpha1

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// ConfigurationPayload defines the parameters for the configuration policy: operator-wide settings
type ConfigurationPayload struct {
	// Csi configures the embedded CSI deployment.
	Csi *CsiSpec `json:"csi,omitempty"`
	// Drivers configures the drivers build and distribution.
	Drivers *DriversSpec `json:"drivers,omitempty"`
	// Scheduling configures scheduling behavior of operator-managed pods.
	Scheduling *SchedulingSpec `json:"scheduling,omitempty"`
	// PodRotation sets the operator-wide default for replacing pods whose pod-affecting spec changed.
	PodRotation *PodRotationSpec `json:"podRotation,omitempty"`
	// NodeAgent configures the node-agent DaemonSet the operator deploys on every node.
	NodeAgent *NodeAgentSpec `json:"nodeAgent,omitempty"`
}

// PodRotationSpec groups settings of config-change pod rotation
type PodRotationSpec struct {
	// Enabled makes WekaClusters without spec.overrides.podRotation replace outdated pods one at a time. False by default.
	Enabled *bool `json:"enabled,omitempty"`
}

// CsiSpec groups settings of the embedded CSI deployment
type CsiSpec struct {
	// MetricsEnabled controls the Prometheus metrics endpoints of the CSI controller, the CSI node
	// plugin and the controller sidecars. Enabled by default.
	MetricsEnabled *bool `json:"metricsEnabled,omitempty"`
	// FsGroupPolicy sets fsGroupPolicy on the CSIDriver object, which decides whether Kubernetes
	// reapplies a pod's fsGroup to the volume. Default value is File.
	// +kubebuilder:validation:Enum=File;None;ReadWriteOnceWithFSType
	FsGroupPolicy *string `json:"fsGroupPolicy,omitempty"`
	// ManageNfsPermissions applies to WekaClients with useNfs only. When true, the CSI plugin creates
	// and removes NFS client groups, rules and permissions (exports) on the WEKA cluster itself.
	// When false (default), the WEKA cluster admin must configure them.
	ManageNfsPermissions *bool `json:"manageNfsPermissions,omitempty"`
}

// DriversSpec groups settings of the drivers build and distribution
type DriversSpec struct {
	// ForceBuilderCli makes the drivers-builder init containers take the weka CLI from the builder
	// image instead of the cluster image. False by default.
	ForceBuilderCli *bool `json:"forceBuilderCli,omitempty"`
}

// SchedulingSpec groups settings that control scheduling behavior of operator-managed pods
type SchedulingSpec struct {
	// TolerateCordon makes client containers and the node-local pods they depend on (drivers-loader,
	// drivers-dist, discovery, adhoc-op) tolerate the node.kubernetes.io/unschedulable:NoSchedule
	// taint, so they keep running and keep being created on a cordoned node. Backends never tolerate
	// it. False by default.
	TolerateCordon *bool `json:"tolerateCordon,omitempty"`
}

// NodeAgentSpec groups settings of the node-agent DaemonSet
// +kubebuilder:validation:XValidation:rule="!has(self.devicePlugin) || !has(self.devicePlugin.enabled) || !self.devicePlugin.enabled || !has(self.updateStrategy) || !has(self.updateStrategy.rollingUpdate) || !has(self.updateStrategy.rollingUpdate.maxSurge) || string(self.updateStrategy.rollingUpdate.maxSurge) in ['0', '0%']",message="updateStrategy.rollingUpdate.maxSurge must be 0 when devicePlugin.enabled is true: two node-agents on one node would fight over the device plugin sockets"
type NodeAgentSpec struct {
	// UpdateStrategy of the DaemonSet. Default is RollingUpdate with maxUnavailable 100% and maxSurge 0,
	// which restarts all node-agent pods at once.
	UpdateStrategy *appsv1.DaemonSetUpdateStrategy `json:"updateStrategy,omitempty"`
	// Resources of the node-agent container. Replaces the defaults as a whole.
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
	// Tolerations of the node-agent pods. Default tolerates every taint.
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
	// NodeSelector of the node-agent pods.
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// Affinity of the node-agent pods. Default requires a linux node with amd64 or arm64 architecture.
	Affinity *corev1.Affinity `json:"affinity,omitempty"`
	// PersistencePath is the host path used for node-agent persistence. Default is /opt/k8s-weka; use
	// /root/k8s-weka on OpenShift and /mnt/stateful_partition/k8s-weka on Google Container-Optimized OS.
	PersistencePath *string `json:"persistencePath,omitempty"`
	// KubeletPath is the kubelet directory of the nodes, used to locate the kubelet device-plugins
	// directory. Default is /var/lib/kubelet.
	KubeletPath *string `json:"kubeletPath,omitempty"`
	// DevicePlugin configures the NUMA region device plugin.
	DevicePlugin *NodeAgentDevicePluginSpec `json:"devicePlugin,omitempty"`
}

// NodeAgentDevicePluginSpec groups settings of the NUMA region device plugin
type NodeAgentDevicePluginSpec struct {
	// Enabled advertises each node's NUMA regions as weka.io/numa-region-<N> extended resources through a
	// kubelet device plugin. Disabled by default.
	Enabled *bool `json:"enabled,omitempty"`
}
