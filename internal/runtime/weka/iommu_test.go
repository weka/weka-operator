package weka

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/weka/weka-operator/internal/pkg/domain"
	"github.com/weka/weka-operator/internal/runtime/paths"
)

func TestCheckIOMMUCompatible(t *testing.T) {
	mkRoots := func(t *testing.T, withGroups bool, entries []string) paths.Roots {
		dir := t.TempDir()
		if withGroups {
			groupsDir := filepath.Join(dir, "kernel", "iommu_groups")
			if err := os.MkdirAll(groupsDir, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				if err := os.WriteFile(filepath.Join(groupsDir, e), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
		return paths.Roots{Sys: dir}
	}

	t.Run("absent directory is always OK", func(t *testing.T) {
		roots := mkRoots(t, false, nil)
		if err := CheckIOMMUCompatible(&roots, domain.FeatureFlags{SsdProxyIommuSupport: false}); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("empty directory is always OK", func(t *testing.T) {
		roots := mkRoots(t, true, nil)
		if err := CheckIOMMUCompatible(&roots, domain.FeatureFlags{SsdProxyIommuSupport: false}); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("groups present requires the feature flag", func(t *testing.T) {
		roots := mkRoots(t, true, []string{"0"})
		if err := CheckIOMMUCompatible(&roots, domain.FeatureFlags{SsdProxyIommuSupport: false}); err == nil {
			t.Error("expected error when IOMMU groups present and feature flag disabled")
		}
		if err := CheckIOMMUCompatible(&roots, domain.FeatureFlags{SsdProxyIommuSupport: true}); err != nil {
			t.Errorf("unexpected error when feature flag enabled: %v", err)
		}
	})
}
