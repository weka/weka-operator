package domain

// FeatureFlags represents the feature flags for a weka image
// These flags are parsed from the weka image's .spec file and written to
// /opt/weka/k8s-runtime/feature_flags.json by weka_runtime.py
type FeatureFlags struct {
	TracesOverridePartialSupport      bool `json:"traces_override_partial_support"`        // 0
	TracesOverrideInSlashTraces       bool `json:"traces_override_in_slash_traces"`        // 1
	SupportsBindingToNotAllInterfaces bool `json:"supports_binding_to_not_all_interfaces"` // 2
	AgentValidate60PortsPerContainer  bool `json:"agent_validate_60_ports_per_container"`  // 3
	AllowPerContainerDriverInterfaces bool `json:"allow_per_container_driver_interface"`   // 4
	WekaGetCopyLocalDriverFiles       bool `json:"weka_get_copy_local_driver_files"`       // 5
	DriverSupportsAutoDrain           bool `json:"driver_supports_auto_drain"`             // 6
	SsdProxyIommuSupport              bool `json:"ssd_proxy_iommu_support"`                // 7
}
