package domain

import (
	"encoding/json"
	"fmt"
)

// FeatureFlags represents the feature flags for a weka image
// These flags are parsed from the weka image's .spec file and written to
// /opt/weka/k8s-runtime/feature_flags.json by weka_runtime.py
type FeatureFlags struct {
	TracesOverridePartialSupport      bool `json:"traces_override_partial_support"`        // 0
	TracesOverrideInSlashTraces       bool `json:"traces_override_in_slash_traces"`        // 1
	SupportsBindingToNotAllInterfaces bool `json:"supports_binding_to_not_all_interfaces"` // 2
	AgentValidate60PortsPerContainer  bool `json:"agent_validate_60_ports_per_container"`  // 3
	AllowPerContainerDriverInterfaces bool `json:"allow_per_container_driver_interfaces"`  // 4
	WekaGetCopyLocalDriverFiles       bool `json:"weka_get_copy_local_driver_files"`       // 5
	DriverSupportsAutoDrain           bool `json:"driver_supports_auto_drain"`             // 6
	SsdProxyIommuSupport              bool `json:"ssd_proxy_iommu_support"`                // 7
	// flag 8 is not used by the operator
	SsdProxyIncludesDpdkMemory bool `json:"ssd_proxy_includes_dpdk_memory"` // 9
	// flags 10, 11 are not used by the operator
	WekaManagesNonIonodeAffinity bool `json:"weka_manages_non_ionode_affinity"` // 12
	// flag 13 is not used by the operator
	// WekactlAsDefault means the go CLI (wekactl) backs the `weka` command. It changes some JSON
	// schemas, notably `weka nfs interface-group` port identity — see NfsInterfaceGroupPort.
	WekactlAsDefault bool `json:"wekactl_as_default"` // 14
	// flags 15, 16 are not used by the operator
	// SsdProxyVidLabelAdd means ssd_proxy_add_virtual_drive takes a label and returns the VID it derives from it.
	SsdProxyVidLabelAdd bool `json:"ssd_proxy_vid_label_add"` // 17
}

// ParseFeatureFlags parses feature_flags.json and rejects a file that lacks a flag the operator
// knows: a pod started by an older operator runs an older runtime, whose file would read the new
// flag as false.
func ParseFeatureFlags(data []byte) (*FeatureFlags, error) {
	var flags FeatureFlags
	if err := json.Unmarshal(data, &flags); err != nil {
		return nil, err
	}
	var present, known map[string]json.RawMessage
	if err := json.Unmarshal(data, &present); err != nil {
		return nil, err
	}
	knownJSON, _ := json.Marshal(FeatureFlags{}) //nolint:errcheck // marshal of a plain bool struct cannot fail
	_ = json.Unmarshal(knownJSON, &known)        //nolint:errcheck // round-trip of the line above
	for name := range known {
		if _, ok := present[name]; !ok {
			return nil, fmt.Errorf("feature flag %s missing, file was written by an older runtime", name)
		}
	}
	return &flags, nil
}
