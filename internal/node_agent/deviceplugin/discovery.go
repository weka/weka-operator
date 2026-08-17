// Package deviceplugin implements a Kubernetes device plugin (kubelet
// pkg/apis/deviceplugin/v1beta1) that advertises each NUMA region present on a node as an
// extended resource (weka.io/numa-region-<N>), so pods can request pinning to a specific
// region via resource requests/limits.
package deviceplugin

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"

	"github.com/pkg/errors"
)

// DefaultNumaNodeDir is the standard sysfs location listing one directory per NUMA node
// (node0, node1, ...) on Linux.
const DefaultNumaNodeDir = "/sys/devices/system/node"

var numaNodeDirPattern = regexp.MustCompile(`^node(\d+)$`)

// DiscoverNumaRegions enumerates NUMA region indexes by listing node<N> directories under
// numaNodeDir (e.g. /sys/devices/system/node). Non-matching entries (other directories,
// regular files, directories not matching "node<digits>") are ignored. A missing
// numaNodeDir is not an error: it yields no regions, since not every node exposes a NUMA
// topology. The returned indexes are sorted ascending.
func DiscoverNumaRegions(numaNodeDir string) ([]int, error) {
	entries, err := os.ReadDir(numaNodeDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, errors.Wrapf(err, "failed to read numa node directory %s", numaNodeDir)
	}

	var regions []int
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		match := numaNodeDirPattern.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		region, err := strconv.Atoi(match[1])
		if err != nil {
			// Unreachable given the regexp only matches digits, but handled explicitly
			// rather than swallowed.
			return nil, errors.Wrapf(err, "failed to parse numa region index from %s", entry.Name())
		}
		regions = append(regions, region)
	}

	sort.Ints(regions)
	return regions, nil
}

// numaNodeDirFromSysfsRoot joins a sysfs root (e.g. "/sys") with the standard NUMA node
// subpath, for callers that configure a sysfs root rather than the node directory directly.
func numaNodeDirFromSysfsRoot(sysfsRoot string) string {
	return filepath.Join(sysfsRoot, "devices", "system", "node")
}
