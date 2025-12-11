package domain

// FeatureFlags represents the feature flags for a weka image
// These flags are parsed from the weka image's .spec file and written to
// /opt/weka/k8s-runtime/feature_flags.json by weka_runtime.py
type FeatureFlags struct {
	TracesOverridePartialSupport      bool `json:"traces_override_partial_support"`
	TracesOverrideInSlashTraces       bool `json:"traces_override_in_slash_traces"`
	SupportsBindingToNotAllInterfaces bool `json:"supports_binding_to_not_all_interfaces"`
	AgentValidate60PortsPerContainer  bool `json:"agent_validate_60_ports_per_container"`
	AllowPerContainerDriverInterfaces bool `json:"allow_per_container_driver_interface"`
}
