package domain

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// FeatureFlags represents the feature flags for a weka image. They come from the feature bitmap in
// the image's release .spec file, so they are a property of the image alone: reading them does not
// depend on which weka_runtime.py a pod happened to start with. The trailing index on each field is
// its bit in that bitmap, assigned by weka-core and mirrored in weka_runtime.py's FeaturesFlags.
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

// ParseReleaseSpec reads the feature bitmap out of a weka image's release .spec file. A spec with
// no feature_flags yields no flags, which is what an image predating the bitmap means.
func ParseReleaseSpec(data []byte) (*FeatureFlags, error) {
	var spec struct {
		FeatureFlags string `json:"feature_flags"`
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("parsing release spec: %w", err)
	}
	return parseFeatureBitmap(spec.FeatureFlags)
}

// parseFeatureBitmap decodes the base64 feature bitmap. It is the inverse of weka_runtime.py's
// get_feature_bitmap() and must keep its bit order: bit i lives in byte i/8 at 1<<(i%8).
func parseFeatureBitmap(b64 string) (*FeatureFlags, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("decoding feature bitmap %q: %w", b64, err)
	}
	// A bit past the end of the bitmap is false: an image too old to carry it lacks the feature.
	bit := func(i int) bool {
		b := i / 8
		return b < len(raw) && raw[b]&(1<<(i%8)) != 0
	}
	return &FeatureFlags{
		TracesOverridePartialSupport:      bit(0),
		TracesOverrideInSlashTraces:       bit(1),
		SupportsBindingToNotAllInterfaces: bit(2),
		AgentValidate60PortsPerContainer:  bit(3),
		AllowPerContainerDriverInterfaces: bit(4),
		WekaGetCopyLocalDriverFiles:       bit(5),
		DriverSupportsAutoDrain:           bit(6),
		SsdProxyIommuSupport:              bit(7),
		SsdProxyIncludesDpdkMemory:        bit(9),
		WekaManagesNonIonodeAffinity:      bit(12),
		WekactlAsDefault:                  bit(14),
		SsdProxyVidLabelAdd:               bit(17),
	}, nil
}
