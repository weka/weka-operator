// Package drivers provides helpers for building and loading Weka kernel drivers.
package drivers

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/weka/weka-operator/internal/pkg/osinfo"
	"github.com/weka/weka-operator/internal/runtime/process"
)

const ubuntu24BuildID = "ubuntu24.04"

// GetWekaVersion returns the version string from the release spec directory.
// Scans <optWekaRoot>/dist/release and /shared-weka-version/opt-weka/dist/release;
// expects exactly one .spec file in whichever directory is found first.
func GetWekaVersion(optWekaRoot string) (string, error) {
	dirs := []string{
		optWekaRoot + "/dist/release",
		"/shared-weka-version/opt-weka/dist/release",
	}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) == 0 {
			continue
		}
		if len(entries) != 1 {
			return "", fmt.Errorf("expected one release spec in %s, found %d", dir, len(entries))
		}
		name := entries[0].Name()
		version := strings.TrimSuffix(name, ".spec")
		version = strings.SplitN(version, ".spec", 2)[0]
		return version, nil
	}
	return "", fmt.Errorf("no release files found in any of: %v", dirs)
}

// KernelBuildID returns the kernel build ID to pass to weka driver commands.
// An empty string means the caller should omit the --kernel-build-id flag (weka uses uname -r).
func KernelBuildID(driversBuildID, distService string) (string, error) {
	if driversBuildID != "" && driversBuildID != "auto" {
		return driversBuildID, nil
	}

	nodeInfo, err := osinfo.Load()
	if err != nil {
		return "", fmt.Errorf("KernelBuildID: load osinfo: %w", err)
	}

	switch {
	case nodeInfo.IsCos():
		if nodeInfo.OsBuildId == "" {
			return "", fmt.Errorf("OS_BUILD_ID is required for Google COS driver builds")
		}
		return nodeInfo.OsBuildId, nil

	case isUbuntu24(nodeInfo):
		if distService != "" {
			return ubuntu24BuildID, nil
		}
		// No dist service: weka will use uname -r internally.
		return "", nil

	default:
		// RHCOS and others use the OS build ID.
		return nodeInfo.OsBuildId, nil
	}
}

// BuilderKernelBuildID returns the kernel build ID for the drivers-builder mode: "ubuntu24.04"
// on Ubuntu 24, otherwise "" (weka falls back to uname -r). Unlike KernelBuildID, the builder
// has no DRIVERS_BUILD_ID override and no COS/OS_BUILD_ID or dist-service branch — it mirrors
// the narrower logic in Python's builder mode at weka_runtime.py:4505-4509.
func BuilderKernelBuildID(nodeInfo *osinfo.NodeInfo) string {
	if isUbuntu24(nodeInfo) {
		return ubuntu24BuildID
	}
	return ""
}

// Build packs weka drivers for version into optWeka/dist and returns the kernel signature.
// kernelBuildID may be empty, in which case --kernel-build-id is omitted from the pack command.
func Build(ctx context.Context, runner process.CommandRunner, optWeka, version, kernelBuildID string) (string, error) {
	versionGetCmd := fmt.Sprintf(
		"weka version get --driver-only --without-agent --no-progress-bar --from file://shared-weka-version/opt-weka %s",
		version,
	)
	if _, err := runner.Run(ctx, process.Shell(versionGetCmd)); err != nil {
		return "", fmt.Errorf("weka version get: %w", err)
	}

	packCmd := fmt.Sprintf("weka driver pack --without-agent --version %s", version)
	if kernelBuildID != "" {
		packCmd += " --kernel-build-id " + kernelBuildID
	}
	if _, err := runner.Run(ctx, process.Shell(packCmd)); err != nil {
		return "", fmt.Errorf("weka driver pack: %w", err)
	}

	distDir := optWeka + "/dist"
	if err := os.MkdirAll(distDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", distDir, err)
	}
	// v1 symlink makes GET /dist/v1/drivers/... resolve to <distDir>/drivers/...
	_ = os.Remove(distDir + "/v1") //nolint:errcheck // best-effort: absent is fine, Symlink below handles IsExist
	if err := os.Symlink(distDir, distDir+"/v1"); err != nil && !os.IsExist(err) {
		return "", fmt.Errorf("symlink v1: %w", err)
	}

	return KernelSignature(distDir + "/drivers")
}

// KernelSignature scans driversDir for a file matching
// weka-driver-<hash>-<sig>.zip and returns the kernel signature hex string.
func KernelSignature(driversDir string) (string, error) {
	entries, err := os.ReadDir(driversDir)
	if err != nil {
		return "", fmt.Errorf("KernelSignature: read dir %s: %w", driversDir, err)
	}
	re := regexp.MustCompile(`^weka-driver-[a-f0-9]+-([a-f0-9]+)\.zip$`)
	for _, e := range entries {
		if m := re.FindStringSubmatch(e.Name()); m != nil {
			return m[1], nil
		}
	}
	return "", fmt.Errorf("no weka-driver zip found in %s", driversDir)
}

// WekaDriversHandling returns true when the Weka version uses new driver handling.
// Delegates to ResolveVersionParams(imageName).WekaDriversHandling, which faithfully
// mirrors the VERSION_TO_DRIVERS_MAP_WEKAFS / DEFAULT_PARAMS lookup in
// weka_runtime.py:1339-1351. The old "4.2." string heuristic was incorrect for
// all explicit map entries (they all set weka_drivers_handling=False regardless of prefix).
func WekaDriversHandling(imageName string) bool {
	return ResolveVersionParams(imageName).WekaDriversHandling
}

func isUbuntu24(nodeInfo *osinfo.NodeInfo) bool {
	if !nodeInfo.IsUbuntu() {
		return false
	}
	parts := strings.SplitN(nodeInfo.OsBuildId, ".", 2)
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	return major >= 24
}
