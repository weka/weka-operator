package weka

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/weka/weka-operator/internal/pkg/domain"
)

// ReleaseSpec is the decoded contents of a Weka release *.spec file.
type ReleaseSpec struct {
	Version      string
	FeatureFlags domain.FeatureFlags
}

// featureBits maps each known feature-bitmap bit index to the FeatureFlags field it sets.
var featureBits = []struct {
	bit int
	set func(*domain.FeatureFlags, bool)
}{
	{0, func(f *domain.FeatureFlags, v bool) { f.TracesOverridePartialSupport = v }},
	{1, func(f *domain.FeatureFlags, v bool) { f.TracesOverrideInSlashTraces = v }},
	{2, func(f *domain.FeatureFlags, v bool) { f.SupportsBindingToNotAllInterfaces = v }},
	{3, func(f *domain.FeatureFlags, v bool) { f.AgentValidate60PortsPerContainer = v }},
	{4, func(f *domain.FeatureFlags, v bool) { f.AllowPerContainerDriverInterfaces = v }},
	{5, func(f *domain.FeatureFlags, v bool) { f.WekaGetCopyLocalDriverFiles = v }},
	{6, func(f *domain.FeatureFlags, v bool) { f.DriverSupportsAutoDrain = v }},
	{7, func(f *domain.FeatureFlags, v bool) { f.SsdProxyIommuSupport = v }},
	{9, func(f *domain.FeatureFlags, v bool) { f.SsdProxyIncludesDpdkMemory = v }},
	{12, func(f *domain.FeatureFlags, v bool) { f.WekaManagesNonIonodeAffinity = v }},
	{14, func(f *domain.FeatureFlags, v bool) { f.WekactlAsDefault = v }},
}

// featureFlagsFromBitmap decodes a base64 feature bitmap into FeatureFlags.
// Bit ordering matches Python's parse_feature_bitmap: byte i, bit j (LSB first) is
// overall bit index i*8+j.
func featureFlagsFromBitmap(b64 string) (domain.FeatureFlags, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return domain.FeatureFlags{}, fmt.Errorf("decode feature bitmap: %w", err)
	}
	var flags domain.FeatureFlags
	for _, fb := range featureBits {
		byteIdx, bitIdx := fb.bit/8, fb.bit%8
		if byteIdx >= len(raw) {
			continue
		}
		fb.set(&flags, raw[byteIdx]&(1<<bitIdx) != 0)
	}
	return flags, nil
}

// ReadReleaseSpec reads the single *.spec file in dir and decodes its feature bitmap.
// Unlike config.loadFeatureFlags, this requires exactly one *.spec file and propagates
// JSON/bitmap decode errors instead of swallowing them.
func ReadReleaseSpec(dir string) (ReleaseSpec, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.spec"))
	if err != nil {
		return ReleaseSpec{}, fmt.Errorf("read release spec: %w", err)
	}
	if len(matches) != 1 {
		return ReleaseSpec{}, fmt.Errorf("read release spec: expected exactly one *.spec file in %s, found %d", dir, len(matches))
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		return ReleaseSpec{}, fmt.Errorf("read release spec: %w", err)
	}
	var spec struct {
		Version      string `json:"version"`
		FeatureFlags string `json:"feature_flags"`
	}
	if err = json.Unmarshal(data, &spec); err != nil {
		return ReleaseSpec{}, fmt.Errorf("read release spec: %w", err)
	}
	if spec.FeatureFlags == "" {
		return ReleaseSpec{Version: spec.Version}, nil
	}
	flags, err := featureFlagsFromBitmap(spec.FeatureFlags)
	if err != nil {
		return ReleaseSpec{}, fmt.Errorf("read release spec: %w", err)
	}
	return ReleaseSpec{Version: spec.Version, FeatureFlags: flags}, nil
}
