package weka

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/runtime/paths"
)

// CheckIOMMUCompatible checks that ssdproxy can run given the host's IOMMU state. An absent or
// empty iommu_groups directory always passes: it means IOMMU is off, which ssdproxy supports
// regardless of the feature flag. IOMMU groups being present requires SsdProxyIommuSupport.
// Mirrors Python assert_ssdproxy_iommu_supported()/has_iommu_groups() at weka_runtime.py:4320.
func CheckIOMMUCompatible(roots *paths.Roots, features domain.FeatureFlags) error {
	if !HasIOMMUGroups(roots.Sys) {
		return nil
	}
	if !features.SsdProxyIommuSupport {
		return fmt.Errorf("SSD proxy mode is not supported on IOMMU-enabled systems with this Weka version; please upgrade to a version that supports IOMMU for SSD proxy")
	}
	return nil
}

// HasIOMMUGroups reports whether sysRoot/kernel/iommu_groups exists and is non-empty. A missing
// or unreadable directory reports false. Mirrors Python has_iommu_groups() at
// weka_runtime.py:3855.
func HasIOMMUGroups(sysRoot string) bool {
	entries, err := os.ReadDir(filepath.Join(sysRoot, "kernel", "iommu_groups"))
	return err == nil && len(entries) > 0
}
