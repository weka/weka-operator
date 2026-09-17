package v1alpha1

// ConfigurationPayload defines the parameters for the configuration policy: operator-wide settings
type ConfigurationPayload struct {
	// Csi configures the embedded CSI deployment.
	Csi *CsiSpec `json:"csi,omitempty"`
	// Drivers configures the drivers build and distribution.
	Drivers *DriversSpec `json:"drivers,omitempty"`
}

// CsiSpec groups settings of the embedded CSI deployment
type CsiSpec struct {
	// MetricsEnabled controls the Prometheus metrics endpoints of the CSI controller, the CSI node
	// plugin and the controller sidecars. Enabled by default.
	MetricsEnabled *bool `json:"metricsEnabled,omitempty"`
	// SkipGarbageCollection stops the CSI controller from reclaiming deleted volume directories in
	// the background. False by default.
	SkipGarbageCollection *bool `json:"skipGarbageCollection,omitempty"`
	// FsGroupPolicy sets fsGroupPolicy on the CSIDriver object, which decides whether Kubernetes
	// reapplies a pod's fsGroup to the volume. Default value is File.
	// +kubebuilder:validation:Enum=File;None;ReadWriteOnceWithFSType
	FsGroupPolicy *string `json:"fsGroupPolicy,omitempty"`
}

// DriversSpec groups settings of the drivers build and distribution
type DriversSpec struct {
	// ForceBuilderCli makes the drivers-builder init containers take the weka CLI from the builder
	// image instead of the cluster image. False by default.
	ForceBuilderCli *bool `json:"forceBuilderCli,omitempty"`
}
